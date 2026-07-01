package app

import (
	"context"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"futures/internal/domain"
	"futures/internal/exchange"
	"futures/internal/intent"
	"futures/internal/scanner"
	"futures/internal/scheduler"
)

func (a *App) setSymbolCooldown(ctx context.Context, symbol string, decision string) {
	cooldown := symbolCooldownDurationLLM
	if decision != "SKIP" {
		cooldown += 10 * time.Minute
	}
	if err := a.cache.SetSymbolCooldown(ctx, symbol, cooldown); err != nil {
		slog.Warn("failed to set symbol cooldown", "symbol", symbol, "error", err)
	}
}

func (a *App) filterCooldownSymbols(ctx context.Context, candidates []domain.Candidate) []domain.Candidate {
	out := candidates[:0]
	for _, c := range candidates {
		on, err := a.cache.IsSymbolOnCooldown(ctx, c.Symbol)
		if err != nil {
			slog.Warn("cooldown check failed, allowing symbol", "symbol", c.Symbol, "error", err)
			out = append(out, c)
			continue
		}
		if on {
			slog.Info("candidate excluded: symbol cooldown", "symbol", c.Symbol)
			continue
		}
		out = append(out, c)
	}
	return out
}

func (a *App) filterOccupiedSymbols(ctx context.Context, candidates []domain.Candidate) []domain.Candidate {
	activeSymbols, err := a.riskEngine.ActiveSymbols(ctx)
	if err != nil {
		slog.Warn("could not fetch active symbols, skipping occupied-symbol filter", "error", err)
		return candidates
	}

	out := candidates[:0]
	for _, c := range candidates {
		if activeSymbols[c.Symbol] {
			slog.Info("candidate excluded: active position", "symbol", c.Symbol)
			continue
		}
		//if a.intentQueue.HasPendingSymbol(c.Symbol) {
		//	slog.Info("candidate excluded: pending intent", "symbol", c.Symbol)
		//	continue
		//}
		out = append(out, c)
	}
	return out
}

// filterThinOrderBook excludes candidates whose order book cannot absorb a
// SL-hunting sweep cheaply. Two-stage to minimise API calls:
//
//  1. Spread pre-filter (bookTickerCache, zero cost) — wide spread = thin book, skip immediately.
//  2. Ask liquidity check (REST /fapi/v1/depth, only for spread-passing symbols) — sum USDT value
//     of all ask levels between entry and SL price; exclude if below DepthRatioN × notional.
//
// position_notional = min(balance × sizePct/100, 500) × leverage
// SL price = entry × (1 + SlPct/100). Entry price is taken from candidate.MarkPrice.
// Balance is read from the Redis cache (2-min TTL); if unavailable the filter is skipped.
func (a *App) filterThinOrderBook(ctx context.Context, candidates []domain.Candidate) []domain.Candidate {
	cfg := a.cfg.OrderBookFilter
	if !cfg.Enabled {
		return candidates
	}
	if a.bookTickerCache == nil {
		return candidates
	}

	balance, err := a.cache.GetCachedBalance(ctx)
	if err != nil {
		slog.Warn("order book filter: balance cache error, skipping filter", "error", err)
		return candidates
	}
	if balance == nil {
		balance, err = a.executor.GetAccountBalance(ctx)
		if err != nil {
			slog.Warn("order book filter: could not fetch balance, skipping filter", "error", err)
			return candidates
		}
		if setErr := a.cache.SetCachedBalance(ctx, balance); setErr != nil {
			slog.Warn("order book filter: failed to cache balance", "error", setErr)
		}
	}

	sizePct := a.cfg.Trading.PositionSizePct
	margin := math.Min(balance.TotalBalance*sizePct/100, 500)
	notional := margin * float64(a.cfg.Trading.Leverage)
	minAskLiquidity := cfg.DepthRatioN * notional
	slPct := a.cfg.Execution.SlPct

	out := candidates[:0]
	for _, c := range candidates {
		// Stage 1: spread pre-filter from WS cache — free.
		spreadBps, spreadOk := a.bookTickerCache.SpreadBps(c.Symbol)
		if !spreadOk {
			slog.Info("candidate excluded: no book ticker data", "symbol", c.Symbol)
			continue
		}
		if spreadBps > cfg.MaxSpreadBps {
			slog.Info("candidate excluded: spread too wide",
				"symbol", c.Symbol,
				"spread_bps", spreadBps,
				"max_spread_bps", cfg.MaxSpreadBps,
			)
			continue
		}

		// Stage 2: ask liquidity between entry and SL price via REST depth.
		slPrice := c.MarkPrice * (1 + slPct/100)
		liq, err := askLiquidityToSL(ctx, a.binanceClient, c.Symbol, slPrice, cfg.DepthLevels)
		if err != nil {
			slog.Warn("candidate excluded: depth fetch failed",
				"symbol", c.Symbol, "error", err)
			continue
		}
		if liq < minAskLiquidity {
			slog.Info("candidate excluded: ask liquidity too thin",
				"symbol", c.Symbol,
				"ask_liquidity_usdt", liq,
				"min_ask_liquidity_usdt", minAskLiquidity,
				"notional_usdt", notional,
				"depth_ratio_n", cfg.DepthRatioN,
			)
			continue
		}

		slog.Info("candidate passed order book filter",
			"symbol", c.Symbol,
			"spread_bps", spreadBps,
			"ask_liquidity_usdt", liq,
			"min_ask_liquidity_usdt", minAskLiquidity,
		)
		out = append(out, c)
	}
	return out
}

// askLiquidityToSL sums the USDT value of all ask levels up to slPrice.
func askLiquidityToSL(ctx context.Context, client *exchange.BinanceClient, symbol string, slPrice float64, levels int) (float64, error) {
	depth, err := client.GetDepth(ctx, symbol, levels)
	if err != nil {
		return 0, err
	}
	var total float64
	for _, level := range depth.Asks {
		if level.Price > slPrice {
			break
		}
		total += level.Price * level.Qty
	}
	return total, nil
}

func (a *App) scanFn(ctx context.Context, window scheduler.WindowType) error {
	scanStart := time.Now()
	defer func() {
		slog.Info("scan completed", "window", window, "latency_ms", float64(time.Since(scanStart).Microseconds())/1000.0)
	}()

	// Reset LLM call state at the start of each new funding cycle so the
	// deduplication check does not suppress calls across settlement boundaries.
	currentDeadline := scheduler.NextFundingTime(time.Now().UTC())
	if window == scheduler.WindowFrontrun && a.lastLLMCall != nil && !a.lastLLMCall.cycleDeadline.Equal(currentDeadline) {
		a.lastLLMCall = nil
	}

	// In the AFTER window, skip a fresh scan if the intent queue already has an
	// AFTER-mode intent queued from the pre-settlement evaluation — the queue tick
	// fires it without needing another LLM call.
	if window == scheduler.WindowAfter && a.intentQueue.HasAfterIntent() {
		slog.Info("after window: intent already queued, skipping scan")
		return nil
	}

	if err := a.riskEngine.PreCheck(ctx); err != nil {
		slog.Info("pre-check rejected", "reason", err)
		return nil
	}

	candidates, err := a.scanner.Scan(ctx)
	if err != nil {
		return err
	}
	if len(candidates) == 0 {
		slog.Info("no candidates after funding filter", "window", window)
		return nil
	}

	// Ensure @bookTicker streams are subscribed for all current candidates so the
	// cache is warm for filterThinOrderBook. WSConnection deduplicates subscriptions
	// so re-subscribing already-active symbols is a no-op.
	if a.wsKlines != nil {
		streams := make([]string, len(candidates))
		for i, c := range candidates {
			streams[i] = strings.ToLower(c.Symbol) + "@bookTicker"
		}
		if err := a.wsKlines.Subscribe(ctx, streams); err != nil {
			slog.Warn("failed to subscribe bookTicker streams", "error", err)
		}
	}

	//candidates = scanner.FilterByROI(candidates, a.filteringMinROI())
	//if len(candidates) == 0 {
	//	slog.Info("no candidates after ROI filter", "window", window)
	//	return nil
	//}

	// COMMENTED HIGH ROI AND MIN FUNDING RATE FILTER TO MONITOR FIRST
	//filtered := candidates[:0]
	//for _, c := range candidates {
	//	if c.DailyROI > 60 && c.FundingRate > -0.01 {
	//		slog.Info("candidate excluded: high ROI requires funding <= -1%",
	//			"symbol", c.Symbol, "daily_roi_pct", c.DailyROI, "funding_rate_pct", c.FundingRate*100)
	//		continue
	//	}
	//	filtered = append(filtered, c)
	//}
	//candidates = filtered
	//if len(candidates) == 0 {
	//	slog.Info("no candidates after high-ROI funding filter", "window", window)
	//	return nil
	//}

	candidates = scanner.FilterByVolume(candidates, a.filteringMinVolume())
	if len(candidates) == 0 {
		slog.Info("no candidates after volume filter", "window", window)
		return nil
	}

	candidates = a.filterThinOrderBook(ctx, candidates)
	if len(candidates) == 0 {
		slog.Info("no candidates after order book filter", "window", window)
		return nil
	}

	candidates = a.filterOccupiedSymbols(ctx, candidates)
	if len(candidates) == 0 {
		slog.Info("no candidates after occupied-symbol filter", "window", window)
		return nil
	}

	candidates = a.filterCooldownSymbols(ctx, candidates)
	if len(candidates) == 0 {
		slog.Info("no candidates after LLM cooldown filter", "window", window)
		return nil
	}

	// Skip LLM call during the first 20 minutes of the frontrun window (T-40m to
	// T-20m) to allow candle backfill to finish processing.

	if window == scheduler.WindowFrontrun {
		next := scheduler.NextFundingTime(time.Now().UTC())
		if timeUntilFunding := time.Until(next); timeUntilFunding > 20*time.Minute {
			slog.Info("frontrun early gate: skipping LLM call until T-20m",
				"time_until_funding", timeUntilFunding.Round(time.Second),
			)
			return nil
		}
	}

	btc, err := a.indEngine.ComputeBTCContext(ctx)
	if err != nil {
		slog.Warn("BTC context unavailable, proceeding without it", "error", err)
	}

	type candResult struct {
		sc  *domain.ScoredCandidate
		err error
	}
	results := make([]candResult, len(candidates))
	var wg sync.WaitGroup
	for i, c := range candidates {
		wg.Add(1)
		go func(idx int, cand domain.Candidate) {
			defer wg.Done()
			snap, ierr := a.indEngine.Compute(ctx, cand.Symbol)
			if ierr != nil {
				results[idx] = candResult{err: ierr}
			} else {
				results[idx] = candResult{sc: a.scorer.Score(cand, snap, btc)}
			}
		}(i, c)
	}
	wg.Wait()

	var scored []*domain.ScoredCandidate
	for _, res := range results {
		if res.err == nil && res.sc != nil {
			scored = append(scored, res.sc)
		}
	}
	if len(scored) == 0 {
		slog.Info("no valid scored candidates", "window", window)
		return nil
	}

	sort.Slice(scored, func(i, j int) bool {
		return scored[i].CompositeScore > scored[j].CompositeScore
	})

	for _, sc := range scored {
		slog.Info("scored candidate",
			"symbol", sc.Candidate.Symbol,
			"funding", sc.Candidate.FundingRate,
			"score", sc.CompositeScore,
			"funding_score", sc.Breakdown.FundingScore,
			"oi_score", sc.Breakdown.OIScore,
			"btc_score", sc.Breakdown.BTCScore,
			"candle_score", sc.Breakdown.CandleScore,
			"volume_score", sc.Breakdown.VolumeScore,
			"volatility_score", sc.Breakdown.VolatilityScore,
			"bullish_candle_penalty", sc.Breakdown.BullishCandlePenalty,
			"momentum_penalty", sc.Breakdown.MomentumPenalty,
			"bullish_momentum", sc.Indicators.BullishMomentum,
			"bearish_momentum_weak", sc.Indicators.BearishMomentumWeak,
		)
	}

	// Confluence gate: hard-block candidates with zero reversal evidence.
	// The scoring system already penalises these heavily (score ~15-20), so
	// most are blocked by min_score. This gate catches any that slip through
	// via RSI overbought (RSI7_5m >= 70 counts as reversal evidence even without
	// a detected candle pattern).
	confluenceFiltered := scored[:0]
	for _, sc := range scored {
		hasReversalEvidence := sc.Breakdown.CandleScore > 0 ||
			sc.Breakdown.RSIDivergenceScore > 0 ||
			sc.Indicators.RSI7_5m >= 70 || sc.CompositeScore >= 50
		if !hasReversalEvidence {
			slog.Info("candidate excluded: no reversal evidence",
				"symbol", sc.Candidate.Symbol,
				"candle_score", sc.Breakdown.CandleScore,
				"rsi_div_score", sc.Breakdown.RSIDivergenceScore,
				"rsi7_5m", sc.Indicators.RSI7_5m,
				"composite_score", sc.CompositeScore,
			)
			continue
		}
		confluenceFiltered = append(confluenceFiltered, sc)
	}
	if len(confluenceFiltered) == 0 {
		slog.Info("no candidates after confluence gate", "window", window)
		return nil
	}
	scored = confluenceFiltered

	topN := a.cfg.LLM.TopCandidates
	if topN > len(scored) {
		topN = len(scored)
	}
	top := scored[:topN]

	btcTrend := ""
	if btc != nil {
		btcTrend = btc.Trend
	}
	cooldown := time.Duration(a.cfg.LLM.CallCooldownSecs) * time.Second
	if lc := a.lastLLMCall; lc != nil {
		withinCooldown := time.Since(lc.calledAt) < cooldown
		inputsUnchanged := lc.window == window &&
			math.Abs(lc.score-top[0].CompositeScore) < 0.5 &&
			lc.btcTrend == btcTrend
		if withinCooldown || inputsUnchanged {
			slog.Info("skipping LLM call: inputs unchanged or in cooldown",
				"symbol", top[0].Candidate.Symbol,
				"age", time.Since(lc.calledAt).Round(time.Second),
				"within_cooldown", withinCooldown,
				"inputs_unchanged", inputsUnchanged,
			)
			return nil
		}
	}

	similarTrades := make(map[string][]domain.SimilarTrade)
	modeWinRates := make(map[string]map[string]domain.ModeWinRate)
	if a.memoryEngine != nil && len(top) > 0 {
		now := time.Now().UTC()

		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, sc := range top {
			wg.Add(1)
			go func(sc *domain.ScoredCandidate) {
				defer wg.Done()
				similar, winRates, serr := a.memoryEngine.RetrieveSimilar(ctx,
					sc.Indicators, btc, &sc.Candidate,
					sc.CompositeScore, string(window), now,
				)
				if serr != nil {
					slog.Warn("memory retrieval failed for candidate",
						"symbol", sc.Candidate.Symbol, "error", serr)
					return
				}
				mu.Lock()
				if len(similar) > 0 {
					similarTrades[sc.Candidate.Symbol] = similar
				}
				if len(winRates) > 0 {
					modeWinRates[sc.Candidate.Symbol] = winRates
				}
				mu.Unlock()
			}(sc)
		}
		wg.Wait()
	}

	llmStart := time.Now()
	decision, err := a.llmEngine.Evaluate(ctx, top, btc, a.cfg.Execution.TpPct, similarTrades, modeWinRates)
	if err != nil {
		slog.Error("LLM engine error", "error", err, "latency_ms", time.Since(llmStart).Milliseconds())
		return nil
	}
	slog.Info("llm_evaluate",
		"latency_ms", time.Since(llmStart).Milliseconds(),
		"action", decision.Action,
		"symbol", decision.Symbol,
		"confidence", decision.Confidence,
	)
	a.setSymbolCooldown(ctx, decision.Symbol, decision.Action)
	selectedScore := float64(0)
	for _, t := range top {
		if decision.Symbol == t.Candidate.Symbol {
			selectedScore = t.CompositeScore
		}
	}
	a.lastLLMCall = &llmCallState{
		symbol:        decision.Symbol,
		score:         selectedScore,
		btcTrend:      btcTrend,
		window:        window,
		calledAt:      time.Now(),
		cycleDeadline: currentDeadline,
	}

	if decision.Action == "SKIP" {
		slog.Info("LLM decided to skip",
			"reason", decision.SkipReason,
			"queue_depth", a.intentQueue.PendingCount(),
		)
		// A late-cycle skip is a fresh reassessment — cancel any stale queued intent
		// for the same symbol so it cannot fire in a later window (e.g. AFTER).
		// FRONTRUN skips are excluded: there are still multiple windows remaining in
		// the cycle so the LLM may take a different view at LAST_MINUTE or AFTER.
		if window != scheduler.WindowFrontrun && len(top) > 0 {
			a.intentQueue.CancelBySymbol(top[0].Candidate.Symbol)
		}
		if a.cfg.Memory.EmbedSkips {
			go func() {
				bgCtx := context.Background()
				if err := a.memoryEngine.RecordSkip(bgCtx, top[0], btc, decision.SkipReason, string(window)); err != nil {
					slog.Error("failed to record skip memory", "error", err)
				}
			}()
		}
		return nil
	}

	var selected *domain.ScoredCandidate
	for _, sc := range top {
		if sc.Candidate.Symbol == decision.Symbol {
			selected = sc
			break
		}
	}
	if selected == nil {
		slog.Warn("LLM selected unknown symbol", "symbol", decision.Symbol)
		return nil
	}

	selected.PositionSizePct = confidenceToSize(decision.Confidence, a.cfg.Trading.PositionSizePct)
	if selected.PositionSizePct <= 0 {
		slog.Info("LLM selected confidence is too low, decided to skip", "symbol", selected.Candidate.Symbol, "confidence", decision.Confidence)
		if a.cfg.Memory.EmbedSkips {
			go func() {
				bgCtx := context.Background()
				if err := a.memoryEngine.RecordSkip(bgCtx, selected, btc, "confidence is too low", string(window)); err != nil {
					slog.Error("failed to record skip memory", "error", err)
				}
			}()
		}
		return nil
	}

	nextSettlement := scheduler.NextFundingTime(time.Now().UTC())
	ti := &intent.TradeIntent{
		Symbol:          decision.Symbol,
		Candidate:       selected,
		Decision:        decision,
		TargetEntryMode: decision.EntryMode,
		CreatedAt:       time.Now(),
		ExpiresAt:       nextSettlement.Add(1 * time.Minute),
		Status:          intent.IntentPending,
	}
	a.intentQueue.Enqueue(ti)
	now := time.Now()
	a.intentQueue.Tick(ctx, window, nextSettlement.Sub(now))

	return nil
}

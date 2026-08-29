package app

import (
	"context"
	"log/slog"
	"math"
	"sort"
	"sync"
	"time"

	"futures/internal/domain"
	"futures/internal/exchange"
	"futures/internal/intent"
	"futures/internal/llm"
	"futures/internal/market"
	"futures/internal/scanner"
	"futures/internal/scheduler"
)

const confluenceScoreThreshold = 47.0
const confluenceRSIThreshold = 50.0
const staleReversalRSIThreshold = 35.0

func (a *App) setSymbolCooldown(ctx context.Context, symbol string, decision string) {
	cooldown := symbolCooldownDurationLLM
	if decision != "SKIP" {
		cooldown += 10 * time.Minute
	}
	if err := a.cache.SetSymbolCooldown(ctx, symbol, cooldown); err != nil {
		slog.Warn("failed to set symbol cooldown", "symbol", symbol, "error", err)
	}
}

// skipCooldownUntilNextWindow returns a TTL that expires when the next scan
// window opens so that skipped symbols are re-eligible from that window onwards.
//
//   - FRONTRUN skip  → expires when LAST_MINUTE starts (T-6m before settlement)
//   - LAST_MINUTE skip → expires at settlement (AFTER window start)
func skipCooldownUntilNextWindow(window scheduler.WindowType, now time.Time) time.Duration {
	next := scheduler.NextFundingTime(now.UTC())
	switch window {
	case scheduler.WindowFrontrun:
		d := time.Until(next.Add(-6 * time.Minute))
		if d < time.Minute {
			d = time.Minute
		}
		return d
	case scheduler.WindowLastMinute:
		d := time.Until(next)
		if d < 30*time.Second {
			d = 30 * time.Second
		}
		return d
	default:
		return symbolCooldownDurationLLM
	}
}

// setAllSkipCooldowns puts all evaluated top candidates on cooldown until the
// next scan window opens, preventing re-evaluation of the same bad setups.
func (a *App) setAllSkipCooldowns(ctx context.Context, top []*domain.ScoredCandidate, window scheduler.WindowType) {
	cooldown := skipCooldownUntilNextWindow(window, time.Now())
	for _, sc := range top {
		if err := a.cache.SetSymbolCooldown(ctx, sc.Candidate.Symbol, cooldown); err != nil {
			slog.Warn("failed to set skip cooldown", "symbol", sc.Candidate.Symbol, "error", err)
		}
	}
	slog.Info("skip cooldown applied to all evaluated candidates",
		"count", len(top),
		"window", window,
		"cooldown", cooldown.Round(time.Second),
	)
}

// buildPriorReasons extracts the rationale from a decision for prior-context
// threading into the next LLM call within the same funding cycle.
func buildPriorReasons(d *domain.LLMDecision) []string {
	if d.Action == "SKIP" {
		if d.SkipReason != "" {
			return []string{d.SkipReason}
		}
		return nil
	}
	return d.EntryReasons
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
			// WS cache miss: try a single REST call to seed the cache.
			// This guards against wsPublicData being down or the symbol being newly added.
			if bt := a.fetchBookTickerREST(ctx, c.Symbol); bt != nil {
				a.bookTickerCache.Update(c.Symbol, bt)
				spreadBps, spreadOk = a.bookTickerCache.SpreadBps(c.Symbol)
			}
		}
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

		out = append(out, c)
	}
	return out
}

// askLiquidityToSL sums the USDT value of all ask levels up to slPrice.
// It logs a warning when the returned levels are exhausted before reaching slPrice,
// meaning depth_levels may be too low to capture all liquidity in the SL range.
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
	asks := depth.Asks
	if len(asks) == levels && len(asks) > 0 && asks[len(asks)-1].Price < slPrice {
		slog.Warn("depth coverage incomplete: all levels below SL price, consider raising depth_levels",
			"symbol", symbol,
			"depth_levels", levels,
			"last_ask_price", asks[len(asks)-1].Price,
			"sl_price", slPrice,
		)
	}
	return total, nil
}

func (a *App) fetchBookTickerREST(ctx context.Context, symbol string) *market.BookTicker {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	prices, err := a.binanceClient.GetBookTicker(ctx, symbol)
	if err != nil {
		slog.Warn("book ticker REST fallback failed", "symbol", symbol, "error", err)
		return nil
	}
	if prices.BidPrice == 0 || prices.AskPrice == 0 {
		return nil
	}
	return &market.BookTicker{
		BidPrice:  prices.BidPrice,
		BidQty:    prices.BidQty,
		AskPrice:  prices.AskPrice,
		AskQty:    prices.AskQty,
		UpdatedAt: time.Now(),
	}
}

// isRestrictedDay returns true on days that historically underperform:
//   - Within 1 hour of Monday 00:00 UTC (Sun 23:00–Mon 01:00 UTC)
//   - First 3 days of the month, except Saturday
//   - Last 5 days of the month, except weekends
func isRestrictedDay(t time.Time) bool {
	t = t.UTC()
	weekday := t.Weekday()

	// Monday window: Sun 23:00–Mon 01:00 UTC (H-1 to H+1 around Monday midnight).
	if weekday == time.Monday || weekday == time.Sunday {
		var mondayMidnight time.Time
		if weekday == time.Monday {
			mondayMidnight = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
		} else {
			mondayMidnight = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, time.UTC)
		}
		if t.After(mondayMidnight.Add(-time.Hour)) && t.Before(mondayMidnight.Add(time.Hour)) {
			return true
		}
	}

	isWeekend := weekday == time.Saturday || weekday == time.Sunday
	day := t.Day()

	// First 3 days: skip except Saturday.
	if day <= 3 && weekday != time.Saturday {
		return true
	}
	// Last 5 days: skip except weekends.
	if !isWeekend {
		lastDay := time.Date(t.Year(), t.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
		if day >= lastDay-4 {
			return true
		}
	}

	return false
}

func (a *App) scanFn(ctx context.Context, window scheduler.WindowType) error {
	scanStart := time.Now()
	defer func() {
		slog.Info("scan completed", "window", window, "latency_ms", float64(time.Since(scanStart).Microseconds())/1000.0)
	}()

	if isRestrictedDay(time.Now()) {
		now := time.Now().UTC()
		slog.Info("skipping scan: restricted trading day",
			"weekday", now.Weekday(),
			"day_of_month", now.Day(),
		)
		return nil
	}

	// Reset LLM call state at the start of each new funding cycle so the
	// deduplication check does not suppress calls across settlement boundaries.
	currentDeadline := scheduler.NextFundingTime(time.Now().UTC())
	if window == scheduler.WindowFrontrun && a.lastLLMCall != nil && !a.lastLLMCall.cycleDeadline.Equal(currentDeadline) {
		a.lastLLMCall = nil
	}

	// Early cooldown gate: if the last LLM call is still within cooldown, skip
	// the entire scan to avoid unnecessary scanning, scoring, and memory retrieval.
	cooldown := time.Duration(a.cfg.LLM.CallCooldownSecs) * time.Second
	if lc := a.lastLLMCall; lc != nil && time.Since(lc.calledAt) < cooldown {
		slog.Info("skipping scan: LLM call in cooldown",
			"age", time.Since(lc.calledAt).Round(time.Second),
			"cooldown", cooldown,
		)
		return nil
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

	staleFiltered := scored[:0]
	for _, sc := range scored {
		isStale := sc.Indicators.RSI7_5m < staleReversalRSIThreshold
		if isStale {
			slog.Info("candidate excluded: stale reversal setup",
				"symbol", sc.Candidate.Symbol,
				"rsi7_5m", sc.Indicators.RSI7_5m,
				"daily_roi", sc.Candidate.DailyROI,
			)
			continue
		}
		staleFiltered = append(staleFiltered, sc)
	}
	if len(staleFiltered) == 0 {
		slog.Info("no candidates after stale-reversal gate", "window", window)
		return nil
	}
	scored = staleFiltered

	// Confluence gate: hard-block candidates without at least two independent
	// pieces of reversal evidence. RSI7_5m>=confluenceRSIThreshold alone or a candle
	// pattern alone each historically ran net-negative (single-signal setups lack
	// momentum-loss confirmation). Composite score must clear confluenceScoreThreshold
	// unconditionally — scoring.min_score/min_score_override already reject anything
	// below that floor at execution time, so admitting a low-score candidate here
	// (even with RSI/candle confirmation) only wastes an LLM call on a trade that
	// will be vetoed downstream anyway.
	confluenceFiltered := scored[:0]
	for _, sc := range scored {
		hasReversalEvidence := sc.CompositeScore >= confluenceScoreThreshold &&
			(sc.Breakdown.RSIDivergenceScore > 0 ||
				sc.Breakdown.CandleScore > 0 ||
				sc.Indicators.RSI7_5m >= confluenceRSIThreshold)
		if !hasReversalEvidence {
			slog.Info("candidate excluded: insufficient reversal confluence",
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
	if lc := a.lastLLMCall; lc != nil {
		inputsUnchanged := lc.window == window &&
			math.Abs(lc.score-top[0].CompositeScore) < 0.5 &&
			lc.btcTrend == btcTrend
		if inputsUnchanged {
			slog.Info("skipping LLM call: inputs unchanged",
				"symbol", top[0].Candidate.Symbol,
				"age", time.Since(lc.calledAt).Round(time.Second),
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

	// Build prior context from the previous LLM call in this cycle.
	var priorCtx *llm.LLMPriorContext
	if lc := a.lastLLMCall; lc != nil && lc.cycleDeadline.Equal(currentDeadline) && lc.priorAction != "" {
		priorCtx = &llm.LLMPriorContext{
			Window:   string(lc.window),
			Action:   lc.priorAction,
			Reasons:  lc.priorReasons,
			Warnings: lc.priorWarnings,
			AgeMins:  int(time.Since(lc.calledAt).Minutes()),
		}
	}

	llmStart := time.Now()
	decision, err := a.llmEngine.Evaluate(ctx, top, btc, a.cfg.Execution.TpPct, similarTrades, modeWinRates, priorCtx)
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
		priorAction:   decision.Action,
		priorReasons:  buildPriorReasons(decision),
		priorWarnings: decision.Warnings,
	}

	if decision.Action == "SKIP" {
		slog.Info("LLM decided to skip",
			"reason", decision.SkipReason,
			"queue_depth", a.intentQueue.PendingCount(),
		)
		// Cooldown every evaluated candidate until the next window opens so the
		// same bad setups are not re-scored in the same cycle.
		a.setAllSkipCooldowns(ctx, top, window)
		// A late-cycle skip is a fresh reassessment — cancel any stale queued intent
		// for the same symbol so it cannot fire in a later window (e.g. AFTER).
		// FRONTRUN skips are excluded: there are still multiple windows remaining in
		// the cycle so the LLM may take a different view at LAST_MINUTE or AFTER.
		if window != scheduler.WindowFrontrun && len(top) > 0 {
			a.intentQueue.CancelBySymbol(top[0].Candidate.Symbol)
		}
		if a.cfg.Memory.EmbedSkips {
			for _, sc := range top {
				sc := sc
				go func() {
					bgCtx := context.Background()
					if err := a.memoryEngine.RecordSkip(bgCtx, sc, btc, decision.SkipReason, string(window)); err != nil {
						slog.Error("failed to record skip memory", "symbol", sc.Candidate.Symbol, "error", err)
					}
				}()
			}
		}
		return nil
	}

	a.setSymbolCooldown(ctx, decision.Symbol, decision.Action)

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

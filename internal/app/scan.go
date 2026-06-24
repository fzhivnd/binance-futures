package app

import (
	"context"
	"log/slog"
	"math"
	"sort"
	"sync"
	"time"

	"futures/internal/domain"
	"futures/internal/intent"
	"futures/internal/scanner"
	"futures/internal/scheduler"
)

func (a *App) setSymbolCooldown(ctx context.Context, symbol string) {
	if err := a.cache.SetSymbolCooldown(ctx, symbol, symbolCooldownDurationLLM); err != nil {
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

// filterOccupiedSymbols removes candidates whose symbol either has an active
// position or already has a pending intent in the queue, so the LLM never sees
// a symbol we are already committed to.
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

	//candidates = scanner.FilterByROI(candidates, a.filteringMinROI())
	//if len(candidates) == 0 {
	//	slog.Info("no candidates after ROI filter", "window", window)
	//	return nil
	//}

	filtered := candidates[:0]
	for _, c := range candidates {
		if c.DailyROI > 60 && c.FundingRate > -0.01 {
			slog.Info("candidate excluded: high ROI requires funding <= -1%",
				"symbol", c.Symbol, "daily_roi_pct", c.DailyROI, "funding_rate_pct", c.FundingRate*100)
			continue
		}
		filtered = append(filtered, c)
	}
	candidates = filtered
	if len(candidates) == 0 {
		slog.Info("no candidates after high-ROI funding filter", "window", window)
		return nil
	}

	candidates = scanner.FilterByVolume(candidates, a.filteringMinVolume())
	if len(candidates) == 0 {
		slog.Info("no candidates after volume filter", "window", window)
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

	// Confluence gate: reject candidates with zero reversal evidence before sending to LLM.
	// Funding alone is not enough — we need at least one bearish signal.
	confluenceFiltered := scored[:0]
	for _, sc := range scored {
		hasReversalEvidence := sc.Breakdown.CandleScore > 0 ||
			sc.Breakdown.RSIDivergenceScore > 0 ||
			sc.Indicators.RSI7_5m >= 65
		if !hasReversalEvidence {
			slog.Info("candidate excluded: no reversal evidence",
				"symbol", sc.Candidate.Symbol,
				"candle_score", sc.Breakdown.CandleScore,
				"rsi_div_score", sc.Breakdown.RSIDivergenceScore,
				"rsi7_5m", sc.Indicators.RSI7_5m,
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
			math.Abs(lc.score-top[0].CompositeScore) < 2.5 &&
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
	if a.memoryEngine != nil && len(top) > 0 {
		now := time.Now().UTC()

		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, sc := range top {
			wg.Add(1)
			go func(sc *domain.ScoredCandidate) {
				defer wg.Done()
				similar, serr := a.memoryEngine.RetrieveSimilar(ctx,
					sc.Indicators, btc, &sc.Candidate,
					sc.CompositeScore, string(window), now,
				)
				if serr != nil {
					slog.Warn("memory retrieval failed for candidate",
						"symbol", sc.Candidate.Symbol, "error", serr)
					return
				}
				if len(similar) > 0 {
					mu.Lock()
					similarTrades[sc.Candidate.Symbol] = similar
					mu.Unlock()
				}
			}(sc)
		}
		wg.Wait()
	}

	llmStart := time.Now()
	decision, err := a.llmEngine.Evaluate(ctx, top, btc, a.cfg.Execution.TpPct, similarTrades)
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
	a.setSymbolCooldown(ctx, decision.Symbol)
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
				if err := a.memoryEngine.RecordSkip(bgCtx, top, btc, decision.SkipReason, string(window)); err != nil {
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

	selected.PositionSizePct = confidenceToSize(decision.Confidence)

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

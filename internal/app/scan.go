package app

import (
	"context"
	"log/slog"
	"math"
	"sort"
	"time"

	"futures/internal/domain"
	"futures/internal/intent"
	"futures/internal/scanner"
	"futures/internal/scheduler"
)

func (a *App) scanFn(ctx context.Context, window scheduler.WindowType) error {
	scanStart := time.Now()
	defer func() {
		slog.Info("scan completed", "window", window, "latency_ms", float64(time.Since(scanStart).Microseconds())/1000.0)
	}()

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
	for _, c := range candidates {
		slog.Info("candidate", "symbol", c.Symbol, "funding", c.FundingRate, "roi_1d", c.ROI1D, "score", c.Score)
	}

	candidates = scanner.FilterByROI(candidates, a.cfg.Filtering.MinDailyROIPct)
	if len(candidates) == 0 {
		slog.Info("no candidates after ROI filter", "window", window)
		return nil
	}

	candidates = scanner.FilterByVolume(candidates, a.cfg.Filtering.MinVolume24hM)
	if len(candidates) == 0 {
		slog.Info("no candidates after volume filter", "window", window)
		return nil
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
	sem := make(chan struct{}, len(candidates))
	for i, c := range candidates {
		go func(idx int, cand domain.Candidate) {
			snap, ierr := a.indEngine.Compute(ctx, cand.Symbol)
			if ierr != nil {
				results[idx] = candResult{err: ierr}
			} else {
				results[idx] = candResult{sc: a.scorer.Score(cand, snap, btc)}
			}
			sem <- struct{}{}
		}(i, c)
	}
	for range candidates {
		<-sem
	}

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

	if !a.cfg.LLM.Enabled || a.llmEngine == nil {
		best := scored[0]

		slog.Info("top scored candidate",
			"symbol", best.Candidate.Symbol,
			"score", best.CompositeScore,
			"confidence", best.Confidence,
			"funding", best.Candidate.FundingRate,
		)

		if window == scheduler.WindowLastMinute {
			rate := best.Candidate.FundingRate
			if rate < -0.01 || rate > -0.002 {
				slog.Info("last-minute window: funding rate outside allowed range, skipping",
					"symbol", best.Candidate.Symbol,
					"funding", rate,
					"allowed", "-0.2% to -1%",
				)
				return nil
			}
		}

		if err := a.riskEngine.EvaluateCandidate(ctx, best, btc); err != nil {
			slog.Info("candidate rejected by risk engine", "symbol", best.Candidate.Symbol, "reason", err)
			return nil
		}
		return a.execEng.ExecuteScored(ctx, best, window)
	}

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
	if lc := a.lastLLMCall; lc != nil &&
		lc.window == window &&
		lc.symbol == top[0].Candidate.Symbol &&
		math.Abs(lc.score-top[0].CompositeScore) < 5 &&
		lc.btcTrend == btcTrend &&
		time.Since(lc.calledAt) < cooldown {
		slog.Debug("skipping LLM call: inputs unchanged",
			"symbol", top[0].Candidate.Symbol,
			"age", time.Since(lc.calledAt).Round(time.Second),
		)
		return nil
	}

	var similarTrades []domain.SimilarTrade
	if a.memoryEngine != nil && len(top) > 0 {
		next := scheduler.NextFundingTime(time.Now().UTC())
		minsToSettle := int(math.Round(time.Until(next).Minutes()))
		if minsToSettle < 0 {
			minsToSettle = 0
		}
		topSnap := top[0].Indicators
		similar, serr := a.memoryEngine.RetrieveSimilar(ctx,
			topSnap, btc, &top[0].Candidate,
			top[0].CompositeScore, "", minsToSettle,
		)
		if serr != nil {
			slog.Warn("memory retrieval failed, proceeding without", "error", serr)
		} else {
			similarTrades = similar
		}
	}

	decision, err := a.llmEngine.Evaluate(ctx, top, btc, a.cfg.Execution.TpPct, similarTrades)
	if err != nil {
		slog.Error("LLM engine error", "error", err)
		return nil
	}

	a.lastLLMCall = &llmCallState{
		symbol:   top[0].Candidate.Symbol,
		score:    top[0].CompositeScore,
		btcTrend: btcTrend,
		window:   window,
		calledAt: time.Now(),
	}

	if decision.Action == "SKIP" {
		slog.Info("LLM decided to skip",
			"reason", decision.SkipReason,
			"queue_depth", a.intentQueue.PendingCount(),
		)
		if a.cfg.Memory.EmbedSkips {
			go func() {
				bgCtx := context.Background()
				if err := a.memoryEngine.RecordSkip(bgCtx, top, btc, decision.SkipReason); err != nil {
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

	return nil
}

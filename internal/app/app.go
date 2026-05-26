package app

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"futures/internal/config"
	"futures/internal/domain"
	"futures/internal/exchange"
	"futures/internal/execution"
	"futures/internal/indicator"
	"futures/internal/intent"
	"futures/internal/llm"
	"futures/internal/market"
	"futures/internal/risk"
	"futures/internal/scanner"
	"futures/internal/scheduler"
	"futures/internal/scoring"
	"futures/internal/storage"
)

// llmCallState tracks what we last sent to the LLM so we can skip redundant calls.
type llmCallState struct {
	symbol   string
	score    float64
	btcTrend string
	window   scheduler.WindowType
	calledAt time.Time
}

type App struct {
	cfg         *config.Config
	engine      *market.MarketEngine
	scanner     *scanner.FundingScanner
	executor    execution.Executor
	execEng     *execution.ExecutionEngine
	posMgr      *execution.PositionManager
	sched       *scheduler.Scheduler
	wsMarkPx    *exchange.WSConnection
	wsKlines    *exchange.WSConnection
	wsUserData  *exchange.WSConnection
	indEngine   *indicator.Engine
	scorer      *scoring.Scorer
	riskEngine  *risk.Engine
	drawdown    *risk.DrawdownTracker
	llmEngine   *llm.DecisionEngine
	intentQueue *intent.Queue
	lastLLMCall *llmCallState
}

func New(cfg *config.Config) (*App, error) {
	return &App{cfg: cfg}, nil
}

func (a *App) Run(
	ctx context.Context,
	pool *pgxpool.Pool,
	redisClient *redis.Client,
	tradeRepo storage.TradeRepository,
	riskRepo *storage.PGRiskRepository,
	cache storage.StateCache,
) error {
	defer func() {
		pool.Close()
		redisClient.Close()
	}()

	// Market engine
	a.engine = market.NewMarketEngine()

	// Binance REST client
	binanceClient := exchange.NewBinanceClient(
		a.cfg.Binance.APIKey,
		a.cfg.Binance.APISecret,
		a.cfg.Binance.BaseURL,
	)

	// Exchange info (symbols)
	slog.Info("fetching exchange info")
	info, err := binanceClient.GetExchangeInfo(ctx)
	if err != nil {
		return err
	}
	slog.Info("exchange info loaded", "symbols", len(info.Symbols))

	// Funding interval hours per symbol — used for accurate daily ROI calculation
	slog.Info("fetching funding info")
	fundingInfoList, err := binanceClient.GetFundingInfo(ctx)
	if err != nil {
		slog.Warn("failed to fetch funding info, defaulting to 4h interval", "error", err)
	} else {
		for _, fi := range fundingInfoList {
			a.engine.SetFundingInterval(fi.Symbol, fi.FundingIntervalHours)
		}
		slog.Info("funding intervals loaded", "symbols", len(fundingInfoList))
	}

	// Executor
	if a.cfg.App.Mode == "live" {
		a.executor = execution.NewLiveExecutor(binanceClient)
	} else {
		a.executor = execution.NewPaperExecutor(
			a.cfg.App.PaperBalance,
			a.cfg.Execution.SlippageBps,
			a.engine.TickerCache(),
		)
	}

	// Scanner
	a.scanner = scanner.NewFundingScanner(a.engine, &a.cfg.Funding)

	// Phase 2: indicator engine, scorer, risk engine
	a.indEngine = indicator.NewEngine(a.engine, a.engine.OIHistory())
	weights := scoring.WeightConfig{
		Funding:    a.cfg.Scoring.Weights.Funding,
		OI:         a.cfg.Scoring.Weights.OI,
		BTC:        a.cfg.Scoring.Weights.BTC,
		Candle:     a.cfg.Scoring.Weights.Candle,
		Volume:     a.cfg.Scoring.Weights.Volume,
		ROI:        a.cfg.Scoring.Weights.ROI,
		Volatility: a.cfg.Scoring.Weights.Volatility,
	}
	a.scorer = scoring.NewScorer(weights)
	a.drawdown = risk.NewDrawdownTracker(a.cfg.App.PaperBalance)
	a.riskEngine = risk.NewEngine(cache, tradeRepo, a.drawdown, risk.Config{
		MaxDailyLosses:    a.cfg.Risk.MaxDailyLosses,
		MaxDrawdownPct:    a.cfg.Risk.MaxDrawdownPct,
		MaxATRRatio:       a.cfg.Risk.MaxATRRatio,
		BTCBreakoutReject: a.cfg.Risk.BTCBreakoutReject,
		MinCompositeScore: a.cfg.Scoring.MinScore,
		MaxPositions:      a.cfg.Trading.MaxPositions,
		CooldownMinutes:   a.cfg.Trading.CooldownMinutes,
	})

	// Execution engine
	a.execEng = execution.NewExecutionEngine(
		a.executor, a.engine, cache, tradeRepo, riskRepo, a.cfg,
	)

	// Position manager
	a.posMgr = execution.NewPositionManager(
		a.executor, a.engine, cache, tradeRepo, riskRepo, a.cfg,
	)

	// Kill switch callback
	killSwitchFn := func() {
		_ = cache.SetKillSwitch(context.Background(), true)
		slog.Error("kill switch activated")
	}

	wsCfg := &a.cfg.WebSocket

	// Mark price WS — combined stream for all symbols
	markPriceURL := a.cfg.Binance.WsURL + "/ws/!markPrice@arr@1s"
	router := exchange.NewStreamRouter(a.engine)
	a.wsMarkPx = exchange.NewWSConnection(
		markPriceURL,
		router.Handle,
		wsCfg.GetStaleTimeout(),
		wsCfg.GetPingInterval(),
		wsCfg.GetReconnectBaseBackoff(),
		wsCfg.GetReconnectMaxBackoff(),
		wsCfg.MaxReconnectFailures,
		killSwitchFn,
	)

	// Kline WS — dynamic subscription for top symbols
	klineURL := a.cfg.Binance.WsURL + "/stream"
	a.wsKlines = exchange.NewWSConnection(
		klineURL,
		router.Handle,
		wsCfg.GetStaleTimeout()*2, // klines less frequent, use longer stale timeout
		wsCfg.GetPingInterval(),
		wsCfg.GetReconnectBaseBackoff(),
		wsCfg.GetReconnectMaxBackoff(),
		wsCfg.MaxReconnectFailures,
		killSwitchFn,
	)

	// User data stream (live mode only) — delivers ORDER_TRADE_UPDATE for accurate close detection
	var listenKey string
	if a.cfg.App.Mode == "live" {
		listenKey, err = binanceClient.CreateListenKey(ctx)
		if err != nil {
			return fmt.Errorf("create listenKey: %w", err)
		}
		userDataURL := a.cfg.Binance.WsURL + "/ws/" + listenKey
		userDataRouter := exchange.NewUserDataRouter(a.posMgr.HandleUserDataEvent)
		a.wsUserData = exchange.NewWSConnection(
			userDataURL,
			userDataRouter.Handle,
			wsCfg.GetStaleTimeout(),
			wsCfg.GetPingInterval(),
			wsCfg.GetReconnectBaseBackoff(),
			wsCfg.GetReconnectMaxBackoff(),
			wsCfg.MaxReconnectFailures,
			killSwitchFn,
		)
	}

	// Phase 3: LLM engine + intent queue
	if a.cfg.LLM.Enabled && a.cfg.LLM.APIKey != "" {
		llmClient := llm.NewClient(llm.ClientConfig{
			APIKey:     a.cfg.LLM.APIKey,
			Model:      a.cfg.LLM.Model,
			Timeout:    time.Duration(a.cfg.LLM.TimeoutSecs) * time.Second,
			RetryCount: a.cfg.LLM.MaxRetries,
			RetryDelay: 500 * time.Millisecond,
			MaxRPM:     a.cfg.LLM.MaxRPM,
		})
		a.llmEngine = llm.NewDecisionEngine(llmClient)
		slog.Info("LLM engine enabled", "model", a.cfg.LLM.Model)
	} else {
		slog.Info("LLM engine disabled, using Phase 2 deterministic scoring")
	}

	frontrunInterval := time.Duration(a.cfg.LLM.FrontrunExecIntervalSecs) * time.Second
	a.intentQueue = intent.NewQueue(func(ctx context.Context, sc *domain.ScoredCandidate, decision *domain.LLMDecision) error {
		// Re-validate risk at fire time — market may have shifted since intent was queued.
		btcAtFire, _ := a.indEngine.ComputeBTCContext(ctx)
		if err := a.riskEngine.EvaluateCandidate(ctx, sc, btcAtFire); err != nil {
			slog.Info("intent rejected by risk engine at fire time",
				"symbol", sc.Candidate.Symbol, "reason", err)
			return nil
		}
		return a.execEng.ExecuteScoredWithLLM(ctx, sc, decision)
	}, frontrunInterval)

	// Scheduler scan function
	scanFn := func(ctx context.Context, window scheduler.WindowType) error {
		// Phase 1: pre-checks
		if err := a.riskEngine.PreCheck(ctx); err != nil {
			slog.Info("pre-check rejected", "reason", err)
			return nil
		}

		// Phase 1: funding scanner
		candidates, err := a.scanner.Scan(ctx)
		if err != nil {
			return err
		}
		if len(candidates) == 0 {
			slog.Info("no candidates after funding filter", "window", window)
			return nil
		}

		// Phase 2: ROI filter
		candidates = scanner.FilterByROI(candidates, a.cfg.Filtering.MinDailyROIPct)
		if len(candidates) == 0 {
			slog.Info("no candidates after ROI filter", "window", window)
			return nil
		}

		// Phase 2: volume filter
		candidates = scanner.FilterByVolume(candidates, a.cfg.Filtering.MinVolume24hM)
		if len(candidates) == 0 {
			slog.Info("no candidates after volume filter", "window", window)
			return nil
		}

		// Phase 2: BTC context
		btc, err := a.indEngine.ComputeBTCContext(ctx)
		if err != nil {
			slog.Warn("BTC context unavailable, proceeding without it", "error", err)
		}

		// Phase 2: compute indicators + score each candidate in parallel
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

		// Collect valid scored candidates
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

		// Sort by score descending
		sort.Slice(scored, func(i, j int) bool {
			return scored[i].CompositeScore > scored[j].CompositeScore
		})

		// ===== PHASE 3: LLM + INTENT QUEUE =====
		if !a.cfg.LLM.Enabled || a.llmEngine == nil {
			// Fallback: Phase 2 deterministic — pick best and execute immediately
			best := scored[0]

			slog.Info("top scored candidate",
				"symbol", best.Candidate.Symbol,
				"score", best.CompositeScore,
				"confidence", best.Confidence,
				"funding", best.Candidate.FundingRate,
			)

			// Last-minute window: only allow moderate funding rates (-0.2% to -1%)
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

		// Take top N candidates for LLM evaluation
		topN := a.cfg.LLM.TopCandidates
		if topN > len(scored) {
			topN = len(scored)
		}
		top := scored[:topN]

		// Skip LLM call if inputs are materially unchanged since the last call.
		// Window change always triggers a fresh call regardless of other fields.
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

		// Call LLM
		decision, err := a.llmEngine.Evaluate(ctx, top, btc, window)
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
			// A SKIP for today's top symbol doesn't invalidate other queued intents.
			slog.Info("LLM decided to skip",
				"reason", decision.SkipReason,
				"queue_depth", a.intentQueue.PendingCount(),
			)
			return nil
		}

		// Find the LLM-selected candidate
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

		// Apply LLM confidence → position sizing
		selected.PositionSizePct = confidenceToSize(decision.Confidence)

		// Queue the intent; it fires when target window arrives
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

	a.sched = scheduler.NewScheduler(&a.cfg.Scheduler, cache, scanFn)
	a.sched.SetIntentQueue(a.intentQueue)

	// Start goroutines
	go a.wsMarkPx.Run(ctx)
	go a.wsKlines.Run(ctx)
	go a.posMgr.Run(ctx)
	go a.sched.Run(ctx)
	go a.oiPoller(ctx, binanceClient)
	go a.klineSubscriber(ctx)
	go a.fundingIntervalRefresher(ctx, binanceClient)
	if a.cfg.App.Mode == "live" {
		go a.wsUserData.Run(ctx)
		go a.keepAliveListenKey(ctx, binanceClient, listenKey)
	}

	slog.Info("bot started", "mode", a.cfg.App.Mode)

	// Wait for shutdown
	<-ctx.Done()
	slog.Info("shutting down...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = shutdownCtx

	slog.Info("shutdown complete")
	return nil
}

// fundingIntervalRefresher re-fetches /fapi/v1/fundingInfo shortly after each
// funding settlement so that interval changes (e.g. 4h → 1h) are picked up
// before the next scan cycle uses the updated daily ROI calculation.
func (a *App) fundingIntervalRefresher(ctx context.Context, client *exchange.BinanceClient) {
	for {
		// Sleep until 30 seconds after the next funding settlement.
		next := scheduler.NextFundingTime(time.Now().UTC())
		sleepUntil := next.Add(30 * time.Second)
		delay := time.Until(sleepUntil)
		if delay < 0 {
			delay = 30 * time.Second
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}

		list, err := client.GetFundingInfo(ctx)
		if err != nil {
			slog.Warn("funding interval refresh failed", "error", err)
			continue
		}
		for _, fi := range list {
			a.engine.SetFundingInterval(fi.Symbol, fi.FundingIntervalHours)
		}
		slog.Info("funding intervals refreshed", "symbols", len(list))
	}
}

func (a *App) oiPoller(ctx context.Context, client *exchange.BinanceClient) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			symbols := a.engine.GetTopNegativeFundingSymbols(20)
			for _, sym := range symbols {
				oi, err := client.GetOpenInterest(ctx, sym)
				if err != nil {
					continue
				}
				a.engine.UpdateOI(sym, oi.OpenInterest)
			}
		}
	}
}

func (a *App) klineSubscriber(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	var currentSymbols []string

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			newSymbols := a.engine.GetTopNegativeFundingSymbols(20)
			if len(newSymbols) == 0 {
				continue
			}
			a.updateKlineSubscriptions(ctx, currentSymbols, newSymbols)
			currentSymbols = newSymbols
		}
	}
}

// keepAliveListenKey pings /fapi/v1/listenKey every 30 minutes to prevent expiry (Binance timeout is 60 min).
func (a *App) keepAliveListenKey(ctx context.Context, client *exchange.BinanceClient, listenKey string) {
	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := client.KeepAliveListenKey(ctx, listenKey); err != nil {
				slog.Warn("listenKey keepalive failed", "error", err)
			}
		}
	}
}

func confidenceToSize(confidence int) float64 {
	switch {
	case confidence >= 90:
		return 5.0
	case confidence >= 80:
		return 4.0
	case confidence >= 70:
		return 3.0
	case confidence >= 60:
		return 2.0
	default:
		return 0
	}
}

func (a *App) updateKlineSubscriptions(ctx context.Context, old, new []string) {
	timeframes := []string{"5m", "15m", "30m", "1h", "4h", "1d"}
	oldSet := make(map[string]bool)
	for _, s := range old {
		oldSet[s] = true
	}
	newSet := make(map[string]bool)
	for _, s := range new {
		newSet[s] = true
	}

	var toUnsub []string
	var removed []string
	for _, s := range old {
		if !newSet[s] {
			for _, tf := range timeframes {
				toUnsub = append(toUnsub, s+"@kline_"+tf)
			}
			removed = append(removed, s)
		}
	}
	var toSub []string
	for _, s := range new {
		if !oldSet[s] {
			for _, tf := range timeframes {
				toSub = append(toSub, s+"@kline_"+tf)
			}
		}
	}

	if len(toUnsub) > 0 {
		_ = a.wsKlines.Unsubscribe(ctx, toUnsub)
	}
	if len(toSub) > 0 {
		_ = a.wsKlines.Subscribe(ctx, toSub)
	}

	// Purge stale candle and OI data for symbols that left the watchlist.
	for _, sym := range removed {
		a.engine.PurgeSymbol(sym)
	}
}

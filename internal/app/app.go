package app

import (
	"context"
	"fmt"
	"log/slog"
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
	"futures/internal/memory"
	"futures/internal/notify"
	"futures/internal/risk"
	"futures/internal/scanner"
	"futures/internal/scheduler"
	"futures/internal/scoring"
	"futures/internal/storage"
	"futures/internal/summary"
	"futures/internal/telemetry"
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
	cfg           *config.Config
	engine        *market.MarketEngine
	scanner       *scanner.FundingScanner
	executor      execution.Executor
	execEng       *execution.ExecutionEngine
	posMgr        *execution.PositionManager
	sched         *scheduler.Scheduler
	wsMarkPx      *exchange.WSConnection
	wsKlines      *exchange.WSConnection
	wsUserData    *exchange.WSConnection
	indEngine     *indicator.Engine
	scorer        *scoring.Scorer
	riskEngine    *risk.Engine
	drawdown      *risk.DrawdownTracker
	llmEngine     *llm.DecisionEngine
	intentQueue   *intent.Queue
	lastLLMCall   *llmCallState
	memoryEngine  *memory.Engine
	binanceClient *exchange.BinanceClient
	// Phase 8
	bookTickerCache *market.BookTickerCache
	afterTrigger    *execution.AfterTrigger
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

	a.engine = market.NewMarketEngine()
	a.bookTickerCache = market.NewBookTickerCache()

	a.binanceClient = exchange.NewBinanceClient(
		a.cfg.Binance.APIKey,
		a.cfg.Binance.APISecret,
		a.cfg.Binance.BaseURL,
	)
	binanceClient := a.binanceClient

	slog.Info("fetching exchange info")
	info, err := binanceClient.GetExchangeInfo(ctx)
	if err != nil {
		return err
	}
	slog.Info("exchange info loaded", "symbols", len(info.Symbols))

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

	if a.cfg.App.Mode == "live" {
		a.executor = telemetry.NewInstrumentedExecutor(execution.NewLiveExecutor(binanceClient))
	} else {
		a.executor = telemetry.NewInstrumentedExecutor(execution.NewPaperExecutor(
			a.cfg.App.PaperBalance,
			a.cfg.Execution.SlippageBps,
			a.engine.TickerCache(),
		))
	}

	a.scanner = scanner.NewFundingScanner(a.engine, &a.cfg.Funding)

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
	notifier := notify.NewNotifier(
		a.cfg.Telegram.Enabled,
		a.cfg.Telegram.BotToken,
		a.cfg.Telegram.ChatID,
		a.cfg.Telegram.TimeoutSecs,
	)

	a.riskEngine = risk.NewEngine(cache, tradeRepo, a.drawdown, risk.Config{
		MaxDailyLosses:    a.cfg.Risk.MaxDailyLosses,
		MaxDrawdownPct:    a.cfg.Risk.MaxDrawdownPct,
		MaxATRRatio:       a.cfg.Risk.MaxATRRatio,
		BTCBreakoutReject: a.cfg.Risk.BTCBreakoutReject,
		MinCompositeScore: a.cfg.Scoring.MinScore,
		MaxPositions:      a.cfg.Trading.MaxPositions,
		CooldownMinutes:   a.cfg.Trading.CooldownMinutes,
	}, notifier)

	a.execEng = execution.NewExecutionEngine(
		a.executor, a.engine, cache, tradeRepo, riskRepo, a.cfg, notifier,
	)

	a.posMgr = execution.NewPositionManager(
		a.executor, a.engine, cache, tradeRepo, riskRepo, a.cfg, notifier,
	)
	a.posMgr.SetIndicatorEngine(a.indEngine)
	a.execEng.SetPositionManager(a.posMgr)

	killSwitchFn := func() {
		_ = cache.SetKillSwitch(context.Background(), true)
		slog.Error("kill switch activated")
		if notifier != nil {
			notifier.NotifyRiskEvent(context.Background(), notify.RiskEvent{
				Type:    "kill_switch",
				Message: "Kill switch activated: WebSocket max failures reached. Bot is halted.",
			})
		}
	}

	wsCfg := &a.cfg.WebSocket

	markPriceURL := a.cfg.Binance.WsURL + "/market/ws/!markPrice@arr@1s"
	router := exchange.NewStreamRouter(a.engine)
	router.SetBookTickerCache(a.bookTickerCache) // Phase 8: route @bookTicker events
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

	klineURL := a.cfg.Binance.WsURL + "/market/stream"
	a.wsKlines = exchange.NewWSConnection(
		klineURL,
		router.Handle,
		10*time.Minute, // kline stream starts empty — long stale timeout until symbols are subscribed
		wsCfg.GetPingInterval(),
		wsCfg.GetReconnectBaseBackoff(),
		wsCfg.GetReconnectMaxBackoff(),
		wsCfg.MaxReconnectFailures,
		nil, // kline stream is non-critical — don't trigger kill switch on failure
	)

	var listenKey string
	if a.cfg.App.Mode == "live" {
		listenKey, err = binanceClient.CreateListenKey(ctx)
		if err != nil {
			return fmt.Errorf("create listenKey: %w", err)
		}
		userDataURL := a.cfg.Binance.WsURL + "/private/ws/" + listenKey
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

	var llmClient *llm.Client
	if a.cfg.LLM.Enabled && a.cfg.LLM.APIKey != "" {
		llmClient = llm.NewClient(llm.ClientConfig{
			APIKey:     a.cfg.LLM.APIKey,
			Model:      a.cfg.LLM.Model,
			Timeout:    time.Duration(a.cfg.LLM.TimeoutSecs) * time.Second,
			RetryCount: a.cfg.LLM.MaxRetries,
			RetryDelay: 500 * time.Millisecond,
			MaxRPM:     a.cfg.LLM.MaxRPM,
		})
		a.llmEngine = llm.NewDecisionEngine(llmClient)
		slog.Info("LLM engine enabled", "model", a.cfg.LLM.Model)

		if a.cfg.Execution.ForceSLEnabled {
			a.posMgr.SetForceSLEngine(llm.NewForceSLEngine(llmClient))
			slog.Info("force-SL engine enabled",
				"start_min", a.cfg.Execution.ForceSLStartMin,
				"fast_interval_sec", a.cfg.Execution.ForceSLFastIntervalSec,
			)
		}
	} else {
		slog.Info("LLM engine disabled, using Phase 2 deterministic scoring")
	}

	if a.cfg.Memory.Enabled && a.cfg.LLM.APIKey != "" {
		memClient := llmClient
		if memClient == nil {
			memClient = llm.NewClient(llm.ClientConfig{
				APIKey:     a.cfg.LLM.APIKey,
				Model:      a.cfg.Memory.SummarizerModel,
				Timeout:    15 * time.Second,
				RetryCount: 1,
				RetryDelay: 500 * time.Millisecond,
				MaxRPM:     30,
			})
		}
		maxAge := a.cfg.Memory.GetMaxMemoryAge()
		memRepo := telemetry.NewInstrumentedMemoryRepo(storage.NewPGMemoryRepository(pool, maxAge))
		a.memoryEngine = memory.NewEngine(memory.EngineConfig{
			Enabled: true,
			EmbedderCfg: memory.EmbedderConfig{
				APIKey:  a.cfg.LLM.APIKey,
				Model:   a.cfg.Memory.EmbeddingModel,
				Timeout: 10 * time.Second,
			},
			RetrieverCfg: memory.RetrieverConfig{
				TopSimilar:    a.cfg.Memory.TopSimilar,
				MinSimilarity: a.cfg.Memory.MinSimilarity,
				MaxAge:        maxAge,
			},
			LLMClient: memClient,
			Repo:      memRepo,
		})
		a.posMgr.SetMemoryRepo(memRepo)
		slog.Info("memory engine enabled",
			"embedding_model", a.cfg.Memory.EmbeddingModel,
			"top_similar", a.cfg.Memory.TopSimilar,
		)
	} else {
		a.memoryEngine = memory.NewEngine(memory.EngineConfig{Enabled: false})
	}

	frontrunInterval := time.Duration(a.cfg.LLM.FrontrunExecIntervalSecs) * time.Second
	a.intentQueue = intent.NewQueue(func(ctx context.Context, sc *domain.ScoredCandidate, decision *domain.LLMDecision) error {
		btcAtFire, _ := a.indEngine.ComputeBTCContext(ctx)
		if err := a.riskEngine.EvaluateCandidate(ctx, sc, btcAtFire); err != nil {
			slog.Info("intent rejected by risk engine at fire time",
				"symbol", sc.Candidate.Symbol, "reason", err)
			return nil
		}
		if err := a.executor.SetLeverage(ctx, sc.Candidate.Symbol, a.cfg.Trading.Leverage); err != nil {
			slog.Warn("set leverage failed before execution, continuing", "symbol", sc.Candidate.Symbol, "error", err)
		}
		return a.execEng.ExecuteScoredWithLLM(ctx, sc, decision)
	}, frontrunInterval)

	a.sched = scheduler.NewScheduler(&a.cfg.Scheduler, cache, a.scanFn)
	a.sched.SetIntentQueue(a.intentQueue)

	// Phase 8: AfterTrigger — precision T+0 execution for AFTER intents.
	a.posMgr.SetFundingInfoGetter(a.engine)
	afterStrategy := execution.NewAfterExecutionStrategy(a.bookTickerCache, a.execEng, notifier, a.cfg)
	a.afterTrigger = execution.NewAfterTrigger(
		a.intentQueue,
		afterStrategy,
		a.bookTickerCache,
		a.wsKlines, // subscribe @bookTicker on the combined stream
		a.binanceClient,
		a.engine,
		a.executor,
		a.cfg,
	)

	// Phase 7: startup reconciliation (live mode only, non-fatal)
	if a.cfg.App.Mode == "live" {
		if err := execution.Reconcile(ctx, binanceClient, a.executor, cache, tradeRepo, a.cfg, notifier); err != nil {
			slog.Warn("startup reconciliation failed", "error", err)
		}
	}

	// Phase 6: daily summary service
	summaryRepo := storage.NewPGSummaryRepository(pool)
	summarySvc := summary.NewService(tradeRepo, summaryRepo, a.cfg.App.Mode == "paper")
	if a.cfg.Summary.Enabled {
		if err := summarySvc.BackfillIfNeeded(ctx); err != nil {
			slog.Warn("daily summary backfill failed", "error", err)
		}
		dailySched := summary.NewDailyScheduler(summarySvc, func(ctx context.Context, s *domain.DailySummary) {
			notifier.NotifyDailySummary(ctx, s)
		})
		go dailySched.Run(ctx)
	}

	go a.wsMarkPx.Run(ctx)
	go a.wsKlines.Run(ctx)
	go a.posMgr.Run(ctx)
	go a.sched.Run(ctx)
	go a.oiPoller(ctx, binanceClient)
	go a.klineSubscriber(ctx)
	go a.fundingIntervalRefresher(ctx, binanceClient)
	if a.cfg.Memory.Enabled {
		go a.startSkipValidator(ctx)
	}
	if a.cfg.App.Mode == "live" {
		go a.wsUserData.Run(ctx)
		go a.keepAliveListenKey(ctx, binanceClient, listenKey)
	}
	// Phase 8: AfterTrigger + pre-settlement checker
	go a.afterTrigger.Run(ctx)
	a.posMgr.RunPreSettlementChecker(ctx)

	slog.Info("bot started", "mode", a.cfg.App.Mode)

	<-ctx.Done()
	slog.Info("shutting down...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = shutdownCtx

	slog.Info("shutdown complete")
	return nil
}

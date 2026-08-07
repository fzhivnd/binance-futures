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
	symbol        string
	score         float64
	btcTrend      string
	window        scheduler.WindowType
	calledAt      time.Time
	cycleDeadline time.Time
	// Prior decision context carried into the next call within the same cycle.
	priorAction   string
	priorReasons  []string
	priorWarnings []string
}

const (
	symbolCooldownDurationLLM   = 10 * time.Minute
	symbolCooldownDurationClose = 5 * time.Minute
)

type App struct {
	cfg             *config.Config
	engine          *market.MarketEngine
	scanner         *scanner.FundingScanner
	executor        execution.Executor
	execEng         *execution.ExecutionEngine
	posMgr          *execution.PositionManager
	sched           *scheduler.Scheduler
	wsMarkPx        *exchange.WSConnection
	wsMarketData    *exchange.WSConnection
	wsPublicData    *exchange.WSConnection
	wsUserData      *exchange.WSConnection
	indEngine       *indicator.Engine
	scorer          *scoring.Scorer
	riskEngine      *risk.Engine
	drawdown        *risk.DrawdownTracker
	llmEngine       *llm.DecisionEngine
	intentQueue     *intent.Queue
	lastLLMCall     *llmCallState
	cache           storage.StateCache
	memoryEngine    *memory.Engine
	binanceClient   *exchange.BinanceClient
	bookTickerCache *market.BookTickerCache
	afterTrigger    *execution.AfterTrigger
}

func New(cfg *config.Config) (*App, error) {
	return &App{cfg: cfg}, nil
}

// applyBotParams writes DB-loaded BotParams back into a.cfg so all downstream
// components (execution engine, scanner, risk engine) pick them up transparently.
func (a *App) applyBotParams(sc *domain.ScoringConfig) {
	p := sc.BotParams
	a.cfg.Trading.MaxPositions = p.MaxPositions
	a.cfg.Trading.Leverage = p.Leverage
	a.cfg.Trading.PositionSizePct = p.PositionSizePct
	a.cfg.Funding.MinRate = p.FundingMinRate
	a.cfg.Funding.MaxRate = p.FundingMaxRate
	a.cfg.Execution.SlPct = p.SlPct
	a.cfg.Execution.TpPct = p.TpPct
	a.cfg.Execution.TP2Pct = p.Tp2Pct
	a.cfg.Execution.TrailingActivationPct = p.TrailingActivationPct
	a.cfg.Execution.BreakevenActivationPct = p.BreakevenActivationPct
}

// filteringMinROI and filteringMinVolume return live values from the active scorer config.
func (a *App) filteringMinROI() float64    { return a.scorer.Config().BotParams.MinDailyROIPct }
func (a *App) filteringMinVolume() float64 { return a.scorer.Config().BotParams.MinVolume24hM }

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

	a.cache = cache
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
		a.executor = telemetry.NewInstrumentedExecutor(execution.NewLiveExecutor(binanceClient, info))
	} else {
		a.executor = telemetry.NewInstrumentedExecutor(execution.NewPaperExecutor(
			a.cfg.App.PaperBalance,
			a.cfg.Execution.SlippageBps,
			a.engine.TickerCache(),
		))
	}

	scoringRepo := storage.NewPGScoringConfigRepo(pool)
	scoringCfg, err := scoringRepo.LoadActive(ctx)
	if err != nil || scoringCfg == nil {
		slog.Error("no active scoring config in DB — cannot start", "error", err)
		return fmt.Errorf("scoring config required: %w", err)
	}
	a.applyBotParams(scoringCfg)
	a.scorer = scoring.NewScorerFromConfig(scoringCfg)
	slog.Info("scoring config loaded", "config_id", scoringCfg.ID, "name", scoringCfg.Name)

	go func() {
		ticker := time.NewTicker(60 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cfg, err := scoringRepo.LoadActive(ctx)
				if err != nil || cfg == nil {
					slog.Warn("scoring config reload failed", "error", err)
					continue
				}
				a.applyBotParams(cfg)
				a.scorer.UpdateConfig(cfg)
				slog.Info("scoring config reloaded", "config_id", cfg.ID, "name", cfg.Name)
			}
		}
	}()

	a.scanner = scanner.NewFundingScanner(a.engine, &a.cfg.Funding)

	a.indEngine = indicator.NewEngine(a.engine, a.engine.OIHistory())
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
		MinScoreOverride:  a.cfg.Scoring.MinScoreOverride,
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

	marketUrl := a.cfg.Binance.WsURL + "/market/stream"
	a.wsMarketData = exchange.NewWSConnection(
		marketUrl,
		router.Handle,
		wsCfg.GetStaleTimeout(), // enforced only while symbols are subscribed (window-scoped lifecycle)
		wsCfg.GetPingInterval(),
		wsCfg.GetReconnectBaseBackoff(),
		wsCfg.GetReconnectMaxBackoff(),
		wsCfg.MaxReconnectFailures,
		nil, // market data stream is non-critical — don't trigger kill switch on failure
	)
	a.wsMarketData.SetIdleWhenUnsubscribed(true)

	publicUrl := a.cfg.Binance.WsURL + "/public/stream"
	a.wsPublicData = exchange.NewWSConnection(
		publicUrl,
		router.Handle,
		wsCfg.GetStaleTimeout(),
		wsCfg.GetPingInterval(),
		wsCfg.GetReconnectBaseBackoff(),
		wsCfg.GetReconnectMaxBackoff(),
		wsCfg.MaxReconnectFailures,
		nil,
	)
	a.wsPublicData.SetIdleWhenUnsubscribed(true)

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
			Leverage:  a.cfg.Trading.Leverage,
		})
		a.posMgr.SetMemoryRepo(memRepo)
		a.posMgr.SetMemoryEngine(a.memoryEngine)
		slog.Info("memory engine enabled",
			"embedding_model", a.cfg.Memory.EmbeddingModel,
			"top_similar", a.cfg.Memory.TopSimilar,
		)
	} else {
		a.memoryEngine = memory.NewEngine(memory.EngineConfig{Enabled: false})
	}

	frontrunInterval := time.Duration(a.cfg.LLM.FrontrunExecIntervalSecs) * time.Second
	intentHandler := execution.NewIntentHandler(a.execEng, a.riskEngine, a.indEngine, a.executor, a.cfg.Trading.Leverage, a.cfg.Scoring.MinScoreOverride)
	a.intentQueue = intent.NewQueue(intentHandler.Fire, frontrunInterval, a.cfg.LLM.FrontrunFireMinutes)

	a.sched = scheduler.NewScheduler(&a.cfg.Scheduler, cache, a.scanFn)
	a.sched.SetIntentQueue(a.intentQueue)

	// Phase 8: AfterTrigger — precision T+0 execution for AFTER intents.
	a.posMgr.SetFundingInfoGetter(a.engine)
	afterStrategy := execution.NewAfterExecutionStrategy(a.bookTickerCache, a.execEng, a.riskEngine, a.indEngine, notifier, a.cfg)
	a.afterTrigger = execution.NewAfterTrigger(
		a.intentQueue,
		afterStrategy,
		a.bookTickerCache,
		a.wsPublicData, // subscribe @bookTicker on the public stream
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
	go a.wsMarketData.Run(ctx)
	go a.wsPublicData.Run(ctx)
	go a.posMgr.Run(ctx)
	go a.sched.Run(ctx)
	go a.oiPoller(ctx, binanceClient)
	go a.windowMarketDataManager(ctx)
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
	a.posMgr.RunFundingAvoidance(ctx)

	slog.Info("bot started", "mode", a.cfg.App.Mode)

	<-ctx.Done()
	slog.Info("shutting down...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = shutdownCtx

	slog.Info("shutdown complete")
	return nil
}

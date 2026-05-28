package execution

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"futures/internal/config"
	"futures/internal/domain"
	"futures/internal/notify"
	"futures/internal/scheduler"
	"futures/internal/storage"
)

type MarketDataProvider interface {
	GetPrice(symbol string) (float64, bool)
	GetFundingRate(symbol string) (float64, bool)
}

type ExecutionEngine struct {
	executor  Executor
	market    MarketDataProvider
	cache     storage.StateCache
	tradeRepo storage.TradeRepository
	riskRepo  *storage.PGRiskRepository
	cfg       *config.Config
	notifier  *notify.Notifier
	posMgr    *PositionManager // used for paper mode immediate finalization
}

func NewExecutionEngine(
	exec Executor,
	market MarketDataProvider,
	cache storage.StateCache,
	tradeRepo storage.TradeRepository,
	riskRepo *storage.PGRiskRepository,
	cfg *config.Config,
	notifier *notify.Notifier,
) *ExecutionEngine {
	return &ExecutionEngine{
		executor:  exec,
		market:    market,
		cache:     cache,
		tradeRepo: tradeRepo,
		riskRepo:  riskRepo,
		cfg:       cfg,
		notifier:  notifier,
	}
}

// SetPositionManager wires the PositionManager so that paper mode can
// call finalizeSLTP immediately (paper fills are synchronous).
func (e *ExecutionEngine) SetPositionManager(pm *PositionManager) {
	e.posMgr = pm
}

// ExecuteScored is the Phase 2 entry point.
func (e *ExecutionEngine) ExecuteScored(ctx context.Context, sc *domain.ScoredCandidate, window scheduler.WindowType) error {
	return e.executeInternal(ctx, sc.Candidate, window, sc.PositionSizePct, int(sc.CompositeScore), nil)
}

// ExecuteScoredWithLLM is the Phase 3 entry point.
func (e *ExecutionEngine) ExecuteScoredWithLLM(ctx context.Context, sc *domain.ScoredCandidate, decision *domain.LLMDecision) error {
	window := entryModeToWindow(decision.EntryMode)
	return e.executeInternal(ctx, sc.Candidate, window, sc.PositionSizePct, decision.Confidence, decision)
}

func (e *ExecutionEngine) Execute(ctx context.Context, candidate domain.Candidate, window scheduler.WindowType) error {
	return e.executeInternal(ctx, candidate, window, e.cfg.Trading.PositionSizePct, 0, nil)
}

func entryModeToWindow(mode domain.EntryMode) scheduler.WindowType {
	switch mode {
	case domain.EntryModeFrontrun:
		return scheduler.WindowFrontrun
	case domain.EntryModeLastMinute:
		return scheduler.WindowLastMinute
	case domain.EntryModeAfter:
		return scheduler.WindowAfter
	default:
		return scheduler.WindowLastMinute
	}
}

func (e *ExecutionEngine) executeInternal(
	ctx context.Context,
	candidate domain.Candidate,
	window scheduler.WindowType,
	positionSizePct float64,
	confidence int,
	llmDecision *domain.LLMDecision,
) error {
	// Pre-checks
	killSwitch, err := e.cache.GetKillSwitch(ctx)
	if err != nil || killSwitch {
		return fmt.Errorf("kill switch active")
	}
	onCooldown, err := e.cache.IsOnCooldown(ctx)
	if err != nil || onCooldown {
		slog.Info("skipping trade: on cooldown", "symbol", candidate.Symbol)
		return nil
	}
	positions, err := e.cache.GetActivePositions(ctx)
	if err != nil {
		return err
	}
	if len(positions) >= e.cfg.Trading.MaxPositions {
		slog.Info("skipping trade: max positions reached", "symbol", candidate.Symbol)
		return nil
	}
	for _, p := range positions {
		if p.Symbol == candidate.Symbol {
			slog.Info("skipping trade: already in position", "symbol", candidate.Symbol)
			return nil
		}
	}

	lossCount, err := e.tradeRepo.GetDailyLossCount(ctx, time.Now().UTC())
	if err == nil && lossCount >= 2 {
		slog.Warn("daily loss limit reached, skipping", "symbol", candidate.Symbol)
		return nil
	}

	balance, err := e.executor.GetAccountBalance(ctx)
	if err != nil {
		return fmt.Errorf("get balance: %w", err)
	}
	if balance.AvailableBalance < 50 {
		return fmt.Errorf("insufficient balance: %.2f", balance.AvailableBalance)
	}
	balance.AvailableBalance = 100 // TODO: REMOVED

	margin := balance.AvailableBalance * positionSizePct / 100
	qty := margin * float64(e.cfg.Trading.Leverage) / candidate.MarkPrice

	if err := e.executor.SetLeverage(ctx, candidate.Symbol, e.cfg.Trading.Leverage); err != nil {
		slog.Warn("set leverage failed, continuing", "symbol", candidate.Symbol, "error", err)
	}

	order, err := e.executor.PlaceMarketOrder(ctx, domain.OrderRequest{
		Symbol:   candidate.Symbol,
		Side:     domain.SideSell,
		Type:     domain.OrderTypeMarket,
		Quantity: qty,
	})
	if err != nil {
		return fmt.Errorf("place order: %w", err)
	}

	slog.Info("market order placed, awaiting WS fill confirmation",
		"symbol", candidate.Symbol,
		"order_id", order.OrderID,
		"qty", order.Quantity,
	)

	now := time.Now()
	pending := domain.PendingEntry{
		OrderID:         order.OrderID,
		Symbol:          candidate.Symbol,
		Quantity:        order.Quantity,
		PositionSizePct: positionSizePct,
		Confidence:      confidence,
		Window:          string(window.ToEntryMode()),
		TpPct:           e.cfg.Execution.TpPct,
		LLMDecision:     llmDecision,
		CreatedAt:       now,
		ExpiresAt:       now.Add(60 * time.Second),
	}

	// Store funding-adjusted tpPct so finalizeSLTP can use it.
	if window == scheduler.WindowFrontrun || window == scheduler.WindowLastMinute {
		extra := -candidate.FundingRate // e.g. 0.002 for -0.2% funding
		pending.TpPct = e.cfg.Execution.TpPct + extra*100
	}

	if err := e.cache.SetPendingEntry(ctx, pending); err != nil {
		slog.Error("failed to store pending entry", "symbol", candidate.Symbol, "error", err)
		// Non-fatal: reconciler will catch this on next startup if WS misses the fill.
	}

	// Paper mode: executor fills synchronously — the user-data WS doesn't exist in paper mode.
	// Finalize SL/TP immediately using the order fill price.
	if e.cfg.App.Mode == "paper" && e.posMgr != nil {
		fillPrice := order.FillPrice
		if fillPrice == 0 {
			fillPrice = candidate.MarkPrice
		}
		e.posMgr.finalizeSLTP(ctx, &pending, fillPrice, order.Quantity)
	}

	return nil
}

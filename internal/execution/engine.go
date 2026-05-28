package execution

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

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

// ExecuteScored is the Phase 2 entry point: uses confidence-based position sizing from a ScoredCandidate.
func (e *ExecutionEngine) ExecuteScored(ctx context.Context, sc *domain.ScoredCandidate, window scheduler.WindowType) error {
	return e.executeInternal(ctx, sc.Candidate, window, sc.PositionSizePct, int(sc.CompositeScore))
}

// ExecuteScoredWithLLM is the Phase 3 entry point: LLM decision overrides TP strategy.
func (e *ExecutionEngine) ExecuteScoredWithLLM(ctx context.Context, sc *domain.ScoredCandidate, decision *domain.LLMDecision) error {
	window := entryModeToWindow(decision.EntryMode)
	return e.executeInternalLLM(ctx, sc.Candidate, window, sc.PositionSizePct, decision)
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

func (e *ExecutionEngine) Execute(ctx context.Context, candidate domain.Candidate, window scheduler.WindowType) error {
	return e.executeInternal(ctx, candidate, window, e.cfg.Trading.PositionSizePct, 0)
}

func (e *ExecutionEngine) executeInternalLLM(ctx context.Context, candidate domain.Candidate, window scheduler.WindowType, positionSizePct float64, decision *domain.LLMDecision) error {
	return e.executeInternalWithTP(ctx, candidate, window, positionSizePct, decision.Confidence, e.cfg.Execution.TpPct, decision)
}

func (e *ExecutionEngine) executeInternal(ctx context.Context, candidate domain.Candidate, window scheduler.WindowType, positionSizePct float64, confidence int) error {
	return e.executeInternalWithTP(ctx, candidate, window, positionSizePct, confidence, e.cfg.Execution.TpPct, nil)
}

func (e *ExecutionEngine) executeInternalWithTP(ctx context.Context, candidate domain.Candidate, window scheduler.WindowType, positionSizePct float64, confidence int, tpPct float64, llmDecision *domain.LLMDecision) error {
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

	// Daily loss check
	lossCount, err := e.tradeRepo.GetDailyLossCount(ctx, time.Now().UTC())
	if err == nil && lossCount >= 2 {
		slog.Warn("daily loss limit reached, skipping", "symbol", candidate.Symbol)
		return nil
	}

	// Get balance
	balance, err := e.executor.GetAccountBalance(ctx)
	if err != nil {
		return fmt.Errorf("get balance: %w", err)
	}
	if balance.AvailableBalance < 50 {
		return fmt.Errorf("insufficient balance: %.2f", balance.AvailableBalance)
	}
	balance.AvailableBalance = 100 // TODO: REMOVED

	// Calculate position size using the provided percentage (confidence-based in Phase 2)
	margin := balance.AvailableBalance * positionSizePct / 100
	qty := margin * float64(e.cfg.Trading.Leverage) / candidate.MarkPrice

	// Set leverage
	if err := e.executor.SetLeverage(ctx, candidate.Symbol, e.cfg.Trading.Leverage); err != nil {
		slog.Warn("set leverage failed, continuing", "symbol", candidate.Symbol, "error", err)
	}

	// Place order
	order, err := e.executor.PlaceMarketOrder(ctx, domain.OrderRequest{
		Symbol:   candidate.Symbol,
		Side:     domain.SideSell,
		Type:     domain.OrderTypeMarket,
		Quantity: qty,
	})
	if err != nil {
		return fmt.Errorf("place order: %w", err)
	}

	entryPrice := order.FillPrice
	if entryPrice == 0 {
		entryPrice = candidate.MarkPrice
	}

	// Calculate SL/TP prices from the actual avg fill price returned by Binance.
	slDistance := entryPrice * (e.cfg.Execution.SlPct / 100)
	stopLoss := entryPrice + slDistance // SHORT: SL above entry

	tpDistance := entryPrice * (tpPct / 100)
	if window == scheduler.WindowFrontrun || window == scheduler.WindowLastMinute {
		tpDistance += entryPrice * (-candidate.FundingRate)
	}
	takeProfit := entryPrice - tpDistance // SHORT: TP below entry

	// SL: 100% quantity — always the backstop
	slOrder, err := e.executor.PlaceStopMarketOrder(ctx, domain.OrderRequest{
		Symbol:     candidate.Symbol,
		Side:       domain.SideBuy,
		Type:       domain.OrderTypeStopMarket,
		Quantity:   order.Quantity,
		StopPrice:  stopLoss,
		ReduceOnly: true,
	})
	if err != nil {
		slog.Error("place SL order failed", "symbol", candidate.Symbol, "error", err)
	}

	// TP1: 50% quantity (partial take-profit — trailing handles the rest)
	tp1SizePct := e.cfg.Execution.TP1SizePct / 100
	tp1Qty := order.Quantity * tp1SizePct

	tpLimit := takeProfit * 0.99
	tpOrder, err := e.executor.PlaceStopLimitOrder(ctx, domain.OrderRequest{
		Symbol:     candidate.Symbol,
		Side:       domain.SideBuy,
		Type:       domain.OrderTypeTakeProfit,
		Quantity:   tp1Qty,
		StopPrice:  takeProfit,
		Price:      tpLimit,
		ReduceOnly: true,
	})
	if err != nil {
		slog.Error("place TP1 order failed", "symbol", candidate.Symbol, "error", err)
	}

	var slOrderID, tpOrderID string
	if slOrder != nil {
		slOrderID = slOrder.OrderID
	}
	if tpOrder != nil {
		tpOrderID = tpOrder.OrderID
	}

	tradeID := uuid.New()
	now := time.Now()

	pos := domain.Position{
		Symbol:             candidate.Symbol,
		Side:               domain.SideSell,
		EntryPrice:         entryPrice,
		Quantity:           order.Quantity,
		OriginalQty:        order.Quantity,
		Leverage:           e.cfg.Trading.Leverage,
		EntryMode:          domain.EntryMode(window.ToEntryMode()),
		StopLoss:           stopLoss,
		TakeProfit:         takeProfit,
		SLOrderID:          slOrderID,
		TPOrderID:          tpOrderID,
		TradeID:            tradeID,
		OpenedAt:           now,
		IsPaper:            e.cfg.App.Mode == "paper",
		HighSinceEntry:     entryPrice,
		LowSinceEntry:      entryPrice,
		OriginalConfidence: confidence,
	}
	if llmDecision != nil {
		pos.LLMEntryReasons = llmDecision.EntryReasons
		pos.OriginalConfidence = llmDecision.Confidence
	}

	trade := &domain.Trade{
		ID:          tradeID,
		Symbol:      candidate.Symbol,
		Side:        domain.SideSell,
		EntryMode:   domain.EntryMode(window.ToEntryMode()),
		Leverage:    e.cfg.Trading.Leverage,
		Confidence:  confidence,
		FundingRate: candidate.FundingRate,
		DailyROI:    candidate.DailyROI,
		EntryPrice:  entryPrice,
		IsPaper:     e.cfg.App.Mode == "paper",
		CreatedAt:   now,
	}

	if llmDecision != nil {
		conf := llmDecision.Confidence
		entryMode := string(llmDecision.EntryMode)
		trade.LLMConfidence = &conf
		trade.LLMEntryMode = &entryMode
		trade.LLMEntryReasons = llmDecision.EntryReasons
		trade.LLMWarnings = llmDecision.Warnings
	}

	if err := e.tradeRepo.Insert(ctx, trade); err != nil {
		slog.Error("insert trade record", "symbol", candidate.Symbol, "error", err)
	}
	if err := e.cache.SetActivePosition(ctx, pos); err != nil {
		slog.Error("set active position", "symbol", candidate.Symbol, "error", err)
	}

	slog.Info("position opened (split TP)",
		"symbol", candidate.Symbol,
		"entry", entryPrice,
		"sl", stopLoss,
		"sl_order", slOrderID,
		"tp1", takeProfit,
		"tp1_order", tpOrderID,
		"qty_total", order.Quantity,
		"qty_tp1", tp1Qty,
		"qty_trail", order.Quantity-tp1Qty,
		"window", window,
		"paper", e.cfg.App.Mode == "paper",
	)

	if e.notifier != nil {
		e.notifier.NotifyTradeOpened(ctx, notify.TradeOpenedEvent{
			Symbol:     candidate.Symbol,
			Side:       "SHORT",
			EntryPrice: entryPrice,
			Quantity:   order.Quantity,
			Leverage:   e.cfg.Trading.Leverage,
			StopLoss:   stopLoss,
			TakeProfit: takeProfit,
			EntryMode:  string(window.ToEntryMode()),
			Confidence: confidence,
			Score:      float64(confidence),
			IsPaper:    e.cfg.App.Mode == "paper",
		})
	}

	return nil
}

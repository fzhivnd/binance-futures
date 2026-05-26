package execution

import (
	"context"
	"log/slog"
	"math"
	"time"

	"github.com/google/uuid"

	"futures/internal/config"
	"futures/internal/domain"
	"futures/internal/storage"
)

type PositionManager struct {
	executor  Executor
	market    MarketDataProvider
	cache     storage.StateCache
	tradeRepo storage.TradeRepository
	riskRepo  *storage.PGRiskRepository
	cfg       *config.Config
}

func NewPositionManager(
	exec Executor,
	market MarketDataProvider,
	cache storage.StateCache,
	tradeRepo storage.TradeRepository,
	riskRepo *storage.PGRiskRepository,
	cfg *config.Config,
) *PositionManager {
	return &PositionManager{
		executor:  exec,
		market:    market,
		cache:     cache,
		tradeRepo: tradeRepo,
		riskRepo:  riskRepo,
		cfg:       cfg,
	}
}

func (m *PositionManager) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.checkAll(ctx)
		}
	}
}

func (m *PositionManager) checkAll(ctx context.Context) {
	positions, err := m.cache.GetActivePositions(ctx)
	if err != nil {
		slog.Error("get active positions", "error", err)
		return
	}
	for _, pos := range positions {
		m.check(ctx, pos)
	}
}

func (m *PositionManager) check(ctx context.Context, pos domain.Position) {
	// Detect exchange-side fill: SL or TP order was triggered
	exchangePos, err := m.executor.GetPosition(ctx, pos.Symbol)
	if err != nil {
		slog.Warn("get exchange position failed", "symbol", pos.Symbol, "error", err)
	} else if exchangePos == nil {
		// Position is closed on the exchange — record the result
		exitPrice, _ := m.market.GetPrice(pos.Symbol)
		result := "WIN"
		if pos.Side == domain.SideSell && exitPrice >= pos.EntryPrice {
			result = "LOSS"
		} else if pos.Side == domain.SideBuy && exitPrice <= pos.EntryPrice {
			result = "LOSS"
		}
		m.recordClose(ctx, pos, exitPrice, result)
		return
	}

	if pos.BreakevenMoved {
		return
	}

	currentPrice, ok := m.market.GetPrice(pos.Symbol)
	if !ok || currentPrice == 0 {
		return
	}

	// For SHORT: profit when price falls below entry
	unrealizedPnlPct := (pos.EntryPrice - currentPrice) / pos.EntryPrice * float64(pos.Leverage) * 100

	// Breakeven: cancel the resting SL order and replace it at entry price.
	// Requires the profit threshold AND at least 5 minutes since open to avoid
	// reacting to the initial spike on entry fill.
	if unrealizedPnlPct > m.cfg.Execution.BreakevenActivationPct &&
		time.Since(pos.OpenedAt) >= 5*time.Minute {
		if pos.SLOrderID != "" {
			if err := m.executor.CancelOrder(ctx, pos.Symbol, pos.SLOrderID); err != nil {
				slog.Warn("cancel SL for breakeven failed", "symbol", pos.Symbol, "error", err)
			}
		}
		breakevenLimit := pos.EntryPrice * 1.001
		newSL, err := m.executor.PlaceStopLimitOrder(ctx, domain.OrderRequest{
			Symbol:     pos.Symbol,
			Side:       domain.SideBuy,
			Type:       domain.OrderTypeStop,
			Quantity:   pos.Quantity,
			StopPrice:  pos.EntryPrice,
			Price:      breakevenLimit,
			ReduceOnly: true,
		})
		if err != nil {
			slog.Error("replace SL at breakeven failed", "symbol", pos.Symbol, "error", err)
			return
		}
		pos.StopLoss = pos.EntryPrice
		pos.BreakevenMoved = true
		if newSL != nil {
			pos.SLOrderID = newSL.OrderID
		}
		if err := m.cache.SetActivePosition(ctx, pos); err != nil {
			slog.Error("update position breakeven", "symbol", pos.Symbol, "error", err)
		}
		slog.Info("moved SL to breakeven", "symbol", pos.Symbol, "entry", pos.EntryPrice, "sl_order", pos.SLOrderID)
	}
}

func (m *PositionManager) recordClose(ctx context.Context, pos domain.Position, exitPrice float64, result string) {
	// Cancel the surviving leg (whichever order didn't trigger)
	if pos.SLOrderID != "" {
		if err := m.executor.CancelOrder(ctx, pos.Symbol, pos.SLOrderID); err != nil {
			slog.Warn("cancel residual SL order", "symbol", pos.Symbol, "error", err)
		}
	}
	if pos.TPOrderID != "" {
		if err := m.executor.CancelOrder(ctx, pos.Symbol, pos.TPOrderID); err != nil {
			slog.Warn("cancel residual TP order", "symbol", pos.Symbol, "error", err)
		}
	}

	pnl := (pos.EntryPrice - exitPrice) * pos.Quantity
	if pos.Side == domain.SideBuy {
		pnl = (exitPrice - pos.EntryPrice) * pos.Quantity
	}
	pnl = math.Round(pnl*1e8) / 1e8

	now := time.Now()
	id := pos.TradeID
	if id == uuid.Nil {
		slog.Warn("recordClose: position has no TradeID, result not persisted", "symbol", pos.Symbol)
	} else {
		if err := m.tradeRepo.UpdateResult(ctx, id, domain.ExitInfo{
			ExitPrice: exitPrice,
			PnL:       pnl,
			Result:    result,
			ClosedAt:  now,
		}); err != nil {
			slog.Error("update trade result", "symbol", pos.Symbol, "error", err)
		}
	}

	if err := m.cache.RemovePosition(ctx, pos.Symbol); err != nil {
		slog.Error("remove position from cache", "symbol", pos.Symbol, "error", err)
	}

	if result == "LOSS" {
		cooldown := time.Duration(m.cfg.Trading.CooldownMinutes) * time.Minute
		_ = m.cache.SetCooldown(ctx, cooldown)
		slog.Info("cooldown set after loss", "symbol", pos.Symbol, "duration", cooldown)
	}

	slog.Info("position closed", "symbol", pos.Symbol, "result", result,
		"entry", pos.EntryPrice, "exit", exitPrice, "pnl", pnl)
}


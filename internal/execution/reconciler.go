package execution

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/google/uuid"

	"futures/internal/config"
	"futures/internal/domain"
	"futures/internal/exchange"
	"futures/internal/notify"
	"futures/internal/storage"
)

// Reconcile runs on startup to sync Binance state with the local Redis cache.
// It handles three cases:
//  1. Position on Binance but not in cache → re-hydrate, place missing SL/TP
//  2. Position in cache but not on Binance → clean up cache, mark trade MANUAL
//  3. Position in both → verify SL/TP order IDs still live, re-place missing ones
//
// Reconcile is non-fatal: a failure logs a warning and returns the error so the
// caller can decide whether to continue or abort.
func Reconcile(
	ctx context.Context,
	client *exchange.BinanceClient,
	executor Executor,
	cache storage.StateCache,
	tradeRepo storage.TradeRepository,
	cfg *config.Config,
	notifier *notify.Notifier,
) error {
	// Paper mode has no real Binance positions to reconcile.
	if cfg.App.Mode == "paper" {
		return nil
	}

	slog.Info("reconcile: starting startup position reconciliation")

	binancePositions, err := client.GetAllPositionRisk(ctx)
	if err != nil {
		return fmt.Errorf("reconcile: get position risk: %w", err)
	}

	openOrders, err := client.GetOpenOrders(ctx)
	if err != nil {
		return fmt.Errorf("reconcile: get open orders: %w", err)
	}

	// Index open orders by symbol for O(1) lookup.
	ordersBySymbol := make(map[string][]exchange.OpenOrderResponse)
	for _, o := range openOrders {
		ordersBySymbol[o.Symbol] = append(ordersBySymbol[o.Symbol], o)
	}

	// Index Binance positions (non-zero positionAmt) by symbol.
	binanceBySymbol := make(map[string]exchange.PositionRiskResponse)
	for _, p := range binancePositions {
		if p.PositionAmt != 0 {
			binanceBySymbol[p.Symbol] = p
		}
	}

	cachedPositions, err := cache.GetActivePositions(ctx)
	if err != nil {
		return fmt.Errorf("reconcile: get cached positions: %w", err)
	}

	cachedBySymbol := make(map[string]domain.Position)
	for _, p := range cachedPositions {
		cachedBySymbol[p.Symbol] = p
	}

	reconciled := 0

	// Case 1 & 3: positions on Binance.
	for sym, bp := range binanceBySymbol {
		cached, inCache := cachedBySymbol[sym]

		if !inCache {
			// Case 1: orphan on Binance — re-hydrate.
			slog.Warn("reconcile: orphaned position found on Binance, re-hydrating",
				"symbol", sym,
				"position_amt", bp.PositionAmt,
				"entry_price", bp.EntryPrice,
			)
			if err := rehydratePosition(ctx, sym, bp, ordersBySymbol[sym], executor, cache, tradeRepo, cfg, notifier); err != nil {
				slog.Error("reconcile: rehydrate failed", "symbol", sym, "error", err)
			} else {
				reconciled++
			}
			continue
		}

		// Case 3: in both — verify order IDs still live and prices are correct.
		orders := ordersBySymbol[sym]
		needsSL, needsTP := verifyProtection(cached, orders)
		if needsSL || needsTP {
			slog.Warn("reconcile: position needs order correction, re-placing",
				"symbol", sym, "needs_sl", needsSL, "needs_tp", needsTP)
			if err := replaceMissingOrders(ctx, sym, cached, needsSL, needsTP, orders, executor, cache); err != nil {
				slog.Error("reconcile: replace orders failed", "symbol", sym, "error", err)
			} else {
				reconciled++
			}
		}
	}

	// Case 2: positions in cache but not on Binance.
	for sym, cached := range cachedBySymbol {
		if _, onBinance := binanceBySymbol[sym]; !onBinance {
			slog.Warn("reconcile: cached position not on Binance, cleaning up", "symbol", sym)
			if err := cleanupStalePosition(ctx, sym, cached, cache, tradeRepo); err != nil {
				slog.Error("reconcile: cleanup failed", "symbol", sym, "error", err)
			} else {
				reconciled++
			}
		}
	}

	slog.Info("reconcile: complete", "actions", reconciled)
	return nil
}

// rehydratePosition rebuilds a Position and Trade from Binance data and places
// any SL/TP orders that are missing.
func rehydratePosition(
	ctx context.Context,
	symbol string,
	bp exchange.PositionRiskResponse,
	liveOrders []exchange.OpenOrderResponse,
	executor Executor,
	cache storage.StateCache,
	tradeRepo storage.TradeRepository,
	cfg *config.Config,
	notifier *notify.Notifier,
) error {
	qty := -bp.PositionAmt // positionAmt is negative for shorts
	if qty <= 0 {
		return fmt.Errorf("unexpected positionAmt sign: %v", bp.PositionAmt)
	}

	slDistance := bp.EntryPrice * (cfg.Execution.SlPct / 100)
	stopLoss := bp.EntryPrice + slDistance
	tpDistance := bp.EntryPrice * (cfg.Execution.TpPct / 100)
	takeProfit := bp.EntryPrice - tpDistance

	// Check if SL/TP already exist among live orders.
	var slOrderID, tpOrderID string
	for _, o := range liveOrders {
		if o.ReduceOnly && o.Side == "BUY" {
			switch o.Type {
			case "STOP_MARKET":
				slOrderID = o.ClientOrderId
			case "MARKET":
				slOrderID = o.ClientOrderId
			case "TAKE_PROFIT":
				tpOrderID = o.ClientOrderId
			case "LIMIT":
				slOrderID = o.ClientOrderId
			}
		}
	}

	// Place missing SL.
	if slOrderID == "" {
		slOrder, err := executor.PlaceStopMarketOrder(ctx, domain.OrderRequest{
			Symbol:        symbol,
			Side:          domain.SideBuy,
			Type:          domain.OrderTypeStopMarket,
			TriggerPrice:  stopLoss,
			ClosePosition: true,
		})
		if err != nil {
			slog.Error("rehydrate: place SL failed", "symbol", symbol, "error", err)
		} else if slOrder != nil {
			slOrderID = slOrder.ClientOrderId
		}
	}

	// Place missing TP1.
	tp1Qty := qty * (cfg.Execution.TP1SizePct / 100)
	if tpOrderID == "" {
		tpOrder, err := executor.PlaceStopLimitOrder(ctx, domain.OrderRequest{
			Symbol:       symbol,
			Side:         domain.SideBuy,
			Type:         domain.OrderTypeTakeProfit,
			Quantity:     tp1Qty,
			Price:        takeProfit,
			TriggerPrice: takeProfit * 1.001,
			ReduceOnly:   true,
		})
		if err != nil {
			slog.Error("rehydrate: place TP1 failed", "symbol", symbol, "error", err)
		} else if tpOrder != nil {
			tpOrderID = tpOrder.ClientOrderId
		}
	}

	now := time.Now()
	tradeID := uuid.New()
	isPaper := cfg.App.Mode == "paper"

	pos := domain.Position{
		Symbol:         symbol,
		Side:           domain.SideSell,
		EntryPrice:     bp.EntryPrice,
		Quantity:       qty,
		OriginalQty:    qty,
		Leverage:       cfg.Trading.Leverage,
		EntryMode:      domain.EntryModeLastMinute,
		StopLoss:       stopLoss,
		TakeProfit:     takeProfit,
		SLOrderID:      slOrderID,
		TPOrderID:      tpOrderID,
		TradeID:        tradeID,
		OpenedAt:       now,
		IsPaper:        isPaper,
		HighSinceEntry: bp.EntryPrice,
		LowSinceEntry:  bp.EntryPrice,
	}
	if err := cache.SetActivePosition(ctx, pos); err != nil {
		return fmt.Errorf("rehydrate: set position: %w", err)
	}

	trade := &domain.Trade{
		ID:         tradeID,
		Symbol:     symbol,
		Side:       domain.SideSell,
		EntryMode:  domain.EntryModeLastMinute,
		Leverage:   cfg.Trading.Leverage,
		EntryPrice: bp.EntryPrice,
		IsPaper:    isPaper,
		CreatedAt:  now,
	}
	if err := tradeRepo.Insert(ctx, trade); err != nil {
		slog.Error("rehydrate: insert trade", "symbol", symbol, "error", err)
	}

	slog.Info("reconcile: position rehydrated",
		"symbol", symbol, "sl", stopLoss, "tp", takeProfit)

	if notifier != nil {
		notifier.NotifyRiskEvent(ctx, notify.RiskEvent{
			Type:    "cooldown",
			Message: fmt.Sprintf("Startup reconciliation: re-hydrated orphaned position for %s at %.8g", symbol, bp.EntryPrice),
		})
	}
	return nil
}

// priceTolerancePct is the maximum allowed deviation between the cached SL/TP
// price and the live order stop price before we consider it stale and re-place.
const priceTolerancePct = 0.001 // 0.1%

// verifyProtection checks whether the cached SL/TP order IDs still exist in
// the live orders returned by Binance, and that their prices haven't drifted.
// Returns (needsSL, needsTP).
func verifyProtection(pos domain.Position, liveOrders []exchange.OpenOrderResponse) (bool, bool) {
	liveByID := make(map[string]exchange.OpenOrderResponse, len(liveOrders))
	for _, o := range liveOrders {
		liveByID[fmt.Sprintf("%d", o.OrderID)] = o
	}

	needsSL := false
	needsTP := false

	if pos.SLOrderID == "" {
		needsSL = true
	} else if o, exists := liveByID[pos.SLOrderID]; !exists {
		needsSL = true
	} else if pos.StopLoss > 0 && o.StopPrice > 0 {
		drift := math.Abs(o.StopPrice-pos.StopLoss) / pos.StopLoss
		if drift > priceTolerancePct {
			slog.Warn("reconcile: SL price drifted, will re-place",
				"symbol", pos.Symbol,
				"cached_sl", pos.StopLoss,
				"live_sl", o.StopPrice,
				"drift_pct", drift*100,
			)
			needsSL = true
		}
	}

	if pos.TPOrderID == "" {
		needsTP = true
	} else if o, exists := liveByID[pos.TPOrderID]; !exists {
		needsTP = true
	} else if pos.TakeProfit > 0 && o.StopPrice > 0 {
		drift := math.Abs(o.StopPrice-pos.TakeProfit) / pos.TakeProfit
		if drift > priceTolerancePct {
			slog.Warn("reconcile: TP price drifted, will re-place",
				"symbol", pos.Symbol,
				"cached_tp", pos.TakeProfit,
				"live_tp", o.StopPrice,
				"drift_pct", drift*100,
			)
			needsTP = true
		}
	}

	return needsSL, needsTP
}

// replaceMissingOrders cancels any stale SL/TP orders and re-places correct ones.
// liveOrders is used to cancel existing orders whose prices have drifted.
func replaceMissingOrders(
	ctx context.Context,
	symbol string,
	pos domain.Position,
	needsSL, needsTP bool,
	liveOrders []exchange.OpenOrderResponse,
	executor Executor,
	cache storage.StateCache,
) error {
	// Cancel any existing stale order before re-placing (avoids duplicate orders).
	liveByID := make(map[string]bool, len(liveOrders))
	for _, o := range liveOrders {
		liveByID[fmt.Sprintf("%d", o.OrderID)] = true
	}

	if needsSL {
		if pos.SLOrderID != "" && liveByID[pos.SLOrderID] {
			if err := executor.CancelOrder(ctx, symbol, pos.SLOrderID); err != nil {
				slog.Warn("replaceMissingOrders: cancel stale SL failed", "symbol", symbol, "error", err)
			}
		}
		slOrder, err := executor.PlaceStopMarketOrder(ctx, domain.OrderRequest{
			Symbol:        symbol,
			Side:          domain.SideBuy,
			Type:          domain.OrderTypeStopMarket,
			TriggerPrice:  pos.StopLoss,
			ClosePosition: true,
		})
		if err != nil {
			return fmt.Errorf("replace SL: %w", err)
		}
		if slOrder != nil {
			pos.SLOrderID = slOrder.ClientOrderId
		}
	}

	if needsTP {
		if pos.TPOrderID != "" && liveByID[pos.TPOrderID] {
			if err := executor.CancelOrder(ctx, symbol, pos.TPOrderID); err != nil {
				slog.Warn("replaceMissingOrders: cancel stale TP failed", "symbol", symbol, "error", err)
			}
		}
		tp1Qty := pos.OriginalQty * tp1SizeFraction
		tpOrder, err := executor.PlaceStopLimitOrder(ctx, domain.OrderRequest{
			Symbol:       symbol,
			Side:         domain.SideBuy,
			Type:         domain.OrderTypeTakeProfit,
			Quantity:     tp1Qty,
			Price:        pos.TakeProfit,
			TriggerPrice: pos.TakeProfit * 1.001,
			ReduceOnly:   true,
		})
		if err != nil {
			return fmt.Errorf("replace TP1: %w", err)
		}
		if tpOrder != nil {
			pos.TPOrderID = tpOrder.ClientOrderId
		}
	}

	return cache.SetActivePosition(ctx, pos)
}

// cleanupStalePosition removes a cached position that no longer exists on Binance.
func cleanupStalePosition(
	ctx context.Context,
	symbol string,
	pos domain.Position,
	cache storage.StateCache,
	tradeRepo storage.TradeRepository,
) error {
	if err := cache.RemovePosition(ctx, symbol); err != nil {
		return fmt.Errorf("remove position: %w", err)
	}
	if pos.TradeID != uuid.Nil {
		now := time.Now()
		_ = tradeRepo.UpdateResult(ctx, pos.TradeID, domain.ExitInfo{
			AvgClosePrice: pos.EntryPrice, // best guess — no fill data
			PnL:           0,
			Result:        "MANUAL",
			CloseReason:   "MANUAL",
			ClosedAt:      now,
		})
	}
	slog.Info("reconcile: stale cache position removed", "symbol", symbol)
	return nil
}

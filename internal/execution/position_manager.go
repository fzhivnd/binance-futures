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
	"futures/internal/indicator"
	"futures/internal/llm"
	"futures/internal/notify"
	"futures/internal/storage"
)

// tp1SizePct is the fraction of the original quantity closed by TP1.
// Mirrors cfg.Execution.TP1SizePct / 100 but kept as a local constant for
// use in pure-math helpers that have no config reference.
const tp1SizeFraction = 0.5

type PositionManager struct {
	executor      Executor
	market        MarketDataProvider
	cache         storage.StateCache
	tradeRepo     storage.TradeRepository
	memoryRepo    storage.MemoryRepository
	riskRepo      *storage.PGRiskRepository
	cfg           *config.Config
	forceSLEngine *llm.ForceSLEngine
	indEngine     *indicator.Engine
	notifier      *notify.Notifier
}

func NewPositionManager(
	exec Executor,
	market MarketDataProvider,
	cache storage.StateCache,
	tradeRepo storage.TradeRepository,
	riskRepo *storage.PGRiskRepository,
	cfg *config.Config,
	notifier *notify.Notifier,
) *PositionManager {
	return &PositionManager{
		executor:  exec,
		market:    market,
		cache:     cache,
		tradeRepo: tradeRepo,
		riskRepo:  riskRepo,
		cfg:       cfg,
		notifier:  notifier,
	}
}

// SetForceSLEngine attaches the force-SL LLM engine (called from app wiring).
func (m *PositionManager) SetForceSLEngine(engine *llm.ForceSLEngine) {
	m.forceSLEngine = engine
}

// SetIndicatorEngine attaches the indicator engine for BTC context in force-SL checks.
func (m *PositionManager) SetIndicatorEngine(engine *indicator.Engine) {
	m.indEngine = engine
}

// SetMemoryRepo attaches the memory repository for recording trade outcomes.
func (m *PositionManager) SetMemoryRepo(repo storage.MemoryRepository) {
	m.memoryRepo = repo
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
	currentPrice, ok := m.market.GetPrice(pos.Symbol)
	if !ok || currentPrice == 0 {
		return
	}

	updated := false

	// Track high/low since entry for force-SL context
	if currentPrice > pos.HighSinceEntry {
		pos.HighSinceEntry = currentPrice
		updated = true
	}
	if pos.LowSinceEntry == 0 || currentPrice < pos.LowSinceEntry {
		pos.LowSinceEntry = currentPrice
		updated = true
	}

	// For SHORT: profit when price falls below entry (raw price PnL%, not leveraged)
	rawPnlPct := (pos.EntryPrice - currentPrice) / pos.EntryPrice * 100

	// === FORCE-SL CHECK (Phase 5) ===
	// Only run if: engine present, feature enabled, TP1 not yet filled (trailing takes over after),
	// and position not already at breakeven.
	if m.forceSLEngine != nil &&
		m.cfg.Execution.ForceSLEnabled &&
		!pos.TP1Filled &&
		m.shouldCheckForceSL(pos, rawPnlPct) {

		decision := m.checkForceSL(ctx, pos, currentPrice, rawPnlPct)
		if decision != nil && decision.Action == "FORCE_CLOSE" {
			m.forceClose(ctx, pos, currentPrice, decision.Reason)
			return
		}
		pos.LastForceSLCheck = time.Now()
		pos.ForceSLCount++
		updated = true
	}

	// === BREAKEVEN LOGIC (only applies before TP1 fills) ===
	if !pos.BreakevenMoved && !pos.TP1Filled {
		leveragedPnl := rawPnlPct * float64(pos.Leverage)
		if leveragedPnl > m.cfg.Execution.BreakevenActivationPct &&
			time.Since(pos.OpenedAt) >= 5*time.Minute {
			if pos.SLOrderID != "" {
				if err := m.executor.CancelOrder(ctx, pos.Symbol, pos.SLOrderID); err != nil {
					slog.Warn("cancel SL for breakeven failed", "symbol", pos.Symbol, "error", err)
				}
			}
			newSL, err := m.executor.PlaceStopMarketOrder(ctx, domain.OrderRequest{
				Symbol:     pos.Symbol,
				Side:       domain.SideBuy,
				Type:       domain.OrderTypeStopMarket,
				Quantity:   pos.Quantity,
				StopPrice:  pos.EntryPrice,
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
			updated = true
			slog.Info("moved SL to breakeven", "symbol", pos.Symbol, "entry", pos.EntryPrice)
		}
	}

	if updated {
		if err := m.cache.SetActivePosition(ctx, pos); err != nil {
			slog.Error("update position state", "symbol", pos.Symbol, "error", err)
		}
	}
}

// shouldCheckForceSL implements the PnL-gated + escalating interval logic.
func (m *PositionManager) shouldCheckForceSL(pos domain.Position, rawPnlPct float64) bool {
	startMin := time.Duration(m.cfg.Execution.ForceSLStartMin) * time.Minute
	if time.Since(pos.OpenedAt) < startMin {
		return false
	}

	gatePct := m.cfg.Execution.ForceSLPnlGatePct // e.g. -0.5
	if rawPnlPct >= gatePct {
		return false // winning or barely negative — no check needed
	}

	escalatePct := m.cfg.Execution.ForceSLEscalatePnlPct // e.g. -2.0
	var intervalSec int
	if rawPnlPct < escalatePct {
		intervalSec = m.cfg.Execution.ForceSLFastIntervalSec // every 1min when severe
	} else {
		intervalSec = m.cfg.Execution.ForceSLSlowIntervalSec // every 5min when mild
	}

	return time.Since(pos.LastForceSLCheck) >= time.Duration(intervalSec)*time.Second
}

func (m *PositionManager) checkForceSL(
	ctx context.Context,
	pos domain.Position,
	currentPrice float64,
	rawPnlPct float64,
) *llm.ForceSLResponse {
	btcContext := llm.LLMBTCContext{}
	if m.indEngine != nil {
		if btc, err := m.indEngine.ComputeBTCContext(ctx); err == nil && btc != nil {
			btcContext = llm.LLMBTCContext{
				Trend:         btc.Trend,
				MomentumScore: btc.MomentumScore,
				Volatility:    btc.Volatility,
				IsBreakout:    btc.IsBreakout,
				RSI:           btc.RSI14_1h,
				PriceChange1h: btc.PriceChange1h,
			}
		}
	}

	highPct := 0.0
	if pos.EntryPrice > 0 {
		highPct = (pos.HighSinceEntry - pos.EntryPrice) / pos.EntryPrice * 100
	}
	lowPct := 0.0
	if pos.EntryPrice > 0 && pos.LowSinceEntry > 0 {
		lowPct = (pos.EntryPrice - pos.LowSinceEntry) / pos.EntryPrice * 100
	}

	slDistancePct := 0.0
	if currentPrice > 0 && pos.StopLoss > 0 {
		slDistancePct = (pos.StopLoss - currentPrice) / currentPrice * 100
	}

	req := &llm.ForceSLRequest{
		Symbol:             pos.Symbol,
		EntryPrice:         pos.EntryPrice,
		CurrentPrice:       currentPrice,
		UnrealizedPnlPct:   rawPnlPct * float64(pos.Leverage),
		HoldMinutes:        int(time.Since(pos.OpenedAt).Minutes()),
		HardSLPrice:        pos.StopLoss,
		HardSLDistancePct:  slDistancePct,
		EntryMode:          string(pos.EntryMode),
		OriginalConfidence: pos.OriginalConfidence,
		EntryReasons:       pos.LLMEntryReasons,
		PriceAction: llm.ForceSLPriceAction{
			HighSinceEntry:   highPct,
			LowSinceEntry:    lowPct,
			CurrentTrend5m:   "sideways", // best-effort; real trend requires kline data
			MomentumShift:    highPct > 1.5 && btcContext.IsBreakout,
			VolumeIncreasing: btcContext.MomentumScore > 60,
		},
		BTCContext: btcContext,
	}

	resp, err := m.forceSLEngine.EvaluatePosition(ctx, req, m.cfg.Execution.ForceSLTimeoutSec)
	if err != nil {
		return nil
	}
	return resp
}

func (m *PositionManager) forceClose(ctx context.Context, pos domain.Position, exitPrice float64, reason string) {
	qtyToClose := pos.Quantity

	_, err := m.executor.PlaceMarketOrder(ctx, domain.OrderRequest{
		Symbol:     pos.Symbol,
		Side:       domain.SideBuy,
		Type:       domain.OrderTypeMarket,
		Quantity:   qtyToClose,
		ReduceOnly: true,
	})
	if err != nil {
		slog.Error("force-SL market close failed", "symbol", pos.Symbol, "error", err)
		return
	}

	m.cancelAllOrders(ctx, pos)

	pnl := (pos.EntryPrice - exitPrice) * pos.OriginalQty
	pnl = math.Round(pnl*1e8) / 1e8

	m.persistClose(ctx, pos, exitPrice, pnl, "FORCE_SL", "FORCE_SL")

	slog.Info("force_sl_executed",
		"symbol", pos.Symbol,
		"entry", pos.EntryPrice,
		"exit", exitPrice,
		"reason", reason,
		"hold_minutes", int(time.Since(pos.OpenedAt).Minutes()),
		"pnl", pnl,
	)
}

func (m *PositionManager) cancelAllOrders(ctx context.Context, pos domain.Position) {
	if pos.SLOrderID != "" {
		_ = m.executor.CancelOrder(ctx, pos.Symbol, pos.SLOrderID)
	}
	if pos.TPOrderID != "" {
		_ = m.executor.CancelOrder(ctx, pos.Symbol, pos.TPOrderID)
	}
	if pos.TrailingOrderID != "" {
		_ = m.executor.CancelOrder(ctx, pos.Symbol, pos.TrailingOrderID)
	}
	if pos.TrailingSLOrderID != "" {
		_ = m.executor.CancelOrder(ctx, pos.Symbol, pos.TrailingSLOrderID)
	}
}

// HandleUserDataEvent is called by the user data stream router on every ORDER_TRADE_UPDATE.
func (m *PositionManager) HandleUserDataEvent(event exchange.UserDataEvent) {
	o := event.Order
	if o.OrderStatus != "FILLED" {
		return
	}

	switch o.OrderType {
	case string(domain.OrderTypeStopMarket),
		string(domain.OrderTypeTakeProfit),
		string(domain.OrderTypeLimit),
		string(domain.OrderTypeTrailingStop):
		// expected close order types — proceed
	default:
		if o.ReduceOnly {
			slog.Warn("unexpected reduce-only fill ignored",
				"symbol", o.Symbol,
				"order_type", o.OrderType,
				"order_id", o.OrderID,
			)
		}
		return
	}

	ctx := context.Background()
	positions, err := m.cache.GetActivePositions(ctx)
	if err != nil {
		slog.Error("HandleUserDataEvent: get active positions", "error", err)
		return
	}

	var pos *domain.Position
	for i := range positions {
		if positions[i].Symbol == o.Symbol {
			pos = &positions[i]
			break
		}
	}
	if pos == nil {
		return
	}

	orderIDStr := orderIDToString(o.OrderID)

	switch orderIDStr {
	case pos.TPOrderID:
		if !pos.TP1Filled {
			m.handleTP1Fill(ctx, pos, o)
		}
	case pos.TrailingOrderID:
		m.handleTrailingFill(ctx, pos, o)
	case pos.TrailingSLOrderID:
		m.handleTrailingSLFill(ctx, pos, o)
	case pos.SLOrderID:
		m.handleHardSLFill(ctx, pos, o)
	default:
		// Not one of our tracked orders — could be a manual close.
		// Detect by checking if the position is now flat.
		if o.ReduceOnly {
			slog.Info("untracked reduce-only fill, treating as manual close",
				"symbol", o.Symbol, "order_id", o.OrderID)
			m.recordClose(ctx, *pos, o.AvgPrice, "MANUAL", "MANUAL")
		}
	}
}

func orderIDToString(id int64) string {
	if id == 0 {
		return ""
	}
	return fmt.Sprintf("%d", id)
}

// handleTP1Fill fires when TP1 (50% qty) fills. Places trailing stop + breakeven SL on the remainder.
func (m *PositionManager) handleTP1Fill(ctx context.Context, pos *domain.Position, o exchange.OrderTradeUpdate) {
	now := time.Now()
	pos.TP1Filled = true
	pos.TP1FillPrice = o.AvgPrice
	pos.TP1FilledAt = &now

	tp1Qty := o.FilledQty
	if tp1Qty == 0 {
		tp1Qty = pos.OriginalQty * tp1SizeFraction
	}
	remainingQty := pos.OriginalQty - tp1Qty

	slog.Info("tp1_filled",
		"symbol", pos.Symbol,
		"fill_price", o.AvgPrice,
		"filled_qty", tp1Qty,
		"remaining_qty", remainingQty,
	)

	// 1. Cancel old hard SL (was for 100% qty)
	if pos.SLOrderID != "" {
		if err := m.executor.CancelOrder(ctx, pos.Symbol, pos.SLOrderID); err != nil {
			slog.Warn("cancel old SL after TP1 failed", "symbol", pos.Symbol, "error", err)
		}
		pos.SLOrderID = ""
	}

	// 2. Place trailing stop on remaining qty
	trailingOrder, err := m.executor.PlaceTrailingStopOrder(ctx, TrailingStopRequest{
		Symbol:       pos.Symbol,
		Side:         domain.SideBuy,
		Quantity:     remainingQty,
		CallbackRate: m.cfg.Execution.TrailingCallbackRate,
		ReduceOnly:   true,
	})
	if err != nil {
		slog.Error("place trailing stop failed, market closing remainder",
			"symbol", pos.Symbol, "error", err)
		m.forceClose(ctx, *pos, o.AvgPrice, "trailing placement failed")
		return
	}
	if trailingOrder != nil {
		pos.TrailingOrderID = trailingOrder.OrderID
	}

	// 3. Place breakeven SL for the trailing portion (entry price as safety net)
	beOrder, err := m.executor.PlaceStopMarketOrder(ctx, domain.OrderRequest{
		Symbol:     pos.Symbol,
		Side:       domain.SideBuy,
		Type:       domain.OrderTypeStopMarket,
		Quantity:   remainingQty,
		StopPrice:  pos.EntryPrice,
		ReduceOnly: true,
	})
	if err != nil {
		slog.Warn("place breakeven SL for trailing failed", "symbol", pos.Symbol, "error", err)
	} else if beOrder != nil {
		pos.TrailingSLOrderID = beOrder.OrderID
	}

	pos.Quantity = remainingQty

	if err := m.cache.SetActivePosition(ctx, *pos); err != nil {
		slog.Error("update position after TP1 fill", "symbol", pos.Symbol, "error", err)
	}

	slog.Info("trailing_stop_placed",
		"symbol", pos.Symbol,
		"trailing_order", pos.TrailingOrderID,
		"callback_rate", m.cfg.Execution.TrailingCallbackRate,
		"remaining_qty", remainingQty,
	)
}

// handleTrailingFill fires when the Binance trailing stop fills (best case: price kept falling).
func (m *PositionManager) handleTrailingFill(ctx context.Context, pos *domain.Position, o exchange.OrderTradeUpdate) {
	if pos.TrailingSLOrderID != "" {
		_ = m.executor.CancelOrder(ctx, pos.Symbol, pos.TrailingSLOrderID)
	}
	avgClose := computeAvgClosePrice(*pos, o.AvgPrice)
	pnl := (pos.EntryPrice - avgClose) * pos.OriginalQty
	pnl = math.Round(pnl*1e8) / 1e8
	m.persistClose(ctx, *pos, avgClose, pnl, "WIN", "TP_TRAIL")
}

// handleTrailingSLFill fires when the breakeven SL on the trailing portion triggers (price reversed).
func (m *PositionManager) handleTrailingSLFill(ctx context.Context, pos *domain.Position, o exchange.OrderTradeUpdate) {
	if pos.TrailingOrderID != "" {
		_ = m.executor.CancelOrder(ctx, pos.Symbol, pos.TrailingOrderID)
	}
	avgClose := computeAvgClosePrice(*pos, o.AvgPrice)
	pnl := (pos.EntryPrice - avgClose) * pos.OriginalQty
	pnl = math.Round(pnl*1e8) / 1e8
	result := "PARTIAL_WIN"
	if pnl <= 0 {
		result = "BREAKEVEN"
	}
	m.persistClose(ctx, *pos, avgClose, pnl, result, "TP_TRAIL")
}

// handleHardSLFill fires when the hard 5% stop-loss triggers (worst case).
func (m *PositionManager) handleHardSLFill(ctx context.Context, pos *domain.Position, o exchange.OrderTradeUpdate) {
	if pos.TPOrderID != "" {
		_ = m.executor.CancelOrder(ctx, pos.Symbol, pos.TPOrderID)
	}
	pnl := (pos.EntryPrice - o.AvgPrice) * pos.OriginalQty
	pnl = math.Round(pnl*1e8) / 1e8
	m.persistClose(ctx, *pos, o.AvgPrice, pnl, "LOSS", "HARD_SL")
}

func (m *PositionManager) recordClose(ctx context.Context, pos domain.Position, exitPrice float64, result string, closeReason string) {
	pnl := (pos.EntryPrice - exitPrice) * pos.OriginalQty
	if pos.Side == domain.SideBuy {
		pnl = (exitPrice - pos.EntryPrice) * pos.OriginalQty
	}
	pnl = math.Round(pnl*1e8) / 1e8
	m.persistClose(ctx, pos, exitPrice, pnl, result, closeReason)
}

func (m *PositionManager) persistClose(ctx context.Context, pos domain.Position, avgClose float64, pnl float64, result string, closeReason string) {
	now := time.Now()
	id := pos.TradeID
	if id == uuid.Nil {
		slog.Warn("persistClose: position has no TradeID, result not persisted", "symbol", pos.Symbol)
	} else {
		cr := closeReason
		ac := avgClose
		if err := m.tradeRepo.UpdateResult(ctx, id, domain.ExitInfo{
			AvgClosePrice: ac,
			PnL:           pnl,
			Result:        result,
			CloseReason:   cr,
			ClosedAt:      now,
		}); err != nil {
			slog.Error("update trade result", "symbol", pos.Symbol, "error", err)
		}
	}

	// Phase 4: update trade_memories with outcome
	if m.memoryRepo != nil && pos.TradeID != uuid.Nil {
		outcome := mapResultToMemoryOutcome(result)
		profitPct := pnl / (pos.EntryPrice * pos.OriginalQty) * float64(pos.Leverage) * 100
		holdMin := int(now.Sub(pos.OpenedAt).Minutes())
		if err := m.memoryRepo.UpdateOutcome(ctx, pos.TradeID, outcome, profitPct, holdMin); err != nil {
			slog.Error("update trade memory outcome", "symbol", pos.Symbol, "error", err)
		}
	}

	if err := m.cache.RemovePosition(ctx, pos.Symbol); err != nil {
		slog.Error("remove position from cache", "symbol", pos.Symbol, "error", err)
	}

	if result == "LOSS" || result == "FORCE_SL" {
		cooldown := time.Duration(m.cfg.Trading.CooldownMinutes) * time.Minute
		_ = m.cache.SetCooldown(ctx, cooldown)
		slog.Info("cooldown set after loss", "symbol", pos.Symbol, "duration", cooldown)
	}

	slog.Info("position_closed",
		"symbol", pos.Symbol,
		"result", result,
		"close_reason", closeReason,
		"avg_close", avgClose,
		"pnl", pnl,
	)

	if m.notifier != nil {
		leveragedPnlPct := pnl / (pos.EntryPrice * pos.OriginalQty) * float64(pos.Leverage) * 100
		m.notifier.NotifyTradeClosed(ctx, notify.TradeClosedEvent{
			Symbol:       pos.Symbol,
			Side:         "SHORT",
			EntryPrice:   pos.EntryPrice,
			ExitPrice:    avgClose,
			PnL:          pnl,
			PnLPct:       leveragedPnlPct,
			Result:       result,
			CloseReason:  closeReason,
			HoldDuration: now.Sub(pos.OpenedAt).Round(time.Second).String(),
			IsPaper:      pos.IsPaper,
		})
	}
}

// computeAvgClosePrice calculates weighted average exit price for split-leg closes.
func computeAvgClosePrice(pos domain.Position, finalExitPrice float64) float64 {
	if !pos.TP1Filled || pos.OriginalQty == 0 {
		return finalExitPrice
	}
	tp1Qty := pos.OriginalQty * tp1SizeFraction
	trailQty := pos.OriginalQty - tp1Qty
	return (pos.TP1FillPrice*tp1Qty + finalExitPrice*trailQty) / pos.OriginalQty
}

func mapResultToMemoryOutcome(result string) string {
	switch result {
	case "WIN":
		return "WIN"
	case "PARTIAL_WIN":
		return "PARTIAL_WIN"
	case "FORCE_SL":
		return "FORCE_SL"
	case "LOSS":
		return "LOSS"
	case "BREAKEVEN":
		return "BREAKEVEN"
	default:
		return "BREAKEVEN"
	}
}

package execution

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/google/uuid"

	"futures/internal/config"
	"futures/internal/domain"
	"futures/internal/exchange"
	"futures/internal/indicator"
	"futures/internal/llm"
	"futures/internal/memory"
	"futures/internal/notify"
	"futures/internal/storage"
)

// tp1SizeFraction is the fraction of the original quantity closed by TP1.
// Mirrors cfg.Execution.TP1SizePct / 100 but kept as a local constant for
// use in pure-math helpers that have no config reference.
const tp1SizeFraction = 0.7

// fundingInfoGetter is the subset of MarketEngine needed for pre-settlement checks.
type fundingInfoGetter interface {
	GetFundingInfo(symbol string) (*domain.FundingRate, bool)
}

type PositionManager struct {
	executor      Executor
	market        MarketDataProvider
	cache         storage.StateCache
	tradeRepo     storage.TradeRepository
	memoryRepo    storage.MemoryRepository
	memoryEngine  *memory.Engine
	riskRepo      *storage.PGRiskRepository
	cfg           *config.Config
	forceSLEngine *llm.ForceSLEngine
	indEngine     *indicator.Engine
	notifier      *notify.Notifier
	fundingInfo   fundingInfoGetter // Phase 8: for pre-settlement check
	// posLocks provides per-symbol mutual exclusion between the 1s tick loop
	// (check) and asynchronous WS fill events (HandleUserDataEvent).
	posLocks          sync.Map // map[string]*sync.Mutex
	pendingForceClose sync.Map // map[symbol]clientOrderID — prevents double-persist when WS fill races ahead of PlaceMarketOrder return
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

// SetMemoryEngine attaches the memory engine for inserting trade records at entry.
func (m *PositionManager) SetMemoryEngine(eng *memory.Engine) {
	m.memoryEngine = eng
}

// SetFundingInfoGetter wires the funding info source for pre-settlement checks (Phase 8).
func (m *PositionManager) SetFundingInfoGetter(fi fundingInfoGetter) {
	m.fundingInfo = fi
}

// lockPosition returns the mutex for a symbol, creating it on first use.
// Always defer unlock immediately after acquiring: mu := m.lockPosition(sym); defer mu.Unlock()
func (m *PositionManager) lockPosition(symbol string) *sync.Mutex {
	v, _ := m.posLocks.LoadOrStore(symbol, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu
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
		m.check(ctx, pos.Symbol)
	}
}

func (m *PositionManager) check(ctx context.Context, symbol string) {
	mu := m.lockPosition(symbol)
	defer mu.Unlock()

	pos, err := m.cache.GetActivePosition(ctx, symbol)
	if err != nil || pos == nil {
		return
	}

	currentPrice, ok := m.market.GetPrice(pos.Symbol)
	if !ok || currentPrice == 0 {
		slog.Info("check position skipped, price not exist", "symbol", pos.Symbol)
		return
	}

	updated := false

	// Phase 7: retry missing SL/TP orders before other checks.
	if pp, _ := m.cache.GetPendingProtection(ctx, pos.Symbol); pp != nil {
		m.retryProtection(ctx, *pos, pp)
		// Reload position in case retryProtection updated order IDs.
		if fresh, err := m.cache.GetActivePosition(ctx, pos.Symbol); err == nil && fresh != nil {
			pos = fresh
		}
	}

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
		m.shouldCheckForceSL(*pos, rawPnlPct) {

		decision := m.checkForceSL(ctx, *pos, currentPrice, rawPnlPct)
		if decision != nil && decision.Action == "FORCE_CLOSE" {
			m.forceClose(ctx, *pos, decision.Reason)
			return
		}
		pos.LastForceSLCheck = time.Now()
		pos.ForceSLCount++
		updated = true
	}

	// === BREAKEVEN LOGIC (only applies before TP1 fills) ===
	if !pos.BreakevenMoved && !pos.TP1Filled {
		breakevenActivationPct := m.cfg.Execution.BreakevenActivationPct
		if pos.FundingFeePaid {
			breakevenActivationPct += pos.FundingFeePaidPct * 100
		}
		if rawPnlPct > breakevenActivationPct &&
			time.Since(pos.OpenedAt) >= 10*time.Minute {
			if pos.SLOrderID != "" {
				if err := m.executor.CancelOrder(ctx, pos.Symbol, pos.SLOrderID); err != nil {
					slog.Warn("cancel SL for breakeven failed", "symbol", pos.Symbol, "error", err)
				}
			}
			stopPrice := pos.EntryPrice
			if pos.FundingFeePaid {
				stopPrice -= pos.FundingFeePaidPct * pos.EntryPrice
			}

			newSL, err := m.executor.PlaceStopMarketOrder(ctx, domain.OrderRequest{
				Symbol:        pos.Symbol,
				Side:          domain.SideBuy,
				Type:          domain.OrderTypeStopMarket,
				TriggerPrice:  stopPrice,
				ClosePosition: true,
			})
			if err != nil {
				slog.Error("replace SL at breakeven failed", "symbol", pos.Symbol, "error", err)
				return
			}
			pos.StopLoss = pos.EntryPrice
			pos.BreakevenMoved = true
			if newSL != nil {
				pos.SLOrderID = newSL.ClientOrderId
			}
			updated = true
			slog.Info("moved SL to breakeven", "symbol", pos.Symbol, "entry", pos.EntryPrice)
		}
	}

	// Paper mode: simulate TP1/SL/trailing fills via price-level crossing.
	if pos.IsPaper {
		if m.checkPaperFills(ctx, pos, currentPrice) {
			return // position closed — no further updates
		}
	}

	if updated {
		if err := m.cache.SetActivePosition(ctx, *pos); err != nil {
			slog.Error("update position state", "symbol", pos.Symbol, "error", err)
		}
	}
}

// checkPaperFills simulates order fills for paper-mode positions by comparing mark price
// against stored price levels. Calls the same handlers as live WS fill events.
// Returns true if the position was closed (caller should return immediately).
func (m *PositionManager) checkPaperFills(ctx context.Context, pos *domain.Position, currentPrice float64) bool {
	fakeOrder := func(_ string, avgPrice, qty float64) exchange.OrderTradeUpdate {
		return exchange.OrderTradeUpdate{
			Symbol:      pos.Symbol,
			OrderID:     0,
			OrderStatus: "FILLED",
			AvgPrice:    avgPrice,
			FilledQty:   qty,
			ReduceOnly:  true,
		}
	}

	if !pos.TP1Filled {
		// Hard SL: price rose above stop loss (SHORT loses when price goes up).
		if currentPrice >= pos.StopLoss {
			if pos.BreakevenMoved {
				slog.Info("paper_breakeven_hit", "symbol", pos.Symbol)
			} else {
				slog.Info("paper_sl_hit", "symbol", pos.Symbol, "price", currentPrice, "sl", pos.StopLoss)
			}
			m.handleHardSLFill(ctx, pos, fakeOrder(pos.SLOrderID, pos.StopLoss, pos.Quantity))
			return true
		}

		// TP1: price fell to or below take profit level.
		if currentPrice <= pos.TakeProfit {
			slog.Info("paper_tp1_hit", "symbol", pos.Symbol, "price", currentPrice, "tp1", pos.TakeProfit)
			o := fakeOrder(pos.TPOrderID, pos.TakeProfit, pos.OriginalQty*tp1SizeFraction)
			o.FilledQty = pos.OriginalQty * tp1SizeFraction
			m.handleTP1Fill(ctx, pos, o)
			// handleTP1Fill sets pos.TP1Filled and updates TrailingPeak via cache;
			// initialise TrailingPeak to the TP1 fill price (best price so far).
			pos.TrailingPeak = pos.TakeProfit
			if err := m.cache.SetActivePosition(ctx, *pos); err != nil {
				slog.Error("paper_tp1: update trailing peak", "symbol", pos.Symbol, "error", err)
			}
			return false // position still open (trailing portion remains)
		}
		return false
	}

	// TP1 already filled — manage the TP2 portion.
	remainingQty := pos.OriginalQty - pos.OriginalQty*tp1SizeFraction

	// Breakeven SL: price rose back to or above entry price.
	if currentPrice >= pos.EntryPrice {
		slog.Info("paper_bep_hit", "symbol", pos.Symbol, "price", currentPrice, "entry", pos.EntryPrice)
		m.handleTrailingSLFill(ctx, pos, fakeOrder(pos.TrailingSLOrderID, pos.EntryPrice, remainingQty))
		return true
	}

	// TP2: fixed limit order — price fell to or below TP2 price.
	if pos.TP2Price > 0 && currentPrice <= pos.TP2Price {
		slog.Info("paper_tp2_hit",
			"symbol", pos.Symbol,
			"price", currentPrice,
			"tp2", pos.TP2Price,
		)
		m.handleTP2Fill(ctx, pos, fakeOrder(pos.TP2OrderID, pos.TP2Price, remainingQty))
		return true
	}

	// --- Trailing stop (commented out, replaced by fixed TP2) ---
	// callbackRate := m.cfg.Execution.TrailingCallbackRate / 100
	// if callbackRate > 0 && pos.TrailingPeak > 0 {
	// 	trailingTrigger := pos.TrailingPeak * (1 + callbackRate)
	// 	if currentPrice >= trailingTrigger {
	// 		slog.Info("paper_trailing_hit", ...)
	// 		m.handleTrailingFill(ctx, pos, fakeOrder(pos.TrailingOrderID, currentPrice, remainingQty))
	// 		return true
	// 	}
	// }

	return false
}

// shouldCheckForceSL implements the PnL-gated + escalating interval logic.
func (m *PositionManager) shouldCheckForceSL(pos domain.Position, rawPnlPct float64) bool {
	startMin := time.Duration(m.cfg.Execution.ForceSLStartMin) * time.Minute
	if time.Since(pos.OpenedAt) < startMin {
		return false
	}

	gatePct := m.cfg.Execution.ForceSLPnlGatePct // e.g. -1.5
	if rawPnlPct >= gatePct {
		return false // winning or barely negative — no check needed
	}

	escalatePct := m.cfg.Execution.ForceSLEscalatePnlPct // e.g. -2.5
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

	// Phase 8: effective loss = raw PnL + funding fee if paid
	leveragedPnlPct := rawPnlPct * float64(pos.Leverage)
	effectiveLossPct := leveragedPnlPct
	if pos.FundingFeePaid {
		effectiveLossPct -= pos.FundingFeePaidPct * 100
	}

	minutesSinceSettle := 0
	if pos.SettlementPassedAt != nil {
		minutesSinceSettle = int(time.Since(*pos.SettlementPassedAt).Minutes())
	}

	req := &llm.ForceSLRequest{
		Symbol:             pos.Symbol,
		EntryPrice:         pos.EntryPrice,
		CurrentPrice:       currentPrice,
		UnrealizedPnlPct:   leveragedPnlPct,
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
		// Phase 8 fields
		FundingRatePct:     pos.FundingRateAtEntry * 100,
		FundingFeePaid:     pos.FundingFeePaid,
		FundingFeePaidPct:  pos.FundingFeePaidPct * 100,
		EffectiveLossPct:   effectiveLossPct,
		SettlementPassed:   pos.SettlementPassedAt != nil,
		MinutesSinceSettle: minutesSinceSettle,
		TPWidened:          pos.TPWidened,
	}

	resp, err := m.forceSLEngine.EvaluatePosition(ctx, req, m.cfg.Execution.ForceSLTimeoutSec)
	if err != nil {
		return nil
	}
	return resp
}

func (m *PositionManager) forceClose(ctx context.Context, pos domain.Position, reason string) {
	clientID := "fclose-" + uuid.New().String()[:16]

	m.pendingForceClose.Store(pos.Symbol, clientID)

	_, err := m.executor.PlaceMarketOrder(ctx, domain.OrderRequest{
		Symbol:           pos.Symbol,
		Side:             domain.SideBuy,
		Type:             domain.OrderTypeMarket,
		Quantity:         pos.Quantity,
		ReduceOnly:       true,
		NewClientOrderID: clientID,
	})
	if err != nil {
		m.pendingForceClose.Delete(pos.Symbol)
		slog.Error("force close market order failed", "symbol", pos.Symbol, "error", err)
		return
	}

	m.cancelAllOrders(ctx, pos)

	slog.Info("force_close_placed",
		"symbol", pos.Symbol,
		"reason", reason,
		"client_order_id", clientID,
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
	if pos.TP2OrderID != "" {
		_ = m.executor.CancelOrder(ctx, pos.Symbol, pos.TP2OrderID)
	}
}

// HandleUserDataEvent is called by the user data stream router on every ORDER_TRADE_UPDATE.
func (m *PositionManager) HandleUserDataEvent(event exchange.UserDataEvent) {
	o := event.Order
	slog.Info("HandleUserDataEvent", "event", event)
	if o.OrderStatus != "FILLED" {
		return
	}

	ctx := context.Background()
	orderId := o.ClientOrderID
	o.TradeTime = event.TradeTime

	// Phase 7: detect entry fills by looking up the PendingEntry — keyed by order ID.
	// We don't rely on ReduceOnly=false because close orders can't match a pending entry
	// (they're placed after finalizeSLTP runs), so the lookup is the correct discriminator.
	if o.OrderType == string(domain.OrderTypeMarket) && o.Side == string(domain.SideSell) {
		pending, err := m.cache.GetPendingEntry(ctx, orderId)
		if err != nil {
			slog.Error("HandleUserDataEvent: get pending entry", "error", err)
			return
		}
		if pending != nil {
			fillPrice := o.AvgPrice
			if fillPrice == 0 {
				slog.Warn("WS fill has zero AvgPrice, skipping finalize",
					"symbol", o.Symbol, "order_id", orderId)
				return
			}
			var tradeTime time.Time
			if event.TradeTime > 0 {
				tradeTime = time.UnixMilli(event.TradeTime).UTC()
			} else {
				tradeTime = time.Now()
			}
			mu := m.lockPosition(o.Symbol)
			defer mu.Unlock()
			m.finalizeSLTP(ctx, pending, fillPrice, o.FilledQty, tradeTime)
			return
		}
		// No pending entry → not our order; fall through to ignore.
		return
	}

	mu := m.lockPosition(o.Symbol)
	defer mu.Unlock()

	pos, err := m.cache.GetActivePosition(ctx, o.Symbol)
	if err != nil {
		slog.Error("HandleUserDataEvent: get active position", "symbol", o.Symbol, "error", err)
		return
	}

	switch orderId {
	case pos.TPOrderID:
		if !pos.TP1Filled {
			m.handleTP1Fill(ctx, pos, o)
		}
	case pos.TP2OrderID:
		m.handleTP2Fill(ctx, pos, o)
	case pos.TrailingOrderID:
		slog.Warn("unexpected trailing fill received — trailing replaced by fixed TP2", "symbol", pos.Symbol, "order", orderId)
	case pos.TrailingSLOrderID:
		m.handleTrailingSLFill(ctx, pos, o)
	case pos.SLOrderID:
		m.handleHardSLFill(ctx, pos, o)
	default:
		if o.ReduceOnly {
			if storedID, ok := m.pendingForceClose.Load(o.Symbol); ok && storedID.(string) == o.ClientOrderID {
				m.pendingForceClose.Delete(o.Symbol)
				avgClose := m.computeAvgClosePrice(*pos, o.AvgPrice)
				pnl := (pos.EntryPrice - avgClose) * pos.OriginalQty
				pnl = math.Round(pnl*1e8) / 1e8
				finalPnl := calcFinalPnl(pnl, *pos)
				var result string
				if pos.TP1Filled {
					result = "WIN"
				} else {
					if finalPnl > 0.0 {
						result = "PARTIAL_WIN"
					} else if finalPnl > -1.5 {
						result = "BREAKEVEN"
					} else {
						result = "LOSS"
					}
				}
				m.persistClose(ctx, *pos, o.AvgPrice, pnl, result, "FORCE_CLOSE")
				slog.Info("force_close_confirmed_ws",
					"symbol", o.Symbol,
					"avg_price", o.AvgPrice,
					"pnl", pnl,
				)
				return
			}
			slog.Info("untracked reduce-only fill, treating as manual close",
				"symbol", o.Symbol, "order_id", o.OrderID)
			m.recordManualClose(ctx, *pos, o.AvgPrice, "MANUAL")
		}
	}
}

// ——————————————————————————————————————————————————————————
// Phase 7: WS-confirmed SL/TP placement
// ——————————————————————————————————————————————————————————

const (
	maxProtectionRetries    = 5
	protectionRetryInterval = 5 * time.Second
)

// finalizeSLTP is called when the entry MARKET SELL fill is confirmed via WS.
// It places SL and TP using the real fill price, writes the trade record, and
// activates the position in Redis.
func (m *PositionManager) finalizeSLTP(
	ctx context.Context,
	pending *domain.PendingEntry,
	avgFillPrice float64,
	filledQty float64,
	openedAt time.Time,
) {
	if filledQty == 0 {
		filledQty = pending.Quantity
	}

	slDistance := avgFillPrice * (m.cfg.Execution.SlPct / 100)
	stopLoss := avgFillPrice + slDistance // SHORT: SL above entry

	tpDistance := avgFillPrice * (pending.TpPct / 100)
	tp1Price := avgFillPrice - tpDistance // SHORT: TP below entry

	tp1SizePct := m.cfg.Execution.TP1SizePct / 100
	tp1Qty := filledQty * tp1SizePct

	slOrder, slErr := m.executor.PlaceStopMarketOrder(ctx, domain.OrderRequest{
		Symbol:        pending.Symbol,
		Side:          domain.SideBuy,
		Type:          domain.OrderTypeStopMarket,
		TriggerPrice:  stopLoss,
		ClosePosition: true,
	})
	if slErr != nil {
		slog.Error("finalizeSLTP: place SL failed, position will not be activated",
			"symbol", pending.Symbol, "error", slErr)
		// Schedule retry — the entry fill is real, so we must keep trying.
		pp := domain.PendingProtection{
			Symbol:     pending.Symbol,
			NeedsSL:    true,
			NeedsTP:    true,
			EntryPrice: avgFillPrice,
			Quantity:   filledQty,
			TP1Qty:     tp1Qty,
			StopLoss:   stopLoss,
			TakeProfit: tp1Price,
		}
		if err := m.cache.SetPendingProtection(ctx, pp); err != nil {
			slog.Error("finalizeSLTP: set pending protection after SL failure", "symbol", pending.Symbol, "error", err)
		}
		return
	}
	slOrderID := slOrder.ClientOrderId

	tpOrder, tpErr := m.executor.PlaceLimitOrder(ctx, domain.OrderRequest{
		Symbol:     pending.Symbol,
		Side:       domain.SideBuy,
		Quantity:   tp1Qty,
		Price:      tp1Price,
		ReduceOnly: true,
	})
	if tpErr != nil {
		slog.Error("finalizeSLTP: place TP1 failed, cancelling SL to retry both",
			"symbol", pending.Symbol, "error", tpErr)
		// Cancel the placed SL so both are retried atomically.
		if cancelErr := m.executor.CancelOrder(ctx, pending.Symbol, slOrderID); cancelErr != nil {
			slog.Error("finalizeSLTP: cancel SL after TP failure", "symbol", pending.Symbol, "error", cancelErr)
		}
		pp := domain.PendingProtection{
			Symbol:     pending.Symbol,
			NeedsSL:    true,
			NeedsTP:    true,
			EntryPrice: avgFillPrice,
			Quantity:   filledQty,
			TP1Qty:     tp1Qty,
			StopLoss:   stopLoss,
			TakeProfit: tp1Price,
		}
		if err := m.cache.SetPendingProtection(ctx, pp); err != nil {
			slog.Error("finalizeSLTP: set pending protection after TP failure", "symbol", pending.Symbol, "error", err)
		}
		return
	}
	tpOrderID := tpOrder.ClientOrderId

	// Place TP2 fixed limit order on the remaining 30% immediately at open.
	tp2Qty := filledQty - tp1Qty
	tp2Price := avgFillPrice * (1 - m.cfg.Execution.TP2Pct/100)

	var tp2OrderID string
	tp2Order, tp2Err := m.executor.PlaceLimitOrder(ctx, domain.OrderRequest{
		Symbol:     pending.Symbol,
		Side:       domain.SideBuy,
		Quantity:   tp2Qty,
		Price:      tp2Price,
		ReduceOnly: true,
	})
	if tp2Err != nil {
		slog.Warn("finalizeSLTP: place TP2 failed, position still activated without TP2",
			"symbol", pending.Symbol, "error", tp2Err)
	} else if tp2Order != nil {
		tp2OrderID = tp2Order.ClientOrderId
	}

	isPaper := m.cfg.App.Mode == "paper"

	pos := domain.Position{
		Symbol:             pending.Symbol,
		Side:               domain.SideSell,
		EntryPrice:         avgFillPrice,
		Quantity:           filledQty,
		OriginalQty:        filledQty,
		Leverage:           m.cfg.Trading.Leverage,
		EntryMode:          domain.EntryMode(pending.Window),
		StopLoss:           stopLoss,
		TakeProfit:         tp1Price,
		SLOrderID:          slOrderID,
		TPOrderID:          tpOrderID,
		TP2OrderID:         tp2OrderID,
		TP2Price:           tp2Price,
		TradeID:            pending.TradeId,
		OpenedAt:           openedAt,
		IsPaper:            isPaper,
		HighSinceEntry:     avgFillPrice,
		LowSinceEntry:      avgFillPrice,
		OriginalConfidence: pending.Confidence,
		FundingRateAtEntry: pending.FundingRateAtEntry,
	}
	if pending.LLMDecision != nil {
		pos.LLMEntryReasons = pending.LLMDecision.EntryReasons
		pos.OriginalConfidence = pending.LLMDecision.Confidence
	}

	trade := &domain.Trade{
		ID:          pending.TradeId,
		Symbol:      pending.Symbol,
		Side:        domain.SideSell,
		EntryMode:   domain.EntryMode(pending.Window),
		Leverage:    m.cfg.Trading.Leverage,
		Confidence:  pending.Confidence,
		EntryPrice:  avgFillPrice,
		Quantity:    filledQty,
		IsPaper:     isPaper,
		CreatedAt:   openedAt,
		FundingRate: pending.FundingRate,
		Change24h:   pending.Change24h,
	}
	if pending.LLMDecision != nil {
		d := pending.LLMDecision
		conf := d.Confidence
		mode := string(d.EntryMode)
		trade.LLMConfidence = &conf
		trade.LLMEntryMode = &mode
		trade.LLMEntryReasons = d.EntryReasons
		trade.LLMWarnings = d.Warnings
	}

	if err := m.tradeRepo.Insert(ctx, trade); err != nil {
		slog.Error("finalizeSLTP: insert trade record", "symbol", pending.Symbol, "error", err)
	}

	if m.memoryEngine != nil && pending.ScoredCandidate != nil && pending.IndicatorSnapshot != nil {
		go func() {
			bgCtx := context.Background()
			if err := m.memoryEngine.RecordTrade(bgCtx, trade, pending.IndicatorSnapshot, pending.BTCContext, pending.ScoredCandidate, pending.LLMDecision); err != nil {
				slog.Error("finalizeSLTP: record trade memory", "symbol", pending.Symbol, "error", err)
			}
		}()
	}

	if err := m.cache.SetActivePosition(ctx, pos); err != nil {
		slog.Error("finalizeSLTP: set active position", "symbol", pending.Symbol, "error", err)
	}
	if err := m.cache.RemovePendingEntry(ctx, pending.TradeId.String()); err != nil {
		slog.Warn("finalizeSLTP: remove pending entry", "symbol", pending.Symbol, "error", err)
	}

	slog.Info("position opened (WS-confirmed)",
		"symbol", pending.Symbol,
		"entry", avgFillPrice,
		"sl", stopLoss,
		"sl_order", slOrderID,
		"tp1", tp1Price,
		"tp1_order", tpOrderID,
		"tp2", tp2Price,
		"tp2_order", tp2OrderID,
		"qty_total", filledQty,
		"qty_tp1", tp1Qty,
		"qty_tp2", tp2Qty,
	)

	if m.notifier != nil {
		m.notifier.NotifyTradeOpened(ctx, notify.TradeOpenedEvent{
			Symbol:      pending.Symbol,
			Side:        "SHORT",
			EntryPrice:  avgFillPrice,
			Quantity:    filledQty,
			Leverage:    m.cfg.Trading.Leverage,
			StopLoss:    stopLoss,
			TakeProfit:  tp1Price,
			EntryMode:   pending.Window,
			Confidence:  pending.Confidence,
			IsPaper:     isPaper,
			OpenedAt:    openedAt,
			FundingRate: pending.FundingRate,
		})
	}

}

// retryProtection attempts to place missing SL/TP orders for an unprotected position.
// It is called from check() every second and enforces a 5-second interval between retries.
// After maxProtectionRetries failures it performs an emergency market close.
func (m *PositionManager) retryProtection(ctx context.Context, pos domain.Position, pp *domain.PendingProtection) {
	if time.Since(pp.LastAttempt) < protectionRetryInterval {
		return
	}

	if pp.Retries >= maxProtectionRetries {
		slog.Error("protection retry exhausted, emergency close",
			"symbol", pos.Symbol, "retries", pp.Retries)
		m.forceClose(ctx, pos, "protection retry exhausted")
		_ = m.cache.RemovePendingProtection(ctx, pos.Symbol)
		if m.notifier != nil {
			m.notifier.NotifyRiskEvent(ctx, notify.RiskEvent{
				Type:    "kill_switch",
				Message: fmt.Sprintf("Emergency close on %s: SL/TP placement failed after %d retries", pos.Symbol, maxProtectionRetries),
			})
		}
		return
	}

	slog.Warn("retrying protection orders",
		"symbol", pos.Symbol,
		"needs_sl", pp.NeedsSL,
		"needs_tp", pp.NeedsTP,
		"attempt", pp.Retries+1,
	)

	if pp.NeedsSL {
		slOrder, err := m.executor.PlaceStopMarketOrder(ctx, domain.OrderRequest{
			Symbol:        pos.Symbol,
			Side:          domain.SideBuy,
			Type:          domain.OrderTypeStopMarket,
			TriggerPrice:  pp.StopLoss,
			ClosePosition: true,
		})
		if err != nil {
			slog.Error("retryProtection: SL still failing", "symbol", pos.Symbol, "error", err)
		} else {
			pp.NeedsSL = false
			pos.SLOrderID = slOrder.ClientOrderId
			pos.StopLoss = pp.StopLoss
			if err := m.cache.SetActivePosition(ctx, pos); err != nil {
				slog.Error("retryProtection: update position", "symbol", pos.Symbol, "error", err)
			}
		}
	}

	if pp.NeedsTP {
		tpOrder, err := m.executor.PlaceLimitOrder(ctx, domain.OrderRequest{
			Symbol:     pos.Symbol,
			Side:       domain.SideBuy,
			Quantity:   pp.TP1Qty,
			Price:      pp.TakeProfit,
			ReduceOnly: true,
		})
		if err != nil {
			slog.Error("retryProtection: TP still failing", "symbol", pos.Symbol, "error", err)
		} else {
			tp2Qty := pp.Quantity - pp.TP1Qty
			tp2Price := pp.EntryPrice * (1 - m.cfg.Execution.TP2Pct/100)
			tp2Order, _ := m.executor.PlaceLimitOrder(ctx, domain.OrderRequest{
				Symbol:     pp.Symbol,
				Side:       domain.SideBuy,
				Quantity:   tp2Qty,
				Price:      tp2Price,
				ReduceOnly: true,
			})
			pp.NeedsTP = false
			pos.TPOrderID = tpOrder.ClientOrderId
			pos.TakeProfit = pp.TakeProfit
			if tp2Order != nil {
				pos.TP2OrderID = tp2Order.ClientOrderId
				pos.TP2Price = tp2Price
			}
			if err := m.cache.SetActivePosition(ctx, pos); err != nil {
				slog.Error("retryProtection: update position", "symbol", pos.Symbol, "error", err)
			}
		}
	}

	if !pp.NeedsSL && !pp.NeedsTP {
		// Both orders placed — clear the protection record.
		_ = m.cache.RemovePendingProtection(ctx, pos.Symbol)
		slog.Info("protection orders placed successfully", "symbol", pos.Symbol)
		return
	}

	pp.Retries++
	pp.LastAttempt = time.Now()
	if err := m.cache.SetPendingProtection(ctx, *pp); err != nil {
		slog.Error("retryProtection: update pending protection", "symbol", pos.Symbol, "error", err)
	}
}

// handleTP1Fill fires when TP1 (50% qty) fills. Places trailing stop + breakeven SL on the remainder.
func (m *PositionManager) handleTP1Fill(ctx context.Context, pos *domain.Position, o exchange.OrderTradeUpdate) {
	now := time.Now()
	pos.TP1Filled = true
	pos.TP1FillPrice = o.AvgPrice
	pos.TP1FilledAt = &now

	tp1Qty := o.FilledQty
	if tp1Qty == 0 {
		tp1Qty = pos.OriginalQty * m.cfg.Execution.TP1SizePct / 100
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

	// 1a. check if fully closed at Tp1
	if pos.TP2OrderID == "" {
		slog.Info("position fully closed at TP1", "symbol", pos.Symbol)
		m.handleTP2Fill(ctx, pos, o)
		return
	}

	// 2. Place breakeven SL at entry price to protect the remaining 30%.
	beOrder, err := m.executor.PlaceStopMarketOrder(ctx, domain.OrderRequest{
		Symbol:        pos.Symbol,
		Side:          domain.SideBuy,
		Type:          domain.OrderTypeStopMarket,
		TriggerPrice:  pos.EntryPrice,
		ClosePosition: true,
	})
	if err != nil {
		slog.Warn("place breakeven SL after TP1 failed", "symbol", pos.Symbol, "error", err)
	} else if beOrder != nil {
		pos.TrailingSLOrderID = beOrder.ClientOrderId
		pos.BreakevenMoved = true
	}

	// 3. TP2 fixed limit order already placed at position open — nothing to do here.

	// --- Trailing stop (commented out, replaced by fixed TP2) ---
	// trailingOrder, err := m.executor.PlaceTrailingStopOrder(ctx, TrailingStopRequest{
	// 	Symbol:       pos.Symbol,
	// 	Side:         domain.SideBuy,
	// 	Quantity:     remainingQty,
	// 	CallbackRate: m.cfg.Execution.TrailingCallbackRate,
	// 	ReduceOnly:   true,
	// })
	// if err != nil {
	// 	slog.Error("place trailing stop failed, market closing remainder",
	// 		"symbol", pos.Symbol, "error", err)
	// 	m.forceClose(ctx, *pos, "trailing placement failed")
	// 	return
	// }
	// if trailingOrder != nil {
	// 	pos.TrailingOrderID = trailingOrder.ClientOrderId
	// }

	pos.Quantity = remainingQty

	if err := m.cache.SetActivePosition(ctx, *pos); err != nil {
		slog.Error("update position after TP1 fill", "symbol", pos.Symbol, "error", err)
	}

	slog.Info("tp1_filled_tp2_active",
		"symbol", pos.Symbol,
		"tp2_order", pos.TP2OrderID,
		"tp2_price", pos.TP2Price,
		"breakeven_sl_order", pos.TrailingSLOrderID,
		"remaining_qty", remainingQty,
	)
	tp1PnLPct := (pos.EntryPrice - o.AvgPrice) / pos.EntryPrice * 100
	securedUSDT := (pos.EntryPrice - o.AvgPrice) * (pos.OriginalQty * m.cfg.Execution.TP1SizePct / 100)
	m.notifier.NotifyTp1Event(ctx, notify.Tp1Event{
		Symbol:      pos.Symbol,
		ClosedAt:    time.UnixMilli(o.TradeTime).UTC(),
		AvgPrice:    o.AvgPrice,
		PnLPct:      tp1PnLPct,
		PnLROI:      tp1PnLPct * float64(m.cfg.Trading.Leverage),
		SecuredUSDT: securedUSDT,
	})
}

// handleTP2Fill fires when the fixed TP2 limit order fills (full thesis validated).
func (m *PositionManager) handleTP2Fill(ctx context.Context, pos *domain.Position, o exchange.OrderTradeUpdate) {
	if pos.TrailingSLOrderID != "" {
		_ = m.executor.CancelOrder(ctx, pos.Symbol, pos.TrailingSLOrderID)
	}
	avgClose := m.computeAvgClosePrice(*pos, o.AvgPrice)
	pnl := (pos.EntryPrice - avgClose) * pos.OriginalQty
	pnl = math.Round(pnl*1e8) / 1e8
	m.persistClose(ctx, *pos, avgClose, pnl, "WIN", "TP2")
}

// handleTrailingSLFill fires when the breakeven SL on the TP2 portion triggers (price reversed after TP1).
func (m *PositionManager) handleTrailingSLFill(ctx context.Context, pos *domain.Position, o exchange.OrderTradeUpdate) {
	if pos.TP2OrderID != "" {
		_ = m.executor.CancelOrder(ctx, pos.Symbol, pos.TP2OrderID)
	}
	avgClose := m.computeAvgClosePrice(*pos, o.AvgPrice)
	pnl := (pos.EntryPrice - avgClose) * pos.OriginalQty
	pnl = math.Round(pnl*1e8) / 1e8
	pnl = calcFinalPnl(pnl, *pos)
	result := "WIN"
	if pnl <= 0 {
		result = "BREAKEVEN"
	}
	m.persistClose(ctx, *pos, avgClose, pnl, result, "TP_TRAIL")
}

// handleTrailingFill is kept for WS routing compatibility but should not fire with fixed TP2.
// --- Trailing stop (commented out, replaced by fixed TP2) ---
// func (m *PositionManager) handleTrailingFill(ctx context.Context, pos *domain.Position, o exchange.OrderTradeUpdate) {
// 	if pos.TrailingSLOrderID != "" {
// 		_ = m.executor.CancelOrder(ctx, pos.Symbol, pos.TrailingSLOrderID)
// 	}
// 	avgClose := computeAvgClosePrice(*pos, o.AvgPrice)
// 	pnl := (pos.EntryPrice - avgClose) * pos.OriginalQty
// 	pnl = math.Round(pnl*1e8) / 1e8
// 	m.persistClose(ctx, *pos, avgClose, pnl, "WIN", "TP_TRAIL")
// }

// handleHardSLFill fires when the SL order triggers. If breakeven was already moved,
// the same SLOrderID points to the breakeven stop — classify as BREAKEVEN, not LOSS.
func (m *PositionManager) handleHardSLFill(ctx context.Context, pos *domain.Position, o exchange.OrderTradeUpdate) {
	if pos.TPOrderID != "" {
		_ = m.executor.CancelOrder(ctx, pos.Symbol, pos.TPOrderID)
	}
	if pos.TP2OrderID != "" {
		_ = m.executor.CancelOrder(ctx, pos.Symbol, pos.TP2OrderID)
	}

	pnl := (pos.EntryPrice - o.AvgPrice) * pos.OriginalQty
	if pos.Side == domain.SideBuy {
		pnl = (o.AvgPrice - pos.EntryPrice) * pos.OriginalQty
	}
	pnl = math.Round(pnl*1e8) / 1e8

	result := "LOSS"
	closeReason := "HARD_SL"
	if pos.BreakevenMoved {
		result = "BREAKEVEN"
		if pnl > 0 {
			result = "PARTIAL_WIN"
		}
		closeReason = "BREAKEVEN_SL"
	}
	m.persistClose(ctx, *pos, o.AvgPrice, pnl, result, closeReason)
}

func (m *PositionManager) recordManualClose(ctx context.Context, pos domain.Position, exitPrice float64, closeReason string) {
	avgClose := m.computeAvgClosePrice(pos, exitPrice)
	pnl := (pos.EntryPrice - avgClose) * pos.OriginalQty
	finalPnl := calcFinalPnl(pnl, pos)
	var result string
	if finalPnl > 0.0 {
		result = "WIN"
	} else {
		result = "LOSS"
	}
	pnl = math.Round(pnl*1e8) / 1e8
	m.persistClose(ctx, pos, exitPrice, pnl, result, closeReason)
}

func (m *PositionManager) persistClose(ctx context.Context, pos domain.Position, avgClose float64, pnl float64, result string, closeReason string) {
	now := time.Now()
	id := pos.TradeID
	pnl = calcFinalPnl(pnl, pos)
	if id == uuid.Nil {
		slog.Warn("persistClose: position has no TradeID, result not persisted", "symbol", pos.Symbol)
	} else {
		roiPct := pnl / (pos.EntryPrice * pos.OriginalQty) * float64(pos.Leverage) * 100
		if err := m.tradeRepo.UpdateResult(ctx, id, domain.ExitInfo{
			AvgClosePrice:  avgClose,
			PnL:            pnl,
			ROIPct:         roiPct,
			Result:         result,
			CloseReason:    closeReason,
			ClosedAt:       now,
			FundingFeePaid: pos.FundingFeePaid,
		}); err != nil {
			slog.Error("update trade result", "symbol", pos.Symbol, "error", err)
		}
	}

	// Phase 4: update trade_memories with outcome, then generate lesson now that outcome is known
	if m.memoryRepo != nil && pos.TradeID != uuid.Nil {
		outcome := mapResultToMemoryOutcome(result)
		profitPct := pnl / (pos.EntryPrice * pos.OriginalQty) * float64(pos.Leverage) * 100
		holdMin := int(now.Sub(pos.OpenedAt).Minutes())
		if err := m.memoryRepo.UpdateOutcomeByTradeID(ctx, pos.TradeID, outcome, profitPct, holdMin); err != nil {
			slog.Error("update trade memory outcome", "symbol", pos.Symbol, "error", err)
		}
		if m.memoryEngine != nil {
			go m.memoryEngine.GenerateLessonForTrade(context.Background(), pos.TradeID)
		}
	}

	if err := m.cache.RemovePosition(ctx, pos.Symbol); err != nil {
		slog.Error("remove position from cache", "symbol", pos.Symbol, "error", err)
	}

	if result == "LOSS" || result == "FORCE_SL" {
		if err := m.cache.IncrDailyLossCount(ctx); err != nil {
			slog.Error("incr daily loss count", "symbol", pos.Symbol, "error", err)
		}
	}
	if time.Since(pos.OpenedAt) < 3*time.Hour {
		if err := m.cache.SetSymbolCooldown(ctx, pos.Symbol, 20*time.Minute); err != nil {
			slog.Error("set symbol cooldown after close", "symbol", pos.Symbol, "error", err)
		}
	} else {
		slog.Info("position opened last cycle, no cooldown", "symbol", pos.Symbol)
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

// ——————————————————————————————————————————————————————————
// Phase 8: Pre-settlement T-2m check + funding fee tracking
// ——————————————————————————————————————————————————————————

// RunPreSettlementChecker starts a goroutine that checks FRONTRUN/LASTMINUTE positions
// at T-2m before each funding settlement for emergency close or TP widen.
func (m *PositionManager) RunPreSettlementChecker(ctx context.Context) {
	if !m.cfg.PreSettlement.Enabled {
		slog.Info("pre-settlement checker disabled by config")
		return
	}
	go m.preSettlementLoop(ctx)
}

func (m *PositionManager) preSettlementLoop(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.checkPreSettlementAll(ctx)
		}
	}
}

func (m *PositionManager) checkPreSettlementAll(ctx context.Context) {
	if m.fundingInfo == nil {
		return
	}
	positions, err := m.cache.GetActivePositions(ctx)
	if err != nil {
		return
	}
	for _, pos := range positions {
		if pos.EntryMode == domain.EntryModeAfter {
			continue
		}
		m.checkPreSettlement(ctx, pos)
		m.checkSettlementPassed(ctx, pos)
	}
}

func preSettlementThreshold(fundingRate float64) float64 {
	abs := math.Abs(fundingRate)
	return math.Abs(0.05 - abs)
}

// checkPreSettlement fires the T-2m check for a FRONTRUN or LASTMINUTE position.
func (m *PositionManager) checkPreSettlement(ctx context.Context, pos domain.Position) {
	if pos.EntryMode != domain.EntryModeFrontrun && pos.EntryMode != domain.EntryModeLastMinute {
		return
	}
	if pos.TP1Filled {
		slog.Debug("pre_settlement_skipped_tp1_filled", "symbol", pos.Symbol)
		return
	}

	fi, ok := m.fundingInfo.GetFundingInfo(pos.Symbol)
	if !ok || fi.NextFunding.IsZero() {
		return
	}

	checkBefore := time.Duration(m.cfg.PreSettlement.CheckBeforeMinutes) * time.Minute
	timeUntil := time.Until(fi.NextFunding)
	if timeUntil > checkBefore || timeUntil < 0 {
		return // not in the check window yet, or already past settlement
	}

	currentPrice, ok := m.market.GetPrice(pos.Symbol)
	if !ok || currentPrice == 0 {
		return
	}

	// raw price move against us (positive = price went up = bad for short)
	rawMoveAgainst := (currentPrice - pos.EntryPrice) / pos.EntryPrice

	threshold := preSettlementThreshold(pos.FundingRateAtEntry)

	if rawMoveAgainst > threshold {
		// Rule 1: emergency close to avoid paying funding fee on a losing position.
		slog.Warn("pre_settlement_emergency_close",
			"symbol", pos.Symbol,
			"loss_pct", rawMoveAgainst*100,
			"current_price", currentPrice,
			"entry_price", pos.EntryPrice,
			"funding_rate", pos.FundingRateAtEntry*100,
			"threshold_pct", threshold*100,
		)
		m.forceClose(ctx, pos, "pre_settlement_emergency_close")
		return
	}

	// Rule 2: widen TP1 to 2% + |funding_rate| if not yet done.
	if m.cfg.PreSettlement.WidenTPOnMiss && !pos.TPWidened && pos.TPOrderID != "" {
		m.widenTP(ctx, &pos, fi)
	}
}

// widenTP cancels the existing TP1 and replaces it at 2% + |funding_rate|.
func (m *PositionManager) widenTP(ctx context.Context, pos *domain.Position, _ *domain.FundingRate) {
	if pos.TP2OrderID != "" {
		m.adjustTP2PreSettlement(ctx, pos)
	}

	if err := m.executor.CancelOrder(ctx, pos.Symbol, pos.TPOrderID); err != nil {
		slog.Warn("widenTP1: cancel old TP failed", "symbol", pos.Symbol, "error", err)
		return
	}
	pos.TPOrderID = ""

	newTPPct := m.cfg.Execution.TpPct + math.Abs(pos.FundingRateAtEntry*100)
	newTP := pos.EntryPrice * (1 - newTPPct/100)

	oldTPPct := m.cfg.Execution.TpPct
	slog.Info("pre_settlement_tp_widened",
		"symbol", pos.Symbol,
		"old_tp_pct", oldTPPct,
		"new_tp_pct", newTPPct,
		"new_tp_price", newTP,
	)

	tp1SizeFrac := m.cfg.Execution.TP1SizePct / 100
	if pos.FundingRateAtEntry < -0.01 { // if funding less than 1% entry before settlement -> full exit at 2% final profit
		tp1SizeFrac = 1
	}
	tp1Qty := pos.OriginalQty * tp1SizeFrac

	newTPOrder, err := m.executor.PlaceLimitOrder(ctx, domain.OrderRequest{
		Symbol:     pos.Symbol,
		Side:       domain.SideBuy,
		Quantity:   tp1Qty,
		Price:      newTP,
		ReduceOnly: true,
	})
	if err != nil {
		slog.Error("widenTP1: place new TP failed", "symbol", pos.Symbol, "error", err)
		return
	}
	if newTPOrder != nil {
		pos.TPOrderID = newTPOrder.ClientOrderId
	}
	pos.TakeProfit = newTP
	pos.TPWidened = true

	if err := m.cache.SetActivePosition(ctx, *pos); err != nil {
		slog.Error("widenTP1: update position", "symbol", pos.Symbol, "error", err)
	}
}

// checkSettlementPassed detects when a funding settlement has passed while a position is open.
func (m *PositionManager) checkSettlementPassed(ctx context.Context, pos domain.Position) {
	if pos.FundingFeePaid || pos.SettlementPassedAt != nil {
		return // already marked
	}
	if pos.FundingRateAtEntry == 0 {
		return
	}

	fi, ok := m.fundingInfo.GetFundingInfo(pos.Symbol)
	if !ok || fi.NextFunding.IsZero() {
		return
	}

	// If next funding is > 50 minute away, a settlement must have just occurred.
	if time.Until(fi.NextFunding) > 50*time.Minute {
		m.onSettlementPassed(ctx, pos)
	}
}

// onSettlementPassed marks the position as having had the funding fee paid.
func (m *PositionManager) onSettlementPassed(ctx context.Context, pos domain.Position) {
	now := time.Now()
	pos.FundingFeePaid = true
	pos.FundingFeePaidPct = math.Abs(pos.FundingRateAtEntry)
	pos.SettlementPassedAt = &now
	pos.FundingFeePaidAmount = pos.FundingFeePaidPct * pos.Quantity * pos.EntryPrice

	slog.Info("position_settlement_passed",
		"symbol", pos.Symbol,
		"funding_rate", pos.FundingRateAtEntry*100,
		"fee_paid_pct", pos.FundingFeePaidPct*100,
	)

	if err := m.cache.SetActivePosition(ctx, pos); err != nil {
		slog.Error("onSettlementPassed: update position", "symbol", pos.Symbol, "error", err)
	}

	if m.notifier != nil {
		m.notifier.NotifyFundingSettlement(ctx, notify.FundingEvent{
			Symbol:      pos.Symbol,
			FundingRate: pos.FundingFeePaidPct * 100,
			FeePaid:     pos.FundingFeePaidAmount,
		})
	}
}

// adjustTP2PreSettlement widens the fixed TP2 by the funding rate magnitude
// to capture the post-settlement panic dump. e.g. funding=-1.5% → new TP2 = 4% + 1.5% = 5.5%.
func (m *PositionManager) adjustTP2PreSettlement(ctx context.Context, pos *domain.Position) {
	if err := m.executor.CancelOrder(ctx, pos.Symbol, pos.TP2OrderID); err != nil {
		slog.Warn("adjustTP2PreSettlement: cancel old TP2 failed", "symbol", pos.Symbol, "error", err)
		return
	}
	pos.TP2OrderID = ""

	if pos.FundingRateAtEntry < -0.01 {
		slog.Info("adjustTP2PreSettlement: full exit at TP1 for funding rate < -1% and entry before settlement", "symbol", pos.Symbol)
		return
	}

	newTP2Pct := m.cfg.Execution.TP2Pct + math.Abs(pos.FundingRateAtEntry*100)
	newTP2Price := pos.EntryPrice * (1 - newTP2Pct/100)

	tp2Qty := pos.OriginalQty * (1 - m.cfg.Execution.TP1SizePct/100)
	newTP2Order, err := m.executor.PlaceLimitOrder(ctx, domain.OrderRequest{
		Symbol:     pos.Symbol,
		Side:       domain.SideBuy,
		Quantity:   tp2Qty,
		Price:      newTP2Price,
		ReduceOnly: true,
	})
	if err != nil {
		slog.Error("adjustTP2PreSettlement: place new TP2 failed", "symbol", pos.Symbol, "error", err)
		return
	}
	if newTP2Order != nil {
		pos.TP2OrderID = newTP2Order.ClientOrderId
	}
	pos.TP2Price = newTP2Price

	slog.Info("tp2_widened_pre_settlement",
		"symbol", pos.Symbol,
		"old_tp2_pct", m.cfg.Execution.TP2Pct,
		"new_tp2_pct", newTP2Pct,
		"new_tp2_price", newTP2Price,
		"new_tp2_order", pos.TP2OrderID,
	)
}

// computeAvgClosePrice calculates weighted average exit price for split-leg closes.
func (m *PositionManager) computeAvgClosePrice(pos domain.Position, finalExitPrice float64) float64 {
	if !pos.TP1Filled || pos.OriginalQty == 0 {
		return finalExitPrice
	}
	tp1Qty := pos.OriginalQty * m.cfg.Execution.TP1SizePct / 100
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

func calcFinalPnl(pnl float64, position domain.Position) float64 {
	finalPnl := pnl
	feePaid := float64(0)
	if position.FundingFeePaid {
		feePaid = position.FundingFeePaidAmount
	}
	finalPnl -= feePaid
	return finalPnl
}

// RunFundingAvoidance starts the background loop that closes all positions at T-CloseBeforeMinutes
// to avoid paying the funding fee. Fires at most once per funding cycle.
func (m *PositionManager) RunFundingAvoidance(ctx context.Context) {
	if !m.cfg.FundingAvoidance.Enabled {
		slog.Info("funding avoidance disabled by config")
		return
	}
	go m.fundingAvoidanceLoop(ctx)
}

func (m *PositionManager) fundingAvoidanceLoop(ctx context.Context) {
	for {
		// Sleep until the next minute-55 mark.
		now := time.Now().UTC()
		next55 := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 55, 0, 0, time.UTC)
		if !now.Before(next55) {
			next55 = next55.Add(time.Hour)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(next55)):
		}
		m.checkFundingAvoidance(ctx)
	}
}

func (m *PositionManager) checkFundingAvoidance(ctx context.Context) {
	closeWindow := time.Duration(m.cfg.FundingAvoidance.CloseBeforeMinutes) * time.Minute

	positions, err := m.cache.GetActivePositions(ctx)
	if err != nil {
		slog.Error("funding_avoidance: get positions failed", "error", err)
		return
	}
	if len(positions) == 0 {
		return
	}

	for _, pos := range positions {
		fi, ok := m.fundingInfo.GetFundingInfo(pos.Symbol)
		if !ok || fi.NextFunding.IsZero() {
			slog.Warn("funding_avoidance: no funding info, skipping", "symbol", pos.Symbol)
			continue
		}

		if pos.SettlementPassedAt == nil && pos.EntryMode != domain.EntryModeAfter {
			continue
		}

		timeUntil := time.Until(fi.NextFunding)
		if timeUntil <= 0 || timeUntil > closeWindow {
			continue
		}

		slog.Warn("funding_avoidance: closing position",
			"symbol", pos.Symbol,
			"time_until_funding", timeUntil.Round(time.Second),
		)
		m.forceClose(ctx, pos, "funding_avoidance")
	}
}

// ——————————————————————————————————————————————————————————
// Protection sweep: self-heal positions whose entry fill confirmation
// (WS ORDER_TRADE_UPDATE + PendingEntry lookup) never arrived, leaving a
// stub position with no SL/TP, no DB trade record, and no notification.
// ——————————————————————————————————————————————————————————

// isUnprotectedStub reports whether pos was entered (an entry order was
// placed and a stub cached) but never finalized with SL/TP — and has been
// open at least graceDuration, so we don't race a normal in-flight WS fill.
func isUnprotectedStub(pos domain.Position, grace time.Duration, now time.Time) bool {
	if pos.SLOrderID != "" || pos.TPOrderID != "" {
		return false
	}
	if pos.TradeID == uuid.Nil {
		return false
	}
	return now.Sub(pos.OpenedAt) >= grace
}

// RunProtectionSweep starts the periodic sweep that detects and repairs
// unprotected entry stubs, so a missed WS fill event doesn't require a
// process restart to recover.
func (m *PositionManager) RunProtectionSweep(ctx context.Context) {
	if !m.cfg.ProtectionSweep.Enabled {
		slog.Info("protection sweep disabled by config")
		return
	}
	go m.protectionSweepLoop(ctx)
}

func (m *PositionManager) protectionSweepLoop(ctx context.Context) {
	interval := time.Duration(m.cfg.ProtectionSweep.IntervalSeconds) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.sweepUnprotectedPositions(ctx)
		}
	}
}

func (m *PositionManager) sweepUnprotectedPositions(ctx context.Context) {
	positions, err := m.cache.GetActivePositions(ctx)
	if err != nil {
		slog.Error("protection_sweep: get active positions failed", "error", err)
		return
	}
	grace := time.Duration(m.cfg.ProtectionSweep.GraceSeconds) * time.Second
	now := time.Now()
	for _, pos := range positions {
		if isUnprotectedStub(pos, grace, now) {
			m.repairUnprotectedPosition(ctx, pos.Symbol)
		}
	}
}

// repairUnprotectedPosition re-verifies the stub under the per-symbol lock
// (the same lock HandleUserDataEvent/check use) before acting, to avoid
// racing a concurrent WS fill that finalizes the position independently.
func (m *PositionManager) repairUnprotectedPosition(ctx context.Context, symbol string) {
	mu := m.lockPosition(symbol)
	defer mu.Unlock()

	pos, err := m.cache.GetActivePosition(ctx, symbol)
	if err != nil || pos == nil {
		return
	}
	grace := time.Duration(m.cfg.ProtectionSweep.GraceSeconds) * time.Second
	if !isUnprotectedStub(*pos, grace, time.Now()) {
		return // resolved concurrently (e.g. a WS fill landed) — nothing to do
	}

	pending, err := m.cache.GetPendingEntry(ctx, pos.TradeID.String())
	if err != nil {
		slog.Error("protection_sweep: get pending entry failed", "symbol", symbol, "error", err)
	}
	if pending != nil {
		slog.Warn("protection_sweep: recovering unprotected position via PendingEntry (tier 1)",
			"symbol", symbol, "trade_id", pos.TradeID.String(),
			"opened_secs_ago", time.Since(pos.OpenedAt).Seconds())
		m.finalizeSLTP(ctx, pending, pos.EntryPrice, pos.OriginalQty, pos.OpenedAt)
		return
	}

	slog.Error("protection_sweep: PendingEntry gone, falling back to emergency protect (tier 2)",
		"symbol", symbol, "trade_id", pos.TradeID.String(),
		"opened_secs_ago", time.Since(pos.OpenedAt).Seconds())
	m.emergencyProtect(ctx, *pos)
}

// emergencyProtect places SL/TP for a position whose PendingEntry has
// expired (or was never written), using conservative default percentages
// instead of the original LLM-decided TpPct. It reuses pos.TradeID (does
// NOT mint a new UUID) so the DB trade record shares identity with the
// already-cached position. This is the degraded "recovered without
// original decision context" path — callers must already hold the
// per-symbol lock.
func (m *PositionManager) emergencyProtect(ctx context.Context, pos domain.Position) {
	entryPrice := pos.EntryPrice
	qty := pos.OriginalQty
	if qty == 0 {
		qty = pos.Quantity
	}

	// Re-confirm against live exchange data — the stub's EntryPrice was only
	// a pre-fill market estimate taken at order-placement time.
	livePos, err := m.executor.GetPosition(ctx, pos.Symbol)
	if err != nil {
		slog.Warn("emergency_protect: could not re-confirm live position, using cached estimate",
			"symbol", pos.Symbol, "error", err)
	} else if livePos == nil {
		// No live position at all — the entry order never actually filled,
		// or the position was already closed by other means. Nothing to
		// protect; clean up the stale stub instead of fabricating SL/TP at
		// a bogus price.
		slog.Error("emergency_protect: no live exchange position found, removing stale stub",
			"symbol", pos.Symbol)
		_ = m.cache.RemovePosition(ctx, pos.Symbol)
		if m.notifier != nil {
			m.notifier.NotifyRiskEvent(ctx, notify.RiskEvent{
				Type: "emergency_protect_orphan",
				Message: fmt.Sprintf(
					"%s: cached unprotected stub had no matching live exchange position; removed from cache. Verify no residual exposure manually.",
					pos.Symbol),
			})
		}
		return
	} else {
		if livePos.EntryPrice > 0 {
			entryPrice = livePos.EntryPrice
		}
		if livePos.Quantity > 0 {
			qty = livePos.Quantity
		}
	}

	slDistance := entryPrice * (m.cfg.Execution.SlPct / 100)
	stopLoss := entryPrice + slDistance // SHORT: SL above entry
	tpDistance := entryPrice * (m.cfg.Execution.TpPct / 100)
	takeProfit := entryPrice - tpDistance // SHORT: TP below entry

	slOrder, slErr := m.executor.PlaceStopMarketOrder(ctx, domain.OrderRequest{
		Symbol:        pos.Symbol,
		Side:          domain.SideBuy,
		Type:          domain.OrderTypeStopMarket,
		TriggerPrice:  stopLoss,
		ClosePosition: true,
	})
	tpOrder, tpErr := m.executor.PlaceLimitOrder(ctx, domain.OrderRequest{
		Symbol:     pos.Symbol,
		Side:       domain.SideBuy,
		Quantity:   qty,
		Price:      takeProfit,
		ReduceOnly: true,
	})

	if slErr != nil || tpErr != nil {
		// Reuse the existing PendingProtection retry machinery (already
		// polled every 1s by check()) instead of a third bespoke retry loop.
		pp := domain.PendingProtection{
			Symbol:     pos.Symbol,
			NeedsSL:    slErr != nil,
			NeedsTP:    tpErr != nil,
			EntryPrice: entryPrice,
			Quantity:   qty,
			TP1Qty:     qty,
			StopLoss:   stopLoss,
			TakeProfit: takeProfit,
		}
		if err := m.cache.SetPendingProtection(ctx, pp); err != nil {
			slog.Error("emergency_protect: set pending protection", "symbol", pos.Symbol, "error", err)
		}
		slog.Error("emergency_protect: SL and/or TP placement failed, handed off to retryProtection",
			"symbol", pos.Symbol, "sl_err", slErr, "tp_err", tpErr)
	}

	pos.EntryPrice = entryPrice
	pos.Quantity = qty
	pos.OriginalQty = qty
	pos.StopLoss = stopLoss
	pos.TakeProfit = takeProfit
	pos.HighSinceEntry = entryPrice
	pos.LowSinceEntry = entryPrice
	if slErr == nil && slOrder != nil {
		pos.SLOrderID = slOrder.ClientOrderId
	}
	if tpErr == nil && tpOrder != nil {
		pos.TPOrderID = tpOrder.ClientOrderId
	}
	if err := m.cache.SetActivePosition(ctx, pos); err != nil {
		slog.Error("emergency_protect: update position", "symbol", pos.Symbol, "error", err)
	}

	trade := &domain.Trade{
		ID:         pos.TradeID,
		Symbol:     pos.Symbol,
		Side:       pos.Side,
		EntryMode:  pos.EntryMode,
		Leverage:   pos.Leverage,
		EntryPrice: entryPrice,
		Quantity:   qty,
		IsPaper:    pos.IsPaper,
		CreatedAt:  pos.OpenedAt,
	}
	if err := m.tradeRepo.Insert(ctx, trade); err != nil {
		slog.Error("emergency_protect: insert trade record", "symbol", pos.Symbol, "error", err)
	}

	slog.Warn("emergency_protect: position recovered without original decision context",
		"symbol", pos.Symbol,
		"entry", entryPrice,
		"sl", stopLoss,
		"tp", takeProfit,
		"qty", qty,
	)

	if m.notifier != nil {
		m.notifier.NotifyRiskEvent(ctx, notify.RiskEvent{
			Type: "emergency_protect",
			Message: fmt.Sprintf(
				"%s: protection sweep recovered an unprotected position WITHOUT original decision context (PendingEntry expired). "+
					"Applied default SL %.8g / TP %.8g at entry %.8g, qty %.8g. Verify manually.",
				pos.Symbol, stopLoss, takeProfit, entryPrice, qty),
		})
	}
}

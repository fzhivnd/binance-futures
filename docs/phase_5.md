# Phase 5: LLM Force Stop-Loss + Split TP / Trailing Stop

---

## 1. Overview

Phase 5 enhances the trade management layer with intelligent position monitoring and optimized exit strategy:

1. **LLM Force Stop-Loss** — After a position is open for 5 minutes, an LLM evaluates every 1 minute whether to force-close before the hard SL (5%) is hit.

2. **Split TP + Trailing Stop** — TP closes only 50% of the position at the base target. The remaining 50% uses Binance's native trailing stop (`TRAILING_STOP_MARKET`, 0.5% callback rate) to ride the move further.

3. **Enhanced Trade Record** — Track partial fills, dual-leg PnL, and force-SL metadata.

### Current Behavior (Phase 3-4)

```
OPEN → Hard SL (5%) placed as STOP_MARKET (100% qty)
     → Full TP placed as TAKE_PROFIT (100% qty)
     → Breakeven: after 1% profit + 5min, SL moves to entry
     → Close: whichever order fills first ends the position
```

### New Behavior (Phase 5)

```
OPEN → Hard SL (5%) placed as STOP_MARKET (100% qty — backstop, unchanged)
     → TP1 placed as TAKE_PROFIT (50% qty) — partial close
     → After 5min: LLM Force-SL check starts (every 1min)
     → If LLM says FORCE_CLOSE → market close remaining position immediately
     → If TP1 fills:
         → Cancel hard SL
         → Place trailing stop on remaining 50% (0.5% callback rate)
         → Place new hard SL on remaining 50% at breakeven (entry price)
     → If trailing fills → position fully closed
```

### Key Principles

1. **Hard SL is always the backstop** — LLM force-SL is an optimization that exits *before* hitting 5%
2. **TP1 only closes half** — lets winners run via trailing
3. **Trailing uses Binance native API** — `TRAILING_STOP_MARKET` order type with `callbackRate`
4. **Trade record tracks partial fills** — PnL is computed from both legs

### What Changes

| Before (Phase 4)                           | After (Phase 5)                                          |
|--------------------------------------------|----------------------------------------------------------|
| Hard SL only (5%)                          | LLM force-SL check every 1m after 5m hold               |
| Single TP closes 100% position             | TP1 closes 50%, remaining 50% trails with 0.5% callback |
| Trade record has single exit               | Trade record tracks partial fills (TP1 + trailing)       |
| Breakeven moves SL to entry               | Breakeven is built into trailing portion's safety SL     |

### What Stays the Same

- Full Phase 1-4 pipeline unchanged (scan → filter → score → memory → LLM → intent → execute)
- Hard SL (5%) remains as absolute backstop
- Trade memory engine records enriched outcomes from split exits
- Risk engine, intent queue: unchanged

---

## 2. Tech Stack Additions

| Component        | Choice                       | Reason                                            |
|------------------|------------------------------|---------------------------------------------------|
| Trailing Order   | Binance `TRAILING_STOP_MARKET` | Native exchange support, no client-side tracking |
| Force-SL Model   | GPT-4.1-mini (same as Phase 3) | Fast (5s timeout), cheap, structured JSON      |
| Price Tracking   | Position state in Redis      | Track high/low since entry for LLM context       |

### Cost Estimate (Force-SL LLM calls)

| Positions/day | Checks/position | Calls/day | Cost/call  | Monthly    |
|---------------|-----------------|-----------|------------|------------|
| 3-6           | ~3-5 (PnL-gated) | ~15-25  | ~$0.0006   | ~$0.45-0.75 |
| 6-12          | ~3-5            | ~30-50    | ~$0.0006   | ~$0.90-1.50 |

---

## 3. Project Structure (New/Modified Files)

```
internal/
├── llm/
│   ├── force_sl.go            # Force-SL engine: LLM call, prompt, parse response
│   ├── schema.go              # + ForceSLRequest, ForceSLResponse types
│   └── prompt.go              # + forceSLSystemPrompt constant
├── execution/
│   ├── engine.go              # Modified: TP1 at 50% qty instead of 100%
│   ├── executor.go            # + PlaceTrailingStopOrder interface method
│   ├── position_manager.go    # Modified: force-SL check, handleTP1Fill, handleTrailingFill
│   ├── live_executor.go       # + trailing stop Binance API implementation
│   └── paper_executor.go      # + trailing stop simulation
├── domain/
│   ├── position.go            # + OriginalQty, TP1Filled, TrailingOrderID, price tracking fields
│   ├── trade.go               # + TP1/Trail split fields, ForceSL fields
│   └── order.go               # + OrderTypeTrailingStop
├── storage/
│   └── trade_repo.go          # + UpdateSplitResult, UpdateForceSL
├── config/
│   └── config.go              # + trailing/force-SL config fields

migrations/
├── 006_add_split_tp_fields.up.sql
└── 006_add_split_tp_fields.down.sql
```

---

## 4. System Design: Force Stop-Loss

### Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                     POSITION MANAGER (enhanced)                   │
├─────────────────────────────────────────────────────────────────┤
│                                                                   │
│  Every 1 second:                                                  │
│    for each active position:                                      │
│      ├── track high/low since entry                               │
│      ├── if position age < 5min → skip force-SL check            │
│      ├── if position age >= 5min AND not checked this minute:     │
│      │     └── call LLM Force-SL Engine                           │
│      │           ├── HOLD → continue                              │
│      │           └── FORCE_CLOSE → market close immediately       │
│      ├── breakeven logic (existing, modified for split TP)        │
│      └── update position state                                    │
│                                                                   │
└─────────────────────────────────────────────────────────────────┘
```

### Force-SL LLM Call Flow

```
Position open ≥ 5 minutes
        │
        ▼
┌─────────────────────────────┐
│  Gather Current State:       │
│  - Current unrealized PnL %  │
│  - Current mark price         │
│  - Price action since entry   │
│  - BTC current context        │
│  - Time in position           │
│  - Original entry reasons     │
└──────────────┬──────────────┘
               │
               ▼
┌─────────────────────────────┐
│  LLM Force-SL Decision       │
│  Model: gpt-4.1-mini         │
│  Timeout: 5s (fast)          │
│  Response: HOLD | FORCE_CLOSE │
└──────────────┬──────────────┘
               │
       ┌───────┴───────┐
       ▼               ▼
   ┌───────┐     ┌───────────────┐
   │ HOLD  │     │ FORCE_CLOSE   │
   │       │     │ → market close │
   │ (noop)│     │ → record trade │
   └───────┘     └───────────────┘
```

### Force-SL Check Interval (PnL-Gated + Escalating)

- **Start**: 5 minutes after position opens
- **PnL Gate**: no LLM call if unrealized PnL >= -0.5% (trade is winning or barely negative)
- **Escalating frequency**:
  - PnL between -0.5% and -2% → check every 5 minutes
  - PnL worse than -2% → check every 1 minute
- **Throttle**: use `lastForceSLCheck` timestamp to enforce interval per position
- **Stop**: when position closes (by SL, TP, trailing, or force-close)

```
Position tick (every 1min):
  if holdTime < 5min → skip
  if unrealizedPnlPct >= -0.5% → skip (winning/flat, no need to check)
  if unrealizedPnlPct between -0.5% and -2%:
      if timeSinceLastCheck < 5min → skip
      else → call LLM
  if unrealizedPnlPct < -2%:
      if timeSinceLastCheck < 1min → skip
      else → call LLM
```

### Why This Approach?

- **PnL gate** eliminates calls on winning trades (majority of checks saved)
- **Escalating intervals** focus attention where it matters — trades heading toward hard SL
- **Average calls per position drops from ~25 to ~3-5** while maintaining responsiveness on bad trades
- **5min start delay** unchanged — positions need time to develop; immediate post-entry volatility shouldn't trigger force-close
- Matches the existing breakeven check delay (5min)

---

## 5. Force-SL LLM Contract

### 5.1 Request Schema

```go
// internal/llm/schema.go

type ForceSLRequest struct {
    Symbol            string  `json:"symbol"`
    EntryPrice        float64 `json:"entry_price"`
    CurrentPrice      float64 `json:"current_price"`
    UnrealizedPnlPct  float64 `json:"unrealized_pnl_pct"`  // at 20x leverage
    HoldMinutes       int     `json:"hold_minutes"`
    HardSLPrice       float64 `json:"hard_sl_price"`
    HardSLDistancePct float64 `json:"hard_sl_distance_pct"` // how far from current price to SL
    EntryMode         string  `json:"entry_mode"`
    OriginalConfidence int    `json:"original_confidence"`
    EntryReasons      []string `json:"entry_reasons"`
    PriceAction       ForceSLPriceAction `json:"price_action"`
    BTCContext        LLMBTCContext `json:"btc_context"`
}

type ForceSLPriceAction struct {
    HighSinceEntry    float64 `json:"high_since_entry_pct"`    // max adverse move since entry
    LowSinceEntry     float64 `json:"low_since_entry_pct"`     // max favorable move since entry
    CurrentTrend5m    string  `json:"current_trend_5m"`        // "up" | "down" | "sideways"
    MomentumShift     bool    `json:"momentum_shift"`          // true if reversal forming against us
    VolumeIncreasing  bool    `json:"volume_increasing"`       // buying volume increasing (bad for short)
}
```

### 5.2 Response Schema

```go
type ForceSLResponse struct {
    Action string `json:"action"` // "HOLD" | "FORCE_CLOSE"
    Reason string `json:"reason"` // concise explanation
}
```

### 5.3 JSON Schema for Structured Output

```json
{
  "name": "force_sl_decision",
  "strict": true,
  "schema": {
    "type": "object",
    "properties": {
      "action": {
        "type": "string",
        "enum": ["HOLD", "FORCE_CLOSE"]
      },
      "reason": {
        "type": "string",
        "description": "1-2 sentence explanation of the decision"
      }
    },
    "required": ["action", "reason"],
    "additionalProperties": false
  }
}
```

---

## 6. Force-SL Prompt Design

### System Prompt

```
You are a position management AI for a Binance Futures funding-rate shorting bot. Your ONLY job is to decide whether to force-close an open SHORT position early (before the hard stop-loss is hit) or hold.

CONTEXT:
- We are SHORT (profit when price goes down)
- Hard SL is set at 5% adverse price move (will trigger automatically if reached)
- Your job: detect when the trade thesis has INVALIDATED and close early to limit loss
- Force-closing at -2% is much better than waiting for -5% hard SL if the setup is dead

WHEN TO FORCE_CLOSE:
- Strong momentum building AGAINST us (sustained buying, not just a wick)
- BTC has started a breakout that will drag the alt up
- Price has consolidated above entry with increasing volume (buyers absorbing sells)
- The original entry thesis (funding reversal, exhaustion) has clearly failed
- Price made a higher high after entry and is holding above it

WHEN TO HOLD:
- Price is just ranging/consolidating around entry (normal noise)
- Temporary wick above entry but price returned
- We are already in profit (price below entry)
- The original thesis is still intact (no momentum shift)
- Current loss is small (<1% unrealized) and no clear directional signal
- BTC is not in breakout mode

BIAS: Lean toward HOLD unless the evidence for thesis invalidation is clear. Forcing a close on noise is expensive (fees + missed recovery). But don't stubbornly hold into a -3% loss when momentum is clearly against us — the hard SL at -5% is the absolute worst case, not the target.

CRITICAL: You are evaluating at 20x leverage. A 2% price move = 40% account impact on this position.
```

### User Message Template

```
Position: {{symbol}} SHORT
Entry: ${{entry_price}} | Current: ${{current_price}}
Unrealized PnL: {{unrealized_pnl_pct}}% (at 20x leverage)
Hold time: {{hold_minutes}} minutes
Hard SL at: ${{hard_sl_price}} ({{hard_sl_distance_pct}}% away)
Entry mode: {{entry_mode}} | Original confidence: {{original_confidence}}/100

Original entry reasons:
{{#each entry_reasons}}
- {{this}}
{{/each}}

=== PRICE ACTION SINCE ENTRY ===
Max adverse move: +{{high_since_entry_pct}}% (against us)
Max favorable move: -{{low_since_entry_pct}}% (in our favor)
Current 5m trend: {{current_trend_5m}}
Momentum shift (reversal against us): {{momentum_shift}}
Volume increasing (buying pressure): {{volume_increasing}}

=== BTC CONTEXT ===
Trend: {{btc_trend}} | Momentum: {{btc_momentum}}/100
Breakout: {{btc_breakout}} | RSI(14): {{btc_rsi}}

Should we FORCE_CLOSE this position now or HOLD?
```

### Example: Force Close Scenario

```
Position: 1000PEPEUSDT SHORT
Entry: $0.01234 | Current: $0.01261
Unrealized PnL: -43.8% (at 20x leverage)   ← note: -2.19% price move × 20x
Hold time: 8 minutes
Hard SL at: $0.01296 (2.78% away)
Entry mode: LAST_MINUTE | Original confidence: 78/100

Original entry reasons:
- Extreme funding -0.85% with OI rising +18%
- Strong bearish candle confluence (1h shooting star + 30m engulfing)
- RSI 71 with momentum loss signal

=== PRICE ACTION SINCE ENTRY ===
Max adverse move: +2.19% (against us)
Max favorable move: -0.12% (in our favor)
Current 5m trend: up
Momentum shift (reversal against us): true
Volume increasing (buying pressure): true

=== BTC CONTEXT ===
Trend: bullish | Momentum: 72/100
Breakout: true | RSI(14): 68.5

Should we FORCE_CLOSE this position now or HOLD?
```

Expected response:
```json
{
  "action": "FORCE_CLOSE",
  "reason": "BTC breakout at momentum 72 is dragging alt up. Price never dipped below entry and is trending up with increasing volume. Original thesis (momentum exhaustion) invalidated. Close at -2.2% before hard SL at -5%."
}
```

---

## 7. Force-SL Implementation

### 7.1 Force-SL Engine (`internal/llm/force_sl.go`)

```go
package llm

import (
    "context"
    "encoding/json"
    "log/slog"
    "time"
)

type ForceSLEngine struct {
    client *Client
}

func NewForceSLEngine(client *Client) *ForceSLEngine {
    return &ForceSLEngine{client: client}
}

// EvaluatePosition decides whether to force-close an open position.
// Uses a shorter timeout (5s) since this runs every minute per position.
func (e *ForceSLEngine) EvaluatePosition(
    ctx context.Context,
    req *ForceSLRequest,
) (*ForceSLResponse, error) {
    ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
    defer cancel()

    userMsg := buildForceSLUserMessage(req)
    raw, err := e.client.Call(ctx, forceSLSystemPrompt, userMsg)
    if err != nil {
        slog.Warn("force-SL LLM call failed, defaulting to HOLD", "error", err)
        return &ForceSLResponse{Action: "HOLD", Reason: "LLM unavailable"}, nil
    }

    var resp ForceSLResponse
    if err := json.Unmarshal([]byte(raw), &resp); err != nil {
        slog.Warn("force-SL response parse failed, defaulting to HOLD", "error", err)
        return &ForceSLResponse{Action: "HOLD", Reason: "Parse error"}, nil
    }

    slog.Info("force_sl_check",
        "symbol", req.Symbol,
        "action", resp.Action,
        "reason", resp.Reason,
        "unrealized_pnl", req.UnrealizedPnlPct,
        "hold_minutes", req.HoldMinutes,
    )

    return &resp, nil
}
```

### 7.2 Position Manager Enhancement (`internal/execution/position_manager.go`)

```go
// New fields in PositionManager
type PositionManager struct {
    // ... existing fields ...
    forceSLEngine *llm.ForceSLEngine
    indEngine     IndicatorEngine // for current BTC context
}

func (m *PositionManager) check(ctx context.Context, pos domain.Position) {
    currentPrice, ok := m.market.GetPrice(pos.Symbol)
    if !ok || currentPrice == 0 {
        return
    }

    // Track high/low since entry
    updated := false
    if currentPrice > pos.HighSinceEntry {
        pos.HighSinceEntry = currentPrice
        updated = true
    }
    if pos.LowSinceEntry == 0 || currentPrice < pos.LowSinceEntry {
        pos.LowSinceEntry = currentPrice
        updated = true
    }

    // For SHORT: profit when price falls below entry
    unrealizedPnlPct := (pos.EntryPrice - currentPrice) / pos.EntryPrice * 100

    // === FORCE-SL CHECK ===
    // Start after 5 minutes, check every 1 minute
    if m.forceSLEngine != nil &&
        m.cfg.Execution.ForceSLEnabled &&
        time.Since(pos.OpenedAt) >= time.Duration(m.cfg.Execution.ForceSLStartMin)*time.Minute &&
        time.Since(pos.LastForceSLCheck) >= time.Duration(m.cfg.Execution.ForceSLIntervalSec)*time.Second &&
        !pos.TP1Filled { // don't force-close if TP1 already hit (trailing takes over)

        decision := m.checkForceSL(ctx, pos, currentPrice, unrealizedPnlPct)
        if decision != nil && decision.Action == "FORCE_CLOSE" {
            m.forceClose(ctx, pos, currentPrice, decision.Reason)
            return
        }
        pos.LastForceSLCheck = time.Now()
        pos.ForceSLCount++
        updated = true
    }

    if updated {
        _ = m.cache.SetActivePosition(ctx, pos)
    }

    // === BREAKEVEN LOGIC (existing, only applies before TP1) ===
    if !pos.BreakevenMoved && !pos.TP1Filled {
        leveragedPnl := unrealizedPnlPct * float64(pos.Leverage)
        if leveragedPnl > m.cfg.Execution.BreakevenActivationPct &&
            time.Since(pos.OpenedAt) >= 5*time.Minute {
            // ... existing breakeven logic ...
        }
    }
}

func (m *PositionManager) checkForceSL(
    ctx context.Context,
    pos domain.Position,
    currentPrice float64,
    unrealizedPnlPct float64,
) *llm.ForceSLResponse {
    priceAction := m.buildPriceAction(pos, currentPrice)

    btc, _ := m.indEngine.ComputeBTCContext(ctx)

    slDistance := (pos.StopLoss - currentPrice) / currentPrice * 100

    req := &llm.ForceSLRequest{
        Symbol:             pos.Symbol,
        EntryPrice:         pos.EntryPrice,
        CurrentPrice:       currentPrice,
        UnrealizedPnlPct:   unrealizedPnlPct * float64(pos.Leverage),
        HoldMinutes:        int(time.Since(pos.OpenedAt).Minutes()),
        HardSLPrice:        pos.StopLoss,
        HardSLDistancePct:  slDistance,
        EntryMode:          string(pos.EntryMode),
        OriginalConfidence: pos.OriginalConfidence,
        EntryReasons:       pos.LLMEntryReasons,
        PriceAction:        priceAction,
    }
    if btc != nil {
        req.BTCContext = llm.LLMBTCContext{
            Trend:         btc.Trend,
            MomentumScore: btc.MomentumScore,
            Volatility:    btc.Volatility,
            IsBreakout:    btc.IsBreakout,
            RSI:           btc.RSI14_1h,
            PriceChange1h: btc.PriceChange1h,
        }
    }

    resp, err := m.forceSLEngine.EvaluatePosition(ctx, req)
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
    m.recordForceSLClose(ctx, pos, exitPrice, reason)

    slog.Info("force_sl_executed",
        "symbol", pos.Symbol,
        "entry", pos.EntryPrice,
        "exit", exitPrice,
        "reason", reason,
        "hold_minutes", int(time.Since(pos.OpenedAt).Minutes()),
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

func (m *PositionManager) buildPriceAction(pos domain.Position, currentPrice float64) llm.ForceSLPriceAction {
    highPct := (pos.HighSinceEntry - pos.EntryPrice) / pos.EntryPrice * 100
    lowPct := (pos.EntryPrice - pos.LowSinceEntry) / pos.EntryPrice * 100

    return llm.ForceSLPriceAction{
        HighSinceEntry:   highPct,
        LowSinceEntry:    lowPct,
        CurrentTrend5m:   detectTrend5m(pos.Symbol),
        MomentumShift:    detectMomentumShift(pos),
        VolumeIncreasing: detectVolumeIncrease(pos.Symbol),
    }
}
```

---

## 8. System Design: Split TP + Trailing Stop

### Order Structure at Entry

```
Position opened: 1000PEPEUSDT SHORT, qty=1000, entry=$0.01234

Orders placed:
  1. STOP_MARKET (SL):     qty=1000 (100%), trigger=$0.01296 (entry + 5%)     ← backstop
  2. TAKE_PROFIT (TP1):    qty=500  (50%),  trigger=$0.01210 (entry - 2%)     ← partial close
```

### After TP1 Fills (50% closed)

```
Position remaining: qty=500 (50% of original)

TP1 fills → event from user data stream → PositionManager handles:
  1. Cancel old STOP_MARKET (was for 1000 qty)
  2. Place TRAILING_STOP_MARKET: qty=500, callbackRate=0.5%                    ← trail the move
  3. Place STOP_MARKET (breakeven SL): qty=500, trigger=entry price             ← protect profit

Net outcome:
  - TP1 profit: locked in at ~2% on 50% of position
  - Trailing: rides further dump with 0.5% callback (if price reverses 0.5% from low, closes)
  - Breakeven SL: if price somehow pumps back to entry, closes remaining at ~breakeven
```

### Trailing Stop Behavior (Binance TRAILING_STOP_MARKET)

```
For SHORT positions:
  - Activation: immediate (activationPrice not set → active from placement)
  - Tracks: lowest price since activation
  - Trigger: when price rises 0.5% from the lowest point
  - Example:
      Entry: $0.01234
      TP1 fills at: $0.01210 (2% below entry)
      Price keeps dropping to: $0.01180 (trailing tracks this low)
      Price bounces to: $0.01186 (+0.5% from low)
      → Trailing triggers, fills at market ≈ $0.01186
      → Extra profit: 3.89% on the trailing portion
```

### Binance API: TRAILING_STOP_MARKET

```go
// Executor interface addition
type Executor interface {
    // ... existing methods ...
    PlaceTrailingStopOrder(ctx context.Context, req TrailingStopRequest) (*domain.OrderResult, error)
}

type TrailingStopRequest struct {
    Symbol       string
    Side         domain.Side // BUY (to close short)
    Quantity     float64
    CallbackRate float64     // 0.5 = 0.5%
    ReduceOnly   bool
}
```

Binance API call:
```
POST /fapi/v1/order
{
  "symbol": "1000PEPEUSDT",
  "side": "BUY",
  "type": "TRAILING_STOP_MARKET",
  "quantity": "500",
  "callbackRate": "0.5",
  "reduceOnly": "true"
}
```

---

## 9. Split TP Implementation

### 9.1 Modified Execution Engine (`internal/execution/engine.go`)

```go
func (e *ExecutionEngine) executeInternalWithTP(ctx context.Context, candidate domain.Candidate, window scheduler.WindowType, positionSizePct float64, confidence int, tpPct float64, llmDecision *domain.LLMDecision) error {
    // ... pre-checks (unchanged) ...
    // ... balance + qty calculation (unchanged) ...
    // ... set leverage (unchanged) ...
    // ... place market order (unchanged) ...

    entryPrice := order.FillPrice
    if entryPrice == 0 {
        entryPrice = candidate.MarkPrice
    }

    // SL: 100% quantity (full backstop — always)
    slDistance := entryPrice * (e.cfg.Execution.SlPct / 100)
    stopLoss := entryPrice + slDistance

    slOrder, err := e.executor.PlaceStopMarketOrder(ctx, domain.OrderRequest{
        Symbol:     candidate.Symbol,
        Side:       domain.SideBuy,
        Type:       domain.OrderTypeStopMarket,
        Quantity:   order.Quantity, // 100% qty
        StopPrice:  stopLoss,
        ReduceOnly: true,
    })
    if err != nil {
        slog.Error("place SL order failed", "symbol", candidate.Symbol, "error", err)
    }

    // TP1: 50% quantity (partial take-profit)
    tp1Pct := e.cfg.Execution.TP1SizePct / 100 // 0.5
    tp1Qty := order.Quantity * tp1Pct
    tpDistance := entryPrice * (tpPct / 100)
    if window == scheduler.WindowFrontrun || window == scheduler.WindowLastMinute {
        tpDistance += entryPrice * (-candidate.FundingRate)
    }
    takeProfit := entryPrice - tpDistance

    tpLimit := takeProfit * 0.99
    tpOrder, err := e.executor.PlaceStopLimitOrder(ctx, domain.OrderRequest{
        Symbol:     candidate.Symbol,
        Side:       domain.SideBuy,
        Type:       domain.OrderTypeTakeProfit,
        Quantity:   tp1Qty, // 50% qty
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
        TP1Filled:          false,
        HighSinceEntry:     entryPrice,
        LowSinceEntry:      entryPrice,
        OriginalConfidence: confidence,
    }
    if llmDecision != nil {
        pos.LLMEntryReasons = llmDecision.EntryReasons
        pos.OriginalConfidence = llmDecision.Confidence
    }

    // ... trade record insert + cache set (unchanged) ...

    slog.Info("position opened (split TP)",
        "symbol", candidate.Symbol,
        "entry", entryPrice,
        "sl", stopLoss,
        "tp1", takeProfit,
        "qty_total", order.Quantity,
        "qty_tp1", tp1Qty,
        "qty_trail", order.Quantity-tp1Qty,
    )
    return nil
}
```

### 9.2 TP1 Fill Handler (Position Manager)

When the user data stream reports TP1 filled, the position manager places the trailing stop:

```go
func (m *PositionManager) HandleUserDataEvent(event exchange.UserDataEvent) {
    o := event.Order
    if o.OrderStatus != "FILLED" {
        return
    }

    ctx := context.Background()
    positions, err := m.cache.GetActivePositions(ctx)
    if err != nil {
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

    // Route by order ID
    switch o.OrderID {
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
    }
}

func (m *PositionManager) handleTP1Fill(ctx context.Context, pos *domain.Position, o exchange.OrderData) {
    now := time.Now()
    pos.TP1Filled = true
    pos.TP1FillPrice = o.AvgPrice
    pos.TP1FilledAt = &now

    remainingQty := pos.OriginalQty - o.FilledQty

    slog.Info("tp1_filled",
        "symbol", pos.Symbol,
        "fill_price", o.AvgPrice,
        "filled_qty", o.FilledQty,
        "remaining_qty", remainingQty,
    )

    // 1. Cancel the old hard SL (was for 100% qty)
    if pos.SLOrderID != "" {
        _ = m.executor.CancelOrder(ctx, pos.Symbol, pos.SLOrderID)
        pos.SLOrderID = ""
    }

    // 2. Place trailing stop on remaining qty (0.5% callback)
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
    pos.TrailingOrderID = trailingOrder.OrderID

    // 3. Place breakeven SL for trailing portion (entry price as safety)
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
    } else {
        pos.TrailingSLOrderID = beOrder.OrderID
    }

    // 4. Update position in cache
    pos.Quantity = remainingQty
    _ = m.cache.SetActivePosition(ctx, *pos)

    slog.Info("trailing_stop_placed",
        "symbol", pos.Symbol,
        "trailing_order", pos.TrailingOrderID,
        "callback_rate", m.cfg.Execution.TrailingCallbackRate,
        "remaining_qty", remainingQty,
    )
}

func (m *PositionManager) handleTrailingFill(ctx context.Context, pos *domain.Position, o exchange.OrderData) {
    if pos.TrailingSLOrderID != "" {
        _ = m.executor.CancelOrder(ctx, pos.Symbol, pos.TrailingSLOrderID)
    }
    m.recordSplitClose(ctx, *pos, o.AvgPrice, "WIN")
}

func (m *PositionManager) handleTrailingSLFill(ctx context.Context, pos *domain.Position, o exchange.OrderData) {
    if pos.TrailingOrderID != "" {
        _ = m.executor.CancelOrder(ctx, pos.Symbol, pos.TrailingOrderID)
    }
    m.recordSplitClose(ctx, *pos, o.AvgPrice, "BREAKEVEN")
}

func (m *PositionManager) handleHardSLFill(ctx context.Context, pos *domain.Position, o exchange.OrderData) {
    if pos.TPOrderID != "" {
        _ = m.executor.CancelOrder(ctx, pos.Symbol, pos.TPOrderID)
    }
    m.recordClose(ctx, *pos, o.AvgPrice, "LOSS")
}
```

---

## 10. Trade Record Enhancement

### 10.1 Updated Domain Types

```go
// internal/domain/position.go

type Position struct {
    Symbol         string
    Side           Side
    EntryPrice     float64
    Quantity       float64
    OriginalQty    float64 // full quantity at entry
    Leverage       int
    EntryMode      EntryMode
    MarkPrice      float64
    StopLoss       float64
    TakeProfit     float64
    SLOrderID      string
    TPOrderID      string
    TradeID        uuid.UUID
    OpenedAt       time.Time
    IsPaper        bool
    BreakevenMoved bool

    // Phase 5: Split TP
    TP1Filled         bool
    TP1FillPrice      float64
    TP1FilledAt       *time.Time
    TrailingOrderID   string
    TrailingSLOrderID string

    // Phase 5: Force-SL
    LastForceSLCheck   time.Time
    ForceSLCount       int
    LLMEntryReasons    []string
    OriginalConfidence int

    // Phase 5: Price tracking
    HighSinceEntry float64
    LowSinceEntry  float64
}
```

```go
// internal/domain/trade.go

type Trade struct {
    // ... existing fields ...

    // Phase 5: Simplified close record
    CloseReason    *string  // "TP_TRAIL" | "FORCE_SL" | "HARD_SL" | "MANUAL"
    AvgClosePrice  *float64 // weighted average close price across all legs
    // PnL and Result fields already exist from Phase 3

    // Result values: "WIN" | "LOSS" | "BREAKEVEN" | "FORCE_SL" | "PARTIAL_WIN" | "MANUAL"
}
```

**Design decision**: No per-leg columns (tp1_exit_price, trail_exit_price, etc.) in the trades table. The trade record stores only:
- `avg_close_price` — weighted average exit price across all fills
- `pnl` — total PnL (sum of all legs)
- `close_reason` — why the trade closed

Detailed per-leg breakdown is unnecessary for the memory engine and analytics. The `avg_close_price` + `pnl` + `close_reason` triple captures the full outcome.

### 10.2 Average Close Price Calculation

```go
func computeAvgClosePrice(pos domain.Position, finalExitPrice float64) float64 {
    if !pos.TP1Filled {
        // Single exit (hard SL, force-SL, or manual) — exit price is the close
        return finalExitPrice
    }
    // TP1 filled + second leg exit → weighted average
    tp1Qty := pos.OriginalQty * (tp1SizePct / 100)
    trailQty := pos.OriginalQty - tp1Qty
    return (pos.TP1FillPrice*tp1Qty + finalExitPrice*trailQty) / pos.OriginalQty
}
```

### 10.3 Result Classification

| Scenario                                    | Result         | CloseReason  | PnL Calculation                              |
|---------------------------------------------|----------------|--------------|----------------------------------------------|
| Hard SL fills (100% qty, before TP1)       | `LOSS`         | `HARD_SL`    | (entry - exit) × qty                         |
| Force-SL by LLM (before TP1)              | `FORCE_SL`     | `FORCE_SL`   | (entry - exit) × qty                         |
| TP1 fills + trailing fills                 | `WIN`          | `TP_TRAIL`   | (entry - avgClose) × totalQty                |
| TP1 fills + breakeven SL fills             | `PARTIAL_WIN`  | `TP_TRAIL`   | (entry - avgClose) × totalQty                |
| TP1 fills + hard SL on trailing portion    | `PARTIAL_WIN`  | `TP_TRAIL`   | (entry - avgClose) × totalQty                |
| Manual close detected                       | `MANUAL`       | `MANUAL`     | (entry - exit) × remaining qty               |

### 10.4 recordClose Implementation

```go
func (m *PositionManager) recordClose(ctx context.Context, pos domain.Position, finalExitPrice float64, closeReason string) {
    avgClose := computeAvgClosePrice(pos, finalExitPrice)
    pnl := (pos.EntryPrice - avgClose) * pos.OriginalQty

    result := classifyResult(pos, pnl, closeReason)

    now := time.Now()
    if err := m.tradeRepo.UpdateResult(ctx, pos.TradeID, domain.ExitInfo{
        AvgClosePrice: avgClose,
        PnL:           pnl,
        Result:        result,
        CloseReason:   closeReason,
        ClosedAt:      now,
    }); err != nil {
        slog.Error("update trade result", "symbol", pos.Symbol, "error", err)
    }

    // Phase 4: update trade_memories with outcome
    m.updateTradeMemory(ctx, pos, result, pnl, now)

    _ = m.cache.RemovePosition(ctx, pos.Symbol)

    if result == "LOSS" || result == "FORCE_SL" {
        cooldown := time.Duration(m.cfg.Trading.CooldownMinutes) * time.Minute
        _ = m.cache.SetCooldown(ctx, cooldown)
    }

    slog.Info("position_closed",
        "symbol", pos.Symbol,
        "result", result,
        "close_reason", closeReason,
        "avg_close", avgClose,
        "pnl", pnl,
    )
}

func classifyResult(pos domain.Position, pnl float64, closeReason string) string {
    switch closeReason {
    case "FORCE_SL":
        return "FORCE_SL"
    case "MANUAL":
        return "MANUAL"
    case "HARD_SL":
        return "LOSS"
    case "TP_TRAIL":
        if pnl > 0 && pos.TP1Filled {
            // TP1 filled but trailing leg hit breakeven or small loss → still net positive
            if pnl < pos.EntryPrice*pos.OriginalQty*0.001 {
                return "BREAKEVEN"
            }
            return "WIN"
        }
        if pos.TP1Filled && pnl > 0 {
            return "PARTIAL_WIN"
        }
        return "WIN"
    }
    if pnl > 0 {
        return "WIN"
    }
    return "LOSS"
}
```

### 10.5 Update trade_memories on Close

Every trade close (including force-SL) updates the Phase 4 `trade_memories` table with the final outcome. This enables the memory engine to learn from real results.

```go
func (m *PositionManager) updateTradeMemory(ctx context.Context, pos domain.Position, result string, pnl float64, closedAt time.Time) {
    profitPct := pnl / (pos.EntryPrice * pos.OriginalQty) * float64(pos.Leverage) * 100
    holdMinutes := int(closedAt.Sub(pos.OpenedAt).Minutes())

    outcome := mapResultToMemoryOutcome(result)

    if err := m.memoryRepo.UpdateOutcome(ctx, pos.TradeID, domain.MemoryOutcome{
        Outcome:     outcome,
        ProfitPct:   profitPct,
        HoldMinutes: holdMinutes,
    }); err != nil {
        slog.Error("update trade memory outcome", "symbol", pos.Symbol, "error", err)
    }
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
```

**Flow**: trade opens → Phase 4 writes `trade_memories` row with embedding + indicators (outcome=NULL) → trade closes → Phase 5 calls `UpdateOutcome` to fill in outcome, profit_pct, hold_minutes.

---

## 11. Database Migration

### `006_add_phase5_fields.up.sql`

```sql
-- Phase 5: Simplified close tracking + Force-SL
ALTER TABLE trades ADD COLUMN IF NOT EXISTS close_reason VARCHAR(16);  -- TP_TRAIL | FORCE_SL | HARD_SL | MANUAL
ALTER TABLE trades ADD COLUMN IF NOT EXISTS avg_close_price NUMERIC(20,8);
```

### `006_add_phase5_fields.down.sql`

```sql
ALTER TABLE trades DROP COLUMN IF EXISTS close_reason;
ALTER TABLE trades DROP COLUMN IF EXISTS avg_close_price;
```

---

## 12. Updated Trade Repository

```go
// internal/storage/trade_repo.go

// UpdateResult is the single close path for all exit types (Phase 5).
func (r *PGTradeRepository) UpdateResult(ctx context.Context, id uuid.UUID, exit domain.ExitInfo) error {
    _, err := r.pool.Exec(ctx, `
        UPDATE trades SET
            avg_close_price=$1, pnl=$2, result=$3, close_reason=$4, closed_at=$5
        WHERE id=$6
    `, exit.AvgClosePrice, exit.PnL, exit.Result, exit.CloseReason, exit.ClosedAt, id)
    return err
}
```

```go
// internal/domain/trade.go

type ExitInfo struct {
    AvgClosePrice float64
    PnL           float64
    Result        string   // WIN | LOSS | PARTIAL_WIN | FORCE_SL | BREAKEVEN | MANUAL
    CloseReason   string   // TP_TRAIL | FORCE_SL | HARD_SL | MANUAL
    ClosedAt      time.Time
}
```

---

## 13. Configuration

### Config Struct Addition

```go
// Added to ExecutionConfig in internal/config/config.go
type ExecutionConfig struct {
    // ... existing fields ...
    TrailingCallbackRate   float64 `yaml:"trailing_callback_rate"`    // 0.5 = 0.5%
    TP1SizePct             float64 `yaml:"tp1_size_pct"`              // 50 = 50% of position
    ForceSLEnabled         bool    `yaml:"force_sl_enabled"`
    ForceSLStartMin        int     `yaml:"force_sl_start_min"`        // 5 = start after 5min
    ForceSLPnlGatePct      float64 `yaml:"force_sl_pnl_gate_pct"`    // -0.5 = skip if PnL above -0.5%
    ForceSLEscalatePnlPct  float64 `yaml:"force_sl_escalate_pnl_pct"` // -2.0 = escalate below -2%
    ForceSLSlowIntervalSec int     `yaml:"force_sl_slow_interval_sec"` // 300 = every 5min when mild
    ForceSLFastIntervalSec int     `yaml:"force_sl_fast_interval_sec"` // 60 = every 1min when severe
    ForceSLTimeoutSec      int     `yaml:"force_sl_timeout_sec"`       // 5 = LLM timeout
}
```

### config.yaml Addition

```yaml
execution:
  # ... existing ...
  trailing_callback_rate: 0.5       # 0.5% callback for trailing stop
  tp1_size_pct: 50                  # TP1 closes 50% of position
  force_sl_enabled: true
  force_sl_start_min: 5             # start checking after 5 minutes
  force_sl_pnl_gate_pct: -0.5      # no check if PnL above this (winning/flat)
  force_sl_escalate_pnl_pct: -2.0  # escalate to fast interval below this
  force_sl_slow_interval_sec: 300   # every 5min when PnL is between gate and escalate
  force_sl_fast_interval_sec: 60    # every 1min when PnL is worse than escalate threshold
  force_sl_timeout_sec: 5           # LLM timeout for force-SL decisions
```

### Defaults

```go
if cfg.Execution.TrailingCallbackRate == 0 {
    cfg.Execution.TrailingCallbackRate = 0.5
}
if cfg.Execution.TP1SizePct == 0 {
    cfg.Execution.TP1SizePct = 50
}
if cfg.Execution.ForceSLStartMin == 0 {
    cfg.Execution.ForceSLStartMin = 5
}
if cfg.Execution.ForceSLPnlGatePct == 0 {
    cfg.Execution.ForceSLPnlGatePct = -0.5
}
if cfg.Execution.ForceSLEscalatePnlPct == 0 {
    cfg.Execution.ForceSLEscalatePnlPct = -2.0
}
if cfg.Execution.ForceSLSlowIntervalSec == 0 {
    cfg.Execution.ForceSLSlowIntervalSec = 300
}
if cfg.Execution.ForceSLFastIntervalSec == 0 {
    cfg.Execution.ForceSLFastIntervalSec = 60
}
if cfg.Execution.ForceSLTimeoutSec == 0 {
    cfg.Execution.ForceSLTimeoutSec = 5
}
```

---

## 14. Memory Engine Integration (Phase 4 → Phase 5)

Phase 4 creates the `trade_memories` row at trade open (with embedding + indicators, outcome=NULL). Phase 5's close flow fills in the outcome.

### Write Path (on trade close)

The `updateTradeMemory` function (section 10.5) calls `memoryRepo.UpdateOutcome`:

```go
// internal/storage/memory_repo.go

func (r *PGMemoryRepository) UpdateOutcome(ctx context.Context, tradeID uuid.UUID, outcome domain.MemoryOutcome) error {
    _, err := r.pool.Exec(ctx, `
        UPDATE trade_memories SET
            outcome=$1, profit_pct=$2, hold_minutes=$3
        WHERE trade_id=$4
    `, outcome.Outcome, outcome.ProfitPct, outcome.HoldMinutes, tradeID)
    return err
}
```

```go
// internal/domain/memory.go

type MemoryOutcome struct {
    Outcome     string  // WIN | PARTIAL_WIN | LOSS | FORCE_SL | BREAKEVEN | SKIP_VALIDATED | SKIP_MISSED
    ProfitPct   float64 // leveraged PnL % (actual for trades, hypothetical for skips)
    HoldMinutes int     // 0 for skips
}
```

### Outcome Mapping (1:1 — preserve granularity)

| Trade Result     | Memory Outcome   | Written By          | Rationale                                                 |
|------------------|------------------|---------------------|-----------------------------------------------------------|
| `WIN`            | `WIN`            | Phase 5 close flow  | Full thesis validated (TP1 + trailing both hit)           |
| `PARTIAL_WIN`    | `PARTIAL_WIN`    | Phase 5 close flow  | Thesis partially right (TP1 hit, trailing reverted to BE) |
| `LOSS`           | `LOSS`           | Phase 5 close flow  | Hard SL hit, setup completely failed                      |
| `FORCE_SL`       | `FORCE_SL`       | Phase 5 close flow  | LLM detected thesis invalidated early                     |
| `BREAKEVEN`      | `BREAKEVEN`      | Phase 5 close flow  | Neutral — neither confirms nor denies thesis              |
| `MANUAL`         | `BREAKEVEN`      | Phase 5 close flow  | Human override, not useful for learning                   |
| (skip decision)  | `SKIP_VALIDATED` | Phase 4 skip validator | We skipped and price went against short (good skip)    |
| (skip decision)  | `SKIP_MISSED`    | Phase 4 skip validator | We skipped but price dropped (missed opportunity)      |

**Who writes what:**
- **Phase 4** creates the `trade_memories` row at trade open (or skip decision) with embedding + indicators. For skips, the Phase 4 background validator fills outcome 2h later.
- **Phase 5** fills outcome for actual trades on close via `updateTradeMemory`.

**Why keep PARTIAL_WIN and FORCE_SL separate?**
- `PARTIAL_WIN` tells the LLM: "this setup works directionally but doesn't have follow-through momentum — consider lower position size or tighter trailing"
- `FORCE_SL` tells the LLM: "this setup looked good at entry but quickly invalidated — watch for this pattern and skip or lower confidence"
- Collapsing them into WIN/LOSS loses actionable nuance

### Read Path (on new trade evaluation)

Unchanged from Phase 4: the LLM decision engine retrieves top-5 similar past setups with outcomes to inform the current decision. Phase 5 enriches the dataset with more granular outcomes.

### Updated LLM Prompt Context (similar trades section)

The Phase 4 prompt injects similar past trades into the LLM decision. With Phase 5 outcomes, the prompt must teach the LLM what each outcome means:

```
## Similar Past Setups (from trade memory):
{{range .SimilarTrades}}
- {{.Symbol}} | Score: {{.CompositeScore}} | Outcome: {{.Outcome}} | PnL: {{.ProfitPct}}% | Hold: {{.HoldMinutes}}min
{{end}}

Outcome definitions:
- WIN: Setup fully validated — hit TP1 and trailing captured extended move
- PARTIAL_WIN: TP1 hit (direction correct) but price reversed before trailing captured gains
- LOSS: Hit hard stop-loss (5%) — setup completely failed
- FORCE_SL: Position was force-closed early by risk check — thesis invalidated after entry
- BREAKEVEN: Position closed flat — inconclusive
- SKIP_VALIDATED: We skipped this setup and price went against short (good skip)
- SKIP_MISSED: We skipped this setup but price dropped significantly (missed opportunity)

Use these outcomes to calibrate your confidence:
- If similar setups show WIN → high confidence, thesis has strong follow-through
- If similar setups show PARTIAL_WIN → setup works directionally but moderate confidence
- If similar setups show LOSS → strong signal to SKIP
- If similar setups show FORCE_SL → setup tends to invalidate quickly — lower confidence or SKIP
- If similar setups show SKIP_MISSED → consider trading if current confluence is strong
- If similar setups show SKIP_VALIDATED → lean toward SKIP unless current setup is clearly better
```

---

## 15. Error Handling & Edge Cases

| Scenario                                    | Behavior                                                      |
|---------------------------------------------|---------------------------------------------------------------|
| Trailing stop placement fails               | Fallback: market close remaining 50% immediately              |
| Force-SL LLM timeout (>5s)                 | Default to HOLD (safe fallback)                               |
| Force-SL LLM unavailable                   | Default to HOLD — hard SL is the backstop                     |
| TP1 fills but position already closed (SL)  | Ignore — check position exists before processing              |
| Price gaps through trailing callback        | Binance fills at market — actual fill may exceed 0.5% slippage|
| TP1 partially fills (exchange quirk)        | Treat as not filled until fully filled (check filledQty)      |
| Manual close via Binance app               | Detected as usual; record with actual exit price              |
| Force-SL triggered while in profit         | Shouldn't happen (LLM should HOLD), but allow it              |
| Both trailing + breakeven SL fill simultaneously | Cancel whichever arrives second (race handling)          |
| `force_sl_enabled: false`                   | No force-SL checks; hard SL only (backward compat)           |

---

## 16. Testing Strategy

### Unit Tests

| Test                                                | What it validates                                              |
|-----------------------------------------------------|----------------------------------------------------------------|
| `TestExecute_SplitTP_OrderQuantities`               | SL=100% qty, TP1=50% qty                                      |
| `TestHandleTP1Fill_PlacesTrailing`                  | After TP1 fills, trailing order placed with correct params     |
| `TestHandleTP1Fill_CancelsOldSL`                    | Old 100% SL is cancelled after TP1                            |
| `TestHandleTP1Fill_PlacesBreakevenSL`               | Breakeven SL placed for remaining qty at entry price           |
| `TestHandleTrailingFill_RecordsSplitResult`         | Both legs' PnL computed and stored correctly                   |
| `TestHandleHardSL_FullLoss`                         | Hard SL fills entire position, records as LOSS                 |
| `TestForceSL_StartsAfter5Min`                       | No force-SL check before 5 minutes                            |
| `TestForceSL_ChecksEvery1Min`                       | Force-SL LLM called once per minute                           |
| `TestForceSL_DefaultHoldOnFailure`                  | LLM error → HOLD (no force close)                             |
| `TestForceSL_ClosesOnForceClose`                    | FORCE_CLOSE → market close + record                           |
| `TestForceSL_SkippedAfterTP1`                       | No force-SL checks once TP1 has filled                        |
| `TestPriceTracking_HighLow`                         | HighSinceEntry/LowSinceEntry updated correctly                |
| `TestResultClassification`                          | All result types (WIN, LOSS, PARTIAL_WIN, FORCE_SL, BREAKEVEN)|
| `TestTrailingFallback_OnPlacementFailure`           | Falls back to market close if trailing placement fails        |

### Integration Tests

| Test                                                | What it validates                                              |
|-----------------------------------------------------|----------------------------------------------------------------|
| `TestSplitTP_FullLifecycle_Win`                     | Open → TP1 fills → trailing fills → WIN recorded              |
| `TestSplitTP_FullLifecycle_BreakevenSL`             | Open → TP1 fills → price reverses → breakeven SL → PARTIAL   |
| `TestForceSL_FullLifecycle`                         | Open → 5min → LLM says FORCE_CLOSE → market close → recorded |
| `TestHardSL_BeforeTP1`                              | Open → price pumps 5% → hard SL fills → LOSS                 |

### Manual Validation

- Paper mode with `force_sl_enabled: true`
- Monitor force-SL checks in logs (should see HOLD for normal trades, FORCE_CLOSE when thesis fails)
- Verify split TP: TP1 fills 50%, trailing placed, trailing eventually fills
- Verify race conditions: fast price moves don't leave orphan orders
- Compare average loss size: with vs without force-SL (should be smaller)

---

## 17. Implementation Checklist

| #  | Task                                                          | Dependencies |
|----|---------------------------------------------------------------|--------------|
|    | **Force Stop-Loss**                                           |              |
| 1  | Create `internal/llm/force_sl.go` (engine + prompt)           | None         |
| 2  | Add `ForceSLRequest`, `ForceSLResponse` to schema.go          | None         |
| 3  | Add Force-SL config fields to `ExecutionConfig` + defaults    | None         |
| 4  | Add price tracking fields to `Position` domain type           | None         |
| 5  | Implement price tracking in position manager tick             | #4           |
| 6  | Implement `checkForceSL` + `forceClose` in position manager   | #1,#4,#5     |
| 7  | Add `FORCE_SL` result type handling                           | None         |
| 8  | Add `UpdateForceSL` to trade repository                       | #7           |
|    | **Split TP + Trailing Stop**                                  |              |
| 9  | Add `PlaceTrailingStopOrder` to Executor interface            | None         |
| 10 | Implement trailing stop in live executor (Binance API)        | #9           |
| 11 | Implement trailing stop in paper executor (simulated)         | #9           |
| 12 | Add TP1/trailing fields to `Position` domain type             | None         |
| 13 | Modify execution engine: TP1 at 50% qty                       | #9,#12       |
| 14 | Implement `handleTP1Fill` in position manager                 | #10,#12      |
| 15 | Implement `handleTrailingFill` + `handleTrailingSLFill`       | #14          |
| 16 | Implement `recordSplitClose` with dual-leg PnL               | #15          |
|    | **Trade Record**                                              |              |
| 17 | Create migration `006_add_split_tp_fields`                    | None         |
| 18 | Add split TP + force-SL fields to `Trade` domain type        | #17          |
| 19 | Add `UpdateSplitResult` to trade repository                   | #17,#18      |
| 20 | Update Phase 4 memory engine `RecordTrade` for split outcomes | #18          |
|    | **Testing & Validation**                                      |              |
| 21 | Write unit tests (force-SL)                                   | #6           |
| 22 | Write unit tests (split TP + trailing)                        | #13,#14,#15  |
| 23 | Write integration tests (full lifecycle)                      | All          |
| 24 | End-to-end paper trading                                      | All          |

---

## 18. Summary

Phase 5 optimizes the exit strategy for asymmetric payoff:

| Feature              | Impact                                                                    |
|----------------------|---------------------------------------------------------------------------|
| **Force Stop-Loss**  | Cuts losses early when thesis invalidates; saves avg ~1-2% per bad trade  |
| **Split TP + Trail** | Lets winners run; avg winning trade improves by capturing extended moves   |
| **Enhanced Record**  | Granular tracking of partial fills enables performance analysis by leg     |

Combined effect on expected value:
- **Losers get smaller** (force-SL at ~-2% instead of -5%)
- **Winners get bigger** (trailing captures 3-5% moves instead of capping at 2%)
- **Net EV per trade improves** even if win rate stays the same

The bot's edge isn't prediction accuracy — it's **asymmetric payoff**: small losses, large wins.

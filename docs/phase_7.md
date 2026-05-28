# Phase 7: Resilient Order Placement — WS-Confirmed Entry + SL/TP Retry + Startup Reconciliation

---

## 1. Overview

Phase 7 fixes a critical reliability gap in the order placement flow. Currently `executeInternalWithTP` places a market order then immediately places SL and TP as REST calls using the API response. Three failure scenarios are unhandled:

1. **Silent unprotected position** — market order succeeds on Binance but the REST response errors (network blip, timeout). Bot gets an error and aborts — but the position is already open on Binance with no SL or TP.

2. **SL/TP on stale price** — even when the REST call succeeds, Binance may return `status: NEW` (not `FILLED`). `order.FillPrice` may be 0, falling back to `markPrice`. SL/TP are placed before the actual fill is confirmed.

3. **Startup gap** — bot crashes after entry but before writing the position to Redis. On restart, Binance has an open position the bot knows nothing about.

### What Changes

| Before (Phase 6)                                          | After (Phase 7)                                                  |
|-----------------------------------------------------------|------------------------------------------------------------------|
| SL/TP placed immediately after market order REST response | SL/TP placed after `ORDER_TRADE_UPDATE` WS confirmation          |
| SL/TP use `order.FillPrice` (may be 0 / mark price)       | SL/TP use `o.AvgPrice` from WS (real fill price)                 |
| Orphaned position if REST errors after fill               | `PendingEntry` in Redis catches the fill via WS                  |
| No SL/TP retry on failure                                 | `PendingProtection` retries every 5s, emergency close after 5x   |
| No startup recovery                                       | `Reconcile()` on boot syncs Binance state into Redis              |

### What Stays the Same

- Full Phase 1–6 pipeline unchanged
- All existing domain types, except additions to `Position`
- `HandleUserDataEvent` close-side logic (TP1, trailing, hard SL, manual) unchanged
- Paper mode: `PendingEntry` flow runs but paper executor always confirms fill immediately

---

## 2. Trade-offs

### Approach A: WS-Confirmed Placement ✅ chosen

Wait for `ORDER_TRADE_UPDATE` with `OrderStatus = FILLED` for the entry order before placing SL/TP.

| | |
|---|---|
| **Pro** | SL/TP use the real fill price (`o.AvgPrice`). Never places SL/TP before position exists. |
| **Pro** | Naturally handles async fills. Decouples placement from confirmation. |
| **Con** | Entry order and SL/TP become decoupled — need `PendingEntry` state in Redis. |
| **Con** | Small latency increase (~50–200ms for WS round-trip). Acceptable for protective orders. |
| **Con** | WS message can be missed if connection drops in that window — mitigated by startup reconciliation. |

### Approach B: REST polling

After placing market order, poll `/fapi/v1/order` every 100ms until `FILLED`.

| | |
|---|---|
| **Pro** | Stays synchronous, simpler state machine. |
| **Con** | Wastes REST quota (Binance rate limits ~1200 weight/min). |
| **Con** | Doesn't help with startup recovery. |

### Approach C: REST with WS-based retry (patch only)

Keep current approach, add post-placement WS confirmation that re-places missing SL/TP.

| | |
|---|---|
| **Pro** | Minimal refactor. |
| **Con** | Still uses `FillPrice` from REST which may be 0 or stale. |

**Decision: Approach A.**

---

## 3. Project Structure (New/Modified Files)

```
internal/
├── domain/
│   └── position.go         Modified: + PendingEntry, PendingProtection structs
├── storage/
│   ├── interfaces.go        Modified: + PendingEntry + PendingProtection methods on StateCache
│   └── redis.go             Modified: implement new cache methods
├── exchange/
│   ├── types.go             Modified: + OpenOrderResponse
│   └── binance_client.go    Modified: + GetOpenOrders()
├── execution/
│   ├── engine.go            Modified: executeInternalWithTP → placeEntryOrder only (no SL/TP)
│   ├── position_manager.go  Modified: HandleUserDataEvent handles MARKET fills; + finalizeSLTP, retryProtection
│   └── reconciler.go        NEW: startup reconciliation logic
└── app/
    └── app.go               Modified: call Reconcile on startup

docs/
└── phase_7.md               This file
```

---

## 4. System Design: WS-Confirmed Entry

### State Machine

```
┌─────────────────────────────────────────────────────────────────┐
│                    ORDER PLACEMENT FLOW                           │
├─────────────────────────────────────────────────────────────────┤
│                                                                   │
│  scan cycle                                                       │
│    └─ placeEntryOrder()                                          │
│         ├── pre-checks (kill switch, cooldown, balance)          │
│         ├── PlaceMarketOrder → OrderID                           │
│         └── SetPendingEntry(OrderID, symbol, qty, tpPct, ...)   │
│                                                                   │
│  USER DATA WS (ORDER_TRADE_UPDATE)                               │
│    └─ HandleUserDataEvent()                                      │
│         ├── if MARKET SELL FILLED                                │
│         │    ├── GetPendingEntry(OrderID)                        │
│         │    ├── finalizeSLTP(pending, avgPrice, filledQty)      │
│         │    │    ├── PlaceStopMarketOrder (SL, 100% qty)        │
│         │    │    ├── PlaceStopLimitOrder  (TP1, 50% qty)        │
│         │    │    ├── tradeRepo.Insert()                         │
│         │    │    ├── cache.SetActivePosition()                  │
│         │    │    └── cache.RemovePendingEntry()                 │
│         │    └── on SL/TP failure: SetPendingProtection()        │
│         │                                                         │
│         └── if SL/TP/trailing FILLED (existing close-side logic) │
│                                                                   │
│  PositionManager.check() — every 1s                              │
│    └─ retryProtection()                                          │
│         ├── if PendingProtection exists for symbol               │
│         ├── retry missing SL/TP (max 5, every 5s)               │
│         └── on exhaustion: emergency market close + notify       │
│                                                                   │
└─────────────────────────────────────────────────────────────────┘
```

### PendingEntry TTL

`PendingEntry` is stored in Redis with a 60-second TTL. If the WS fill event is never received (WS outage), startup reconciliation will detect the orphaned Binance position and re-hydrate it. The TTL prevents stale pending entries from accumulating.

---

## 5. Domain Types

### PendingEntry

```go
// internal/domain/position.go
type PendingEntry struct {
    OrderID         string
    Symbol          string
    Quantity        float64
    PositionSizePct float64
    Confidence      int
    Window          string         // scheduler.WindowType serialized
    TpPct           float64
    LLMDecision     *LLMDecision   // nil if Phase 2 path
    CreatedAt       time.Time
    ExpiresAt       time.Time      // CreatedAt + 60s
}
```

### PendingProtection

```go
// internal/domain/position.go
type PendingProtection struct {
    Symbol      string
    NeedsSL     bool
    NeedsTP     bool
    EntryPrice  float64
    Quantity    float64
    TP1Qty      float64
    StopLoss    float64
    TakeProfit  float64
    Retries     int
    LastAttempt time.Time
}
```

---

## 6. Cache Interface Additions

```go
// internal/storage/interfaces.go
type StateCache interface {
    // ... existing ...

    // PendingEntry: market order placed, waiting for WS fill confirmation
    SetPendingEntry(ctx context.Context, entry domain.PendingEntry) error
    GetPendingEntry(ctx context.Context, orderID string) (*domain.PendingEntry, error)
    RemovePendingEntry(ctx context.Context, orderID string) error
    GetAllPendingEntries(ctx context.Context) ([]domain.PendingEntry, error)

    // PendingProtection: position open but SL/TP placement failed, needs retry
    SetPendingProtection(ctx context.Context, p domain.PendingProtection) error
    GetPendingProtection(ctx context.Context, symbol string) (*domain.PendingProtection, error)
    RemovePendingProtection(ctx context.Context, symbol string) error
}
```

Redis keys:
- `pending_entry:<orderID>` — JSON string, TTL 60s
- `pending_protection:<symbol>` — JSON string, no TTL (cleared when retries succeed or emergency close fires)

---

## 7. ExecutionEngine Changes

### Before
```
executeInternalWithTP():
  1. pre-checks
  2. PlaceMarketOrder → fill price
  3. PlaceStopMarketOrder (SL)
  4. PlaceStopLimitOrder (TP1)
  5. Insert trade record
  6. SetActivePosition
```

### After
```
placeEntryOrder():
  1. pre-checks
  2. PlaceMarketOrder → OrderID
  3. SetPendingEntry(OrderID, ...)
  -- done; SL/TP deferred to WS --
```

Public API unchanged: `ExecuteScored` and `ExecuteScoredWithLLM` still call through `executeInternal` → `placeEntryOrder`.

---

## 8. PositionManager: finalizeSLTP

Called from `HandleUserDataEvent` when a MARKET SELL FILLED event arrives:

```go
func (m *PositionManager) finalizeSLTP(
    ctx context.Context,
    pending *domain.PendingEntry,
    avgFillPrice float64,
    filledQty float64,
) {
    // 1. Calculate SL/TP from real fill price
    slDistance   := avgFillPrice * (m.cfg.Execution.SlPct / 100)
    stopLoss     := avgFillPrice + slDistance          // SHORT: SL above entry
    tpDistance   := avgFillPrice * (pending.TpPct / 100)
    takeProfit   := avgFillPrice - tpDistance          // SHORT: TP below entry

    // 2. Place SL (100% qty)
    // 3. Place TP1 (50% qty, stop-limit)
    // 4. Build Position struct
    // 5. Insert trade record
    // 6. SetActivePosition
    // 7. RemovePendingEntry
    // On any SL/TP failure: SetPendingProtection
}
```

---

## 9. PositionManager: retryProtection

Called from `check()` every second when `PendingProtection` exists for a position:

```go
const maxProtectionRetries = 5
const protectionRetryInterval = 5 * time.Second

func (m *PositionManager) retryProtection(ctx context.Context, pos domain.Position, pp *domain.PendingProtection) {
    if time.Since(pp.LastAttempt) < protectionRetryInterval {
        return
    }
    if pp.Retries >= maxProtectionRetries {
        // Emergency: market close to avoid unprotected exposure
        m.forceClose(ctx, pos, currentPrice, "protection retry exhausted")
        m.cache.RemovePendingProtection(ctx, pos.Symbol)
        return
    }
    // retry missing orders...
    pp.Retries++
    pp.LastAttempt = time.Now()
    m.cache.SetPendingProtection(ctx, *pp)
}
```

---

## 10. Startup Reconciliation

### `internal/execution/reconciler.go`

```go
func Reconcile(
    ctx context.Context,
    client *exchange.BinanceClient,
    executor Executor,
    cache storage.StateCache,
    tradeRepo storage.TradeRepository,
    cfg *config.Config,
    notifier *notify.Notifier,
) error
```

Steps:
1. `GET /fapi/v2/positionRisk` (all symbols, `positionAmt != 0`) — existing `GetPositionRisk`
2. `GET /fapi/v1/openOrders` — new `GetOpenOrders()`
3. Build index: `openOrdersBySymbol map[string][]OpenOrderResponse`
4. Compare Binance positions vs Redis cache:

| Binance | Cache | Action |
|---------|-------|--------|
| Open | Missing | Re-hydrate: insert Position + Trade, place missing SL/TP from openOrders |
| Closed / zero | Present | Remove from cache; update trade DB record as MANUAL |
| Open | Present | Verify order IDs in openOrders; re-place any missing SL/TP |

### New BinanceClient Method

```go
func (c *BinanceClient) GetOpenOrders(ctx context.Context) ([]OpenOrderResponse, error)
// GET /fapi/v1/openOrders (signed, no symbol param = all symbols)
```

```go
// internal/exchange/types.go
type OpenOrderResponse struct {
    Symbol     string  `json:"symbol"`
    OrderID    int64   `json:"orderId"`
    Side       string  `json:"side"`
    Type       string  `json:"type"`
    StopPrice  float64 `json:"stopPrice,string"`
    Price      float64 `json:"price,string"`
    OrigQty    float64 `json:"origQty,string"`
    Status     string  `json:"status"`
    ReduceOnly bool    `json:"reduceOnly"`
}
```

---

## 11. Paper Mode Behaviour

In paper mode, `PlaceMarketOrder` returns a fill synchronously (the paper executor simulates it). The `PendingEntry` is still written and the WS handler still fires `finalizeSLTP` — but the paper executor's user-data stream doesn't exist.

To handle this cleanly: in paper mode, `placeEntryOrder` calls `finalizeSLTP` directly after setting the pending entry (the paper executor already has a fill price). The `PendingEntry` is immediately consumed. This keeps the code path identical for live and paper modes with a one-line branch in paper execution.

---

## 12. Error Handling

| Scenario | Behavior |
|---|---|
| Market order REST error | Abort; no PendingEntry written. Existing behavior. |
| Market order succeeds, WS never fires (WS outage) | PendingEntry expires after 60s; startup reconciliation re-hydrates on next boot. |
| SL placement fails in finalizeSLTP | SetPendingProtection; position active but unprotected — retry in 5s. |
| TP placement fails in finalizeSLTP | SetPendingProtection(NeedsTP=true); retry in 5s. |
| Both fail | Emergency market close after 5 retries (25s window). |
| Reconcile finds orphan on startup | Re-inserts position + trade; places missing SL/TP; logs reconciliation. |
| Reconcile finds stale cache entry | Removes from cache; marks trade MANUAL in DB. |
| Reconcile itself fails | Log warning, continue — non-fatal. |

---

## 13. Goroutine Roster (Updated)

No new goroutines. The WS handler and PositionManager tick already exist. All new logic runs inline in those existing loops.

---

## 14. Implementation Checklist

| # | Task | Dependencies |
|---|------|--------------|
| 1 | Add `PendingEntry` + `PendingProtection` to `domain/position.go` | None |
| 2 | Add new methods to `StateCache` interface | #1 |
| 3 | Implement new Redis cache methods | #2 |
| 4 | Add `OpenOrderResponse` to `exchange/types.go` | None |
| 5 | Add `GetOpenOrders()` to `BinanceClient` | #4 |
| 6 | Refactor `engine.go` → `placeEntryOrder` (no SL/TP) | #2 |
| 7 | Add `finalizeSLTP` + `retryProtection` to `position_manager.go` | #2,#6 |
| 8 | Extend `HandleUserDataEvent` for MARKET fills | #7 |
| 9 | Create `execution/reconciler.go` | #3,#5 |
| 10 | Wire `Reconcile` into `app.go` Run() | #9 |
| 11 | Handle paper mode in `placeEntryOrder` | #6,#7 |

---

## 15. Implementation Order

1. Domain types + storage interface + Redis (tasks 1–3) — foundation everything depends on
2. Exchange additions (tasks 4–5) — isolated, no dependencies
3. Engine refactor (task 6) — breaks SL/TP placement out
4. PositionManager additions (tasks 7–8) — SL/TP now lives here
5. Reconciler (task 9) — uses all the above
6. Wiring + paper mode (tasks 10–11) — final integration

---

## 16. Summary

| Feature | Impact |
|---|---|
| **WS-confirmed SL/TP** | SL/TP always use real fill price; never placed before position confirmed |
| **PendingEntry** | Market fills can never be lost; WS outage caught by reconciler on next boot |
| **PendingProtection + retry** | 25s window to recover from SL/TP placement failures before emergency close |
| **Startup reconciliation** | Crash recovery; heals orphaned Binance positions on every restart |

Trading logic, risk engine, memory engine, and scoring are entirely unchanged.

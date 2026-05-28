# Phase 8: Precision AFTER Entry — Sub-Second T+0 Trigger + Bid Depth Sizing + BookTicker Integration

---

## 1. Overview

Phase 8 optimizes the AFTER entry mode for extreme negative funding symbols (-0.8% to -2%). The current implementation relies on the 1-second scheduler tick to fire the AFTER intent, then places a market order. This introduces up to ~1s latency after settlement — in a fast dump, that's 10-30 bps of worse entry.

### Problem Statement

At T+0 on extreme-funding symbols:
1. **Many bots short simultaneously** — the bid side of the book gets eaten through multiple levels within milliseconds.
2. **Being first matters** — the first sell orders fill at the top of the book; late orders fill 3-5 levels deep.
3. **Current 1s tick = up to 1000ms of unnecessary latency** — during which price may already move 0.1-0.3%.

### What Changes

| Before (Phase 7)                                 | After (Phase 8)                                              |
|--------------------------------------------------|--------------------------------------------------------------|
| AFTER intent fires on next scheduler tick (~1s)  | Dedicated goroutine fires at T+0.000s (sub-10ms precision)   |
| Blind market order, no book awareness            | Bid depth check → position sizing based on available liquidity|
| No real-time spread/book data                    | `@bookTicker` stream for target symbol during AFTER window   |
| Same execution path for all entry modes          | AFTER mode uses specialized `AfterExecutor` path             |

### What Stays the Same

- Full Phase 1–7 pipeline unchanged
- FRONTRUN and LAST_MINUTE execution paths untouched
- LLM decision-making during FRONTRUN window unchanged
- WS-confirmed SL/TP placement (Phase 7) still applies after fill
- Paper mode behavior (immediate fill simulation)
- Intent queue semantics (AFTER intent queued during FRONTRUN, waits for window)

---

## 2. Trade-offs

### Approach A: Dedicated T+0 Goroutine ✅ chosen

A goroutine sleeps until the exact funding timestamp, then fires the AFTER intent bypassing the scheduler tick.

| Pro | Con |
|-----|-----|
| Sub-10ms latency at T+0 | Adds a parallel execution path alongside scheduler |
| Deterministic timing (not dependent on tick alignment) | Must coordinate with intent queue (don't double-fire) |
| Simple implementation (time.Sleep or time.Timer) | Clock skew with Binance server possible |

### Approach B: Reduce scheduler tick to 100ms ❌ rejected

| Pro | Con |
|-----|-----|
| No new goroutine | 10x more ticks = 10x more lock acquisitions for all windows |
| Simpler code path | Worst case still 100ms late |
| | Wastes CPU during non-critical windows |

### Approach C: Pre-place limit order at T-0.5s ❌ rejected

| Pro | Con |
|-----|-----|
| Order is already in Binance matching engine at T+0 | If filled before settlement → you PAY the funding fee |
| Zero latency | Funding fee on -1% symbol = immediate -1% ROI hit |
| | Defeats purpose of AFTER mode |

---

## 3. Architecture

```
                    ┌─────────────────────────────────────────────┐
                    │            AfterTrigger (new)                │
                    │                                             │
                    │  1. Watches intent queue for AFTER intents  │
                    │  2. Computes exact T+0 timestamp            │
                    │  3. Sleeps until T+0                        │
                    │  4. Subscribes @bookTicker at T-5s          │
                    │  5. Fires at T+0.000s with bid depth info   │
                    └─────────────┬───────────────────────────────┘
                                  │
                    ┌─────────────▼───────────────────────────────┐
                    │         AfterExecutionStrategy (new)         │
                    │                                             │
                    │  1. Read current bid depth from BookTicker  │
                    │  2. Compute effective position size          │
                    │  3. Place market order                       │
                    │  4. Mark intent as fired                     │
                    └─────────────┬───────────────────────────────┘
                                  │
                    ┌─────────────▼───────────────────────────────┐
                    │      Existing ExecutionEngine (Phase 7)      │
                    │                                             │
                    │  - PendingEntry → WS fill confirmation      │
                    │  - finalizeSLTP after confirmed fill         │
                    └─────────────────────────────────────────────┘
```

### File Structure

```
internal/
├── execution/
│   ├── after_trigger.go        # T+0 precision goroutine
│   └── after_strategy.go       # Bid-depth-aware order placement
├── market/
│   └── book_ticker.go          # @bookTicker stream + cache
├── exchange/
│   └── types.go                # BookTickerEvent struct (addition)
└── config/
    └── config.go               # AfterExecution config section (addition)
```

---

## 4. Component Design

### 4.1 BookTicker Cache (`market/book_ticker.go`)

Stores the latest best bid/ask per symbol from the `@bookTicker` WebSocket stream.

```go
type BookTicker struct {
    BidPrice float64
    BidQty   float64
    AskPrice float64
    AskQty   float64
    UpdatedAt time.Time
}

type BookTickerCache struct {
    mu   sync.RWMutex
    data map[string]*BookTicker
}

func (c *BookTickerCache) Update(symbol string, bt *BookTicker)
func (c *BookTickerCache) Get(symbol string) (*BookTicker, bool)
func (c *BookTickerCache) SpreadBps(symbol string) (float64, bool)
func (c *BookTickerCache) BidDepthUSDT(symbol string) (float64, bool) // bidQty * bidPrice
```

**Lifecycle:**
- NOT subscribed by default (saves bandwidth)
- Subscribed on-demand when an AFTER intent is queued for a specific symbol
- Unsubscribed after AFTER window ends (T+1m) or intent expires

### 4.2 BookTicker WebSocket Subscription

```
Stream: <symbol_lowercase>@bookTicker
Example: btcusdt@bookTicker
```

**Event format:**
```go
type BookTickerEvent struct {
    EventType string `json:"e"` // "bookTicker"
    Symbol    string `json:"s"`
    BidPrice  string `json:"b"`
    BidQty    string `json:"B"`
    AskPrice  string `json:"a"`
    AskQty    string `json:"A"`
    UpdateTime int64 `json:"T"`
}
```

**Subscribe timing:**
- Subscribe at T-5s for the target symbol (gives time for stream to stabilize)
- This is lightweight: single symbol, single stream, updates every ~100ms

### 4.3 AfterTrigger (`execution/after_trigger.go`)

A long-lived goroutine that handles precision execution for AFTER intents.

```go
type AfterTrigger struct {
    intentQueue   *intent.Queue
    execEngine    *ExecutionEngine
    bookTicker    *market.BookTickerCache
    wsManager     *exchange.WSManager    // to subscribe @bookTicker on-demand
    cfg           *config.AfterExecConfig
    market        MarketDataProvider
}

func (t *AfterTrigger) Run(ctx context.Context)
```

**Run loop:**

```
Loop:
  1. Check if AFTER intent exists in queue
     - If no: sleep 1s, retry
     - If yes: proceed

  2. Compute next funding time (NextFundingTime)
     - timeUntilSettlement = nextFunding - now

  3. If timeUntilSettlement > 5s:
     - Sleep until T-5s

  4. At T-5s:
     - Subscribe @bookTicker for intent symbol(s)
     - Arm the trigger timer for T+0

  5. At T+0 (time.Timer or time.After):
     - Pull best eligible AFTER intent from queue
     - Read bid depth from BookTickerCache
     - Execute via AfterExecutionStrategy
     - Mark intent as fired in queue

  6. At T+60s:
     - Unsubscribe @bookTicker
     - Reset for next funding cycle
```

**Coordination with scheduler:**
- The AfterTrigger takes ownership of AFTER intent firing
- Scheduler's `tickTransitionLocked` for AFTER window is disabled when AfterTrigger is active
- If AfterTrigger fails (crash, timeout), scheduler remains as fallback (fires on next tick)

**Implementation detail — preventing double-fire:**
```go
// In intent queue: add AtomicClaim method
func (q *Queue) ClaimAfterIntent(ctx context.Context) *TradeIntent {
    q.mu.Lock()
    defer q.mu.Unlock()
    
    intent := q.bestEligibleLocked(scheduler.WindowAfter)
    if intent == nil {
        return nil
    }
    intent.Status = IntentFired
    q.removeByIndexLocked(intent)
    q.firedInWindow[scheduler.WindowAfter] = true
    return intent
}
```

### 4.4 AfterExecutionStrategy (`execution/after_strategy.go`)

Bid-depth-aware order placement for AFTER mode.

```go
type AfterExecutionStrategy struct {
    bookTicker *market.BookTickerCache
    execEngine *ExecutionEngine
    cfg        *config.AfterExecConfig
}

func (s *AfterExecutionStrategy) Execute(
    ctx context.Context,
    intent *intent.TradeIntent,
    positionSizePct float64,
) error
```

**Execution logic:**

```
1. Get current BookTicker for symbol
2. Compute bid depth in USDT:
     bidDepthUSDT = bestBidQty * bestBidPrice

3. Compute desired order size in USDT:
     desiredUSDT = balance * positionSizePct/100 * leverage

4. Bid depth sizing:
     if bidDepthUSDT >= 3x desiredUSDT:
         → full size (clean fill expected)
     elif bidDepthUSDT >= 1.5x desiredUSDT:
         → 75% size (moderate slippage expected)
     elif bidDepthUSDT >= desiredUSDT:
         → 50% size (significant slippage)
     else:
         → minimum size or skip
         → log warning: "thin book for AFTER entry"

5. Place market sell order with adjusted quantity
6. Store PendingEntry (same as current flow)
7. SL/TP handled by existing WS fill confirmation (Phase 7)
```

**Why market order (not limit):**
- At T+0, price is falling — a limit sell above market won't fill
- A limit sell below market = worse than market order (you give away price)
- Market order into a dump fills at or below current bid — which is fine for a short

### 4.5 Clock Synchronization

Binance server time may differ from local clock by 100-500ms. To ensure we fire at the right moment:

```go
func (t *AfterTrigger) syncOffset(ctx context.Context) time.Duration {
    // GET /fapi/v1/time → compare with local time
    // Cache offset, refresh every 5 minutes
    // Apply offset when computing sleep duration
}
```

**On startup and every 5 minutes:**
```
binanceTime = GET /fapi/v1/time
localTime = time.Now()
offset = binanceTime - localTime  // e.g., +200ms means Binance is 200ms ahead
```

**When computing sleep:**
```
sleepDuration = nextFunding - time.Now() - offset
```

This ensures we fire at T+0 in Binance's clock, not ours.

---

## 5. Configuration

```yaml
after_execution:
  enabled: true
  subscribe_before_seconds: 5       # subscribe @bookTicker at T-Xs
  min_bid_depth_multiplier: 1.0     # skip if bidDepthUSDT < multiplier * orderSize
  full_size_depth_multiplier: 3.0   # use full size if bidDepth >= 3x order
  reduced_size_pct: 50              # position size when book is thin
  clock_sync_interval_seconds: 300  # how often to sync with Binance time
  fallback_to_scheduler: true       # if AfterTrigger fails, scheduler still fires
```

---

## 6. Sequence Diagram — AFTER Entry Flow

```
Timeline         AfterTrigger              BookTickerCache         Binance           IntentQueue
─────────────────────────────────────────────────────────────────────────────────────────────────
T-30m            │                          │                      │                  │
                 │  [checks queue: AFTER    │                      │                  │
                 │   intent exists]          │                      │                  │
                 │                          │                      │                  │
T-5s             │──subscribe───────────────┼──────────────────────▶ @bookTicker      │
                 │                          │◀─────stream starts───│                  │
                 │                          │  (updates every ~100ms)                 │
                 │                          │                      │                  │
T-1s             │  [arm timer]             │                      │                  │
                 │  [sync clock offset]     │                      │                  │
                 │                          │                      │                  │
T+0.000s         │──ClaimAfterIntent────────┼──────────────────────┼──────────────────▶ fired
                 │                          │                      │                  │
                 │──Get(symbol)─────────────▶                      │                  │
                 │◀─BidPrice,BidQty─────────│                      │                  │
                 │                          │                      │                  │
                 │  [compute adjusted qty]  │                      │                  │
                 │                          │                      │                  │
T+0.005s         │──PlaceMarketOrder────────┼──────────────────────▶ SELL SHORT       │
                 │◀─OrderResult─────────────┼──────────────────────│                  │
                 │                          │                      │                  │
                 │  [store PendingEntry]    │                      │                  │
                 │                          │                      │                  │
T+0.1-0.5s       │                          │                      │──ORDER_TRADE_UPDATE──▶ WS
                 │                          │                      │  (fill confirmed)
                 │                          │                      │
T+60s            │──unsubscribe─────────────┼──────────────────────▶ @bookTicker      │
                 │                          │                      │                  │
```

---

## 7. Entry Staleness Guard

Between when the LLM made the AFTER decision (T-30m to T-3m) and actual execution (T+0), market conditions may have changed drastically. The AfterTrigger should validate before firing:

```go
func (s *AfterExecutionStrategy) validateEntry(intent *TradeIntent, currentPrice float64) error {
    decisionPrice := intent.Candidate.MarkPrice // price when LLM decided
    priceDelta := (currentPrice - decisionPrice) / decisionPrice * 100

    // Price pumped significantly → even better short entry, proceed
    if priceDelta > 0 {
        slog.Info("price above decision level, favorable entry", 
            "symbol", intent.Symbol, "delta_pct", priceDelta)
        return nil
    }

    // Price already dumped significantly before T+0
    // Still enter — extreme funding (-0.8 to -2%) means more dump is expected
    // But log for observability
    if priceDelta < -1.0 {
        slog.Warn("price already dropped >1% before AFTER entry",
            "symbol", intent.Symbol, "delta_pct", priceDelta)
    }

    // Only skip if price dropped more than expected total dump
    // (means the move already happened, nothing left to capture)
    maxExpectedDump := math.Abs(intent.Candidate.FundingRate) * 200 // 2x funding as heuristic
    if priceDelta < -maxExpectedDump {
        return fmt.Errorf("price already moved %.2f%% (> %.2f%% max expected), skipping",
            priceDelta, -maxExpectedDump)
    }

    return nil
}
```

**Logic:**
- Price went UP since decision → great, we short higher = better entry
- Price went down 0-1% → expected pre-dump, still enter (main dump at T+0 is separate event)
- Price went down > 2x funding rate → the move already happened, skip
  - Example: -1% funding, price already dropped 2%+ → dump is done, don't chase

---

## 8. Failure Modes & Fallbacks

| Scenario | Handling |
|----------|----------|
| AfterTrigger goroutine panics | Scheduler's `tickTransitionLocked` still fires as fallback (up to 1s late) |
| `@bookTicker` subscription fails | Execute without bid depth check (full size market order, same as current) |
| BookTicker data stale (>2s old) | Log warning, execute anyway (data may still be directionally correct) |
| Clock sync API fails | Use last known offset, or 0 offset (worst case: ~200ms early/late) |
| Network latency on market order | Unavoidable — order reaches Binance in 10-50ms (already fast) |
| Double-fire (trigger + scheduler) | `ClaimAfterIntent` is atomic — first claim wins, second gets nil |
| Intent expired before T+0 | AfterTrigger checks expiry before firing — no-op if expired |

---

## 9. Observability

### New Metrics (structured logs)

```
after_trigger_fired        symbol, latency_ms (time since T+0)
after_bid_depth_usdt       symbol, depth (bid liquidity at time of execution)
after_spread_bps           symbol, spread (at time of execution)
after_size_adjusted        symbol, original_pct, adjusted_pct, reason
after_entry_skipped        symbol, reason (staleness, depth, etc.)
after_clock_offset_ms      offset (Binance vs local)
after_fill_slippage_bps    symbol, expected_price, fill_price
```

### Key Metric: `latency_ms`

The primary success metric. Measures time between T+0 and when the market order API call is dispatched. Target: < 10ms.

```go
fireTime := time.Now()
// ... execute ...
latencyMs := float64(time.Since(fireTime).Microseconds()) / 1000.0
slog.Info("after_trigger_fired", "symbol", symbol, "latency_ms", latencyMs)
```

### Fill Slippage Tracking

After WS confirms the fill (`ORDER_TRADE_UPDATE`), compare fill price to the bookTicker bid at time of order:

```go
slippageBps := (expectedBid - fillPrice) / expectedBid * 10000
slog.Info("after_fill_slippage_bps", "symbol", symbol, "slippage", slippageBps)
```

This tells us how much the book moved between our order and the fill — indicates how much competition we face at T+0.

---

## 10. Testing Strategy

### Unit Tests

| Test | What it validates |
|------|-------------------|
| `TestAfterTrigger_FiresAtExactTime` | Timer fires within 5ms of target |
| `TestAfterTrigger_NoIntentNoFire` | Does not fire if queue is empty |
| `TestAfterTrigger_DoubleFirPrevention` | ClaimAfterIntent returns nil on second call |
| `TestBidDepthSizing_FullSize` | Full position when depth > 3x |
| `TestBidDepthSizing_Reduced` | 50% position when depth < 1.5x |
| `TestBidDepthSizing_Thin` | Minimum size when depth < 1x |
| `TestStalenessGuard_PriceUp` | Allows entry when price pumped |
| `TestStalenessGuard_PriceDownModerate` | Allows entry, logs warning |
| `TestStalenessGuard_PriceMovedTooFar` | Skips entry |
| `TestClockSync_AppliesOffset` | Sleep duration adjusted by offset |
| `TestBookTickerCache_Concurrent` | Thread-safe reads/writes |

### Integration Tests

| Test | What it validates |
|------|-------------------|
| `TestAfterTrigger_E2E_PaperMode` | Full flow: intent → T+0 → fill → SL/TP |
| `TestAfterTrigger_FallbackToScheduler` | Scheduler fires if trigger fails |
| `TestBookTickerSubscription` | Subscribe/unsubscribe lifecycle |

---

## 11. Implementation Order

| Step | Component | Depends On |
|------|-----------|------------|
| 1 | `BookTickerEvent` struct in `exchange/types.go` | — |
| 2 | `BookTickerCache` in `market/book_ticker.go` | Step 1 |
| 3 | `@bookTicker` subscribe/unsubscribe in WS manager | Step 2 |
| 4 | Stream router handling for `bookTicker` events | Step 2, 3 |
| 5 | `ClaimAfterIntent` method in intent queue | — |
| 6 | `AfterTrigger` goroutine | Step 2, 5 |
| 7 | `AfterExecutionStrategy` (bid depth sizing) | Step 2, 6 |
| 8 | Entry staleness guard | Step 7 |
| 9 | Clock synchronization | Step 6 |
| 10 | Disable scheduler AFTER tick when trigger is active | Step 6 |
| 11 | Config additions | — |
| 12 | Paper mode support | Step 6, 7 |
| 13 | Observability (structured logs) | Step 6, 7 |
| 14 | Change FRONTRUN/LASTMINUTE TP1 to 2% pure (remove funding add-on) | — |
| 15 | Pre-settlement T-2m check goroutine (emergency close + TP widen) | Step 14 |
| 16 | Add funding fee tracking fields to `Position` domain struct | — |
| 17 | Implement `onSettlementPassed` to mark fee-paid positions | Step 16 |
| 18 | Extend `ForceSLRequest` with fee/settlement/mode context fields | Step 16 |
| 19 | Update Force-SL system prompt with mode-aware thesis invalidation | Step 18 |
| 20 | Update Force-SL user message builder with new fields | Step 18 |
| 21 | Update entry LLM system prompt with entry mode behavior details | — |
| 22 | Unit + integration tests | All |

---

## 12. Pre-Settlement TP/SL Optimization (T-2m Check)

### Problem

Current FRONTRUN/LASTMINUTE entries set TP1 at `2% + |funding_rate|`. This means if price drops 2% before settlement (achieving the real profit goal), TP1 doesn't trigger because it's waiting for the extra funding-rate distance. Meanwhile, if the position is losing at settlement time, we pay the funding fee on top of the loss.

### Solution: T-2m Pre-Settlement Check

A dedicated check fires at **T-2 minutes before funding settlement** for any open position entered during FRONTRUN or LASTMINUTE windows, where TP1 has NOT yet filled.

#### 12.1 Initial TP Placement Change

**Before (current):**
```
FRONTRUN/LASTMINUTE TP1 = EntryPrice - EntryPrice × (2% + |funding_rate|)
```

**After (Phase 8):**
```
FRONTRUN/LASTMINUTE TP1 = EntryPrice - EntryPrice × 2%  (pure target)
```

The real intention is to capture 2% profit. By placing TP1 at 2% pure, if price drops 2% before settlement, TP1 fills and we exit before paying the funding fee — best-case outcome (full 2% profit, zero fee paid).

#### 12.2 Rule 1 — Emergency Close (Funding Fee Loss Guard)

**Trigger:** T-2m before funding settlement
**Applies to:** FRONTRUN and LASTMINUTE entries where TP1 has NOT filled
**Skip if:** TP1 already filled (trailing stop is managing the remaining half)

**Condition:**
```
raw_price_move_against = (current_price - entry_price) / entry_price
threshold = 0.75 × |funding_rate|

if raw_price_move_against > threshold:
    → market-close entire position immediately
```

**Example:**
- Funding rate: -0.8% → threshold = 0.6%
- Entry price: $100, current price: $100.65 (moved up 0.65%)
- 0.65% > 0.6% → close immediately

**Rationale:** If we're already losing AND we'll have to pay the funding fee at settlement, the combined hit is devastating (loss + fee). Closing now avoids paying the fee on top of the loss. For example: 0.65% loss + 0.8% fee if held = 1.45% total damage. Closing at 0.65% loss with no fee is far better.

#### 12.3 Rule 2 — TP1 Widening (Fee Adjustment)

**Trigger:** T-2m before funding settlement (same check as Rule 1)
**Applies to:** FRONTRUN and LASTMINUTE entries where TP1 has NOT filled AND Rule 1 did NOT trigger
**Skip if:** TP1 already filled

**Action:**
```
1. Cancel existing TP1 order (placed at 2% pure)
2. Place new TP1 at: EntryPrice - EntryPrice × (2% + |funding_rate|)
```

**Rationale:** If we reach T-2m without TP1 filling, we will hold through settlement and **pay** the funding fee. Widening TP1 to include the fee ensures our net profit target remains 2% after the fee cost is covered. Example: funding = -0.8%, new TP1 = 2.8% price drop. If hit: 2.8% profit - 0.8% fee paid = 2% net.

#### 12.4 Execution Flow

```
T-30m to T-5m    FRONTRUN entry opens, TP1 placed at 2% (pure)
   │
   ▼
T-5m to T-0      LASTMINUTE entry opens, TP1 placed at 2% (pure)
   │
   ▼
T-2m             Pre-settlement check fires:
                   │
                   ├─ TP1 already filled? → SKIP (trailing stop handles rest)
                   │
                   ├─ Position in loss > 0.75 × |funding_rate|?
                   │     → RULE 1: Market-close immediately
                   │
                   └─ Position OK but TP1 not filled?
                         → RULE 2: Cancel TP1, replace with (2% + |funding_rate|)
                   │
                   ▼
T-0              Funding settlement
                   - If still open: we PAY the funding fee (shorts pay on negative funding)
                   - TP1 now accounts for the fee cost (widened at T-2m)
```

#### 12.5 Configuration

```yaml
pre_settlement:
  enabled: true
  check_before_minutes: 2              # fire check at T-Xm
  emergency_close_threshold: 0.75      # close if loss > X × |funding_rate|
  widen_tp_on_miss: true               # widen TP1 if not filled by T-2m
```

#### 12.6 Observability

```
pre_settlement_emergency_close    symbol, loss_pct, funding_rate, threshold
pre_settlement_tp_widened         symbol, old_tp_pct, new_tp_pct
pre_settlement_skipped_tp1_filled symbol (TP1 already hit, trailing active)
```

---

## 13. LLM Decision Improvement — Entry Mode Awareness

### Problem

The current LLM prompt describes entry modes briefly but doesn't explain the **downstream behavior differences** between FRONTRUN, LASTMINUTE, and AFTER. The LLM doesn't know that:
- We PAY the funding fee if we hold through settlement (shorts pay on negative funding)
- FRONTRUN goal is to TP before settlement → avoid paying the fee entirely
- LASTMINUTE will likely hold through settlement → fee paid, TP widens to compensate
- AFTER enters post-settlement → no fee to pay, clean 2% profit
- FRONTRUN positions face a T-2m emergency close check if losing (to avoid paying fee on a loser)
- Each mode targets a different price phenomenon with different fee implications

A better-informed LLM can make smarter entry mode selections and confidence calibrations.

### Solution: Enhanced Entry Mode Section in System Prompt

Replace the current `ENTRY MODE LOGIC` section with a detailed breakdown of each mode's behavior:

```
ENTRY MODE BEHAVIOR:

We short coins with extreme negative funding rates. Each entry mode targets a DIFFERENT
price movement phenomenon. Understanding what you're trying to capture is critical.

═══════════════════════════════════════════════════════════════════════════════════════
WHAT WE'RE CAPTURING — THE CORE THESIS PER MODE:
═══════════════════════════════════════════════════════════════════════════════════════

IMPORTANT — FUNDING FEE MECHANICS:
  We SHORT on NEGATIVE funding. Negative funding = shorts PAY longs at settlement.
  If we hold a short through settlement → WE PAY the funding fee (0.5-2% cost).
  Our goal is to profit from price drop and ideally EXIT BEFORE settlement to avoid paying.
  If we can't exit in time, TP is widened so the price drop covers the fee we'll pay.

FRONTRUN thesis: "Price will drop BEFORE funding settlement — exit before paying the fee."
  → WHY: When funding is extremely negative, smart money starts closing longs (or opening
    shorts) 10-30 minutes before settlement to avoid paying the fee. This selling pressure
    causes a pre-settlement dump. We want to be positioned early to ride this wave down.
  → WHAT WE GET: 2% price drop profit, AND we exit before settlement = NO fee paid.
  → BEST CASE: TP1 hits before settlement → 2% pure profit, zero funding fee cost.
  → WORST CASE: TP1 doesn't hit, we hold through settlement → pay the fee, TP widens
    to compensate. Or if losing at T-2m → emergency close to avoid paying fee on a loser.
  → EDGE: Enter early, catch the pre-settlement dump, exit clean before the fee hits.

LAST_MINUTE thesis: "Price is already starting to drop — confirm direction, still try to exit before fee."
  → WHY: Same mechanic as FRONTRUN (pre-settlement selling pressure) but we wait until
    T-5m to see if the dump is actually starting. Less time means less chance TP1 hits
    before settlement, so we're more likely to hold through and pay the fee.
  → WHAT WE GET: 2% price drop target. May or may not exit before settlement.
  → LIKELY OUTCOME: Hold through settlement → pay fee → TP widened to cover it.
  → EDGE: Less time exposed to squeezes. Directional confirmation before committing.
    Accepts that we'll probably pay the fee, but the dump momentum makes it worth it.

AFTER thesis: "Price will dump HARD right after funding settlement — ride the post-fee panic."
  → WHY: At T+0, longs who just PAID a massive fee (-0.8% to -2%) panic-sell to cut
    losses. Bots fire sell orders simultaneously. This creates a concentrated dump in
    the first 0-60 seconds after settlement.
  → WHAT WE GET: 2% price drop profit. NO fee to pay (we entered AFTER settlement).
  → EDGE: We DON'T pay the funding fee (entered post-settlement). No squeeze risk
    (settlement already happened, no fee event ahead of us). We ride pure momentum.
    The 2% target is fully ours — no fee to deduct.
  → KEY: This is the only mode where we keep 100% of the price move with ZERO fee cost.
    But we miss the pre-settlement dump and enter at a potentially worse price.

═══════════════════════════════════════════════════════════════════════════════════════
MODE MECHANICS & SYSTEM BEHAVIOR:
═══════════════════════════════════════════════════════════════════════════════════════

┌─────────────────────────────────────────────────────────────────────────────────┐
│ FRONTRUN (Enter T-30m to T-5m before settlement)                                │
├─────────────────────────────────────────────────────────────────────────────────┤
│ Capturing: Pre-settlement selling pressure. Goal = exit BEFORE paying fee.      │
│ TP1 Target: 2% (pure price drop)                                                 │
│ Funding Fee: We PAY if we hold through settlement. Goal is to TP before that.    │
│ T-2m Safety: If losing > 0.75×funding_rate → force-closed (avoid fee on loser)  │
│ T-2m Adjust: If TP1 not hit → TP widens to 2%+funding (cover the fee cost)     │
│ Hard SL: 5% adverse move                                                        │
│                                                                                  │
│ Risk profile:                                                                    │
│  • Longest time in market = highest exposure to squeezes                         │
│  • If thesis is wrong (no pre-settlement dump), you sit in a losing position    │
│  • T-2m guard limits downside: closes before we pay fee on a losing trade       │
│  • If we hold through settlement: fee is DEDUCTED from profit (TP widens)       │
│                                                                                  │
│ Ideal setup signals:                                                             │
│  • RSI showing overbought exhaustion (reversal forming)                          │
│  • OI rising = longs still piling in (fuel for the dump)                         │
│  • Early selling volume appearing (smart money exiting longs before fee)         │
│  • Bearish candle patterns on 15m/5m timeframe                                   │
│                                                                                  │
│ Outcome scenarios:                                                               │
│  • Price drops 2% before settlement → TP1 fills, exit, NO fee paid = BEST CASE  │
│  • Price flat at T-2m → TP widens, hold through settlement, pay fee, need 2.8%  │
│  • Price up > 0.75×funding at T-2m → emergency close, avoid paying fee on loss  │
│  • Price up > 5% → hard SL, full loss                                            │
└─────────────────────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────────────────────┐
│ LAST_MINUTE (Enter T-5m to T-0)                                                  │
├─────────────────────────────────────────────────────────────────────────────────┤
│ Capturing: Confirmed dump momentum near settlement. Likely pays fee.             │
│ TP1 Target: 2% (pure price drop) — same mechanics as FRONTRUN                   │
│ Funding Fee: Almost certainly PAY (not enough time to TP before settlement)      │
│ T-2m Safety: Applies if entry was before T-2m (e.g. entered at T-4m)            │
│ Hard SL: 5% adverse move                                                        │
│                                                                                  │
│ Risk profile:                                                                    │
│  • Shorter exposure = less squeeze risk than FRONTRUN                            │
│  • Very likely to hold through settlement → pay fee → TP widens to compensate   │
│  • Entry at T-1m bypasses T-2m check entirely — goes straight to settlement     │
│                                                                                  │
│ Ideal setup signals:                                                             │
│  • Dump already beginning (price starting to fall in last 5-10 minutes)          │
│  • Momentum loss confirmed on 5m candles                                         │
│  • You can SEE the selling pressure but it hasn't reached 2% yet                 │
│  • Moderate confidence — you want to see the move starting, not predict it       │
│                                                                                  │
│ Outcome scenarios:                                                               │
│  • Dump continues through settlement → pay fee but price drop covers it          │
│  • Price stalls → hold with widened TP, need (2%+fee) total drop for net 2%     │
│  • Squeeze in final minutes → emergency close or hard SL                         │
└─────────────────────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────────────────────┐
│ AFTER (Enter T+0 to T+1m after settlement)                                       │
├─────────────────────────────────────────────────────────────────────────────────┤
│ Capturing: Post-settlement panic dump. NO fee to pay = clean 2% profit.          │
│ TP1 Target: 2% (pure price drop) — this is 100% yours, no fee deduction          │
│ Funding Fee: NOT paid (entered after settlement — next settlement is 8h away)    │
│ T-2m Safety: Does NOT apply (no upcoming settlement to worry about)              │
│ Hard SL: 5% adverse move                                                        │
│                                                                                  │
│ Risk profile:                                                                    │
│  • Lowest risk — settlement already happened, no fee event ahead                 │
│  • No squeeze risk from funding mechanics                                        │
│  • Entry price is WORSE (price already dropped somewhat during settlement)       │
│  • Need the remaining momentum to carry price another 2% from our entry          │
│                                                                                  │
│ Ideal setup signals:                                                             │
│  • Extreme funding rate (-1% to -2%) = longs just paid huge fee → panic selling │
│  • High OI = many longs who just got hit with fee → likely to dump positions    │
│  • You were unsure pre-settlement (squeeze risk was real) → now confirmed safe  │
│  • BTC uncertain — didn't want to hold short through volatile settlement         │
│                                                                                  │
│ Outcome scenarios:                                                               │
│  • Violent dump at T+0 → ride momentum to 2%, all profit is yours               │
│  • Moderate dump → takes longer, but no fee ticking against you                  │
│  • No dump (absorbed by buyers) → stuck, but at least no fee pressure            │
│  • Price bounces → loss, but same loss as FRONTRUN/LM without the fee penalty   │
│                                                                                  │
│ Key insight: AFTER = no fee risk. You keep 100% of the 2% move.                 │
│ FRONTRUN/LASTMINUTE = fee risk. You might pay 0.5-2% and need more drop.        │
│ AFTER trades on worse entry price but zero fee overhead.                         │
└─────────────────────────────────────────────────────────────────────────────────┘

═══════════════════════════════════════════════════════════════════════════════════════
MODE SELECTION DECISION TREE:
═══════════════════════════════════════════════════════════════════════════════════════

Ask yourself: "Am I confident price will drop BEFORE settlement?"

  YES (confidence ≥ 80, clear reversal forming):
    → FRONTRUN
    → You believe the pre-settlement dump is coming — enter early, TP before fee hits
    → Best case: 2% profit with ZERO fee paid (exited before settlement)
    → You accept the risk of T-2m emergency close if wrong

  MAYBE (confidence 65-79, some signals but not all aligned):
    → LAST_MINUTE
    → You want to confirm the dump is starting before committing
    → Accept that you'll likely pay the fee (TP widens to cover it)
    → Net profit target is still 2% after fee deduction

  NO / UNSURE (confidence 60-69, or squeeze risk is elevated):
    → AFTER
    → Enter after settlement — NO fee to pay, ride post-settlement panic
    → You keep 100% of the 2% move (no fee overhead)
    → But you enter at a worse price (some dump already happened)
    → Only works if post-settlement momentum is strong enough for another 2%

  NOTHING LOOKS GOOD (confidence < 60):
    → SKIP
    → No trade is better than a forced trade

Additional factors:
- BTC in breakout → penalize FRONTRUN heavily (squeeze risk during long exposure)
- Very high ATR ratio (>3) → prefer LAST_MINUTE or AFTER (less time for volatile swings)
- Funding rate > -0.5% → AFTER less attractive (weak fee = weak post-settlement panic)
- Funding rate < -1.5% → FRONTRUN attractive (strong pre-dump pressure from big fee)
- If choosing FRONTRUN: you're betting price drops 2% in 10-30 minutes. Is that realistic
  for this coin's volatility? Check ATR — if 2% is within normal 30m range, thesis is viable.
```

### Implementation

Update `internal/llm/prompt.go` to replace the current `ENTRY MODE LOGIC` block with the expanded version above. The key additions:
1. Concrete TP/SL mechanics per mode (what the system will actually do)
2. T-2m check awareness (LLM knows FRONTRUN entries get force-closed if losing)
3. Outcome scenarios (helps LLM reason about expected value per mode)
4. Mode selection guidance with confidence thresholds

### Why This Matters

When the LLM understands the full fee mechanics, it will:
- Only recommend FRONTRUN for high-confidence setups (betting on 2% drop before settlement to avoid fee)
- Understand LASTMINUTE = accept paying fee, need bigger move to compensate
- Correctly assess AFTER as "no fee risk, clean profit, but worse entry price"
- Know FRONTRUN entries get emergency-closed at T-2m if losing (to avoid paying fee on losers)
- Make better risk/reward tradeoffs: is the confidence high enough to justify fee exposure?

### 13.2 Force-SL LLM Improvement — Mode-Aware Thesis Invalidation

#### Problem

The current Force-SL prompt is mode-agnostic. It asks "should we hold or cut?" without considering:
- **What the original entry thesis was** per mode (and therefore what invalidation looks like)
- **Whether funding settlement has already passed** (changes the cost/risk calculus)
- **The effective loss** (raw PnL + funding fee paid, if applicable)
- **Time since settlement** (for AFTER entries, how long the "panic dump" thesis has had to play out)

This means the Force-SL LLM applies the same generic "momentum against us" heuristic regardless of whether we're in a FRONTRUN trade that held through settlement (already a partial thesis failure) or an AFTER trade where the dump simply didn't happen.

#### Solution: Enhanced Force-SL Prompt + Request Data

##### New fields in `ForceSLRequest`:

```go
type ForceSLRequest struct {
    // ... existing fields ...

    // Phase 8 additions:
    FundingRatePct       float64 `json:"funding_rate_pct"`        // original funding rate at entry
    FundingFeePaid       bool    `json:"funding_fee_paid"`        // whether we held through settlement and paid
    FundingFeePaidPct    float64 `json:"funding_fee_paid_pct"`    // actual fee percentage paid (0 if not paid)
    EffectiveLossPct     float64 `json:"effective_loss_pct"`      // unrealized PnL + fee paid (true damage)
    SettlementPassed     bool    `json:"settlement_passed"`       // has funding settlement occurred since entry?
    MinutesSinceSettle   int     `json:"minutes_since_settlement"`// 0 if settlement hasn't passed yet
    TPWidened            bool    `json:"tp_widened"`              // whether TP was already widened at T-2m
}
```

##### New fields in `Position` domain struct:

```go
type Position struct {
    // ... existing fields ...

    // Phase 8: Funding fee tracking
    FundingRateAtEntry float64   // funding rate when trade was opened
    FundingFeePaid     bool      // set true after settlement passes while holding
    FundingFeePaidPct  float64   // actual fee paid (e.g. 0.008 = 0.8%)
    SettlementPassedAt *time.Time // when settlement occurred (nil if not yet)
    TPWidened          bool      // whether T-2m TP widen was applied
}
```

##### Updated Force-SL System Prompt:

```
You are a position management AI for a Binance Futures funding-rate shorting bot.
Your ONLY job: decide whether to force-close an open SHORT position early or hold.

CONTEXT:
- We are SHORT (profit when price goes down)
- Hard SL is set at 5% adverse price move (triggers automatically if reached)
- Your job: detect when the trade thesis has INVALIDATED and close early to limit loss
- Force-closing at -2% is better than waiting for -5% if the setup is dead

FUNDING FEE MECHANICS:
- We short on NEGATIVE funding. Shorts PAY longs at settlement.
- If we held through settlement → we PAID the funding fee (added cost on top of any loss)
- "Effective loss" = unrealized PnL + funding fee paid. This is the TRUE damage.
- A position showing -1.5% raw loss that also paid 0.8% fee = -2.3% effective loss.

═══════════════════════════════════════════════════════════════════════════════════
THESIS INVALIDATION PER ENTRY MODE:
═══════════════════════════════════════════════════════════════════════════════════

FRONTRUN entry thesis: "Price will dump BEFORE settlement due to pre-settlement selling."
  Invalidation signals:
  • Settlement has passed and we're still holding → thesis partially failed already
  • If post-settlement AND losing: the pre-dump didn't happen, and we paid the fee.
    Very little reason to hold unless strong delayed reversal signs exist.
  • If post-settlement AND profitable: thesis is playing out late. Hold for TP.
  • If pre-settlement AND losing: still within thesis window. More lenient on hold.
    But watch for squeeze momentum — the T-2m guard is the primary safety net here.

LAST_MINUTE entry thesis: "Dump is starting near settlement, momentum will carry through."
  Invalidation signals:
  • Settlement passed + price is ABOVE entry → dump didn't materialize, we paid the fee.
    Lower bar for FORCE_CLOSE — the confirmation we saw was false signal.
  • Price consolidating above entry with no downward momentum post-settlement
    → buyers absorbed the selling pressure. Thesis dead.
  • >15 minutes post-settlement with no progress toward TP → momentum exhausted.

AFTER entry thesis: "Post-settlement panic dump will push price down 2% from our entry."
  Invalidation signals:
  • >10 minutes post-entry with price flat or rising → panic dump didn't happen.
    The sellers are done. Remaining holders are committed. Lower bar for cut.
  • >30 minutes with < 0.5% favorable move → dump momentum fully exhausted.
    Strong signal to FORCE_CLOSE — the window for this thesis is short.
  • Price made higher high after entry → buyers are in control post-settlement.
    The panic selling was absorbed. Cut early.
  • NOTE: AFTER has NO fee overhead. The loss is purely from price movement.
    This means the raw loss IS the true loss — no hidden fee cost behind it.
    Slightly more lenient on small losses since there's no fee compounding the damage.

═══════════════════════════════════════════════════════════════════════════════════
GENERAL RULES (apply to all modes):
═══════════════════════════════════════════════════════════════════════════════════

WHEN TO FORCE_CLOSE:
- Strong momentum building AGAINST us (sustained buying, not just a wick)
- BTC has started a breakout that will drag the alt up
- Price consolidated above entry with increasing volume (buyers absorbing sells)
- The original entry thesis has clearly failed (see mode-specific signals above)
- Price made a higher high after entry and is holding above it
- Effective loss (raw + fee) exceeds -3% → thesis is deeply underwater, don't wait for -5%

WHEN TO HOLD:
- Price is just ranging/consolidating near entry (normal noise)
- Temporary wick above entry but price returned
- We are in profit (price below entry) — thesis working
- The mode-specific thesis is still intact (see criteria above)
- Current loss is small AND no clear directional signal against us
- BTC is not in breakout mode
- For AFTER entries: still within first 10 minutes (thesis needs time to play out)

AGGRESSION CALIBRATION:
- Effective loss > -3%: Be more aggressive about cutting. We're already deep.
- Fee paid + losing: Extra reason to cut. The fee is sunk cost but holding risks more.
- AFTER entry + flat after 30min: Strong cut signal. The dump window is gone.
- FRONTRUN post-settlement + losing: Cut aggressively. Original thesis window passed.
- Low original confidence (60-69): Less conviction → lower bar for FORCE_CLOSE.
- High original confidence (80+): More conviction → slightly more patient.

BIAS: Lean toward HOLD when thesis is intact. Lean toward FORCE_CLOSE when:
(a) thesis time window has expired, (b) fee was paid on top of loss, or
(c) effective loss > -3%. The hard SL at -5% is the WORST case, not the target.

CRITICAL: You are evaluating at 20x leverage. A 2% price move = 40% account impact.
```

##### Updated User Message Builder:

```go
func buildForceSLUserMessage(req *ForceSLRequest) string {
    var sb strings.Builder

    sb.WriteString(fmt.Sprintf("Position: %s SHORT\n", req.Symbol))
    sb.WriteString(fmt.Sprintf("Entry: $%.8g | Current: $%.8g\n", req.EntryPrice, req.CurrentPrice))
    sb.WriteString(fmt.Sprintf("Unrealized PnL: %.2f%% (raw price move at 20x)\n", req.UnrealizedPnlPct))

    // Phase 8: Effective loss (includes fee if paid)
    if req.FundingFeePaid {
        sb.WriteString(fmt.Sprintf("Funding fee PAID: %.2f%% (held through settlement)\n", req.FundingFeePaidPct*100))
        sb.WriteString(fmt.Sprintf("Effective loss: %.2f%% (PnL + fee paid)\n", req.EffectiveLossPct))
    } else {
        sb.WriteString("Funding fee: NOT paid (exited/entered after settlement)\n")
    }

    sb.WriteString(fmt.Sprintf("Hold time: %d minutes\n", req.HoldMinutes))
    sb.WriteString(fmt.Sprintf("Hard SL at: $%.8g (%.2f%% away)\n", req.HardSLPrice, req.HardSLDistancePct))
    sb.WriteString(fmt.Sprintf("Entry mode: %s | Original confidence: %d/100\n", req.EntryMode, req.OriginalConfidence))

    // Phase 8: Settlement context
    if req.SettlementPassed {
        sb.WriteString(fmt.Sprintf("Settlement: PASSED (%d minutes ago)\n", req.MinutesSinceSettle))
    } else {
        sb.WriteString("Settlement: NOT YET PASSED (still in pre-settlement window)\n")
    }
    if req.TPWidened {
        sb.WriteString("TP status: WIDENED at T-2m (adjusted to cover funding fee)\n")
    }
    sb.WriteString("\n")

    // ... rest of existing message (entry reasons, price action, BTC context) ...
}
```

##### Logic Changes in `position_manager.go`:

```go
// When settlement passes while position is open, mark it:
func (m *PositionManager) onSettlementPassed(pos *domain.Position) {
    pos.FundingFeePaid = true
    pos.FundingFeePaidPct = math.Abs(pos.FundingRateAtEntry)
    now := time.Now()
    pos.SettlementPassedAt = &now
}

// When building ForceSLRequest, compute effective loss:
func (m *PositionManager) buildForceSLRequest(pos domain.Position, ...) *llm.ForceSLRequest {
    // ... existing fields ...

    effectiveLoss := rawPnlPct * float64(pos.Leverage)
    if pos.FundingFeePaid {
        effectiveLoss -= pos.FundingFeePaidPct * 100 // fee is additional cost
    }

    minutesSinceSettle := 0
    if pos.SettlementPassedAt != nil {
        minutesSinceSettle = int(time.Since(*pos.SettlementPassedAt).Minutes())
    }

    req.FundingRatePct = pos.FundingRateAtEntry * 100
    req.FundingFeePaid = pos.FundingFeePaid
    req.FundingFeePaidPct = pos.FundingFeePaidPct
    req.EffectiveLossPct = effectiveLoss
    req.SettlementPassed = pos.SettlementPassedAt != nil
    req.MinutesSinceSettle = minutesSinceSettle
    req.TPWidened = pos.TPWidened
}
```

##### Key Behavior Changes:

| Scenario | Before (generic) | After (mode-aware) |
|----------|------------------|--------------------|
| FRONTRUN, post-settlement, -1.5% raw + 0.8% fee paid | LLM sees "-1.5% loss", might HOLD | LLM sees "-2.3% effective", knows thesis window passed → FORCE_CLOSE |
| AFTER entry, 30min in, -0.5% flat | LLM sees "small loss, HOLD" | LLM knows dump window expired for AFTER → FORCE_CLOSE |
| AFTER entry, 5min in, -0.3% | LLM sees "small loss, HOLD" | LLM knows thesis still has time → HOLD (same, but for the right reason) |
| LASTMINUTE, post-settlement, +0.5% profit | LLM sees "in profit, HOLD" | Same HOLD, but now knows fee was paid → net is -0.3%. Might still HOLD waiting for TP |

#### Why This Matters

The Force-SL LLM currently treats a FRONTRUN trade that's been underwater for 40 minutes (through settlement) the same as a fresh AFTER trade that's 5 minutes old. These are completely different situations:
- The FRONTRUN trade's thesis already failed (didn't dump before settlement) and has compounding damage (fee paid). Cut it.
- The AFTER trade just needs time for the panic dump to play out. Hold it.

Mode-aware thesis invalidation means fewer premature cuts on AFTER trades and faster exits on failed FRONTRUN/LASTMINUTE trades.

---

## 14. Open Questions

1. **Should we support multiple AFTER intents per cycle?** Currently `firedInWindow[AFTER] = true` means only one entry per funding cycle. With sub-second execution, we could potentially enter 2-3 symbols. Deferred to Phase 9.

2. **Should bid depth include multiple levels (depth5/depth10)?** `@bookTicker` only gives top-of-book. `@depth5` gives 5 best levels — more accurate for sizing but more bandwidth. Start with top-of-book, upgrade if slippage data shows we regularly eat through level 1.

3. **IOC (Immediate-Or-Cancel) vs plain market order?** IOC guarantees no partial fill hangs. Binance market orders on futures already behave as IOC. No change needed.

4. **T-2m check granularity:** Should the check fire exactly once at T-2m, or continuously from T-2m to T-0 (e.g. every 5s)? Single check is simpler but could miss a spike at T-90s. Start with single check, upgrade to polling if needed based on observed outcomes.

---

## 15. Success Criteria

| Metric | Target |
|--------|--------|
| T+0 latency (order dispatch) | < 10ms |
| Fill slippage vs top-of-book bid | < 5 bps (median) |
| AFTER entries that were size-reduced due to thin book | < 30% of total |
| Entries skipped by staleness guard | < 10% of total |
| Zero double-fires (trigger + scheduler) | 0 incidents |
| FRONTRUN/LASTMINUTE TP1 fills before settlement (2% pure) | > 20% of entries |
| T-2m emergency closes that avoided funding fee loss | tracked (no target yet) |
| LLM mode selection matches confidence guidelines | > 80% adherence |
| Force-SL cuts on post-settlement FRONTRUN losers (thesis expired) | faster than before |
| Force-SL holds on early AFTER entries (< 10min) | no premature cuts |
| Average loss on force-closed positions | < -3% effective (raw + fee) |

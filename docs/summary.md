# Futures Bot — System Summary

---

## Stack

| Layer | Choice |
|---|---|
| Language | Go |
| Exchange | Binance Futures (`/fapi`) |
| Database | PostgreSQL + pgvector |
| Cache / state | Redis |
| LLM | OpenAI (GPT-4.1-mini default) |
| Notifications | Telegram |
| Deployment | Docker Compose |

---

## High-Level Flow

```
WebSocket streams (mark price, funding, klines, user data)
        │
        ▼
  Market Engine (in-memory cache)
        │
  Scheduler fires every 1–5 min inside T-30m → T+1m window
        │
        ▼
  Scan → Filter → Score → LLM → Intent Queue
        │
        ▼
  Execution Engine → place MARKET SELL → await WS fill
        │
        ▼
  Position Manager (SL / TP1 / trailing / force-SL / pre-settlement)
        │
        ▼
  Trade Record → pgvector Memory → Telegram notification
```

---

## Scheduler Windows

Funding settlements happen at UTC `00:00 04:00 08:00 12:00 16:00 20:00`.

| Window | Time range | Interval |
|---|---|---|
| `FRONTRUN` | `T-30m → T-5m` | every 5 min |
| `LAST_MINUTE` | `T-5m → T+0` | every 1 min |
| `AFTER` | `T+0 → T+1m` | — |

Outside these windows the bot does nothing.

---

## Scan Pipeline

Each scan cycle runs these steps in order. Any step can abort the cycle.

### 1. Risk pre-check
Aborts if: kill switch active, cooldown active, max positions reached (`2`), daily loss limit reached (`2 losses`).

### 2. Funding scanner
- Reads all funding rates from the in-memory cache (populated by WS stream).
- Hard filter: `−2% ≤ rate ≤ −0.2%`
- Skips symbols with `1h` funding interval.
- Skips symbols whose `nextFunding` does not align with the current settlement window (±65 min tolerance). This removes 8h-interval symbols at windows where they are not settling (e.g. at UTC 04:00, an 8h symbol settling at 08:00 is skipped).
- Returns top-N candidates sorted by funding score.

### 3. ROI filter
- Drops symbols where 24h price change (`roi_1d`) < 15%.
- Candle data is seeded from REST on first subscription, so this works from boot.

### 4. Volume filter
- Drops symbols with 24h volume < 50M USDT.

### 5. Indicator computation (parallel)
For each remaining candidate:
- **RSI-14** on 15m (40 candles)
- **RSI-7** on 5m (20 candles)
- **ATR-14** on 1h (20 candles)
- **Volume anomaly** on 5m (25 candles)
- **OI delta** 1h and 15m (from REST-polled ring buffer, every 5 min)
- **Candle patterns** on 1h, 30m, 15m, 5m (5 candles each): shooting star, bearish engulfing, upper wick rejection, evening star, doji after pump, failed breakout
- **BTC context**: trend, RSI, ATR, breakout flag (40 × 1h + 20 × 15m for BTCUSDT)

### 6. Scoring
Composite score (0–100):

| Category | Weight |
|---|---|
| Funding rate | 25 |
| Candle pattern | 20 |
| OI buildup | 15 |
| ROI quality | 15 |
| BTC weakness | 10 |
| Volume anomaly | 10 |
| Volatility quality | 5 |

### 7. Risk candidate check
- Score < 60 → reject
- ATR ratio > 6% → reject
- BTC bullish breakout → reject
- Drawdown > 10% → reject

### 8. LLM decision (if enabled)
- Top-5 scored candidates sent to GPT-4.1-mini.
- LLM returns: `SKIP` or a selected symbol with `confidence`, `entry_mode`, `entry_reasons`, `warnings`.
- Call is skipped if inputs are unchanged within the cooldown window (5 min).
- Similar past trades from pgvector memory are injected into the prompt.
- Decision is wrapped in a `TradeIntent` and queued.

### 9. Execution (LLM disabled path)
- Best scored candidate is executed directly.
- `LAST_MINUTE` window applies a tighter funding rate filter: `−0.2% to −1%`.

---

## Trade Lifecycle

### Entry
1. `MARKET SELL` placed (always short).
2. `PendingEntry` stored in Redis (keyed by Binance order ID).
3. User data WebSocket receives `ORDER_TRADE_UPDATE FILLED`.
4. `finalizeSLTP` runs using the real `avgFillPrice`:
   - Hard SL at `entry + 5%` (`STOP_MARKET`, full qty)
   - TP1 at `entry − 2%` (`TAKE_PROFIT` stop-limit with 1% buffer, 50% qty)
5. Trade record inserted to PostgreSQL.
6. Position activated in Redis.

Paper mode: fill is synchronous — `finalizeSLTP` runs immediately.

### Position management (runs every 1 second)

**Before TP1 fills:**

| Trigger | Action |
|---|---|
| Price ≥ SL | Hard SL fills → `LOSS` / `HARD_SL` |
| Price ≤ TP1 | TP1 fills → cancel hard SL, place trailing stop + breakeven SL on remaining 50% |
| Leveraged PnL > 1% AND held ≥ 5 min | Move SL to breakeven |
| Force-SL check (LLM) | If raw PnL < −0.5% and held ≥ 5 min: ask LLM whether to close. Interval: every 5 min (mild loss) or every 1 min (PnL < −2%) |

**After TP1 fills (trailing 50% remains):**

| Trigger | Action |
|---|---|
| Trailing stop fills | `WIN` / `TP_TRAIL` |
| Breakeven SL fills | `PARTIAL_WIN` or `BREAKEVEN` / `TP_TRAIL` |

**Pre-settlement check (T-2m, runs every 10 sec):**

Applies to `FRONTRUN` and `LAST_MINUTE` positions only, before TP1 fills.

| Check | Action |
|---|---|
| Price moved against us > `0.75 × |funding_rate|` | Emergency market close (`FORCE_SL`) |
| TP1 not yet filled | Widen TP1 to `2% + |funding_rate|` |

**Manual close detection:**
Any reduce-only fill that doesn't match a tracked order ID is treated as `MANUAL` close.

### Close results

| Result | Meaning |
|---|---|
| `WIN` | Trailing stop filled (full run after TP1) |
| `PARTIAL_WIN` | TP1 filled, trailing SL filled at breakeven |
| `BREAKEVEN` | TP1 filled, remaining closed at or below entry |
| `LOSS` | Hard SL hit |
| `FORCE_SL` | LLM or pre-settlement emergency close |
| `MANUAL` | Closed from Binance app or external action |

After `LOSS` or `FORCE_SL`: 15-min cooldown set, daily loss counter incremented.

---

## Candle Backfill

On each new symbol subscription, the bot fetches REST history before subscribing to the WS stream:

| Timeframe | Candles fetched | Why |
|---|---|---|
| `5m` | 25 | RSI-7 + volume anomaly baseline |
| `15m` | 40 | RSI-14 (Wilder smoothing) |
| `30m` | 5 | Candle pattern detection |
| `1h` | 40 | ATR-14 + BTC context |
| `4h` | 5 | calcPriceROI |
| `1d` | 5 | calcPriceROI |

OI history is not backfilled — it builds up from REST polling within one 5-min cycle.

---

## pgvector Memory

When a trade closes, its indicators are embedded (OpenAI `text-embedding-3-small`, 1536 dims) and stored in `trade_memories`. Before each LLM call, the top-5 most similar past trades are retrieved and injected as context summaries.

Skipped trades can also be embedded (controlled by `memory.embed_skips`). After a configurable delay, skip decisions are validated against actual price movement.

---

## Risk Controls

| Control | Rule |
|---|---|
| Max positions | 2 concurrent |
| Daily loss limit | 2 losses → trading disabled until 00:00 UTC |
| Cooldown | 15 min after any loss or force-SL |
| Kill switch | Redis flag; set on critical errors or manually; blocks all new entries |
| Max drawdown | 10% of account → reject new entries |
| ATR ratio | > 6% → too volatile, reject |
| BTC breakout | Bullish breakout detected → no new alt shorts |

---

## Database

```
trades              — every opened/closed trade
daily_summaries     — aggregated per day (upserted by summary service)
trade_memories      — pgvector embeddings of past trades
trade_embeddings    — raw vectors
indicator_snapshots — indicator state at entry
risk_state          — daily loss tracking
```

Redis holds runtime state only: active positions, pending entries, pending protections, cooldown, kill switch, scheduler locks, funding snapshot.

---

## Notifications (Telegram)

Sent for: trade opened, trade closed, SL moved to breakeven, kill switch activated, daily loss limit reached, funding window skipped.

Confirmation required for `FRONTRUN` window entries only. No confirmation at `T+0` (latency critical).

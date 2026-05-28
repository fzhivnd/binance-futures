# ROI Backfill Design

## Problem

`roi_1d` (and other timeframe ROIs) read from the in-memory `CandleStore`, which is populated
solely by the WebSocket kline stream. The store starts empty on boot and only receives a closed
candle after a day boundary passes while the bot is running.

Result: any symbol that enters the top-N scanner list on startup (or re-enters after being purged)
has `roi_1d=0`, which causes it to be immediately dropped by `FilterByROI` even if it is a valid
candidate.

## Root Cause

`updateKlineSubscriptions` subscribes to kline streams for new symbols but does not seed historical
data. `calcPriceROI` needs at least one closed candle — it never gets one until the WS naturally
delivers it.

---

## What Each Consumer Actually Needs

### 1. ROI filter (`calcPriceROI`)
- Reads: `1d`, `4h`, `1h` — 2 candles each
- Needs: **2 candles** per timeframe (index 0 = prior closed, index 1 = current)

### 2. RSI-14 on 15m (`indicator.Engine`)
- Reads: 40 × `15m` candles
- Needs: **`period + 1` = 15 minimum**, 40 for accurate Wilder smoothing

### 3. RSI-7 on 5m
- Reads: 20 × `5m` candles
- Needs: **8 minimum**, 20 for accuracy

### 4. ATR-14 on 1h
- Reads: 20 × `1h` candles
- Needs: **15 minimum**, 20 for accuracy

### 5. Candle patterns (`DetectPatterns`)
- Reads: 5 candles from `1h`, `30m`, `15m`, `5m`
- Needs: **3 minimum** (uses last 3: `c`, `p`, `pp`)

### 6. Volume anomaly (`VolumeAnomaly`)
- Reads: up to 25 × `5m` candles
- Needs: **2 minimum**, 25 for a meaningful 24-candle average baseline

### 7. BTC context (`ComputeBTCContext`)
- Reads: 40 × `1h` + 20 × `15m` for BTCUSDT
- Needs: **20 × `1h`** minimum (hard-coded guard), 40 for RSI/ATR accuracy

### 8. OI delta (`OIHistory.Delta`)
- Source: REST polling every 5 minutes — **cannot be backfilled from klines**
- The `oiPoller` runs every 5 minutes against live REST. On startup, no history exists.
- OI delta just returns `0, err` when insufficient — the indicator engine silently skips it.
- **No backfill needed here** — OI builds up naturally within one poll cycle (5 min).

---

## Required Candle Limits Per Timeframe

| Timeframe | Used by | Minimum | **Backfill limit** |
|-----------|---------|---------|-------------------|
| `5m`      | RSI-7, VolumeAnomaly, DetectPatterns | 8 | **25** |
| `15m`     | RSI-14, DetectPatterns, BTC context | 15 | **40** |
| `30m`     | DetectPatterns | 3 | **5** |
| `1h`      | ATR-14, DetectPatterns, calcPriceROI, BTC context | 15 | **40** |
| `4h`      | calcPriceROI | 2 | **5** |
| `1d`      | calcPriceROI | 2 | **5** |

2 candles was not enough. Correct limits are listed above.

---

## Fix: REST Backfill on New Subscription

When a symbol is newly added to the kline subscription set, fetch the required history for
each timeframe from the Binance REST API and inject it into the `CandleStore` before the
first scan runs.

### REST endpoint

```
GET /fapi/v1/klines?symbol=FLNCUSDT&interval=1h&limit=40
```

Returns array of kline arrays. Each entry: `[openTime, open, high, low, close, volume, closeTime, ...]`.
A candle is closed if `closeTime < now`.

### Where to implement

| Location | Change |
|---|---|
| `internal/exchange/BinanceClient` | Add `FetchKlines(ctx, symbol, interval, limit)` — returns `[]domain.Candle` |
| `internal/market/MarketEngine` | Add `SeedCandles(candles []domain.Candle)` — calls `CandleStore.Update` in a loop |
| `internal/app/websocket.go` | In `updateKlineSubscriptions`, backfill each new symbol before subscribing |

### Flow

```
updateKlineSubscriptions()
  └─ for each newly added symbol:
       for each (timeframe, limit) in backfill table:
         candles = binanceClient.FetchKlines(ctx, symbol, tf, limit)
         engine.SeedCandles(candles)
       wsKlines.Subscribe(...)   ← subscribe after seed
```

### Error handling

Backfill failure is non-fatal — log a warning and continue. The symbol will still be subscribed;
it just starts with no history and indicators degrade gracefully (same as today).

### Concurrency

`CandleStore.Update` already holds a write lock — no additional locking needed. Backfill runs
synchronously before the WS subscribe call, so there is no race between seeded data and
incoming WS updates.

### Rate limits

Binance `/fapi/v1/klines` costs **2 weight** per call (futures). 6 calls per symbol.
Top-N = 20 symbols → 120 weight per subscription cycle. Binance futures limit = 2400/min.
Well within budget even at full 200-symbol cold start (1200 weight).

---

## What Is NOT Backfilled

| Data | Why not needed |
|---|---|
| OI history | Comes from REST polling, not klines. Builds up within 5 min naturally. Indicators degrade gracefully when missing. |
| Funding rates | Already populated from the funding WS stream before scan runs. |
| Mark price / ticker | Already populated from the funding/mark price stream. |

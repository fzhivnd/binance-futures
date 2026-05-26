# Personal Binance Futures Funding-Rate Trading Bot — Finalized Design

---

## 1. Summary

A low-latency funding-rate shorting bot for Binance Futures built as a single monolith service with:

- One programming language (Go)
- Local-first deployment
- OpenClaw + LLM-assisted decision engine
- Funding-rate + momentum + candlestick strategy
- Memory-enhanced trade learning via pgvector
- Execution-sensitive architecture

### Recommended Stack

| Layer         | Choice                              |
|---------------|-------------------------------------|
| Language      | Golang                              |
| Exchange API  | Binance Futures API                 |
| DB            | PostgreSQL                          |
| Vector DB     | pgvector                            |
| Cache         | Redis                               |
| LLM           | OpenAI GPT-4.1-mini / local Qwen    |
 | OpenClaw      |    |
| Scheduler     | Native goroutines + cron engine     |
| Telegram      | Telegram Bot API                    |
| Deployment    | Docker Compose                      |
| Market Stream | Binance WebSocket                   |
| Indicators    | TA-Lib / custom Go indicators       |

> **Why Go over Java?** Lower memory footprint, faster startup, easier concurrency and WebSocket fanout, simpler deployment, lower VPS cost. This is a latency-sensitive trading system — not an enterprise backend.

---

## 2. High-Level Design

```
┌────────────────────┐
│  Binance WebSocket │
└─────────┬──────────┘
          │
          ▼
┌─────────────────────────┐
│   Market Data Engine    │
│  - ticker cache         │
│  - funding cache        │
│  - candle aggregator    │
└─────────┬───────────────┘
          │
          ▼
┌─────────────────────────┐
│   Candidate Scanner     │
│  funding + ROI filter   │
└─────────┬───────────────┘
          │
          ▼
┌─────────────────────────┐
│   Indicator Engine      │
│  - RSI                  │
│  - OI                   │
│  - ATR                  │
│  - candle pattern       │
│  - BTC correlation      │
└─────────┬───────────────┘
          │
          ▼
┌─────────────────────────┐
│   Scoring & Ranking     │
│  pre-LLM lightweight    │
└─────────┬───────────────┘
          │ top 3–5 only
          ▼
┌─────────────────────────┐
│   LLM Decision Engine   │
│  coin pick + trade plan │
└─────────┬───────────────┘
          │
          ▼
┌─────────────────────────┐
│   Execution Engine      │
│  - order placement      │
│  - SL/TP                │
│  - trailing             │
└─────────┬───────────────┘
          │
          ▼
┌─────────────────────────┐
│   Trade Memory Engine   │
│  pgvector similarity    │
└─────────┬───────────────┘
          │
          ▼
┌─────────────────────────┐
│  Telegram Notification  │
└─────────────────────────┘
```

---

## 3. Core Services (Inside Monolith)

### 1. Market Engine
Responsible for: funding rates, candles, ticker, open interest, BTC trend, volume, volatility.

### 2. Scheduler Engine
Runs trading windows: `T-30m`, `T-5m`, `T+0`.

### 3. Candidate Scanner
Fast filtering **before** LLM. This is critical.

The LLM should **never** analyze all coins, all indicators, or all raw market data — only the top-ranked candidates.

### 4. LLM Decision Engine
Responsible for: final coin selection, confidence score, entry mode, TP/SL strategy, skip/no-trade decisions.

### 5. Execution Engine
Responsible for: leverage, entry, stop loss, take profit, trailing stop, kill switch.

### 6. Trade Memory Engine
Responsible for: embedding past trades, similarity search, improving prompt context.

### 7. Risk Engine
Responsible for: max daily loss, max concurrent positions, cooldown, dangerous volatility filtering.

---

## 4. Main Flow & Schedule

### Funding Timeline

| Window          | Interval   | Purpose                                   |
|-----------------|------------|-------------------------------------------|
| `T-30m → T-5m`  | Every 5min | Frontrun opportunity detection            |
| `T-5m → T+1m`   | Every 1min | Last-minute & after-funding execution     |

**At each cycle:** `scan → filter → score → LLM → maybe trade`

If a position is already open, stop new frontrun entries.

### Position Limit Logic

- **Window:** `T-30m → T+0`
- **Max:** 2 open positions total (e.g. 1 frontrun + 1 last-minute, or 1 frontrun + 1 after)
- **Priority:** `frontrun > last-minute > after`

### Trade Lifecycle

```
SCAN → PRE-FILTER → INDICATOR SCORE → TOP 3 ONLY
    → LLM ANALYSIS → RISK CHECK → PLACE ORDER
    → MANAGE POSITION → STORE RESULT → VECTOR MEMORY UPDATE
```

---

## 5. Indicators, Filters & Ranking Logic

### Mandatory Filters

**Funding Rate Filter:** `-0.2% ≥ funding ≥ -2%`

| Funding Rate | Score  |
|--------------|--------|
| -0.2%        | 40     |
| -0.5%        | 70     |
| -1.0%        | 90     |
| < -2.0%      | reject |

> Extremely negative funding can indicate a crowded short or squeeze risk.

**Daily ROI Filter:** `daily ROI > 20%`

| ROI Range | Interpretation |
|-----------|----------------|
| 20–50%    | Optimal        |
| 50–80%    | Dangerous      |
| > 80%     | Avoid          |

### Additional Indicators

1. **Open Interest Delta** _(very important)_ — detect aggressive leverage buildup. Good short setup: `price up + OI up + funding deeply negative`.
2. **BTC Correlation** — avoid shorting alts during strong BTC bullish breakout; strong momentum invalidates the funding strategy.
3. **ATR Volatility** — need enough volatility for TP. Too low → skip. Too high → dangerous.
4. **Liquidation Heatmap** _(optional, future enhancement)_ — very powerful signal.

### Candlestick Strategy

**Required timeframes:** `1h`, `30m`, `15m`, `5m`

| Strength | Patterns                                                      |
|----------|---------------------------------------------------------------|
| Strong   | shooting star, bearish engulfing, evening star, upper-wick rejection |
| Medium   | doji after pump, failed breakout candle                      |

**Multi-Timeframe Confirmation example:**
- `1h` bearish → `30m` rejection → `15m` momentum loss → `5m` entry trigger

### Scoring System (Total = 100)

| Category          | Weight |
|-------------------|--------|
| Funding Rate      | 25     |
| OI Buildup        | 20     |
| BTC Weakness      | 15     |
| Candle Pattern    | 15     |
| Volume Anomaly    | 10     |
| ROI Quality       | 10     |
| Volatility Quality| 5      |

### Confidence → Position Size Mapping

| Score | Confidence | Position Size |
|-------|------------|---------------|
| 90+   | Very High  | 5%            |
| 80–89 | High       | 4%            |
| 70–79 | Medium     | 3%            |
| 60–69 | Low        | 2%            |
| < 60  | —          | skip          |

---

## 6. LLM Interface Contracts

### Coin Analysis Request

```json
{
  "timestamp": 1710000000,
  "funding_window": "T-5",
  "btc_market_context": {
    "trend": "bullish",
    "volatility": "medium",
    "momentum_score": 72
  },
  "candidate": {
    "symbol": "1000PEPEUSDT",
    "funding_rate": -0.85,
    "daily_roi": 34,
    "oi_change_1h": 18,
    "volume_change_1h": 25,
    "atr_ratio": 3.2,
    "candles": {
      "1h": "shooting_star",
      "30m": "bearish_engulfing",
      "15m": "doji",
      "5m": "rejection"
    }
  },
  "similar_past_trades": [
    {
      "outcome": "WIN",
      "profit_pct": 4.2,
      "reason": "similar funding + OI exhaustion"
    }
  ]
}
```

### LLM Response

```json
{
  "action": "OPEN_SHORT",
  "confidence": 84,
  "entry_mode": "LAST_MINUTE",
  "entry_reason": [
    "extreme funding",
    "weakening momentum",
    "bearish candle stack"
  ],
  "tp_strategy": {
    "base_tp_pct": 2,
    "trailing": true
  },
  "sl_strategy": {
    "type": "dynamic",
    "max_account_loss_pct": 5
  },
  "warning": [
    "btc momentum still bullish"
  ]
}
```

---

## 7. LLM Prompt & Agent Design

### Agent Roles

**1. Coin Selection Agent**
> You are a professional futures funding-rate reversal trader. Focus on overextended long squeeze risk, momentum exhaustion, and bearish rejection. Reason in terms of probability, not certainty. Avoid trades during strong BTC breakout momentum.

**2. Risk Evaluation Agent** — decides skip/no-trade.

**3. Trade Management Agent** — dynamic SL/TP adjustment.
> Example: "Price failed to dump after funding reset. Momentum stabilizing. Recommend moving SL to breakeven."

### LLM Optimization Rules

| Never Send             | Always Send Instead     |
|------------------------|-------------------------|
| All market data        | Preprocessed summary    |
| All coins              | Top 5 candidates only   |
| Raw candles            | Indicator signals       |
| Verbose JSON           | Compact structured JSON |

This reduces latency, cost, and hallucination.

---

## 8. pgvector Memory

### Why It Matters
Your edge improves over time. The goal: *"Find historical trades with similar conditions."*

### What to Embed Per Trade
`funding`, `ROI`, `OI`, `BTC trend`, `candle patterns`, `volatility`, `result`

### Similarity Query Example
```
Current setup: funding=-0.9, ROI=40%, OI=+18%, BTC bullish (weak)
→ Retrieve: top 5 most similar historical setups
→ Inject: summarized lessons into LLM prompt
```

> **Important:** Do **not** inject full historical trades. Inject **summarized lessons only**.
> Example: *"Past similar setups performed poorly when BTC momentum > 80."*

---

## 9. Database Schemas

### PostgreSQL

```sql
CREATE TABLE trades (
  id           UUID PRIMARY KEY,
  symbol       VARCHAR(32),
  side         VARCHAR(8),
  entry_mode   VARCHAR(32),
  leverage     INT,
  confidence   INT,
  funding_rate NUMERIC,
  daily_roi    NUMERIC,
  entry_price  NUMERIC,
  exit_price   NUMERIC,
  pnl          NUMERIC,
  result       VARCHAR(16),
  created_at   TIMESTAMP
);

CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE trade_embeddings (
  trade_id  UUID PRIMARY KEY,
  embedding vector(1536),
  metadata  JSONB
);

CREATE TABLE risk_state (
  trade_date       DATE PRIMARY KEY,
  daily_loss_count INT,
  disabled         BOOLEAN
);
```

### Redis Use Cases

| Use Case               | Notes                      |
|------------------------|----------------------------|
| Cooldown state         | After-loss lockout         |
| Active positions       | Fast runtime lookup        |
| Scheduler locks        | Prevent duplicate cycles   |
| Market snapshot cache  | Latest ticker/funding      |
| Kill switch state      | Emergency flag             |

> Do **not** use PostgreSQL for fast runtime states.

---

## 10. Execution & SL/TP Logic

### Entry Price

`avgPrice` from Binance market order response (actual weighted fill price). Falls back to `candidate.MarkPrice` if response returns zero.

### Order Types

| Order      | Type             | Notes                                                      |
|------------|------------------|------------------------------------------------------------|
| Entry      | `MARKET`         | Always market fill                                         |
| Stop Loss  | `STOP_MARKET`    | Trigger at SL price, fills at market — no gap risk        |
| Take Profit| `TAKE_PROFIT`    | Stop-limit with 1% buffer below trigger                   |
| Breakeven  | `STOP_MARKET`    | Replaces initial SL after profit threshold is hit         |

### Position Rules

| Rule              | Trigger                          | Action                                                   |
|-------------------|----------------------------------|----------------------------------------------------------|
| Initial SL        | On entry (STOP_MARKET)           | Placed at `entryPrice + slPct%`                         |
| Initial TP        | On entry (TAKE_PROFIT)           | Placed at `entryPrice - tpPct%` (+ funding adj. for frontrun/last-min) |
| Move to breakeven | `profit > BreakevenActivationPct` AND open ≥ 5 min | Cancel SL → place new STOP_MARKET at entryPrice |
| Base TP           | —                                | 2% move ≈ 40% ROI at 20x                               |
| Trailing stop     | `unrealized PnL > 1.5%`          | Enable trailing (Phase 5)                               |

### Close Detection

Positions are closed via **Binance user data stream** (`ORDER_TRADE_UPDATE` events, live mode). When a reduce-only order (SL or TP) is `FILLED`:
- `exitPrice` = actual `avgPrice` from Binance (not approximated mark price)
- Surviving leg (other SL or TP order) is cancelled
- Trade result recorded with accurate PnL

In paper mode, close detection falls back to polling `GetPosition` (REST).

---

## 11. Telegram Integration

### Notifications
- Candidate selected
- Order opened
- SL moved / TP hit
- Kill switch activated
- Risk engine disabled
- Funding window skipped

### Confirmation Flow
- Require confirmation: `T-30m → T-1m`
- No confirmation at `T+0` — latency matters more.

---

## 12. Risk Controls

| Control              | Rule                                                              |
|----------------------|-------------------------------------------------------------------|
| Daily loss limit     | 2 losses/day → disable trading                                    |
| Kill switch          | Trigger on: WebSocket instability, order rejection, abnormal volatility, Binance API degradation |
| Cooldown             | 10–15 minutes after any loss                                      |

---

## 13. Cost Estimation

### Local Development

| Item        | Cost     |
|-------------|----------|
| Local machine | Existing |
| PostgreSQL  | Free     |
| Redis       | Free     |
| Docker      | Free     |

### VPS Production

| Spec    | Assessment |
|---------|------------|
| 2 vCPU  | Sufficient |
| 4 GB RAM | Sufficient |
| Cost    | ~$10–20/month |

### LLM Cost

| Use Case        | Model            |
|-----------------|------------------|
| Fast production | GPT-4.1-mini     |
| Cheap local     | Qwen3            |
| Higher accuracy | GPT-5 mini       |
| Full local (future) | DeepSeek R1  |

Estimated LLM cost: **$10–40/month** (varies by frequency, model, prompt size).

---

## 14. Development Phases

| Phase | Focus                      | Build                                                                                |
|-------|----------------------------|--------------------------------------------------------------------------------------|
| 1     | Core Infrastructure        | Binance WebSocket, funding scanner, candle aggregation, order execution (no LLM yet) |
| 2     | Strategy Engine            | Filters, indicators, ROI, volume, volatility, scoring, risk engine — manual backtest |
| 3     | LLM + OpenClaw Integration | Compact prompt, decision engine, JSON contract                                       |
| 4     | pgvector Memory            | Trade embedding, similarity retrieval, memory summarization                          |
| 5     | Trade Management           | Dynamic SL, trailing stop, break-even logic                                          |
| 6     | Telegram & Kill Switch     | Confirmation flow, alerts, emergency disable                                         |

---

## 15. Final Recommendations

### 1. Do Not Overuse the LLM
The LLM should **decide, reason, and contextualize** — not compute indicators, scan all pairs, or analyze raw candles.

### 2. Precompute Everything
LLM input must be compact and structured. Never send raw data.

### 3. Focus on Execution Latency
Funding trades are extremely competitive. Your edge is:
- **Timing**
- **Filtering**
- **Avoiding bad setups**

Not prediction perfection.

### 4. Start Simple
Version 1: one strategy, one position, one exchange — no multi-agent complexity. Avoid premature abstraction.

### 5. The Most Important Metric
Not winrate — **expected value consistency**.

additional: detect manual action from binance app for close position

> You can have a 45% winrate and still be highly profitable — if losers are controlled and winners are trailed properly.

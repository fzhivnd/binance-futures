# Phase 2: Strategy Engine — Design Document

---

## 1. Summary

Phase 2 builds the full indicator and scoring engine on top of Phase 1's infrastructure. This phase transforms the bot from a simple funding-rate filter into a multi-factor strategy system with weighted scoring, candlestick pattern recognition, momentum analysis, and confidence-based position sizing.

### What Phase 2 Delivers

| Component | Description |
|-----------|-------------|
| Indicator Engine | RSI, OI delta, ATR, volume anomaly, BTC correlation — computed per candidate |
| Candle Pattern Detector | Multi-timeframe bearish pattern recognition (5m, 15m, 30m, 1h) |
| Weighted Scoring System | 7-category composite score (total = 100) replacing Phase 1's funding-only score |
| Confidence-Based Sizing | Position size scales with score: 2%–5% of balance |
| Enhanced Risk Engine | Volatility rejection, correlation guards, max drawdown tracking |
| BTC Context Monitor | Detects strong BTC breakout momentum that invalidates funding shorts |
| Backtester | Offline replay of historical data through the scoring pipeline for parameter tuning |

### What Phase 2 Does NOT Include

- LLM decision engine (Phase 3)
- pgvector trade memory (Phase 4)
- Dynamic trailing stop intelligence (Phase 5)
- Telegram notifications (Phase 6)

### Tech Stack (Additions to Phase 1)

| Layer | Choice | Reason |
|-------|--------|--------|
| Indicators | Custom Go implementation | No CGo dependency (avoid TA-Lib), full control over edge cases |
| BTC data | Binance WS (already connected) | `BTCUSDT` mark price + klines already flowing through Phase 1 streams |
| Backtester | CLI subcommand + CSV export | Simple replay, no external tooling |
| Historical data | Binance REST `/fapi/v1/klines` + `/fapi/v1/fundingRate` | Seed backtester with real data |

---

## 2. Project Structure (Phase 2 Additions)

```
futures/
├── cmd/
│   ├── bot/
│   │   └── main.go                     # (unchanged)
│   └── backtest/
│       └── main.go                     # Backtester entry point
├── internal/
│   ├── indicator/
│   │   ├── rsi.go                      # RSI calculation (Wilder's smoothing)
│   │   ├── atr.go                      # ATR calculation
│   │   ├── oi_delta.go                 # Open interest delta % over window
│   │   ├── volume.go                   # Volume anomaly detection
│   │   ├── btc.go                      # BTC momentum & correlation scoring
│   │   ├── candle_pattern.go           # Bearish candlestick patterns
│   │   └── indicator.go               # IndicatorSet struct, Compute() orchestrator
│   ├── scoring/
│   │   ├── scorer.go                   # Weighted composite scoring
│   │   ├── weights.go                  # Weight constants, confidence mapping
│   │   └── result.go                   # ScoredCandidate, ScoreBreakdown types
│   ├── risk/
│   │   ├── engine.go                   # Enhanced risk engine
│   │   ├── drawdown.go                 # Max drawdown tracker
│   │   └── volatility_guard.go         # ATR-based trade rejection
│   ├── backtest/
│   │   ├── runner.go                   # Replay engine
│   │   ├── data_loader.go             # Fetch historical candles/funding from Binance
│   │   ├── report.go                   # Performance metrics + CSV output
│   │   └── types.go                    # BacktestConfig, BacktestResult
│   ├── scanner/
│   │   ├── funding_scanner.go          # (modified) calls indicator + scoring pipeline
│   │   └── candidate.go               # (modified) richer Candidate struct
│   ├── domain/
│   │   ├── indicator.go               # IndicatorSnapshot, CandlePattern types
│   │   └── scored_candidate.go        # ScoredCandidate type
│   └── ...                             # (Phase 1 packages unchanged)
├── migrations/
│   ├── 003_create_indicator_snapshots.up.sql
│   └── 003_create_indicator_snapshots.down.sql
└── ...
```

### New Package Responsibilities

| Package | Responsibility | Dependencies |
|---------|---------------|--------------|
| `internal/indicator` | Compute technical indicators from market data | `domain`, `market` |
| `internal/scoring` | Weight and combine indicator signals into composite score | `domain`, `indicator` |
| `internal/risk` | Enhanced pre-trade risk evaluation | `domain`, `market`, `storage` |
| `internal/backtest` | Offline strategy replay and performance reporting | `indicator`, `scoring`, `risk`, `exchange` |
| `cmd/backtest` | CLI entry point for backtesting | `internal/backtest`, `internal/config` |

---

## 3. System Design

### 3.1 Phase 2 Pipeline Integration

```
Phase 1 Pipeline (unchanged):
    Scheduler → FundingScanner.Scan() → top N by funding

Phase 2 Additions (inserted between scanner and execution):
    ┌─────────────────────────────────────────────────────────┐
    │                                                         │
    │  FundingScanner.Scan()                                  │
    │       │                                                 │
    │       ▼                                                 │
    │  []Candidate (filtered by funding rate)                  │
    │       │                                                 │
    │       ▼                                                 │
    │  ROI Filter: reject if daily_roi < 15%                  │
    │       │                                                 │
    │       ▼                                                 │
    │  Volume Filter: reject if 24h volume < threshold M      │
    │       │                                                 │
    │       ▼                                                 │
    │  []Candidate (funding + ROI + volume qualified)         │
    │       │                                                 │
    │       ▼                                                 │
    │  IndicatorEngine.Compute(candidate)                     │
    │       │                                                 │
    │       ├── RSI(symbol, 14, 1h)                           │
    │       ├── OIDelta(symbol, 1h)                           │
    │       ├── ATR(symbol, 14, 1h)                           │
    │       ├── VolumeAnomaly(symbol, 1h)                     │
    │       ├── BTCContext()                                  │
    │       └── CandlePatterns(symbol, [5m,15m,30m,1h])       │
    │       │                                                 │
    │       ▼                                                 │
    │  IndicatorSnapshot per candidate                        │
    │       │                                                 │
    │       ▼                                                 │
    │  Scorer.Score(candidate, indicators, btcContext)         │
    │       │                                                 │
    │       ▼                                                 │
    │  []ScoredCandidate (sorted by composite score DESC)     │
    │       │                                                 │
    │       ▼                                                 │
    │  RiskEngine.Evaluate(scoredCandidate)                   │
    │       │                                                 │
    │       ├── Reject if score < 60                          │
    │       ├── Reject if ATR too high (squeeze risk)         │
    │       ├── Reject if BTC breakout momentum > threshold   │
    │       └── Reject if max drawdown breached              │
    │       │                                                 │
    │       ▼                                                 │
    │  Top candidate → ExecutionEngine.Execute()              │
    │       (with confidence-based position size)             │
    │                                                         │
    └─────────────────────────────────────────────────────────┘
```

### 3.2 New Interfaces

```go
// === Indicator Engine ===

type IndicatorEngine interface {
    Compute(ctx context.Context, symbol string) (*IndicatorSnapshot, error)
    ComputeBTCContext(ctx context.Context) (*BTCContext, error)
}

// === Scorer ===

type Scorer interface {
    Score(candidate Candidate, indicators *IndicatorSnapshot, btc *BTCContext) *ScoredCandidate
}

// === Risk Engine (enhanced) ===

type RiskEngine interface {
    // Phase 1 checks (kill switch, cooldown, position count, daily loss)
    PreCheck(ctx context.Context) error
    // Phase 2 checks (volatility, BTC correlation, drawdown, score threshold)
    EvaluateCandidate(ctx context.Context, sc *ScoredCandidate, btc *BTCContext) error
}
```

### 3.3 New Domain Types

```go
// === Indicator Types ===

type CandlePattern string
const (
    PatternNone             CandlePattern = "NONE"
    PatternShootingStar     CandlePattern = "SHOOTING_STAR"
    PatternBearishEngulfing CandlePattern = "BEARISH_ENGULFING"
    PatternEveningStar      CandlePattern = "EVENING_STAR"
    PatternUpperWickReject  CandlePattern = "UPPER_WICK_REJECTION"
    PatternDojiAfterPump    CandlePattern = "DOJI_AFTER_PUMP"
    PatternFailedBreakout   CandlePattern = "FAILED_BREAKOUT"
)

type PatternStrength string
const (
    StrengthStrong PatternStrength = "STRONG"
    StrengthMedium PatternStrength = "MEDIUM"
    StrengthWeak   PatternStrength = "WEAK"
)

type CandleSignal struct {
    Timeframe Timeframe
    Pattern   CandlePattern
    Strength  PatternStrength
}

type IndicatorSnapshot struct {
    Symbol         string
    Timestamp      time.Time

    // RSI — two timeframes matched to short trade duration (1–50 min)
    RSI14_15m      float64  // setup context (~3.5h lookback)
    RSI7_5m        float64  // entry timing (~35 min lookback)

    // Open Interest
    OIDelta1h      float64  // % change in OI over 1h (setup confirmation)
    OIDelta15m     float64  // % change in OI over 15m (recent leverage buildup)

    // ATR — 1h used only as pre-trade volatility filter, not timing
    ATR14_1h       float64  // absolute ATR value
    ATRRatio       float64  // ATR / price * 100 (normalized volatility %)

    // Volume — 5m candles: catches the surge happening at entry time
    VolChange5m    float64  // current 5m volume vs 2h average of 5m candles (ratio)
    VolumeSpike    bool     // volume > 2x average

    // Candle Patterns
    Patterns       []CandleSignal

    // Computed flags
    MomentumLoss   bool     // RSI declining on both 15m and 5m + OI flat/down
}

type BTCContext struct {
    Trend          string   // "bullish", "bearish", "neutral" — derived from 1h RSI + 1h change
    MomentumScore  int      // 0-100
    Volatility     string   // "low", "medium", "high"
    RSI14_1h       float64
    PriceChange1h  float64  // % change over last 1h candle
    PriceChange15m float64  // % change over last 15m candle — for breakout detection
    IsBreakout     bool     // strong directional move on 15m with volume spike
}

// === Scored Candidate ===

type ScoredCandidate struct {
    Candidate       Candidate
    Indicators      *IndicatorSnapshot
    CompositeScore  float64         // 0-100
    Breakdown       ScoreBreakdown
    Confidence      string          // "VERY_HIGH", "HIGH", "MEDIUM", "LOW"
    PositionSizePct float64         // derived from confidence: 2-5%
}

type ScoreBreakdown struct {
    FundingScore    float64  // max 25
    OIScore         float64  // max 15
    BTCScore        float64  // max 10
    CandleScore     float64  // max 20
    VolumeScore     float64  // max 10
    ROIScore        float64  // max 15
    VolatilityScore float64  // max 5
}
```

### 3.4 Modified Concurrency Model

Phase 2 adds no new long-running goroutines. The indicator computation happens synchronously within the scan cycle (triggered by the scheduler). The only addition is a BTC context cache that's updated alongside the existing market data flow.

```
Existing goroutines (unchanged):
    ├── WS markPrice stream
    ├── WS kline stream
    ├── MarketEngine
    ├── OI Poller
    ├── Scheduler
    ├── PositionManager
    ├── Kline subscriber
    └── Funding interval refresher

Modified flow within Scheduler scan:
    scanFn(ctx, window):
        1. FundingScanner.Scan()           — returns []Candidate (Phase 1 funding filter)
        2. ROI Filter                      — reject candidates with daily_roi < 15% (NEW)
        3. Volume Filter                   — reject candidates with 24h volume < threshold (NEW)
        4. IndicatorEngine.Compute()       — for each remaining candidate (NEW)
        4. IndicatorEngine.ComputeBTCContext() — once per cycle (NEW)
        5. Scorer.Score()                  — for each candidate (NEW)
        6. RiskEngine.EvaluateCandidate()  — for top candidate (NEW)
        7. ExecutionEngine.Execute()       — if passed (modified: uses confidence sizing)
```

---

## 4. Flows

### 4.1 Full Scan Cycle (Phase 2)

```
                    ┌──────────────┐
                    │  Scheduler   │
                    │  triggers    │
                    └──────┬───────┘
                           │
                    ┌──────▼──────────────┐
                    │ Phase 1 Pre-checks   │
                    │ (kill switch,        │
                    │  cooldown, pos limit) │
                    └──────┬──────────────┘
                           │ pass
                           ▼
                    ┌──────────────────────┐
                    │ FundingScanner.Scan() │
                    │ funding rate filter   │
                    └──────┬───────────────┘
                           │ []Candidate
                           ▼
                    ┌──────────────────────┐
                    │ ROI Filter            │
                    │ reject daily_roi < 15%│
                    └──────┬───────────────┘
                           │
                           ▼
                    ┌──────────────────────┐
                    │ Volume Filter         │
                    │ reject vol < threshold│
                    └──────┬───────────────┘
                           │ []Candidate (max 10)
                           ▼
                    ┌──────────────────────┐
                    │ ComputeBTCContext()   │
                    │ (once per cycle)      │
                    └──────┬───────────────┘
                           │ BTCContext
                           ▼
              ┌────────────────────────────────┐
              │  For each candidate (parallel): │
              │                                │
              │  ┌─────────────────────────┐   │
              │  │ IndicatorEngine.Compute()│   │
              │  │  - RSI                   │   │
              │  │  - OI delta              │   │
              │  │  - ATR                   │   │
              │  │  - Volume anomaly        │   │
              │  │  - Candle patterns       │   │
              │  └──────────┬──────────────┘   │
              │             │                   │
              │  ┌──────────▼──────────────┐   │
              │  │ Scorer.Score()           │   │
              │  │  - 7 weighted categories │   │
              │  │  - composite 0-100       │   │
              │  └──────────┬──────────────┘   │
              │             │                   │
              └─────────────┼──────────────────┘
                            │ []ScoredCandidate
                            ▼
                    ┌───────────────────────┐
                    │ Sort by composite DESC │
                    │ Take top 1             │
                    └──────┬────────────────┘
                           │
                           ▼
                    ┌──────────────────────────┐
                    │ RiskEngine.Evaluate()     │
                    │  - score >= 60?           │
                    │  - ATR not too high?      │
                    │  - BTC not breakout?      │
                    │  - drawdown not breached? │
                    └──────┬───────────────────┘
                           │ pass
                           ▼
                    ┌──────────────────────────┐
                    │ ExecutionEngine.Execute() │
                    │  posSize = confidence map │
                    └──────────────────────────┘
```

### 4.2 Indicator Computation Flow

```
IndicatorEngine.Compute(symbol)
       │
       ├── Get candles from MarketEngine
       │       15m candles (40+ for RSI-14 warmup)
       │       5m candles (20+ for RSI-7 + volume anomaly)
       │       1h candles (20+ for ATR-14 — filter only)
       │       5m, 15m, 30m, 1h candles (for candle patterns)
       │
       ├── RSI Calculation
       │       │
       │       ├── Wilder's smoothed RSI(14) on 15m closes (setup context, ~3.5h lookback)
       │       └── Wilder's smoothed RSI(7) on 5m closes (entry timing, ~35 min lookback)
       │               returns: 0-100 float each
       │
       ├── OI Delta
       │       │
       │       ├── (current_OI - OI_1h_ago) / OI_1h_ago * 100  (setup confirmation)
       │       └── (current_OI - OI_15m_ago) / OI_15m_ago * 100 (recent leverage buildup)
       │               requires: OI cache with historical snapshots
       │               returns: % change
       │
       ├── ATR Calculation
       │       │
       │       └── ATR(14) on 1h candles (pre-trade volatility filter only)
       │               ATRRatio = ATR / currentPrice * 100
       │               returns: absolute ATR + ratio %
       │
       ├── Volume Anomaly
       │       │
       │       └── current_5m_volume / avg_5m_volume (last 24 candles = 2h window)
       │               spike = ratio > 2.0
       │               returns: ratio + spike bool
       │
       └── Candle Pattern Detection
               │
               └── For each timeframe [1h, 30m, 15m, 5m]:
                       detect patterns on last 3 candles
                       returns: []CandleSignal
```

### 4.3 BTC Context Flow

```
ComputeBTCContext()
       │
       ├── Get BTCUSDT 1h candles (last 40 — RSI warmup)
       ├── Get BTCUSDT 15m candles (last 20 — breakout detection)
       │
       ├── Calculate RSI(14) on 1h BTC candles
       │
       ├── Calculate price change 1h (trend) and 15m (breakout)
       │
       ├── Determine trend:
       │       RSI > 70 && change1h > 1.5%  → "bullish"
       │       RSI < 30 && change1h < -1.5% → "bearish"
       │       RSI > 60 && change1h > 0.5%  → "bullish"
       │       RSI < 40 && change1h < -0.5% → "bearish"
       │       else                          → "neutral"
       │
       ├── Determine momentum score (0-100):
       │       Based on RSI distance from 50 + rate of change
       │
       ├── Determine volatility:
       │       ATR-based: low / medium / high
       │
       └── Detect breakout (uses 15m for lower latency):
               |change_15m| > 0.8% AND volume_spike_15m → true
               (replaces 1h change > 2% — detects moves sooner)
```

### 4.4 Scoring Flow

```
Scorer.Score(candidate, indicators, btcContext)
       │
       ├── Funding Score (max 25):
       │       mapFundingToScore(rate) * 25 / 90
       │       (reuse Phase 1 mapping, normalize to 25-point scale)
       │
       ├── OI Score (max 15):
       │       Best setup: price up + OI up + funding deeply negative
       │       oi_delta_1h > 10%  → 15
       │       oi_delta_1h > 5%   → 11
       │       oi_delta_1h > 0%   → 7
       │       oi_delta_1h <= 0%  → 2 (OI decreasing = less conviction)
       │
       ├── BTC Score (max 10):
       │       BTC bearish         → 10 (ideal for alt shorts)
       │       BTC neutral         → 7
       │       BTC bullish weak    → 3
       │       BTC breakout        → 0 (invalidates strategy)
       │
       ├── Candle Score (max 20):
       │       Sum pattern signals across timeframes
       │       Strong pattern on higher TF → more weight
       │       Multi-TF confirmation bonus
       │
       ├── Volume Score (max 10):
       │       volume_spike_5m + RSI7_5m overbought (>65) → 10 (exhaustion right now)
       │       volume_spike_5m only → 5 (surge without overbought confirmation)
       │       no spike → 3
       │
       ├── ROI Score (max 15) — based on 24h price change %:
       │       daily_roi 15-50% → 15 (optimal pump, good reversal setup)
       │       daily_roi 50-80% → 7 (dangerous, extreme momentum)
       │       daily_roi > 80%  → 0 (avoid, parabolic — too risky)
       │       daily_roi < 15%  → 0 (already filtered out)
       │
       └── Volatility Score (max 5):
               ATRRatio 1-3%    → 5 (enough movement for TP)
               ATRRatio 3-5%    → 3 (volatile but manageable)
               ATRRatio > 5%    → 0 (too dangerous)
               ATRRatio < 1%    → 0 (too flat, won't reach TP)
```

### 4.5 Backtester Flow

```
cmd/backtest
       │
       ├── Load config + backtest parameters
       │       - date range
       │       - symbols (or "all")
       │       - initial balance
       │
       ├── Fetch historical data from Binance REST
       │       - /fapi/v1/klines (1h, 30m, 15m, 5m)
       │       - /fapi/v1/fundingRate (historical)
       │       - /fapi/v1/openInterest (historical via /futures/data/openInterestHist)
       │
       ├── Build time-ordered event stream
       │       candle closes + funding settlements
       │
       ├── For each funding window in range:
       │       │
       │       ├── Reconstruct market state at that time
       │       │
       │       ├── Run full pipeline:
       │       │       Scanner → Indicators → Scorer → Risk → Execute (paper)
       │       │
       │       ├── Simulate position management:
       │       │       Check SL/TP against subsequent candles
       │       │
       │       └── Record trade result
       │
       └── Generate report:
               - Total trades, win rate, avg profit, avg loss
               - Max drawdown, Sharpe-like ratio
               - Score distribution (trades by confidence bucket)
               - Per-indicator contribution analysis
               - CSV export of all simulated trades
```

---

## 5. Detailed Logic

### 5.1 Daily ROI Filter (Mandatory Pre-Indicator Gate)

Daily ROI here means the **coin's 24-hour price change percentage** — how much the coin has pumped (or dumped) in the last 24 hours. This is NOT the funding rate yield; it measures price momentum.

The daily ROI filter is a **hard rejection gate** applied after funding rate filtering and before any indicator computation. Candidates with `daily_roi < 15%` are immediately discarded — they do not proceed to the indicator engine or scoring.

**Why:** A coin with extreme negative funding but less than 15% price increase in 24h hasn't pumped hard enough — the short squeeze risk isn't justified by the setup quality. We want coins that have run up aggressively (creating overextension) alongside deeply negative funding. Below 15% daily price change, the momentum isn't extreme enough to expect a meaningful reversal.

```go
// FilterByROI removes candidates whose 24h price change doesn't meet the minimum threshold.
// Daily ROI = (current_price - price_24h_ago) / price_24h_ago * 100
// This runs AFTER funding rate filtering, BEFORE indicator computation.
func FilterByROI(candidates []domain.Candidate, minROIPct float64) []domain.Candidate {
    filtered := make([]domain.Candidate, 0, len(candidates))
    for _, c := range candidates {
        if c.DailyROI >= minROIPct {
            filtered = append(filtered, c)
        }
    }
    return filtered
}
```

**Filter criteria:**

| 24h Price Change (Daily ROI) | Action |
|------------------------------|--------|
| < 15% | **REJECT** — pump not extreme enough |
| >= 15% | PASS to indicator engine |

**ROI scoring (applied later in Scorer, max 15 points):**

| 24h Price Change | Interpretation | Score |
|------------------|---------------|-------|
| 15–50% | Optimal — strong pump creating overextension, good reversal setup | 15 |
| 50–80% | Dangerous — extreme pump but elevated risk of continued momentum | 7 |
| > 80% | Avoid — parabolic move, too unpredictable, squeeze likely to continue | 0 |

**Daily ROI calculation:**
```
daily_roi = (current_price - price_24h_ago) / price_24h_ago * 100
```

Computed from the 1D kline close-to-close, or from comparing the current mark price against the price 24 hours ago using 1h candles.

**Example:**
- 1000PEPEUSDT: price 24h ago = $0.010, now = $0.013 → daily_roi = 30% → **PASS** (optimal range)
- DOGEUSDT: price 24h ago = $0.15, now = $0.17 → daily_roi = 13.3% → **REJECT** (< 15%)
- WIFUSDT: price 24h ago = $1.00, now = $1.85 → daily_roi = 85% → **PASS** (but scored 0 — avoid)

> Note: The 15% threshold ensures we only short coins that have pumped hard enough to create genuine overextension. Combined with deeply negative funding, this setup targets the "overheated long squeeze" scenario where aggressive longs are paying extreme funding while price is already overextended.

---

### 5.2 Volume Filter (Minimum Liquidity Gate)

The volume filter is a **hard rejection gate** applied after the ROI filter and before indicator computation. Candidates with 24h trading volume below the configured threshold (in millions USDT) are immediately discarded.

**Why:** Low-volume coins are dangerous for leveraged trading — wide spreads, slippage on entry/exit, inability to close positions quickly during adverse moves, and susceptibility to manipulation. A minimum volume floor ensures the bot only trades liquid markets where orders fill cleanly.

```go
// FilterByVolume removes candidates whose 24h trading volume is below the minimum threshold.
// volumeThresholdM is in millions of USDT (e.g., 50.0 means $50M minimum 24h volume).
func FilterByVolume(candidates []domain.Candidate, volumeThresholdM float64) []domain.Candidate {
    thresholdRaw := volumeThresholdM * 1_000_000
    filtered := make([]domain.Candidate, 0, len(candidates))
    for _, c := range candidates {
        if c.Volume24h >= thresholdRaw {
            filtered = append(filtered, c)
        }
    }
    return filtered
}
```

**Filter criteria:**

| 24h Volume (USDT) | Action |
|--------------------|--------|
| < threshold M | **REJECT** — too illiquid |
| >= threshold M | PASS to indicator engine |

**How 24h volume is obtained:**

The 24h quote volume is available from the `!ticker@arr` stream or computed by summing the last 24 1h-candle volumes from the candle store. Phase 2 adds a `Volume24h` field to the `Candidate` struct, populated during the scan step.

```go
// Addition to domain.Candidate struct
type Candidate struct {
    Symbol      string
    FundingRate float64
    MarkPrice   float64
    DailyROI    float64
    Volume24h   float64  // 24h trading volume in quote asset (USDT)
    Score       float64
}
```

**Example** (with threshold = 50M):
- 1000PEPEUSDT: 24h volume = $320M → **PASS**
- OBSCURECOINUSDT: 24h volume = $8M → **REJECT** (< 50M)
- DOGEUSDT: 24h volume = $1.2B → **PASS**

> Note: The threshold is configurable. Start conservative (e.g., 50M) and lower it only if you observe the bot skipping valid setups due to the filter. Higher thresholds = safer fills but fewer candidates.

---

### 5.3 RSI Calculation (Wilder's Smoothing)

```go
// RSI computes the 14-period RSI using Wilder's smoothing method.
// Requires at least period+1 candles (15 for RSI-14).
func RSI(candles []domain.Candle, period int) (float64, error) {
    if len(candles) < period+1 {
        return 0, fmt.Errorf("need %d candles, got %d", period+1, len(candles))
    }

    // Step 1: Calculate price changes
    changes := make([]float64, len(candles)-1)
    for i := 1; i < len(candles); i++ {
        changes[i-1] = candles[i].Close - candles[i-1].Close
    }

    // Step 2: Initial average gain/loss (simple average of first `period` changes)
    var avgGain, avgLoss float64
    for i := 0; i < period; i++ {
        if changes[i] > 0 {
            avgGain += changes[i]
        } else {
            avgLoss += -changes[i]
        }
    }
    avgGain /= float64(period)
    avgLoss /= float64(period)

    // Step 3: Wilder's smoothing for remaining data
    for i := period; i < len(changes); i++ {
        if changes[i] > 0 {
            avgGain = (avgGain*float64(period-1) + changes[i]) / float64(period)
            avgLoss = (avgLoss * float64(period-1)) / float64(period)
        } else {
            avgGain = (avgGain * float64(period-1)) / float64(period)
            avgLoss = (avgLoss*float64(period-1) + (-changes[i])) / float64(period)
        }
    }

    // Step 4: Calculate RSI
    if avgLoss == 0 {
        return 100, nil
    }
    rs := avgGain / avgLoss
    rsi := 100 - (100 / (1 + rs))

    return rsi, nil
}
```

### 5.4 ATR Calculation

```go
// ATR computes the Average True Range over `period` candles.
// True Range = max(high-low, |high-prevClose|, |low-prevClose|)
func ATR(candles []domain.Candle, period int) (float64, error) {
    if len(candles) < period+1 {
        return 0, fmt.Errorf("need %d candles, got %d", period+1, len(candles))
    }

    // Calculate True Range for each candle (starting from index 1)
    trValues := make([]float64, len(candles)-1)
    for i := 1; i < len(candles); i++ {
        hl := candles[i].High - candles[i].Low
        hpc := math.Abs(candles[i].High - candles[i-1].Close)
        lpc := math.Abs(candles[i].Low - candles[i-1].Close)
        trValues[i-1] = math.Max(hl, math.Max(hpc, lpc))
    }

    // Initial ATR: simple average of first `period` TRs
    var atr float64
    for i := 0; i < period; i++ {
        atr += trValues[i]
    }
    atr /= float64(period)

    // Wilder's smoothing for remaining
    for i := period; i < len(trValues); i++ {
        atr = (atr*float64(period-1) + trValues[i]) / float64(period)
    }

    return atr, nil
}

// ATRRatio returns ATR as percentage of current price.
func ATRRatio(atr float64, currentPrice float64) float64 {
    if currentPrice == 0 {
        return 0
    }
    return atr / currentPrice * 100
}
```

### 5.5 OI Delta Calculation

```go
// OIDelta computes the percentage change in open interest over a time window.
// Requires the OI cache to store historical snapshots.
type OIHistory struct {
    mu       sync.RWMutex
    // symbol → time-ordered OI snapshots (ring buffer, keep last 24h at 5min intervals)
    snapshots map[string][]OISnapshot
}

type OISnapshot struct {
    OI        float64
    Timestamp time.Time
}

// Delta returns (current - past) / past * 100
func (h *OIHistory) Delta(symbol string, window time.Duration) (float64, error) {
    h.mu.RLock()
    defer h.mu.RUnlock()

    snaps, ok := h.snapshots[symbol]
    if !ok || len(snaps) == 0 {
        return 0, fmt.Errorf("no OI data for %s", symbol)
    }

    current := snaps[len(snaps)-1]
    cutoff := time.Now().Add(-window)

    // Find closest snapshot to cutoff time
    var past OISnapshot
    for i := len(snaps) - 1; i >= 0; i-- {
        if snaps[i].Timestamp.Before(cutoff) {
            past = snaps[i]
            break
        }
    }

    if past.OI == 0 {
        return 0, fmt.Errorf("no historical OI for %s at window %v", symbol, window)
    }

    return (current.OI - past.OI) / past.OI * 100, nil
}
```

**OI Cache Enhancement:** Phase 1's OI cache stores only the latest value per symbol. Phase 2 adds a ring buffer of timestamped snapshots (288 entries per symbol = 24h at 5-minute polling intervals) to compute deltas.

### 5.6 Volume Anomaly Detection

```go
// VolumeAnomaly compares the most recent candle's volume against the trailing average.
// Pass 5m candles: last candle = current entry period, preceding 24 = ~2h baseline.
func VolumeAnomaly(candles []domain.Candle) (ratio float64, spike bool) {
    if len(candles) < 2 {
        return 1.0, false
    }

    // Current candle volume (last closed)
    current := candles[len(candles)-1].Volume

    // Average of preceding candles (up to 24 — ~2h on 5m timeframe)
    var sum float64
    count := min(len(candles)-1, 24)
    for i := len(candles) - 1 - count; i < len(candles)-1; i++ {
        sum += candles[i].Volume
    }
    avg := sum / float64(count)

    if avg == 0 {
        return 1.0, false
    }

    ratio = current / avg
    spike = ratio > 2.0
    return ratio, spike
}
```

### 5.7 Candlestick Pattern Detection

```go
// DetectPatterns analyzes the last 3 closed candles for bearish reversal patterns.
func DetectPatterns(candles []domain.Candle) []CandleSignal {
    if len(candles) < 3 {
        return nil
    }

    var signals []CandleSignal
    c := candles[len(candles)-1]   // current (most recent closed)
    p := candles[len(candles)-2]   // previous
    pp := candles[len(candles)-3]  // two bars ago

    body := c.Close - c.Open
    upperWick := c.High - math.Max(c.Open, c.Close)
    lowerWick := math.Min(c.Open, c.Close) - c.Low
    candleRange := c.High - c.Low

    if candleRange == 0 {
        return nil
    }

    // --- SHOOTING STAR ---
    // Small body at bottom, long upper wick (>= 2x body), after uptrend
    bodySize := math.Abs(body)
    if upperWick >= 2*bodySize && lowerWick < bodySize && p.Close < c.High {
        if p.Close > pp.Close { // confirming prior uptrend
            signals = append(signals, CandleSignal{
                Pattern:  PatternShootingStar,
                Strength: StrengthStrong,
            })
        }
    }

    // --- BEARISH ENGULFING ---
    // Current red candle body fully engulfs previous green candle body
    prevBody := p.Close - p.Open
    if body < 0 && prevBody > 0 { // current bearish, previous bullish
        if c.Open >= p.Close && c.Close <= p.Open {
            signals = append(signals, CandleSignal{
                Pattern:  PatternBearishEngulfing,
                Strength: StrengthStrong,
            })
        }
    }

    // --- UPPER WICK REJECTION ---
    // Upper wick > 60% of total range, body in lower 40%
    if upperWick/candleRange > 0.6 && body < 0 {
        signals = append(signals, CandleSignal{
            Pattern:  PatternUpperWickReject,
            Strength: StrengthStrong,
        })
    }

    // --- EVENING STAR ---
    // 3-candle pattern: big green, small body (doji-like), big red
    ppBody := pp.Close - pp.Open
    pBodySize := math.Abs(prevBody)
    if ppBody > 0 && body < 0 { // first green, last red
        avgBody := (math.Abs(float64(ppBody)) + math.Abs(body)) / 2
        if pBodySize < avgBody*0.3 { // middle candle has tiny body
            signals = append(signals, CandleSignal{
                Pattern:  PatternEveningStar,
                Strength: StrengthStrong,
            })
        }
    }

    // --- DOJI AFTER PUMP ---
    // Previous candle was a strong green, current is doji-like
    if prevBody > 0 && prevBody/math.Abs(p.High-p.Low) > 0.6 {
        if bodySize/candleRange < 0.1 { // very small body relative to range
            signals = append(signals, CandleSignal{
                Pattern:  PatternDojiAfterPump,
                Strength: StrengthMedium,
            })
        }
    }

    // --- FAILED BREAKOUT ---
    // Price made new high vs previous but closed below previous high
    if c.High > p.High && c.Close < p.High && body < 0 {
        signals = append(signals, CandleSignal{
            Pattern:  PatternFailedBreakout,
            Strength: StrengthMedium,
        })
    }

    return signals
}
```

### 5.8 BTC Context Scoring

```go
// ComputeBTCContext derives trend from 1h data and breakout from 15m data.
// Using 15m for breakout detection reduces latency from ~45 min to ~12 min.
func ComputeBTCContext(candles1h []domain.Candle, candles15m []domain.Candle) (*domain.BTCContext, error) {
    if len(candles1h) < 15 {
        return nil, fmt.Errorf("need 15+ BTC 1h candles, got %d", len(candles1h))
    }

    rsi, _ := RSI(candles1h, 14)

    // Trend uses 1h change (macro context)
    last1h := candles1h[len(candles1h)-1]
    prev1h := candles1h[len(candles1h)-2]
    change1h := (last1h.Close - prev1h.Close) / prev1h.Close * 100

    // Trend determination (thresholds tuned to 1h change magnitude)
    var trend string
    switch {
    case rsi > 70 && change1h > 1.5:
        trend = "bullish"
    case rsi < 30 && change1h < -1.5:
        trend = "bearish"
    case rsi > 60 && change1h > 0.5:
        trend = "bullish"
    case rsi < 40 && change1h < -0.5:
        trend = "bearish"
    default:
        trend = "neutral"
    }

    // Momentum score: how far RSI is from neutral (50)
    momentum := int(math.Abs(rsi-50) * 2) // 0-100
    if momentum > 100 {
        momentum = 100
    }

    // Volatility from ATR on 1h
    atr, _ := ATR(candles1h, 14)
    atrRatio := ATRRatio(atr, last1h.Close)
    var volatility string
    switch {
    case atrRatio > 3:
        volatility = "high"
    case atrRatio > 1.5:
        volatility = "medium"
    default:
        volatility = "low"
    }

    // Breakout detection on 15m — detects moves sooner than 1h change
    var change15m float64
    var isBreakout bool
    if len(candles15m) >= 2 {
        last15m := candles15m[len(candles15m)-1]
        prev15m := candles15m[len(candles15m)-2]
        change15m = (last15m.Close - prev15m.Close) / prev15m.Close * 100
        _, volumeSpike := VolumeAnomaly(candles15m)
        isBreakout = math.Abs(change15m) > 0.8 && volumeSpike
    }

    return &domain.BTCContext{
        Trend:          trend,
        MomentumScore:  momentum,
        Volatility:     volatility,
        RSI14_1h:       rsi,
        PriceChange1h:  change1h,
        PriceChange15m: change15m, // used for breakout detection
        IsBreakout:     isBreakout,
    }, nil
}
```

### 5.9 Weighted Scoring Logic

```go
type WeightConfig struct {
    Funding    float64 // 25
    OI         float64 // 15
    BTC        float64 // 10
    Candle     float64 // 20
    Volume     float64 // 10
    ROI        float64 // 15
    Volatility float64 // 5
}

var DefaultWeights = WeightConfig{
    Funding:    25,
    OI:         15,
    BTC:        10,
    Candle:     20,
    Volume:     10,
    ROI:        15,
    Volatility: 5,
}

func (s *ScorerImpl) Score(c domain.Candidate, ind *IndicatorSnapshot, btc *BTCContext) *ScoredCandidate {
    var bd ScoreBreakdown

    // 1. Funding Score (max 25)
    rawFunding := mapFundingToScore(c.FundingRate) // 0-90 from Phase 1
    bd.FundingScore = rawFunding / 90 * s.weights.Funding

    // 2. OI Score (max 15)
    switch {
    case ind.OIDelta1h > 15:
        bd.OIScore = s.weights.OI
    case ind.OIDelta1h > 10:
        bd.OIScore = s.weights.OI * 0.75
    case ind.OIDelta1h > 5:
        bd.OIScore = s.weights.OI * 0.5
    case ind.OIDelta1h > 0:
        bd.OIScore = s.weights.OI * 0.3
    default:
        bd.OIScore = s.weights.OI * 0.13
    }

    // 3. BTC Score (max 10)
    switch {
    case btc.IsBreakout && btc.Trend == "bullish":
        bd.BTCScore = 0
    case btc.Trend == "bullish" && btc.MomentumScore > 70:
        bd.BTCScore = s.weights.BTC * 0.2
    case btc.Trend == "bullish":
        bd.BTCScore = s.weights.BTC * 0.3
    case btc.Trend == "neutral":
        bd.BTCScore = s.weights.BTC * 0.7
    case btc.Trend == "bearish":
        bd.BTCScore = s.weights.BTC
    }

    // 4. Candle Score (max 20)
    bd.CandleScore = scoreCandlePatterns(ind.Patterns, s.weights.Candle)

    // 5. Volume Score (max 10) — 5m RSI signals exhaustion at entry time
    switch {
    case ind.VolumeSpike && ind.RSI7_5m > 65:
        bd.VolumeScore = s.weights.Volume // spike + overbought on 5m = exhaustion right now
    case ind.VolumeSpike:
        bd.VolumeScore = s.weights.Volume * 0.5
    default:
        bd.VolumeScore = s.weights.Volume * 0.3
    }

    // 6. ROI Score (max 15) — based on 24h price change %
    // Measures how much the coin has pumped; higher pump = more overextension
    switch {
    case c.DailyROI >= 20 && c.DailyROI <= 50:
        bd.ROIScore = s.weights.ROI       // optimal: strong pump, good reversal setup
    case c.DailyROI > 50 && c.DailyROI <= 80:
        bd.ROIScore = s.weights.ROI * 0.47 // dangerous: extreme momentum, risky short
    case c.DailyROI > 80:
        bd.ROIScore = 0                    // avoid: parabolic, too unpredictable
    default:
        bd.ROIScore = 0                    // < 15% already filtered out upstream
    }

    // 7. Volatility Score (max 5)
    switch {
    case ind.ATRRatio >= 1 && ind.ATRRatio <= 3:
        bd.VolatilityScore = s.weights.Volatility
    case ind.ATRRatio > 3 && ind.ATRRatio <= 5:
        bd.VolatilityScore = s.weights.Volatility * 0.6
    default:
        bd.VolatilityScore = 0
    }

    composite := bd.FundingScore + bd.OIScore + bd.BTCScore +
        bd.CandleScore + bd.VolumeScore + bd.ROIScore + bd.VolatilityScore

    confidence, sizePct := mapScoreToConfidence(composite)

    return &ScoredCandidate{
        Candidate:       c,
        Indicators:      ind,
        CompositeScore:  composite,
        Breakdown:       bd,
        Confidence:      confidence,
        PositionSizePct: sizePct,
    }
}
```

### 5.10 Candle Pattern Scoring (Multi-Timeframe)

```go
// scoreCandlePatterns scores candle signals with timeframe weighting.
// Higher timeframes carry more weight. Multi-TF confirmation adds a bonus.
func scoreCandlePatterns(signals []CandleSignal, maxScore float64) float64 {
    if len(signals) == 0 {
        return 0
    }

    // Timeframe weight multipliers — near-equal to reflect short trade duration (1–50 min)
    tfWeight := map[domain.Timeframe]float64{
        domain.Timeframe1h:  1.00,
        domain.Timeframe30m: 0.95,
        domain.Timeframe15m: 0.90,
        domain.Timeframe5m:  0.85,
    }

    // Strength multipliers
    strWeight := map[PatternStrength]float64{
        StrengthStrong: 1.0,
        StrengthMedium: 0.7,
        StrengthWeak:   0.35,
    }

    var rawScore float64
    tfHit := make(map[domain.Timeframe]bool)

    for _, sig := range signals {
        tw := tfWeight[sig.Timeframe]
        sw := strWeight[sig.Strength]
        rawScore += tw * sw
        tfHit[sig.Timeframe] = true
    }

    // Multi-timeframe confirmation bonus
    confirmedTFs := len(tfHit)
    var bonus float64
    switch {
    case confirmedTFs >= 4:
        bonus = 0.4 // all TFs aligned
    case confirmedTFs >= 3:
        bonus = 0.25
    case confirmedTFs >= 2:
        bonus = 0.1
    }

    // Normalize: max possible raw score = 3.70 (one STRONG on each of 4 TFs: 1.00+0.95+0.90+0.85)
    normalized := rawScore / 3.70
    if normalized > 1.0 {
        normalized = 1.0
    }

    return (normalized + bonus) * maxScore
}
```

### 5.11 Confidence to Position Size Mapping

```go
func mapScoreToConfidence(score float64) (confidence string, sizePct float64) {
    switch {
    case score >= 90:
        return "VERY_HIGH", 5.0
    case score >= 80:
        return "HIGH", 4.5
    case score >= 70:
        return "MEDIUM", 3.0
    case score >= 60:
        return "LOW", 2.0
    default:
        return "SKIP", 0.0
    }
}
```

### 5.12 Enhanced Risk Engine

```go
type RiskEngineImpl struct {
    cache       StateCache
    riskRepo    RiskRepository
    market      MarketDataProvider
    drawdown    *DrawdownTracker
    cfg         *RiskConfig
}

type RiskConfig struct {
    MaxDailyLosses        int     // 2
    MaxDrawdownPct        float64 // 10% of starting balance
    MaxATRRatio           float64 // 6% — reject if volatility too extreme
    BTCBreakoutReject     bool    // true — skip trades during BTC breakout
    MinCompositeScore     float64 // 60
    CooldownMinutes       int     // 15
}

// PreCheck runs Phase 1 checks (unchanged).
func (r *RiskEngineImpl) PreCheck(ctx context.Context) error {
    if kill, _ := r.cache.GetKillSwitch(ctx); kill {
        return fmt.Errorf("kill switch active")
    }
    if cool, _ := r.cache.IsOnCooldown(ctx); cool {
        return fmt.Errorf("on cooldown")
    }
    positions, _ := r.cache.GetActivePositions(ctx)
    if len(positions) >= r.cfg.MaxPositions {
        return fmt.Errorf("max positions reached")
    }
    losses, _ := r.riskRepo.GetDailyLossCount(ctx, time.Now().UTC())
    if losses >= r.cfg.MaxDailyLosses {
        return fmt.Errorf("daily loss limit reached")
    }
    return nil
}

// EvaluateCandidate runs Phase 2 indicator-aware checks.
func (r *RiskEngineImpl) EvaluateCandidate(ctx context.Context, sc *ScoredCandidate, btc *BTCContext) error {
    // Score threshold
    if sc.CompositeScore < r.cfg.MinCompositeScore {
        return fmt.Errorf("score %.1f below threshold %.1f", sc.CompositeScore, r.cfg.MinCompositeScore)
    }

    // Volatility guard
    if sc.Indicators.ATRRatio > r.cfg.MaxATRRatio {
        return fmt.Errorf("ATR ratio %.2f%% exceeds max %.2f%%", sc.Indicators.ATRRatio, r.cfg.MaxATRRatio)
    }

    // BTC breakout guard
    if r.cfg.BTCBreakoutReject && btc.IsBreakout && btc.Trend == "bullish" {
        return fmt.Errorf("BTC bullish breakout detected, skipping alt short")
    }

    // Max drawdown guard
    if r.drawdown.CurrentDrawdownPct() > r.cfg.MaxDrawdownPct {
        return fmt.Errorf("max drawdown %.2f%% breached (limit %.2f%%)",
            r.drawdown.CurrentDrawdownPct(), r.cfg.MaxDrawdownPct)
    }

    return nil
}
```

### 5.13 Drawdown Tracker

```go
type DrawdownTracker struct {
    mu             sync.Mutex
    peakBalance    float64
    currentBalance float64
}

func NewDrawdownTracker(initialBalance float64) *DrawdownTracker {
    return &DrawdownTracker{
        peakBalance:    initialBalance,
        currentBalance: initialBalance,
    }
}

func (d *DrawdownTracker) Update(newBalance float64) {
    d.mu.Lock()
    defer d.mu.Unlock()
    d.currentBalance = newBalance
    if newBalance > d.peakBalance {
        d.peakBalance = newBalance
    }
}

func (d *DrawdownTracker) CurrentDrawdownPct() float64 {
    d.mu.Lock()
    defer d.mu.Unlock()
    if d.peakBalance == 0 {
        return 0
    }
    return (d.peakBalance - d.currentBalance) / d.peakBalance * 100
}
```

### 5.14 OI Cache Enhancement (Historical Snapshots)

Phase 1's OI cache stores only the latest value. Phase 2 wraps it with a history ring buffer:

```go
type OIHistoryCache struct {
    mu        sync.RWMutex
    current   map[string]float64        // symbol → latest OI (Phase 1 compat)
    history   map[string]*ring.Ring     // symbol → ring buffer of OISnapshot
    maxSize   int                        // 288 = 24h at 5-min intervals
}

type OISnapshot struct {
    OI        float64
    Timestamp time.Time
}

func NewOIHistoryCache(maxSize int) *OIHistoryCache {
    return &OIHistoryCache{
        current: make(map[string]float64),
        history: make(map[string]*ring.Ring),
        maxSize: maxSize,
    }
}

func (c *OIHistoryCache) Update(symbol string, oi float64) {
    c.mu.Lock()
    defer c.mu.Unlock()

    c.current[symbol] = oi

    r, ok := c.history[symbol]
    if !ok {
        r = ring.New(c.maxSize)
        c.history[symbol] = r
    }

    r.Value = OISnapshot{OI: oi, Timestamp: time.Now()}
    c.history[symbol] = r.Next()
}

func (c *OIHistoryCache) Delta(symbol string, window time.Duration) (float64, error) {
    c.mu.RLock()
    defer c.mu.RUnlock()

    r, ok := c.history[symbol]
    if !ok {
        return 0, fmt.Errorf("no OI history for %s", symbol)
    }

    var current, past OISnapshot
    cutoff := time.Now().Add(-window)

    // Walk the ring to find current and past
    r.Do(func(v any) {
        if v == nil {
            return
        }
        snap := v.(OISnapshot)
        if snap.Timestamp.After(current.Timestamp) {
            current = snap
        }
        if snap.Timestamp.Before(cutoff) && snap.Timestamp.After(past.Timestamp) {
            past = snap
        }
    })

    if past.OI == 0 {
        return 0, fmt.Errorf("insufficient OI history for %s", symbol)
    }

    return (current.OI - past.OI) / past.OI * 100, nil
}
```

### 5.15 Modified Execution Flow (Confidence-Based Sizing)

The execution engine's `Execute()` method changes to accept position size from the scored candidate:

```go
// Phase 1: positionSizePct was fixed from config
// Phase 2: positionSizePct comes from ScoredCandidate.PositionSizePct

func (e *ExecutionEngine) ExecuteScored(ctx context.Context, sc *ScoredCandidate, window WindowType) error {
    // ... pre-checks same as Phase 1 ...

    balance, err := e.executor.GetAccountBalance(ctx)
    if err != nil {
        return err
    }

    // Phase 2: dynamic position sizing
    margin := balance.Available * sc.PositionSizePct / 100
    quantity := margin * float64(e.cfg.Leverage) / sc.Candidate.MarkPrice

    // ... rest of execution same as Phase 1 ...
    // Trade record now includes confidence score from scoring
    trade := &domain.Trade{
        // ...
        Confidence: int(sc.CompositeScore),
        // ...
    }
}
```

### 5.16 Backtester Implementation

```go
type BacktestConfig struct {
    StartDate      time.Time
    EndDate        time.Time
    Symbols        []string // empty = all USDT-M perps
    InitialBalance float64
    Leverage       int
    Weights        WeightConfig
    RiskConfig     RiskConfig
}

type BacktestResult struct {
    TotalTrades    int
    WinRate        float64
    AvgProfit      float64
    AvgLoss        float64
    TotalPnL       float64
    MaxDrawdownPct float64
    SharpeRatio    float64
    Trades         []BacktestTrade
}

type BacktestTrade struct {
    Timestamp      time.Time
    Symbol         string
    FundingRate    float64
    CompositeScore float64
    Confidence     string
    EntryPrice     float64
    ExitPrice      float64
    PnL            float64
    Result         string
    HoldDuration   time.Duration
    Breakdown      ScoreBreakdown
}

type Runner struct {
    cfg       BacktestConfig
    client    *exchange.BinanceClient
    scorer    Scorer
    risk      RiskEngine
    indicator IndicatorEngine
}

func (r *Runner) Run(ctx context.Context) (*BacktestResult, error) {
    // 1. Fetch historical data
    data, err := r.loadHistoricalData(ctx)
    if err != nil {
        return nil, err
    }

    // 2. Iterate funding windows
    balance := r.cfg.InitialBalance
    peakBalance := balance
    var maxDD float64
    var trades []BacktestTrade

    for _, window := range data.FundingWindows {
        // Reconstruct market state
        state := r.buildMarketState(data, window.Time)

        // Run scanner
        candidates := r.scanCandidates(state)
        if len(candidates) == 0 {
            continue
        }

        // Compute indicators + score
        btc := r.computeBTCState(state)
        var best *ScoredCandidate
        for _, c := range candidates {
            ind := r.computeIndicators(state, c.Symbol)
            scored := r.scorer.Score(c, ind, btc)
            if best == nil || scored.CompositeScore > best.CompositeScore {
                best = scored
            }
        }

        // Risk check
        if best.CompositeScore < r.cfg.RiskConfig.MinCompositeScore {
            continue
        }

        // Simulate trade
        entry := best.Candidate.MarkPrice
        posSize := balance * best.PositionSizePct / 100 * float64(r.cfg.Leverage)

        // Walk forward through candles to find exit
        exit, result, duration := r.simulateExit(data, window.Time, best.Candidate.Symbol, entry)

        pnl := (entry - exit) / entry * posSize // SHORT
        balance += pnl

        if balance > peakBalance {
            peakBalance = balance
        }
        dd := (peakBalance - balance) / peakBalance * 100
        if dd > maxDD {
            maxDD = dd
        }

        trades = append(trades, BacktestTrade{
            Timestamp:      window.Time,
            Symbol:         best.Candidate.Symbol,
            FundingRate:    best.Candidate.FundingRate,
            CompositeScore: best.CompositeScore,
            Confidence:     best.Confidence,
            EntryPrice:     entry,
            ExitPrice:      exit,
            PnL:            pnl,
            Result:         result,
            HoldDuration:   duration,
            Breakdown:      best.Breakdown,
        })
    }

    return r.buildReport(trades, balance, maxDD), nil
}
```

### 5.17 Configuration Additions (Phase 2)

```yaml
# Additions to config.yaml

filtering:
  min_daily_roi_pct: 15.0    # Hard filter: reject coins with 24h price change < 15%
  min_volume_24h_m: 50.0     # Hard filter: reject coins with 24h volume < 50M USDT

scoring:
  weights:
    funding: 25
    oi: 15
    btc: 10
    candle: 20
    volume: 10
    roi: 15
    volatility: 5
  min_score: 60              # Reject candidates below this composite score

indicators:
  rsi_period: 14             # RSI-14 on 15m (setup context)
  rsi_fast_period: 7         # RSI-7 on 5m (entry timing)
  atr_period: 14             # ATR-14 on 1h (volatility filter only)
  oi_window_1h: "1h"         # OI delta for setup confirmation
  oi_window_15m: "15m"       # OI delta for recent leverage buildup
  volume_avg_window: 24      # Candles for volume baseline (24 × 5m = 2h)
  volume_spike_threshold: 2.0 # Volume ratio to consider "spike"

risk:
  max_daily_losses: 2
  max_drawdown_pct: 10.0     # Disable trading if drawdown exceeds this
  max_atr_ratio: 6.0         # Reject if ATR/price > 6%
  btc_breakout_reject: true  # Skip trades during BTC breakout

backtest:
  data_dir: "./data/backtest" # Directory for cached historical data
```

---

## 6. Database Schema (Phase 2 Additions)

### 6.1 Indicator Snapshots Table

Stores the indicator state at the time of each trade for post-analysis and strategy tuning.

```sql
-- migrations/003_create_indicator_snapshots.up.sql

CREATE TABLE IF NOT EXISTS indicator_snapshots (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    trade_id         UUID NOT NULL REFERENCES trades(id) ON DELETE CASCADE,
    symbol           VARCHAR(32) NOT NULL,

    -- RSI: 15m for setup context, 5m for entry timing
    rsi_14_15m       NUMERIC(6,2),
    rsi_7_5m         NUMERIC(6,2),

    -- OI delta: 1h for setup confirmation, 15m for recent leverage buildup
    oi_delta_1h      NUMERIC(8,4),
    oi_delta_15m     NUMERIC(8,4),

    -- ATR on 1h: pre-trade volatility filter only
    atr_14_1h        NUMERIC(20,8),
    atr_ratio        NUMERIC(6,4),

    -- Volume anomaly on 5m: surge at entry time
    vol_change_5m    NUMERIC(8,4),
    volume_spike     BOOLEAN NOT NULL DEFAULT false,

    -- Candle patterns (JSON array of {timeframe, pattern, strength})
    candle_patterns  JSONB,

    -- BTC context: trend on 1h, breakout detection on 15m
    btc_trend        VARCHAR(16),
    btc_momentum     INT,
    btc_rsi          NUMERIC(6,2),
    btc_change_1h    NUMERIC(8,4),
    btc_change_15m   NUMERIC(8,4),
    btc_breakout     BOOLEAN NOT NULL DEFAULT false,

    -- Composite scoring
    composite_score  NUMERIC(6,2) NOT NULL,
    score_funding    NUMERIC(6,2),
    score_oi         NUMERIC(6,2),
    score_btc        NUMERIC(6,2),
    score_candle     NUMERIC(6,2),
    score_volume     NUMERIC(6,2),
    score_roi        NUMERIC(6,2),
    score_volatility NUMERIC(6,2),
    confidence       VARCHAR(16),
    position_size_pct NUMERIC(4,2),

    created_at       TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_indicator_snapshots_trade_id ON indicator_snapshots(trade_id);
CREATE INDEX idx_indicator_snapshots_symbol   ON indicator_snapshots(symbol);
CREATE INDEX idx_indicator_snapshots_score    ON indicator_snapshots(composite_score DESC);
```

```sql
-- migrations/003_create_indicator_snapshots.down.sql

DROP TABLE IF EXISTS indicator_snapshots;
```

### 6.2 Updated Redis Key Layout

```
# Phase 1 keys (unchanged):
positions:active          HASH    {symbol → JSON(Position)}
state:cooldown            STRING  "1" (with TTL)
state:kill_switch         STRING  "1" or "0"
lock:scan:{timestamp}     STRING  "1" (with TTL)
market:funding_snapshot   HASH    {symbol → rate}
meta:last_ws_message      STRING  Unix timestamp

# Phase 2 additions:
oi:history:{symbol}       LIST    JSON(OISnapshot) — last 288 entries (24h @ 5min)
state:peak_balance        STRING  float64 — for drawdown tracking
state:current_balance     STRING  float64 — updated after each trade
```

---

## 7. API Endpoints (Phase 2 Additions)

### Binance REST (new usage)

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/fapi/v1/klines` | GET | Historical candles for backtester |
| `/fapi/v1/fundingRate` | GET | Historical funding rates for backtester |
| `/futures/data/openInterestHist` | GET | Historical OI for backtester |

### Backtester CLI Usage

```bash
# Run backtest over last 30 days
go run ./cmd/backtest \
    --config config/config.yaml \
    --start 2026-04-26 \
    --end 2026-05-26 \
    --balance 1000

# Run backtest for specific symbols
go run ./cmd/backtest \
    --config config/config.yaml \
    --start 2026-04-26 \
    --end 2026-05-26 \
    --symbols "1000PEPEUSDT,DOGEUSDT,WIFUSDT"

# Output: prints summary to stdout + writes trades.csv to --output dir
```

---

## 8. Build Order (Implementation Sequence)

```
Week 1: Indicators
    [1] internal/domain/indicator.go         — New domain types
    [2] internal/domain/scored_candidate.go   — ScoredCandidate type
    [3] internal/indicator/rsi.go            — RSI implementation + tests
    [4] internal/indicator/atr.go            — ATR implementation + tests
    [5] internal/indicator/oi_delta.go       — OI delta with history ring buffer
    [6] internal/indicator/volume.go         — Volume anomaly detection
    [7] internal/indicator/candle_pattern.go — 6 bearish patterns + tests
    [8] internal/indicator/btc.go           — BTC context computation

Week 2: Scoring + Risk
    [9]  internal/indicator/indicator.go     — IndicatorEngine orchestrator
    [10] internal/scoring/weights.go         — Weight constants + confidence map
    [11] internal/scoring/result.go          — ScoreBreakdown type
    [12] internal/scoring/scorer.go          — Weighted composite scorer + tests
    [13] internal/risk/drawdown.go           — Drawdown tracker
    [14] internal/risk/volatility_guard.go   — ATR-based rejection
    [15] internal/risk/engine.go             — Enhanced risk engine

Week 3: Integration + Migration
    [16] internal/market/oi_cache.go         — Add OI history ring buffer
    [17] internal/scanner/funding_scanner.go — Wire indicators + scoring into pipeline
    [18] internal/execution/engine.go        — Accept ScoredCandidate, confidence sizing
    [19] internal/config/config.go           — Add scoring/indicator/risk config sections
    [20] migrations/003_*                    — Indicator snapshots table
    [21] internal/storage/indicator_repo.go  — Insert/query indicator snapshots

Week 4: Backtester
    [22] internal/backtest/types.go          — Config + result types
    [23] internal/backtest/data_loader.go    — Fetch historical data from Binance
    [24] internal/backtest/runner.go         — Replay engine
    [25] internal/backtest/report.go         — Metrics calculation + CSV output
    [26] cmd/backtest/main.go               — CLI entry point
    [27] Integration tests                   — End-to-end scoring pipeline validation
```

---

## 9. Testing Strategy

### Unit Tests

| Package | Test Focus |
|---------|-----------|
| `indicator/rsi` | Edge cases: all gains, all losses, exactly `period` candles, flat price |
| `indicator/atr` | Gaps, zero-range candles, trending vs ranging markets |
| `indicator/candle_pattern` | Each pattern in isolation, no-match cases, multi-pattern detection |
| `indicator/volume` | Zero volume, exactly 2x spike, < 2 candles |
| `scoring/scorer` | Perfect score (100), minimum pass (60), zero score, each weight contribution |
| `risk/drawdown` | Peak tracking, recovery, exact threshold |

### Integration Tests

| Test | Description |
|------|-------------|
| Full pipeline | Feed known market state → verify correct score + trade decision |
| Backtest regression | Run on fixed historical dataset → compare results against snapshot |
| Config variations | Verify different weight configs produce expected ranking changes |

### Test Data

Use recorded Binance data from known high-funding events (e.g., meme coin pumps with -1%+ funding) as golden test fixtures.

---

## 10. In-Memory Footprint and Retention Policy

### 10.1 Memory Estimates

Binance USDⓂ-M has ~350 active perpetuals. The mark price stream covers all of them; kline subscriptions are limited to the top-20 most-negative-funding symbols.

| Store | Scope | Per-entry size | Total estimate |
|-------|-------|---------------|----------------|
| `FundingCache` | All 350 symbols | ~145 B (3× float64 + 2× time.Time + int + map overhead + symbol key) | ~51 KB |
| `TickerCache` | All 350 symbols | ~65 B (float64 + map overhead + symbol key) | ~23 KB |
| `CandleStore` | Top-20 symbols × 6 TFs × 200 candles | ~130 B/candle (symbol, TF, 2× time.Time, 5× float64, bool + padding) | ~31 MB |
| `OIHistory` | Top-20 symbols × 288 snapshots | ~32 B/snapshot (float64 + time.Time) | ~184 KB |
| **Total (logical)** | | | **~32 MB** |

Go GC overhead and slice backing arrays add ~1.5–2× headroom in practice, so real RSS is approximately **50–70 MB** for these caches.

`CandleStore` dominates: 20 symbols × 6 timeframes × 200 candles × 130 bytes = 31 MB. The other stores are negligible by comparison.

### 10.2 Retention Policy

**FundingCache / TickerCache** — no TTL or cleanup. They are driven by the `!markPrice@arr@1s` WebSocket stream which covers every symbol on the exchange. Entries are updated in-place every second and stay accurate. Delisted symbols stop appearing in the stream and become stale, but at ~210 bytes per dead entry the cost is negligible.

**CandleStore** — capped at 200 candles per `{symbol, timeframe}` series (enforced in `CandleStore.Update`). When a symbol rotates out of the top-20 watchlist, `PurgeSymbol` is called from `updateKlineSubscriptions` to delete all 6 timeframe series for that symbol. This prevents unbounded growth as symbols cycle through the watchlist over time.

**OIHistory** — capped at 288 snapshots per symbol (ring buffer, enforced in `OIHistory.Add`). Same rotation cleanup: `PurgeSymbol` calls `OIHistory.Purge` when a symbol leaves the watchlist.

### 10.3 Purge Flow

```
klineSubscriber goroutine (every 60s)
    │
    ├── GetTopNegativeFundingSymbols(20) → newSymbols
    ├── diff old vs new
    │
    ├── removed symbols:
    │       wsKlines.Unsubscribe(streams)    — stop receiving kline events
    │       MarketEngine.PurgeSymbol(sym)    — free memory
    │           ├── CandleStore.Purge(sym)   — delete all 6 TF series
    │           └── OICache.Purge(sym)       — delete current OI + history ring buffer
    │
    └── added symbols:
            wsKlines.Subscribe(streams)      — start receiving kline events
            (data populates naturally as candles arrive)
```

Purge happens synchronously before the next scan cycle, so an indicator computation for the outgoing symbol cannot race against the purge — the symbol will not appear in the top-20 candidate list again until it re-enters, by which point fresh candles will have accumulated.

---

## 11. Key Design Decisions

### 1. No External TA Library

Custom Go implementations for RSI, ATR, and pattern detection. Avoids CGo (TA-Lib), keeps the binary static, and gives full control over edge cases like insufficient data.

### 2. OI History as In-Memory Ring Buffer

Not in PostgreSQL. OI snapshots are ephemeral (only need last 24h for delta calculations). Storing in memory avoids DB writes every 5 minutes × 20 symbols. On restart, the bot needs ~1h of polling to rebuild history — acceptable since the bot shouldn't trade in its first cycle anyway.

### 3. Indicators Computed On-Demand (Not Streaming)

Indicators are calculated only when a scan cycle runs (every 1–5 minutes during windows). This avoids continuous computation for 200+ symbols and keeps CPU usage minimal during idle periods.

### 4. Backtester Shares Production Code

The scorer, indicators, and risk engine are the same code used in live trading. The backtester only provides a different data source (historical REST vs. live WebSocket). This ensures backtest results correlate with live behavior.

### 5. Score Threshold Over LLM (Phase 2 Only)

In Phase 2 (before LLM integration in Phase 3), the composite score directly gates trade execution. A score >= 60 proceeds to execution. This provides a deterministic, debuggable strategy baseline before adding LLM judgment.

### 6. BTC as Universal Context (Not Per-Pair Correlation)

Phase 2 uses BTC market state as a universal context signal rather than computing per-pair correlation coefficients. This is simpler, faster, and captures the primary risk: strong BTC momentum invalidates alt funding shorts regardless of individual pair correlation strength.

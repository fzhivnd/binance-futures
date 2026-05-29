# Candle Pattern Detection — Improvement Design

## Problem

Currently, the pattern detection mostly triggers on the **1h timeframe**. Lower timeframes (30m, 15m, 5m) rarely produce signals.

## Root Cause Analysis

The thresholds in `DetectPatterns()` were designed with 1h-scale moves in mind. Lower timeframes have:
- Smaller body-to-wick ratios overall (more noise)
- More doji-like candles that don't pass thresholds but are just illiquid
- Less reliable "trend" context (checking `p.Close > pp.Close` for uptrend on 5m is flimsy)

Current candle fetch per timeframe (all get 5 candles):

| Timeframe | Candles Fetched | Issue |
|-----------|----------------|-------|
| **1h** | 5 | Fully formed candles, definitive body/wick ratios |
| **30m** | 5 | Less likely to show strong reversal patterns — noise is higher |
| **15m** | 5 | Even noisier — small moves look like shooting stars but aren't meaningful |
| **5m** | 5 | Very noisy — patterns form and invalidate rapidly |

---

## Improvement 1: ATR-Relative Filtering (Context-Aware)

**Impact: High | Effort: Low**

Patterns are detected purely on candle shape. A "shooting star" on 5m with a 0.02% range means nothing — it needs to be significant relative to recent volatility.

```go
func DetectPatterns(candles []domain.Candle, tf domain.Timeframe, atr float64) []domain.CandleSignal {
    // Reject if the candle's range is < 50% of ATR for that timeframe
    if candleRange < atr * 0.5 {
        return nil
    }
    // ...
}
```

ATR is already computed on 1h (14-period). Scale it for other timeframes:
- 30m ATR ≈ 1h ATR × 0.7
- 15m ATR ≈ 1h ATR × 0.5
- 5m ATR ≈ 1h ATR × 0.35

This ensures only *significant* candles on each timeframe generate signals.

---

## Improvement 2: Increase Candle Lookback for Lower TFs

**Impact: High | Effort: Low**

Currently only 3 candles are analyzed (`c`, `p`, `pp`). For lower timeframes, a "shooting star after uptrend" should confirm the uptrend over more candles:

```go
func isUptrend(candles []domain.Candle, lookback int) bool {
    if len(candles) < lookback {
        return false
    }
    start := candles[len(candles)-lookback]
    end := candles[len(candles)-2] // previous candle
    return end.Close > start.Close &&
           countGreenCandles(candles[len(candles)-lookback:]) > lookback/2
}
```

Recommended lookback per timeframe:

| Timeframe | Trend Lookback | Rationale | Fetch Count |
|-----------|---------------|-----------|-------------|
| 1h | 3–5 candles | 3–5 hours of trend | 10 |
| 30m | 5–6 candles | ~3 hours | 12 |
| 15m | 8–10 candles | ~2.5 hours | 15 |
| 5m | 12–15 candles | ~1 hour | 20 |

---

## Improvement 3: Timeframe-Adaptive Thresholds

**Impact: Medium | Effort: Medium**

Instead of one-size-fits-all thresholds, scale them by timeframe:

```go
type PatternConfig struct {
    WickBodyRatio    float64 // Shooting star: upperWick >= N * bodySize
    EngulfMinBody    float64 // Min body % of range to count as engulfing
    WickRejectRatio  float64 // Upper wick % of range
    EveningStarRatio float64 // Middle candle max body % vs avg
    DojiThreshold    float64 // Max body/range for doji
    MinCandleRange   float64 // Min range in % to filter noise
}

var tfConfigs = map[domain.Timeframe]PatternConfig{
    domain.Timeframe1h:  {WickBodyRatio: 2.0, WickRejectRatio: 0.6, MinCandleRange: 0.001},
    domain.Timeframe30m: {WickBodyRatio: 2.2, WickRejectRatio: 0.55, MinCandleRange: 0.0015},
    domain.Timeframe15m: {WickBodyRatio: 2.5, WickRejectRatio: 0.5, MinCandleRange: 0.002},
    domain.Timeframe5m:  {WickBodyRatio: 3.0, WickRejectRatio: 0.45, MinCandleRange: 0.003},
}
```

Lower timeframes need *stricter* wick/body ratios but *looser* rejection ratios because candles are smaller. The `MinCandleRange` filter eliminates noise candles with tiny absolute moves.

---

## Improvement 4: Volume-Weighted Strength

**Impact: Medium | Effort: Low**

Currently strength is static (STRONG/MEDIUM/WEAK hardcoded per pattern). Make it dynamic based on volume:

```go
func adjustStrength(base domain.Strength, volumeRatio float64) domain.Strength {
    // volumeRatio = current candle volume / avg volume (last 20 candles)
    if volumeRatio > 2.0 && base == domain.StrengthMedium {
        return domain.StrengthStrong  // High volume upgrades pattern
    }
    if volumeRatio < 0.5 && base == domain.StrengthStrong {
        return domain.StrengthMedium  // Low volume downgrades pattern
    }
    return base
}
```

A shooting star on 3x average volume is *much* more significant than one on 0.3x volume.

---

## Improvement 5: Top-Down Confirmation Cascade

**Impact: High | Effort: Medium**

Instead of treating each timeframe independently, use a hierarchical approach:

```
4h pattern detected? → Check if 1h confirms → Check if 15m shows entry timing
1h pattern detected? → Check if 15m/5m shows entry timing
Lower TF pattern alone → Only if volume spike confirms
```

Implementation:

```go
type ConfirmedPattern struct {
    Primary       domain.CandleSignal   // Highest TF where detected
    Confirmations []domain.CandleSignal // Lower TF signals that align
    Confidence    float64               // Based on # of confirmations
}

func ConfirmPatterns(allPatterns []domain.CandleSignal) []ConfirmedPattern {
    // Group by pattern type
    // If same pattern type on 1h + 15m → high confidence
    // If only on 5m with no higher TF → low confidence
}
```

This replaces the current flat "multi-timeframe bonus" scoring with actual directional confluence logic.

---

## Improvement 6: Add Bullish Patterns

**Impact: High (if going long) | Effort: Medium**

The current system only detects **bearish reversal** patterns. Mirror patterns for long entries:

- **Hammer / Inverted Hammer** (mirror of shooting star)
- **Bullish Engulfing** (mirror of bearish engulfing)
- **Morning Star** (mirror of evening star)
- **Doji After Dump** (mirror of doji after pump)
- **Failed Breakdown** (mirror of failed breakout)

This would double detection rate across all timeframes.

---

## Priority Ranking

| # | Improvement | Impact | Effort | Recommendation |
|---|-------------|--------|--------|----------------|
| 1 | ATR-relative filtering | High | Low | **Do first** |
| 2 | Increase candle lookback | High | Low | **Do first** |
| 3 | Timeframe-adaptive thresholds | Medium | Medium | Phase 2 |
| 4 | Volume-weighted strength | Medium | Low | Phase 2 |
| 5 | Top-down confirmation cascade | High | Medium | Phase 3 |
| 6 | Add bullish patterns | High | Medium | When long positions enabled |

---

## Affected Files

- `internal/indicator/candle_pattern.go` — main detection logic
- `internal/indicator/indicator.go` — orchestration, candle fetch counts
- `internal/domain/indicator.go` — CandleSignal struct (if adding Confidence)
- `internal/scoring/scorer.go` — scoring weights if cascade replaces flat bonus
- `internal/market/candle_store.go` — may need more candles stored per series

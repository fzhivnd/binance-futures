# RSI Divergence — Design & Change Plan

## What is RSI Divergence (for this bot)

Bearish RSI divergence: price makes a **higher high** but RSI makes a **lower high** over the same window.
This signals weakening momentum at the top — the most useful pre-short confirmation for funding-rate setups.

Only bearish divergence is implemented (the bot only goes short).

---

## Detection Algorithm

```
Input: N closed candles (price highs), RSI series computed over same candles

1. Compute RSI series for the full candle slice using Wilder smoothing.
2. Find swing highs in price: candle[i].High > candle[i-1].High && candle[i].High > candle[i+1].High
3. Find the two most recent swing highs (A = older, B = newer).
4. If B.price > A.price  AND  RSI[B] < RSI[A]  →  bearish divergence confirmed.
5. Classify strength:
   - STRONG: price diff ≥ StrongPriceDiffPct AND rsi diff ≥ StrongRSIDiffPts
   - MEDIUM: either threshold met but not both
   - WEAK: divergence confirmed but below both thresholds
```

Configurable thresholds (see Scoring Config section below):
- `Lookback` — candle window to search for swing highs (default 20)
- `MinSwingDistance` — minimum candles between two swing highs (default 3)
- `StrongPriceDiffPct` — price diff between highs to qualify as STRONG (default 0.5%)
- `StrongRSIDiffPts` — RSI diff between highs to qualify as STRONG (default 5.0)

Run on: **1h** and **15m** timeframes (the two most signal-relevant for this strategy).
5m is too noisy; 30m can be added later.

---

## Data Structures

### `domain/indicator.go` — add to `IndicatorSnapshot`

```go
type RSIDivergence struct {
    Timeframe Timeframe
    Strength  PatternStrength // STRONG | MEDIUM | WEAK
}

// In IndicatorSnapshot:
RSIDivergences []RSIDivergence
```

---

## File Changes

### 1. `internal/domain/indicator.go`
Add `RSIDivergence` struct and `RSIDivergences []RSIDivergence` field to `IndicatorSnapshot`.

---

### 2. `internal/indicator/rsi_divergence.go` (new file)

```go
// DetectRSIDivergence returns a RSIDivergence if bearish divergence is found, nil otherwise.
// candles must be closed candles sorted oldest→newest.
func DetectRSIDivergence(candles []domain.Candle, tf domain.Timeframe, cfg RSIDivergenceConfig) *domain.RSIDivergence
```

Internally:
- Calls the existing `RSI()` function over the full candle slice (period 14 for 1h, period 14 for 15m)
- Finds swing highs in `candle[i].High`
- Compares the two most recent swing highs
- Returns nil if fewer than 2 swing highs found within the lookback window

`RSIDivergenceConfig` struct (used for per-TF thresholds, sourced from scoring config at runtime):
```go
type RSIDivergenceConfig struct {
    Lookback          int
    MinSwingDistance  int
    StrongPriceDiffPct float64
    StrongRSIDiffPts   float64
}
```

---

### 3. `internal/indicator/indicator.go`
In `Compute()`, after the existing RSI blocks, add:

```go
// RSI divergence on 1h and 15m
for _, tf := range []domain.Timeframe{domain.Timeframe1h, domain.Timeframe15m} {
    candles := e.market.GetCandles(symbol, tf, 42) // 40 closed + 1 forming + buffer
    closed := candles[:len(candles)-1]
    cfg := rsidiv.ConfigForTF(tf) // pulls from scoring config or default
    if div := rsidiv.DetectRSIDivergence(closed, tf, cfg); div != nil {
        snap.RSIDivergences = append(snap.RSIDivergences, *div)
    }
}
```

---

### 4. `internal/llm/schema.go`
Add to `LLMCandidate`:

```go
RSIDivergences []LLMRSIDivergence `json:"rsi_divergences,omitempty"`
```

New type:
```go
type LLMRSIDivergence struct {
    Timeframe string `json:"timeframe"`
    Strength  string `json:"strength"`
}
```

---

### 5. `internal/llm/mapper.go`
In the `for _, sc := range candidates` loop, after `CandlePatterns`:

```go
for _, div := range sc.Indicators.RSIDivergences {
    c.RSIDivergences = append(c.RSIDivergences, LLMRSIDivergence{
        Timeframe: string(div.Timeframe),
        Strength:  string(div.Strength),
    })
}
```

The LLM already receives `rsi_14_15m` and `rsi_7_5m` values. The divergence field gives it the higher-order interpretation: "these two RSI readings are diverging from price."

---

### 6. `internal/memory/feature_builder.go`
In `BuildFeatureText()`, after the `candle_patterns` block:

```go
if len(snap.RSIDivergences) > 0 {
    sb.WriteString("rsi_divergences: ")
    for _, d := range snap.RSIDivergences {
        sb.WriteString(fmt.Sprintf("%s:%s ", d.Timeframe, d.Strength))
    }
    sb.WriteString("\n")
}
```

This ensures the embedding vector for a trade that had a divergence signal is semantically distinct from one that didn't — memory retrieval will surface relevant past trades.

---

## Scoring Config Changes

RSI divergence contributes a **new scoring component** with its own weight and threshold tiers, consistent with how candle patterns are scored.

### New DB Tables / Columns

No new tables needed. The existing `scoring_weights` and `scoring_thresholds` tables handle it via a new `category = "rsi_divergence"`.

**`scoring_weights`** — add one row per config:
```
category: "rsi_divergence"
max_points: <configurable, suggested default: 10>
```

**`scoring_thresholds`** — add tiers per config:
```
category: "rsi_divergence"
tier_order: 1  condition: "strong_multitf"   multiplier: 1.00
tier_order: 2  condition: "strong"           multiplier: 0.80
tier_order: 3  condition: "medium_multitf"   multiplier: 0.60
tier_order: 4  condition: "medium"           multiplier: 0.40
tier_order: 5  condition: "weak"             multiplier: 0.20
tier_order: 6  condition: "none"             multiplier: 0.00
```

Condition resolution logic (strongest signal wins):
- `strong_multitf`: STRONG divergence on both 1h and 15m
- `strong`: STRONG divergence on either timeframe
- `medium_multitf`: MEDIUM on both
- `medium`: MEDIUM on either
- `weak`: WEAK on any
- `none`: no divergence detected

Divergence thresholds (`StrongPriceDiffPct`, `StrongRSIDiffPts`, `Lookback`, `MinSwingDistance`) are stored as new rows in `scoring_thresholds` using a `condition` field to identify them, or alternatively as a new `rsi_divergence_params` table. The simpler approach: store them as four special tiers with fixed `tier_order` values and the config value in `min_value`.

### `domain/scoring_config.go` — add

```go
type RSIDivergenceParams struct {
    Lookback           int
    MinSwingDistance   int
    StrongPriceDiffPct float64
    StrongRSIDiffPts   float64
}

// In ScoringConfig:
RSIDivergenceParams RSIDivergenceParams
```

### `scoring/scorer.go` — add

```go
// 8. RSI Divergence
bd.RSIDivergenceScore = evalRSIDivergence(cfg, ind.RSIDivergences) * cfg.Weights.RSIDivergence
```

New `evalRSIDivergence()` function resolves the condition string from the signals and looks up the multiplier from thresholds — same pattern as `evalBTC()`.

### `domain/scored_candidate.go` — add `RSIDivergenceScore` to `ScoreBreakdown`

### `internal/llm/schema.go` — add `RSIDivergence float64` to `LLMBreakdown`

### `internal/llm/mapper.go` — map `sc.Breakdown.RSIDivergenceScore` to `LLMBreakdown`

---

## Dashboard (scoring config editor)

The dashboard already handles arbitrary categories in `scoring_weights` and `scoring_thresholds` — no new API endpoints or handler changes required.

The `GetConfig` / `UpdateWeights` / `UpdateThresholds` handlers are category-agnostic; adding the new rows through the dashboard edit flow will automatically persist and display them.

The only dashboard-visible change: the weight editor will show a new `rsi_divergence` row, and the threshold editor will show its six condition tiers. Labels and descriptions in the UI are driven by the `category` + `condition` strings, so no frontend code changes are needed unless you want custom display names.

---

## Summary of Files Changed

| File | Change |
|------|--------|
| `internal/domain/indicator.go` | Add `RSIDivergence` struct + `RSIDivergences` field |
| `internal/domain/scoring_config.go` | Add `RSIDivergenceParams` struct + field on `ScoringConfig` |
| `internal/domain/scored_candidate.go` | Add `RSIDivergenceScore` to `ScoreBreakdown` |
| `internal/indicator/rsi_divergence.go` | New file — detection logic |
| `internal/indicator/indicator.go` | Call divergence detector, store on snap |
| `internal/scoring/scorer.go` | Add `evalRSIDivergence()`, wire into `Score()` |
| `internal/llm/schema.go` | Add `RSIDivergences` to `LLMCandidate`, `RSIDivergence` to `LLMBreakdown` |
| `internal/llm/mapper.go` | Map divergences + breakdown score |
| `internal/memory/feature_builder.go` | Append divergence lines to feature text |
| `internal/storage/scoring_config_repo.go` | Load/save `RSIDivergenceParams` |
| DB (migration) | `scoring_weights` + `scoring_thresholds` get new rows; `scoring_config_audit` unchanged |

No changes needed to: dashboard handlers, LLM prompt template (the JSON fields are self-describing), position manager, execution engine, or any WebSocket/exchange layer.

---

## Default Config Values

```
Weights.RSIDivergence: 10
RSIDivergenceParams:
  Lookback:            20
  MinSwingDistance:    3
  StrongPriceDiffPct:  0.5
  StrongRSIDiffPts:    5.0
Thresholds (rsi_divergence):
  strong_multitf  → 1.00
  strong          → 0.80
  medium_multitf  → 0.60
  medium          → 0.40
  weak            → 0.20
  none            → 0.00
```

Adding 10 points max to the composite score keeps the existing confidence tier boundaries meaningful without requiring recalibration (a MEDIUM confidence trade at 70 still needs 70 points from the other seven components; a divergence can push a borderline setup over the threshold).

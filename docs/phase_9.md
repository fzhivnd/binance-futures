# Phase 9: Configurable Scoring Weights via Database

Make all scoring weights, thresholds, and confidence tiers configurable and stored in the database, with versioned profiles and full audit history.

---

## Motivation

Currently all scoring parameters are hardcoded in `internal/scoring/scorer.go` and `internal/scoring/weights.go`. Phase 9 moves these into the database so they can be tuned without redeploying, rolled back instantly, and compared across named profiles.

---

## Schema

### `scoring_configs` — named versioned profiles

```sql
CREATE TABLE scoring_configs (
    id          SERIAL PRIMARY KEY,
    name        TEXT NOT NULL,
    version     INT NOT NULL DEFAULT 1,
    is_active   BOOLEAN NOT NULL DEFAULT false,
    notes       TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (name, version)
);

-- Only one active config at a time
CREATE UNIQUE INDEX ON scoring_configs (is_active) WHERE is_active = true;
```

### `scoring_weights` — max points per category

```sql
CREATE TABLE scoring_weights (
    config_id   INT NOT NULL REFERENCES scoring_configs(id),
    category    TEXT NOT NULL,   -- 'funding','oi','btc','candle','volume','roi','volatility'
    max_points  FLOAT NOT NULL,
    PRIMARY KEY (config_id, category)
);
```

### `scoring_thresholds` — tier rules per category

```sql
CREATE TABLE scoring_thresholds (
    config_id   INT NOT NULL REFERENCES scoring_configs(id),
    category    TEXT NOT NULL,
    tier_order  INT NOT NULL,    -- lower = checked first
    min_value   FLOAT,           -- NULL = no lower bound
    max_value   FLOAT,           -- NULL = no upper bound
    multiplier  FLOAT NOT NULL,  -- fraction of max_points, e.g. 0.75
    interpolate BOOLEAN NOT NULL DEFAULT false,  -- true = linear ramp between bands (used by funding)
    condition   TEXT,            -- for BTC tiers: 'breakout_bullish','bullish_high_momentum','bullish','neutral','bearish'
    PRIMARY KEY (config_id, category, tier_order)
);
```

### `candle_weights` — timeframe, strength, multi-TF bonus

```sql
CREATE TABLE candle_weights (
    config_id   INT NOT NULL REFERENCES scoring_configs(id),
    type        TEXT NOT NULL,   -- 'timeframe', 'strength', 'multitf_bonus'
    label       TEXT NOT NULL,   -- '1h','30m','15m','5m' | 'STRONG','MEDIUM','WEAK' | '2','3','4'
    weight      FLOAT NOT NULL,
    PRIMARY KEY (config_id, type, label)
);
```

### `confidence_tiers` — composite score → confidence → position size

```sql
CREATE TABLE confidence_tiers (
    config_id           INT NOT NULL REFERENCES scoring_configs(id),
    min_score           FLOAT NOT NULL,
    confidence          TEXT NOT NULL,   -- 'VERY_HIGH','HIGH','MEDIUM','LOW','SKIP'
    position_size_pct   FLOAT NOT NULL,
    PRIMARY KEY (config_id, min_score)
);
```

### `scoring_config_audit` — change history

```sql
CREATE TABLE scoring_config_audit (
    id          SERIAL PRIMARY KEY,
    config_id   INT NOT NULL REFERENCES scoring_configs(id),
    changed_by  TEXT,
    changed_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    change_note TEXT
);
```

---

## Example Data

### `scoring_configs`

| id | name | version | is_active | notes | created_at |
|---|---|---|---|---|---|
| 1 | default | 1 | true | Initial production weights | 2026-05-29 00:00:00+00 |
| 2 | aggressive | 1 | false | Higher funding weight, looser ROI | 2026-05-29 01:00:00+00 |

### `scoring_weights`

| config_id | category | max_points |
|---|---|---|
| 1 | funding | 25 |
| 1 | oi | 15 |
| 1 | btc | 10 |
| 1 | candle | 20 |
| 1 | volume | 10 |
| 1 | roi | 15 |
| 1 | volatility | 5 |
| 2 | funding | 30 |
| 2 | oi | 15 |
| 2 | btc | 10 |
| 2 | candle | 15 |
| 2 | volume | 10 |
| 2 | roi | 15 |
| 2 | volatility | 5 |

### `scoring_thresholds`

| config_id | category | tier_order | min_value | max_value | multiplier | interpolate | condition |
|---|---|---|---|---|---|---|---|
| 1 | oi | 1 | 15 | NULL | 1.00 | false | NULL |
| 1 | oi | 2 | 10 | 15 | 0.75 | false | NULL |
| 1 | oi | 3 | 5 | 10 | 0.50 | false | NULL |
| 1 | oi | 4 | 0 | 5 | 0.30 | false | NULL |
| 1 | oi | 5 | NULL | 0 | 0.13 | false | NULL |
| 1 | roi | 1 | 15 | 50 | 1.00 | false | NULL |
| 1 | roi | 2 | 50 | 80 | 0.47 | false | NULL |
| 1 | roi | 3 | NULL | NULL | 0.00 | false | NULL |
| 1 | volatility | 1 | 1 | 3 | 1.00 | false | NULL |
| 1 | volatility | 2 | 3 | 5 | 0.60 | false | NULL |
| 1 | volatility | 3 | NULL | NULL | 0.00 | false | NULL |
| 1 | volume | 1 | 65 | NULL | 1.00 | false | NULL |
| 1 | volume | 2 | NULL | 65 | 0.50 | false | NULL |
| 1 | volume | 3 | NULL | NULL | 0.30 | false | NULL |
| 1 | funding | 1 | -0.002 | NULL | 0.00 | false | NULL |
| 1 | funding | 2 | -0.005 | -0.002 | 0.67 | true | NULL |
| 1 | funding | 3 | -0.010 | -0.005 | 0.89 | true | NULL |
| 1 | funding | 4 | -0.020 | -0.010 | 1.00 | true | NULL |
| 1 | funding | 5 | NULL | -0.020 | 0.00 | false | NULL |
| 1 | btc | 1 | NULL | NULL | 0.00 | false | breakout_bullish |
| 1 | btc | 2 | 70 | NULL | 0.20 | false | bullish_high_momentum |
| 1 | btc | 3 | NULL | NULL | 0.30 | false | bullish |
| 1 | btc | 4 | NULL | NULL | 0.70 | false | neutral |
| 1 | btc | 5 | NULL | NULL | 1.00 | false | bearish |

### `candle_weights`

| config_id | type | label | weight |
|---|---|---|---|
| 1 | timeframe | 1h | 1.00 |
| 1 | timeframe | 30m | 0.95 |
| 1 | timeframe | 15m | 0.90 |
| 1 | timeframe | 5m | 0.85 |
| 1 | strength | STRONG | 1.00 |
| 1 | strength | MEDIUM | 0.70 |
| 1 | strength | WEAK | 0.35 |
| 1 | multitf_bonus | 4 | 0.40 |
| 1 | multitf_bonus | 3 | 0.25 |
| 1 | multitf_bonus | 2 | 0.10 |
| 1 | multitf_bonus | 1 | 0.00 |

### `confidence_tiers`

| config_id | min_score | confidence | position_size_pct |
|---|---|---|---|
| 1 | 90 | VERY_HIGH | 5.5 |
| 1 | 80 | HIGH | 5.0 |
| 1 | 70 | MEDIUM | 4.0 |
| 1 | 60 | LOW | 2.0 |
| 1 | 0 | SKIP | 0.0 |

### `scoring_config_audit`

| id | config_id | changed_by | changed_at | change_note |
|---|---|---|---|---|
| 1 | 1 | fazha | 2026-05-29 00:00:00+00 | Initial config seeded from DefaultWeights |
| 2 | 2 | fazha | 2026-05-29 01:00:00+00 | Testing aggressive funding weight (25→30), reduced candle (20→15) |

---

## Design Notes

- **Single active config** enforced via partial unique index on `is_active = true`
- **Versioning**: bump `version` and set `is_active = true` to roll forward; flip back to roll back — no destructive updates
- **`interpolate` flag** on `scoring_thresholds`: preserves the linear ramp behavior currently used by the funding scorer between bands, rather than forcing flat tier multipliers
- **`condition` column** on `scoring_thresholds`: handles BTC tiers which are driven by `trend` + `momentum_score` combined, not a single numeric range — evaluated as named conditions in code
- **`candle_weights` `type` column** covers timeframe weights, strength weights, and multi-TF bonuses in one table
- The app loads the active config at startup and can hot-reload on change; falls back to `DefaultWeights` if no active config exists

---

## Implementation Plan

### Step 1 — Migration (`migrations/007_create_scoring_configs.up/down.sql`)
Create all five tables with `ON DELETE CASCADE` on FK references. Seed the `default` config using CTEs to capture the inserted `id` and populate all child tables with current `DefaultWeights` values. Down migration drops all five tables.

### Step 2 — Domain (`internal/domain/scoring_config.go`)
Add structs:
```go
type ScoringConfig struct {
    ID              int
    Name            string
    Version         int
    Weights         ScoringWeights
    Thresholds      []ScoringThreshold
    CandleWeights   []CandleWeight
    ConfidenceTiers []ConfidenceTier
}
type ScoringWeights     struct { Funding, OI, BTC, Candle, Volume, ROI, Volatility float64 }
type ScoringThreshold   struct { Category string; TierOrder int; MinValue, MaxValue *float64; Multiplier float64; Interpolate bool; Condition string }
type CandleWeight       struct { Type, Label string; Weight float64 }
type ConfidenceTier     struct { MinScore float64; Confidence string; PositionSizePct float64 }
```

### Step 3 — Storage (`internal/storage/scoring_config_repo.go`)
`PGScoringConfigRepo` with one method `LoadActive(ctx) (*domain.ScoringConfig, error)`. Runs four queries in sequence: load `scoring_configs WHERE is_active=true`, then join-load weights/thresholds/candle_weights/confidence_tiers by `config_id`. Returns `nil, nil` if no active config (triggers fallback in app).

### Step 4 — Scoring refactor (`internal/scoring/scorer.go`, `weights.go`)
- `Scorer` gains `mu sync.RWMutex` and `cfg *domain.ScoringConfig`
- `UpdateConfig(cfg *domain.ScoringConfig)` swaps under write lock
- `Score()` acquires read lock; evaluates thresholds via filtered slice loop, candle weights via maps built from `cfg.CandleWeights`, confidence via `cfg.ConfidenceTiers` sorted desc
- BTC tiers: derive condition string from `btc.IsBreakout`, `btc.Trend`, `btc.MomentumScore`; match against `ScoringThreshold.Condition`
- `weights.go`: add `DefaultScoringConfig() *domain.ScoringConfig` returning current hardcoded values; `NewDefaultScorer()` uses it

### Step 5 — App wiring (`internal/app/app.go`)
At startup in `Run()`:
```go
scoringRepo := storage.NewPGScoringConfigRepo(pool)
scoringCfg, err := scoringRepo.LoadActive(ctx)
if err != nil || scoringCfg == nil {
    slog.Error("no active scoring config in DB — cannot start", "error", err)
    return fmt.Errorf("scoring config required: %w", err)
}
a.scorer = scoring.NewScorerFromConfig(scoringCfg)
slog.Info("scoring config loaded", "config_id", scoringCfg.ID, "name", scoringCfg.Name)

go func() {
    ticker := time.NewTicker(2 * time.Hour)
    defer ticker.Stop()
    for {
        select {
        case <-ctx.Done(): return
        case <-ticker.C:
            cfg, err := scoringRepo.LoadActive(ctx)
            if err != nil || cfg == nil { slog.Warn("scoring config reload failed", "error", err); continue }
            a.scorer.UpdateConfig(cfg)
            slog.Info("scoring config reloaded", "config_id", cfg.ID)
        }
    }
}()
```

`NewScorerFromConfig(cfg *domain.ScoringConfig) *Scorer` — constructs a `Scorer` with the loaded config pre-set. Also applies `cfg.BotParams` to replace the relevant `cfg.Filtering`, `cfg.Trading`, and `cfg.Funding` fields used by scanner, risk engine, and executor.

### Step 6 — Dashboard (`internal/dashboard/scoring_queries.go` + `handler.go`)
New file `scoring_queries.go` with:
- `ListScoringConfigs(ctx)` — `SELECT id, name, version, is_active, notes, created_at FROM scoring_configs ORDER BY created_at DESC`
- `GetActiveScoringConfig(ctx)` — full config with all child rows joined
- `GetScoringConfig(ctx, id)` — full config by id (for editing)
- `CreateScoringConfig(ctx, name, notes string)` — inserts new config copying weights/thresholds/candle_weights/confidence_tiers from current active config; version = `MAX(version)+1` for same name, or 1 for a new name; returns new `id`
- `UpdateScoringWeights(ctx, id int, weights)` — upsert into `scoring_weights`; only allowed if config is **inactive**
- `UpdateScoringThresholds(ctx, id int, thresholds)` — replace all rows for `config_id` in `scoring_thresholds`; only if inactive
- `UpdateCandleWeights(ctx, id int, weights)` — replace all rows for `config_id` in `candle_weights`; only if inactive
- `UpdateConfidenceTiers(ctx, id int, tiers)` — replace all rows for `config_id` in `confidence_tiers`; only if inactive
- `ActivateScoringConfig(ctx, id int, changedBy string)` — wraps in a transaction: sets all `is_active=false`, sets target active, inserts audit row
- `DeleteScoringConfig(ctx, id int)` — deletes if **inactive**; returns error if trying to delete the active config

Register in `handler.go`:
```
GET    /api/scoring/configs                      → list all
GET    /api/scoring/configs/active               → full active config
GET    /api/scoring/configs/{id}                 → full config by id
POST   /api/scoring/configs                      → create/clone
PUT    /api/scoring/configs/{id}/weights         → update weights (inactive only)
PUT    /api/scoring/configs/{id}/thresholds      → update thresholds (inactive only)
PUT    /api/scoring/configs/{id}/candle-weights  → update candle weights (inactive only)
PUT    /api/scoring/configs/{id}/confidence-tiers → update confidence tiers (inactive only)
PUT    /api/scoring/configs/{id}/activate        → activate by id
DELETE /api/scoring/configs/{id}                 → delete inactive config
```

**Edit guard:** All `PUT` update endpoints check `is_active = false` before writing. If the config is active, return `HTTP 409 Conflict` with message `"cannot edit active config — clone it first"`. This enforces the clone → edit → activate workflow.


### Step 7 (revised) — Expand configurable params

The following fields from `config.yaml` are also moved into the DB config, stored in a new `bot_params` table on the same `config_id`:

**Filtering** (`FilteringConfig`):
- `min_daily_roi_pct` — minimum 24h ROI % to pass candidate filter
- `min_volume_24h_m` — minimum 24h volume in millions

**Trading** (`TradingConfig`):
- `max_positions` — max concurrent open positions
- `leverage` — position leverage (e.g. 20)
- `position_size_pct` — default position size as % of balance

**Funding** (`FundingConfig`):
- `min_rate` — minimum funding rate to consider (e.g. -0.02)
- `max_rate` — maximum funding rate to consider (e.g. -0.002)

**Execution** (`ExecutionConfig`):
- `sl_pct` — stop-loss distance % (e.g. 5.0)
- `tp_pct` — take-profit distance % (e.g. 2.1)
- `trailing_activation_pct` — profit % at which trailing stop activates (e.g. 1.5)
- `breakeven_activation_pct` — profit % at which SL moves to breakeven (e.g. 1.5)

```sql
CREATE TABLE bot_params (
    config_id                  INT NOT NULL REFERENCES scoring_configs(id) ON DELETE CASCADE,
    -- filtering
    min_daily_roi_pct          FLOAT NOT NULL,
    min_volume_24h_m           FLOAT NOT NULL,
    -- trading
    max_positions              INT NOT NULL,
    leverage                   INT NOT NULL,
    position_size_pct          FLOAT NOT NULL,
    -- funding
    funding_min_rate           FLOAT NOT NULL,
    funding_max_rate           FLOAT NOT NULL,
    -- execution
    sl_pct                     FLOAT NOT NULL,
    tp_pct                     FLOAT NOT NULL,
    trailing_activation_pct    FLOAT NOT NULL,
    breakeven_activation_pct   FLOAT NOT NULL,
    PRIMARY KEY (config_id)
);
```

Seed row for `default` config uses current `config.yaml` values:
- `min_daily_roi_pct=5`, `min_volume_24h_m=50`, `max_positions=3`, `leverage=20`, `position_size_pct=5.0`, `funding_min_rate=-0.02`, `funding_max_rate=-0.002`, `sl_pct=5.0`, `tp_pct=2.1`, `trailing_activation_pct=1.5`, `breakeven_activation_pct=1.5`

`domain.ScoringConfig` gains a `BotParams` field:
```go
type BotParams struct {
    MinDailyROIPct         float64
    MinVolume24hM          float64
    MaxPositions           int
    Leverage               int
    PositionSizePct        float64
    FundingMinRate         float64
    FundingMaxRate         float64
    SlPct                  float64
    TpPct                  float64
    TrailingActivationPct  float64
    BreakevenActivationPct float64
}
```

`LoadActive` joins `bot_params` in the same query. App uses `scoringCfg.BotParams` instead of `cfg.Filtering`, `cfg.Trading`, and `cfg.Funding` fields at runtime. Dashboard adds `PUT /api/scoring/configs/{id}/params` endpoint.

### Cache refresh frequency
**2-hour poll** — config changes are infrequent and deliberate. Bot reloads scorer + bot params every 2 hours; in-flight positions are not affected by a reload.

### Fallback & safety rules
- **No active config in DB at startup → app terminates with `log.Fatal`**. No local fallback. The DB is the single source of truth; running with unknown params is worse than not running.
- DB miss on reload tick → keep existing config unchanged, log `WARN`, retry next tick
- Activating a config with missing child rows → `ActivateScoringConfig` validates all required tables have rows before committing; returns `HTTP 422` if incomplete
- Editing active config → blocked (`HTTP 409 "cannot edit active config — clone it first"`)
- Deleting active config → blocked (`HTTP 409`)
- `bot_params` is required for activation validation — a config without a `bot_params` row cannot be activated

CREATE TABLE IF NOT EXISTS scoring_configs (
    id          SERIAL PRIMARY KEY,
    name        TEXT NOT NULL,
    version     INT NOT NULL DEFAULT 1,
    is_active   BOOLEAN NOT NULL DEFAULT false,
    notes       TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (name, version)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_scoring_configs_one_active
    ON scoring_configs (is_active) WHERE is_active = true;

CREATE TABLE IF NOT EXISTS scoring_weights (
    config_id   INT NOT NULL REFERENCES scoring_configs(id) ON DELETE CASCADE,
    category    TEXT NOT NULL,
    max_points  FLOAT NOT NULL,
    PRIMARY KEY (config_id, category)
);

CREATE TABLE IF NOT EXISTS scoring_thresholds (
    config_id   INT NOT NULL REFERENCES scoring_configs(id) ON DELETE CASCADE,
    category    TEXT NOT NULL,
    tier_order  INT NOT NULL,
    min_value   FLOAT,
    max_value   FLOAT,
    multiplier  FLOAT NOT NULL,
    interpolate BOOLEAN NOT NULL DEFAULT false,
    condition   TEXT,
    PRIMARY KEY (config_id, category, tier_order)
);

CREATE TABLE IF NOT EXISTS candle_weights (
    config_id   INT NOT NULL REFERENCES scoring_configs(id) ON DELETE CASCADE,
    type        TEXT NOT NULL,
    label       TEXT NOT NULL,
    weight      FLOAT NOT NULL,
    PRIMARY KEY (config_id, type, label)
);

CREATE TABLE IF NOT EXISTS confidence_tiers (
    config_id           INT NOT NULL REFERENCES scoring_configs(id) ON DELETE CASCADE,
    min_score           FLOAT NOT NULL,
    confidence          TEXT NOT NULL,
    position_size_pct   FLOAT NOT NULL,
    PRIMARY KEY (config_id, min_score)
);

CREATE TABLE IF NOT EXISTS bot_params (
    config_id                   INT NOT NULL REFERENCES scoring_configs(id) ON DELETE CASCADE,
    min_daily_roi_pct           FLOAT NOT NULL,
    min_volume_24h_m            FLOAT NOT NULL,
    max_positions               INT NOT NULL,
    leverage                    INT NOT NULL,
    position_size_pct           FLOAT NOT NULL,
    funding_min_rate            FLOAT NOT NULL,
    funding_max_rate            FLOAT NOT NULL,
    sl_pct                      FLOAT NOT NULL,
    tp_pct                      FLOAT NOT NULL,
    trailing_activation_pct     FLOAT NOT NULL,
    breakeven_activation_pct    FLOAT NOT NULL,
    PRIMARY KEY (config_id)
);

CREATE TABLE IF NOT EXISTS scoring_config_audit (
    id          SERIAL PRIMARY KEY,
    config_id   INT NOT NULL REFERENCES scoring_configs(id) ON DELETE CASCADE,
    changed_by  TEXT,
    changed_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    change_note TEXT
);

-- Seed default config
WITH ins AS (
    INSERT INTO scoring_configs (name, version, is_active, notes)
    VALUES ('default', 1, true, 'Initial config seeded from DefaultWeights')
    RETURNING id
)
INSERT INTO scoring_weights (config_id, category, max_points)
SELECT ins.id, v.category, v.max_points FROM ins, (VALUES
    ('funding',    25::float),
    ('oi',         15::float),
    ('btc',        10::float),
    ('candle',     20::float),
    ('volume',     10::float),
    ('roi',        15::float),
    ('volatility',  5::float)
) AS v(category, max_points);

WITH ins AS (
    SELECT id FROM scoring_configs WHERE name = 'default' AND version = 1
)
INSERT INTO scoring_thresholds (config_id, category, tier_order, min_value, max_value, multiplier, interpolate, condition)
SELECT ins.id, v.category, v.tier_order, v.min_value, v.max_value, v.multiplier, v.interpolate, v.condition
FROM ins, (VALUES
    ('oi',         1,  15.0,   NULL,   1.00, false, NULL),
    ('oi',         2,  10.0,   15.0,   0.75, false, NULL),
    ('oi',         3,   5.0,   10.0,   0.50, false, NULL),
    ('oi',         4,   0.0,    5.0,   0.30, false, NULL),
    ('oi',         5,  NULL,    0.0,   0.13, false, NULL),
    ('roi',        1,  15.0,   50.0,   1.00, false, NULL),
    ('roi',        2,  50.0,   80.0,   0.47, false, NULL),
    ('roi',        3,  NULL,   NULL,   0.00, false, NULL),
    ('volatility', 1,   1.0,    3.0,   1.00, false, NULL),
    ('volatility', 2,   3.0,    5.0,   0.60, false, NULL),
    ('volatility', 3,  NULL,   NULL,   0.00, false, NULL),
    ('volume',     1,  65.0,   NULL,   1.00, false, NULL),
    ('volume',     2,  NULL,   65.0,   0.50, false, NULL),
    ('volume',     3,  NULL,   NULL,   0.30, false, NULL),
    ('funding',    1,  -0.002, NULL,   0.00, false, NULL),
    ('funding',    2,  -0.005, -0.002, 0.67, true,  NULL),
    ('funding',    3,  -0.010, -0.005, 0.89, true,  NULL),
    ('funding',    4,  -0.020, -0.010, 1.00, true,  NULL),
    ('funding',    5,  NULL,   -0.020, 0.00, false, NULL),
    ('btc',        1,  NULL,   NULL,   0.00, false, 'breakout_bullish'),
    ('btc',        2,  70.0,   NULL,   0.20, false, 'bullish_high_momentum'),
    ('btc',        3,  NULL,   NULL,   0.30, false, 'bullish'),
    ('btc',        4,  NULL,   NULL,   0.70, false, 'neutral'),
    ('btc',        5,  NULL,   NULL,   1.00, false, 'bearish')
) AS v(category, tier_order, min_value, max_value, multiplier, interpolate, condition);

WITH ins AS (
    SELECT id FROM scoring_configs WHERE name = 'default' AND version = 1
)
INSERT INTO candle_weights (config_id, type, label, weight)
SELECT ins.id, v.type, v.label, v.weight FROM ins, (VALUES
    ('timeframe',     '1h',     1.00),
    ('timeframe',     '30m',    0.95),
    ('timeframe',     '15m',    0.90),
    ('timeframe',     '5m',     0.85),
    ('strength',      'STRONG', 1.00),
    ('strength',      'MEDIUM', 0.70),
    ('strength',      'WEAK',   0.35),
    ('multitf_bonus', '4',      0.40),
    ('multitf_bonus', '3',      0.25),
    ('multitf_bonus', '2',      0.10),
    ('multitf_bonus', '1',      0.00)
) AS v(type, label, weight);

WITH ins AS (
    SELECT id FROM scoring_configs WHERE name = 'default' AND version = 1
)
INSERT INTO confidence_tiers (config_id, min_score, confidence, position_size_pct)
SELECT ins.id, v.min_score, v.confidence, v.position_size_pct FROM ins, (VALUES
    (90.0, 'VERY_HIGH', 5.5),
    (80.0, 'HIGH',      5.0),
    (70.0, 'MEDIUM',    4.0),
    (60.0, 'LOW',       2.0),
    ( 0.0, 'SKIP',      0.0)
) AS v(min_score, confidence, position_size_pct);

WITH ins AS (
    SELECT id FROM scoring_configs WHERE name = 'default' AND version = 1
)
INSERT INTO bot_params (
    config_id, min_daily_roi_pct, min_volume_24h_m,
    max_positions, leverage, position_size_pct,
    funding_min_rate, funding_max_rate,
    sl_pct, tp_pct, trailing_activation_pct, breakeven_activation_pct
)
SELECT ins.id, 15.0, 25.0, 3, 20, 5.0, -0.02, -0.002, 5.0, 2.1, 1.5, 1.5 FROM ins;

WITH ins AS (
    SELECT id FROM scoring_configs WHERE name = 'default' AND version = 1
)
INSERT INTO scoring_config_audit (config_id, changed_by, change_note)
SELECT ins.id, 'migration', 'Initial seed from DefaultWeights' FROM ins;

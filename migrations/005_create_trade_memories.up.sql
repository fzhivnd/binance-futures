CREATE TABLE IF NOT EXISTS trade_memories (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    trade_id          UUID REFERENCES trades(id) ON DELETE SET NULL,
    symbol            VARCHAR(32) NOT NULL,
    action            VARCHAR(8) NOT NULL,  -- 'TRADE' | 'SKIP'
    created_at        TIMESTAMP NOT NULL DEFAULT NOW(),

    -- Setup conditions (embedded into vector)
    funding_rate      NUMERIC(10,6) NOT NULL,
    daily_roi         NUMERIC(10,4),
    oi_delta_1h       NUMERIC(8,4),
    oi_delta_15m      NUMERIC(8,4),
    rsi_14_15m        NUMERIC(6,2),
    rsi_7_5m          NUMERIC(6,2),
    atr_ratio         NUMERIC(6,4),
    vol_change_5m     NUMERIC(8,4),
    volume_spike      BOOLEAN NOT NULL DEFAULT false,
    momentum_loss     BOOLEAN NOT NULL DEFAULT false,
    candle_patterns   TEXT,
    composite_score   NUMERIC(6,2),
    entry_mode        VARCHAR(16),
    minutes_to_settle INT,

    -- BTC context
    btc_trend         VARCHAR(16),
    btc_momentum      INT,
    btc_breakout      BOOLEAN NOT NULL DEFAULT false,
    btc_rsi           NUMERIC(6,2),
    btc_change_1h     NUMERIC(8,4),

    -- Outcome (updated after trade closes / skip validation)
    outcome           VARCHAR(16),  -- 'WIN' | 'PARTIAL_WIN' | 'LOSS' | 'FORCE_SL' | 'BREAKEVEN' | 'SKIP_VALIDATED' | 'SKIP_MISSED'
    profit_pct        NUMERIC(8,4),
    hold_minutes      INT,

    -- LLM-generated lesson summary
    lesson            TEXT,

    -- Metadata for filtered vector search
    btc_regime        VARCHAR(16) NOT NULL,  -- 'bullish' | 'neutral' | 'bearish'
    funding_bucket    INT NOT NULL,          -- 1: mild, 2: moderate, 3: extreme

    -- Vector embedding (1536 dimensions — text-embedding-3-small)
    embedding         vector(1536) NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_trade_memories_embedding
    ON trade_memories USING ivfflat (embedding vector_cosine_ops)
    WITH (lists = 50);

CREATE INDEX IF NOT EXISTS idx_trade_memories_regime_bucket ON trade_memories (btc_regime, funding_bucket);
CREATE INDEX IF NOT EXISTS idx_trade_memories_trade_id      ON trade_memories (trade_id) WHERE trade_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_trade_memories_outcome_null  ON trade_memories (created_at) WHERE outcome IS NULL;
CREATE INDEX IF NOT EXISTS idx_trade_memories_created_at    ON trade_memories (created_at DESC);

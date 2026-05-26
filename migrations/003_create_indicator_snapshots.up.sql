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

    candle_patterns  JSONB,

    -- BTC context: trend on 1h, breakout detection on 15m
    btc_trend        VARCHAR(16),
    btc_momentum     INT,
    btc_rsi          NUMERIC(6,2),
    btc_change_1h    NUMERIC(8,4),
    btc_change_15m   NUMERIC(8,4),
    btc_breakout     BOOLEAN NOT NULL DEFAULT false,

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

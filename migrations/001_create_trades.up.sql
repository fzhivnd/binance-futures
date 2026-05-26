CREATE TABLE IF NOT EXISTS trades (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    symbol       VARCHAR(32) NOT NULL,
    side         VARCHAR(8) NOT NULL,
    entry_mode   VARCHAR(32) NOT NULL,
    leverage     INT NOT NULL,
    confidence   INT DEFAULT 0,
    funding_rate NUMERIC(10,6),
    daily_roi    NUMERIC(10,4),
    entry_price  NUMERIC(20,8) NOT NULL,
    exit_price   NUMERIC(20,8),
    pnl          NUMERIC(20,8),
    result       VARCHAR(16),
    is_paper     BOOLEAN NOT NULL DEFAULT true,
    created_at   TIMESTAMP NOT NULL DEFAULT NOW(),
    closed_at    TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_trades_symbol ON trades(symbol);
CREATE INDEX IF NOT EXISTS idx_trades_created_at ON trades(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_trades_result ON trades(result) WHERE result IS NOT NULL;

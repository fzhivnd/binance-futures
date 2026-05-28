CREATE TABLE IF NOT EXISTS daily_summaries (
    id          SERIAL PRIMARY KEY,
    trade_date  DATE NOT NULL,
    trade_count INTEGER NOT NULL DEFAULT 0,
    win_count   INTEGER NOT NULL DEFAULT 0,
    loss_count  INTEGER NOT NULL DEFAULT 0,
    win_rate    NUMERIC(5,2) NOT NULL DEFAULT 0,
    total_pnl   NUMERIC(20,8) NOT NULL DEFAULT 0,
    is_paper    BOOLEAN NOT NULL DEFAULT true,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT uq_daily_summaries_date UNIQUE (trade_date, is_paper)
);

CREATE INDEX idx_daily_summaries_date ON daily_summaries (trade_date DESC);

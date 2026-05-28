CREATE TABLE IF NOT EXISTS risk_state (
    trade_date       DATE PRIMARY KEY,
    daily_loss_count INT NOT NULL DEFAULT 0,
    disabled         BOOLEAN NOT NULL DEFAULT false,
    updated_at       TIMESTAMP NOT NULL DEFAULT NOW()
);

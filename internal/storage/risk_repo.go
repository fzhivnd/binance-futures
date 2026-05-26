package storage

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type RiskState struct {
	TradeDate      time.Time
	DailyLossCount int
	Disabled       bool
	UpdatedAt      time.Time
}

type PGRiskRepository struct {
	pool *pgxpool.Pool
}

func NewPGRiskRepository(pool *pgxpool.Pool) *PGRiskRepository {
	return &PGRiskRepository{pool: pool}
}

func (r *PGRiskRepository) Get(ctx context.Context, date time.Time) (*RiskState, error) {
	var rs RiskState
	err := r.pool.QueryRow(ctx, `
		SELECT trade_date, daily_loss_count, disabled, updated_at
		FROM risk_state WHERE trade_date = $1
	`, date.Format("2006-01-02")).Scan(&rs.TradeDate, &rs.DailyLossCount, &rs.Disabled, &rs.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &rs, nil
}

func (r *PGRiskRepository) Upsert(ctx context.Context, rs *RiskState) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO risk_state (trade_date, daily_loss_count, disabled, updated_at)
		VALUES ($1, $2, $3, NOW())
		ON CONFLICT (trade_date) DO UPDATE SET
		    daily_loss_count = EXCLUDED.daily_loss_count,
		    disabled = EXCLUDED.disabled,
		    updated_at = NOW()
	`, rs.TradeDate.Format("2006-01-02"), rs.DailyLossCount, rs.Disabled)
	return err
}

func (r *PGRiskRepository) IncrementLoss(ctx context.Context, date time.Time) (*RiskState, error) {
	var rs RiskState
	err := r.pool.QueryRow(ctx, `
		INSERT INTO risk_state (trade_date, daily_loss_count, disabled, updated_at)
		VALUES ($1, 1, false, NOW())
		ON CONFLICT (trade_date) DO UPDATE SET
		    daily_loss_count = risk_state.daily_loss_count + 1,
		    updated_at = NOW()
		RETURNING trade_date, daily_loss_count, disabled, updated_at
	`, date.Format("2006-01-02")).Scan(&rs.TradeDate, &rs.DailyLossCount, &rs.Disabled, &rs.UpdatedAt)
	return &rs, err
}

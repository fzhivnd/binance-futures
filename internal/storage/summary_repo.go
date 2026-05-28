package storage

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"futures/internal/domain"
)

type PGSummaryRepository struct {
	pool *pgxpool.Pool
}

func NewPGSummaryRepository(pool *pgxpool.Pool) *PGSummaryRepository {
	return &PGSummaryRepository{pool: pool}
}

func (r *PGSummaryRepository) Upsert(ctx context.Context, s *domain.DailySummary) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO daily_summaries (trade_date, trade_count, win_count, loss_count, win_rate, total_pnl, is_paper)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (trade_date, is_paper) DO UPDATE SET
			trade_count = EXCLUDED.trade_count,
			win_count   = EXCLUDED.win_count,
			loss_count  = EXCLUDED.loss_count,
			win_rate    = EXCLUDED.win_rate,
			total_pnl   = EXCLUDED.total_pnl
	`, s.TradeDate, s.TradeCount, s.WinCount, s.LossCount, s.WinRate, s.TotalPnL, s.IsPaper)
	return err
}

func (r *PGSummaryRepository) Exists(ctx context.Context, date time.Time, isPaper bool) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM daily_summaries WHERE trade_date=$1 AND is_paper=$2)
	`, date, isPaper).Scan(&exists)
	return exists, err
}

func (r *PGSummaryRepository) GetByDate(ctx context.Context, date time.Time, isPaper bool) (*domain.DailySummary, error) {
	s := &domain.DailySummary{}
	err := r.pool.QueryRow(ctx, `
		SELECT id, trade_date, trade_count, win_count, loss_count, win_rate, total_pnl, is_paper, created_at
		FROM daily_summaries WHERE trade_date=$1 AND is_paper=$2
	`, date, isPaper).Scan(&s.ID, &s.TradeDate, &s.TradeCount, &s.WinCount, &s.LossCount, &s.WinRate, &s.TotalPnL, &s.IsPaper, &s.CreatedAt)
	if err != nil {
		return nil, err
	}
	return s, nil
}

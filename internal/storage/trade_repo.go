package storage

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"futures/internal/domain"
)

type PGTradeRepository struct {
	pool *pgxpool.Pool
}

func NewPGTradeRepository(pool *pgxpool.Pool) *PGTradeRepository {
	return &PGTradeRepository{pool: pool}
}

func (r *PGTradeRepository) Insert(ctx context.Context, trade *domain.Trade) error {
	if trade.ID == uuid.Nil {
		trade.ID = uuid.New()
	}
	if trade.CreatedAt.IsZero() {
		trade.CreatedAt = time.Now()
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO trades (id, symbol, side, entry_mode, leverage, confidence,
		    funding_rate, change_24h, entry_price, quantity, is_paper, created_at,
		    llm_confidence, llm_entry_mode,
		    llm_entry_reasons, llm_warnings)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
	`, trade.ID, trade.Symbol, trade.Side, trade.EntryMode, trade.Leverage,
		trade.Confidence, trade.FundingRate, trade.Change24h,
		trade.EntryPrice, trade.Quantity, trade.IsPaper, trade.CreatedAt,
		trade.LLMConfidence, trade.LLMEntryMode,
		trade.LLMEntryReasons, trade.LLMWarnings)
	return err
}

func (r *PGTradeRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.Trade, error) {
	var t domain.Trade
	err := r.pool.QueryRow(ctx, `
		SELECT id, symbol, side, entry_mode, leverage, confidence,
		    funding_rate, change_24h, roi_pct, entry_price, exit_price, pnl, result,
		    is_paper, created_at, closed_at,
		    llm_confidence, llm_entry_mode,
		    llm_entry_reasons, llm_warnings
		FROM trades WHERE id = $1
	`, id).Scan(&t.ID, &t.Symbol, &t.Side, &t.EntryMode, &t.Leverage,
		&t.Confidence, &t.FundingRate, &t.Change24h, &t.ROIPct,
		&t.EntryPrice, &t.ExitPrice, &t.PnL, &t.Result,
		&t.IsPaper, &t.CreatedAt, &t.ClosedAt,
		&t.LLMConfidence, &t.LLMEntryMode,
		&t.LLMEntryReasons, &t.LLMWarnings)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (r *PGTradeRepository) GetRecent(ctx context.Context, limit int) ([]domain.Trade, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, symbol, side, entry_mode, leverage, confidence,
		    funding_rate, change_24h, roi_pct, entry_price, exit_price, pnl, result,
		    is_paper, created_at, closed_at,
		    llm_confidence, llm_entry_mode,
		    llm_entry_reasons, llm_warnings
		FROM trades ORDER BY created_at DESC LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var trades []domain.Trade
	for rows.Next() {
		var t domain.Trade
		if err := rows.Scan(&t.ID, &t.Symbol, &t.Side, &t.EntryMode, &t.Leverage,
			&t.Confidence, &t.FundingRate, &t.Change24h, &t.ROIPct,
			&t.EntryPrice, &t.ExitPrice, &t.PnL, &t.Result,
			&t.IsPaper, &t.CreatedAt, &t.ClosedAt,
			&t.LLMConfidence, &t.LLMEntryMode,
			&t.LLMEntryReasons, &t.LLMWarnings); err != nil {
			return nil, err
		}
		trades = append(trades, t)
	}
	return trades, nil
}

func (r *PGTradeRepository) UpdateResult(ctx context.Context, id uuid.UUID, exit domain.ExitInfo) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE trades SET
			exit_price=$1, avg_close_price=$1, pnl=$2, roi_pct=$3, result=$4,
			close_reason=$5, closed_at=$6
		WHERE id=$7
	`, exit.AvgClosePrice, exit.PnL, exit.ROIPct, exit.Result, exit.CloseReason, exit.ClosedAt, id)
	return err
}

func (r *PGTradeRepository) FindByOpenTimeRange(ctx context.Context, from, to time.Time, isPaper bool) ([]domain.Trade, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, symbol, side, entry_price, avg_close_price, pnl, result, close_reason, is_paper, created_at, closed_at
		FROM trades
		WHERE created_at >= $1 AND created_at < $2 AND is_paper = $3
		ORDER BY created_at ASC
	`, from, to, isPaper)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var trades []domain.Trade
	for rows.Next() {
		var t domain.Trade
		var pnl *float64
		var result *string
		if err := rows.Scan(&t.ID, &t.Symbol, &t.Side, &t.EntryPrice, &t.AvgClosePrice, &pnl, &result, &t.CloseReason, &t.IsPaper, &t.CreatedAt, &t.ClosedAt); err != nil {
			return nil, err
		}
		if pnl != nil {
			t.PnL = *pnl
		}
		if result != nil {
			t.Result = *result
		}
		trades = append(trades, t)
	}
	return trades, nil
}

func (r *PGTradeRepository) GetDailyLossCount(ctx context.Context, date time.Time) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM trades
		WHERE result='LOSS' AND DATE(created_at AT TIME ZONE 'UTC') = $1
	`, date.Format("2006-01-02")).Scan(&count)
	return count, err
}

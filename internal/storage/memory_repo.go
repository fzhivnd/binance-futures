package storage

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	pgvector "github.com/pgvector/pgvector-go"

	"futures/internal/domain"
)

type PGMemoryRepository struct {
	pool   *pgxpool.Pool
	maxAge time.Duration
}

func NewPGMemoryRepository(pool *pgxpool.Pool, maxAge time.Duration) *PGMemoryRepository {
	return &PGMemoryRepository{pool: pool, maxAge: maxAge}
}

func (r *PGMemoryRepository) Insert(ctx context.Context, memory *domain.TradeMemory, embedding pgvector.Vector) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO trade_memories (
			id, trade_id, symbol, action, created_at,
			funding_rate, daily_roi, oi_delta_1h, oi_delta_15m,
			rsi_14_15m, rsi_7_5m, atr_ratio, vol_change_5m,
			volume_spike, momentum_loss, candle_patterns,
			composite_score, entry_mode, minutes_to_settle,
			btc_trend, btc_momentum, btc_breakout, btc_rsi, btc_change_1h,
			outcome, profit_pct, hold_minutes, lesson,
			btc_regime, funding_bucket, embedding
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,
			$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30,$31
		)`,
		memory.ID, memory.TradeID, memory.Symbol, string(memory.Action), memory.CreatedAt,
		memory.FundingRate, memory.DailyROI, memory.OIDelta1h, memory.OIDelta15m,
		memory.RSI14_15m, memory.RSI7_5m, memory.ATRRatio, memory.VolChange5m,
		memory.VolumeSpike, memory.MomentumLoss, memory.CandlePatterns,
		memory.CompositeScore, memory.EntryMode, memory.MinutesToSettle,
		memory.BTCTrend, memory.BTCMomentum, memory.BTCBreakout, memory.BTCRSI, memory.BTCChange1h,
		nullableString(memory.Outcome), memory.ProfitPct, memory.HoldMinutes, nullableString(memory.Lesson),
		memory.BTCRegime, memory.FundingBucket, embedding,
	)
	return err
}

func (r *PGMemoryRepository) FindSimilar(
	ctx context.Context,
	embedding pgvector.Vector,
	btcRegime string,
	fundingBucket int,
	limit int,
) ([]MemorySearchResult, error) {
	cutoff := time.Now().Add(-r.maxAge)

	rows, err := r.pool.Query(ctx, `
		SELECT
			id, trade_id, symbol, action, created_at,
			funding_rate, daily_roi, oi_delta_1h, oi_delta_15m,
			rsi_14_15m, rsi_7_5m, atr_ratio, vol_change_5m,
			volume_spike, momentum_loss, candle_patterns,
			composite_score, entry_mode, minutes_to_settle,
			btc_trend, btc_momentum, btc_breakout, btc_rsi, btc_change_1h,
			outcome, profit_pct, hold_minutes, lesson,
			btc_regime, funding_bucket,
			1 - (embedding <=> $1) AS similarity
		FROM trade_memories
		WHERE outcome IS NOT NULL
		  AND created_at > $2
		  AND btc_regime = $3
		  AND ABS(funding_bucket - $4) <= 1
		ORDER BY embedding <=> $1
		LIMIT $5
	`, embedding, cutoff, btcRegime, fundingBucket, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []MemorySearchResult
	for rows.Next() {
		var m domain.TradeMemory
		var action string
		var similarity float64
		var outcome, lesson, entryMode, candlePatterns, btcTrend string
		var tradeID *uuid.UUID

		err := rows.Scan(
			&m.ID, &tradeID, &m.Symbol, &action, &m.CreatedAt,
			&m.FundingRate, &m.DailyROI, &m.OIDelta1h, &m.OIDelta15m,
			&m.RSI14_15m, &m.RSI7_5m, &m.ATRRatio, &m.VolChange5m,
			&m.VolumeSpike, &m.MomentumLoss, &candlePatterns,
			&m.CompositeScore, &entryMode, &m.MinutesToSettle,
			&btcTrend, &m.BTCMomentum, &m.BTCBreakout, &m.BTCRSI, &m.BTCChange1h,
			&outcome, &m.ProfitPct, &m.HoldMinutes, &lesson,
			&m.BTCRegime, &m.FundingBucket,
			&similarity,
		)
		if err != nil {
			return nil, err
		}
		m.Action = domain.TradeAction(action)
		m.Outcome = outcome
		m.Lesson = lesson
		m.EntryMode = entryMode
		m.CandlePatterns = candlePatterns
		m.BTCTrend = btcTrend
		m.TradeID = tradeID
		results = append(results, MemorySearchResult{Memory: m, Similarity: similarity})
	}

	return results, rows.Err()
}

func (r *PGMemoryRepository) UpdateOutcome(ctx context.Context, id uuid.UUID, outcome string, profitPct float64, holdMinutes int) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE trade_memories SET outcome=$1, profit_pct=$2, hold_minutes=$3
		WHERE id=$4
	`, outcome, profitPct, holdMinutes, id)
	return err
}

func (r *PGMemoryRepository) UpdateLesson(ctx context.Context, id uuid.UUID, lesson string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE trade_memories SET lesson=$1 WHERE id=$2
	`, lesson, id)
	return err
}

func (r *PGMemoryRepository) GetByTradeID(ctx context.Context, tradeID uuid.UUID) (*domain.TradeMemory, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, trade_id, symbol, action, created_at,
			funding_rate, daily_roi, oi_delta_1h, oi_delta_15m,
			rsi_14_15m, rsi_7_5m, atr_ratio, vol_change_5m,
			volume_spike, momentum_loss, candle_patterns,
			composite_score, entry_mode, minutes_to_settle,
			btc_trend, btc_momentum, btc_breakout, btc_rsi, btc_change_1h,
			outcome, profit_pct, hold_minutes, lesson,
			btc_regime, funding_bucket
		FROM trade_memories WHERE trade_id=$1
	`, tradeID)

	var m domain.TradeMemory
	var action, outcome, lesson, entryMode, candlePatterns, btcTrend string
	var tid *uuid.UUID

	err := row.Scan(
		&m.ID, &tid, &m.Symbol, &action, &m.CreatedAt,
		&m.FundingRate, &m.DailyROI, &m.OIDelta1h, &m.OIDelta15m,
		&m.RSI14_15m, &m.RSI7_5m, &m.ATRRatio, &m.VolChange5m,
		&m.VolumeSpike, &m.MomentumLoss, &candlePatterns,
		&m.CompositeScore, &entryMode, &m.MinutesToSettle,
		&btcTrend, &m.BTCMomentum, &m.BTCBreakout, &m.BTCRSI, &m.BTCChange1h,
		&outcome, &m.ProfitPct, &m.HoldMinutes, &lesson,
		&m.BTCRegime, &m.FundingBucket,
	)
	if err != nil {
		return nil, err
	}
	m.Action = domain.TradeAction(action)
	m.Outcome = outcome
	m.Lesson = lesson
	m.EntryMode = entryMode
	m.CandlePatterns = candlePatterns
	m.BTCTrend = btcTrend
	m.TradeID = tid
	return &m, nil
}

func (r *PGMemoryRepository) GetPendingSkipValidations(ctx context.Context, olderThan time.Duration) ([]domain.TradeMemory, error) {
	cutoff := time.Now().Add(-olderThan)
	rows, err := r.pool.Query(ctx, `
		SELECT id, symbol, action, created_at, funding_rate, daily_roi,
			btc_trend, btc_momentum, btc_breakout, btc_rsi, btc_change_1h,
			oi_delta_1h, rsi_14_15m, candle_patterns, composite_score,
			btc_regime, funding_bucket
		FROM trade_memories
		WHERE action = 'SKIP' AND outcome IS NULL AND created_at < $1
		ORDER BY created_at ASC
		LIMIT 50
	`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var memories []domain.TradeMemory
	for rows.Next() {
		var m domain.TradeMemory
		var action, btcTrend, candlePatterns string
		if err := rows.Scan(
			&m.ID, &m.Symbol, &action, &m.CreatedAt, &m.FundingRate, &m.DailyROI,
			&btcTrend, &m.BTCMomentum, &m.BTCBreakout, &m.BTCRSI, &m.BTCChange1h,
			&m.OIDelta1h, &m.RSI14_15m, &candlePatterns, &m.CompositeScore,
			&m.BTCRegime, &m.FundingBucket,
		); err != nil {
			return nil, err
		}
		m.Action = domain.TradeAction(action)
		m.BTCTrend = btcTrend
		m.CandlePatterns = candlePatterns
		memories = append(memories, m)
	}
	return memories, rows.Err()
}

// nullableString returns nil when s is empty so Postgres stores NULL instead of "".
func nullableString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

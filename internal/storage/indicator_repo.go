package storage

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/google/uuid"

	"futures/internal/domain"
)

type IndicatorRepository interface {
	Insert(ctx context.Context, tradeID uuid.UUID, snap *domain.IndicatorSnapshot, btc *domain.BTCContext, sc *domain.ScoredCandidate) error
}

type PGIndicatorRepository struct {
	pool *pgxpool.Pool
}

func NewPGIndicatorRepository(pool *pgxpool.Pool) *PGIndicatorRepository {
	return &PGIndicatorRepository{pool: pool}
}

func (r *PGIndicatorRepository) Insert(
	ctx context.Context,
	tradeID uuid.UUID,
	snap *domain.IndicatorSnapshot,
	btc *domain.BTCContext,
	sc *domain.ScoredCandidate,
) error {
	patternsJSON, _ := json.Marshal(snap.Patterns)

	var btcTrend string
	var btcMomentum int
	var btcRSI, btcChange1h, btcChange4h float64
	var btcBreakout bool
	if btc != nil {
		btcTrend = btc.Trend
		btcMomentum = btc.MomentumScore
		btcRSI = btc.RSI14_1h
		btcChange1h = btc.PriceChange1h
		btcChange4h = btc.PriceChange4h
		btcBreakout = btc.IsBreakout
	}

	_, err := r.pool.Exec(ctx, `
		INSERT INTO indicator_snapshots (
			trade_id, symbol,
			rsi_14_1h, oi_delta_1h, oi_delta_4h,
			atr_14_1h, atr_ratio, vol_change_1h, volume_spike,
			candle_patterns,
			btc_trend, btc_momentum, btc_rsi, btc_change_1h, btc_change_4h, btc_breakout,
			composite_score, score_funding, score_oi, score_btc, score_candle,
			score_volume, score_roi, score_volatility, confidence, position_size_pct
		) VALUES (
			$1, $2,
			$3, $4, $5,
			$6, $7, $8, $9,
			$10,
			$11, $12, $13, $14, $15, $16,
			$17, $18, $19, $20, $21,
			$22, $23, $24, $25, $26
		)`,
		tradeID, snap.Symbol,
		snap.RSI14_1h, snap.OIDelta1h, snap.OIDelta4h,
		snap.ATR14_1h, snap.ATRRatio, snap.VolChange1h, snap.VolumeSpike,
		patternsJSON,
		btcTrend, btcMomentum, btcRSI, btcChange1h, btcChange4h, btcBreakout,
		sc.CompositeScore,
		sc.Breakdown.FundingScore, sc.Breakdown.OIScore, sc.Breakdown.BTCScore, sc.Breakdown.CandleScore,
		sc.Breakdown.VolumeScore, sc.Breakdown.ROIScore, sc.Breakdown.VolatilityScore,
		sc.Confidence, sc.PositionSizePct,
	)
	return err
}

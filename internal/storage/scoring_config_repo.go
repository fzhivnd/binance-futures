package storage

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"futures/internal/domain"
)

type PGScoringConfigRepo struct {
	pool *pgxpool.Pool
}

func NewPGScoringConfigRepo(pool *pgxpool.Pool) *PGScoringConfigRepo {
	return &PGScoringConfigRepo{pool: pool}
}

// LoadActive loads the active scoring config with all child tables.
// Returns nil, nil if no active config exists.
func (r *PGScoringConfigRepo) LoadActive(ctx context.Context) (*domain.ScoringConfig, error) {
	cfg := &domain.ScoringConfig{}

	err := r.pool.QueryRow(ctx, `
		SELECT id, name, version FROM scoring_configs WHERE is_active = true LIMIT 1
	`).Scan(&cfg.ID, &cfg.Name, &cfg.Version)
	if err != nil {
		return nil, nil
	}

	if err := r.loadWeights(ctx, cfg); err != nil {
		return nil, fmt.Errorf("load weights: %w", err)
	}
	if err := r.loadThresholds(ctx, cfg); err != nil {
		return nil, fmt.Errorf("load thresholds: %w", err)
	}
	if err := r.loadCandleWeights(ctx, cfg); err != nil {
		return nil, fmt.Errorf("load candle weights: %w", err)
	}
	if err := r.loadConfidenceTiers(ctx, cfg); err != nil {
		return nil, fmt.Errorf("load confidence tiers: %w", err)
	}
	if err := r.loadBotParams(ctx, cfg); err != nil {
		return nil, fmt.Errorf("load bot params: %w", err)
	}

	return cfg, nil
}

func (r *PGScoringConfigRepo) loadWeights(ctx context.Context, cfg *domain.ScoringConfig) error {
	rows, err := r.pool.Query(ctx,
		`SELECT category, max_points FROM scoring_weights WHERE config_id = $1`, cfg.ID)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var category string
		var maxPoints float64
		if err := rows.Scan(&category, &maxPoints); err != nil {
			return err
		}
		switch category {
		case "funding":
			cfg.Weights.Funding = maxPoints
		case "oi":
			cfg.Weights.OI = maxPoints
		case "btc":
			cfg.Weights.BTC = maxPoints
		case "candle":
			cfg.Weights.Candle = maxPoints
		case "volume":
			cfg.Weights.Volume = maxPoints
		case "roi":
			cfg.Weights.ROI = maxPoints
		case "volatility":
			cfg.Weights.Volatility = maxPoints
		case "rsi_divergence":
			cfg.Weights.RSIDivergence = maxPoints
		}
	}
	return rows.Err()
}

func (r *PGScoringConfigRepo) loadThresholds(ctx context.Context, cfg *domain.ScoringConfig) error {
	rows, err := r.pool.Query(ctx, `
		SELECT category, tier_order, min_value, max_value, multiplier, interpolate, condition
		FROM scoring_thresholds
		WHERE config_id = $1
		ORDER BY category, tier_order
	`, cfg.ID)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var t domain.ScoringThreshold
		var cond *string
		if err := rows.Scan(&t.Category, &t.TierOrder, &t.MinValue, &t.MaxValue, &t.Multiplier, &t.Interpolate, &cond); err != nil {
			return err
		}
		if cond != nil {
			t.Condition = *cond
		}
		cfg.Thresholds = append(cfg.Thresholds, t)
	}
	return rows.Err()
}

func (r *PGScoringConfigRepo) loadCandleWeights(ctx context.Context, cfg *domain.ScoringConfig) error {
	rows, err := r.pool.Query(ctx,
		`SELECT type, label, weight FROM candle_weights WHERE config_id = $1`, cfg.ID)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var cw domain.CandleWeight
		if err := rows.Scan(&cw.Type, &cw.Label, &cw.Weight); err != nil {
			return err
		}
		cfg.CandleWeights = append(cfg.CandleWeights, cw)
	}
	return rows.Err()
}

func (r *PGScoringConfigRepo) loadConfidenceTiers(ctx context.Context, cfg *domain.ScoringConfig) error {
	rows, err := r.pool.Query(ctx, `
		SELECT min_score, confidence, position_size_pct
		FROM confidence_tiers
		WHERE config_id = $1
		ORDER BY min_score DESC
	`, cfg.ID)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var ct domain.ConfidenceTier
		if err := rows.Scan(&ct.MinScore, &ct.Confidence, &ct.PositionSizePct); err != nil {
			return err
		}
		cfg.ConfidenceTiers = append(cfg.ConfidenceTiers, ct)
	}
	return rows.Err()
}

func (r *PGScoringConfigRepo) loadBotParams(ctx context.Context, cfg *domain.ScoringConfig) error {
	return r.pool.QueryRow(ctx, `
		SELECT min_daily_roi_pct, min_volume_24h_m,
		       max_positions, leverage, position_size_pct,
		       funding_min_rate, funding_max_rate,
		       sl_pct, tp_pct, trailing_activation_pct, breakeven_activation_pct
		FROM bot_params WHERE config_id = $1
	`, cfg.ID).Scan(
		&cfg.BotParams.MinDailyROIPct,
		&cfg.BotParams.MinVolume24hM,
		&cfg.BotParams.MaxPositions,
		&cfg.BotParams.Leverage,
		&cfg.BotParams.PositionSizePct,
		&cfg.BotParams.FundingMinRate,
		&cfg.BotParams.FundingMaxRate,
		&cfg.BotParams.SlPct,
		&cfg.BotParams.TpPct,
		&cfg.BotParams.TrailingActivationPct,
		&cfg.BotParams.BreakevenActivationPct,
	)
}

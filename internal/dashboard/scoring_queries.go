package dashboard

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ScoringQueries struct {
	pool *pgxpool.Pool
}

func NewScoringQueries(pool *pgxpool.Pool) *ScoringQueries {
	return &ScoringQueries{pool: pool}
}

// --- Response types ---

type ScoringConfigRow struct {
	ID        int       `json:"id"`
	Name      string    `json:"name"`
	Version   int       `json:"version"`
	IsActive  bool      `json:"is_active"`
	Notes     *string   `json:"notes,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type ScoringWeightRow struct {
	Category  string  `json:"category"`
	MaxPoints float64 `json:"max_points"`
}

type ScoringThresholdRow struct {
	Category    string   `json:"category"`
	TierOrder   int      `json:"tier_order"`
	MinValue    *float64 `json:"min_value,omitempty"`
	MaxValue    *float64 `json:"max_value,omitempty"`
	Multiplier  float64  `json:"multiplier"`
	Interpolate bool     `json:"interpolate"`
	Condition   *string  `json:"condition,omitempty"`
}

type CandleWeightRow struct {
	Type   string  `json:"type"`
	Label  string  `json:"label"`
	Weight float64 `json:"weight"`
}

type ConfidenceTierRow struct {
	MinScore        float64 `json:"min_score"`
	Confidence      string  `json:"confidence"`
	PositionSizePct float64 `json:"position_size_pct"`
}

type BotParamsRow struct {
	MinDailyROIPct         float64 `json:"min_daily_roi_pct"`
	MinVolume24hM          float64 `json:"min_volume_24h_m"`
	MaxPositions           int     `json:"max_positions"`
	Leverage               int     `json:"leverage"`
	PositionSizePct        float64 `json:"position_size_pct"`
	FundingMinRate         float64 `json:"funding_min_rate"`
	FundingMaxRate         float64 `json:"funding_max_rate"`
	SlPct                  float64 `json:"sl_pct"`
	TpPct                  float64 `json:"tp_pct"`
	Tp2Pct                 float64 `json:"tp2_pct"`
	TrailingActivationPct  float64 `json:"trailing_activation_pct"`
	BreakevenActivationPct float64 `json:"breakeven_activation_pct"`
}

type FullScoringConfig struct {
	ScoringConfigRow
	Weights         []ScoringWeightRow    `json:"weights"`
	Thresholds      []ScoringThresholdRow `json:"thresholds"`
	CandleWeights   []CandleWeightRow     `json:"candle_weights"`
	ConfidenceTiers []ConfidenceTierRow   `json:"confidence_tiers"`
	BotParams       *BotParamsRow         `json:"bot_params,omitempty"`
}

// --- Queries ---

func (q *ScoringQueries) ListConfigs(ctx context.Context) ([]ScoringConfigRow, error) {
	rows, err := q.pool.Query(ctx,
		`SELECT id, name, version, is_active, notes, created_at
		 FROM scoring_configs ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ScoringConfigRow
	for rows.Next() {
		var r ScoringConfigRow
		if err := rows.Scan(&r.ID, &r.Name, &r.Version, &r.IsActive, &r.Notes, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (q *ScoringQueries) GetConfig(ctx context.Context, id int) (*FullScoringConfig, error) {
	cfg := &FullScoringConfig{}
	err := q.pool.QueryRow(ctx,
		`SELECT id, name, version, is_active, notes, created_at FROM scoring_configs WHERE id = $1`, id,
	).Scan(&cfg.ID, &cfg.Name, &cfg.Version, &cfg.IsActive, &cfg.Notes, &cfg.CreatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := q.fillChildren(ctx, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (q *ScoringQueries) GetActiveConfig(ctx context.Context) (*FullScoringConfig, error) {
	cfg := &FullScoringConfig{}
	err := q.pool.QueryRow(ctx,
		`SELECT id, name, version, is_active, notes, created_at FROM scoring_configs WHERE is_active = true LIMIT 1`,
	).Scan(&cfg.ID, &cfg.Name, &cfg.Version, &cfg.IsActive, &cfg.Notes, &cfg.CreatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := q.fillChildren(ctx, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (q *ScoringQueries) fillChildren(ctx context.Context, cfg *FullScoringConfig) error {
	// weights
	wrows, err := q.pool.Query(ctx,
		`SELECT category, max_points FROM scoring_weights WHERE config_id = $1`, cfg.ID)
	if err != nil {
		return err
	}
	defer wrows.Close()
	for wrows.Next() {
		var r ScoringWeightRow
		if err := wrows.Scan(&r.Category, &r.MaxPoints); err != nil {
			return err
		}
		cfg.Weights = append(cfg.Weights, r)
	}
	wrows.Close()

	// thresholds
	trows, err := q.pool.Query(ctx,
		`SELECT category, tier_order, min_value, max_value, multiplier, interpolate, condition
		 FROM scoring_thresholds WHERE config_id = $1 ORDER BY category, tier_order`, cfg.ID)
	if err != nil {
		return err
	}
	defer trows.Close()
	for trows.Next() {
		var r ScoringThresholdRow
		if err := trows.Scan(&r.Category, &r.TierOrder, &r.MinValue, &r.MaxValue, &r.Multiplier, &r.Interpolate, &r.Condition); err != nil {
			return err
		}
		cfg.Thresholds = append(cfg.Thresholds, r)
	}
	trows.Close()

	// candle weights
	crows, err := q.pool.Query(ctx,
		`SELECT type, label, weight FROM candle_weights WHERE config_id = $1`, cfg.ID)
	if err != nil {
		return err
	}
	defer crows.Close()
	for crows.Next() {
		var r CandleWeightRow
		if err := crows.Scan(&r.Type, &r.Label, &r.Weight); err != nil {
			return err
		}
		cfg.CandleWeights = append(cfg.CandleWeights, r)
	}
	crows.Close()

	// confidence tiers
	ctrows, err := q.pool.Query(ctx,
		`SELECT min_score, confidence, position_size_pct FROM confidence_tiers
		 WHERE config_id = $1 ORDER BY min_score DESC`, cfg.ID)
	if err != nil {
		return err
	}
	defer ctrows.Close()
	for ctrows.Next() {
		var r ConfidenceTierRow
		if err := ctrows.Scan(&r.MinScore, &r.Confidence, &r.PositionSizePct); err != nil {
			return err
		}
		cfg.ConfidenceTiers = append(cfg.ConfidenceTiers, r)
	}
	ctrows.Close()

	// bot params
	var bp BotParamsRow
	err = q.pool.QueryRow(ctx, `
		SELECT min_daily_roi_pct, min_volume_24h_m,
		       max_positions, leverage, position_size_pct,
		       funding_min_rate, funding_max_rate,
		       sl_pct, tp_pct, trailing_activation_pct, breakeven_activation_pct
		FROM bot_params WHERE config_id = $1`, cfg.ID,
	).Scan(
		&bp.MinDailyROIPct, &bp.MinVolume24hM,
		&bp.MaxPositions, &bp.Leverage, &bp.PositionSizePct,
		&bp.FundingMinRate, &bp.FundingMaxRate,
		&bp.SlPct, &bp.TpPct, &bp.TrailingActivationPct, &bp.BreakevenActivationPct,
	)
	if err == nil {
		cfg.BotParams = &bp
	}

	return nil
}

// CreateConfig clones the active config under a new name (or version-bumps if same name).
func (q *ScoringQueries) CreateConfig(ctx context.Context, name, notes string) (int, error) {
	tx, err := q.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	// determine version
	var version int
	err = tx.QueryRow(ctx,
		`SELECT COALESCE(MAX(version), 0) + 1 FROM scoring_configs WHERE name = $1`, name,
	).Scan(&version)
	if err != nil {
		return 0, err
	}

	var newID int
	err = tx.QueryRow(ctx,
		`INSERT INTO scoring_configs (name, version, is_active, notes) VALUES ($1, $2, false, $3) RETURNING id`,
		name, version, notes,
	).Scan(&newID)
	if err != nil {
		return 0, err
	}

	// copy all child tables from active config
	_, err = tx.Exec(ctx, `
		INSERT INTO scoring_weights (config_id, category, max_points)
		SELECT $1, category, max_points FROM scoring_weights
		WHERE config_id = (SELECT id FROM scoring_configs WHERE is_active = true LIMIT 1)
	`, newID)
	if err != nil {
		return 0, fmt.Errorf("copy weights: %w", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO scoring_thresholds (config_id, category, tier_order, min_value, max_value, multiplier, interpolate, condition)
		SELECT $1, category, tier_order, min_value, max_value, multiplier, interpolate, condition
		FROM scoring_thresholds
		WHERE config_id = (SELECT id FROM scoring_configs WHERE is_active = true LIMIT 1)
	`, newID)
	if err != nil {
		return 0, fmt.Errorf("copy thresholds: %w", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO candle_weights (config_id, type, label, weight)
		SELECT $1, type, label, weight FROM candle_weights
		WHERE config_id = (SELECT id FROM scoring_configs WHERE is_active = true LIMIT 1)
	`, newID)
	if err != nil {
		return 0, fmt.Errorf("copy candle weights: %w", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO confidence_tiers (config_id, min_score, confidence, position_size_pct)
		SELECT $1, min_score, confidence, position_size_pct FROM confidence_tiers
		WHERE config_id = (SELECT id FROM scoring_configs WHERE is_active = true LIMIT 1)
	`, newID)
	if err != nil {
		return 0, fmt.Errorf("copy confidence tiers: %w", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO bot_params (config_id, min_daily_roi_pct, min_volume_24h_m,
		    max_positions, leverage, position_size_pct,
		    funding_min_rate, funding_max_rate,
		    sl_pct, tp_pct, trailing_activation_pct, breakeven_activation_pct)
		SELECT $1, min_daily_roi_pct, min_volume_24h_m,
		    max_positions, leverage, position_size_pct,
		    funding_min_rate, funding_max_rate,
		    sl_pct, tp_pct, trailing_activation_pct, breakeven_activation_pct
		FROM bot_params
		WHERE config_id = (SELECT id FROM scoring_configs WHERE is_active = true LIMIT 1)
	`, newID)
	if err != nil {
		return 0, fmt.Errorf("copy bot params: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return newID, nil
}

// UpdateWeights replaces scoring_weights for an inactive config.
func (q *ScoringQueries) UpdateWeights(ctx context.Context, id int, weights []ScoringWeightRow) error {
	if err := q.guardInactive(ctx, id); err != nil {
		return err
	}
	tx, err := q.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM scoring_weights WHERE config_id = $1`, id); err != nil {
		return err
	}
	for _, w := range weights {
		if _, err := tx.Exec(ctx,
			`INSERT INTO scoring_weights (config_id, category, max_points) VALUES ($1, $2, $3)`,
			id, w.Category, w.MaxPoints); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// UpdateThresholds replaces scoring_thresholds for an inactive config.
func (q *ScoringQueries) UpdateThresholds(ctx context.Context, id int, thresholds []ScoringThresholdRow) error {
	if err := q.guardInactive(ctx, id); err != nil {
		return err
	}
	tx, err := q.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM scoring_thresholds WHERE config_id = $1`, id); err != nil {
		return err
	}
	for _, t := range thresholds {
		if _, err := tx.Exec(ctx,
			`INSERT INTO scoring_thresholds (config_id, category, tier_order, min_value, max_value, multiplier, interpolate, condition)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			id, t.Category, t.TierOrder, t.MinValue, t.MaxValue, t.Multiplier, t.Interpolate, t.Condition,
		); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// UpdateCandleWeights replaces candle_weights for an inactive config.
func (q *ScoringQueries) UpdateCandleWeights(ctx context.Context, id int, weights []CandleWeightRow) error {
	if err := q.guardInactive(ctx, id); err != nil {
		return err
	}
	tx, err := q.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM candle_weights WHERE config_id = $1`, id); err != nil {
		return err
	}
	for _, w := range weights {
		if _, err := tx.Exec(ctx,
			`INSERT INTO candle_weights (config_id, type, label, weight) VALUES ($1, $2, $3, $4)`,
			id, w.Type, w.Label, w.Weight); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// UpdateConfidenceTiers replaces confidence_tiers for an inactive config.
func (q *ScoringQueries) UpdateConfidenceTiers(ctx context.Context, id int, tiers []ConfidenceTierRow) error {
	if err := q.guardInactive(ctx, id); err != nil {
		return err
	}
	tx, err := q.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM confidence_tiers WHERE config_id = $1`, id); err != nil {
		return err
	}
	for _, t := range tiers {
		if _, err := tx.Exec(ctx,
			`INSERT INTO confidence_tiers (config_id, min_score, confidence, position_size_pct) VALUES ($1, $2, $3, $4)`,
			id, t.MinScore, t.Confidence, t.PositionSizePct); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// UpdateBotParams replaces bot_params for an inactive config.
func (q *ScoringQueries) UpdateBotParams(ctx context.Context, id int, p BotParamsRow) error {
	if err := q.guardInactive(ctx, id); err != nil {
		return err
	}
	_, err := q.pool.Exec(ctx, `
		INSERT INTO bot_params (config_id, min_daily_roi_pct, min_volume_24h_m,
		    max_positions, leverage, position_size_pct,
		    funding_min_rate, funding_max_rate,
		    sl_pct, tp_pct, tp2_pct, trailing_activation_pct, breakeven_activation_pct)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (config_id) DO UPDATE SET
		    min_daily_roi_pct = EXCLUDED.min_daily_roi_pct,
		    min_volume_24h_m  = EXCLUDED.min_volume_24h_m,
		    max_positions     = EXCLUDED.max_positions,
		    leverage          = EXCLUDED.leverage,
		    position_size_pct = EXCLUDED.position_size_pct,
		    funding_min_rate  = EXCLUDED.funding_min_rate,
		    funding_max_rate  = EXCLUDED.funding_max_rate,
		    sl_pct            = EXCLUDED.sl_pct,
		    tp_pct            = EXCLUDED.tp_pct,
		    tp2_pct           = EXCLUDED.tp2_pct,
		    trailing_activation_pct  = EXCLUDED.trailing_activation_pct,
		    breakeven_activation_pct = EXCLUDED.breakeven_activation_pct
	`, id, p.MinDailyROIPct, p.MinVolume24hM,
		p.MaxPositions, p.Leverage, p.PositionSizePct,
		p.FundingMinRate, p.FundingMaxRate,
		p.SlPct, p.TpPct, p.Tp2Pct, p.TrailingActivationPct, p.BreakevenActivationPct,
	)
	return err
}

// ActivateConfig validates completeness then atomically activates the config.
func (q *ScoringQueries) ActivateConfig(ctx context.Context, id int, changedBy string) error {
	if err := q.validateComplete(ctx, id); err != nil {
		return err
	}
	tx, err := q.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `UPDATE scoring_configs SET is_active = false WHERE is_active = true`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE scoring_configs SET is_active = true WHERE id = $1`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO scoring_config_audit (config_id, changed_by, change_note) VALUES ($1, $2, 'activated')`,
		id, changedBy,
	); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// DeleteConfig deletes an inactive config.
func (q *ScoringQueries) DeleteConfig(ctx context.Context, id int) error {
	var isActive bool
	err := q.pool.QueryRow(ctx, `SELECT is_active FROM scoring_configs WHERE id = $1`, id).Scan(&isActive)
	if err == pgx.ErrNoRows {
		return fmt.Errorf("config %d not found", id)
	}
	if err != nil {
		return err
	}
	if isActive {
		return fmt.Errorf("cannot delete active config")
	}
	_, err = q.pool.Exec(ctx, `DELETE FROM scoring_configs WHERE id = $1`, id)
	return err
}

// guardInactive returns an error if the config is active or not found.
func (q *ScoringQueries) guardInactive(ctx context.Context, id int) error {
	var isActive bool
	err := q.pool.QueryRow(ctx, `SELECT is_active FROM scoring_configs WHERE id = $1`, id).Scan(&isActive)
	if err == pgx.ErrNoRows {
		return fmt.Errorf("config %d not found", id)
	}
	if err != nil {
		return err
	}
	if isActive {
		return fmt.Errorf("cannot edit active config — clone it first")
	}
	return nil
}

// validateComplete checks all required child tables have rows before activation.
func (q *ScoringQueries) validateComplete(ctx context.Context, id int) error {
	checks := []struct {
		table string
		query string
	}{
		{"scoring_weights", `SELECT COUNT(*) FROM scoring_weights WHERE config_id = $1`},
		{"scoring_thresholds", `SELECT COUNT(*) FROM scoring_thresholds WHERE config_id = $1`},
		{"candle_weights", `SELECT COUNT(*) FROM candle_weights WHERE config_id = $1`},
		{"confidence_tiers", `SELECT COUNT(*) FROM confidence_tiers WHERE config_id = $1`},
		{"bot_params", `SELECT COUNT(*) FROM bot_params WHERE config_id = $1`},
	}
	for _, c := range checks {
		var count int
		if err := q.pool.QueryRow(ctx, c.query, id).Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			return fmt.Errorf("incomplete config: %s has no rows", c.table)
		}
	}
	return nil
}

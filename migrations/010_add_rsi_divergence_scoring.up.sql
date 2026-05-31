-- Add rsi_divergence weight to all existing scoring configs
INSERT INTO scoring_weights (config_id, category, max_points)
SELECT id, 'rsi_divergence', 10.0
FROM scoring_configs
ON CONFLICT (config_id, category) DO NOTHING;
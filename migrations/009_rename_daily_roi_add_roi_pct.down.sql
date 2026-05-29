ALTER TABLE trades DROP COLUMN IF EXISTS roi_pct;
ALTER TABLE trades RENAME COLUMN change_24h TO daily_roi;

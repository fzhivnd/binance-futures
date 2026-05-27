-- Phase 5: simplified close tracking + force-SL result types
ALTER TABLE trades ADD COLUMN IF NOT EXISTS close_reason    VARCHAR(16);
ALTER TABLE trades ADD COLUMN IF NOT EXISTS avg_close_price NUMERIC(20,8);

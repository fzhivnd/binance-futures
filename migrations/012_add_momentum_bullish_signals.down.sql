ALTER TABLE trade_memories
    DROP COLUMN IF EXISTS bullish_momentum,
    DROP COLUMN IF EXISTS bearish_momentum_weak,
    DROP COLUMN IF EXISTS bullish_candle_patterns;

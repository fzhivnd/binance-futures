ALTER TABLE trades DROP COLUMN IF EXISTS llm_confidence;
ALTER TABLE trades DROP COLUMN IF EXISTS llm_entry_mode;
ALTER TABLE trades DROP COLUMN IF EXISTS llm_entry_reasons;
ALTER TABLE trades DROP COLUMN IF EXISTS llm_warnings;

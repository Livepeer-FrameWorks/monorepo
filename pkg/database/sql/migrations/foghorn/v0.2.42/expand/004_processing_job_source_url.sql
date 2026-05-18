ALTER TABLE foghorn.processing_jobs
  ADD COLUMN IF NOT EXISTS source_url TEXT,
  ADD COLUMN IF NOT EXISTS source_params JSONB,
  ADD COLUMN IF NOT EXISTS preferred_node_id VARCHAR(100);

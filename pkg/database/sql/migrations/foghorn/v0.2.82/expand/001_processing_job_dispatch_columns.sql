ALTER TABLE foghorn.processing_jobs
  ADD COLUMN IF NOT EXISTS source_url TEXT,
  ADD COLUMN IF NOT EXISTS source_params JSONB,
  ADD COLUMN IF NOT EXISTS preferred_node_id VARCHAR(100);

CREATE INDEX IF NOT EXISTS idx_foghorn_processing_jobs_queued_attempt
    ON foghorn.processing_jobs(status, updated_at, created_at)
    WHERE status = 'queued';

CREATE INDEX IF NOT EXISTS idx_foghorn_processing_jobs_queued_attempt
    ON foghorn.processing_jobs(status, updated_at, created_at)
    WHERE status = 'queued';

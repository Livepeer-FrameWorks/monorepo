-- Media progress watchdog for processing jobs. Lease heartbeats refresh
-- updated_at but not these columns, so stale recovery can fail a job that keeps
-- heartbeating without producing output. Active jobs from before this release
-- keep a NULL progress_advanced_at and are only covered by the lease timeout.
ALTER TABLE foghorn.processing_jobs
    ADD COLUMN IF NOT EXISTS progress_last_ms BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS progress_advanced_at TIMESTAMP;

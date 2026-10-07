-- Source-stall retries for processing jobs. source_stalled_since starts an
-- unbroken run of attempts whose source read stalled in storage or upstream, and
-- clears when an attempt's media advances; next_attempt_at holds a requeued job
-- back for its backoff. Neither consumes the job's retry budget.
ALTER TABLE foghorn.processing_jobs
    ADD COLUMN IF NOT EXISTS source_stalled_since TIMESTAMP,
    ADD COLUMN IF NOT EXISTS next_attempt_at TIMESTAMP;

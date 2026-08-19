-- Backoff for catalog projection. A row the reconciler can't project (Commodore RPC failure,
-- or the catalog row isn't registered yet) never advances its watermark; since the projection
-- scan is oldest-watermark-first + LIMIT batch, a cluster of stuck rows would head-of-line block
-- every newer artifact indefinitely. catalog_next_attempt_at holds a stuck row out of the scan
-- until its backoff elapses, so newer rows always get a turn while stuck rows retry periodically.
--
-- Schema source of truth: pkg/database/sql/schema/foghorn.sql.

ALTER TABLE foghorn.artifacts
    ADD COLUMN IF NOT EXISTS catalog_next_attempt_at TIMESTAMPTZ,
    -- Consecutive failed projection attempts, driving exponential backoff. A fixed backoff
    -- shorter than the reconcile interval leaves a permanently-failing row eligible again before
    -- every pass; exponential growth pushes a stuck row out of contention so newer rows progress.
    ADD COLUMN IF NOT EXISTS catalog_projection_attempts INTEGER NOT NULL DEFAULT 0;

-- foghorn.artifacts.sync_status gains a documented terminal value 'lost_local'
-- to mark generic artifacts whose local source file is gone (ENOENT) before
-- any S3 sync. lost_local is terminal: no retries, row stays as a tombstone.
-- Combined with status='failed' (an existing enforced value) it is excluded
-- from playback / billing / cleanup-pressure paths via existing filters.
--
-- sync_status is VARCHAR(50) with no CHECK constraint, so this migration
-- documents the new value and adds a failure_count column used by the retry
-- budget (see retryFailed).

ALTER TABLE foghorn.artifacts
    ADD COLUMN IF NOT EXISTS failure_count INT NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_foghorn_artifacts_lost_local
    ON foghorn.artifacts (sync_status)
    WHERE sync_status = 'lost_local';

COMMENT ON COLUMN foghorn.artifacts.sync_status IS
    'pending | in_progress | synced | failed | lost_local. lost_local is terminal (local source gone before sync; never retried, tombstone).';

COMMENT ON COLUMN foghorn.artifacts.failure_count IS
    'Number of failed sync attempts for this artifact. retryFailed caps retries by this count.';

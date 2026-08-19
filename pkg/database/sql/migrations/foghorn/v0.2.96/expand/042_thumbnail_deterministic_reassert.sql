-- Bounded-reassert clock for the deterministic served key. The projection to the deterministic key
-- (thumbnails/{asset}/{file}) is a NON-transactional S3 copy whose destination write is unconditional, so a PostgreSQL
-- lock cannot make it strictly serial: a copy accepted by S3 can complete AFTER the client context is cancelled and the
-- transaction released, letting a stale loser's late copy overwrite the winner's bytes. The contract is therefore
-- EVENTUAL, not strict. deterministic_reassert_at is when the CURRENT winner must re-copy its bytes to the deterministic
-- key ONE more time, past the maximum in-flight-copy window — so any straggler overwrite from an earlier loser is
-- corrected. Set at projection settle to NOW() + window; the reassert reconciler re-copies the still-active winner and
-- clears it to NULL. NULL = no reassert pending (converged, or superseded — the newer winner carries its own clock).
ALTER TABLE foghorn.thumbnail_task_assignment
    ADD COLUMN IF NOT EXISTS deterministic_reassert_at TIMESTAMPTZ;

-- The reassert reconciler scans winners whose reassert is DUE oldest-first; a partial index keeps that scan cheap since
-- the vast majority of rows have converged (reassert_at IS NULL) and are excluded by the predicate.
CREATE INDEX IF NOT EXISTS idx_foghorn_thumb_reassert_due
    ON foghorn.thumbnail_task_assignment(deterministic_reassert_at)
    WHERE deterministic_reassert_at IS NOT NULL;

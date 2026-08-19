-- Two-phase stream deletion (deleting → tombstoned → deleted). A stream is not HARD-deleted the moment the caller
-- asks; it is first SOFT-deleted (deleted_at set) and the thumbnail-cleanup obligation enqueued, so the caller is
-- never told "deleted" before the SERVING authority (Foghorn) durably holds the tombstone. The stream is excluded
-- from all user-facing + serving/resolve reads while deleted_at IS NOT NULL, and is HARD-deleted (finalized) only
-- after Foghorn positively acks the cleanup obligation (synchronously in DeleteStream, or by the outbox worker on
-- a later delivery). During a Foghorn outage the row lingers as 'deleting' and DeleteStream returns deletion_pending
-- — never a false "deleted". Schema source of truth: pkg/database/sql/schema/commodore.sql.
ALTER TABLE commodore.streams
    ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;

-- Partial index so the soft-deleted (finalization-pending) set is cheap to scan for reconciliation.
CREATE INDEX IF NOT EXISTS idx_commodore_streams_deleting
    ON commodore.streams(deleted_at)
    WHERE deleted_at IS NOT NULL;

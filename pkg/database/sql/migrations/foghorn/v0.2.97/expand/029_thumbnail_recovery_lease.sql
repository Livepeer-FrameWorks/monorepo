-- HA-safe crash recovery for stuck thumbnail completions. The recovery reconciler re-drives attempts whose node
-- completion was lost (a dropped ThumbnailUploaded / a crash before 'publishing'). Without leasing, every replica
-- re-drives the SAME attempts each tick (duplicate S3 HEAD/promote work), and a poison attempt (staging never
-- uploaded, or a genuinely broken one) is re-selected at the head of every pass, starving other lost completions.
-- These columns make the re-drive claim a LEASED, BACKOFF-fenced queue over the assignment rows themselves: a
-- worker leases a batch (fencing token), and a not-progressed attempt is backed off (recovery_next_attempt_at)
-- instead of re-selected. The lease is the concurrency fence — the job sizes each batch to PROVABLY finish within
-- the lease (see NewThumbnailRecoveryJob's budget), so a peer does not reclaim mid-batch and re-drive the same
-- attempt; and the re-drive itself (PublishThumbnailAttempt / the completion CAS) is a GUARDED, idempotent
-- transition, so even a lease overrun cannot corrupt — at worst it duplicates work. Idempotent ADDs.
-- Schema source of truth: pkg/database/sql/schema/foghorn.sql.
ALTER TABLE foghorn.thumbnail_task_assignment
    ADD COLUMN IF NOT EXISTS recovery_leased_until    TIMESTAMPTZ,   -- in-flight recovery lease; a worker skips a leased row until it expires
    ADD COLUMN IF NOT EXISTS recovery_lease_token     TEXT,          -- fences settlement: a worker whose lease expired cannot settle another's claim
    ADD COLUMN IF NOT EXISTS recovery_attempts        INTEGER NOT NULL DEFAULT 0, -- poison-backoff counter
    ADD COLUMN IF NOT EXISTS recovery_next_attempt_at TIMESTAMPTZ,   -- due time; a backed-off attempt is not re-selected until this passes
    ADD COLUMN IF NOT EXISTS recovery_last_error      TEXT;

-- Due-recovery scan: only non-terminal attempts are ever re-driven; ordering by next_attempt_at keeps it fair
-- (a backed-off poison row sinks below due lost completions).
CREATE INDEX IF NOT EXISTS idx_foghorn_thumb_recovery_due
    ON foghorn.thumbnail_task_assignment(recovery_next_attempt_at)
    WHERE status IN ('assigned', 'uploading', 'verifying', 'publishing');

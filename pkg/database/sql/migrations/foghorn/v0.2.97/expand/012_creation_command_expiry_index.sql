-- Expiry-worker scan index for foghorn.artifact_creation_commands: 'accepted' rows
-- oldest-first by updated_at. Partial so the committed/rejected terminal rows never
-- widen the bounded scan the CreationCommandExpiryJob runs each minute; without it the
-- worker degrades with total historical volume.
--
-- Schema source of truth: pkg/database/sql/schema/foghorn.sql — same index in the
-- baseline so a fresh init and an upgrade converge.
CREATE INDEX IF NOT EXISTS idx_foghorn_creation_commands_accepted
    ON foghorn.artifact_creation_commands(updated_at)
    WHERE status = 'accepted';

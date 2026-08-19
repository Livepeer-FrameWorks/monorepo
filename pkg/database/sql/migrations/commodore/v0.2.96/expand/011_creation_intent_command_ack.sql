-- Durable ack obligation for commodore.artifact_creation_intents. An intent that
-- terminalizes from a KNOWN Foghorn command (a commit, or a definitive rejection) sets
-- command_ack_pending=TRUE in the SAME transaction as the terminal transition; the
-- ack-drain worker calls AckArtifactCreationCommand and clears the flag (command_acked_at
-- set) on success. Because the obligation is a column, not in-memory state, it survives a
-- restart, so Foghorn's GC only reclaims a command row Commodore has durably consumed. The
-- ack is not guaranteed to succeed — Commodore may be unable to reach Foghorn, and a
-- persistently-failing ack backs off indefinitely without converging; the obligation is
-- retained (surfaced via Foghorn's unconsumed-backlog WARN), never silently cleared. A
-- MISSING-abort has no Foghorn command, so it leaves command_ack_pending FALSE.
--
-- Schema source of truth: pkg/database/sql/schema/commodore.sql — same columns and index in
-- the baseline so a fresh init and an upgrade converge.
ALTER TABLE commodore.artifact_creation_intents
    ADD COLUMN IF NOT EXISTS command_ack_pending BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS command_acked_at TIMESTAMP;

-- Ack-drain scan index: intents still owing Foghorn an ack, oldest-first. Partial so the
-- drain touches only outstanding obligations, not the terminal rows already acked.
CREATE INDEX IF NOT EXISTS idx_commodore_creation_intents_ack_pending
    ON commodore.artifact_creation_intents(updated_at)
    WHERE command_ack_pending = TRUE;

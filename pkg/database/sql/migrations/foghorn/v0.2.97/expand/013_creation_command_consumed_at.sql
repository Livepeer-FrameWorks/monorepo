-- Durable consumer-ack column for foghorn.artifact_creation_commands. Commodore sets
-- consumed_at (via AckArtifactCreationCommand) once it has read a terminal
-- (committed/rejected) outcome and terminalized its intent. The retention GC deletes a
-- terminal row only when consumed_at IS NOT NULL and older than the retention horizon,
-- so a convergence outage longer than that horizon cannot erase an outcome a recovering
-- Commodore has not yet read (which would otherwise make the outcome read as MISSING and
-- trip the bounded abort). NULL while 'accepted' or while a terminal outcome is unconsumed.
--
-- Schema source of truth: pkg/database/sql/schema/foghorn.sql — same column in the
-- baseline so a fresh init and an upgrade converge.
ALTER TABLE foghorn.artifact_creation_commands
    ADD COLUMN IF NOT EXISTS consumed_at TIMESTAMP;

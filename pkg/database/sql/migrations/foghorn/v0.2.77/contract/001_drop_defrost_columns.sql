-- Phase 8 defrost purge — contract migration.
-- Drops the legacy defrost machinery (S3 -> local hydration). Replaced by
-- Helmsman's read-through relay: artifacts on S3 stream on demand via
-- Foghorn's RelayResolve, no bulk-copy step or tracker columns needed.
DROP INDEX IF EXISTS foghorn.idx_foghorn_artifacts_defrosting;

ALTER TABLE foghorn.artifacts
    DROP COLUMN IF EXISTS defrost_node_id,
    DROP COLUMN IF EXISTS defrost_started_at;

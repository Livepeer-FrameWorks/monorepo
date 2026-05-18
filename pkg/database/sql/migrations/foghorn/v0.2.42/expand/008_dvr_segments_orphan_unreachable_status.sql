-- Add status='orphan_unreachable' to foghorn.dvr_segments so the chapter
-- reclaim sweep can record "recording node gone past grace; Foghorn
-- presumes local file unrecoverable" without overloading deleted_local,
-- which means "Helmsman acknowledged the local delete via
-- DVRSegmentDropped(was_uploaded=true)". Phase B (S3 delete) and
-- startup reconcile interpret each state separately.
--
-- Schema source of truth: pkg/database/sql/schema/foghorn.sql

ALTER TABLE foghorn.dvr_segments
    DROP CONSTRAINT IF EXISTS chk_foghorn_dvr_segments_status;

-- NOT VALID skips existing-row validation; VALIDATE CONSTRAINT fires in
-- a postdeploy migration. The new value set is strictly broader.
ALTER TABLE foghorn.dvr_segments
    ADD CONSTRAINT chk_foghorn_dvr_segments_status CHECK (status IN (
        'pending', 'uploaded', 'failed_upload',
        'deleted_local', 'orphan_unreachable',
        'lost_local', 'reclaimed'
    )) NOT VALID;

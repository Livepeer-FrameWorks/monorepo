-- Allow status='reclaimed' on foghorn.dvr_segments so the chapter
-- reclaim sweep can mark TS segments deleted once every covering
-- chapter reaches state='frozen'. Without this the CHECK constraint
-- rejects the write and reclaim retries indefinitely.
--
-- Schema source of truth: pkg/database/sql/schema/foghorn.sql

ALTER TABLE foghorn.dvr_segments
    DROP CONSTRAINT IF EXISTS chk_foghorn_dvr_segments_status;

-- NOT VALID skips existing-row validation; VALIDATE CONSTRAINT fires
-- in a postdeploy migration. The new value set is strictly broader
-- than the old one, so validation is a trivial scan.
ALTER TABLE foghorn.dvr_segments
    ADD CONSTRAINT chk_foghorn_dvr_segments_status CHECK (status IN (
        'pending', 'uploaded', 'failed_upload', 'deleted_local', 'lost_local', 'reclaimed'
    )) NOT VALID;

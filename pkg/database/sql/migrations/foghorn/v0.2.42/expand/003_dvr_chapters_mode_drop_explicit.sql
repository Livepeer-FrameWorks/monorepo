-- Retire 'explicit_range' from dvr_chapters.mode. The GraphQL API no
-- longer exposes it; chapter mode is configured on commodore.streams
-- and snapshotted at StartDVR as window_sized_chapters or fixed_interval.
--
-- This migration is safe-by-design: there are no production chapter rows
-- (clean-slate refactor) so the CHECK constraint can shrink without a
-- data backfill. If any rows ever existed with mode='explicit_range',
-- they would need to be migrated to fixed_interval before this lands.
--
-- Schema source of truth: pkg/database/sql/schema/foghorn.sql

ALTER TABLE foghorn.dvr_chapters
    DROP CONSTRAINT IF EXISTS chk_foghorn_dvr_chapters_mode;

-- NOT VALID skips existing-row validation; VALIDATE CONSTRAINT fires
-- in a postdeploy migration. Per the clean-slate note above no rows
-- carry the retired 'explicit_range' value, so validation succeeds.
ALTER TABLE foghorn.dvr_chapters
    ADD CONSTRAINT chk_foghorn_dvr_chapters_mode CHECK (mode IN (
        'window_sized_chapters', 'fixed_interval'
    )) NOT VALID;

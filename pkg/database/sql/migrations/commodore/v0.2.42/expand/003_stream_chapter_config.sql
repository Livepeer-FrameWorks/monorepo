-- DVR chapter policy moves to Stream-level config, snapshotted onto
-- the DVR artifact at StartDVR. The previous mid-recording
-- SetDVRChapterPolicy mutation retires; viewers configure chapter mode
-- on the Stream itself and changes apply to the NEXT recording.
--
-- NULL mode = chapters disabled for this stream.
--
-- Schema source of truth: pkg/database/sql/schema/commodore.sql

ALTER TABLE commodore.streams
    ADD COLUMN IF NOT EXISTS dvr_chapter_mode             VARCHAR(32),
    ADD COLUMN IF NOT EXISTS dvr_chapter_interval_seconds INTEGER;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
         WHERE conname = 'chk_streams_chapter_mode'
    ) THEN
        -- NOT VALID skips existing-row validation in expand;
        -- VALIDATE CONSTRAINT fires in a postdeploy migration once
        -- the new binaries are everywhere. New writes are checked
        -- immediately regardless.
        ALTER TABLE commodore.streams
            ADD CONSTRAINT chk_streams_chapter_mode CHECK (
                dvr_chapter_mode IS NULL
                OR dvr_chapter_mode IN ('window_sized_chapters', 'fixed_interval')
            ) NOT VALID;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
         WHERE conname = 'chk_streams_chapter_interval'
    ) THEN
        ALTER TABLE commodore.streams
            ADD CONSTRAINT chk_streams_chapter_interval CHECK (
                dvr_chapter_mode IS DISTINCT FROM 'fixed_interval'
                OR dvr_chapter_interval_seconds > 0
            ) NOT VALID;
    END IF;
END$$;

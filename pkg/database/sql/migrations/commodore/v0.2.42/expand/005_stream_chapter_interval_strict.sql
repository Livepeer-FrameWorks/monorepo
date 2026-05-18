-- Tighten chk_streams_chapter_interval: the previous form returned
-- NULL (treated as "pass") when dvr_chapter_mode='fixed_interval' and
-- dvr_chapter_interval_seconds IS NULL, which let "fixed_interval + no
-- interval" land in the DB and silently disabled chapter creation in
-- the sweeper. The new form rejects NULL explicitly when
-- mode='fixed_interval'.
--
-- Schema source of truth: pkg/database/sql/schema/commodore.sql

ALTER TABLE commodore.streams
    DROP CONSTRAINT IF EXISTS chk_streams_chapter_interval;

-- NOT VALID skips existing-row validation; VALIDATE CONSTRAINT fires
-- in a postdeploy migration after any rows with the old loose state
-- are repaired. Per the chapter feature being clean-slate, no rows
-- should currently satisfy the old shape but fail the new one.
ALTER TABLE commodore.streams
    ADD CONSTRAINT chk_streams_chapter_interval CHECK (
        dvr_chapter_mode IS DISTINCT FROM 'fixed_interval'
        OR (dvr_chapter_interval_seconds IS NOT NULL
            AND dvr_chapter_interval_seconds > 0)
    ) NOT VALID;

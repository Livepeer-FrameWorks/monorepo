-- Tighten chk_streams_chapter_interval: GraphQL documents a minimum of
-- 3600s (1 hour) for fixed-interval chapter rotation, and the web form
-- enforces min=3600, but Commodore was accepting any positive value.
-- Sub-hour intervals can explode finalization-job count and storage
-- churn (every chapter becomes a separate MKV + .dtsh + thumbnail
-- bundle). Authority moves to Commodore + DB; the web form is just a
-- friendly UX layer over it.
--
-- Schema source of truth: pkg/database/sql/schema/commodore.sql

ALTER TABLE commodore.streams
    DROP CONSTRAINT IF EXISTS chk_streams_chapter_interval;

ALTER TABLE commodore.streams
    ADD CONSTRAINT chk_streams_chapter_interval CHECK (
        dvr_chapter_mode IS DISTINCT FROM 'fixed_interval'
        OR (dvr_chapter_interval_seconds IS NOT NULL
            AND dvr_chapter_interval_seconds >= 3600)
    ) NOT VALID;

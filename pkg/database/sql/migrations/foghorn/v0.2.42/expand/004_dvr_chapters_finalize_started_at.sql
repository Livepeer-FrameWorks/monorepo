-- Track when a chapter actually started finalizing so the stale-
-- finalizing requeue uses the dispatch time, not the chapter row's
-- created_at. For hours-long chapters, created_at is the boundary-
-- open time, which can be much older than the finalize dispatch and
-- would false-positive-requeue a healthy in-flight finalize.
--
-- Schema source of truth: pkg/database/sql/schema/foghorn.sql

ALTER TABLE foghorn.dvr_chapters
    ADD COLUMN IF NOT EXISTS finalize_started_at TIMESTAMPTZ;

-- Add ingest_mode='mist_native' and the stream-level always_on column.
-- Schema source of truth: pkg/database/sql/schema/commodore.sql

ALTER TABLE commodore.streams
    ADD COLUMN IF NOT EXISTS always_on BOOLEAN NOT NULL DEFAULT FALSE;

-- NOT VALID skips a full-table scan on apply; VALIDATE runs in postdeploy
-- after every binary that emits 'mist_native' has rolled forward.
ALTER TABLE commodore.streams
    DROP CONSTRAINT IF EXISTS streams_ingest_mode_chk;

ALTER TABLE commodore.streams
    ADD CONSTRAINT streams_ingest_mode_chk
        CHECK (ingest_mode IN ('push', 'pull', 'mist_native')) NOT VALID;

-- Partial index keeps the always_on lookup cheap; the column is FALSE for
-- the vast majority of streams.
CREATE INDEX IF NOT EXISTS idx_commodore_streams_always_on
    ON commodore.streams(ingest_mode) WHERE always_on = TRUE;

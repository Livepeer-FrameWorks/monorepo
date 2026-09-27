-- Mist event time of an ingest session's first playable STREAM_BUFFER. The
-- STREAM_END reaper orders the end event against it; sessions that became
-- playable before this release keep NULL and are ended by the exact
-- close or runtime-absence paths instead.
ALTER TABLE foghorn.ingest_sessions
    ADD COLUMN IF NOT EXISTS playable_at_unix_millis BIGINT;

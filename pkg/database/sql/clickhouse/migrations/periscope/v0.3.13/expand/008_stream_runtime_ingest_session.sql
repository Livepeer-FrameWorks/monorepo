-- The stream runtime ledger keys a push session's live rows (from
-- stream_state_current) and its final rows (from stream_sessions_final) by
-- Foghorn's ingest session id, so the final rows replace the live rows however
-- the session's start events arrived. stream_state_current takes it from the
-- lifecycle report or stream_start that wrote the row; stream_sessions_final
-- takes it from the stream_start of the session its STREAM_END ends. Rows
-- written before these columns, and sessions without one (pulled streams),
-- read as empty and keep the start-timestamp key.
ALTER TABLE periscope.stream_state_current
    ADD COLUMN IF NOT EXISTS ingest_session_id String DEFAULT '' AFTER updated_at;
ALTER TABLE periscope.stream_sessions_final
    ADD COLUMN IF NOT EXISTS ingest_session_id String DEFAULT '' AFTER ingest_id;

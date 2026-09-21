-- Public stream identity and first-playable time of an ingest session. stream_id
-- keys the stream.connected/live/idle domain events; playable_at is set once per
-- session by the owner node's first playable STREAM_BUFFER. Sessions admitted
-- before this release keep NULL stream_id and emit no stream lifecycle events.
ALTER TABLE foghorn.ingest_sessions
    ADD COLUMN IF NOT EXISTS stream_id UUID,
    ADD COLUMN IF NOT EXISTS playable_at TIMESTAMPTZ;

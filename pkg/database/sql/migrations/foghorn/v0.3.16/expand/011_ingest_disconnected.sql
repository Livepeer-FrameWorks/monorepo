ALTER TABLE foghorn.ingest_sessions ADD COLUMN IF NOT EXISTS ingest_disconnected_at TIMESTAMPTZ;
ALTER TABLE foghorn.ingest_sessions ADD COLUMN IF NOT EXISTS ingest_disconnect_requested_at TIMESTAMPTZ;

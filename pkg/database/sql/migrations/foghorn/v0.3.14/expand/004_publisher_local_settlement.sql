-- Local retirement drains independently of global lease forwarding and outages.
ALTER TABLE foghorn.ingest_admission_abandonments
    ADD COLUMN IF NOT EXISTS publisher_sessions_settled_at TIMESTAMPTZ;

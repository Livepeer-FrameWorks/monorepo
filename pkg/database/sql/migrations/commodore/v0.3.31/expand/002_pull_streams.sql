-- Pull-input streams: ingest_mode discriminator on commodore.streams + stream_pull_sources sidecar table.
-- Schema source of truth: pkg/database/sql/schema/commodore.sql

ALTER TABLE commodore.streams
    ADD COLUMN IF NOT EXISTS ingest_mode TEXT NOT NULL DEFAULT 'push';

CREATE TABLE IF NOT EXISTS commodore.stream_pull_sources (
    stream_id UUID PRIMARY KEY REFERENCES commodore.streams(id) ON DELETE CASCADE,
    source_uri_enc TEXT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_commodore_streams_ingest_mode
    ON commodore.streams(ingest_mode) WHERE ingest_mode <> 'push';

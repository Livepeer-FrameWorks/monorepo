-- Durable delivery of a deleted stream's thumbnail-cleanup obligation to Foghorn. Commodore owns the stream row
-- but not the thumbnail bytes; on DeleteStream it inserts ONE row here inside the SAME transaction that SOFT-deletes
-- the stream (sets deleted_at; the hard-delete is deferred to finalize), so the cleanup obligation is atomic with the
-- soft-delete (a rolled-back soft-delete records no obligation, a committed one always does). A background worker
-- drains it: it calls Foghorn's DeleteStreamThumbnails (which
-- durably records the tombstone + cleanup obligation on its side) and marks the row completed only on a positive
-- ack, retrying with backoff otherwise. Keyed by stream_id so a re-delete is idempotent.
-- Schema source of truth: pkg/database/sql/schema/commodore.sql.
CREATE TABLE IF NOT EXISTS commodore.stream_cleanup_outbox (
    stream_id       UUID PRIMARY KEY,                       -- deleted stream (asset_key); idempotent on re-delete
    tenant_id       UUID NOT NULL,                          -- ownership attribution the deletion was authorized for
    status          TEXT NOT NULL DEFAULT 'pending',        -- 'pending' | 'completed' (Foghorn acked the obligation)
    attempts        INT NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMP NOT NULL DEFAULT NOW(),       -- due time; bumped with exponential backoff on failure
    last_error      TEXT,
    created_at      TIMESTAMP NOT NULL DEFAULT NOW(),
    completed_at    TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_commodore_stream_cleanup_outbox_pending
    ON commodore.stream_cleanup_outbox(next_attempt_at)
    WHERE status = 'pending';

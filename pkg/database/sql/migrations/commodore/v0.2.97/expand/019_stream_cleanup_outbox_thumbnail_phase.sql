-- Durable per-phase progress for the two-phase stream-cleanup obligation. The obligation does multi-cell thumbnail
-- cleanup THEN the clip/DVR child cascade in one dispatch under a bounded item budget. Without a durable phase marker a
-- consistently slow-but-successful thumbnail cell re-consumes that budget on EVERY retry, so the child cascade never
-- gets enough of it and finalization is starved. Once the thumbnail phase acks, this stamp lets the worker SKIP it on
-- later ticks and give the whole budget to the children (which self-track: already-deleted children no longer
-- enumerate, so the cascade converges incrementally). Named for what it records — Foghorn ACKED the tombstone
-- obligation for every owning cell — NOT that the S3 bytes are already gone. NULL = phase not yet acked.
-- Schema source of truth: pkg/database/sql/schema/commodore.sql.
ALTER TABLE commodore.stream_cleanup_outbox
    ADD COLUMN IF NOT EXISTS thumbnail_cleanup_acked_at TIMESTAMP;

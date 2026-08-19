-- Durable, append-only set of every media cluster that has served this stream's live thumbnails (minted on that cell's
-- local Foghorn store). Its SOLE writer is RegisterStreamThumbnailServingCell — the service-fenced register-before-mint
-- call Foghorn makes before minting a live thumbnail — so a cell appears here iff it durably registered before creating
-- any object. active_ingest_cluster_id is NOT a writer (it names only the current ingest cell and is cleared when ingest
-- stops, so it cannot route a deletion for an offline stream). Never cleared, so the stream-cleanup outbox dispatches
-- the thumbnail-cleanup obligation to EVERY owning cell (each has its own per-cell Foghorn database) and finalizes
-- deletion only after all acknowledge. Empty = no registered owner (no thumbnail was ever minted).
-- Schema source of truth: pkg/database/sql/schema/commodore.sql.
ALTER TABLE commodore.streams
    ADD COLUMN IF NOT EXISTS thumbnail_serving_cluster_ids TEXT[] NOT NULL DEFAULT '{}';

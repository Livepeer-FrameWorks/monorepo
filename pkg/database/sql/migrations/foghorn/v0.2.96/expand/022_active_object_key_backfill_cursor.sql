-- Durable keyset cursor for the active_object_key backfill (jobs.ArtifactReconciler.backfillActiveObjectKey),
-- which converges the authoritative object pointer for LEGACY synced clip/vod rows that predate
-- version-addressing by parsing it prefix-aware from s3_url. Without a cursor, an ordered LIMIT scan restarts
-- at the head every pass, so any row that cannot advance (an anomalous locally-backed row whose s3_url does not
-- resolve to the local bucket, which the backfill deliberately skips) would starve every later row behind it.
-- This single-row cursor lets each pass process a bounded batch PAST the last hash and persist progress,
-- wrapping to the start on a short page.
--
-- Schema source of truth: pkg/database/sql/schema/foghorn.sql.
CREATE TABLE IF NOT EXISTS foghorn.active_object_key_backfill_cursor (
    id        BOOLEAN PRIMARY KEY DEFAULT true CHECK (id),  -- single-row guard
    last_hash TEXT NOT NULL DEFAULT ''
);
INSERT INTO foghorn.active_object_key_backfill_cursor (id) VALUES (true) ON CONFLICT (id) DO NOTHING;

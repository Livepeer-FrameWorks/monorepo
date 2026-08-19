-- Durable S3-copy lifecycle facts on the catalog rows, projected by the Foghorn artifact
-- reconciler (the single catalog writer) from its authoritative foghorn.artifacts row
-- (sync_status, dtsh_synced, storage_location) via UpdateArtifactCatalogSnapshot.
-- The reconciler drives this from a source-owned monotonic revision on foghorn.artifacts:
-- it projects every row whose catalog_revision exceeds a per-row watermark (oldest first) and
-- advances the watermark only on confirmed coverage, so EVERY authoritative row is eventually
-- covered (not just the newest batch) and a wiped Periscope projection no longer loses these
-- facts. catalog_revision stored here is that source revision, and it is also the monotonic
-- guard: the snapshot write applies only when the incoming revision is newer, so a stale or
-- non-authoritative write can't regress newer state. has_local_copy (transient local-copy presence)
-- stays a Periscope/node-copy signal and is NOT stored here.
--
-- Schema source of truth: pkg/database/sql/schema/commodore.sql — same columns in the
-- baseline so a fresh init and an upgrade converge.

ALTER TABLE commodore.clips
    ADD COLUMN IF NOT EXISTS sync_status VARCHAR(20),
    ADD COLUMN IF NOT EXISTS is_synced BOOLEAN,
    ADD COLUMN IF NOT EXISTS is_finalized BOOLEAN,
    ADD COLUMN IF NOT EXISTS storage_location VARCHAR(20),
    ADD COLUMN IF NOT EXISTS catalog_revision BIGINT;

ALTER TABLE commodore.dvr_recordings
    ADD COLUMN IF NOT EXISTS sync_status VARCHAR(20),
    ADD COLUMN IF NOT EXISTS is_synced BOOLEAN,
    ADD COLUMN IF NOT EXISTS is_finalized BOOLEAN,
    ADD COLUMN IF NOT EXISTS storage_location VARCHAR(20),
    ADD COLUMN IF NOT EXISTS catalog_revision BIGINT;

ALTER TABLE commodore.vod_assets
    ADD COLUMN IF NOT EXISTS sync_status VARCHAR(20),
    ADD COLUMN IF NOT EXISTS is_synced BOOLEAN,
    ADD COLUMN IF NOT EXISTS is_finalized BOOLEAN,
    ADD COLUMN IF NOT EXISTS storage_location VARCHAR(20),
    ADD COLUMN IF NOT EXISTS catalog_revision BIGINT;

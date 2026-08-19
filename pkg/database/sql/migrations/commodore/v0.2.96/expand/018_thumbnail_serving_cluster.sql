-- Authoritative thumbnail serving cluster, projected from Foghorn (foghorn.artifacts.thumbnail_serving_cluster_id via
-- the artifact catalog snapshot). It is the tenant's OFFICIAL durable cluster the thumbnail was projected to, which is
-- NOT necessarily storage_cluster_id (the artifact's byte-storage cluster) nor the origin cluster (provenance). Thumbnail
-- URL builders prefer it — COALESCE(thumbnail_serving_cluster_id, storage_cluster_id, origin_cluster_id) — so thumbnail
-- serving is DECOUPLED from the byte-storage cluster. Cross-cell artifact thumbnails are NOT published (a remote
-- official destination fails closed), so this stays NULL for a cross-cell artifact. NULL = legacy/not-yet-projected →
-- COALESCE falls back to storage/origin cluster.
ALTER TABLE commodore.clips
    ADD COLUMN IF NOT EXISTS thumbnail_serving_cluster_id VARCHAR(100);

ALTER TABLE commodore.dvr_recordings
    ADD COLUMN IF NOT EXISTS thumbnail_serving_cluster_id VARCHAR(100);

ALTER TABLE commodore.vod_assets
    ADD COLUMN IF NOT EXISTS thumbnail_serving_cluster_id VARCHAR(100);

-- Project artifact thumbnail binding into Commodore so list/get APIs can
-- answer thumbnail URLs without a runtime call to Foghorn.
--
-- storage_cluster_id mirrors foghorn.artifacts.storage_cluster_id; the
-- authoritative thumbnail cluster at SELECT time is
-- COALESCE(storage_cluster_id, origin_cluster_id).
--
-- has_thumbnails flips false→true via Commodore.MarkArtifactThumbnailsReady,
-- called from Foghorn's processThumbnailUploaded confirmation site (NOT the
-- mint-URL request site). storage_cluster_id updates via the separate
-- UpdateArtifactStorageCluster RPC, which never touches has_thumbnails.
--
-- Schema source of truth: pkg/database/sql/schema/commodore.sql

ALTER TABLE commodore.clips
    ADD COLUMN IF NOT EXISTS storage_cluster_id VARCHAR(100),
    ADD COLUMN IF NOT EXISTS has_thumbnails BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE commodore.dvr_recordings
    ADD COLUMN IF NOT EXISTS storage_cluster_id VARCHAR(100),
    ADD COLUMN IF NOT EXISTS has_thumbnails BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE commodore.vod_assets
    ADD COLUMN IF NOT EXISTS storage_cluster_id VARCHAR(100),
    ADD COLUMN IF NOT EXISTS has_thumbnails BOOLEAN NOT NULL DEFAULT FALSE;

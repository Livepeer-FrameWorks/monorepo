-- Add storage_cluster_id to foghorn.artifacts so storage ownership can diverge
-- from origin cluster. NULL means the storage owner equals origin_cluster_id;
-- a populated value names the cluster whose S3 actually holds the bytes (set
-- by freeze/thumbnail mints when the storage resolver picks a cluster other
-- than origin). No backfill: existing rows stay NULL and resolve to origin
-- via COALESCE on read.
--
-- Read sites use COALESCE(storage_cluster_id, origin_cluster_id) as the
-- authoritative cluster for Chandler URL selection, PrepareArtifact target
-- routing, and remote-synced delete decisions.

ALTER TABLE foghorn.artifacts
    ADD COLUMN IF NOT EXISTS storage_cluster_id VARCHAR(100);

CREATE INDEX IF NOT EXISTS idx_foghorn_artifacts_storage_cluster
    ON foghorn.artifacts(storage_cluster_id, sync_status)
    WHERE storage_cluster_id IS NOT NULL;

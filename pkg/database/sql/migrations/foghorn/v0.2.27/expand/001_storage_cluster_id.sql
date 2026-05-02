-- Add storage_cluster_id to foghorn.artifacts so storage ownership can diverge
-- from origin cluster. NULL = same as origin_cluster_id (preserves prior
-- semantics for every existing row; no backfill).
--
-- Read sites that consume this column use COALESCE(storage_cluster_id,
-- origin_cluster_id) as the authoritative cluster, so adding the column is a
-- no-op for behaviour until write paths start populating it in a later release.

ALTER TABLE foghorn.artifacts
    ADD COLUMN IF NOT EXISTS storage_cluster_id VARCHAR(100);

CREATE INDEX IF NOT EXISTS idx_foghorn_artifacts_storage_cluster
    ON foghorn.artifacts(storage_cluster_id, sync_status)
    WHERE storage_cluster_id IS NOT NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_artifacts_capture_projection
    ON foghorn.artifacts(origin_cluster_id,catalog_synced_rev,catalog_revision)
    WHERE artifact_type='dvr' AND catalog_revision>catalog_capture_times_synced_rev;

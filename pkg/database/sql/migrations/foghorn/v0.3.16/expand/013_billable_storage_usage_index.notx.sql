CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_artifacts_billable_storage
    ON foghorn.artifacts (tenant_id ASC, artifact_type ASC, artifact_hash ASC)
    INCLUDE (size_bytes, sync_status)
    WHERE tenant_id IS NOT NULL AND federated_pointer = false
      AND status <> 'deleted'
      AND (artifact_type <> 'vod' OR status NOT IN ('failed', 'expired', 'aborted'))
      AND durable_backend_local = true;

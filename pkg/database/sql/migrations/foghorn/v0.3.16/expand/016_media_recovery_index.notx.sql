-- Serves the media recovery sweep: its tenant cursor and each tenant's artifact page.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_artifacts_media_recovery
    ON foghorn.artifacts (tenant_id ASC, artifact_hash ASC)
    WHERE federated_pointer = false
      AND status IN ('completed', 'completed_partial', 'ready', 'failed');

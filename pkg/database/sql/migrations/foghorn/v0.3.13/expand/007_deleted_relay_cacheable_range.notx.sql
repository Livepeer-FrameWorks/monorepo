CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_artifacts_deleted_relay_cacheable_range
    ON foghorn.artifacts(updated_at ASC)
    WHERE status = 'deleted' AND artifact_type IN ('clip', 'vod', 'chapter');

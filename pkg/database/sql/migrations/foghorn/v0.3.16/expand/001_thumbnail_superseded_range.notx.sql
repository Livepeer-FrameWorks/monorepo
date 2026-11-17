CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_thumb_superseded_range
    ON foghorn.thumbnail_task_assignment(superseded_at ASC)
    INCLUDE (attempt_id, asset_key, version)
    WHERE status = 'published' AND superseded_at IS NOT NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_thumbnail_active_pointer_version
    ON foghorn.thumbnail_active_pointer(active_version);

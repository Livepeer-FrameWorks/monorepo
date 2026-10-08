CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_periscope_delegated_jwt_replays_expires_at_range
    ON periscope.delegated_jwt_replays (expires_at ASC);

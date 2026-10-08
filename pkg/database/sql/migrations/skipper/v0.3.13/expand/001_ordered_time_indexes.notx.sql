CREATE INDEX CONCURRENTLY IF NOT EXISTS skipper_usage_publish_pending_idx_range
    ON skipper.skipper_usage (created_at ASC)
    WHERE published_at IS NULL;

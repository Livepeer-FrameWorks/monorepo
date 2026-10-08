CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_notification_outbox_pending_range
    ON lookout.notification_outbox (next_attempt_at ASC, created_at)
    WHERE delivered_at IS NULL AND failed_at IS NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_notification_outbox_delivered_range
    ON lookout.notification_outbox (delivered_at ASC)
    WHERE delivered_at IS NOT NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_notification_outbox_failed_range
    ON lookout.notification_outbox (failed_at ASC)
    WHERE failed_at IS NOT NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_operator_activity_pending_range
    ON lookout.operator_activity_outbox (next_attempt_at ASC, created_at)
    WHERE delivered_at IS NULL AND failed_at IS NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_operator_activity_delivered_range
    ON lookout.operator_activity_outbox (delivered_at ASC)
    WHERE delivered_at IS NOT NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_operator_activity_failed_range
    ON lookout.operator_activity_outbox (failed_at ASC)
    WHERE failed_at IS NOT NULL;

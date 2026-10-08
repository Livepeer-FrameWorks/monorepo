CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_webhook_events_received_range
    ON bosun.webhook_events (received_at ASC);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_webhook_deliveries_due_range
    ON bosun.webhook_deliveries (next_attempt_at ASC, id)
    WHERE status = 'pending';

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_webhook_deliveries_test_created_range
    ON bosun.webhook_deliveries (created_at ASC)
    WHERE kind = 'test';

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_webhook_notification_outbox_pending_range
    ON bosun.webhook_notification_outbox (next_attempt_at ASC, id)
    WHERE completed_at IS NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_webhook_notification_outbox_completed_range
    ON bosun.webhook_notification_outbox (completed_at ASC)
    WHERE completed_at IS NOT NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_bosun_domain_event_outbox_pending_range
    ON bosun.domain_event_outbox (enqueued_at ASC, event_id)
    WHERE completed_at IS NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_bosun_domain_event_outbox_completed_range
    ON bosun.domain_event_outbox (completed_at ASC)
    WHERE completed_at IS NOT NULL;

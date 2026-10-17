CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_purser_billing_event_outbox_completed_range
    ON purser.billing_event_outbox (completed_at ASC)
    WHERE completed_at IS NOT NULL;

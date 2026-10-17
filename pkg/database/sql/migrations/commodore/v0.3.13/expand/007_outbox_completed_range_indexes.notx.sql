CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_commodore_service_event_outbox_completed_range
    ON commodore.service_event_outbox (completed_at ASC)
    WHERE completed_at IS NOT NULL;

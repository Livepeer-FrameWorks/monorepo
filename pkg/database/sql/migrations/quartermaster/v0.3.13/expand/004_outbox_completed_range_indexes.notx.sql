CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_qm_service_event_outbox_completed_range
    ON quartermaster.service_event_outbox (completed_at ASC)
    WHERE completed_at IS NOT NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_qm_navigator_custom_domain_outbox_completed_range
    ON quartermaster.navigator_custom_domain_outbox (completed_at ASC)
    WHERE completed_at IS NOT NULL;

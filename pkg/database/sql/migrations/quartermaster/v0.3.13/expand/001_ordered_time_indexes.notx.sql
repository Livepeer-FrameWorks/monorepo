CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_quartermaster_delegated_jwt_replays_expires_at_range
    ON quartermaster.delegated_jwt_replays (expires_at ASC);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_qm_service_event_outbox_pending_range
    ON quartermaster.service_event_outbox(created_at ASC)
    WHERE completed_at IS NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_quartermaster_domain_event_outbox_pending_range
    ON quartermaster.domain_event_outbox (enqueued_at ASC, event_id)
    WHERE completed_at IS NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_quartermaster_domain_event_outbox_completed_range
    ON quartermaster.domain_event_outbox (completed_at ASC)
    WHERE completed_at IS NOT NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_qm_navigator_custom_domain_outbox_pending_range
    ON quartermaster.navigator_custom_domain_outbox(created_at ASC)
    WHERE completed_at IS NULL;

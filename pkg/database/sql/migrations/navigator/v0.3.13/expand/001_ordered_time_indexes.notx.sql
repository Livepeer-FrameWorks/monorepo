CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_tls_bundles_expires_at_range ON navigator.tls_bundles(expires_at ASC);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_internal_certificates_expires_at_range ON navigator.internal_certificates(expires_at ASC);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_navigator_domain_event_outbox_pending_range
    ON navigator.domain_event_outbox (enqueued_at ASC, event_id)
    WHERE completed_at IS NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_navigator_domain_event_outbox_completed_range
    ON navigator.domain_event_outbox (completed_at ASC)
    WHERE completed_at IS NOT NULL;

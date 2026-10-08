CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_lookout_incidents_open_cluster
    ON lookout.incidents(cluster_id ASC, created_at ASC, id ASC)
    WHERE status <> 'resolved';

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_lookout_notification_outbox_incident_pending
    ON lookout.notification_outbox (incident_id, tenant_id)
    WHERE channel = 'kafka' AND delivered_at IS NULL AND failed_at IS NULL;

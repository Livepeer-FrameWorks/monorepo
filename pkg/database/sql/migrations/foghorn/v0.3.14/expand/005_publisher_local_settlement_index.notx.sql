CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_publisher_abandonments_sessions_unsettled
    ON foghorn.ingest_admission_abandonments(created_at ASC, node_id, start_trigger_uuid)
    WHERE publisher_sessions_settled_at IS NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_ingest_sessions_projected_node_covering
    ON foghorn.ingest_sessions(node_id ASC, started_at ASC)
    INCLUDE (tenant_id, stream_internal_name, ingest_cluster_id, start_trigger_uuid)
    WHERE ended_at IS NULL AND projection_state = 'active';

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_media_object_authority_publishing_credential_lookup
    ON foghorn.media_object_authority_projection(publishing_credential_sha256)
    WHERE publishing_credential_sha256 IS NOT NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_ingest_sessions_execution_identity
    ON foghorn.ingest_sessions(node_id, start_trigger_uuid)
    INCLUDE (tenant_id, stream_internal_name, ingest_cluster_id, ended_at, connector_pid, publisher_admission_version);

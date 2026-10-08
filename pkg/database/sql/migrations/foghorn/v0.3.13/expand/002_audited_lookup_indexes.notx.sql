CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_dvr_chapters_playback_artifact
    ON foghorn.dvr_chapters(playback_artifact_hash)
    WHERE playback_artifact_hash IS NOT NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_ingest_sessions_projected_node
    ON foghorn.ingest_sessions(node_id ASC, started_at ASC)
    WHERE ended_at IS NULL AND projection_state = 'active';

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_ingest_sessions_open_trigger
    ON foghorn.ingest_sessions(node_id, start_trigger_uuid)
    WHERE ended_at IS NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_ingest_sessions_stream_lookup
    ON foghorn.ingest_sessions(stream_internal_name);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_artifact_nodes_orphan_expiry
    ON foghorn.artifact_nodes (last_seen_at ASC)
    WHERE is_orphaned = true;

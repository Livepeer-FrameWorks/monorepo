CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_ingest_sessions_ended_range
    ON foghorn.ingest_sessions(ended_at ASC)
    WHERE ended_at IS NOT NULL;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_processing_jobs_terminal_range
    ON foghorn.processing_jobs(updated_at ASC)
    WHERE status IN ('completed', 'failed');
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_artifact_event_outbox_completed_range
    ON foghorn.artifact_event_outbox(completed_at ASC)
    WHERE completed_at IS NOT NULL;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_creation_commands_unconsumed_artifact
    ON foghorn.artifact_creation_commands(artifact_hash)
    WHERE consumed_at IS NULL;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_artifacts_missing_thumbnails_range
    ON foghorn.artifacts(created_at ASC)
    WHERE status = 'ready' AND artifact_type IN ('vod', 'clip') AND has_thumbnails IS NOT TRUE;

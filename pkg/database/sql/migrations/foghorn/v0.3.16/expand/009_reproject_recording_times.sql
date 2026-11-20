-- Capture times retain their own acknowledgement through mixed-version consumers.
ALTER TABLE foghorn.artifacts ADD COLUMN IF NOT EXISTS catalog_capture_times_synced_rev BIGINT NOT NULL DEFAULT 0;

DROP TRIGGER IF EXISTS artifact_catalog_revision_upd ON foghorn.artifacts;
CREATE TRIGGER artifact_catalog_revision_upd
    BEFORE UPDATE ON foghorn.artifacts
    FOR EACH ROW
    WHEN (
        OLD.catalog_synced_rev IS NOT DISTINCT FROM NEW.catalog_synced_rev
        AND (
            OLD.artifact_type IS DISTINCT FROM NEW.artifact_type
            OR OLD.status IS DISTINCT FROM NEW.status
            OR OLD.size_bytes IS DISTINCT FROM NEW.size_bytes
            OR OLD.duration_ms IS DISTINCT FROM NEW.duration_ms
            OR OLD.duration_seconds IS DISTINCT FROM NEW.duration_seconds
            OR OLD.tracks IS DISTINCT FROM NEW.tracks
            OR OLD.started_at IS DISTINCT FROM NEW.started_at
            OR OLD.ended_at IS DISTINCT FROM NEW.ended_at
            OR OLD.sync_status IS DISTINCT FROM NEW.sync_status
            OR OLD.dtsh_synced IS DISTINCT FROM NEW.dtsh_synced
            OR OLD.frozen_at IS DISTINCT FROM NEW.frozen_at
            OR OLD.storage_location IS DISTINCT FROM NEW.storage_location
            OR OLD.storage_cluster_id IS DISTINCT FROM NEW.storage_cluster_id
            OR OLD.has_thumbnails IS DISTINCT FROM NEW.has_thumbnails
            OR OLD.thumbnail_serving_cluster_id IS DISTINCT FROM NEW.thumbnail_serving_cluster_id
            OR OLD.retention_until IS DISTINCT FROM NEW.retention_until
            OR OLD.error_message IS DISTINCT FROM NEW.error_message
        )
    )
    EXECUTE FUNCTION foghorn.bump_artifact_catalog_revision();

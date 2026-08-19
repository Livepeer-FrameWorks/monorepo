-- Authoritative thumbnail serving cluster. A thumbnail is projected to the DETERMINISTIC served key on the tenant's
-- OFFICIAL durable cluster (resolveThumbnailStorageCluster), which is NOT necessarily where the artifact's own bytes
-- live (storage_cluster_id) nor its provenance (origin_cluster_id). Recording the winning assignment's
-- destination_cluster here — at projection settlement, alongside has_thumbnails so it rides the same catalog-revision
-- bump and projects together — gives the catalog an EVIDENCE-BASED fact for which Chandler serves the thumbnail,
-- DECOUPLED from the byte-storage cluster. Cross-cell artifact thumbnails are NOT published: a remote official
-- destination fails closed (StorageMintViaFederation), so a cross-cell artifact never reaches projection and this
-- column stays NULL for it. For a supported (same-cell) artifact this records that local official cluster. NULL =
-- legacy/not-yet-projected row; readers COALESCE back to storage/origin cluster.
ALTER TABLE foghorn.artifacts
    ADD COLUMN IF NOT EXISTS thumbnail_serving_cluster_id VARCHAR(100);

-- The catalog-projection revision trigger must ALSO fire when only thumbnail_serving_cluster_id changes — otherwise an
-- artifact already at has_thumbnails=true whose serving cluster is first stamped (or corrected) never bumps
-- catalog_revision, so Commodore keeps the wrong (or missing) Chandler hostname. Recreate the trigger with the field
-- added to its WHEN list (must stay byte-identical to pkg/database/sql/schema/foghorn.sql for baseline convergence).
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

-- Catalog-projection support on the authoritative Foghorn artifact row.
--   duration_ms        — precise clip/VOD duration (ms) from the processing output.
--   catalog_revision   — source-owned MONOTONIC revision, bumped from a sequence by a
--                        trigger on every catalog-relevant row change (distinct even for
--                        same-millisecond updates, so the Commodore revision guard never
--                        conflates two distinct authoritative states).
--   catalog_synced_rev — last revision the reconciler confirmed Commodore has covered.
-- The reconciler projects rows with catalog_revision > catalog_synced_rev (oldest first) and
-- advances the watermark only on confirmed coverage.
--
-- Schema source of truth: pkg/database/sql/schema/foghorn.sql.

ALTER TABLE foghorn.artifacts
    ADD COLUMN IF NOT EXISTS duration_ms BIGINT,
    ADD COLUMN IF NOT EXISTS catalog_revision BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS catalog_synced_rev BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS catalog_quarantined_rev BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS catalog_quarantine_error TEXT;

CREATE SEQUENCE IF NOT EXISTS foghorn.artifact_catalog_revision_seq AS BIGINT;

CREATE OR REPLACE FUNCTION foghorn.bump_artifact_catalog_revision() RETURNS trigger AS $$
BEGIN
    NEW.catalog_revision := nextval('foghorn.artifact_catalog_revision_seq');
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS artifact_catalog_revision_ins ON foghorn.artifacts;
CREATE TRIGGER artifact_catalog_revision_ins
    BEFORE INSERT ON foghorn.artifacts
    FOR EACH ROW EXECUTE FUNCTION foghorn.bump_artifact_catalog_revision();

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
            OR OLD.retention_until IS DISTINCT FROM NEW.retention_until
            OR OLD.error_message IS DISTINCT FROM NEW.error_message
        )
    )
    EXECUTE FUNCTION foghorn.bump_artifact_catalog_revision();

-- Existing rows keep catalog_revision = 0 (an expand migration must not bulk-rewrite data); the
-- Foghorn artifact reconciler assigns each a fresh revision in batches (backfillCatalogRevisions)
-- so it projects once. Deleted rows are seeded too so a prior deletion projects as a catalog
-- deletion. New rows get their revision from the INSERT trigger.

-- Ordered by catalog_synced_rev (projection age) so the reconciler serves least-recently-
-- projected rows first — a continuously-mutating cohort can't starve rows behind it.
CREATE INDEX IF NOT EXISTS idx_foghorn_artifacts_catalog_projection
    ON foghorn.artifacts(catalog_synced_rev, catalog_revision)
    WHERE catalog_revision > catalog_synced_rev;

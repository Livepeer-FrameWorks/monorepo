-- Durable-projection marker for the deterministic served key. A thumbnail attempt reaches 'published' at the pointer
-- CAS, but the object Chandler serves is a DETERMINISTIC key (thumbnails/{asset}/{file}) projected from the winner's
-- version key by a separate, non-transactional S3 copy. deterministic_projected_at stamps when that copy has landed:
-- has_thumbnails is exposed ONLY after it is set, so the API never advertises a thumbnail Chandler cannot serve, and
-- the recovery reconciler re-drives any 'published' attempt still NULL here (a crash between the CAS and the copy).
-- NULL = published-but-not-yet-projected.
ALTER TABLE foghorn.thumbnail_task_assignment
    ADD COLUMN IF NOT EXISTS deterministic_projected_at TIMESTAMPTZ;

-- Recovery scans published+unprojected attempts oldest-first; a partial index keeps that scan cheap as the vast
-- majority of published rows are projected (predicate excludes them).
CREATE INDEX IF NOT EXISTS idx_foghorn_thumb_unprojected
    ON foghorn.thumbnail_task_assignment(updated_at)
    WHERE status = 'published' AND deterministic_projected_at IS NULL;

-- Null out any stale finalize_node_id on non-finalizing rows (pre-existing data), then VALIDATE the lifecycle
-- CHECK added NOT VALID in v0.2.97/expand/026. Idempotent: the UPDATE is a no-op once clean, and VALIDATE is
-- safe to re-run.
UPDATE foghorn.dvr_chapters
   SET finalize_node_id = NULL
 WHERE finalize_node_id IS NOT NULL
   AND state <> 'finalizing';

ALTER TABLE foghorn.dvr_chapters
    VALIDATE CONSTRAINT chk_foghorn_dvr_chapters_finalize_node;

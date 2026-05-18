-- Retire the chapter-as-S3-manifest model. Chapters now resolve to a
-- finalized VOD artifact via dvr_chapters.playback_artifact_hash; the
-- S3 manifest path, sweeper materialization timestamps, and the
-- segment-count/has-gaps rebuild fields are replaced by the chapter
-- artifact's metadata + the finalization job's bookkeeping.
--
-- segment_count and has_gaps stay; they're populated by the chapter
-- finalization job at completion and consumed by chapter-list UIs.
--
-- Schema source of truth: pkg/database/sql/schema/foghorn.sql

ALTER TABLE foghorn.dvr_chapters
    DROP COLUMN IF EXISTS manifest_s3_key,
    DROP COLUMN IF EXISTS materialized_at,
    DROP COLUMN IF EXISTS last_rebuilt_at;

-- The unmaterialized-chapter index referenced manifest_s3_key and
-- becomes meaningless once the column is gone; drop alongside.
DROP INDEX IF EXISTS foghorn.idx_foghorn_dvr_chapters_unmaterialized;

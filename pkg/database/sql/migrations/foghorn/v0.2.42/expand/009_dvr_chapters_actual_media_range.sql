-- Add actual media start/end to foghorn.dvr_chapters. The existing
-- start_ms/end_ms columns are the scheduled interval bounds and feed
-- chapter_id derivation; they don't shift. But the chapter VOD's
-- actual media span is [first_owned_segment.media_start_ms,
-- last_owned_segment.media_end_ms), which can differ from the
-- scheduled bounds when chapter boundaries don't align to segment
-- boundaries (e.g., the first chapter of a fixed_interval recording
-- that started mid-bucket). Populated by MarkChapterFinalized.
-- Players use these to anchor video.currentTime to wall-clock without
-- drift; GraphQL exposes them alongside the scheduled bounds.
--
-- Schema source of truth: pkg/database/sql/schema/foghorn.sql

ALTER TABLE foghorn.dvr_chapters
    ADD COLUMN IF NOT EXISTS actual_media_start_ms BIGINT,
    ADD COLUMN IF NOT EXISTS actual_media_end_ms   BIGINT;

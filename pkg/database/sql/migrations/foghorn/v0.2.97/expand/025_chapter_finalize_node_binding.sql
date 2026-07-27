-- Bind a chapter-finalize dispatch to the node it was sent to. Chapter-finalize jobs are routed around
-- foghorn.processing_jobs (their job_id is "chapter-finalize-<chapter_id>" with no processing_jobs row), so
-- without this column the result/progress handlers have no way to prove the reporting node received the job.
-- Persisted at MarkChapterFinalizing (before the node can report) and verified in every finalize transition.
ALTER TABLE foghorn.dvr_chapters
    ADD COLUMN IF NOT EXISTS finalize_node_id VARCHAR(100);

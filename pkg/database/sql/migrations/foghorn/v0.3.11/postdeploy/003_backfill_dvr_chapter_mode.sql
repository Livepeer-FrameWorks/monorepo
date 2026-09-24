-- Recordings started without a chapter mode kept no replayable chapters.
-- Running and finalized recordings take window-sized chapters: the chapter
-- sweeper opens chapters for running ones and, for finalized ones whose
-- segments remain, materializes the terminal chapter set because their
-- dvr_chapter_backfill_complete is still false. The interval applies only to
-- fixed_interval.
UPDATE foghorn.artifacts
SET dvr_chapter_mode = 'window_sized_chapters',
    dvr_chapter_interval = NULL,
    dvr_chapter_backfill_complete = false
WHERE artifact_type = 'dvr'
  AND (dvr_chapter_mode IS NULL OR dvr_chapter_mode = '')
  AND status IN ('requested', 'starting', 'recording', 'stopping', 'finalizing',
                 'completed', 'completed_partial', 'ready');

-- Streams without a chapter mode saved nothing after a broadcast ended. They
-- move to window-sized chapters, the default for new streams. The interval
-- column is meaningful only for fixed_interval, so any other row drops it.
UPDATE commodore.streams
SET dvr_chapter_mode = 'window_sized_chapters'
WHERE dvr_chapter_mode IS NULL OR dvr_chapter_mode = '';

UPDATE commodore.streams
SET dvr_chapter_interval_seconds = NULL
WHERE dvr_chapter_interval_seconds IS NOT NULL
  AND dvr_chapter_mode IS DISTINCT FROM 'fixed_interval';

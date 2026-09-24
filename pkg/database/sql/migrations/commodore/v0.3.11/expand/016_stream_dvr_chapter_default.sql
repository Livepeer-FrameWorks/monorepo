-- New streams save their recordings as window-sized chapters unless the
-- stream selects fixed_interval or NONE (stored as NULL).
ALTER TABLE commodore.streams
    ALTER COLUMN dvr_chapter_mode SET DEFAULT 'window_sized_chapters';

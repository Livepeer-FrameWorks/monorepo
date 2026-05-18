-- Validate the chapter-config CHECK constraints added NOT VALID in
-- expand. Every row written so far either keeps dvr_chapter_mode NULL
-- (cleanly allowed) or already conforms to the new value set, so
-- validation is a trivial scan under SHARE lock. New writes have been
-- enforced since expand ran.

ALTER TABLE commodore.streams
    VALIDATE CONSTRAINT chk_streams_chapter_mode;

ALTER TABLE commodore.streams
    VALIDATE CONSTRAINT chk_streams_chapter_interval;

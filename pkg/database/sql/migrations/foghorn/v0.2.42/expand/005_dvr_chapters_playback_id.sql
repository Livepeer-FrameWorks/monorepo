-- Cache the Commodore-minted public playback_id on the chapter row so
-- chapter list resolvers don't fan out to Commodore per-row. The
-- authoritative mapping lives in commodore.dvr_chapter_playback; this
-- column is a derived cache populated at finalization dispatch.

ALTER TABLE foghorn.dvr_chapters
    ADD COLUMN IF NOT EXISTS playback_id VARCHAR(32);

CREATE INDEX IF NOT EXISTS idx_foghorn_dvr_chapters_playback_id
    ON foghorn.dvr_chapters(playback_id)
    WHERE playback_id IS NOT NULL;

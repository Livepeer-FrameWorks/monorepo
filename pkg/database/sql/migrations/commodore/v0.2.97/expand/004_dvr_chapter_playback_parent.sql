-- Links a chapter playback row to its parent DVR so DeleteDVR can cascade the chapter catalog
-- (vod_assets + dvr_chapter_playback) by parent, atomically and idempotently on retry.
-- dvr_hash is NULLABLE: it is set by MintChapterPlaybackID, but Commodore holds no parent link for a
-- chapter mapping that lacks it (the link lives in Foghorn's dvr_chapters), so a NULL row is instead
-- cascaded media-plane-side — Foghorn's reconciler (RepairDeletedDVRChildrenBatch) projects each
-- still-live child's OWN deletion keyed by the child hash, never needing dvr_hash here.
ALTER TABLE commodore.dvr_chapter_playback
    ADD COLUMN IF NOT EXISTS dvr_hash VARCHAR(32);

CREATE INDEX IF NOT EXISTS idx_commodore_dvr_chapter_playback_dvr
    ON commodore.dvr_chapter_playback(dvr_hash) WHERE dvr_hash IS NOT NULL;

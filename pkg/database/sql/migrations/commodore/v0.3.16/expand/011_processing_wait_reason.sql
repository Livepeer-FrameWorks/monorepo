ALTER TABLE commodore.clips ADD COLUMN IF NOT EXISTS processing_wait_reason TEXT;
ALTER TABLE commodore.vod_assets ADD COLUMN IF NOT EXISTS processing_wait_reason TEXT;
ALTER TABLE commodore.dvr_recordings ADD COLUMN IF NOT EXISTS processing_wait_reason TEXT;

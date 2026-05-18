ALTER TABLE commodore.clips
    ADD COLUMN IF NOT EXISTS size_bytes BIGINT;

ALTER TABLE commodore.dvr_recordings
    ADD COLUMN IF NOT EXISTS size_bytes BIGINT;

CREATE INDEX IF NOT EXISTS idx_commodore_clips_size
    ON commodore.clips(tenant_id, size_bytes)
    WHERE size_bytes IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_commodore_dvr_recordings_size
    ON commodore.dvr_recordings(tenant_id, size_bytes)
    WHERE size_bytes IS NOT NULL;

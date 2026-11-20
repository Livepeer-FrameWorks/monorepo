ALTER TABLE commodore.vod_assets
    ADD COLUMN IF NOT EXISTS transcode_ladder VARCHAR(64) NOT NULL DEFAULT '';

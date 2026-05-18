ALTER TABLE commodore.vod_assets
    ADD COLUMN IF NOT EXISTS stream_id UUID REFERENCES commodore.streams(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS library_visible BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN IF NOT EXISTS origin_type VARCHAR(32),
    ADD COLUMN IF NOT EXISTS origin_id VARCHAR(64);

CREATE INDEX IF NOT EXISTS idx_commodore_vod_stream
    ON commodore.vod_assets(stream_id)
    WHERE stream_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_commodore_vod_origin
    ON commodore.vod_assets(origin_type, origin_id)
    WHERE origin_type IS NOT NULL;

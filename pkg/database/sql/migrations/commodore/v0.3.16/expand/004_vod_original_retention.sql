ALTER TABLE commodore.tenant_media_retention_policies
    ADD COLUMN IF NOT EXISTS keep_vod_originals BOOLEAN;
ALTER TABLE commodore.vod_assets
    ADD COLUMN IF NOT EXISTS original_size_bytes BIGINT;

CREATE TABLE IF NOT EXISTS commodore.vod_original_download_limits (
    tenant_id UUID PRIMARY KEY,
    next_grant_at TIMESTAMPTZ NOT NULL
);

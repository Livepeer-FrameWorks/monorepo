ALTER TABLE foghorn.vod_metadata
    ADD COLUMN IF NOT EXISTS keep_original BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS original_s3_key TEXT,
    ADD COLUMN IF NOT EXISTS original_size_bytes BIGINT;
ALTER TABLE foghorn.vod_metadata DROP CONSTRAINT IF EXISTS chk_vod_original_retention;
ALTER TABLE foghorn.vod_metadata ADD CONSTRAINT chk_vod_original_retention CHECK (
    (original_s3_key IS NULL AND original_size_bytes IS NULL) OR
    (keep_original AND original_s3_key IS NOT NULL AND original_s3_key <> ''
     AND original_size_bytes IS NOT NULL AND original_size_bytes > 0)
) NOT VALID;

-- Tenant-supplied http(s) source of a VOD imported from a URL; NULL for uploads.
-- The processing node's Helmsman relay fetches it in place of an uploaded S3
-- object.
ALTER TABLE foghorn.vod_metadata ADD COLUMN IF NOT EXISTS source_url TEXT;

-- Server-derived canonical S3 object key bound to a freeze attempt. The presigned PUT is minted for
-- this exact key when the attempt is claimed, and completion promotes the SAME key
-- (s3_url = BuildS3URL(sync_object_key)) — so a transient catalog read can never let the bytes upload
-- to one object while a different object is recorded synced. Retained after completion so the
-- incremental .dtsh sync derives its sidecar key (sync_object_key + '.dtsh') from the same descriptor.
--
-- Schema source of truth: pkg/database/sql/schema/foghorn.sql — same column in the baseline so a
-- fresh init and an upgrade converge.
ALTER TABLE foghorn.artifacts ADD COLUMN IF NOT EXISTS sync_object_key TEXT;

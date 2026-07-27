-- The IMMUTABLE, attempt-versioned key of the current durable object. Completion publishes by promoting the
-- verified staging object to a fresh candidate key and FLIPPING this pointer in the same transaction, so a
-- promote never overwrites a served object and a rollback cannot expose uncommitted bytes. This is the
-- authoritative read/delete address; s3_url and vod_metadata.s3_key are kept in sync with it. NULL for
-- legacy rows (readers fall back to s3_url / sync_object_key).
--
-- Schema source of truth: pkg/database/sql/schema/foghorn.sql.
ALTER TABLE foghorn.artifacts
    ADD COLUMN IF NOT EXISTS active_object_key TEXT;
-- The .dtsh index is version-addressed too (control.FreezePublishDtshKey), so a late/duplicate attempt can
-- never overwrite the live index. Reads use it; legacy rows fall back to the co-located <active_object_key>.dtsh.
ALTER TABLE foghorn.artifacts
    ADD COLUMN IF NOT EXISTS active_dtsh_key TEXT;

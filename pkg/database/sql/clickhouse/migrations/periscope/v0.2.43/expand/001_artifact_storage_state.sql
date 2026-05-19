-- Add explicit hot/synced/finalized/frozen storage state to the current
-- artifact projection. StorageLifecycle events maintain these columns; media
-- lifecycle events keep writing playback/progress fields.
ALTER TABLE artifact_state_current
    ADD COLUMN IF NOT EXISTS storage_location Nullable(String) AFTER expires_at,
    ADD COLUMN IF NOT EXISTS sync_status Nullable(String) AFTER storage_location,
    ADD COLUMN IF NOT EXISTS is_hot Nullable(Bool) AFTER sync_status,
    ADD COLUMN IF NOT EXISTS is_synced Nullable(Bool) AFTER is_hot,
    ADD COLUMN IF NOT EXISTS is_finalized Nullable(Bool) AFTER is_synced,
    ADD COLUMN IF NOT EXISTS is_frozen Nullable(Bool) AFTER is_finalized;

-- Backfill the thumbnail backend-evidence column (expand/030) for rows that predate it. Every EXISTING thumbnail
-- assignment is a LOCAL mint: the mint (processThumbnailUploadRequest) has always dropped StorageUnavailable +
-- StorageMintViaFederation BEFORE it claims, so no federated thumbnail attempt was ever created. Historical rows
-- are therefore known-local, not unknown-defaulted-remote — this one-shot UPDATE corrects the ADD COLUMN default so
-- cleanup routes them locally (a false value would misroute a locally-backed official alias to disabled federation
-- and leak). New rows are inserted true by ClaimThumbnailAttempt. Idempotent (re-run touches nothing).
UPDATE foghorn.thumbnail_task_assignment SET durable_backend_local = true WHERE durable_backend_local = false;

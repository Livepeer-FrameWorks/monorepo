-- Capture the deterministic fingerprint (BackendFingerprint) of the immutable local store an artifact's own bytes
-- (clip/dvr/vod) were written to. Recorded when storage is assigned — at upload/artifact CREATION, from this cell's
-- local backend fingerprint (a computed snapshot; no registry table). Under the one-immutable-backend-per-cell model,
-- cleanup and the multipart-ownership fence compare THIS recorded id to the cell's current store and fail closed on a
-- mismatch or an absent id, so no operation ever targets or completes on the wrong backend.
-- Nullable at the column level so it can be added online; in-flight rows that predate this cut are adopted from the
-- proven cell identity at Foghorn boot (AdoptOrEnforceLocalBackend), NOT left to route by current placement.
ALTER TABLE foghorn.artifacts
    ADD COLUMN IF NOT EXISTS backend_id TEXT;

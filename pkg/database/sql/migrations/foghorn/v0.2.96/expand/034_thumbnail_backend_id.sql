-- Capture the deterministic fingerprint (BackendFingerprint over kind/bucket/endpoint/region/prefix) of the immutable
-- local store each thumbnail attempt's objects were written to, recorded at claim (a computed snapshot; no registry
-- table). Under the one-immutable-backend-per-cell model cleanup later compares THIS recorded id to the cell's current
-- store and fails closed on a mismatch (rather than resolving an arbitrary backend's adapter), so it never targets the
-- wrong backend.
ALTER TABLE foghorn.thumbnail_task_assignment
    ADD COLUMN IF NOT EXISTS backend_id TEXT;

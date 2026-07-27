-- STABLE billing attribution: whether an artifact's durable bytes live on THIS cell's local S3 backend is
-- captured at WRITE time and read directly, never recomputed from mutable tenant routing (access can expire
-- or a tenant can reconfigure BYOC while the bytes remain in this bucket — the old read-time resolver dropped
-- exactly those from billing). Set true where a local mint is claimed/completed or a VOD lands on local S3;
-- false for playback-federation adopted-remote rows. The backfill of pre-existing rows runs in postdeploy
-- (bulk rewrites are not expand-safe).
--
-- Schema source of truth: pkg/database/sql/schema/foghorn.sql.
ALTER TABLE foghorn.artifacts
    ADD COLUMN IF NOT EXISTS durable_backend_local BOOLEAN NOT NULL DEFAULT false;

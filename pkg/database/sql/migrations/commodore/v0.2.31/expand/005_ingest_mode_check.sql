-- Constrain commodore.streams.ingest_mode to the known set. Code in
-- api_control/internal/bootstrap/pull_streams.go and api_balancing/internal/control/playback.go
-- compares this column against string literals; an unconstrained TEXT column
-- lets a typo land silently and break routing/auth assumptions.
-- Schema source of truth: pkg/database/sql/schema/commodore.sql
--
-- Expand-safe shape: NOT VALID lets old binaries continue to write existing
-- rows without a full table scan. The VALIDATE step lives in the postdeploy
-- phase (005_ingest_mode_check.sql) so it runs after binaries roll forward.

ALTER TABLE commodore.streams
    DROP CONSTRAINT IF EXISTS streams_ingest_mode_chk;

ALTER TABLE commodore.streams
    ADD CONSTRAINT streams_ingest_mode_chk CHECK (ingest_mode IN ('push', 'pull')) NOT VALID;

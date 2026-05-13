-- Per-pull-source placement pin: allowed_cluster_ids on stream_pull_sources.
-- Schema source of truth: pkg/database/sql/schema/commodore.sql
--
-- Existing rows land with the empty default, which is correct for public
-- pull sources (unchanged behavior) and fails closed for private/multicast
-- pull sources (must be re-declared with explicit allowed_cluster_ids
-- before v0.2.33 binaries roll to edges). See release notes.

ALTER TABLE commodore.stream_pull_sources
    ADD COLUMN IF NOT EXISTS allowed_cluster_ids TEXT[] NOT NULL DEFAULT '{}';

-- Schema source of truth: pkg/database/sql/schema/quartermaster.sql
-- Capacity limits are optional metadata, not default platform policy.
ALTER TABLE quartermaster.infrastructure_clusters
    ALTER COLUMN max_concurrent_streams SET DEFAULT 0,
    ALTER COLUMN max_concurrent_viewers SET DEFAULT 0,
    ALTER COLUMN max_bandwidth_mbps SET DEFAULT 0;

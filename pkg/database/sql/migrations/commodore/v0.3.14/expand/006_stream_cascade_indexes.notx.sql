CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_commodore_push_targets_stream_fk
ON commodore.push_targets(stream_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_commodore_policy_bundle_versions_stream_fk
ON commodore.policy_bundle_versions(stream_id);

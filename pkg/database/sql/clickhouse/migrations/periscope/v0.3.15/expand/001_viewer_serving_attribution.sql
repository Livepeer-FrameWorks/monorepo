-- Trusted connect-only serving identity is retained for the exact lifecycle,
-- independently of raw-event retention and disconnect/QoE projections.
CREATE TABLE IF NOT EXISTS periscope.viewer_lifecycle_serving_attribution (
    tenant_id UUID,
    stream_id UUID,
    node_id String,
    session_id String,
    lifecycle_connected_at_ms Int64,
    serving_clusters AggregateFunction(groupUniqArray, String)
) ENGINE = ReplicatedAggregatingMergeTree()
ORDER BY (tenant_id, stream_id, node_id, session_id, lifecycle_connected_at_ms);

CREATE MATERIALIZED VIEW IF NOT EXISTS periscope.viewer_lifecycle_serving_attribution_mv
TO periscope.viewer_lifecycle_serving_attribution AS
SELECT tenant_id, stream_id, node_id, session_id,
       source_timestamp_ms AS lifecycle_connected_at_ms,
       groupUniqArrayState(CAST(cluster_id AS String)) AS serving_clusters
FROM periscope.viewer_connection_events
WHERE event_type = 'connect' AND session_id != '' AND cluster_id != ''
GROUP BY tenant_id, stream_id, node_id, session_id, lifecycle_connected_at_ms;


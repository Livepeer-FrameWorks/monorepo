ALTER TABLE periscope.viewer_connection_events
    ADD COLUMN IF NOT EXISTS control_cell_id LowCardinality(String) DEFAULT '' AFTER origin_cluster_id;

-- Current placement must come from the authenticated media event. Joining a
-- content-tenant session to infrastructure inventory by node_id can fan one
-- workload out across unrelated tenant namespaces or stale cluster rows.
ALTER TABLE periscope.stream_state_current
    ADD COLUMN IF NOT EXISTS cluster_id LowCardinality(String) DEFAULT '' AFTER node_id;

ALTER TABLE periscope.viewer_sessions_current
    ADD COLUMN IF NOT EXISTS cluster_id SimpleAggregateFunction(any, LowCardinality(String)) AFTER node_id;

ALTER TABLE periscope.viewer_sessions_connect_mv MODIFY QUERY
SELECT
    tenant_id, stream_id, internal_name, session_id, node_id, cluster_id,
    timestamp AS connected_at,
    CAST(NULL AS Nullable(DateTime)) AS disconnected_at,
    connector, country_code, city, latitude, longitude,
    bytes_transferred, session_duration, timestamp AS last_updated
FROM periscope.viewer_connection_events
WHERE event_type = 'connect' AND session_id != '';

ALTER TABLE periscope.viewer_sessions_disconnect_mv MODIFY QUERY
SELECT
    tenant_id, stream_id, internal_name, session_id, node_id, cluster_id,
    CAST(NULL AS Nullable(DateTime)) AS connected_at,
    timestamp AS disconnected_at,
    connector, country_code, city, latitude, longitude,
    bytes_transferred, session_duration, timestamp AS last_updated
FROM periscope.viewer_connection_events
WHERE event_type = 'disconnect' AND session_id != '';

ALTER TABLE periscope.routing_decisions
    ADD COLUMN IF NOT EXISTS selected_cluster_id LowCardinality(String) DEFAULT '' AFTER remote_cluster_id;
ALTER TABLE periscope.routing_decisions
    ADD COLUMN IF NOT EXISTS control_cell_id LowCardinality(String) DEFAULT '' AFTER selected_cluster_id;
ALTER TABLE periscope.routing_decisions
    ADD COLUMN IF NOT EXISTS origin_cluster_id LowCardinality(String) DEFAULT '' AFTER control_cell_id;
ALTER TABLE periscope.routing_decisions
    ADD INDEX IF NOT EXISTS idx_routing_stream_tenant stream_tenant_id TYPE bloom_filter(0.01) GRANULARITY 1;
ALTER TABLE periscope.routing_decisions
    MATERIALIZE INDEX idx_routing_stream_tenant;

ALTER TABLE periscope.processing_events
    ADD COLUMN IF NOT EXISTS control_cell_id LowCardinality(String) DEFAULT '' AFTER origin_cluster_id;

ALTER TABLE periscope.viewer_sessions_final
    ADD COLUMN IF NOT EXISTS origin_cluster_id LowCardinality(String) DEFAULT '' AFTER cluster_id;
ALTER TABLE periscope.viewer_sessions_final
    ADD COLUMN IF NOT EXISTS control_cell_id LowCardinality(String) DEFAULT '' AFTER origin_cluster_id;

ALTER TABLE periscope.stream_sessions_final
    ADD COLUMN IF NOT EXISTS origin_cluster_id LowCardinality(String) DEFAULT '' AFTER cluster_id;
ALTER TABLE periscope.stream_sessions_final
    ADD COLUMN IF NOT EXISTS control_cell_id LowCardinality(String) DEFAULT '' AFTER origin_cluster_id;

ALTER TABLE periscope.processing_segments_final
    ADD COLUMN IF NOT EXISTS origin_cluster_id LowCardinality(String) DEFAULT '' AFTER cluster_id;
ALTER TABLE periscope.processing_segments_final
    ADD COLUMN IF NOT EXISTS control_cell_id LowCardinality(String) DEFAULT '' AFTER origin_cluster_id;

ALTER TABLE periscope.federation_events
    ADD COLUMN IF NOT EXISTS stream_tenant_id Nullable(UUID) AFTER tenant_id;
ALTER TABLE periscope.federation_events
    ADD COLUMN IF NOT EXISTS control_cell_id LowCardinality(String) DEFAULT '' AFTER stream_origin_cluster_id;
ALTER TABLE periscope.federation_events
    ADD INDEX IF NOT EXISTS idx_federation_stream_tenant stream_tenant_id TYPE bloom_filter(0.01) GRANULARITY 1;
ALTER TABLE periscope.federation_events
    MATERIALIZE INDEX idx_federation_stream_tenant;

ALTER TABLE periscope.storage_events
    ADD COLUMN IF NOT EXISTS control_cell_id LowCardinality(String) DEFAULT '' AFTER origin_cluster_id;

-- The original rollup remains the complete operator-history source. A separate
-- table keeps the expand migration additive and adds content-owner attribution
-- from this deployment onward. Readers union only cross-tenant content rows from
-- this table so the two active materialized views cannot double-count events.
CREATE TABLE IF NOT EXISTS periscope.federation_hourly_v2 (
    hour DateTime,
    tenant_id UUID,
    content_tenant_id UUID DEFAULT toUUIDOrZero(''),
    local_cluster LowCardinality(String),
    remote_cluster LowCardinality(String),
    event_type LowCardinality(String),
    event_count UInt32,
    sum_latency_ms Float32,
    sum_time_to_live_ms Float32,
    failure_count UInt32
) ENGINE = ReplicatedSummingMergeTree((event_count, sum_latency_ms, sum_time_to_live_ms, failure_count))
PARTITION BY toYYYYMM(hour)
ORDER BY (hour, tenant_id, content_tenant_id, local_cluster, remote_cluster, event_type)
TTL hour + INTERVAL 365 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS periscope.federation_hourly_v2_mv TO periscope.federation_hourly_v2 AS
SELECT
    toStartOfHour(timestamp) AS hour,
    tenant_id,
    ifNull(stream_tenant_id, toUUIDOrZero('')) AS content_tenant_id,
    local_cluster,
    remote_cluster,
    event_type,
    count() AS event_count,
    sum(ifNull(latency_ms, 0)) AS sum_latency_ms,
    sum(ifNull(time_to_live_ms, 0)) AS sum_time_to_live_ms,
    countIf(event_type LIKE '%failed' OR (failure_reason != '' AND failure_reason IS NOT NULL)) AS failure_count
FROM periscope.federation_events
GROUP BY hour, tenant_id, content_tenant_id, local_cluster, remote_cluster, event_type;

-- Additive placement views keep existing billing/read views stable during the
-- expand phase while exposing the new identity dimensions immediately.
CREATE VIEW IF NOT EXISTS periscope.viewer_sessions_topology_v AS
SELECT f.*, p.origin_cluster_id, p.control_cell_id
FROM periscope.viewer_sessions_final_v AS f
LEFT JOIN
(
    SELECT tenant_id, node_id, session_id,
           argMax(origin_cluster_id, projection_version_ms) AS origin_cluster_id,
           argMax(control_cell_id, projection_version_ms) AS control_cell_id
    FROM periscope.viewer_sessions_final
    GROUP BY tenant_id, node_id, session_id
) AS p USING (tenant_id, node_id, session_id);

CREATE VIEW IF NOT EXISTS periscope.stream_sessions_topology_v AS
SELECT f.*, p.origin_cluster_id, p.control_cell_id
FROM periscope.stream_sessions_final_v AS f
LEFT JOIN
(
    SELECT tenant_id, node_id, stream_id, source_event_id,
           argMax(origin_cluster_id, projection_version_ms) AS origin_cluster_id,
           argMax(control_cell_id, projection_version_ms) AS control_cell_id
    FROM periscope.stream_sessions_final
    GROUP BY tenant_id, node_id, stream_id, source_event_id
) AS p USING (tenant_id, node_id, stream_id, source_event_id);

CREATE VIEW IF NOT EXISTS periscope.processing_segments_topology_v AS
SELECT f.*, p.origin_cluster_id, p.control_cell_id
FROM periscope.processing_segments_final_v AS f
LEFT JOIN
(
    SELECT tenant_id, node_id, stream_id, source_event_id,
           argMax(origin_cluster_id, projection_version_ms) AS origin_cluster_id,
           argMax(control_cell_id, projection_version_ms) AS control_cell_id
    FROM periscope.processing_segments_final
    GROUP BY tenant_id, node_id, stream_id, source_event_id
) AS p USING (tenant_id, node_id, stream_id, source_event_id);

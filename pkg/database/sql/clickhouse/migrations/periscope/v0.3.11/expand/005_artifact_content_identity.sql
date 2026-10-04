-- Non-live content (uploaded VOD, clip, DVR, DVR chapter) is identified by its
-- artifact_hash; stream_id on such rows only names the source stream, and an
-- uploaded VOD has none. Viewer, routing and client QoE events carry both.
ALTER TABLE periscope.viewer_connection_events
    ADD COLUMN IF NOT EXISTS artifact_hash String DEFAULT '' AFTER stream_id;

ALTER TABLE periscope.viewer_sessions_current
    ADD COLUMN IF NOT EXISTS artifact_hash SimpleAggregateFunction(any, String) AFTER stream_id;

ALTER TABLE periscope.viewer_sessions_connect_mv MODIFY QUERY
SELECT
    tenant_id,
    stream_id,
    artifact_hash,
    internal_name,
    session_id,
    node_id,
    cluster_id,
    timestamp AS connected_at,
    CAST(NULL AS Nullable(DateTime)) AS disconnected_at,
    connector,
    country_code,
    city,
    latitude,
    longitude,
    bytes_transferred,
    session_duration,
    timestamp AS last_updated
FROM periscope.viewer_connection_events
WHERE event_type = 'connect' AND session_id != '';

ALTER TABLE periscope.viewer_sessions_disconnect_mv MODIFY QUERY
SELECT
    tenant_id,
    stream_id,
    artifact_hash,
    internal_name,
    session_id,
    node_id,
    cluster_id,
    CAST(NULL AS Nullable(DateTime)) AS connected_at,
    timestamp AS disconnected_at,
    connector,
    country_code,
    city,
    latitude,
    longitude,
    bytes_transferred,
    session_duration,
    timestamp AS last_updated
FROM periscope.viewer_connection_events
WHERE event_type = 'disconnect' AND session_id != '';

ALTER TABLE periscope.client_qoe_samples
    ADD COLUMN IF NOT EXISTS artifact_hash String DEFAULT '' AFTER stream_id;

ALTER TABLE periscope.client_qoe_5m
    ADD COLUMN IF NOT EXISTS artifact_hash String DEFAULT '' AFTER stream_id;

ALTER TABLE periscope.client_qoe_5m_mv MODIFY QUERY
SELECT
    toStartOfInterval(timestamp, INTERVAL 5 MINUTE) AS timestamp_5m,
    tenant_id,
    stream_id,
    artifact_hash,
    internal_name,
    node_id,
    count(DISTINCT session_id) as active_sessions,
    avg(bandwidth_in) AS avg_bw_in,
    avg(bandwidth_out) AS avg_bw_out,
    avg(connection_time) AS avg_connection_time,
    if(sum(packets_sent) > 0, sum(packets_lost) / sum(packets_sent), NULL) AS pkt_loss_rate
FROM periscope.client_qoe_samples
GROUP BY timestamp_5m, tenant_id, stream_id, artifact_hash, internal_name, node_id;

ALTER TABLE periscope.routing_decisions
    ADD COLUMN IF NOT EXISTS artifact_hash String DEFAULT '' AFTER stream_id;

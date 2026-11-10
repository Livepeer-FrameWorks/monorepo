-- Source milliseconds distinguish a reconnect from a disconnect in the same
-- second without treating retained, ambiguous second-only rows as open.
ALTER TABLE periscope.viewer_connection_events
    ADD COLUMN IF NOT EXISTS source_timestamp_ms Int64 DEFAULT toInt64(toUnixTimestamp(timestamp)) * 1000 AFTER timestamp;
ALTER TABLE periscope.viewer_sessions_current
    ADD COLUMN IF NOT EXISTS last_connected_at_ms SimpleAggregateFunction(max, Int64) AFTER last_connected_at,
    ADD COLUMN IF NOT EXISTS last_disconnected_at_ms SimpleAggregateFunction(max, Int64) AFTER last_connected_at_ms;

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
    timestamp AS last_connected_at,
    source_timestamp_ms AS last_connected_at_ms,
    toInt64(0) AS last_disconnected_at_ms,
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
    CAST(NULL AS Nullable(DateTime)) AS last_connected_at,
    toInt64(0) AS last_connected_at_ms,
    source_timestamp_ms AS last_disconnected_at_ms,
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

DROP TABLE IF EXISTS viewer_usage_5m_v;

CREATE VIEW viewer_usage_5m_v AS
SELECT
    window_start, tenant_id, cluster_id, stream_id, node_id, session_id,
    billable_at_ms, seconds_observed, up_bytes_observed, down_bytes_observed,
    source_event_id, latest_projection_version_ms
FROM (
    SELECT
        window_start, tenant_id, cluster_id, stream_id, node_id, session_id,
        min(projection_version_ms) AS billable_at_ms,
        argMax(seconds_observed,    projection_version_ms) AS seconds_observed,
        argMax(up_bytes_observed,   projection_version_ms) AS up_bytes_observed,
        argMax(down_bytes_observed, projection_version_ms) AS down_bytes_observed,
        argMax(source_event_id,     projection_version_ms) AS source_event_id,
        max(projection_version_ms) AS latest_projection_version_ms
    FROM viewer_usage_5m
    GROUP BY window_start, tenant_id, cluster_id, stream_id, node_id, session_id
)
WHERE seconds_observed > 0 OR up_bytes_observed > 0 OR down_bytes_observed > 0;

INSERT INTO viewer_usage_5m (
    window_start, tenant_id, cluster_id, stream_id, node_id, session_id,
    seconds_observed, up_bytes_observed, down_bytes_observed,
    source_event_id, projection_version_ms
)
SELECT
    u.window_start,
    u.tenant_id,
    u.cluster_id,
    u.stream_id,
    u.node_id,
    u.session_id,
    toUInt32(0),
    toUInt64(0),
    toUInt64(0),
    u.source_event_id,
    toUnixTimestamp64Milli(now64(3))
FROM viewer_usage_5m_v u
INNER JOIN viewer_sessions_final_v f
    ON u.tenant_id = f.tenant_id
   AND u.node_id = f.node_id
   AND u.session_id = f.session_id
WHERE f.closed_reason = 'final'
  AND NOT (
      u.cluster_id = f.cluster_id
      AND u.stream_id = f.stream_id
      AND (toInt64(toUnixTimestamp(u.window_start)) * 1000) < f.source_ended_at_ms
      AND (toInt64(toUnixTimestamp(u.window_start)) * 1000) + 300000 > f.source_started_at_ms
  );

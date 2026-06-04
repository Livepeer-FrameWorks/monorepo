DROP TABLE IF EXISTS viewer_usage_5m_v;

CREATE VIEW IF NOT EXISTS viewer_usage_5m_v AS
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

-- Mist derives a viewer session id from the stream, viewer address and
-- playback token, so a viewer resuming a playback after Mist ended its session
-- starts another lifecycle under the same id on the same node, ending with its
-- own USER_END and source_event_id. Each lifecycle is its own final fact and
-- its own ledger usage: the views group by source_event_id, and the rollups
-- join a ledger row to the final fact of its own lifecycle.
CREATE OR REPLACE VIEW periscope.viewer_sessions_final_v AS
SELECT
    tenant_id, node_id, session_id,
    min(projection_version_ms) AS billable_at_ms,
    source_event_id,
    argMax(cluster_id,            projection_version_ms) AS cluster_id,
    argMax(stream_id,             projection_version_ms) AS stream_id,
    argMax(artifact_hash,         projection_version_ms) AS artifact_hash,
    argMax(stream_name,           projection_version_ms) AS stream_name,
    argMax(connector,             projection_version_ms) AS connector,
    argMax(host,                  projection_version_ms) AS host,
    argMax(country_code,          projection_version_ms) AS country_code,
    argMax(city,                  projection_version_ms) AS city,
    argMax(latitude,              projection_version_ms) AS latitude,
    argMax(longitude,             projection_version_ms) AS longitude,
    argMax(tags,                  projection_version_ms) AS tags,
    argMax(duration_seconds,      projection_version_ms) AS duration_seconds,
    argMax(uploaded_bytes,        projection_version_ms) AS uploaded_bytes,
    argMax(downloaded_bytes,      projection_version_ms) AS downloaded_bytes,
    argMax(seconds_connected,     projection_version_ms) AS seconds_connected,
    argMax(source_started_at_ms,  projection_version_ms) AS source_started_at_ms,
    argMax(source_ended_at_ms,    projection_version_ms) AS source_ended_at_ms,
    argMax(edge_received_at_ms,   projection_version_ms) AS edge_received_at_ms,
    max(projection_version_ms) AS latest_projection_version_ms,
    argMax(closed_reason,         projection_version_ms) AS closed_reason,
    -- USER_END breakdown arrays from MistServer src/session.cpp. Multi-stream
    -- sessions land here as parallel (name, seconds) tuples so per-stream /
    -- per-connector / per-host attribution can split a single session across
    -- the elements it touched. Empty array for single-element sessions.
    argMax(stream_times,          projection_version_ms) AS stream_times,
    argMax(connector_times,       projection_version_ms) AS connector_times,
    argMax(host_times,            projection_version_ms) AS host_times
FROM periscope.viewer_sessions_final
GROUP BY tenant_id, node_id, session_id, source_event_id;

-- The topology view selects f.*, which ClickHouse expands when the view is
-- created, so it is recreated over the lifecycle-keyed final view.
CREATE OR REPLACE VIEW periscope.viewer_sessions_topology_v AS
SELECT f.*, p.origin_cluster_id, p.control_cell_id
FROM periscope.viewer_sessions_final_v AS f
LEFT JOIN
(
    SELECT tenant_id, node_id, session_id, source_event_id,
           argMax(origin_cluster_id, projection_version_ms) AS origin_cluster_id,
           argMax(control_cell_id, projection_version_ms) AS control_cell_id
    FROM periscope.viewer_sessions_final
    GROUP BY tenant_id, node_id, session_id, source_event_id
) AS p USING (tenant_id, node_id, session_id, source_event_id);

CREATE OR REPLACE VIEW periscope.viewer_usage_5m_v AS
SELECT
    window_start, tenant_id, cluster_id, stream_id, node_id, session_id,
    billable_at_ms, seconds_observed, up_bytes_observed, down_bytes_observed,
    source_event_id, latest_projection_version_ms
FROM (
    SELECT
        window_start, tenant_id, cluster_id, stream_id, node_id, session_id, source_event_id,
        min(projection_version_ms) AS billable_at_ms,
        argMax(seconds_observed,    projection_version_ms) AS seconds_observed,
        argMax(up_bytes_observed,   projection_version_ms) AS up_bytes_observed,
        argMax(down_bytes_observed, projection_version_ms) AS down_bytes_observed,
        max(projection_version_ms) AS latest_projection_version_ms
    FROM periscope.viewer_usage_5m
    GROUP BY window_start, tenant_id, cluster_id, stream_id, node_id, session_id, source_event_id
)
WHERE seconds_observed > 0 OR up_bytes_observed > 0 OR down_bytes_observed > 0;

CREATE OR REPLACE VIEW periscope.delivery_usage_5m_v AS
SELECT
    window_start, tenant_id, cluster_id, stream_id, node_id, delivery_kind,
    delivery_id, platform,
    min(projection_version_ms) AS billable_at_ms,
    argMax(seconds_observed, projection_version_ms) AS seconds_observed,
    argMax(up_bytes_observed, projection_version_ms) AS up_bytes_observed,
    argMax(down_bytes_observed, projection_version_ms) AS down_bytes_observed,
    source_event_id,
    max(projection_version_ms) AS latest_projection_version_ms
FROM periscope.delivery_usage_5m
GROUP BY window_start, tenant_id, cluster_id, stream_id, node_id, delivery_kind, delivery_id, platform, source_event_id
HAVING seconds_observed > 0 OR up_bytes_observed > 0 OR down_bytes_observed > 0;

ALTER TABLE periscope.viewer_hours_hourly_mv MODIFY QUERY
WITH affected AS (
    SELECT DISTINCT toStartOfHour(u.window_start) AS hour, u.tenant_id, u.cluster_id, u.stream_id, s.country_code
    FROM periscope.viewer_usage_5m u
    LEFT JOIN periscope.viewer_sessions_final_v s USING (tenant_id, node_id, session_id, source_event_id)
    WHERE u.projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 2 DAY)
      AND u.window_start >= toStartOfHour(now() - INTERVAL 89 DAY)
)
SELECT
    affected.hour, affected.tenant_id, affected.cluster_id, affected.stream_id, affected.country_code,
    metrics.total_session_seconds, metrics.total_bytes, metrics.egress_bytes, metrics.unique_viewers_state,
    now64(3) AS refresh_version_ms
FROM affected
LEFT JOIN (
    SELECT toStartOfHour(u.window_start) AS hour, u.tenant_id, u.cluster_id, u.stream_id, s.country_code,
        toUInt64(sum(u.seconds_observed)) AS total_session_seconds,
        toUInt64(sum(u.up_bytes_observed + u.down_bytes_observed)) AS total_bytes,
        toUInt64(sum(u.down_bytes_observed)) AS egress_bytes,
        uniqCombinedState(if(s.host != '', s.host, concat(toString(u.node_id), '|', u.session_id))) AS unique_viewers_state
    FROM periscope.viewer_usage_5m_v u
    LEFT JOIN periscope.viewer_sessions_final_v s USING (tenant_id, node_id, session_id, source_event_id)
    WHERE (toStartOfHour(u.window_start), u.tenant_id, u.cluster_id, u.stream_id, s.country_code) IN
          (SELECT hour, tenant_id, cluster_id, stream_id, country_code FROM affected)
    GROUP BY hour, u.tenant_id, u.cluster_id, u.stream_id, s.country_code
) AS metrics USING (hour, tenant_id, cluster_id, stream_id, country_code);

ALTER TABLE periscope.viewer_geo_hourly_mv MODIFY QUERY
WITH affected AS (
    SELECT DISTINCT toStartOfHour(u.window_start) AS hour, u.tenant_id, s.country_code
    FROM periscope.viewer_usage_5m u
    LEFT JOIN periscope.viewer_sessions_final_v s USING (tenant_id, node_id, session_id, source_event_id)
    WHERE u.projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 2 DAY)
      AND u.window_start >= toStartOfHour(now() - INTERVAL 89 DAY)
)
SELECT
    affected.hour, affected.tenant_id, affected.country_code,
    metrics.viewer_count, metrics.viewer_hours, metrics.egress_gb, metrics.unique_viewers_state,
    now64(3) AS refresh_version_ms
FROM affected
LEFT JOIN (
    SELECT toStartOfHour(u.window_start) AS hour, u.tenant_id, s.country_code,
        toUInt64(uniqCombined(if(s.host != '', s.host, concat(toString(u.node_id), '|', u.session_id)))) AS viewer_count,
        sum(u.seconds_observed) / 3600.0 AS viewer_hours,
        sum(u.down_bytes_observed) / pow(1024, 3) AS egress_gb,
        uniqCombinedState(if(s.host != '', s.host, concat(toString(u.node_id), '|', u.session_id))) AS unique_viewers_state
    FROM periscope.viewer_usage_5m_v u
    LEFT JOIN periscope.viewer_sessions_final_v s USING (tenant_id, node_id, session_id, source_event_id)
    WHERE (toStartOfHour(u.window_start), u.tenant_id, s.country_code) IN
          (SELECT hour, tenant_id, country_code FROM affected)
    GROUP BY hour, u.tenant_id, s.country_code
) AS metrics USING (hour, tenant_id, country_code);

ALTER TABLE periscope.viewer_city_hourly_mv MODIFY QUERY
WITH affected AS (
    SELECT DISTINCT toStartOfHour(u.window_start) AS hour, u.tenant_id, u.stream_id, s.country_code, s.city
    FROM periscope.viewer_usage_5m u
    INNER JOIN periscope.viewer_sessions_final_v s USING (tenant_id, node_id, session_id, source_event_id)
    WHERE u.projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 2 DAY)
      AND u.window_start >= toStartOfHour(now() - INTERVAL 89 DAY)
      AND s.city != ''
)
SELECT
    affected.hour, affected.tenant_id, affected.stream_id, affected.country_code, affected.city,
    metrics.latitude, metrics.longitude, metrics.viewer_count, metrics.viewer_hours, metrics.egress_gb,
    metrics.unique_viewers_state, now64(3) AS refresh_version_ms
FROM affected
LEFT JOIN (
    SELECT toStartOfHour(u.window_start) AS hour, u.tenant_id, u.stream_id, s.country_code, s.city,
        any(s.latitude) AS latitude,
        any(s.longitude) AS longitude,
        toUInt64(uniqCombined(if(s.host != '', s.host, concat(toString(u.node_id), '|', u.session_id)))) AS viewer_count,
        sum(u.seconds_observed) / 3600.0 AS viewer_hours,
        sum(u.down_bytes_observed) / pow(1024, 3) AS egress_gb,
        uniqCombinedState(if(s.host != '', s.host, concat(toString(u.node_id), '|', u.session_id))) AS unique_viewers_state
    FROM periscope.viewer_usage_5m_v u
    INNER JOIN periscope.viewer_sessions_final_v s USING (tenant_id, node_id, session_id, source_event_id)
    WHERE s.city != '' AND (toStartOfHour(u.window_start), u.tenant_id, u.stream_id, s.country_code, s.city) IN
          (SELECT hour, tenant_id, stream_id, country_code, city FROM affected)
    GROUP BY hour, u.tenant_id, u.stream_id, s.country_code, s.city
) AS metrics USING (hour, tenant_id, stream_id, country_code, city);

ALTER TABLE periscope.stream_connection_hourly_mv MODIFY QUERY
WITH affected AS (
    SELECT DISTINCT toStartOfHour(window_start) AS hour, tenant_id, stream_id
    FROM periscope.viewer_usage_5m
    WHERE projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 2 DAY)
      AND window_start >= toStartOfHour(now() - INTERVAL 89 DAY)
)
SELECT
    affected.hour, affected.tenant_id, affected.stream_id,
    metrics.internal_name, metrics.total_bytes, metrics.total_sessions, metrics.unique_viewers_state,
    now64(3) AS refresh_version_ms
FROM affected
LEFT JOIN (
    SELECT toStartOfHour(u.window_start) AS hour, u.tenant_id, u.stream_id,
        any(s.stream_name) AS internal_name,
        toUInt64(sum(u.up_bytes_observed + u.down_bytes_observed)) AS total_bytes,
        toUInt64(uniqCombined(u.session_id)) AS total_sessions,
        uniqCombinedState(if(s.host != '', s.host, concat(toString(u.node_id), '|', u.session_id))) AS unique_viewers_state
    FROM periscope.viewer_usage_5m_v u
    LEFT JOIN periscope.viewer_sessions_final_v s USING (tenant_id, node_id, session_id, source_event_id)
    WHERE (toStartOfHour(u.window_start), u.tenant_id, u.stream_id) IN
          (SELECT hour, tenant_id, stream_id FROM affected)
    GROUP BY hour, u.tenant_id, u.stream_id
) AS metrics USING (hour, tenant_id, stream_id);

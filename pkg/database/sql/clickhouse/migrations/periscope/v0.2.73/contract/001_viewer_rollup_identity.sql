-- v0.2.73 contract: use stable viewer identity for viewer rollups.
--
-- USER_END sessions can produce multiple session IDs for the same viewer host
-- across reconnects. Viewer-facing unique-user rollups should count host when
-- present and fall back to node/session only when host is unavailable.

DROP VIEW IF EXISTS viewer_hours_hourly_mv;
DROP VIEW IF EXISTS viewer_geo_hourly_mv;
DROP VIEW IF EXISTS viewer_city_hourly_mv;
DROP VIEW IF EXISTS stream_connection_hourly_mv;
DROP VIEW IF EXISTS tenant_viewer_daily_mv;
DROP VIEW IF EXISTS tenant_analytics_daily_mv;
DROP VIEW IF EXISTS stream_analytics_daily_mv;

CREATE MATERIALIZED VIEW IF NOT EXISTS viewer_hours_hourly_mv
REFRESH EVERY 5 MINUTE APPEND TO viewer_hours_hourly_store AS
SELECT
    toStartOfHour(u.window_start) AS hour,
    u.tenant_id,
    u.cluster_id,
    u.stream_id,
    s.country_code,
    toUInt64(sum(u.seconds_observed))                          AS total_session_seconds,
    toUInt64(sum(u.up_bytes_observed + u.down_bytes_observed)) AS total_bytes,
    toUInt64(sum(u.down_bytes_observed))                        AS egress_bytes,
    uniqCombinedState(if(s.host != '', s.host, concat(toString(u.node_id), '|', u.session_id))) AS unique_viewers_state,
    now64(3)                                                   AS refresh_version_ms
FROM viewer_usage_5m_v u
LEFT JOIN viewer_sessions_final_v s USING (tenant_id, node_id, session_id)
WHERE (hour, u.tenant_id, u.cluster_id, u.stream_id, s.country_code) IN (
    SELECT DISTINCT toStartOfHour(u.window_start) AS hour, u.tenant_id, u.cluster_id, u.stream_id, s.country_code
    FROM viewer_usage_5m_v u
    LEFT JOIN viewer_sessions_final_v s USING (tenant_id, node_id, session_id)
    WHERE u.latest_projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 2 DAY)
)
GROUP BY hour, u.tenant_id, u.cluster_id, u.stream_id, s.country_code;

CREATE MATERIALIZED VIEW IF NOT EXISTS viewer_geo_hourly_mv
REFRESH EVERY 5 MINUTE APPEND TO viewer_geo_hourly_store AS
SELECT
    toStartOfHour(u.window_start) AS hour,
    u.tenant_id,
    s.country_code,
    toUInt64(uniqCombined(if(s.host != '', s.host, concat(toString(u.node_id), '|', u.session_id)))) AS viewer_count,
    sum(u.seconds_observed) / 3600.0                                AS viewer_hours,
    sum(u.down_bytes_observed) / pow(1024, 3) AS egress_gb,
    uniqCombinedState(if(s.host != '', s.host, concat(toString(u.node_id), '|', u.session_id))) AS unique_viewers_state,
    now64(3)                                                        AS refresh_version_ms
FROM viewer_usage_5m_v u
LEFT JOIN viewer_sessions_final_v s USING (tenant_id, node_id, session_id)
WHERE (hour, u.tenant_id, s.country_code) IN (
    SELECT DISTINCT toStartOfHour(u.window_start) AS hour, u.tenant_id, s.country_code
    FROM viewer_usage_5m_v u
    LEFT JOIN viewer_sessions_final_v s USING (tenant_id, node_id, session_id)
    WHERE u.latest_projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 2 DAY)
)
GROUP BY hour, u.tenant_id, s.country_code;

CREATE MATERIALIZED VIEW IF NOT EXISTS viewer_city_hourly_mv
REFRESH EVERY 5 MINUTE APPEND TO viewer_city_hourly_store AS
SELECT
    toStartOfHour(u.window_start) AS hour,
    u.tenant_id,
    u.stream_id,
    s.country_code,
    s.city,
    any(s.latitude)                                                 AS latitude,
    any(s.longitude)                                                AS longitude,
    toUInt64(uniqCombined(if(s.host != '', s.host, concat(toString(u.node_id), '|', u.session_id)))) AS viewer_count,
    sum(u.seconds_observed) / 3600.0                                AS viewer_hours,
    sum(u.down_bytes_observed) / pow(1024, 3) AS egress_gb,
    uniqCombinedState(if(s.host != '', s.host, concat(toString(u.node_id), '|', u.session_id))) AS unique_viewers_state,
    now64(3)                                                        AS refresh_version_ms
FROM viewer_usage_5m_v u
INNER JOIN viewer_sessions_final_v s USING (tenant_id, node_id, session_id)
WHERE (hour, u.tenant_id, u.stream_id, s.country_code, s.city) IN (
    SELECT DISTINCT toStartOfHour(u.window_start) AS hour, u.tenant_id, u.stream_id, s.country_code, s.city
    FROM viewer_usage_5m_v u
    INNER JOIN viewer_sessions_final_v s USING (tenant_id, node_id, session_id)
    WHERE s.city != ''
      AND u.latest_projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 2 DAY)
)
GROUP BY hour, u.tenant_id, u.stream_id, s.country_code, s.city;

CREATE MATERIALIZED VIEW IF NOT EXISTS stream_connection_hourly_mv
REFRESH EVERY 5 MINUTE APPEND TO stream_connection_hourly_store AS
SELECT
    toStartOfHour(u.window_start) AS hour,
    u.tenant_id,
    u.stream_id,
    any(s.stream_name)                                         AS internal_name,
    toUInt64(sum(u.up_bytes_observed + u.down_bytes_observed)) AS total_bytes,
    toUInt64(uniqCombined(u.session_id))                       AS total_sessions,
    uniqCombinedState(if(s.host != '', s.host, concat(toString(u.node_id), '|', u.session_id))) AS unique_viewers_state,
    now64(3)                                                   AS refresh_version_ms
FROM viewer_usage_5m_v u
LEFT JOIN viewer_sessions_final_v s USING (tenant_id, node_id, session_id)
WHERE (hour, u.tenant_id, u.stream_id) IN (
    SELECT DISTINCT toStartOfHour(u.window_start) AS hour, u.tenant_id, u.stream_id
    FROM viewer_usage_5m_v u
    LEFT JOIN viewer_sessions_final_v s USING (tenant_id, node_id, session_id)
    WHERE u.latest_projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 2 DAY)
)
GROUP BY hour, u.tenant_id, u.stream_id;

CREATE MATERIALIZED VIEW IF NOT EXISTS tenant_viewer_daily_mv
REFRESH EVERY 1 HOUR APPEND TO tenant_viewer_daily_store AS
SELECT
    toDate(u.window_start) AS day,
    u.tenant_id,
    u.cluster_id,
    sum(u.seconds_observed) / 3600.0 AS viewer_hours,
    sum(u.down_bytes_observed) / pow(1024, 3) AS egress_gb,
    uniqCombinedState(if(s.host != '', s.host, concat(toString(u.node_id), '|', u.session_id))) AS unique_viewers_state,
    toUInt64(uniqCombined(u.session_id)) AS total_sessions,
    now64(3) AS refresh_version_ms
FROM viewer_usage_5m_v u
LEFT JOIN viewer_sessions_final_v s USING (tenant_id, node_id, session_id)
WHERE (toDate(u.window_start), u.tenant_id, u.cluster_id) IN (
    SELECT DISTINCT toDate(window_start) AS day, tenant_id, cluster_id
    FROM viewer_usage_5m_v
    WHERE latest_projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 7 DAY)
)
GROUP BY day, u.tenant_id, u.cluster_id;

CREATE MATERIALIZED VIEW IF NOT EXISTS tenant_analytics_daily_mv
REFRESH EVERY 1 HOUR APPEND TO tenant_analytics_daily_store AS
SELECT
    toDate(u.window_start) AS day,
    u.tenant_id,
    toUInt64(uniqCombined(u.stream_id)) AS total_streams,
    toUInt64(uniqCombined(u.session_id)) AS total_views,
    uniqCombinedState(if(s.host != '', s.host, concat(toString(u.node_id), '|', u.session_id))) AS unique_viewers_state,
    toUInt64(sum(u.down_bytes_observed)) AS egress_bytes,
    now64(3) AS refresh_version_ms
FROM viewer_usage_5m_v u
LEFT JOIN viewer_sessions_final_v s USING (tenant_id, node_id, session_id)
WHERE (toDate(u.window_start), u.tenant_id) IN (
    SELECT DISTINCT toDate(window_start) AS day, tenant_id
    FROM viewer_usage_5m_v
    WHERE latest_projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 7 DAY)
)
GROUP BY day, u.tenant_id;

CREATE MATERIALIZED VIEW IF NOT EXISTS stream_analytics_daily_mv
REFRESH EVERY 1 HOUR APPEND TO stream_analytics_daily_store AS
SELECT
    toDate(u.window_start) AS day,
    u.tenant_id,
    u.stream_id,
    any(s.stream_name)                                         AS internal_name,
    toUInt64(uniqCombined(u.session_id))                       AS total_views,
    uniqCombinedState(if(s.host != '', s.host, concat(toString(u.node_id), '|', u.session_id))) AS unique_viewers_state,
    toUInt32(uniqCombined(s.country_code))                     AS unique_countries,
    toUInt32(uniqCombined(s.city))                             AS unique_cities,
    toUInt64(sum(u.down_bytes_observed)) AS egress_bytes,
    now64(3)                                                   AS refresh_version_ms
FROM viewer_usage_5m_v u
LEFT JOIN viewer_sessions_final_v s USING (tenant_id, node_id, session_id)
WHERE (day, u.tenant_id, u.stream_id) IN (
    SELECT DISTINCT toDate(u.window_start) AS day, u.tenant_id, u.stream_id
    FROM viewer_usage_5m_v u
    LEFT JOIN viewer_sessions_final_v s USING (tenant_id, node_id, session_id)
    WHERE u.latest_projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 2 DAY)
)
GROUP BY day, u.tenant_id, u.stream_id;

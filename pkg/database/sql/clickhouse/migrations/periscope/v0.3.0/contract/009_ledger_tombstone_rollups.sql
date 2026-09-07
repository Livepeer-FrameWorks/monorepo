-- Refresh every direct ledger-backed rollup from raw append-table keys. A
-- tombstone disappears from each *_v view, so using only the deduplicated view
-- to discover affected keys leaves stale aggregate rows forever.

DROP VIEW IF EXISTS periscope.viewer_hours_hourly_mv;
CREATE MATERIALIZED VIEW IF NOT EXISTS periscope.viewer_hours_hourly_mv
REFRESH EVERY 5 MINUTE APPEND TO periscope.viewer_hours_hourly_store AS
WITH affected AS (
    SELECT DISTINCT toStartOfHour(u.window_start) AS hour, u.tenant_id, u.cluster_id, u.stream_id, s.country_code
    FROM periscope.viewer_usage_5m u
    LEFT JOIN periscope.viewer_sessions_final_v s USING (tenant_id, node_id, session_id)
    WHERE u.projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 2 DAY)
      AND u.window_start >= toStartOfHour(now() - INTERVAL 89 DAY)
)
SELECT affected.hour, affected.tenant_id, affected.cluster_id, affected.stream_id, affected.country_code,
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
    LEFT JOIN periscope.viewer_sessions_final_v s USING (tenant_id, node_id, session_id)
    WHERE (toStartOfHour(u.window_start), u.tenant_id, u.cluster_id, u.stream_id, s.country_code) IN
          (SELECT hour, tenant_id, cluster_id, stream_id, country_code FROM affected)
    GROUP BY hour, u.tenant_id, u.cluster_id, u.stream_id, s.country_code
) AS metrics USING (hour, tenant_id, cluster_id, stream_id, country_code);

DROP VIEW IF EXISTS periscope.viewer_geo_hourly_mv;
CREATE MATERIALIZED VIEW IF NOT EXISTS periscope.viewer_geo_hourly_mv
REFRESH EVERY 5 MINUTE APPEND TO periscope.viewer_geo_hourly_store AS
WITH affected AS (
    SELECT DISTINCT toStartOfHour(u.window_start) AS hour, u.tenant_id, s.country_code
    FROM periscope.viewer_usage_5m u
    LEFT JOIN periscope.viewer_sessions_final_v s USING (tenant_id, node_id, session_id)
    WHERE u.projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 2 DAY)
      AND u.window_start >= toStartOfHour(now() - INTERVAL 89 DAY)
)
SELECT affected.hour, affected.tenant_id, affected.country_code,
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
    LEFT JOIN periscope.viewer_sessions_final_v s USING (tenant_id, node_id, session_id)
    WHERE (toStartOfHour(u.window_start), u.tenant_id, s.country_code) IN
          (SELECT hour, tenant_id, country_code FROM affected)
    GROUP BY hour, u.tenant_id, s.country_code
) AS metrics USING (hour, tenant_id, country_code);

DROP VIEW IF EXISTS periscope.viewer_city_hourly_mv;
CREATE MATERIALIZED VIEW IF NOT EXISTS periscope.viewer_city_hourly_mv
REFRESH EVERY 5 MINUTE APPEND TO periscope.viewer_city_hourly_store AS
WITH affected AS (
    SELECT DISTINCT toStartOfHour(u.window_start) AS hour, u.tenant_id, u.stream_id, s.country_code, s.city
    FROM periscope.viewer_usage_5m u
    INNER JOIN periscope.viewer_sessions_final_v s USING (tenant_id, node_id, session_id)
    WHERE u.projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 2 DAY)
      AND u.window_start >= toStartOfHour(now() - INTERVAL 89 DAY)
      AND s.city != ''
)
SELECT affected.hour, affected.tenant_id, affected.stream_id, affected.country_code, affected.city,
    metrics.latitude, metrics.longitude, metrics.viewer_count, metrics.viewer_hours, metrics.egress_gb,
    metrics.unique_viewers_state, now64(3) AS refresh_version_ms
FROM affected
LEFT JOIN (
    SELECT toStartOfHour(u.window_start) AS hour, u.tenant_id, u.stream_id, s.country_code, s.city,
        any(s.latitude) AS latitude, any(s.longitude) AS longitude,
        toUInt64(uniqCombined(if(s.host != '', s.host, concat(toString(u.node_id), '|', u.session_id)))) AS viewer_count,
        sum(u.seconds_observed) / 3600.0 AS viewer_hours,
        sum(u.down_bytes_observed) / pow(1024, 3) AS egress_gb,
        uniqCombinedState(if(s.host != '', s.host, concat(toString(u.node_id), '|', u.session_id))) AS unique_viewers_state
    FROM periscope.viewer_usage_5m_v u
    INNER JOIN periscope.viewer_sessions_final_v s USING (tenant_id, node_id, session_id)
    WHERE s.city != '' AND (toStartOfHour(u.window_start), u.tenant_id, u.stream_id, s.country_code, s.city) IN
          (SELECT hour, tenant_id, stream_id, country_code, city FROM affected)
    GROUP BY hour, u.tenant_id, u.stream_id, s.country_code, s.city
) AS metrics USING (hour, tenant_id, stream_id, country_code, city);

DROP VIEW IF EXISTS periscope.stream_connection_hourly_mv;
CREATE MATERIALIZED VIEW IF NOT EXISTS periscope.stream_connection_hourly_mv
REFRESH EVERY 5 MINUTE APPEND TO periscope.stream_connection_hourly_store AS
WITH affected AS (
    SELECT DISTINCT toStartOfHour(window_start) AS hour, tenant_id, stream_id
    FROM periscope.viewer_usage_5m
    WHERE projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 2 DAY)
      AND window_start >= toStartOfHour(now() - INTERVAL 89 DAY)
)
SELECT affected.hour, affected.tenant_id, affected.stream_id,
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
    LEFT JOIN periscope.viewer_sessions_final_v s USING (tenant_id, node_id, session_id)
    WHERE (toStartOfHour(u.window_start), u.tenant_id, u.stream_id) IN
          (SELECT hour, tenant_id, stream_id FROM affected)
    GROUP BY hour, u.tenant_id, u.stream_id
) AS metrics USING (hour, tenant_id, stream_id);

DROP VIEW IF EXISTS periscope.stream_runtime_hourly_mv;
CREATE MATERIALIZED VIEW IF NOT EXISTS periscope.stream_runtime_hourly_mv
REFRESH EVERY 5 MINUTE APPEND TO periscope.stream_runtime_hourly_store AS
WITH affected AS (
    SELECT DISTINCT toStartOfHour(window_start) AS hour, tenant_id, cluster_id, stream_id
    FROM periscope.stream_runtime_5m
    WHERE projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 2 DAY)
      AND window_start >= toStartOfHour(now() - INTERVAL 89 DAY)
)
SELECT affected.hour, affected.tenant_id, affected.cluster_id, affected.stream_id,
    metrics.runtime_seconds, metrics.peak_viewers, now64(3) AS refresh_version_ms
FROM affected
LEFT JOIN (
    SELECT toStartOfHour(window_start) AS hour, tenant_id, cluster_id, stream_id,
        toUInt64(sum(active_seconds)) AS runtime_seconds,
        toUInt32(max(peak_viewers)) AS peak_viewers
    FROM periscope.stream_runtime_5m_v
    WHERE (toStartOfHour(window_start), tenant_id, cluster_id, stream_id) IN
          (SELECT hour, tenant_id, cluster_id, stream_id FROM affected)
    GROUP BY hour, tenant_id, cluster_id, stream_id
) AS metrics USING (hour, tenant_id, cluster_id, stream_id);

DROP VIEW IF EXISTS periscope.processing_hourly_mv;
CREATE MATERIALIZED VIEW IF NOT EXISTS periscope.processing_hourly_mv
REFRESH EVERY 5 MINUTE APPEND TO periscope.processing_hourly_store AS
WITH affected AS (
    SELECT DISTINCT toStartOfHour(window_start) AS hour, tenant_id, cluster_id, process_type, output_codec, track_type
    FROM periscope.processing_5m
    WHERE projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 2 DAY)
      AND window_start >= toStartOfHour(now() - INTERVAL 89 DAY)
)
SELECT affected.hour, affected.tenant_id, affected.cluster_id, affected.process_type, affected.output_codec, affected.track_type,
    metrics.media_seconds, metrics.segment_count, now64(3) AS refresh_version_ms
FROM affected
LEFT JOIN (
    SELECT toStartOfHour(window_start) AS hour, tenant_id, cluster_id, process_type, output_codec, track_type,
        sum(p5.media_seconds) AS media_seconds,
        toUInt64(uniqCombined(p5.source_event_id)) AS segment_count
    FROM periscope.processing_5m_v AS p5
    WHERE (toStartOfHour(window_start), tenant_id, cluster_id, process_type, output_codec, track_type) IN
          (SELECT hour, tenant_id, cluster_id, process_type, output_codec, track_type FROM affected)
    GROUP BY hour, tenant_id, cluster_id, process_type, output_codec, track_type
) AS metrics USING (hour, tenant_id, cluster_id, process_type, output_codec, track_type);

DROP VIEW IF EXISTS periscope.api_usage_hourly_mv;
CREATE MATERIALIZED VIEW IF NOT EXISTS periscope.api_usage_hourly_mv
REFRESH EVERY 5 MINUTE APPEND TO periscope.api_usage_hourly_store AS
WITH affected AS (
    SELECT DISTINCT toStartOfHour(window_start) AS hour, tenant_id, auth_type, operation_type, operation_name
    FROM periscope.api_usage_5m
    WHERE projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 2 DAY)
      AND window_start >= toStartOfHour(now() - INTERVAL 364 DAY)
)
SELECT affected.hour, affected.tenant_id, affected.auth_type, affected.operation_type, affected.operation_name,
    metrics.requests, metrics.errors, metrics.duration_ms, metrics.complexity,
    metrics.unique_users_state, metrics.unique_tokens_state, now64(3) AS refresh_version_ms
FROM affected
LEFT JOIN (
    SELECT toStartOfHour(window_start) AS hour, tenant_id, auth_type, operation_type, operation_name,
        sum(api5.requests) AS requests, sum(api5.errors) AS errors,
        sum(api5.duration_ms) AS duration_ms, sum(api5.complexity) AS complexity,
        uniqCombinedMergeState(api5.unique_users_state) AS unique_users_state,
        uniqCombinedMergeState(api5.unique_tokens_state) AS unique_tokens_state
    FROM periscope.api_usage_5m_v AS api5
    WHERE (toStartOfHour(window_start), tenant_id, auth_type, operation_type, operation_name) IN
          (SELECT hour, tenant_id, auth_type, operation_type, operation_name FROM affected)
    GROUP BY hour, tenant_id, auth_type, operation_type, operation_name
) AS metrics USING (hour, tenant_id, auth_type, operation_type, operation_name);

DROP VIEW IF EXISTS periscope.tenant_viewer_daily_mv;
CREATE MATERIALIZED VIEW IF NOT EXISTS periscope.tenant_viewer_daily_mv
REFRESH EVERY 1 HOUR APPEND TO periscope.tenant_viewer_daily_store AS
WITH affected AS (
    SELECT DISTINCT toDate(window_start) AS day, tenant_id, cluster_id
    FROM periscope.viewer_usage_5m
    WHERE projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 7 DAY)
      AND window_start >= toStartOfDay(now() - INTERVAL 89 DAY)
    UNION DISTINCT
    SELECT DISTINCT toDate(toDateTime(intDiv(old.source_ended_at_ms, 1000))) AS day, old.tenant_id, old.cluster_id
    FROM periscope.viewer_sessions_final AS old
    INNER JOIN (
        SELECT DISTINCT tenant_id, node_id, session_id
        FROM periscope.viewer_sessions_final
        WHERE projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 7 DAY)
    ) AS changed USING (tenant_id, node_id, session_id)
    WHERE old.tenant_id IN (
        SELECT tenant_id FROM periscope.viewer_sessions_final
        WHERE projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 7 DAY)
    )
      AND old.source_ended_at_ms >= toInt64(toUnixTimestamp(toStartOfDay(now() - INTERVAL 89 DAY))) * 1000
    UNION DISTINCT
    SELECT toDate(bucket) AS day, tenant_id, cluster_id
    FROM periscope.rollup_backfill_markers
    WHERE scope = 'tenant_viewer_daily'
      AND seed_version_ms >= now64(3) - INTERVAL 1 DAY
      AND bucket >= toStartOfDay(now() - INTERVAL 89 DAY)
)
SELECT
    affected.day AS day,
    affected.tenant_id AS tenant_id,
    affected.cluster_id AS cluster_id,
    usage.viewer_hours AS viewer_hours,
    usage.egress_gb AS egress_gb,
    audience.unique_viewers_state AS unique_viewers_state,
    audience.total_sessions AS total_sessions,
    now64(3) AS refresh_version_ms
FROM affected
LEFT JOIN (
    SELECT toDate(window_start) AS day, tenant_id, cluster_id,
        sum(seconds_observed) / 3600.0 AS viewer_hours,
        sum(down_bytes_observed) / pow(1024, 3) AS egress_gb
    FROM periscope.viewer_usage_5m_v
    WHERE (toDate(window_start), tenant_id, cluster_id) IN
          (SELECT day, tenant_id, cluster_id FROM affected)
    GROUP BY day, tenant_id, cluster_id
) AS usage USING (day, tenant_id, cluster_id)
LEFT JOIN (
    SELECT day, tenant_id, cluster_id,
        uniqCombinedStateIf(viewer_key, included = 1) AS unique_viewers_state,
        toUInt64(uniqCombinedIf(tuple(node_id, session_id), included = 1)) AS total_sessions
    FROM (
        SELECT day, tenant_id, cluster_id, '' AS viewer_key, '' AS node_id, '' AS session_id, toUInt8(0) AS included
        FROM affected
        UNION ALL
        SELECT toDate(toDateTime(intDiv(source_ended_at_ms, 1000))) AS day, tenant_id, cluster_id,
            if(host != '', host, concat(toString(node_id), '|', session_id)) AS viewer_key,
            node_id, session_id, toUInt8(1) AS included
        FROM periscope.viewer_sessions_final_v
        WHERE (toDate(toDateTime(intDiv(source_ended_at_ms, 1000))), tenant_id, cluster_id) IN
              (SELECT day, tenant_id, cluster_id FROM affected)
          AND closed_reason = 'final'
    )
    GROUP BY day, tenant_id, cluster_id
) AS audience USING (day, tenant_id, cluster_id)
SETTINGS max_bytes_before_external_group_by = 268435456,
         max_bytes_before_external_sort = 268435456;

DROP VIEW IF EXISTS periscope.storage_usage_hourly_mv;
CREATE MATERIALIZED VIEW IF NOT EXISTS periscope.storage_usage_hourly_mv
REFRESH EVERY 5 MINUTE APPEND TO periscope.storage_usage_hourly_store AS
WITH affected AS (
    SELECT DISTINCT toStartOfHour(window_start) AS hour, tenant_id, cluster_id, storage_scope,
        storage_provider_tenant_id, storage_provider_cluster_id, storage_backend
    FROM periscope.storage_gb_seconds_5m
    WHERE projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 2 DAY)
      AND window_start >= toStartOfHour(now() - INTERVAL 89 DAY)
)
SELECT affected.hour, affected.tenant_id, affected.cluster_id, affected.storage_scope,
    affected.storage_provider_tenant_id, affected.storage_provider_cluster_id, affected.storage_backend,
    metrics.gb_seconds, metrics.gb_hours, metrics.avg_gb, now64(3) AS refresh_version_ms
FROM affected
LEFT JOIN (
    SELECT toStartOfHour(window_start) AS hour, tenant_id, cluster_id, storage_scope,
        storage_provider_tenant_id, storage_provider_cluster_id, storage_backend,
        sum(sg5.gb_seconds) AS gb_seconds,
        sum(sg5.gb_seconds) / 3600.0 AS gb_hours,
        sum(sg5.gb_seconds) / 3600.0 AS avg_gb
    FROM periscope.storage_gb_seconds_5m_v AS sg5
    WHERE (toStartOfHour(window_start), tenant_id, cluster_id, storage_scope, storage_provider_tenant_id, storage_provider_cluster_id, storage_backend) IN
          (SELECT hour, tenant_id, cluster_id, storage_scope, storage_provider_tenant_id, storage_provider_cluster_id, storage_backend FROM affected)
    GROUP BY hour, tenant_id, cluster_id, storage_scope, storage_provider_tenant_id, storage_provider_cluster_id, storage_backend
) AS metrics USING (hour, tenant_id, cluster_id, storage_scope, storage_provider_tenant_id, storage_provider_cluster_id, storage_backend);

ALTER TABLE periscope.tenant_analytics_daily_store
    MODIFY TTL day + INTERVAL 730 DAY;
ALTER TABLE periscope.stream_analytics_daily_store
    MODIFY TTL day + INTERVAL 730 DAY;

DROP VIEW IF EXISTS periscope.tenant_usage_5m_mv;
CREATE MATERIALIZED VIEW IF NOT EXISTS periscope.tenant_usage_5m_mv
REFRESH EVERY 1 MINUTE APPEND TO periscope.tenant_usage_5m_store AS
WITH affected AS (
    SELECT DISTINCT window_start, tenant_id, cluster_id
    FROM periscope.delivery_usage_5m
    WHERE projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 2 HOUR)
      AND window_start >= toStartOfFiveMinute(now() - INTERVAL 29 DAY)
    UNION DISTINCT
    SELECT DISTINCT window_start, tenant_id, cluster_id
    FROM periscope.viewer_usage_5m
    WHERE projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 2 HOUR)
      AND window_start >= toStartOfFiveMinute(now() - INTERVAL 29 DAY)
    UNION DISTINCT
    SELECT bucket AS window_start, tenant_id, cluster_id
    FROM periscope.rollup_backfill_markers
    WHERE scope = 'tenant_usage_5m'
      AND seed_version_ms >= now64(3) - INTERVAL 1 DAY
      AND bucket >= toStartOfFiveMinute(now() - INTERVAL 29 DAY)
)
SELECT
    affected.window_start AS window_start,
    affected.tenant_id AS tenant_id,
    affected.cluster_id AS cluster_id,
    delivery.seconds_observed AS seconds_observed,
    delivery.up_bytes AS up_bytes,
    delivery.down_bytes AS down_bytes,
    audience.unique_sessions_state AS unique_sessions_state,
    delivery.unique_streams_state AS unique_streams_state,
    now64(3) AS refresh_version_ms
FROM affected
LEFT JOIN (
    SELECT window_start, tenant_id, cluster_id,
        toUInt64(sum(seconds_observed)) AS seconds_observed,
        toUInt64(sum(up_bytes_observed)) AS up_bytes,
        toUInt64(sum(down_bytes_observed)) AS down_bytes,
        uniqCombinedState(stream_id) AS unique_streams_state
    FROM periscope.delivery_usage_5m_v
    WHERE (window_start, tenant_id, cluster_id) IN (SELECT window_start, tenant_id, cluster_id FROM affected)
    GROUP BY window_start, tenant_id, cluster_id
) AS delivery USING (window_start, tenant_id, cluster_id)
LEFT JOIN (
    SELECT window_start, tenant_id, cluster_id,
        uniqCombinedState(session_id) AS unique_sessions_state
    FROM periscope.viewer_usage_5m_v
    WHERE (window_start, tenant_id, cluster_id) IN (SELECT window_start, tenant_id, cluster_id FROM affected)
    GROUP BY window_start, tenant_id, cluster_id
) AS audience USING (window_start, tenant_id, cluster_id)
SETTINGS max_bytes_before_external_group_by = 268435456,
         max_bytes_before_external_sort = 268435456;

DROP VIEW IF EXISTS periscope.tenant_analytics_daily_mv;
CREATE MATERIALIZED VIEW IF NOT EXISTS periscope.tenant_analytics_daily_mv
REFRESH EVERY 1 HOUR APPEND TO periscope.tenant_analytics_daily_store AS
WITH affected AS (
    SELECT DISTINCT toDate(window_start) AS day, tenant_id
    FROM periscope.delivery_usage_5m
    WHERE projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 7 DAY)
      AND window_start >= toStartOfDay(now() - INTERVAL 89 DAY)
    UNION DISTINCT
    SELECT DISTINCT toDate(window_start) AS day, tenant_id
    FROM periscope.viewer_usage_5m
    WHERE projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 7 DAY)
      AND window_start >= toStartOfDay(now() - INTERVAL 89 DAY)
    UNION DISTINCT
    SELECT DISTINCT toDate(toDateTime(intDiv(old.source_ended_at_ms, 1000))) AS day, old.tenant_id
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
      AND old.source_ended_at_ms >= toInt64(toUnixTimestamp(toStartOfDay(now() - INTERVAL 729 DAY))) * 1000
    UNION DISTINCT
    SELECT DISTINCT toDate(toDateTime(intDiv(old.source_ended_at_ms, 1000))) AS day, old.tenant_id
    FROM periscope.restream_sessions_final AS old
    INNER JOIN (
        SELECT DISTINCT tenant_id, node_id, source_event_id
        FROM periscope.restream_sessions_final
        WHERE projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 7 DAY)
    ) AS changed USING (tenant_id, node_id, source_event_id)
    WHERE old.tenant_id IN (
        SELECT tenant_id FROM periscope.restream_sessions_final
        WHERE projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 7 DAY)
    )
      AND old.source_ended_at_ms >= toInt64(toUnixTimestamp(toStartOfDay(now() - INTERVAL 729 DAY))) * 1000
    UNION DISTINCT
    SELECT toDate(bucket) AS day, tenant_id
    FROM periscope.rollup_backfill_markers
    WHERE scope = 'tenant_analytics_daily'
      AND seed_version_ms >= now64(3) - INTERVAL 1 DAY
      AND bucket >= toStartOfDay(now() - INTERVAL 729 DAY)
)
SELECT
    affected.day AS day,
    affected.tenant_id AS tenant_id,
    delivery.total_streams AS total_streams,
    audience.total_views AS total_views,
    audience.unique_viewers_state AS unique_viewers_state,
    delivery.egress_bytes AS egress_bytes,
    now64(3) AS refresh_version_ms
FROM affected
LEFT JOIN (
    SELECT day, tenant_id,
        toUInt64(uniqCombined(stream_id)) AS total_streams,
        toUInt64(sum(bytes)) AS egress_bytes
    FROM (
        SELECT toDate(toDateTime(intDiv(source_ended_at_ms, 1000))) AS day, tenant_id, stream_id,
            downloaded_bytes AS bytes
        FROM periscope.viewer_sessions_final_v
        WHERE (toDate(toDateTime(intDiv(source_ended_at_ms, 1000))), tenant_id) IN (SELECT day, tenant_id FROM affected)
          AND closed_reason = 'final'
        UNION ALL
        SELECT toDate(toDateTime(intDiv(source_ended_at_ms, 1000))) AS day, tenant_id, stream_id,
            bytes_sent AS bytes
        FROM periscope.restream_sessions_final_v
        WHERE (toDate(toDateTime(intDiv(source_ended_at_ms, 1000))), tenant_id) IN (SELECT day, tenant_id FROM affected)
          AND state IN ('idle', 'failed')
          AND source_started_at_ms > 0 AND source_ended_at_ms > source_started_at_ms
    )
    GROUP BY day, tenant_id
) AS delivery USING (day, tenant_id)
LEFT JOIN (
    SELECT day, tenant_id,
        toUInt64(uniqCombinedIf(tuple(node_id, session_id), included = 1)) AS total_views,
        uniqCombinedStateIf(viewer_key, included = 1) AS unique_viewers_state
    FROM (
        SELECT day, tenant_id, '' AS viewer_key, '' AS node_id, '' AS session_id, toUInt8(0) AS included
        FROM affected
        UNION ALL
        SELECT toDate(toDateTime(intDiv(source_ended_at_ms, 1000))) AS day, tenant_id,
            if(host != '', host, concat(toString(node_id), '|', session_id)) AS viewer_key,
            node_id, session_id, toUInt8(1) AS included
        FROM periscope.viewer_sessions_final_v
        WHERE (toDate(toDateTime(intDiv(source_ended_at_ms, 1000))), tenant_id) IN (SELECT day, tenant_id FROM affected)
          AND closed_reason = 'final'
    )
    GROUP BY day, tenant_id
) AS audience USING (day, tenant_id)
SETTINGS max_bytes_before_external_group_by = 268435456,
         max_bytes_before_external_sort = 268435456;

DROP VIEW IF EXISTS periscope.stream_analytics_daily_mv;
CREATE MATERIALIZED VIEW IF NOT EXISTS periscope.stream_analytics_daily_mv
REFRESH EVERY 1 HOUR APPEND TO periscope.stream_analytics_daily_store AS
WITH affected AS (
    SELECT DISTINCT toDate(window_start) AS day, tenant_id, stream_id
    FROM periscope.delivery_usage_5m
    WHERE projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 2 DAY)
      AND window_start >= toStartOfDay(now() - INTERVAL 89 DAY)
    UNION DISTINCT
    SELECT DISTINCT toDate(window_start) AS day, tenant_id, stream_id
    FROM periscope.viewer_usage_5m
    WHERE projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 2 DAY)
      AND window_start >= toStartOfDay(now() - INTERVAL 89 DAY)
    UNION DISTINCT
    SELECT DISTINCT toDate(toDateTime(intDiv(old.source_ended_at_ms, 1000))) AS day, old.tenant_id, old.stream_id
    FROM periscope.viewer_sessions_final AS old
    INNER JOIN (
        SELECT DISTINCT tenant_id, node_id, session_id
        FROM periscope.viewer_sessions_final
        WHERE projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 2 DAY)
    ) AS changed USING (tenant_id, node_id, session_id)
    WHERE old.tenant_id IN (
        SELECT tenant_id FROM periscope.viewer_sessions_final
        WHERE projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 2 DAY)
    )
      AND old.source_ended_at_ms >= toInt64(toUnixTimestamp(toStartOfDay(now() - INTERVAL 729 DAY))) * 1000
    UNION DISTINCT
    SELECT DISTINCT toDate(toDateTime(intDiv(old.source_ended_at_ms, 1000))) AS day, old.tenant_id, old.stream_id
    FROM periscope.restream_sessions_final AS old
    INNER JOIN (
        SELECT DISTINCT tenant_id, node_id, source_event_id
        FROM periscope.restream_sessions_final
        WHERE projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 2 DAY)
    ) AS changed USING (tenant_id, node_id, source_event_id)
    WHERE old.tenant_id IN (
        SELECT tenant_id FROM periscope.restream_sessions_final
        WHERE projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 2 DAY)
    )
      AND old.source_ended_at_ms >= toInt64(toUnixTimestamp(toStartOfDay(now() - INTERVAL 729 DAY))) * 1000
    UNION DISTINCT
    SELECT toDate(bucket) AS day, tenant_id, stream_id
    FROM periscope.rollup_backfill_markers
    WHERE scope = 'stream_analytics_daily'
      AND seed_version_ms >= now64(3) - INTERVAL 1 DAY
      AND bucket >= toStartOfDay(now() - INTERVAL 729 DAY)
)
SELECT
    affected.day AS day,
    affected.tenant_id AS tenant_id,
    affected.stream_id AS stream_id,
    audience.internal_name AS internal_name,
    audience.total_views AS total_views,
    audience.unique_viewers_state AS unique_viewers_state,
    audience.unique_countries AS unique_countries,
    audience.unique_cities AS unique_cities,
    delivery.egress_bytes AS egress_bytes,
    now64(3) AS refresh_version_ms
FROM affected
LEFT JOIN (
    SELECT day, tenant_id, stream_id, toUInt64(sum(bytes)) AS egress_bytes
    FROM (
        SELECT toDate(toDateTime(intDiv(source_ended_at_ms, 1000))) AS day, tenant_id, stream_id,
            downloaded_bytes AS bytes
        FROM periscope.viewer_sessions_final_v
        WHERE (toDate(toDateTime(intDiv(source_ended_at_ms, 1000))), tenant_id, stream_id) IN (SELECT day, tenant_id, stream_id FROM affected)
          AND closed_reason = 'final'
        UNION ALL
        SELECT toDate(toDateTime(intDiv(source_ended_at_ms, 1000))) AS day, tenant_id, stream_id,
            bytes_sent AS bytes
        FROM periscope.restream_sessions_final_v
        WHERE (toDate(toDateTime(intDiv(source_ended_at_ms, 1000))), tenant_id, stream_id) IN (SELECT day, tenant_id, stream_id FROM affected)
          AND state IN ('idle', 'failed')
          AND source_started_at_ms > 0 AND source_ended_at_ms > source_started_at_ms
    )
    GROUP BY day, tenant_id, stream_id
) AS delivery USING (day, tenant_id, stream_id)
LEFT JOIN (
    SELECT day, tenant_id, stream_id,
        anyIf(stream_name, included = 1) AS internal_name,
        toUInt64(uniqCombinedIf(tuple(node_id, session_id), included = 1)) AS total_views,
        uniqCombinedStateIf(viewer_key, included = 1) AS unique_viewers_state,
        toUInt32(uniqCombinedIf(country_code, included = 1 AND country_code != '')) AS unique_countries,
        toUInt32(uniqCombinedIf(city, included = 1 AND city != '')) AS unique_cities
    FROM (
        SELECT day, tenant_id, stream_id, '' AS stream_name, '' AS viewer_key,
            '' AS node_id, '' AS session_id, '' AS country_code, '' AS city, toUInt8(0) AS included
        FROM affected
        UNION ALL
        SELECT toDate(toDateTime(intDiv(source_ended_at_ms, 1000))) AS day, tenant_id, stream_id,
            stream_name, if(host != '', host, concat(toString(node_id), '|', session_id)) AS viewer_key,
            node_id, session_id, country_code, city, toUInt8(1) AS included
        FROM periscope.viewer_sessions_final_v
        WHERE (toDate(toDateTime(intDiv(source_ended_at_ms, 1000))), tenant_id, stream_id) IN (SELECT day, tenant_id, stream_id FROM affected)
          AND closed_reason = 'final'
    )
    GROUP BY day, tenant_id, stream_id
) AS audience USING (day, tenant_id, stream_id)
SETTINGS max_bytes_before_external_group_by = 268435456,
         max_bytes_before_external_sort = 268435456;

DROP VIEW IF EXISTS periscope.tenant_usage_hourly_mv;
CREATE MATERIALIZED VIEW IF NOT EXISTS periscope.tenant_usage_hourly_mv
REFRESH EVERY 5 MINUTE APPEND TO periscope.tenant_usage_hourly_store AS
WITH affected AS (
    SELECT DISTINCT toStartOfHour(window_start) AS hour, tenant_id, cluster_id
    FROM periscope.delivery_usage_5m
    WHERE projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 2 DAY)
      AND window_start >= toStartOfHour(now() - INTERVAL 89 DAY)
    UNION DISTINCT
    SELECT DISTINCT toStartOfHour(window_start) AS hour, tenant_id, cluster_id
    FROM periscope.viewer_usage_5m
    WHERE projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 2 DAY)
      AND window_start >= toStartOfHour(now() - INTERVAL 89 DAY)
    UNION DISTINCT
    SELECT toStartOfHour(bucket) AS hour, tenant_id, cluster_id
    FROM periscope.rollup_backfill_markers
    WHERE scope = 'tenant_usage_hourly'
      AND seed_version_ms >= now64(3) - INTERVAL 1 DAY
      AND bucket >= toStartOfHour(now() - INTERVAL 89 DAY)
)
SELECT
    affected.hour AS hour,
    affected.tenant_id AS tenant_id,
    affected.cluster_id AS cluster_id,
    delivery.seconds_observed AS seconds_observed,
    delivery.up_bytes AS up_bytes,
    delivery.down_bytes AS down_bytes,
    audience.unique_sessions_state AS unique_sessions_state,
    delivery.unique_streams_state AS unique_streams_state,
    now64(3) AS refresh_version_ms
FROM affected
LEFT JOIN (
    SELECT toStartOfHour(window_start) AS hour, tenant_id, cluster_id,
        toUInt64(sum(seconds_observed)) AS seconds_observed,
        toUInt64(sum(up_bytes_observed)) AS up_bytes,
        toUInt64(sum(down_bytes_observed)) AS down_bytes,
        uniqCombinedState(stream_id) AS unique_streams_state
    FROM periscope.delivery_usage_5m_v
    WHERE (toStartOfHour(window_start), tenant_id, cluster_id) IN (SELECT hour, tenant_id, cluster_id FROM affected)
    GROUP BY hour, tenant_id, cluster_id
) AS delivery USING (hour, tenant_id, cluster_id)
LEFT JOIN (
    SELECT toStartOfHour(window_start) AS hour, tenant_id, cluster_id,
        uniqCombinedState(session_id) AS unique_sessions_state
    FROM periscope.viewer_usage_5m_v
    WHERE (toStartOfHour(window_start), tenant_id, cluster_id) IN (SELECT hour, tenant_id, cluster_id FROM affected)
    GROUP BY hour, tenant_id, cluster_id
) AS audience USING (hour, tenant_id, cluster_id)
SETTINGS max_bytes_before_external_group_by = 268435456,
         max_bytes_before_external_sort = 268435456;

-- The v0.2.96 refreshable view still maintains window-day rows until the
-- contract phase replaces it. Do not write end-day aggregates here: active
-- legacy rows could overwrite them with a newer refresh version before the
-- contract migration runs. Only retire legacy keys that no longer have a
-- retained final fact; contract/011 performs the end-day rewrite atomically
-- with the view replacement.
SET max_bytes_before_external_group_by = 268435456;
SET max_bytes_before_external_sort = 268435456;

INSERT INTO periscope.stream_analytics_daily_store
    (day, tenant_id, stream_id, internal_name, total_views,
     unique_viewers_state, unique_countries, unique_cities, egress_bytes,
     refresh_version_ms)
WITH current_keys AS (
    SELECT DISTINCT
        toDate(toDateTime(intDiv(source_ended_at_ms, 1000))) AS day,
        tenant_id,
        stream_id
    FROM periscope.viewer_sessions_final_v
    WHERE source_ended_at_ms >= toInt64(toUnixTimestamp(toStartOfDay(now() - INTERVAL 729 DAY))) * 1000
      AND closed_reason = 'final'
    UNION DISTINCT
    SELECT DISTINCT
        toDate(toDateTime(intDiv(source_ended_at_ms, 1000))) AS day,
        tenant_id,
        stream_id
    FROM periscope.restream_sessions_final_v
    WHERE source_ended_at_ms >= toInt64(toUnixTimestamp(toStartOfDay(now() - INTERVAL 729 DAY))) * 1000
      AND state IN ('idle', 'failed')
      AND source_started_at_ms > 0
      AND source_ended_at_ms > source_started_at_ms
), retired_keys AS (
    SELECT DISTINCT day, tenant_id, stream_id
    FROM periscope.stream_analytics_daily_store
    WHERE day >= toDate(now() - INTERVAL 729 DAY)
      AND (day, tenant_id, stream_id) NOT IN (
          SELECT day, tenant_id, stream_id FROM current_keys
      )
)
SELECT
    day,
    tenant_id,
    stream_id,
    '' AS internal_name,
    toUInt64(0) AS total_views,
    uniqCombinedStateIf(toString(''), toUInt8(0)) AS unique_viewers_state,
    toUInt32(0) AS unique_countries,
    toUInt32(0) AS unique_cities,
    toUInt64(0) AS egress_bytes,
    now64(3) AS refresh_version_ms
FROM retired_keys
GROUP BY day, tenant_id, stream_id;

DROP VIEW IF EXISTS periscope.tenant_usage_daily_mv;

CREATE MATERIALIZED VIEW IF NOT EXISTS periscope.tenant_usage_daily_mv
REFRESH EVERY 1 HOUR APPEND TO periscope.tenant_usage_daily_store AS
SELECT
    toDate(hour) AS day,
    tenant_id,
    cluster_id,
    sum(tuh.seconds_observed) AS seconds_observed,
    sum(tuh.up_bytes)         AS up_bytes,
    sum(tuh.down_bytes)       AS down_bytes,
    uniqCombinedMergeState(tuh.unique_sessions_state) AS unique_sessions_state,
    uniqCombinedMergeState(tuh.unique_streams_state)  AS unique_streams_state,
    now64(3)                                      AS refresh_version_ms
FROM periscope.tenant_usage_hourly AS tuh
WHERE toDate(tuh.hour) >= toDate(now() - INTERVAL 364 DAY)
  AND (day, tenant_id, cluster_id) IN (
    SELECT DISTINCT toDate(hour) AS day, tenant_id, cluster_id
    FROM periscope.tenant_usage_hourly
    WHERE latest_refresh_version_ms >= now64(3) - INTERVAL 7 DAY
      AND toDate(hour) >= toDate(now() - INTERVAL 364 DAY)
)
GROUP BY day, tenant_id, cluster_id
SETTINGS max_bytes_before_external_group_by = 268435456,
         max_bytes_before_external_sort = 268435456;

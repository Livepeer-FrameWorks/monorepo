-- Seed the discovery keys at the start of every contract attempt. Keeping the
-- idempotent seed and its consumers in one migration file makes a delayed
-- contract run and a retry after a mid-file failure independently recoverable;
-- the migration ledger cannot suppress the seed on a retry of this file.
-- Spill large grouping/sorting intermediates instead of requiring them all in
-- memory while discovering the retained key set.
SET max_bytes_before_external_group_by = 268435456;
SET max_bytes_before_external_sort = 268435456;

INSERT INTO periscope.rollup_backfill_markers
    (scope, bucket, tenant_id, cluster_id, stream_id, seed_version_ms)
SELECT 'tenant_usage_5m', bucket, tenant_id, cluster_id, toUUIDOrZero(''), now64(3)
FROM (
    SELECT window_start AS bucket, tenant_id, cluster_id
    FROM periscope.delivery_usage_5m_v
    WHERE window_start >= toStartOfFiveMinute(now() - INTERVAL 29 DAY)
    UNION DISTINCT
    SELECT window_start, tenant_id, cluster_id
    FROM periscope.viewer_usage_5m_v
    WHERE window_start >= toStartOfFiveMinute(now() - INTERVAL 29 DAY)
);
INSERT INTO periscope.rollup_backfill_seed_receipts VALUES ('tenant_usage_5m', now64(3));

INSERT INTO periscope.rollup_backfill_markers
    (scope, bucket, tenant_id, cluster_id, stream_id, seed_version_ms)
SELECT 'tenant_usage_hourly', bucket, tenant_id, cluster_id, toUUIDOrZero(''), now64(3)
FROM (
    SELECT toStartOfHour(window_start) AS bucket, tenant_id, cluster_id
    FROM periscope.delivery_usage_5m_v
    WHERE window_start >= toStartOfHour(now() - INTERVAL 89 DAY)
    UNION DISTINCT
    SELECT toStartOfHour(window_start), tenant_id, cluster_id
    FROM periscope.viewer_usage_5m_v
    WHERE window_start >= toStartOfHour(now() - INTERVAL 89 DAY)
);
INSERT INTO periscope.rollup_backfill_seed_receipts VALUES ('tenant_usage_hourly', now64(3));

-- This scope is downstream of tenant_usage_hourly and needs no direct marker.
INSERT INTO periscope.rollup_backfill_seed_receipts VALUES ('tenant_usage_daily', now64(3));

INSERT INTO periscope.rollup_backfill_markers
    (scope, bucket, tenant_id, cluster_id, stream_id, seed_version_ms)
SELECT 'tenant_viewer_daily', bucket, tenant_id, cluster_id, toUUIDOrZero(''), now64(3)
FROM (
    SELECT toStartOfDay(window_start) AS bucket, tenant_id, cluster_id
    FROM periscope.viewer_usage_5m_v
    WHERE window_start >= toStartOfDay(now() - INTERVAL 89 DAY)
    UNION DISTINCT
    SELECT toStartOfDay(toDateTime(intDiv(source_ended_at_ms, 1000))), tenant_id, cluster_id
    FROM periscope.viewer_sessions_final_v
    WHERE source_ended_at_ms >= toInt64(toUnixTimestamp(toStartOfDay(now() - INTERVAL 89 DAY))) * 1000
      AND closed_reason = 'final'
);
INSERT INTO periscope.rollup_backfill_seed_receipts VALUES ('tenant_viewer_daily', now64(3));

INSERT INTO periscope.rollup_backfill_markers
    (scope, bucket, tenant_id, cluster_id, stream_id, seed_version_ms)
SELECT 'tenant_analytics_daily', bucket, tenant_id, '', toUUIDOrZero(''), now64(3)
FROM (
    SELECT toStartOfDay(window_start) AS bucket, tenant_id
    FROM periscope.delivery_usage_5m_v
    WHERE window_start >= toStartOfDay(now() - INTERVAL 89 DAY)
    UNION DISTINCT
    SELECT toStartOfDay(window_start), tenant_id
    FROM periscope.viewer_usage_5m_v
    WHERE window_start >= toStartOfDay(now() - INTERVAL 89 DAY)
    UNION DISTINCT
    SELECT toStartOfDay(toDateTime(intDiv(source_ended_at_ms, 1000))), tenant_id
    FROM periscope.viewer_sessions_final_v
    WHERE source_ended_at_ms >= toInt64(toUnixTimestamp(toStartOfDay(now() - INTERVAL 729 DAY))) * 1000
      AND closed_reason = 'final'
    UNION DISTINCT
    SELECT toStartOfDay(toDateTime(intDiv(source_ended_at_ms, 1000))), tenant_id
    FROM periscope.restream_sessions_final_v
    WHERE source_ended_at_ms >= toInt64(toUnixTimestamp(toStartOfDay(now() - INTERVAL 729 DAY))) * 1000
      AND state IN ('idle', 'failed')
      AND source_started_at_ms > 0 AND source_ended_at_ms > source_started_at_ms
);
INSERT INTO periscope.rollup_backfill_seed_receipts VALUES ('tenant_analytics_daily', now64(3));

INSERT INTO periscope.rollup_backfill_markers
    (scope, bucket, tenant_id, cluster_id, stream_id, seed_version_ms)
SELECT 'stream_analytics_daily', bucket, tenant_id, '', stream_id, now64(3)
FROM (
    SELECT toStartOfDay(window_start) AS bucket, tenant_id, stream_id
    FROM periscope.delivery_usage_5m_v
    WHERE window_start >= toStartOfDay(now() - INTERVAL 89 DAY)
    UNION DISTINCT
    SELECT toStartOfDay(window_start), tenant_id, stream_id
    FROM periscope.viewer_usage_5m_v
    WHERE window_start >= toStartOfDay(now() - INTERVAL 89 DAY)
    UNION DISTINCT
    SELECT toStartOfDay(toDateTime(intDiv(source_ended_at_ms, 1000))), tenant_id, stream_id
    FROM periscope.viewer_sessions_final_v
    WHERE source_ended_at_ms >= toInt64(toUnixTimestamp(toStartOfDay(now() - INTERVAL 729 DAY))) * 1000
      AND closed_reason = 'final'
    UNION DISTINCT
    SELECT toStartOfDay(toDateTime(intDiv(source_ended_at_ms, 1000))), tenant_id, stream_id
    FROM periscope.restream_sessions_final_v
    WHERE source_ended_at_ms >= toInt64(toUnixTimestamp(toStartOfDay(now() - INTERVAL 729 DAY))) * 1000
      AND state IN ('idle', 'failed')
      AND source_started_at_ms > 0 AND source_ended_at_ms > source_started_at_ms
);
INSERT INTO periscope.rollup_backfill_seed_receipts VALUES ('stream_analytics_daily', now64(3));

-- Refuse the destructive refresh unless this invocation completed every seed.
SELECT throwIf(
    (
        SELECT count()
        FROM (
            SELECT scope
            FROM periscope.rollup_backfill_seed_receipts FINAL
            WHERE seed_version_ms >= now64(3) - INTERVAL 1 DAY
              AND scope IN (
                  'tenant_usage_5m', 'tenant_usage_hourly', 'tenant_usage_daily',
                  'tenant_viewer_daily', 'tenant_analytics_daily', 'stream_analytics_daily'
              )
            GROUP BY scope
        )
    ) != 6,
    'delivery rollup seed receipts are incomplete; retry the contract migration'
);

SYSTEM REFRESH VIEW periscope.tenant_usage_5m_mv;
SYSTEM WAIT VIEW periscope.tenant_usage_5m_mv;
SYSTEM REFRESH VIEW periscope.tenant_usage_hourly_mv;
SYSTEM WAIT VIEW periscope.tenant_usage_hourly_mv;
SYSTEM REFRESH VIEW periscope.tenant_usage_daily_mv;
SYSTEM WAIT VIEW periscope.tenant_usage_daily_mv;
SYSTEM REFRESH VIEW periscope.tenant_viewer_daily_mv;
SYSTEM WAIT VIEW periscope.tenant_viewer_daily_mv;
SYSTEM REFRESH VIEW periscope.tenant_analytics_daily_mv;
SYSTEM WAIT VIEW periscope.tenant_analytics_daily_mv;
SYSTEM REFRESH VIEW periscope.stream_analytics_daily_mv;
SYSTEM WAIT VIEW periscope.stream_analytics_daily_mv;

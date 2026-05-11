-- v0.2.33 expand: federation topology events do not always carry latency or
-- TTL measures, but federation_hourly stores non-null aggregate columns.

ALTER TABLE federation_hourly_mv MODIFY QUERY
SELECT
    toStartOfHour(timestamp) AS hour,
    tenant_id,
    local_cluster,
    remote_cluster,
    event_type,
    count() AS event_count,
    sum(ifNull(latency_ms, 0)) AS sum_latency_ms,
    sum(ifNull(time_to_live_ms, 0)) AS sum_time_to_live_ms,
    countIf(failure_reason != '' AND failure_reason IS NOT NULL) AS failure_count
FROM federation_events
GROUP BY hour, tenant_id, local_cluster, remote_cluster, event_type;

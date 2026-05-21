-- v0.2.54 expand: load_balancing events may omit routing_distance_km and
-- latency_ms, while the hourly routing rollup stores non-null aggregate
-- columns.

ALTER TABLE routing_cluster_hourly_mv MODIFY QUERY
SELECT
    toStartOfHour(timestamp) AS hour,
    tenant_id,
    cluster_id,
    remote_cluster_id,
    status,
    toUInt32(count()) AS event_count,
    toUInt32(countIf(status = 'success')) AS success_count,
    toFloat32(sum(if(isNull(latency_ms), 0., assumeNotNull(latency_ms)))) AS sum_latency_ms,
    toFloat64(sum(if(isNull(routing_distance_km), 0., assumeNotNull(routing_distance_km)))) AS sum_distance_km,
    toFloat32(max(if(isNull(latency_ms), 0., assumeNotNull(latency_ms)))) AS max_latency_ms,
    toFloat64(avg(score)) AS avg_score
FROM routing_decisions
GROUP BY hour, tenant_id, cluster_id, remote_cluster_id, status;

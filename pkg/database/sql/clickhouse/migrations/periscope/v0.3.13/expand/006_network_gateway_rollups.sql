-- Gateway-keyed snapshots of each gateway's discovery and dispatched attempts.
-- network_observation_rollups is keyed by region and mixes gateways with
-- probers, so the per-gateway performance series reads these beyond the raw
-- observation retention. Periscope's rollup loop fills them from the retained
-- raw observations on its first pass after the upgrade.
CREATE TABLE IF NOT EXISTS periscope.network_gateway_rollups (
 tenant_id UUID, provider LowCardinality(String), network_id LowCardinality(String), environment LowCardinality(String),
 bucket DateTime, interval_seconds UInt32, kind LowCardinality(String), resource_id String, resolved_ip String,
 gateway_id String, gateway_region LowCardinality(String),
 samples UInt64, successes UInt64, failures UInt64,
 latency_sum UInt64, latency_count UInt64, max_latency UInt32, returned_pixels UInt64,
 snapshot_version DateTime64(3)
) ENGINE = ReplicatedReplacingMergeTree(snapshot_version)
PARTITION BY toYYYYMM(bucket)
ORDER BY (tenant_id,provider,network_id,environment,interval_seconds,bucket,kind,resource_id,resolved_ip,gateway_id,gateway_region)
TTL bucket + toIntervalDay(multiIf(interval_seconds=300,180,kind='discovery',365,730)) DELETE;

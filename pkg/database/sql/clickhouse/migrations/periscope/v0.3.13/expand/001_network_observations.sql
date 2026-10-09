
CREATE TABLE IF NOT EXISTS periscope.network_observations (
 tenant_id UUID, provider LowCardinality(String), network_id LowCardinality(String), environment LowCardinality(String),
 timestamp DateTime64(3), event_id UUID, gateway_id String, gateway_region LowCardinality(String),
 producer_kind LowCardinality(String), kind LowCardinality(String),
 resource_id String, endpoint_uri String, resolved_ip String,
 workload LowCardinality(String), profile_key String,
 success UInt8, qualifying UInt8, failure_kind LowCardinality(String), disposition LowCardinality(String),
 latency_ms UInt32, source_duration_ms UInt32, response_headers_ms UInt32, response_body_ms UInt32,
 attempts UInt32, fallback_count UInt32, deadline_exceeded UInt8, cadence_seconds UInt32,
 price_numerator String, price_denominator String, currency LowCardinality(String), price_unit LowCardinality(String),
 public_data String, private_tenant_id UUID, private_data String,
 inserted_at DateTime64(3) DEFAULT now64(3)
) ENGINE = ReplicatedReplacingMergeTree(inserted_at)
PARTITION BY toYYYYMM(timestamp)
ORDER BY (tenant_id,provider,network_id,environment,toDate(timestamp),event_id)
TTL timestamp + INTERVAL 30 DAY DELETE;

CREATE TABLE IF NOT EXISTS periscope.network_observation_rollups (
 tenant_id UUID, provider LowCardinality(String), network_id LowCardinality(String), environment LowCardinality(String),
 bucket DateTime, interval_seconds UInt32, kind LowCardinality(String), resource_id String, resolved_ip String,
 gateway_region LowCardinality(String), workload LowCardinality(String), profile_key String,
 samples UInt64, successes UInt64, failures UInt64, qualified_samples UInt64,
 latency_state AggregateFunction(quantilesTDigestIf(0.5,0.95,0.99), UInt32, UInt8),
 duration_samples UInt64, duration_ratio_state AggregateFunction(quantileTDigestIf(0.95), Float64, UInt8),
 retries UInt64, fallbacks UInt64, deadline_misses UInt64, snapshot_version DateTime64(3)
) ENGINE = ReplicatedReplacingMergeTree(snapshot_version)
PARTITION BY toYYYYMM(bucket)
ORDER BY (tenant_id,provider,network_id,environment,interval_seconds,bucket,kind,resource_id,resolved_ip,gateway_region,workload,profile_key)
TTL bucket + toIntervalDay(multiIf(kind IN ('state','coverage'),30,interval_seconds=300,180,kind='discovery',365,730)) DELETE;

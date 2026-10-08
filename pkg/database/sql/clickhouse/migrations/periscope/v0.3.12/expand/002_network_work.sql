ALTER TABLE periscope.network_observations
 ADD COLUMN IF NOT EXISTS accounting_version UInt32,
 ADD COLUMN IF NOT EXISTS request_dispatched UInt8,
 ADD COLUMN IF NOT EXISTS returned_pixels_known UInt8,
 ADD COLUMN IF NOT EXISTS returned_pixels UInt64,
 ADD COLUMN IF NOT EXISTS accepted_pixels UInt64,
 ADD COLUMN IF NOT EXISTS accepted_renditions UInt32,
 ADD COLUMN IF NOT EXISTS service_charge_known UInt8,
 ADD COLUMN IF NOT EXISTS service_charge_numerator Decimal256(0),
 ADD COLUMN IF NOT EXISTS service_charge_denominator String DEFAULT '1',
 ADD COLUMN IF NOT EXISTS ticket_credit_known UInt8,
 ADD COLUMN IF NOT EXISTS ticket_credit_numerator String DEFAULT '0',
 ADD COLUMN IF NOT EXISTS ticket_credit_denominator String DEFAULT '1',
 ADD COLUMN IF NOT EXISTS tickets_sent UInt64;

CREATE TABLE IF NOT EXISTS periscope.network_work_rollups (
 tenant_id UUID, provider LowCardinality(String), network_id LowCardinality(String), environment LowCardinality(String),
 bucket DateTime, interval_seconds UInt32, resource_id String, resolved_ip String,
 gateway_region LowCardinality(String), profile_key String,
 service_charge_denominator String, ticket_credit_denominator String,
 accounted_attempts UInt64, unaccounted_samples UInt64, dispatched_attempts UInt64,
 dispatched_source_ms UInt64, returned_attempts UInt64, returned_pixels UInt64,
 accepted_segments UInt64, accepted_source_ms UInt64, accepted_pixels UInt64, accepted_rendition_ms UInt64,
 service_charge_samples UInt64, service_charge_numerator Decimal256(0),
 ticket_credit_samples UInt64, ticket_credit_numerator String, tickets_sent UInt64,
 dispatched_duration_samples UInt64, accepted_duration_samples UInt64, accepted_output_samples UInt64,
 snapshot_version DateTime64(3)
) ENGINE = ReplicatedReplacingMergeTree(snapshot_version)
PARTITION BY toYYYYMM(bucket)
ORDER BY (tenant_id,provider,network_id,environment,interval_seconds,bucket,resource_id,resolved_ip,gateway_region,profile_key,service_charge_denominator,ticket_credit_denominator,ticket_credit_numerator)
TTL bucket + toIntervalDay(if(interval_seconds=300,180,730)) DELETE;

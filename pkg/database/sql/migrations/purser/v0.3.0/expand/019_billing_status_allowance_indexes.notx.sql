-- Keep the full billing-status allowance aggregates bounded by tenant, meter,
-- value semantics, granularity/status, and billing-period overlap. These run in
-- autocommit mode so PostgreSQL can build them without blocking writes.

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_purser_usage_records_allowance
    ON purser.usage_records(tenant_id, usage_type, value_kind, granularity, period_start, period_end);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_usage_adjustments_allowance
    ON purser.usage_adjustments(tenant_id, usage_type, value_kind, status, period_start, period_end);

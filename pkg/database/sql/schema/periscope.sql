-- ============================================================================
-- PERISCOPE SCHEMA - ANALYTICS & METRICS AGGREGATION
-- ============================================================================
-- Manages PostgreSQL-based stream analytics aggregation and real-time metrics
-- Complements ClickHouse time-series data with relational analytics
-- ============================================================================

CREATE SCHEMA IF NOT EXISTS periscope;

CREATE TABLE IF NOT EXISTS periscope.delegated_jwt_replays (
    jti TEXT PRIMARY KEY,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_periscope_delegated_jwt_replays_expires_at
    ON periscope.delegated_jwt_replays (expires_at);

-- ============================================================================
-- EXTENSIONS
-- ============================================================================

CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- ============================================================================
-- BILLING CURSORS
-- ============================================================================

-- Tracks the last acknowledged window for one logical ClickHouse source and
-- tenant. Worker replicas share a source_id and compete through the fenced
-- lease below; distinct regional ClickHouse deployments never share cursors.
CREATE TABLE IF NOT EXISTS periscope.billing_cursors (
    source_id VARCHAR(128) NOT NULL,
    tenant_id UUID NOT NULL,
    last_processed_at TIMESTAMP WITH TIME ZONE NOT NULL,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    PRIMARY KEY (source_id, tenant_id)
);

CREATE TABLE IF NOT EXISTS periscope.metering_leases (
    source_id VARCHAR(128) NOT NULL,
    partition_key VARCHAR(128) NOT NULL,
    owner_id VARCHAR(128) NOT NULL,
    fencing_token BIGINT NOT NULL DEFAULT 1,
    lease_until TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (source_id, partition_key)
);

-- Immutable activation boundary for one logical ClickHouse metering source.
-- New tenant cursors may look back to their first fact, but never before this
-- boundary, so enabling v0.3 metering cannot replay historical analytics.
CREATE TABLE IF NOT EXISTS periscope.metering_sources (
    source_id VARCHAR(128) PRIMARY KEY,
	source_region VARCHAR(128) NOT NULL DEFAULT '',
	activated_at TIMESTAMPTZ NOT NULL,
	completed_through TIMESTAMPTZ,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS periscope.metering_reservation_keys (
    source_id VARCHAR(128) NOT NULL,
    tenant_id UUID NOT NULL,
    cluster_id VARCHAR(100) NOT NULL,
    last_sequence BIGINT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (source_id, tenant_id, cluster_id)
);

-- Schema baseline identity marker. Records that this database was created from the
-- consolidated baseline at this floor, so the migration min-version guard treats
-- below-floor migrations as folded into the baseline (not missing). An existing
-- cluster upgraded in place has no marker and is checked for ledger completeness
-- instead. The floor value is kept in sync with provisioner.schemaMigrationBaselineFloor
-- by TestBaselineMarkerFloorMatchesConst. See docs/standards/schema-migrations.md.
CREATE TABLE IF NOT EXISTS public._schema_baseline (
    floor TEXT NOT NULL,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO public._schema_baseline (floor)
    SELECT 'v0.3.0' WHERE NOT EXISTS (SELECT 1 FROM public._schema_baseline);

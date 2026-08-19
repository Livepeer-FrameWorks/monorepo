-- ============================================================================
-- PERISCOPE SCHEMA - ANALYTICS & METRICS AGGREGATION
-- ============================================================================
-- Manages PostgreSQL-based stream analytics aggregation and real-time metrics
-- Complements ClickHouse time-series data with relational analytics
-- ============================================================================

CREATE SCHEMA IF NOT EXISTS periscope;

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

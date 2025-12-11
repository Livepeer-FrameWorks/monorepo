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

-- Tracks the last processed timestamp for billing aggregation per tenant
-- Ensures no data gaps or overlaps when reporting to Purser
CREATE TABLE IF NOT EXISTS periscope.billing_cursors (
    tenant_id UUID PRIMARY KEY,
    last_processed_at TIMESTAMP WITH TIME ZONE NOT NULL,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);



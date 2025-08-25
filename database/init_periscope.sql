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
-- STREAM ANALYTICS AGGREGATION
-- ============================================================================

-- Real-time stream analytics with session-based aggregation
CREATE TABLE IF NOT EXISTS periscope.stream_analytics (
    -- ===== IDENTITY =====
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    stream_id UUID,
    internal_name VARCHAR(255) NOT NULL,
    
    -- ===== SESSION LIFECYCLE =====
    session_start_time TIMESTAMP,
    session_end_time TIMESTAMP,
    total_session_duration INTEGER DEFAULT 0, -- Seconds
    
    -- ===== VIEWER METRICS =====
    current_viewers INTEGER DEFAULT 0,
    peak_viewers INTEGER DEFAULT 0,
    total_connections INTEGER DEFAULT 0,
    
    -- ===== BANDWIDTH METRICS =====
    bandwidth_in BIGINT DEFAULT 0,      -- Bytes received
    bandwidth_out BIGINT DEFAULT 0,     -- Bytes sent
    total_bandwidth_gb DECIMAL(15,6) DEFAULT 0,
    
    -- ===== STREAM QUALITY =====
    bitrate_kbps INTEGER,
    resolution VARCHAR(50),
    current_codec VARCHAR(50),
    current_fps DECIMAL(5,2),
    
    -- ===== NETWORK PERFORMANCE =====
    packets_sent BIGINT DEFAULT 0,
    packets_lost BIGINT DEFAULT 0,
    packets_retrans BIGINT DEFAULT 0,
    upbytes BIGINT DEFAULT 0,
    downbytes BIGINT DEFAULT 0,
    first_ms BIGINT,                    -- First packet timestamp
    last_ms BIGINT,                     -- Last packet timestamp
    
    -- ===== STREAM CONFIGURATION =====
    track_count INTEGER DEFAULT 0,
    inputs INTEGER DEFAULT 0,
    outputs INTEGER DEFAULT 0,
    
    -- ===== HEALTH & STATUS =====
    current_health_score DECIMAL(3,2),
    current_buffer_state VARCHAR(20),   -- FULL, EMPTY, DRY, RECOVER
    current_issues TEXT,
    status VARCHAR(50) DEFAULT 'offline', -- offline, live, terminated
    mist_status VARCHAR(50),            -- MistServer internal status
    
    -- ===== METADATA =====
    track_details JSONB,                -- Track/codec information
    health_data JSONB,                  -- Detailed health metrics
    
    -- ===== GEOGRAPHIC DATA =====
    node_id VARCHAR(100),
    node_name VARCHAR(255),
    latitude DECIMAL(10, 8),
    longitude DECIMAL(11, 8),
    location VARCHAR(255),
    
    last_updated TIMESTAMP DEFAULT NOW(),
    created_at TIMESTAMP DEFAULT NOW(),
    UNIQUE(tenant_id, internal_name)
);

-- ============================================================================
-- STREAM ANALYTICS INDEXES
-- ============================================================================

CREATE UNIQUE INDEX IF NOT EXISTS ux_periscope_stream_analytics_tenant_stream_id ON periscope.stream_analytics(tenant_id, stream_id);
CREATE INDEX IF NOT EXISTS idx_periscope_stream_analytics_tenant_id ON periscope.stream_analytics(tenant_id);
CREATE INDEX IF NOT EXISTS idx_periscope_stream_analytics_internal_name ON periscope.stream_analytics(internal_name);
CREATE INDEX IF NOT EXISTS idx_periscope_stream_analytics_tenant_internal ON periscope.stream_analytics(tenant_id, internal_name);
CREATE INDEX IF NOT EXISTS idx_periscope_stream_analytics_status ON periscope.stream_analytics(tenant_id, status);
CREATE INDEX IF NOT EXISTS idx_periscope_stream_analytics_last_updated ON periscope.stream_analytics(tenant_id, last_updated);



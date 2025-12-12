-- ============================================================================
-- FOGHORN SCHEMA - LOAD BALANCING, CLIPS & STORAGE ORCHESTRATION
-- ============================================================================
-- Manages clips, storage policies, node outputs cache, and DVR requests
-- Core load balancing and content delivery orchestration
-- ============================================================================

CREATE SCHEMA IF NOT EXISTS foghorn;

-- ============================================================================
-- EXTENSIONS
-- ============================================================================

CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- ============================================================================
-- CLIP MANAGEMENT & STORAGE
-- ============================================================================

-- Clip requests and storage tracking with lifecycle management
CREATE TABLE IF NOT EXISTS foghorn.clips (
    -- ===== IDENTITY =====
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    stream_id UUID NOT NULL,
    user_id UUID NOT NULL,
    clip_hash VARCHAR(32) UNIQUE NOT NULL,
    
    -- ===== CLIP DEFINITION =====
    stream_name VARCHAR(255) NOT NULL,
    title VARCHAR(255),
    description TEXT,            -- User-provided description
    start_time BIGINT NOT NULL,  -- Resolved start time (Unix timestamp in milliseconds)
    duration BIGINT NOT NULL,    -- Resolved duration in milliseconds

    -- ===== CLIP CREATION MODE & ORIGINAL PARAMS =====
    clip_mode VARCHAR(20) DEFAULT 'absolute', -- absolute, relative, duration, clip_now
    requested_params JSONB,      -- Original request params for audit (start_unix, stop_unix, etc.)

    -- ===== STORAGE LOCATION =====
    node_id VARCHAR(100),        -- Storage node assignment
    storage_path VARCHAR(500),   -- File path on storage node
    base_url VARCHAR(500),       -- Base URL for access
    size_bytes BIGINT,           -- File size after processing
    
    -- ===== PROCESSING STATUS =====
    status VARCHAR(50) DEFAULT 'requested', -- requested, processing, ready, failed
    error_message TEXT,
    request_id UUID,             -- Original request tracking
    
    -- ===== ACCESS & RETENTION =====
    access_count INTEGER DEFAULT 0,
    last_accessed_at TIMESTAMP,
    retention_until TIMESTAMP,   -- Automatic cleanup date
    
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

-- ============================================================================
-- CLIP INDEXES
-- ============================================================================

CREATE INDEX IF NOT EXISTS idx_foghorn_clips_tenant ON foghorn.clips(tenant_id);
CREATE INDEX IF NOT EXISTS idx_foghorn_clips_stream ON foghorn.clips(stream_id);
CREATE INDEX IF NOT EXISTS idx_foghorn_clips_user ON foghorn.clips(user_id);
CREATE INDEX IF NOT EXISTS idx_foghorn_clips_hash ON foghorn.clips(clip_hash);
CREATE INDEX IF NOT EXISTS idx_foghorn_clips_node ON foghorn.clips(node_id);
CREATE INDEX IF NOT EXISTS idx_foghorn_clips_status ON foghorn.clips(status);
CREATE INDEX IF NOT EXISTS idx_foghorn_clips_created_at ON foghorn.clips(created_at);
CREATE INDEX IF NOT EXISTS idx_foghorn_clips_retention ON foghorn.clips(retention_until);
CREATE INDEX IF NOT EXISTS idx_foghorn_clips_request_id ON foghorn.clips(request_id);
CREATE INDEX IF NOT EXISTS idx_foghorn_clips_mode ON foghorn.clips(clip_mode);

-- ============================================================================
-- NODE OUTPUT CACHING & LOAD BALANCING
-- ============================================================================

-- Cached node output configurations for load balancing decisions
CREATE TABLE IF NOT EXISTS foghorn.node_outputs (
    -- ===== IDENTITY =====
    node_id VARCHAR(100) PRIMARY KEY,
    
    -- ===== CACHED DATA =====
    outputs JSONB NOT NULL,     -- MistServer output configuration
    base_url VARCHAR(500),      -- Node base URL for routing
    last_updated TIMESTAMP DEFAULT NOW()
);

-- ============================================================================
-- ARTIFACT REGISTRY & STORAGE TRACKING
-- ============================================================================

-- Registry of stored artifacts across storage nodes
CREATE TABLE IF NOT EXISTS foghorn.artifact_registry (
    -- ===== IDENTITY =====
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    node_id VARCHAR(100) NOT NULL,
    clip_hash VARCHAR(32) NOT NULL,
    
    -- ===== ARTIFACT DETAILS =====
    stream_name VARCHAR(255),
    file_path VARCHAR(500),
    size_bytes BIGINT,
    
    -- ===== LIFECYCLE TRACKING =====
    created_at TIMESTAMP,
    last_seen_at TIMESTAMP DEFAULT NOW(),
    is_orphaned BOOLEAN DEFAULT false, -- No longer referenced
    
    UNIQUE(node_id, clip_hash)
);

-- ============================================================================
-- ARTIFACT REGISTRY INDEXES
-- ============================================================================

CREATE INDEX IF NOT EXISTS idx_foghorn_artifact_registry_node ON foghorn.artifact_registry(node_id);
CREATE INDEX IF NOT EXISTS idx_foghorn_artifact_registry_hash ON foghorn.artifact_registry(clip_hash);
CREATE INDEX IF NOT EXISTS idx_foghorn_artifact_registry_orphaned ON foghorn.artifact_registry(is_orphaned);
CREATE INDEX IF NOT EXISTS idx_foghorn_artifact_registry_last_seen ON foghorn.artifact_registry(last_seen_at);

-- Node output cache index
CREATE INDEX IF NOT EXISTS idx_foghorn_node_outputs_updated ON foghorn.node_outputs(last_updated);

-- Full node lifecycle snapshot storage for readiness and audit
CREATE TABLE IF NOT EXISTS foghorn.node_lifecycle (
    node_id VARCHAR(100) PRIMARY KEY,
    lifecycle JSONB NOT NULL,
    last_updated TIMESTAMP DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_foghorn_node_lifecycle_updated ON foghorn.node_lifecycle(last_updated);

-- ============================================================================
-- DVR (DIGITAL VIDEO RECORDING) REQUESTS
-- ============================================================================

-- DVR recording requests with minimal state tracking in Foghorn
CREATE TABLE IF NOT EXISTS foghorn.dvr_requests (
    -- ===== IDENTITY =====
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    request_hash VARCHAR(32) UNIQUE NOT NULL, -- Unique request identifier
    tenant_id UUID NOT NULL,
    stream_id UUID,
    internal_name VARCHAR(255) NOT NULL,
    
    -- ===== STORAGE ASSIGNMENT =====
    storage_node_id UUID,       -- Assigned storage node
    storage_node_url VARCHAR(255),
    
    -- ===== STATUS TRACKING =====
    status VARCHAR(50) DEFAULT 'requested', -- requested, recording, completed, failed
    error_message TEXT,
    
    -- ===== RECORDING METRICS =====
    started_at TIMESTAMP,       -- Recording start time
    ended_at TIMESTAMP,         -- Recording end time
    duration_seconds INTEGER,   -- Total recording duration
    size_bytes BIGINT,          -- Final file size
    manifest_path VARCHAR(500), -- HLS/DASH manifest for VOD playback
    
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

-- ============================================================================
-- DVR REQUEST INDEXES
-- ============================================================================

CREATE INDEX IF NOT EXISTS idx_dvr_requests_stream ON foghorn.dvr_requests(internal_name, status);
CREATE INDEX IF NOT EXISTS idx_dvr_requests_hash ON foghorn.dvr_requests(request_hash);
CREATE INDEX IF NOT EXISTS idx_dvr_requests_node ON foghorn.dvr_requests(storage_node_id);
CREATE INDEX IF NOT EXISTS idx_dvr_requests_tenant ON foghorn.dvr_requests(tenant_id);
CREATE INDEX IF NOT EXISTS idx_dvr_requests_status ON foghorn.dvr_requests(status);


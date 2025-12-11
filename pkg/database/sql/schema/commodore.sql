-- ============================================================================
-- COMMODORE SCHEMA - CONTROL PLANE & USER MANAGEMENT
-- ============================================================================
-- Manages users, streams, recordings, sessions, and API tokens
-- Core control plane for tenant business operations and content management
-- ============================================================================

CREATE SCHEMA IF NOT EXISTS commodore;

-- ============================================================================
-- EXTENSIONS
-- ============================================================================

CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- ============================================================================
-- USER MANAGEMENT & AUTHENTICATION
-- ============================================================================

-- User accounts with authentication and profile information
CREATE TABLE IF NOT EXISTS commodore.users (
    -- ===== IDENTITY =====
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    email VARCHAR(255) NOT NULL,
    
    -- ===== AUTHENTICATION =====
    password_hash VARCHAR(255) NOT NULL,
    verified BOOLEAN DEFAULT FALSE,
    verification_token VARCHAR(255),
    token_expires_at TIMESTAMP,
    
    -- ===== PROFILE =====
    first_name VARCHAR(100),
    last_name VARCHAR(100),
    
    -- ===== AUTHORIZATION =====
    role VARCHAR(50) DEFAULT 'member',
    permissions TEXT[] DEFAULT ARRAY['streams:read'],
    
    -- ===== STATUS & ACTIVITY =====
    is_active BOOLEAN DEFAULT TRUE,
    last_login_at TIMESTAMP,
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW(),
    UNIQUE(tenant_id, email)
);

-- Secure refresh tokens for session management
CREATE TABLE IF NOT EXISTS commodore.refresh_tokens (
    -- ===== IDENTITY =====
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    user_id UUID NOT NULL,
    token_hash VARCHAR(64) NOT NULL, -- SHA256 hash of the actual token

    -- ===== LIFECYCLE =====
    expires_at TIMESTAMP NOT NULL,
    created_at TIMESTAMP DEFAULT NOW(),
    revoked BOOLEAN DEFAULT FALSE
);

CREATE INDEX IF NOT EXISTS idx_refresh_tokens_user_id ON commodore.refresh_tokens(user_id);
CREATE INDEX IF NOT EXISTS idx_refresh_tokens_token_hash ON commodore.refresh_tokens(token_hash);

-- API tokens for programmatic access
CREATE TABLE IF NOT EXISTS commodore.api_tokens (
    -- ===== IDENTITY =====
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    user_id UUID NOT NULL,
    token_value VARCHAR(100) UNIQUE NOT NULL,  -- "fw_" + 64 hex chars = 67 chars
    token_name VARCHAR(255) NOT NULL,

    -- ===== AUTHORIZATION =====
    permissions TEXT[] DEFAULT ARRAY['read'],

    -- ===== STATUS =====
    is_active BOOLEAN DEFAULT TRUE,
    last_used_at TIMESTAMP,
    expires_at TIMESTAMP,
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_commodore_users_tenant_id ON commodore.users(tenant_id);
CREATE INDEX IF NOT EXISTS idx_commodore_refresh_tokens_tenant_id ON commodore.refresh_tokens(tenant_id);
CREATE INDEX IF NOT EXISTS idx_commodore_api_tokens_tenant_id ON commodore.api_tokens(tenant_id);

-- ============================================================================
-- STREAM MANAGEMENT
-- ============================================================================

-- Live streams with metadata, settings, and current status
CREATE TABLE IF NOT EXISTS commodore.streams (
    -- ===== IDENTITY =====
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    user_id UUID NOT NULL,
    
    -- ===== STREAM IDENTIFIERS =====
    stream_key VARCHAR(255) UNIQUE NOT NULL,    -- For RTMP ingest
    playback_id VARCHAR(255) UNIQUE NOT NULL,   -- For HLS/DASH playback
    internal_name VARCHAR(255) UNIQUE NOT NULL, -- MistServer internal reference
    
    -- ===== CONTENT METADATA =====
    title VARCHAR(255) NOT NULL,
    description TEXT,
    
    -- ===== DVR RECORDING =====
    is_recording_enabled BOOLEAN DEFAULT FALSE,

    -- NOTE: Operational state (status, start_time, end_time) removed
    -- Stream status now comes from Periscope Data Plane via ClickHouse analytics
    -- See: Control/Data Plane separation migration

    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

-- Stream authentication keys (multiple keys per stream for rotation)
CREATE TABLE IF NOT EXISTS commodore.stream_keys (
    -- ===== IDENTITY =====
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    user_id UUID,
    stream_id UUID NOT NULL,
    
    -- ===== KEY DETAILS =====
    key_value VARCHAR(255) UNIQUE NOT NULL,
    key_name VARCHAR(100),
    
    -- ===== STATUS & USAGE =====
    is_active BOOLEAN DEFAULT TRUE,
    last_used_at TIMESTAMP,
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

-- ============================================================================
-- STREAM INDEXES
-- ============================================================================

CREATE INDEX IF NOT EXISTS idx_commodore_streams_tenant_id ON commodore.streams(tenant_id);
CREATE INDEX IF NOT EXISTS idx_commodore_streams_internal_name ON commodore.streams(internal_name);
CREATE INDEX IF NOT EXISTS idx_commodore_stream_keys_stream_id ON commodore.stream_keys(stream_id);

-- ============================================================================
-- UTILITY FUNCTIONS
-- ============================================================================

-- Generate random alphanumeric strings for keys and tokens
CREATE OR REPLACE FUNCTION commodore.generate_random_string(length INTEGER) RETURNS TEXT AS $$
DECLARE
    chars TEXT := 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789';
    result TEXT := '';
    i INTEGER := 0;
BEGIN
    FOR i IN 1..length LOOP
        result := result || substr(chars, floor(random() * length(chars) + 1)::INTEGER, 1);
    END LOOP;
    RETURN result;
END;
$$ LANGUAGE plpgsql;

-- Create a new stream with generated keys and identifiers
CREATE OR REPLACE FUNCTION commodore.create_user_stream(p_tenant_id UUID, p_user_id UUID, p_title VARCHAR DEFAULT 'My Stream') 
RETURNS TABLE(stream_id UUID, stream_key VARCHAR, playback_id VARCHAR, internal_name VARCHAR) AS $$
DECLARE
    new_stream_id UUID;
    new_stream_key VARCHAR(32);
    new_playback_id VARCHAR(16);
    new_internal_name VARCHAR(64);
BEGIN
    -- Generate unique identifiers
    new_stream_id := gen_random_uuid();
    new_stream_key := 'sk_' || commodore.generate_random_string(28);
    new_playback_id := commodore.generate_random_string(16);
    new_internal_name := new_stream_id::TEXT;

    -- Create stream record
    INSERT INTO commodore.streams (id, tenant_id, user_id, stream_key, playback_id, internal_name, title)
    VALUES (new_stream_id, p_tenant_id, p_user_id, new_stream_key, new_playback_id, new_internal_name, p_title);

    -- Create primary stream key
    INSERT INTO commodore.stream_keys (tenant_id, user_id, stream_id, key_value, key_name, is_active)
    VALUES (p_tenant_id, p_user_id, new_stream_id, new_stream_key, 'Primary Key', TRUE);

    RETURN QUERY SELECT new_stream_id, new_stream_key, new_playback_id, new_internal_name;
END;
$$ LANGUAGE plpgsql;

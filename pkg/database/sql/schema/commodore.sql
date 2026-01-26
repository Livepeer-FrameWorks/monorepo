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
CREATE EXTENSION IF NOT EXISTS "citext";

-- ============================================================================
-- USER MANAGEMENT & AUTHENTICATION
-- ============================================================================

-- User accounts with authentication and profile information
-- Supports both email/password and wallet-based authentication
CREATE TABLE IF NOT EXISTS commodore.users (
    -- ===== IDENTITY =====
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    email CITEXT,                                 -- NULL for wallet-only accounts

    -- ===== AUTHENTICATION =====
    password_hash VARCHAR(255),                   -- NULL for wallet-only accounts
    verified BOOLEAN DEFAULT FALSE,
    verification_token VARCHAR(255),
    token_expires_at TIMESTAMP,
    reset_token VARCHAR(255),
    reset_token_expires TIMESTAMP,

    -- ===== PROFILE =====
    first_name VARCHAR(100),
    last_name VARCHAR(100),

    -- ===== AUTHORIZATION =====
    role VARCHAR(50) DEFAULT 'member',
    permissions TEXT[] DEFAULT ARRAY['streams:read'],

    -- ===== STATUS & ACTIVITY =====
    is_active BOOLEAN DEFAULT TRUE,
    newsletter_subscribed BOOLEAN DEFAULT TRUE,
    last_login_at TIMESTAMP,
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW(),
    UNIQUE(email)
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
    token_value VARCHAR(100) UNIQUE NOT NULL,  -- SHA-256 hash of token ("fw_" + 64 hex chars input)
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
CREATE INDEX IF NOT EXISTS idx_commodore_users_reset_token ON commodore.users(reset_token) WHERE reset_token IS NOT NULL;

-- ============================================================================
-- WALLET-BASED IDENTITY (Agent Access / x402)
-- ============================================================================
-- Enables wallet-based authentication as alternative to email/password.
-- Supports multiple blockchain types (Ethereum, Solana, Bitcoin, etc.)
-- See: docs/rfcs/agent-access.md
-- ============================================================================

-- Wallet identities linked to users (chain-agnostic design)
CREATE TABLE IF NOT EXISTS commodore.wallet_identities (
    -- ===== IDENTITY =====
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    wallet_address VARCHAR(128) NOT NULL,        -- Chain-specific format (e.g., 0x... for Ethereum)
    chain_type VARCHAR(20) NOT NULL,             -- 'ethereum', 'solana', 'bitcoin', etc.

    -- ===== OWNERSHIP =====
    tenant_id UUID NOT NULL,
    user_id UUID NOT NULL REFERENCES commodore.users(id) ON DELETE CASCADE,

    -- ===== LIFECYCLE =====
    created_at TIMESTAMP DEFAULT NOW(),
    last_auth_at TIMESTAMP,

    -- Composite unique: same address can exist on different chains
    UNIQUE(chain_type, wallet_address)
);

CREATE INDEX IF NOT EXISTS idx_commodore_wallet_chain_address
    ON commodore.wallet_identities(chain_type, wallet_address);
CREATE INDEX IF NOT EXISTS idx_commodore_wallet_tenant
    ON commodore.wallet_identities(tenant_id);
CREATE INDEX IF NOT EXISTS idx_commodore_wallet_user
    ON commodore.wallet_identities(user_id);

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
    stream_key CITEXT UNIQUE NOT NULL,          -- For RTMP ingest
    playback_id CITEXT UNIQUE NOT NULL,         -- For HLS/DASH playback
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
CREATE INDEX IF NOT EXISTS idx_commodore_streams_tenant_user_created_internal
    ON commodore.streams(tenant_id, user_id, created_at, internal_name);
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
    new_internal_name := commodore.generate_random_string(32);  -- Independent random string (not derivable from stream_id)

    -- Create stream record
    INSERT INTO commodore.streams (id, tenant_id, user_id, stream_key, playback_id, internal_name, title)
    VALUES (new_stream_id, p_tenant_id, p_user_id, new_stream_key, new_playback_id, new_internal_name, p_title);

    -- Create primary stream key
    INSERT INTO commodore.stream_keys (tenant_id, user_id, stream_id, key_value, key_name, is_active)
    VALUES (p_tenant_id, p_user_id, new_stream_id, new_stream_key, 'Primary Key', TRUE);

    RETURN QUERY SELECT new_stream_id, new_stream_key, new_playback_id, new_internal_name;
END;
$$ LANGUAGE plpgsql;

-- ============================================================================
-- CLIP & DVR REGISTRY (BUSINESS METADATA)
-- ============================================================================
-- Business registry for clips and DVR recordings.
-- Lifecycle/storage state is managed by Foghorn (foghorn.artifacts).
-- See: docs/architecture/CLIP_DVR_REGISTRY.md
-- ============================================================================

-- Clip business registry (metadata only, lifecycle in Foghorn)
CREATE TABLE IF NOT EXISTS commodore.clips (
    -- ===== IDENTITY =====
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    user_id UUID NOT NULL,
    stream_id UUID NOT NULL,
    clip_hash VARCHAR(32) UNIQUE NOT NULL,  -- Generated by Commodore
    artifact_internal_name VARCHAR(64) UNIQUE NOT NULL, -- Opaque routing name (not stream internal_name)
    playback_id CITEXT UNIQUE NOT NULL,     -- Public view key (artifact playback ID)

    -- ===== METADATA =====
    title VARCHAR(255),
    description TEXT,

    -- ===== CLIP DEFINITION =====
    start_time BIGINT NOT NULL,             -- Unix timestamp (ms)
    duration BIGINT NOT NULL,               -- Duration (ms)
    clip_mode VARCHAR(20) DEFAULT 'absolute', -- absolute, relative, duration, clip_now
    requested_params JSONB,                 -- Original request for audit

    -- ===== LIFECYCLE =====
    retention_until TIMESTAMP,
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_commodore_clips_tenant ON commodore.clips(tenant_id);
CREATE INDEX IF NOT EXISTS idx_commodore_clips_stream ON commodore.clips(stream_id);
CREATE INDEX IF NOT EXISTS idx_commodore_clips_user ON commodore.clips(user_id);
CREATE INDEX IF NOT EXISTS idx_commodore_clips_hash ON commodore.clips(clip_hash);
CREATE INDEX IF NOT EXISTS idx_commodore_clips_internal ON commodore.clips(artifact_internal_name);
CREATE INDEX IF NOT EXISTS idx_commodore_clips_playback ON commodore.clips(playback_id);
CREATE INDEX IF NOT EXISTS idx_commodore_clips_created ON commodore.clips(created_at);

-- DVR recording business registry (metadata only, lifecycle in Foghorn)
CREATE TABLE IF NOT EXISTS commodore.dvr_recordings (
    -- ===== IDENTITY =====
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    user_id UUID NOT NULL,
    stream_id UUID,
    dvr_hash VARCHAR(32) UNIQUE NOT NULL,   -- Generated by Commodore
    artifact_internal_name VARCHAR(64) UNIQUE NOT NULL, -- Opaque routing name (not stream internal_name)
    playback_id CITEXT UNIQUE NOT NULL,     -- Public view key (artifact playback ID)

    -- ===== METADATA =====
    internal_name VARCHAR(255) NOT NULL,    -- Stream name for MistServer

    -- ===== LIFECYCLE =====
    retention_until TIMESTAMP,
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_commodore_dvr_tenant ON commodore.dvr_recordings(tenant_id);
CREATE INDEX IF NOT EXISTS idx_commodore_dvr_stream ON commodore.dvr_recordings(stream_id);
CREATE INDEX IF NOT EXISTS idx_commodore_dvr_user ON commodore.dvr_recordings(user_id);
CREATE INDEX IF NOT EXISTS idx_commodore_dvr_hash ON commodore.dvr_recordings(dvr_hash);
CREATE INDEX IF NOT EXISTS idx_commodore_dvr_internal ON commodore.dvr_recordings(internal_name);
CREATE INDEX IF NOT EXISTS idx_commodore_dvr_artifact_internal ON commodore.dvr_recordings(artifact_internal_name);
CREATE INDEX IF NOT EXISTS idx_commodore_dvr_playback ON commodore.dvr_recordings(playback_id);
CREATE INDEX IF NOT EXISTS idx_commodore_dvr_created ON commodore.dvr_recordings(created_at);

-- Generate clip hash (deterministic based on stream + timing)
CREATE OR REPLACE FUNCTION commodore.generate_clip_hash(
    p_stream_id UUID,
    p_start_time BIGINT,
    p_duration BIGINT
) RETURNS VARCHAR(32) AS $$
BEGIN
    RETURN encode(
        digest(
            p_stream_id::TEXT || ':' || p_start_time::TEXT || ':' || p_duration::TEXT || ':' || extract(epoch from now())::TEXT,
            'md5'
        ),
        'hex'
    );
END;
$$ LANGUAGE plpgsql;

-- Generate DVR hash (deterministic based on stream + timestamp)
CREATE OR REPLACE FUNCTION commodore.generate_dvr_hash(
    p_stream_id UUID,
    p_internal_name VARCHAR
) RETURNS VARCHAR(32) AS $$
BEGIN
    RETURN encode(
        digest(
            COALESCE(p_stream_id::TEXT, '') || ':' || p_internal_name || ':' || extract(epoch from now())::TEXT,
            'md5'
        ),
        'hex'
    );
END;
$$ LANGUAGE plpgsql;

-- ============================================================================
-- VOD ASSET REGISTRY (BUSINESS METADATA)
-- ============================================================================
-- Business registry for user-uploaded video files (VOD).
-- Unlike clips/DVR, VOD assets are user-initiated uploads, not stream-derived.
-- Lifecycle/storage state is managed by Foghorn (foghorn.artifacts + foghorn.vod_metadata).
-- ============================================================================

-- VOD business registry (metadata only, lifecycle/upload state in Foghorn)
CREATE TABLE IF NOT EXISTS commodore.vod_assets (
    -- ===== IDENTITY =====
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    user_id UUID NOT NULL,
    vod_hash VARCHAR(32) UNIQUE NOT NULL,   -- Generated by Commodore
    artifact_internal_name VARCHAR(64) UNIQUE NOT NULL, -- Opaque routing name (not stream internal_name)
    playback_id CITEXT UNIQUE NOT NULL,     -- Public view key (artifact playback ID)

    -- ===== METADATA =====
    title VARCHAR(255),
    description TEXT,
    filename VARCHAR(255) NOT NULL,         -- Original uploaded filename
    content_type VARCHAR(100),              -- MIME type (video/mp4, etc.)

    -- ===== SIZE =====
    size_bytes BIGINT,                      -- Expected file size

    -- ===== LIFECYCLE =====
    retention_until TIMESTAMP,
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_commodore_vod_tenant ON commodore.vod_assets(tenant_id);
CREATE INDEX IF NOT EXISTS idx_commodore_vod_user ON commodore.vod_assets(user_id);
CREATE INDEX IF NOT EXISTS idx_commodore_vod_hash ON commodore.vod_assets(vod_hash);
CREATE INDEX IF NOT EXISTS idx_commodore_vod_internal ON commodore.vod_assets(artifact_internal_name);
CREATE INDEX IF NOT EXISTS idx_commodore_vod_playback ON commodore.vod_assets(playback_id);
CREATE INDEX IF NOT EXISTS idx_commodore_vod_created ON commodore.vod_assets(created_at);

-- Generate VOD hash (includes tenant + user + filename + timestamp for uniqueness)
CREATE OR REPLACE FUNCTION commodore.generate_vod_hash(
    p_tenant_id UUID,
    p_user_id UUID,
    p_filename VARCHAR
) RETURNS VARCHAR(32) AS $$
BEGIN
    RETURN encode(
        digest(
            p_tenant_id::TEXT || ':' || p_user_id::TEXT || ':' || p_filename || ':' || extract(epoch from now())::TEXT,
            'md5'
        ),
        'hex'
    );
END;
$$ LANGUAGE plpgsql;


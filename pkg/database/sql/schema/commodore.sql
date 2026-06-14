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
    platform_operator BOOLEAN NOT NULL DEFAULT FALSE, -- platform staff grant (RFC 9068 platform_operator role)

    -- ===== STATUS & ACTIVITY =====
    is_active BOOLEAN DEFAULT TRUE,
    last_login_at TIMESTAMP,
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
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
    rotated_at TIMESTAMP,
    revoked BOOLEAN DEFAULT FALSE,
    replaced_by UUID -- successor issued by rotation; reuse with an unused successor = lost response, not theft
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
CREATE UNIQUE INDEX IF NOT EXISTS idx_commodore_users_email_ci
    ON commodore.users((lower(email::text)))
    WHERE email IS NOT NULL;

-- ============================================================================
-- BROWSER-HANDOFF + DEVICE LOGIN (Tray / CLI)
-- ============================================================================
-- Replaces native email/password forms in non-browser clients (the macOS tray
-- and the CLI) so Cloudflare Turnstile keeps protecting the password surface.
-- Tray uses OAuth 2.0 Authorization Code with PKCE over a loopback redirect
-- (RFC 8252 + RFC 7636). CLI uses the Device Authorization Grant (RFC 8628).
-- Existing commodore.api_tokens remain the automation escape hatch.

-- PKCE authorization codes. Single-use; hashed at rest. consumed_at is set
-- in the same transaction that issues the JWT.
CREATE TABLE IF NOT EXISTS commodore.auth_authorization_codes (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id             UUID NOT NULL,
    user_id               UUID NOT NULL,
    client_id             VARCHAR(64) NOT NULL,
    code_hash             VARCHAR(64) NOT NULL,  -- SHA-256 hex of raw code
    code_challenge        VARCHAR(128) NOT NULL,
    code_challenge_method VARCHAR(16) NOT NULL,
    redirect_uri          TEXT NOT NULL,
    scope                 VARCHAR(64) NOT NULL DEFAULT 'account',
    state                 TEXT,
    expires_at            TIMESTAMP NOT NULL,
    consumed_at           TIMESTAMP,
    created_at            TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_commodore_auth_authz_codes_hash
    ON commodore.auth_authorization_codes(code_hash);
CREATE INDEX IF NOT EXISTS idx_commodore_auth_authz_codes_tenant
    ON commodore.auth_authorization_codes(tenant_id);
CREATE INDEX IF NOT EXISTS idx_commodore_auth_authz_codes_expires
    ON commodore.auth_authorization_codes(expires_at)
    WHERE consumed_at IS NULL;

-- Device Authorization Grant state. user_id/tenant_id stay NULL until the
-- user confirms the user_code in a browser.
CREATE TABLE IF NOT EXISTS commodore.auth_device_codes (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id             UUID,
    user_id               UUID,
    client_id             VARCHAR(64) NOT NULL,
    device_code_hash      VARCHAR(64) NOT NULL,  -- SHA-256 hex of raw device_code
    user_code             VARCHAR(32) NOT NULL,
    scope                 VARCHAR(64) NOT NULL DEFAULT 'account',
    status                VARCHAR(16) NOT NULL DEFAULT 'pending'
                          CHECK (status IN ('pending', 'approved', 'denied', 'expired')),
    poll_interval_seconds INTEGER NOT NULL DEFAULT 5,
    last_polled_at        TIMESTAMP,
    expires_at            TIMESTAMP NOT NULL,
    approved_at           TIMESTAMP,
    created_at            TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_commodore_auth_device_codes_device_hash
    ON commodore.auth_device_codes(device_code_hash);
CREATE UNIQUE INDEX IF NOT EXISTS idx_commodore_auth_device_codes_user_code
    ON commodore.auth_device_codes(user_code);
CREATE INDEX IF NOT EXISTS idx_commodore_auth_device_codes_user
    ON commodore.auth_device_codes(user_id)
    WHERE user_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_commodore_auth_device_codes_expires
    ON commodore.auth_device_codes(expires_at)
    WHERE status = 'pending';

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
    stream_key CITEXT NOT NULL,                 -- For RTMP ingest
    playback_id CITEXT NOT NULL,                -- For HLS/DASH playback
    internal_name VARCHAR(255) UNIQUE NOT NULL, -- MistServer internal reference
    
    -- ===== CONTENT METADATA =====
    title VARCHAR(255) NOT NULL,
    description TEXT,
    
    -- ===== DVR RECORDING =====
    is_recording_enabled BOOLEAN DEFAULT FALSE,

    -- ===== SKIPPER MONITORING =====
    -- Per-stream override for Skipper AI health monitoring. Tri-state:
    --   NULL  = INHERIT (follow the tenant's tier entitlement) -- default
    --   TRUE  = ON  (monitor regardless of billing tier)
    --   FALSE = OFF (never monitor, even if the tenant is entitled)
    monitoring_enabled BOOLEAN,

    -- ===== DVR CHAPTER POLICY =====
    -- Snapshotted onto foghorn.artifacts at StartDVR. NULL = chapters
    -- disabled. Changes take effect on the next recording, not in-flight.
    dvr_chapter_mode             VARCHAR(32)
        CONSTRAINT chk_streams_chapter_mode CHECK (
            dvr_chapter_mode IS NULL
            OR dvr_chapter_mode IN ('window_sized_chapters', 'fixed_interval')
        ),
    dvr_chapter_interval_seconds INTEGER
        CONSTRAINT chk_streams_chapter_interval CHECK (
            dvr_chapter_mode IS DISTINCT FROM 'fixed_interval'
            OR (dvr_chapter_interval_seconds IS NOT NULL
                AND dvr_chapter_interval_seconds >= 3600)
        ),

    -- ===== INGEST MODE =====
    -- 'push':        encoder pushes via RTMP/WHIP/SRT (default).
    -- 'pull':        MistServer pulls from a configured upstream URI; see commodore.stream_pull_sources.
    -- 'mist_native': MistServer generates the source via a literal config
    --                (ts-exec:..., file://..., playlist:...); see commodore.stream_mist_sources.
    --                Concrete config, no wildcard prefix in Mist stream name.
    ingest_mode TEXT NOT NULL DEFAULT 'push'
        CONSTRAINT streams_ingest_mode_chk CHECK (ingest_mode IN ('push', 'pull', 'mist_native')),

    -- ===== ALWAYS-ON =====
    -- When true, Foghorn keeps the stream's input running on the elected
    -- edge regardless of viewer demand.
    always_on BOOLEAN NOT NULL DEFAULT FALSE,

    -- ===== PER-STREAM RETENTION OVERRIDES =====
    -- NULL = inherit tenant default; 0 = no auto-expire (infinite).
    -- VOD uploads aren't stream-bound, so no override here for VOD.
    dvr_retention_days_override  INTEGER,
    clip_retention_days_override INTEGER,

    -- ===== CLUSTER TRACKING =====
    -- Cluster currently sinking the stream's ingest, recorded by Foghorn:
    --   * push streams      → set by ValidateStreamKey on PUSH_REWRITE
    --   * mist_native       → set by RecordStreamActiveCluster after the
    --                          managed-stream Apply is verified by the
    --                          sidecar's Heartbeat-borne applied set
    --                          (cleared by ClearStreamActiveCluster on
    --                          verified retract)
    -- Both writers share the 30 s contended-update guard so a stale claim
    -- cannot overwrite a fresh lease from a different cluster.
    -- Consumed by Commodore to route stream-scoped commands (CreateClip,
    -- etc.) and by ResolvePlaybackID/ResolveInternalName to override the
    -- tenant default origin when set.
    active_ingest_cluster_id VARCHAR(100),
    active_ingest_cluster_updated_at TIMESTAMP,

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
CREATE UNIQUE INDEX IF NOT EXISTS idx_commodore_streams_stream_key_ci
    ON commodore.streams((lower(stream_key::text)));
CREATE UNIQUE INDEX IF NOT EXISTS idx_commodore_streams_playback_id_ci
    ON commodore.streams((lower(playback_id::text)));
CREATE INDEX IF NOT EXISTS idx_commodore_streams_ingest_mode
    ON commodore.streams(ingest_mode) WHERE ingest_mode <> 'push';
CREATE INDEX IF NOT EXISTS idx_commodore_streams_always_on
    ON commodore.streams(ingest_mode) WHERE always_on = TRUE;
CREATE INDEX IF NOT EXISTS idx_commodore_stream_keys_stream_id ON commodore.stream_keys(stream_id);

-- ============================================================================
-- PULL STREAMS — upstream source config for ingest_mode='pull' streams
-- ============================================================================
-- One row per pull stream. Foghorn's STREAM_SOURCE handler and /source resolver
-- look this up via Commodore's ResolvePullSourceByInternalName RPC. source_uri_enc
-- is application-encrypted (same convention as push_targets.target_uri /
-- playback_webhook_secret_enc) because pull URIs may carry credentials in-URI
-- (e.g. rtsp://user:pass@host).
-- ============================================================================

CREATE TABLE IF NOT EXISTS commodore.stream_pull_sources (
    stream_id UUID PRIMARY KEY REFERENCES commodore.streams(id) ON DELETE CASCADE,

    source_uri_enc TEXT NOT NULL,                -- encrypted upstream URI
    enabled BOOLEAN NOT NULL DEFAULT TRUE,

    -- Per-source placement pin. Empty = "any media (edge) cluster" for public
    -- sources; required (non-empty) for private/multicast sources. Distinct
    -- from quartermaster.infrastructure_clusters.allow_private_pull_sources,
    -- which is the cluster-side capability flag. Placement is enforced at
    -- render, bootstrap apply, CreateStream/UpdateStream, viewer routing,
    -- /source, and the STREAM_SOURCE trigger via pkg/pullsource.FilterPlacementClusters.
    allowed_cluster_ids TEXT[] NOT NULL DEFAULT '{}',

    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

-- ============================================================================
-- MIST-NATIVE STREAMS — concrete source config for ingest_mode='mist_native'
-- ============================================================================
-- One row per Mist-native stream. Foghorn reads these on its managed-stream
-- reconciler tick, runs deterministic-hash placement against
-- allowed_cluster_ids, and sends an ApplyManagedStream command on the
-- control channel to the elected node. Sidecar issues mist.AddStreams with
-- the literal source; MistServer's controller spawns the input immediately
-- when always_on is true.
--
-- source_kind discriminates safe input forms. Bootstrap render restricts
-- mist_native streams of every source_kind to the operator/system tenant.
-- ============================================================================

CREATE TABLE IF NOT EXISTS commodore.stream_mist_sources (
    stream_id UUID PRIMARY KEY REFERENCES commodore.streams(id) ON DELETE CASCADE,

    source_spec TEXT NOT NULL,                    -- literal Mist source string
    source_kind TEXT NOT NULL
        CONSTRAINT stream_mist_sources_kind_chk CHECK (source_kind IN ('file', 'playlist', 'exec')),

    placement_count INTEGER NOT NULL DEFAULT 1
        CONSTRAINT stream_mist_sources_placement_chk CHECK (placement_count >= 1),
    -- allowed_cluster_ids currently names exactly one source cluster.
    -- Foghorn elects an edge inside that cluster and records it in
    -- commodore.streams.active_ingest_cluster_id; federation routes viewers
    -- cross-cluster from that elected source. The array shape is retained for
    -- pull-stream symmetry, but the table contract matches the current runtime.
    allowed_cluster_ids TEXT[] NOT NULL
        CONSTRAINT stream_mist_sources_allowed_clusters_chk
            CHECK (cardinality(allowed_cluster_ids) = 1),

    -- Informational: bootstrap declares expected on-disk asset paths; Ansible
    -- places the actual files. Reconcilers do not validate file presence;
    -- per-feedback Ansible owns proof.
    local_asset_paths JSONB NOT NULL DEFAULT '[]'::JSONB,

    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

-- ============================================================================
-- MULTISTREAM PUSH TARGETS
-- ============================================================================
-- External RTMP/SRT destinations for simultaneous restreaming.
-- When a stream goes live, Foghorn activates enabled push targets on the
-- origin node via Helmsman → MistServer push API.
-- target_uri stores an application-encrypted payload because it contains
-- third-party platform stream keys.
-- ============================================================================

-- Push targets for multistreaming to external platforms
CREATE TABLE IF NOT EXISTS commodore.push_targets (
    -- ===== IDENTITY =====
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    stream_id UUID NOT NULL REFERENCES commodore.streams(id) ON DELETE CASCADE,

    -- ===== TARGET CONFIG =====
    platform VARCHAR(50),                         -- 'twitch', 'youtube', 'facebook', 'kick', 'x', 'custom'
    name VARCHAR(255) NOT NULL,                   -- User-friendly label ("My Twitch")
    target_uri VARCHAR(512) NOT NULL,             -- encrypted rtmp://live.twitch.tv/app/{stream_key}
    is_enabled BOOLEAN DEFAULT TRUE,

    -- ===== RUNTIME STATE =====
    -- Updated by Foghorn when PUSH_OUT_START / PUSH_END triggers fire
    status VARCHAR(50) DEFAULT 'idle',            -- idle | pushing | failed
    last_error TEXT,
    last_pushed_at TIMESTAMP,

    -- ===== LIFECYCLE =====
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_commodore_push_targets_tenant ON commodore.push_targets(tenant_id);
CREATE INDEX IF NOT EXISTS idx_commodore_push_targets_stream ON commodore.push_targets(tenant_id, stream_id);

-- ============================================================================
-- UTILITY FUNCTIONS
-- ============================================================================

-- Generate random alphanumeric strings for keys and tokens (uses pgcrypto for CSPRNG)
CREATE OR REPLACE FUNCTION commodore.generate_random_string(length INTEGER) RETURNS TEXT AS $$
DECLARE
    chars TEXT := 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789';
    chars_len INTEGER := 62;
    rand_bytes BYTEA;
    result TEXT := '';
    i INTEGER := 0;
BEGIN
    rand_bytes := gen_random_bytes(length);
    FOR i IN 0..length-1 LOOP
        result := result || substr(chars, (get_byte(rand_bytes, i) % chars_len) + 1, 1);
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
-- See: docs/architecture/clips-dvr.md
-- ============================================================================

-- Clip business registry (metadata only, lifecycle in Foghorn)
CREATE TABLE IF NOT EXISTS commodore.clips (
    -- ===== IDENTITY =====
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    user_id UUID NOT NULL,
    stream_id UUID NOT NULL,
    clip_hash VARCHAR(32) UNIQUE NOT NULL,  -- Generated by Commodore
    internal_name VARCHAR(64) UNIQUE NOT NULL, -- MistServer routing name (vod+<internal_name>)
    playback_id CITEXT NOT NULL,            -- Public view key (artifact playback ID)

    -- ===== METADATA =====
    title VARCHAR(255),
    description TEXT,

    -- ===== CLIP DEFINITION =====
    start_time BIGINT NOT NULL,             -- Unix timestamp (ms)
    duration BIGINT NOT NULL,               -- Duration (ms)
    clip_mode VARCHAR(20) DEFAULT 'absolute', -- absolute, relative, duration, clip_now
    requested_params JSONB,                 -- Original request for audit

    -- ===== CLUSTER ORIGIN / STORAGE =====
    origin_cluster_id VARCHAR(100),
    storage_cluster_id VARCHAR(100),

    -- ===== SIZE PROJECTION =====
    size_bytes BIGINT,

    -- ===== THUMBNAIL PROJECTION =====
    has_thumbnails BOOLEAN NOT NULL DEFAULT FALSE,

    -- ===== LIFECYCLE =====
    retention_until TIMESTAMP,
    -- Per-asset retention overrides resolved at UpdateAssetRetention before
    -- the value is propagated into foghorn.artifacts.retention_until.
    retention_override_days INTEGER,
    retention_override_until TIMESTAMP,
    retention_source VARCHAR(32),       -- 'tenant_default' | 'per_asset_override' | NULL
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_commodore_clips_tenant ON commodore.clips(tenant_id);
CREATE INDEX IF NOT EXISTS idx_commodore_clips_retention_override
    ON commodore.clips(tenant_id, retention_override_until)
    WHERE retention_override_until IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_commodore_clips_stream ON commodore.clips(stream_id);
CREATE INDEX IF NOT EXISTS idx_commodore_clips_user ON commodore.clips(user_id);
CREATE INDEX IF NOT EXISTS idx_commodore_clips_hash ON commodore.clips(clip_hash);
CREATE INDEX IF NOT EXISTS idx_commodore_clips_internal ON commodore.clips(internal_name);
CREATE UNIQUE INDEX IF NOT EXISTS idx_commodore_clips_playback_ci
    ON commodore.clips((lower(playback_id::text)));
CREATE INDEX IF NOT EXISTS idx_commodore_clips_created ON commodore.clips(created_at);

-- DVR recording business registry (metadata only, lifecycle in Foghorn)
CREATE TABLE IF NOT EXISTS commodore.dvr_recordings (
    -- ===== IDENTITY =====
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    user_id UUID NOT NULL,
    stream_id UUID,
    dvr_hash VARCHAR(32) UNIQUE NOT NULL,   -- Generated by Commodore
    internal_name VARCHAR(64) UNIQUE NOT NULL, -- MistServer routing name (vod+<internal_name>)
    playback_id CITEXT NOT NULL,            -- Public view key (artifact playback ID)

    -- ===== METADATA =====
    stream_internal_name VARCHAR(255) NOT NULL, -- Source stream's MistServer routing name

    -- ===== CLUSTER ORIGIN / STORAGE =====
    origin_cluster_id VARCHAR(100),
    storage_cluster_id VARCHAR(100),

    -- ===== SIZE PROJECTION =====
    size_bytes BIGINT,

    -- ===== THUMBNAIL PROJECTION =====
    has_thumbnails BOOLEAN NOT NULL DEFAULT FALSE,

    -- ===== LIFECYCLE =====
    retention_until TIMESTAMP,
    -- Per-asset retention overrides resolved at Commodore.StartDVR before
    -- the value is snapshotted into foghorn.artifacts.dvr_retention_days.
    retention_override_days INTEGER,
    retention_override_until TIMESTAMP,
    retention_source VARCHAR(32),       -- 'tenant_default' | 'per_asset_override' | NULL
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_commodore_dvr_tenant ON commodore.dvr_recordings(tenant_id);
CREATE INDEX IF NOT EXISTS idx_commodore_dvr_retention_override
    ON commodore.dvr_recordings(tenant_id, retention_override_until)
    WHERE retention_override_until IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_commodore_dvr_stream ON commodore.dvr_recordings(stream_id);
CREATE INDEX IF NOT EXISTS idx_commodore_dvr_user ON commodore.dvr_recordings(user_id);
CREATE INDEX IF NOT EXISTS idx_commodore_dvr_hash ON commodore.dvr_recordings(dvr_hash);
CREATE INDEX IF NOT EXISTS idx_commodore_dvr_stream_internal ON commodore.dvr_recordings(stream_internal_name);
CREATE INDEX IF NOT EXISTS idx_commodore_dvr_internal ON commodore.dvr_recordings(internal_name);
CREATE UNIQUE INDEX IF NOT EXISTS idx_commodore_dvr_playback_ci
    ON commodore.dvr_recordings((lower(playback_id::text)));
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
    stream_id UUID,
    vod_hash VARCHAR(32) UNIQUE NOT NULL,   -- Generated by Commodore
    internal_name VARCHAR(64) UNIQUE NOT NULL, -- MistServer routing name (vod+<internal_name>)
    playback_id CITEXT NOT NULL,            -- Public view key (artifact playback ID)

    -- ===== METADATA =====
    title VARCHAR(255),
    description TEXT,
    filename VARCHAR(255) NOT NULL,         -- Original uploaded filename
    content_type VARCHAR(100),              -- MIME type (video/mp4, etc.)

    -- ===== SIZE =====
    size_bytes BIGINT,                      -- Expected file size

    -- ===== CLUSTER ORIGIN / STORAGE =====
    origin_cluster_id VARCHAR(100),
    storage_cluster_id VARCHAR(100),

    -- ===== THUMBNAIL PROJECTION =====
    has_thumbnails BOOLEAN NOT NULL DEFAULT FALSE,
    library_visible BOOLEAN NOT NULL DEFAULT TRUE,
    origin_type VARCHAR(32),
    origin_id VARCHAR(64),

    -- ===== LIFECYCLE =====
    retention_until TIMESTAMP,
    retention_override_days INTEGER,
    retention_override_until TIMESTAMP,
    retention_source VARCHAR(32),
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_commodore_vod_retention_override
    ON commodore.vod_assets(tenant_id, retention_override_until)
    WHERE retention_override_until IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_commodore_vod_tenant ON commodore.vod_assets(tenant_id);
CREATE INDEX IF NOT EXISTS idx_commodore_vod_user ON commodore.vod_assets(user_id);
CREATE INDEX IF NOT EXISTS idx_commodore_vod_stream ON commodore.vod_assets(stream_id)
    WHERE stream_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_commodore_vod_hash ON commodore.vod_assets(vod_hash);
CREATE INDEX IF NOT EXISTS idx_commodore_vod_internal ON commodore.vod_assets(internal_name);
CREATE UNIQUE INDEX IF NOT EXISTS idx_commodore_vod_playback_ci
    ON commodore.vod_assets((lower(playback_id::text)));
CREATE INDEX IF NOT EXISTS idx_commodore_vod_created ON commodore.vod_assets(created_at);
CREATE INDEX IF NOT EXISTS idx_commodore_vod_origin
    ON commodore.vod_assets(origin_type, origin_id)
    WHERE origin_type IS NOT NULL;

-- ============================================================================
-- DVR CHAPTER PLAYBACK ID REGISTRY
-- ============================================================================
-- Hidden chapter artifacts (origin_type='dvr_chapter', library_visible=false)
-- get real Commodore-minted public playback IDs so chapter playback uses the
-- same public-ID boundary as VOD. Keyed by chapter_id; artifact_hash is
-- denormalized for the resolver hot path.

CREATE TABLE IF NOT EXISTS commodore.dvr_chapter_playback (
    chapter_id    VARCHAR(32) PRIMARY KEY,
    tenant_id     UUID NOT NULL,
    playback_id   CITEXT NOT NULL,
    artifact_hash VARCHAR(32) NOT NULL,
    created_at    TIMESTAMP DEFAULT NOW(),
    updated_at    TIMESTAMP DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_commodore_dvr_chapter_playback_pid_ci
    ON commodore.dvr_chapter_playback((lower(playback_id::text)));
CREATE INDEX IF NOT EXISTS idx_commodore_dvr_chapter_playback_tenant
    ON commodore.dvr_chapter_playback(tenant_id);
CREATE INDEX IF NOT EXISTS idx_commodore_dvr_chapter_playback_artifact
    ON commodore.dvr_chapter_playback(artifact_hash);

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

-- ============================================================================
-- TENANT PROCESSING CONFIG (Enterprise Overrides)
-- ============================================================================
-- Per-tenant overrides for MistServer process definitions.
-- Only applied when the tenant's billing tier has processing_customizable = true.
-- NULL columns mean "use tier defaults".

CREATE TABLE IF NOT EXISTS commodore.tenant_processing_config (
    tenant_id UUID PRIMARY KEY,
    processes_live JSONB,           -- Override for live STREAM_PROCESS (NULL = use tier default)
    processes_dvr JSONB,            -- Override for rolling DVR STREAM_PROCESS
    processes_clip JSONB,           -- Override for clip materialization
    processes_dvr_finalize JSONB,   -- Override for DVR chapter/finalization materialization
    processes_vod JSONB,            -- Override for uploaded VOD processing
    updated_at TIMESTAMP DEFAULT NOW()
);

-- Per-stream override layer. Resolution order in resolveProcessesJSON is:
--   stream override (this table)
--   tenant override (commodore.tenant_processing_config)
--   tier default (purser.billing_tiers.processes_live/_vod)
-- Sibling to tenant_processing_config rather than a column on commodore.streams
-- so the processing-policy authority stays in one place.
CREATE TABLE IF NOT EXISTS commodore.stream_processing_config (
    stream_id UUID PRIMARY KEY REFERENCES commodore.streams(id) ON DELETE CASCADE,
    processes_live JSONB,
    processes_dvr JSONB,
    processes_clip JSONB,
    processes_dvr_finalize JSONB,
    processes_vod  JSONB,
    updated_at TIMESTAMP DEFAULT NOW()
);

-- ============================================================================
-- TENANT MEDIA RETENTION POLICY (Customer-Tunable Storage Cost Control)
-- ============================================================================
-- Tenant-wide retention defaults, one row per tenant, one column per asset
-- class. NULL means "inherit the per-class system default" (VOD: keep
-- forever, DVR/clip: 30d), then clamp by the Purser recording_retention_days
-- tier cap (Free has a finite cap; paid tiers carry 0 = no cap). 0 in a
-- column means "no auto-expire" (only meaningful on uncapped tiers; Free
-- clamps 0 up to its cap at write time). Resolution cascade at artifact
-- create / Commodore.StartDVR is:
--   per-asset override → per-stream override (DVR/clip) → this row → system
--     default → tier cap.
-- The resolved value is snapshotted onto the artifact (foghorn) at start;
-- the enforcement loop in Foghorn is unchanged.

CREATE TABLE IF NOT EXISTS commodore.tenant_media_retention_policies (
    tenant_id UUID PRIMARY KEY,
    default_vod_retention_days  INTEGER,
    default_dvr_retention_days  INTEGER,
    default_clip_retention_days INTEGER,
    updated_by UUID,
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

-- ============================================================================
-- PULL SOURCE LIFECYCLE EVENTS (Resolution stage)
-- ============================================================================
-- Append-only audit of Foghorn STREAM_SOURCE resolutions against pull+
-- streams. Captures the customer-facing resolution outcome — Mist's
-- downstream dial result is NOT yet captured here (separate trigger work).

CREATE TABLE IF NOT EXISTS commodore.pull_source_events (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    stream_id UUID,
    internal_name VARCHAR(255) NOT NULL,
    event_kind VARCHAR(32) NOT NULL,
    detail TEXT,
    created_at TIMESTAMP DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_commodore_pull_source_events_tenant
    ON commodore.pull_source_events(tenant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_commodore_pull_source_events_stream
    ON commodore.pull_source_events(stream_id, created_at DESC)
    WHERE stream_id IS NOT NULL;

-- ============================================================================
-- PLAYBACK ACCESS CONTROL
-- ============================================================================
-- Customer-managed signing keys + per-stream/asset/clip playback policies.
-- Foghorn enforces in the USER_NEW (MistTrigger_ViewerConnect) handler.

-- Customer-supplied ES256 public keys. Private key never stored.
CREATE TABLE IF NOT EXISTS commodore.signing_keys (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    kid VARCHAR(64) NOT NULL,                    -- Short ID embedded in JWT header
    name VARCHAR(255) NOT NULL,
    public_key_pem TEXT NOT NULL,                -- ES256 public key, PEM-encoded
    algorithm VARCHAR(16) NOT NULL DEFAULT 'ES256',
    status VARCHAR(16) NOT NULL DEFAULT 'active', -- active | revoked
    created_at TIMESTAMP DEFAULT NOW(),
    last_used_at TIMESTAMP,
    revoked_at TIMESTAMP,
    UNIQUE (tenant_id, kid)
);

CREATE INDEX IF NOT EXISTS idx_commodore_signing_keys_tenant_status
    ON commodore.signing_keys(tenant_id, status);

-- Per-playback-object policy + local-marker for fail-closed enforcement.
-- requires_auth flips automatically with setPlaybackPolicy:
--   public  -> false
--   jwt     -> true
--   webhook -> true
-- Webhook secret is encrypted via pkg/crypto/fieldcrypt (not in JSONB).

ALTER TABLE commodore.streams
    ADD COLUMN IF NOT EXISTS requires_auth BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS playback_policy JSONB,
    ADD COLUMN IF NOT EXISTS playback_webhook_secret_enc TEXT;

ALTER TABLE commodore.vod_assets
    ADD COLUMN IF NOT EXISTS requires_auth BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS playback_policy JSONB,
    ADD COLUMN IF NOT EXISTS playback_webhook_secret_enc TEXT;

-- Clips snapshot policy at creation; independent of source stream after.
ALTER TABLE commodore.clips
    ADD COLUMN IF NOT EXISTS requires_auth BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS playback_policy JSONB,
    ADD COLUMN IF NOT EXISTS playback_webhook_secret_enc TEXT;

CREATE INDEX IF NOT EXISTS idx_commodore_streams_requires_auth
    ON commodore.streams(requires_auth) WHERE requires_auth;
CREATE INDEX IF NOT EXISTS idx_commodore_vod_assets_requires_auth
    ON commodore.vod_assets(requires_auth) WHERE requires_auth;
CREATE INDEX IF NOT EXISTS idx_commodore_clips_requires_auth
    ON commodore.clips(requires_auth) WHERE requires_auth;

-- ============================================================================
-- PLAYBACK POLICY INVALIDATION OUTBOX
-- ============================================================================
-- Durable per-mutation record. Commodore writes one row per signing-key revoke
-- or policy mutation, inside the same transaction as the underlying UPDATE so
-- the mutation cannot succeed without a retry record. A worker re-resolves the
-- tenant's cluster footprint each pass and fans out to every cluster whose
-- Foghorn has not yet acknowledged the invalidation. There is no terminal
-- abandon state — backoff caps at invalidationOutboxMaxBackoff and a stuck
-- row triggers an Error log line for alerting, but retry continues
-- indefinitely so a long-partitioned cluster catches up when it returns.

CREATE TABLE IF NOT EXISTS commodore.playback_policy_invalidation_outbox (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    reason TEXT NOT NULL,
    internal_names JSONB NOT NULL DEFAULT '[]'::jsonb,
    status TEXT NOT NULL DEFAULT 'pending',
    attempts INT NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMP NOT NULL DEFAULT NOW(),
    last_error TEXT,
    -- Slugs (e.g. "demo-media", "peer-media"). Cluster IDs are VARCHAR(100)
    -- strings everywhere else in this codebase, never UUIDs.
    last_failed_clusters JSONB,
    -- Signed-policy-bundle watermark. Set only when reason='bundle_revoke';
    -- carries the minimum-acceptable bundle_version after which Foghorn must
    -- drop cached bundles. Identifies the (tenant_id, stream_id) pair via
    -- stream_id below. NULL for tenant+internal_names-scoped invalidations.
    bundle_min_version BIGINT,
    -- Bundle revocation target. Together with bundle_min_version this lets
    -- Foghorn BumpWatermark(tenantID, streamID, minVersion) on receipt. NULL
    -- when the row is a tenant+internal_names-scoped invalidation.
    stream_id UUID,
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_commodore_invalidation_outbox_pending
    ON commodore.playback_policy_invalidation_outbox(next_attempt_at)
    WHERE status = 'pending';

CREATE INDEX IF NOT EXISTS idx_commodore_invalidation_outbox_tenant
    ON commodore.playback_policy_invalidation_outbox(tenant_id, status);

-- ============================================================================
-- SERVICE EVENT OUTBOX
-- ============================================================================
-- Durable outbox for Commodore service events emitted to Decklog. Producers
-- write a row in the same DB transaction as the state mutation; a drain
-- worker dispatches with exponential backoff. Payload is the full
-- pb.ServiceEvent serialized as protojson (StreamChangeEvent / AuthEvent /
-- other oneof variants ride inside the payload).

CREATE TABLE IF NOT EXISTS commodore.service_event_outbox (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    event_type    TEXT NOT NULL,
    tenant_id     UUID NOT NULL,
    user_id       TEXT NOT NULL DEFAULT '',
    resource_type TEXT NOT NULL DEFAULT '',
    resource_id   TEXT NOT NULL DEFAULT '',
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    claimed_at   TIMESTAMPTZ,
    attempts     INTEGER NOT NULL DEFAULT 0,
    last_error   TEXT,
    completed_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_commodore_service_event_outbox_pending
    ON commodore.service_event_outbox(created_at)
    WHERE completed_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_commodore_service_event_outbox_tenant
    ON commodore.service_event_outbox(tenant_id, created_at DESC);

-- ============================================================================
-- SIGNING-KEY AUDIT LOG
-- ============================================================================
-- Per-action audit trail for customer signing-key lifecycle events.
-- Holds metadata + actor identity only — no key material.
-- Runtime JWT verification updates signing_keys.last_used_at only; it does not
-- append per-viewer rows here. Per-use observability lives in the last_used_at
-- timestamp + Foghorn metrics.

CREATE TABLE IF NOT EXISTS commodore.signing_key_audit (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    kid VARCHAR(64) NOT NULL,
    action TEXT NOT NULL,                 -- create | revoke
    actor_user_id UUID,
    actor_ip TEXT,
    detail TEXT,
    at TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_commodore_signing_key_audit_tenant_at
    ON commodore.signing_key_audit(tenant_id, at DESC);

CREATE INDEX IF NOT EXISTS idx_commodore_signing_key_audit_kid_at
    ON commodore.signing_key_audit(kid, at DESC);

-- ============================================================================
-- STREAM CLUSTER PINS
-- ============================================================================
-- Enterprise stream pinning: lock a specific stream to a constrained set of
-- clusters regardless of tenant-wide tenant_cluster_access. Resolver joins
-- LEFT to apply pins when present; absence (no row) means policy-derived
-- placement applies normally. Empty for ~all rows in production, so the
-- side-table shape avoids a perpetually-NULL TEXT[] on commodore.streams.

CREATE TABLE IF NOT EXISTS commodore.stream_cluster_pins (
    stream_id UUID PRIMARY KEY REFERENCES commodore.streams(id) ON DELETE CASCADE,
    allowed_cluster_ids TEXT[] NOT NULL,
    pinned_by UUID,
    pin_reason TEXT,
    pinned_at TIMESTAMP NOT NULL DEFAULT NOW()
);

-- ============================================================================
-- PLAYBACK POLICY BUNDLE VERSIONS
-- ============================================================================
-- Commodore mints a signed policy bundle per (tenant_id, stream_id) carrying
-- the tenant's plan, allowed cluster set, JWT verification keys, webhook
-- config, and a monotonic bundle_version. Foghorn caches by version with a
-- soft TTL (background refresh) and a hard TTL (refuse stale past the cap).
-- Revocation rides the existing playback_policy_invalidation_outbox with a
-- 'bundle_revoke' entry carrying the new minimum-acceptable bundle_version;
-- Foghorn invalidates cached entries below the watermark. This survives a
-- central Commodore outage for the hard-TTL window without serving stale
-- policy past plan downgrades.

CREATE TABLE IF NOT EXISTS commodore.policy_bundle_versions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    stream_id UUID REFERENCES commodore.streams(id) ON DELETE CASCADE,
    bundle_version BIGINT NOT NULL,
    bundle_jwt TEXT NOT NULL,
    issued_at TIMESTAMP NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMP NOT NULL,
    revoked_at TIMESTAMP,
    UNIQUE (tenant_id, stream_id, bundle_version)
);

CREATE INDEX IF NOT EXISTS idx_commodore_policy_bundle_versions_active
    ON commodore.policy_bundle_versions(tenant_id, stream_id, bundle_version DESC)
    WHERE revoked_at IS NULL;

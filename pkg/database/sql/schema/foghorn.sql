-- ============================================================================
-- FOGHORN SCHEMA - MEDIA PLANE & ARTIFACT ORCHESTRATION
-- ============================================================================
-- Manages artifact lifecycle, storage distribution, and node orchestration.
-- Business registry (tenant, stream, metadata) is owned by Commodore.
-- See: docs/architecture/CLIP_DVR_REGISTRY.md for full architecture details.
-- ============================================================================

CREATE SCHEMA IF NOT EXISTS foghorn;

-- ============================================================================
-- EXTENSIONS
-- ============================================================================

CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- ============================================================================
-- UNIFIED ARTIFACT MODEL
-- ============================================================================
-- Replaces legacy: foghorn.clips, foghorn.dvr_requests, foghorn.artifact_registry
--
-- Storage model:
--   artifacts      = cold storage state (S3 is authoritative, 1 row per artifact)
--   artifact_nodes = warm storage cache (which nodes have copies, N rows per artifact)
-- ============================================================================

-- Unified artifact lifecycle table (cold storage = S3 is authoritative)
CREATE TABLE IF NOT EXISTS foghorn.artifacts (
    -- ===== IDENTITY =====
    artifact_hash VARCHAR(32) PRIMARY KEY,
    artifact_type VARCHAR(10) NOT NULL,     -- 'clip', 'dvr', 'upload' (future)

    -- ===== DENORMALIZED FIELDS (authoritative source: Commodore) =====
    -- Cached here for operational efficiency (stream routing, rehydration, Decklog events)
    internal_name VARCHAR(255),             -- Stream identifier for routing
    artifact_internal_name VARCHAR(64),     -- Artifact routing name (vod+<artifact_internal_name>)
    tenant_id UUID,                         -- Fallback when Commodore unavailable
    user_id UUID,                           -- User who created the artifact (for Decklog events)

    -- ===== LIFECYCLE STATE =====
    status VARCHAR(50) DEFAULT 'requested', -- requested, processing, ready, failed, deleted
    error_message TEXT,
    request_id UUID,                        -- Original request tracking

    -- ===== STORAGE METRICS =====
    size_bytes BIGINT,
    manifest_path VARCHAR(500),             -- HLS/DASH manifest (DVR/clip)
    format VARCHAR(20),                     -- Container format: mp4, m3u8, webm, etc. (set at creation)

    -- ===== COLD STORAGE (S3 = AUTHORITATIVE) =====
    storage_location VARCHAR(20) DEFAULT 'pending',
        -- pending: not yet stored anywhere
        -- local: only on node(s), not synced to S3
        -- freezing: being uploaded to S3
        -- s3: frozen to S3, may have warm copies
        -- defrosting: being downloaded from S3
    s3_url VARCHAR(500),
    sync_status VARCHAR(20) DEFAULT 'pending',
        -- pending: not synced
        -- in_progress: syncing
        -- synced: on S3
        -- failed: sync error
    sync_error TEXT,
    last_sync_attempt TIMESTAMP,
    frozen_at TIMESTAMP,
    dtsh_synced BOOLEAN DEFAULT FALSE,      -- True if .dtsh index was synced

    -- ===== DVR-SPECIFIC TIMING =====
    started_at TIMESTAMP,
    ended_at TIMESTAMP,
    duration_seconds INTEGER,

    -- ===== ACCESS TRACKING =====
    access_count INTEGER DEFAULT 0,
    last_accessed_at TIMESTAMP,

    -- ===== RETENTION =====
    retention_until TIMESTAMP,              -- When artifact should be soft-deleted (from Commodore)

    -- ===== TIMESTAMPS =====
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

-- ============================================================================
-- ARTIFACT INDEXES
-- ============================================================================

CREATE INDEX IF NOT EXISTS idx_foghorn_artifacts_type ON foghorn.artifacts(artifact_type);
CREATE INDEX IF NOT EXISTS idx_foghorn_artifacts_status ON foghorn.artifacts(status);
CREATE INDEX IF NOT EXISTS idx_foghorn_artifacts_storage ON foghorn.artifacts(storage_location);
CREATE INDEX IF NOT EXISTS idx_foghorn_artifacts_sync ON foghorn.artifacts(sync_status);
CREATE INDEX IF NOT EXISTS idx_foghorn_artifacts_created ON foghorn.artifacts(created_at);
CREATE INDEX IF NOT EXISTS idx_foghorn_artifacts_request_id ON foghorn.artifacts(request_id);
CREATE INDEX IF NOT EXISTS idx_foghorn_artifacts_frozen ON foghorn.artifacts(frozen_at) WHERE frozen_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_foghorn_artifacts_internal_name ON foghorn.artifacts(internal_name);
CREATE INDEX IF NOT EXISTS idx_foghorn_artifacts_artifact_internal ON foghorn.artifacts(artifact_internal_name);
CREATE INDEX IF NOT EXISTS idx_foghorn_artifacts_tenant ON foghorn.artifacts(tenant_id) WHERE tenant_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_foghorn_artifacts_user ON foghorn.artifacts(user_id) WHERE user_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_foghorn_artifacts_retention ON foghorn.artifacts(retention_until) WHERE retention_until IS NOT NULL;

-- ============================================================================
-- WARM STORAGE DISTRIBUTION (NODE CACHES)
-- ============================================================================

-- Tracks which nodes have local copies of artifacts
CREATE TABLE IF NOT EXISTS foghorn.artifact_nodes (
    -- ===== IDENTITY =====
    artifact_hash VARCHAR(32) NOT NULL REFERENCES foghorn.artifacts(artifact_hash) ON DELETE CASCADE,
    node_id VARCHAR(100) NOT NULL,

    -- ===== NODE-SPECIFIC STORAGE =====
    file_path VARCHAR(500),
    base_url VARCHAR(500),                  -- Node base URL for routing
    size_bytes BIGINT,                      -- Size on this node (may differ during sync)

    -- ===== DVR SEGMENT TRACKING (PER-NODE) =====
    segment_count INT DEFAULT 0,
    segment_bytes BIGINT DEFAULT 0,

    -- ===== HEALTH TRACKING =====
    access_count BIGINT DEFAULT 0,          -- Best-effort local access count
    last_accessed TIMESTAMP,                -- Last access time on this node
    last_seen_at TIMESTAMP DEFAULT NOW(),
    is_orphaned BOOLEAN DEFAULT false,      -- Not seen in recent node reports
    cached_at TIMESTAMP,                    -- When cached locally (for warm duration tracking)

    PRIMARY KEY (artifact_hash, node_id)
);

-- ============================================================================
-- ARTIFACT NODE INDEXES
-- ============================================================================

CREATE INDEX IF NOT EXISTS idx_foghorn_artifact_nodes_node ON foghorn.artifact_nodes(node_id);
CREATE INDEX IF NOT EXISTS idx_foghorn_artifact_nodes_orphaned ON foghorn.artifact_nodes(is_orphaned) WHERE is_orphaned = true;
CREATE INDEX IF NOT EXISTS idx_foghorn_artifact_nodes_seen ON foghorn.artifact_nodes(last_seen_at);
CREATE INDEX IF NOT EXISTS idx_foghorn_artifact_nodes_cached ON foghorn.artifact_nodes(cached_at);

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

CREATE INDEX IF NOT EXISTS idx_foghorn_node_outputs_updated ON foghorn.node_outputs(last_updated);

-- ============================================================================
-- NODE LIFECYCLE SNAPSHOTS
-- ============================================================================

-- Full node lifecycle snapshot storage for readiness and audit
CREATE TABLE IF NOT EXISTS foghorn.node_lifecycle (
    node_id VARCHAR(100) PRIMARY KEY,
    lifecycle JSONB NOT NULL,
    last_updated TIMESTAMP DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_foghorn_node_lifecycle_updated ON foghorn.node_lifecycle(last_updated);

-- ============================================================================
-- VOD UPLOAD METADATA
-- ============================================================================
-- VOD-specific fields for user-uploaded video files.
-- Core artifact tracking (status, storage_location, size_bytes, s3_url) is in foghorn.artifacts.
-- This table holds VOD-specific metadata not applicable to clips or DVR recordings.
-- ============================================================================

CREATE TABLE IF NOT EXISTS foghorn.vod_metadata (
    -- ===== IDENTITY =====
    artifact_hash VARCHAR(32) PRIMARY KEY REFERENCES foghorn.artifacts(artifact_hash) ON DELETE CASCADE,

    -- ===== UPLOAD METADATA =====
    filename VARCHAR(255),                  -- Original uploaded filename
    title VARCHAR(255),                     -- User-provided title
    description TEXT,                       -- User-provided description
    content_type VARCHAR(100),              -- MIME type (video/mp4, video/webm, etc.)

    -- ===== S3 MULTIPART UPLOAD TRACKING =====
    s3_upload_id VARCHAR(255),              -- S3 multipart upload ID (null after completion)
    s3_key VARCHAR(500),                    -- S3 object key

    -- ===== FILE METADATA (populated after validation) =====
    duration_ms INTEGER,                    -- Video duration in milliseconds
    resolution VARCHAR(20),                 -- e.g., "1920x1080"
    video_codec VARCHAR(50),                -- e.g., "h264", "h265", "vp9", "av1"
    audio_codec VARCHAR(50),                -- e.g., "aac", "opus", "mp3"
    bitrate_kbps INTEGER,                   -- Average bitrate in kbps
    width INTEGER,                          -- Video width in pixels
    height INTEGER,                         -- Video height in pixels
    fps REAL,                               -- Frames per second
    audio_channels INTEGER,                 -- Number of audio channels
    audio_sample_rate INTEGER,              -- Audio sample rate in Hz

    -- ===== TIMESTAMPS =====
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

-- ============================================================================
-- VOD METADATA INDEXES
-- ============================================================================

CREATE INDEX IF NOT EXISTS idx_foghorn_vod_metadata_title ON foghorn.vod_metadata(title) WHERE title IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_foghorn_vod_metadata_s3_upload ON foghorn.vod_metadata(s3_upload_id) WHERE s3_upload_id IS NOT NULL;

-- ============================================================================
-- PROCESSING JOBS (TRANSCODING QUEUE)
-- ============================================================================
-- Tracks async transcoding jobs for VOD post-processing and live ABR generation.
-- Routing decision (Gateway vs local) is based on input codec and Gateway availability.
-- ============================================================================

CREATE TABLE IF NOT EXISTS foghorn.processing_jobs (
    -- ===== IDENTITY =====
    job_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    artifact_hash VARCHAR(32) REFERENCES foghorn.artifacts(artifact_hash) ON DELETE SET NULL,

    -- ===== JOB CONFIGURATION =====
    job_type VARCHAR(20) NOT NULL,           -- 'transcode', 'thumbnail', 'extract_audio'
    input_codec VARCHAR(20),                 -- Source codec (H264, H265, AV1, VP9)
    output_profiles JSONB,                   -- [{name, codec, bitrate, width, height}]

    -- ===== ROUTING DECISION =====
    use_gateway BOOLEAN DEFAULT FALSE,       -- Use Livepeer Gateway if true
    gateway_url VARCHAR(255),                -- Gateway URL (if use_gateway=true)
    processing_node_id VARCHAR(100),         -- Node handling the job (if use_gateway=false)
    routing_reason VARCHAR(255),             -- Human-readable reason for routing decision

    -- ===== JOB STATUS =====
    status VARCHAR(20) DEFAULT 'queued',     -- queued, dispatched, processing, completed, failed
    progress INTEGER DEFAULT 0,              -- 0-100 progress percentage
    error_message TEXT,
    retry_count INTEGER DEFAULT 0,           -- Number of retries attempted

    -- ===== OUTPUT =====
    output_artifact_hash VARCHAR(32),        -- New artifact hash for transcoded output
    output_s3_url VARCHAR(500),              -- S3 URL of transcoded output

    -- ===== TIMESTAMPS =====
    created_at TIMESTAMP DEFAULT NOW(),
    started_at TIMESTAMP,
    completed_at TIMESTAMP,
    updated_at TIMESTAMP DEFAULT NOW()
);

-- ============================================================================
-- PROCESSING JOBS INDEXES
-- ============================================================================

CREATE INDEX IF NOT EXISTS idx_foghorn_processing_jobs_tenant ON foghorn.processing_jobs(tenant_id);
CREATE INDEX IF NOT EXISTS idx_foghorn_processing_jobs_status ON foghorn.processing_jobs(status);
CREATE INDEX IF NOT EXISTS idx_foghorn_processing_jobs_artifact ON foghorn.processing_jobs(artifact_hash);
CREATE INDEX IF NOT EXISTS idx_foghorn_processing_jobs_queued ON foghorn.processing_jobs(status, created_at) WHERE status = 'queued';
CREATE INDEX IF NOT EXISTS idx_foghorn_processing_jobs_node ON foghorn.processing_jobs(processing_node_id) WHERE processing_node_id IS NOT NULL;

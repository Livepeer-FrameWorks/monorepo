-- ============================================================================
-- FOGHORN SCHEMA - MEDIA PLANE & ARTIFACT ORCHESTRATION
-- ============================================================================
-- Manages artifact lifecycle, storage distribution, and node orchestration.
-- Business registry (tenant, stream, metadata) is owned by Commodore.
-- See: docs/architecture/clips-dvr.md for full architecture details.
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
    artifact_type VARCHAR(10) NOT NULL,     -- 'clip', 'dvr', 'vod'

    -- ===== DENORMALIZED FIELDS (authoritative source: Commodore) =====
    -- Cached here for operational efficiency (stream routing, rehydration, Decklog events)
    stream_internal_name VARCHAR(255),      -- Source stream identifier (denormalized from Commodore)
    internal_name VARCHAR(64),              -- Artifact routing name (vod+<internal_name>)
    stream_id UUID,                         -- Public stream ID (for DVR local path reconstruction)
    tenant_id UUID NOT NULL,                -- Tenant owning the artifact (required)
    user_id UUID,                           -- User who created the artifact (for Decklog events)
    origin_cluster_id VARCHAR(100),         -- Which cluster originally created the artifact (NULL = local)
    storage_cluster_id VARCHAR(100),        -- Which cluster's S3 actually holds the bytes; NULL = same as origin_cluster_id

    -- ===== LIFECYCLE STATE =====
    status VARCHAR(50) DEFAULT 'requested',
        -- VOD/clip:  requested, processing, ready, failed, deleted
        -- DVR:       requested, starting, recording, finalizing,
        --            completed, completed_partial, failed, deleted
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
    defrost_node_id VARCHAR(100),
    defrost_started_at TIMESTAMP,
    sync_status VARCHAR(20) DEFAULT 'pending',
        -- pending: not synced
        -- in_progress: syncing
        -- synced: on S3
        -- failed: sync error (retryable)
        -- lost_local: local source gone before any sync; terminal tombstone, never retried
    sync_error TEXT,
    last_sync_attempt TIMESTAMP,
    failure_count INT NOT NULL DEFAULT 0,
    frozen_at TIMESTAMP,
    dtsh_synced BOOLEAN DEFAULT FALSE,      -- True if .dtsh index was synced

    -- ===== DVR-SPECIFIC TIMING =====
    started_at TIMESTAMP,
    ended_at TIMESTAMP,
    duration_seconds INTEGER,

    -- ===== THUMBNAIL STATE =====
    has_thumbnails BOOLEAN DEFAULT FALSE,   -- True after THUMBNAIL_UPDATED upload completes (DVR sprite sheets)

    -- ===== ACCESS TRACKING =====
    access_count INTEGER DEFAULT 0,
    last_accessed_at TIMESTAMP,

    -- ===== RETENTION =====
    retention_until TIMESTAMP,              -- When artifact should be soft-deleted; for DVR computed at FinalizeDVR as ended_at + dvr_retention_days*24h

    -- ===== DVR POLICY SNAPSHOT (DVR rows only) =====
    -- Captured at DVR start so finalize months later applies the same policy
    -- even if the tenant's tier changed during a long-running stream.
    dvr_window_seconds   INTEGER,           -- resolved live DVR window (Mist targetAge); also passed in DVRConfig
    dvr_chapter_mode     VARCHAR(32),       -- default mode for the chapter sweeper to materialize
    dvr_chapter_interval INTEGER,           -- interval_seconds for fixed_interval mode
    dvr_retention_days   INTEGER,           -- per-class cascade snapshot (Commodore-resolved); NULL = keep forever
    dvr_chapter_backfill_complete BOOLEAN NOT NULL DEFAULT false, -- terminal chapter index materialized through ended_at
    dvr_processes_json   TEXT,              -- Tenant's live MistProc config snapshot for the dvr+ rolling-DVR surface (resolved at StartDVR)

    -- ===== ORIGIN / VISIBILITY =====
    -- origin_type identifies *how* this artifact was produced. NULL or
    -- 'upload' for ordinary VOD uploads, 'dvr_chapter' for the hidden
    -- canonical .mkv produced by chapter finalization, 'clip_source'
    -- reserved for future clip-source bookkeeping. origin_id is the
    -- domain id (e.g. chapter_id) that uniquely identifies the source
    -- when origin_type is set.
    origin_type     VARCHAR(32),
    origin_id       VARCHAR(64),
    -- library_visible=false hides the artifact from user-facing library
    -- listings (e.g. chapter-origin artifacts) without affecting playback
    -- resolution through the explicit artifact path.
    library_visible BOOLEAN NOT NULL DEFAULT TRUE,

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
CREATE INDEX IF NOT EXISTS idx_foghorn_artifacts_dvr_backfill_pending
    ON foghorn.artifacts(ended_at, artifact_hash)
    WHERE artifact_type = 'dvr'
      AND status IN ('completed', 'completed_partial', 'failed', 'ready')
      AND ended_at IS NOT NULL
      AND dvr_chapter_mode IS NOT NULL
      AND dvr_chapter_mode != ''
      AND dvr_chapter_backfill_complete = false;
CREATE INDEX IF NOT EXISTS idx_foghorn_artifacts_defrosting ON foghorn.artifacts(defrost_started_at) WHERE storage_location = 'defrosting';
CREATE INDEX IF NOT EXISTS idx_foghorn_artifacts_stream_internal ON foghorn.artifacts(stream_internal_name);
CREATE INDEX IF NOT EXISTS idx_foghorn_artifacts_internal_name ON foghorn.artifacts(internal_name);
CREATE INDEX IF NOT EXISTS idx_foghorn_artifacts_tenant ON foghorn.artifacts(tenant_id) WHERE tenant_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_foghorn_artifacts_user ON foghorn.artifacts(user_id) WHERE user_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_foghorn_artifacts_retention ON foghorn.artifacts(retention_until) WHERE retention_until IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_foghorn_artifacts_storage_cluster ON foghorn.artifacts(storage_cluster_id, sync_status) WHERE storage_cluster_id IS NOT NULL;
-- Idempotent chapter finalization: at most one chapter-origin artifact
-- per chapter_id. Retries reuse the existing row via ON CONFLICT.
CREATE UNIQUE INDEX IF NOT EXISTS uq_foghorn_artifacts_chapter_origin
    ON foghorn.artifacts(origin_id) WHERE origin_type = 'dvr_chapter';
CREATE INDEX IF NOT EXISTS idx_foghorn_artifacts_origin
    ON foghorn.artifacts(origin_type, origin_id) WHERE origin_type IS NOT NULL;

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
-- DVR PER-SEGMENT LEDGER
-- ============================================================================
-- Durable record of every recorded segment for DVR/always-on streams.
-- Foghorn is the source of truth; sidecar reports segments via control stream.
-- Segments are recovery-source durability for chapter finalization: a chapter's
-- canonical .mkv VOD artifact is remuxed from a bounded range in this ledger,
-- then the segments reclaim once the chapter is frozen.
-- ============================================================================

CREATE TABLE IF NOT EXISTS foghorn.dvr_segments (
    artifact_hash    VARCHAR(32) NOT NULL REFERENCES foghorn.artifacts(artifact_hash) ON DELETE CASCADE,
    segment_name     TEXT NOT NULL,
    sequence         BIGINT NOT NULL,            -- Foghorn-assigned monotonic per artifact
    media_start_ms   BIGINT NOT NULL,
    media_end_ms     BIGINT NOT NULL,
    duration_ms      BIGINT NOT NULL,
    size_bytes       BIGINT,
    s3_key           TEXT NOT NULL,
    status           VARCHAR(20) NOT NULL DEFAULT 'pending',
        -- pending | uploaded | failed_upload | deleted_local | orphan_unreachable | lost_local | reclaimed
    drop_reason      VARCHAR(32),
        -- disk_pressure | retention_expired | operator_cleanup | upload_failed | chapter_reclaim
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    uploaded_at      TIMESTAMPTZ,
    deleted_local_at TIMESTAMPTZ,
    dropped_at       TIMESTAMPTZ,
    PRIMARY KEY (artifact_hash, segment_name),
    CONSTRAINT chk_foghorn_dvr_segments_status CHECK (status IN (
        'pending', 'uploaded', 'failed_upload',
        'deleted_local', 'orphan_unreachable',
        'lost_local', 'reclaimed'
    ))
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_foghorn_dvr_segments_sequence
    ON foghorn.dvr_segments(artifact_hash, sequence);
CREATE INDEX IF NOT EXISTS idx_foghorn_dvr_segments_media_order
    ON foghorn.dvr_segments(artifact_hash, media_start_ms, sequence);
CREATE INDEX IF NOT EXISTS idx_foghorn_dvr_segments_evictable
    ON foghorn.dvr_segments(artifact_hash, status, media_end_ms)
    WHERE status = 'uploaded';
CREATE INDEX IF NOT EXISTS idx_foghorn_dvr_segments_pending
    ON foghorn.dvr_segments(artifact_hash, status, created_at)
    WHERE status IN ('pending', 'failed_upload');

-- ============================================================================
-- DVR CHAPTERS - RANGE METADATA + FINALIZATION STATE MACHINE
-- ============================================================================
-- A chapter is a (start_ms, end_ms) slice that the finalization queue
-- remuxes to a canonical .mkv VOD artifact (referenced via
-- playback_artifact_hash). The sidecar never writes chapter manifests;
-- chapter playback uses the chapter artifact's standard VOD path.
-- See: docs/architecture/dvr-continuous-archive.md
-- ============================================================================

CREATE TABLE IF NOT EXISTS foghorn.dvr_chapters (
    chapter_id             VARCHAR(32) PRIMARY KEY,
    artifact_hash          VARCHAR(32) NOT NULL REFERENCES foghorn.artifacts(artifact_hash) ON DELETE CASCADE,
    mode                   VARCHAR(32) NOT NULL,
        -- window_sized_chapters | fixed_interval
    interval_seconds       INTEGER,
    start_ms               BIGINT NOT NULL,
    end_ms                 BIGINT NOT NULL,
    is_current             BOOLEAN NOT NULL DEFAULT false,
    state                  VARCHAR(32) NOT NULL DEFAULT 'open',
        -- open | closed | finalizing | finalized | frozen | reclaimed
        -- | failed_source_missing | failed_permanent
    playback_artifact_hash VARCHAR(32) REFERENCES foghorn.artifacts(artifact_hash) ON DELETE SET NULL,
    playback_id            VARCHAR(32),
        -- Commodore-minted public playback key (cached). Authoritative
        -- mapping lives in commodore.dvr_chapter_playback; this column
        -- avoids a per-row Commodore fan-out from the chapter list resolver.
    finalize_attempts      INTEGER NOT NULL DEFAULT 0,
    frozen_at              TIMESTAMPTZ,
        -- Set when state transitions to 'frozen' (artifact + .dtsh durably
        -- on S3). Anchors the reclaim sweep's abandoned-node grace so a
        -- recently-frozen chapter doesn't immediately skip Phase A just
        -- because the chapter row itself is old.
    finalize_started_at    TIMESTAMPTZ,
    last_failure_reason    TEXT,
    reclaim_started_at     TIMESTAMPTZ,
    segment_count          INTEGER NOT NULL DEFAULT 0,
    has_gaps               BOOLEAN NOT NULL DEFAULT false,
    actual_media_start_ms  BIGINT,
    actual_media_end_ms    BIGINT,
        -- Actual MKV span = [first_owned_segment.media_start_ms,
        -- last_owned_segment.media_end_ms). May differ from the scheduled
        -- start_ms/end_ms when chapter boundaries don't align with
        -- segment boundaries. Populated at MarkChapterFinalized; player
        -- timeline uses these so video.currentTime maps to wall-clock
        -- without drift.
    created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_foghorn_dvr_chapters_mode CHECK (mode IN (
        'window_sized_chapters', 'fixed_interval'
    )),
    CONSTRAINT chk_foghorn_dvr_chapters_state CHECK (state IN (
        'open', 'closed', 'finalizing', 'finalized', 'frozen',
        'reclaimed', 'failed_source_missing', 'failed_permanent'
    )),
    CONSTRAINT chk_foghorn_dvr_chapters_range CHECK (end_ms > start_ms)
);

CREATE INDEX IF NOT EXISTS idx_foghorn_dvr_chapters_artifact
    ON foghorn.dvr_chapters(artifact_hash, start_ms);
CREATE INDEX IF NOT EXISTS idx_foghorn_dvr_chapters_current
    ON foghorn.dvr_chapters(artifact_hash) WHERE is_current = true;
CREATE INDEX IF NOT EXISTS idx_foghorn_dvr_chapters_pending
    ON foghorn.dvr_chapters(state)
    WHERE state IN ('closed', 'finalizing');
CREATE INDEX IF NOT EXISTS idx_foghorn_dvr_chapters_reclaim
    ON foghorn.dvr_chapters(state, reclaim_started_at)
    WHERE state = 'frozen';
CREATE INDEX IF NOT EXISTS idx_foghorn_dvr_chapters_playback_id
    ON foghorn.dvr_chapters(playback_id) WHERE playback_id IS NOT NULL;

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
-- NODE MAINTENANCE MODES
-- ============================================================================

CREATE TABLE IF NOT EXISTS foghorn.node_maintenance (
    node_id VARCHAR(100) PRIMARY KEY,
    mode VARCHAR(20) NOT NULL DEFAULT 'normal',
    set_at TIMESTAMP DEFAULT NOW(),
    set_by VARCHAR(100)
);

-- ============================================================================
-- NODE LIFECYCLE SNAPSHOTS
-- ============================================================================
-- NOTE: This table is currently WRITTEN but NOT READ. The same NodeLifecycleUpdate
-- data is also sent to ClickHouse (node_state_current, node_metrics_samples) where
-- it IS queried. This PostgreSQL copy may be useful for:
--   - Disaster recovery (rehydrating Foghorn state if ClickHouse is unavailable)
--   - Audit trail in PostgreSQL (queryable via standard SQL tools)
--   - Future features requiring control-plane access to node state
-- Until a read path is added, this table accumulates data but serves no active purpose.
-- See: UpsertNodeLifecycle in api_balancing/internal/control/repos.go

-- Full node lifecycle snapshot storage for readiness and audit
CREATE TABLE IF NOT EXISTS foghorn.node_lifecycle (
    node_id VARCHAR(100) PRIMARY KEY,
    lifecycle JSONB NOT NULL,
    last_updated TIMESTAMP DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_foghorn_node_lifecycle_updated ON foghorn.node_lifecycle(last_updated);

CREATE TABLE IF NOT EXISTS foghorn.node_components (
    node_id VARCHAR(100) NOT NULL,
    component VARCHAR(64) NOT NULL,
    current_version TEXT,
    last_reported_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (node_id, component)
);

CREATE INDEX IF NOT EXISTS idx_foghorn_node_components_component ON foghorn.node_components(component);
CREATE INDEX IF NOT EXISTS idx_foghorn_node_components_reported ON foghorn.node_components(last_reported_at);

CREATE TABLE IF NOT EXISTS foghorn.node_update_state (
    node_id VARCHAR(100) PRIMARY KEY,
    target_release TEXT,
    phase VARCHAR(32) NOT NULL DEFAULT 'idle',
    started_at TIMESTAMPTZ,
    deadline TIMESTAMPTZ,
    expected_components JSONB NOT NULL DEFAULT '{}',
    last_error TEXT,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_foghorn_node_update_phase CHECK (phase IN (
        'idle', 'cordoning', 'draining', 'drained', 'updating', 'updating_restore', 'warming', 'warming_restore', 'failed'
    )),
    CONSTRAINT chk_foghorn_node_update_expected_components_object CHECK (jsonb_typeof(expected_components) = 'object')
);

CREATE INDEX IF NOT EXISTS idx_foghorn_node_update_state_phase ON foghorn.node_update_state(phase);

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
    upload_expires_at TIMESTAMPTZ,          -- S3 multipart session deadline; status returns EXPIRED past this
    total_parts INTEGER,                    -- Number of parts declared at create time, used to compute missing_parts

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

-- ===== DEPENDENCY TRACKING =====
ALTER TABLE foghorn.processing_jobs
  ADD COLUMN IF NOT EXISTS parent_job_id UUID REFERENCES foghorn.processing_jobs(job_id),
  ADD COLUMN IF NOT EXISTS output_metadata JSONB,
  ADD COLUMN IF NOT EXISTS processes_json TEXT,
  ADD COLUMN IF NOT EXISTS source_url TEXT,
  ADD COLUMN IF NOT EXISTS source_params JSONB,
  ADD COLUMN IF NOT EXISTS preferred_node_id VARCHAR(100);

CREATE INDEX IF NOT EXISTS idx_foghorn_processing_jobs_tenant ON foghorn.processing_jobs(tenant_id);
CREATE INDEX IF NOT EXISTS idx_foghorn_processing_jobs_status ON foghorn.processing_jobs(status);
CREATE INDEX IF NOT EXISTS idx_foghorn_processing_jobs_artifact ON foghorn.processing_jobs(artifact_hash);
CREATE INDEX IF NOT EXISTS idx_foghorn_processing_jobs_queued ON foghorn.processing_jobs(status, created_at) WHERE status = 'queued';
CREATE INDEX IF NOT EXISTS idx_foghorn_processing_jobs_node ON foghorn.processing_jobs(processing_node_id) WHERE processing_node_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_foghorn_processing_jobs_parent ON foghorn.processing_jobs(parent_job_id) WHERE parent_job_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_foghorn_processing_jobs_artifact_status ON foghorn.processing_jobs(artifact_hash, status);

-- ============================================================================
-- ARTIFACT EVENT OUTBOX
-- ============================================================================
-- Durable outbox for Foghorn artifact-lifecycle (DVR / VOD / Clip) and
-- federation peer-registry events. A drain worker dispatches pending rows
-- to Decklog with exponential backoff.

CREATE TABLE IF NOT EXISTS foghorn.artifact_event_outbox (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- event_kind discriminates the typed payload (clip_lifecycle,
    -- dvr_lifecycle, vod_lifecycle, federation_event).
    event_kind   TEXT NOT NULL,
    tenant_id    UUID,
    stream_id    TEXT NOT NULL DEFAULT '',
    artifact_id  TEXT NOT NULL DEFAULT '',
    -- protojson-encoded typed payload (pb.{ClipLifecycleData,
    -- DVRLifecycleData,VodLifecycleData,FederationEventData}).
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    claimed_at   TIMESTAMPTZ,
    attempts     INTEGER NOT NULL DEFAULT 0,
    last_error   TEXT,
    completed_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_foghorn_artifact_event_outbox_pending
    ON foghorn.artifact_event_outbox(created_at)
    WHERE completed_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_foghorn_artifact_event_outbox_tenant
    ON foghorn.artifact_event_outbox(tenant_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_foghorn_artifact_event_outbox_stream
    ON foghorn.artifact_event_outbox(stream_id, created_at DESC)
    WHERE stream_id <> '';

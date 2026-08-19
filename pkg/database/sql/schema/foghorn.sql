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
    -- STABLE billing attribution captured at WRITE time (never recomputed from mutable tenant routing at read):
    -- TRUE means the bytes are durable on THIS cell's local S3 backend and are billable by this provider. Set
    -- when a local mint is claimed/completed or a VOD is uploaded to local S3; left FALSE for playback-federation
    -- adopted-remote rows (bytes on another provider's backend). Cross-provider settlement is the RFC's scope.
    durable_backend_local BOOLEAN NOT NULL DEFAULT false,
    -- Deterministic fingerprint (BackendFingerprint over kind/bucket/endpoint/region/prefix) of the immutable local
    -- store these bytes were written to, recorded when storage is ASSIGNED (upload/artifact creation; freeze at claim).
    -- Cleanup deletes locally ONLY on an EXACT match against the cell's current store and fails closed otherwise
    -- (empty/legacy or a mismatch after a forbidden repoint → retained, never a guessed-store delete). Legacy NULL rows
    -- are adopted once at boot; a remote-owned row routes via the federation delegate, never this column.
    backend_id TEXT,

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
        -- s3: frozen to S3; warm copies served via Helmsman read-through relay
    s3_url VARCHAR(500),
    sync_status VARCHAR(20) DEFAULT 'pending',
        -- pending: not synced
        -- in_progress: syncing
        -- synced: on S3
        -- failed: sync error (retryable)
        -- lost_local: local source gone before any sync; terminal tombstone, never retried
    sync_error TEXT,
    last_sync_attempt TIMESTAMP,
    -- Outstanding sync-attempt identity: the (request_id [SERVER-MINTED attempt id], node_id [authenticated connection]) of the in-flight
    -- attempt, set when a row transitions to sync_status='in_progress' (freeze permission granted /
    -- reconciler dispatch). The request_id is minted by Foghorn at permission time and echoed by the node;
    -- authentication is the authenticated node connection; this pair is a causal/idempotency binding, NOT a
    -- secret capability. A SyncComplete is applied
    -- ONLY when it matches this EXACT (request_id, node_id) attempt and the row is still in_progress, so
    -- a duplicate, stale, or wrong-attempt completion is a guarded no-op. The artifact-owner tenant_id
    -- also scopes every completion read/write as PARTITION SCOPING sourced from the locked row
    -- (satisfying the tenant-filter rule). Cleared when the attempt terminates (sync completion success
    -- or failure, stale-freeze recovery) and when the artifact is deleted (VOD DeleteVod), so a late
    -- completion for an abandoned/terminal attempt matches nothing.
    sync_request_id VARCHAR(200),
    sync_node_id VARCHAR(100),
    -- Server-derived CANONICAL descriptor selected when this attempt is claimed (freeze permission /
    -- reconciler dispatch). It is the STABLE identity from which the attempt-scoped STAGING key
    -- (sync_object_key + '.staging.' + sync_request_id) and the immutable published key are derived; the
    -- node's presigned PUT targets STAGING, never this key. Retained across attempts for key derivation.
    sync_object_key TEXT,
    -- The IMMUTABLE, attempt-versioned key of the CURRENT durable object (sync_object_key + '.att-' +
    -- <publishing attempt id>). Completion PUBLISHES by promoting the verified staging object to a fresh
    -- candidate key and then FLIPPING this pointer in the same transaction — so a promote never overwrites a
    -- served object, and a rollback leaves the previous pointer (and its object) untouched. This is the
    -- authoritative read/delete address: s3_url and vod_metadata.s3_key are kept in sync with it. NULL for
    -- legacy rows (fall back to s3_url / sync_object_key). The superseded object from a re-publish is enqueued
    -- for durable cleanup at commit.
    active_object_key TEXT,
    -- The IMMUTABLE, attempt-versioned key of the current .dtsh index (control.FreezePublishDtshKey =
    -- sync_object_key + '.dtsh.att-' + attempt). Version-addressed like the media object (NOT co-located at a
    -- fixed <media>.dtsh) so a late/duplicate attempt writes a DIFFERENT key and can never overwrite the live
    -- index before losing its CAS. Reads use this key; NULL legacy rows fall back to <active_object_key>.dtsh.
    active_dtsh_key TEXT,
    failure_count INT NOT NULL DEFAULT 0,
    frozen_at TIMESTAMP,
    dtsh_synced BOOLEAN DEFAULT FALSE,      -- True if .dtsh index was synced
    -- Incremental .dtsh sync attempt identity. A .dtsh sync runs on an ALREADY-SYNCED artifact
    -- (the main upload finished; the Mist index arrived after), so it can't reuse the main
    -- sync_status/sync_request_id attempt. This is its OWN attempt: set to 'in_progress' with the
    -- request/node when TriggerDtshSync dispatches, and a DtshSync completion (success OR failure)
    -- is applied ONLY when it matches this exact attempt — so a stale/duplicate/wrong-node dtsh
    -- completion can neither flip dtsh_synced nor advance chapter reclaim. Cleared on any terminal
    -- transition; a 'failed' attempt is retryable (dtsh_failure_count drives backoff).
    dtsh_status VARCHAR(20),                 -- NULL | in_progress | failed (synced is recorded on dtsh_synced)
    dtsh_sync_request_id VARCHAR(200),
    dtsh_sync_node_id VARCHAR(100),
    dtsh_last_attempt TIMESTAMP,
    dtsh_failure_count INT NOT NULL DEFAULT 0,

    -- ===== DVR-SPECIFIC TIMING =====
    started_at TIMESTAMP,
    ended_at TIMESTAMP,
    duration_seconds INTEGER,
    duration_ms BIGINT,                     -- Precise measured duration (ms) for clip/VOD from the processing output; NULL for DVR (second-granularity only, from duration_seconds)
    tracks JSONB,                           -- Finalized A/V track summary captured from the completion-validated ProcessingJobResult (the processing+<hash> job's accepted RECORDING_END); durable source for the Commodore catalog projection
    -- Commodore catalog projection revisions. catalog_revision is a source-owned monotonic
    -- revision bumped from a sequence by a trigger on every catalog-relevant row change
    -- (distinct values even for same-millisecond updates). catalog_synced_rev is the last
    -- revision the reconciler confirmed Commodore has covered. The reconciler projects any
    -- row with catalog_revision > catalog_synced_rev (oldest revision first) and advances the
    -- watermark ONLY on confirmed coverage, so every authoritative row is eventually covered
    -- and a same-revision/not-found result never falsely marks a row synchronized.
    catalog_revision BIGINT NOT NULL DEFAULT 0,
    catalog_synced_rev BIGINT NOT NULL DEFAULT 0,
    -- Projection quarantine: when the reconciler can't build a valid snapshot for a row
    -- (unsupported type, malformed persisted tracks), it records the offending revision +
    -- reason here instead of falsely advancing catalog_synced_rev (which would claim
    -- Commodore coverage it never got). The scan skips rows at/below catalog_quarantined_rev;
    -- a later re-mutation bumps catalog_revision above it and re-enqueues the row.
    catalog_quarantined_rev BIGINT NOT NULL DEFAULT 0,
    catalog_quarantine_error TEXT,

    -- ===== THUMBNAIL STATE =====
    has_thumbnails BOOLEAN DEFAULT FALSE,   -- True after THUMBNAIL_UPDATED upload completes (DVR sprite sheets)
    -- The cluster whose Chandler serves the thumbnail — the tenant's OFFICIAL durable cluster the winning assignment
    -- projected to, NOT necessarily storage_cluster_id (bytes) or origin_cluster_id (provenance). Recorded at projection
    -- settlement alongside has_thumbnails (same catalog-revision bump). NULL = legacy/unprojected → readers COALESCE to
    -- storage/origin. Evidence-based, so a BYOC/cross-cell artifact links the correct Chandler.
    thumbnail_serving_cluster_id VARCHAR(100),

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
    dvr_processes_json   TEXT,              -- Tenant's live thumbnail MistProc config snapshot for the dvr+ rolling-DVR surface (resolved at StartDVR)
    -- Durable DVR start-dispatch descriptor (DVR rows only). Persisted with the 'requested'->'starting'
    -- transition: the target storage node plus every field needed to rebuild the DVRStartRequest, and a
    -- 'state' key ('pending' = start dispatched, awaiting node ack; 'stop_pending' = a compensating stop
    -- must be drained). jobs.DVRStartingRecoveryJob reads it to idempotently re-dispatch (or finalize) a
    -- recording whose node ack never arrived, so a 'starting' row is never permanently strandable.
    dvr_start_dispatch   JSONB,
    -- Ingest session (foghorn.ingest_sessions.id) this recording is bound to. A same-node
    -- reconnect is a new session id → a new recording; PUSH_INPUT_CLOSE finalizes the
    -- recording for exactly the closing session. See migration 045.
    ingest_generation    UUID,

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
CREATE INDEX IF NOT EXISTS idx_foghorn_artifacts_stream_internal ON foghorn.artifacts(stream_internal_name);
CREATE INDEX IF NOT EXISTS idx_foghorn_artifacts_ingest_generation ON foghorn.artifacts(ingest_generation) WHERE ingest_generation IS NOT NULL;
-- Drives the catalog-projection scan. Ordered by catalog_synced_rev (projection age) so the
-- reconciler serves the LEAST-RECENTLY-PROJECTED rows first, not the least-recently-mutated:
-- a continuously-mutating cohort (e.g. active DVRs minting revisions on every segment) can't
-- stay at the head of the queue and starve rows behind it. catalog_revision is the stable
-- tie-breaker.
CREATE INDEX IF NOT EXISTS idx_foghorn_artifacts_catalog_projection
    ON foghorn.artifacts(catalog_synced_rev, catalog_revision)
    WHERE catalog_revision > catalog_synced_rev;

-- catalog_next_attempt_at backs off a row the reconciler couldn't project (Commodore RPC
-- failure, or the catalog row isn't registered yet). Without it, such a row never advances its
-- watermark and — since the scan is oldest-watermark-first + LIMIT batch — a cluster of stuck
-- rows would head-of-line block newer artifacts every pass. The reconciler stamps it on a
-- non-advancing outcome and the scan skips rows still in backoff, so newer rows always get a
-- turn while stuck rows are retried periodically. It is NOT a snapshot-projected column, so the
-- catalog-revision trigger ignores it.
ALTER TABLE foghorn.artifacts ADD COLUMN IF NOT EXISTS catalog_next_attempt_at TIMESTAMPTZ;
-- Consecutive failed projection attempts, driving EXPONENTIAL backoff. A fixed backoff shorter
-- than the reconcile interval would leave a permanently-failing row eligible again before every
-- pass (re-filling the batch and starving newer rows); exponential growth quickly pushes a
-- genuinely-stuck row out to a long backoff so newer rows always get a turn. Reset to 0 on a
-- successful projection.
ALTER TABLE foghorn.artifacts ADD COLUMN IF NOT EXISTS catalog_projection_attempts INTEGER NOT NULL DEFAULT 0;

-- Source-owned monotonic catalog revision. A sequence gives distinct values even for
-- same-millisecond updates, so the Commodore projection's revision guard never conflates
-- two distinct authoritative states. The BEFORE UPDATE trigger skips watermark-only writes
-- (catalog_synced_rev changed) so the reconciler advancing the watermark doesn't re-dirty
-- the row (which would loop forever).
CREATE SEQUENCE IF NOT EXISTS foghorn.artifact_catalog_revision_seq AS BIGINT;

CREATE OR REPLACE FUNCTION foghorn.bump_artifact_catalog_revision() RETURNS trigger AS $$
BEGIN
    NEW.catalog_revision := nextval('foghorn.artifact_catalog_revision_seq');
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS artifact_catalog_revision_ins ON foghorn.artifacts;
CREATE TRIGGER artifact_catalog_revision_ins
    BEFORE INSERT ON foghorn.artifacts
    FOR EACH ROW EXECUTE FUNCTION foghorn.bump_artifact_catalog_revision();

-- Only bump when a field the catalog snapshot actually projects changes — so routine node
-- reports (which touch updated_at/access_count/last_seen for every stored artifact every
-- ~10s) do NOT re-dirty every row and starve the bounded reconciler batch. The
-- catalog_synced_rev guard also skips the reconciler's own watermark write.
DROP TRIGGER IF EXISTS artifact_catalog_revision_upd ON foghorn.artifacts;
CREATE TRIGGER artifact_catalog_revision_upd
    BEFORE UPDATE ON foghorn.artifacts
    FOR EACH ROW
    WHEN (
        OLD.catalog_synced_rev IS NOT DISTINCT FROM NEW.catalog_synced_rev
        AND (
            OLD.artifact_type IS DISTINCT FROM NEW.artifact_type
            OR OLD.status IS DISTINCT FROM NEW.status
            OR OLD.size_bytes IS DISTINCT FROM NEW.size_bytes
            OR OLD.duration_ms IS DISTINCT FROM NEW.duration_ms
            OR OLD.duration_seconds IS DISTINCT FROM NEW.duration_seconds
            OR OLD.tracks IS DISTINCT FROM NEW.tracks
            OR OLD.sync_status IS DISTINCT FROM NEW.sync_status
            OR OLD.dtsh_synced IS DISTINCT FROM NEW.dtsh_synced
            OR OLD.frozen_at IS DISTINCT FROM NEW.frozen_at
            OR OLD.storage_location IS DISTINCT FROM NEW.storage_location
            OR OLD.storage_cluster_id IS DISTINCT FROM NEW.storage_cluster_id
            OR OLD.has_thumbnails IS DISTINCT FROM NEW.has_thumbnails
            OR OLD.thumbnail_serving_cluster_id IS DISTINCT FROM NEW.thumbnail_serving_cluster_id
            OR OLD.retention_until IS DISTINCT FROM NEW.retention_until
            OR OLD.error_message IS DISTINCT FROM NEW.error_message
        )
    )
    EXECUTE FUNCTION foghorn.bump_artifact_catalog_revision();

-- Terminal-state / freeze-attempt-identity contract. When an artifact transitions to a terminal status
-- (deleted/expired/aborted) via ANY writer (VOD/clip delete, retention, DVR/chapter cascade, abort
-- recovery, purge), clear the outstanding freeze + .dtsh attempt identity so a late completion for the
-- abandoned attempt matches nothing and stale recovery can never rewrite the terminal row. sync_object_key
-- is RETAINED so the purge sweep can free an object whose PUT lands after deletion. Centralized here so
-- every terminal writer is covered without touching each site.
CREATE OR REPLACE FUNCTION foghorn.clear_sync_identity_on_terminal() RETURNS trigger AS $$
BEGIN
    -- Before clearing the attempt identity, durably enqueue the abandoned attempt's STAGING objects AND its
    -- published CANDIDATE (+ co-located .dtsh) for cleanup: once the request ids are NULL, purge can no longer
    -- DERIVE these keys, so a PUT/promote that landed for the abandoned attempt would leak. The MAIN attempt
    -- enqueues all FOUR keys it can produce (a main upload can BUNDLE a .dtsh, staged at <k>.dtsh.staging.<req>
    -- and promoted to <k>.dtsh.att-<req>), mirroring control.applySyncCompletionFailure's enqueue set:
    -- staging <k>.staging.<req>; .dtsh staging <k>.dtsh.staging.<req>; media candidate <k>.att-<req>;
    -- .dtsh candidate <k>.dtsh.att-<req>.
    -- backend_id carries the artifact's recorded owner (OLD.backend_id) onto each queued key: these staging objects
    -- live on the artifact's OWN store, so the strict cleanup worker resolves that recorded backend rather than the
    -- current store. NULL only for a legacy artifact not yet adopted (the worker then retains the row, never guesses).
    IF OLD.sync_object_key IS NOT NULL AND OLD.sync_object_key <> '' THEN
        IF OLD.sync_request_id IS NOT NULL AND OLD.sync_request_id <> '' THEN
            INSERT INTO foghorn.staging_cleanup_queue (object_key, backend_id) VALUES
                (OLD.sync_object_key || '.staging.' || OLD.sync_request_id, OLD.backend_id),
                (OLD.sync_object_key || '.dtsh.staging.' || OLD.sync_request_id, OLD.backend_id),
                (OLD.sync_object_key || '.att-' || OLD.sync_request_id, OLD.backend_id),
                (OLD.sync_object_key || '.dtsh.att-' || OLD.sync_request_id, OLD.backend_id)
            ON CONFLICT (object_key) DO NOTHING;
        END IF;
        IF OLD.dtsh_sync_request_id IS NOT NULL AND OLD.dtsh_sync_request_id <> '' THEN
            INSERT INTO foghorn.staging_cleanup_queue (object_key, backend_id) VALUES
                (OLD.sync_object_key || '.dtsh.staging.' || OLD.dtsh_sync_request_id, OLD.backend_id),
                (OLD.sync_object_key || '.dtsh.att-' || OLD.dtsh_sync_request_id, OLD.backend_id)
            ON CONFLICT (object_key) DO NOTHING;
        END IF;
    END IF;
    NEW.sync_request_id := NULL;
    NEW.sync_node_id := NULL;
    NEW.dtsh_sync_request_id := NULL;
    NEW.dtsh_sync_node_id := NULL;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS artifact_clear_sync_identity_on_terminal ON foghorn.artifacts;
-- WHEN gates on OLD identity: the production VOD/clip delete clears NEW.sync_request_id in the SAME terminal
-- UPDATE, so a NEW-gated predicate would never fire and the staging/candidate keys would be lost.
CREATE TRIGGER artifact_clear_sync_identity_on_terminal
    BEFORE UPDATE ON foghorn.artifacts
    FOR EACH ROW
    WHEN (
        NEW.status IN ('deleted', 'expired', 'aborted')
        AND OLD.status IS DISTINCT FROM NEW.status
        AND (OLD.sync_request_id IS NOT NULL OR OLD.sync_node_id IS NOT NULL
             OR OLD.dtsh_sync_request_id IS NOT NULL OR OLD.dtsh_sync_node_id IS NOT NULL)
    )
    EXECUTE FUNCTION foghorn.clear_sync_identity_on_terminal();

-- Scoped invariants for the freeze-attempt state machine:
--   1. the main AND the .dtsh request_id/node_id pairs are each set/cleared TOGETHER (no half-identity).
--   2. a terminal artifact holds NO active attempt identity — ALL FOUR columns null (trigger enforces on write).
--   3. an active main freeze identity implies the freezing/in-progress state with a bound descriptor.
ALTER TABLE foghorn.artifacts
    DROP CONSTRAINT IF EXISTS chk_foghorn_artifacts_sync_identity_paired;
ALTER TABLE foghorn.artifacts
    ADD CONSTRAINT chk_foghorn_artifacts_sync_identity_paired
        CHECK ((sync_request_id IS NULL) = (sync_node_id IS NULL));
ALTER TABLE foghorn.artifacts
    DROP CONSTRAINT IF EXISTS chk_foghorn_artifacts_dtsh_identity_paired;
ALTER TABLE foghorn.artifacts
    ADD CONSTRAINT chk_foghorn_artifacts_dtsh_identity_paired
        CHECK ((dtsh_sync_request_id IS NULL) = (dtsh_sync_node_id IS NULL));
ALTER TABLE foghorn.artifacts
    DROP CONSTRAINT IF EXISTS chk_foghorn_artifacts_terminal_no_identity;
ALTER TABLE foghorn.artifacts
    ADD CONSTRAINT chk_foghorn_artifacts_terminal_no_identity
        CHECK (
            status NOT IN ('deleted', 'expired', 'aborted')
            OR (sync_request_id IS NULL AND sync_node_id IS NULL
                AND dtsh_sync_request_id IS NULL AND dtsh_sync_node_id IS NULL)
        );
-- NULL-safe: a CHECK passes on NULL, so the governing columns are compared with IS NOT DISTINCT FROM /
-- IS FALSE (never bare '=') — a NULL status/storage_location/sync_status/dtsh_synced can no longer let an
-- active identity slip through. The descriptor must be present AND non-blank (trimmed).
ALTER TABLE foghorn.artifacts
    DROP CONSTRAINT IF EXISTS chk_foghorn_artifacts_active_freeze_state;
ALTER TABLE foghorn.artifacts
    ADD CONSTRAINT chk_foghorn_artifacts_active_freeze_state
        CHECK (
            sync_request_id IS NULL
            OR (status IS NOT DISTINCT FROM 'ready'
                AND storage_location IS NOT DISTINCT FROM 'freezing'
                AND sync_status IS NOT DISTINCT FROM 'in_progress'
                AND NULLIF(BTRIM(sync_object_key), '') IS NOT NULL)
        );
-- An active .dtsh attempt runs on an already-synced CLIP or VOD whose index isn't yet marked synced. Whole-DVR
-- .dtsh sync was retired (a DVR is reclaimed segment-wise; its chapters freeze as their own VOD artifacts), so
-- a DVR row may never hold an active .dtsh identity. NULL-safe: the lifecycle sub-expression is COALESCEd to
-- false so a NULL status/artifact_type cannot let a NULL/processing/failed/requested artifact hold one.
ALTER TABLE foghorn.artifacts
    DROP CONSTRAINT IF EXISTS chk_foghorn_artifacts_active_dtsh_state;
ALTER TABLE foghorn.artifacts
    ADD CONSTRAINT chk_foghorn_artifacts_active_dtsh_state
        CHECK (
            dtsh_sync_request_id IS NULL
            OR (dtsh_status IS NOT DISTINCT FROM 'in_progress'
                AND sync_status IS NOT DISTINCT FROM 'synced'
                AND dtsh_synced IS FALSE
                AND COALESCE(artifact_type IN ('clip', 'vod') AND status = 'ready', false))
        );

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

    -- ===== PRESENCE ROLE (of this node's local copy) =====
    -- 'origin' = the full file written locally by the recording/processing sidecar
    -- that produced it; eligible to serve cross-cluster peer-relay reads when
    -- is_complete=true. This is a producer/serving role, not the durable copy (that
    -- lives in object storage). 'cache' = a copy synced from S3; never authoritative
    -- for peer-relay.
    role TEXT NOT NULL DEFAULT 'cache' CHECK (role IN ('origin', 'cache')),
    -- Writer-authoritative: only flipped true by a finalizer that knows the
    -- file is fully written (clip create, processing finalize, DVR chapter
    -- finalize — each registers its own VOD artifact hash). Polling never
    -- sets this.
    is_complete BOOLEAN NOT NULL DEFAULT false,

    -- CAPTURE marker (from artifact_node_copy_version_seq): the version of the last node-copy
    -- event EMITTED for this row — a present event (GAINED/UPDATED) records the live version, a
    -- LOST records 0. So 0 = "no present event emitted" (never emitted, or last event was LOST).
    -- The reconcile pass re-emits =0 present rows so every present copy gets its GAINED once —
    -- this is emission CORRECTNESS (healing rows created by non-emitting writers: DVR-start /
    -- reconciler / segment inserts), NOT loss recovery.
    --
    -- The durable, authoritative record of which nodes hold a copy is THIS table itself,
    -- re-asserted by every node's ~10s report (UPSERT) and read directly by the media plane for
    -- routing / serving / peer-relay. The ClickHouse artifact_node_copy_current table is a
    -- DERIVED ANALYTICS projection (the library "node copies" panel only) — nothing operational
    -- reads it. So a wiped projection is NOT a scenario to design around: it merely omits
    -- still-present stable copies from that analytics panel until they next transition. There is
    -- deliberately no reseed mechanism, and none is needed.
    last_emitted_version BIGINT NOT NULL DEFAULT 0,

    PRIMARY KEY (artifact_hash, node_id)
);

-- Whole-node artifact report ordering. Foghorn issues each node control connection a monotonic
-- ownership fence from node_control_fence_seq when it registers; a report is ordered by
-- (connection_fence, report_seq), where report_seq is the sidecar's per-connection counter. Each
-- poller report persists on its own goroutine, so DB commit order is not report order; the
-- upsert/orphan paths advance the watermark via an atomic compare-and-set (DO UPDATE only when the
-- incoming pair beats the stored one) and drop any report that loses. A reconnect gets a strictly
-- higher fence and supersedes; a delayed report from a superseded connection loses. This is an
-- ownership fence, restart-safe without wall-clock ordering, and a nextval() per connection (not a
-- report hot path) that stays monotonic across a Redis restart.
CREATE SEQUENCE IF NOT EXISTS foghorn.node_control_fence_seq AS BIGINT;

-- Per-node high-water mark of the last APPLIED (connection_fence, seq). The durable backstop for
-- the in-memory acceptance gate; an internal/eviction mutation persists unversioned and bypasses it.
CREATE TABLE IF NOT EXISTS foghorn.node_artifact_report_watermark (
    node_id VARCHAR(100) PRIMARY KEY,
    connection_fence BIGINT NOT NULL,
    seq BIGINT NOT NULL
);

-- ============================================================================
-- ARTIFACT NODE INDEXES
-- ============================================================================

CREATE INDEX IF NOT EXISTS idx_foghorn_artifact_nodes_node ON foghorn.artifact_nodes(node_id);
CREATE INDEX IF NOT EXISTS idx_foghorn_artifact_nodes_orphaned ON foghorn.artifact_nodes(is_orphaned) WHERE is_orphaned = true;
CREATE INDEX IF NOT EXISTS idx_foghorn_artifact_nodes_seen ON foghorn.artifact_nodes(last_seen_at);
CREATE INDEX IF NOT EXISTS idx_foghorn_artifact_nodes_cached ON foghorn.artifact_nodes(cached_at);
-- Peer-fallback resolver filters on (artifact_hash, role='origin',
-- is_complete=true, is_orphaned=false). Include is_orphaned in the
-- partial predicate so orphaned-but-not-cleaned rows stay out of the
-- index and don't dilute selectivity during heartbeat lulls.
CREATE INDEX IF NOT EXISTS idx_foghorn_artifact_nodes_origin_complete
    ON foghorn.artifact_nodes(artifact_hash)
    WHERE role = 'origin' AND is_complete = true AND is_orphaned = false;

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
    finalize_node_id       VARCHAR(100),
        -- Node this finalize attempt was dispatched to. Persisted at
        -- MarkChapterFinalizing (before the node can report) so the result +
        -- progress handlers bind the reporting connection to the assignment —
        -- a chapter-finalize job is routed around processing_jobs, so this is
        -- its only reporting-node authorization anchor.
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
    CONSTRAINT chk_foghorn_dvr_chapters_range CHECK (end_ms > start_ms),
    -- A finalize-node assignment is valid ONLY while the chapter is actively finalizing. Every transition out
    -- of 'finalizing' (finalized/closed/failed) clears finalize_node_id, so a retired node can never authorize
    -- a later transition. Encoded as an invariant, not just enforced procedurally.
    CONSTRAINT chk_foghorn_dvr_chapters_finalize_node CHECK (finalize_node_id IS NULL OR state = 'finalizing')
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

-- Durable ingest-session identity: one publisher connection = (tenant, node, Mist
-- connector PID), fenced by the start trigger UUID/time. Minted on the accepted
-- PUSH_REWRITE (PID from the X-PID header), ended on PUSH_INPUT_CLOSE carrying the
-- same PID; each DVR recording binds to a session id so a same-node reconnect is a new
-- session. See migration foghorn/v0.2.96/expand/045_dvr_ingest_sessions.sql.
CREATE TABLE IF NOT EXISTS foghorn.ingest_sessions (
    id                     UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id              UUID NOT NULL,
    node_id                VARCHAR(100) NOT NULL,
    stream_internal_name   VARCHAR(255) NOT NULL,
    connector_pid          BIGINT NOT NULL CHECK (connector_pid > 0),
    start_trigger_uuid     VARCHAR(64) NOT NULL CHECK (start_trigger_uuid <> ''),
    started_at_unix_millis BIGINT NOT NULL CHECK (started_at_unix_millis > 0),
    started_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ended_at               TIMESTAMPTZ,
    ended_at_unix_millis   BIGINT,
    ended_reason           VARCHAR(64),
    -- Virtual media cluster this session was ADMITTED into, captured at mint. Placement claim,
    -- renewal, and release all replay this value rather than re-resolving the node's current
    -- cluster, so a node reassigned mid-session cannot strand the claim it was admitted under.
    ingest_cluster_id      VARCHAR(100),
    -- Durable DVR start intent (StartDVR inputs) for a record:true session, set at admission
    -- before the push is approved; DVRIntentRecovery replays the start if the async StartDVR
    -- was lost to a crash. NULL for a non-recording session.
    dvr_intent             JSONB,
    -- DVRIntentRecovery bookkeeping: leased-attempt counter (diagnostic; retries are UNBOUNDED while
    -- the session is active — there is no attempt cap), per-attempt HA-safe lease/backoff, and an
    -- explicit operator-visible terminal error (set ONLY for an undecodable payload).
    dvr_intent_attempts    INT NOT NULL DEFAULT 0,
    dvr_intent_lease_until TIMESTAMPTZ,
    dvr_intent_error       TEXT,
    -- Admission is not complete until the source projection wins the shared Redis revision CAS.
    -- A pending row remains the stream authority while the blocking trigger retries, then the
    -- ingest-session reaper retires it if projection never confirms.
    projection_state       VARCHAR(16) NOT NULL DEFAULT 'pending',
    source_revision        BIGINT,
    projected_at           TIMESTAMPTZ,
    UNIQUE (id, tenant_id, stream_internal_name),
    CONSTRAINT ck_foghorn_ingest_sessions_projection_state
        CHECK (projection_state IN ('pending', 'active')),
    CONSTRAINT ck_foghorn_ingest_sessions_source_revision
        CHECK (source_revision IS NULL OR source_revision > 0)
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_foghorn_ingest_sessions_active_pid
    ON foghorn.ingest_sessions(tenant_id, node_id, connector_pid)
    WHERE ended_at IS NULL;
-- HA authority: at most one ACTIVE session per (tenant, stream) across ALL nodes (admission is
-- serialized cross-replica by a (tenant, stream) advisory lock; this is the durable backstop), and
-- a Mist trigger UUID identifies one trigger EXECUTION — stable across its retries, distinct for a
-- later reconnect — so it is unique per (tenant, node). See migration
-- foghorn/v0.2.96/expand/046_ingest_session_stream_authority.sql.
CREATE UNIQUE INDEX IF NOT EXISTS uq_foghorn_ingest_sessions_active_per_stream
    ON foghorn.ingest_sessions(tenant_id, stream_internal_name)
    WHERE ended_at IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_foghorn_ingest_sessions_trigger_uuid
    ON foghorn.ingest_sessions(tenant_id, node_id, start_trigger_uuid);
CREATE INDEX IF NOT EXISTS idx_foghorn_ingest_sessions_stream
    ON foghorn.ingest_sessions(tenant_id, stream_internal_name);
CREATE INDEX IF NOT EXISTS idx_foghorn_ingest_sessions_pending_projection
    ON foghorn.ingest_sessions(started_at)
    WHERE ended_at IS NULL AND projection_state = 'pending';
-- Enforce the DVR generation binding in schema (same-tenant, same-stream, existing session)
-- and one active DVR per generation. Placed after ingest_sessions so the FK target exists.
ALTER TABLE foghorn.artifacts
    DROP CONSTRAINT IF EXISTS fk_foghorn_artifacts_ingest_generation;
ALTER TABLE foghorn.artifacts
    ADD CONSTRAINT fk_foghorn_artifacts_ingest_generation
    FOREIGN KEY (ingest_generation, tenant_id, stream_internal_name)
    REFERENCES foghorn.ingest_sessions(id, tenant_id, stream_internal_name);
CREATE UNIQUE INDEX IF NOT EXISTS uq_foghorn_artifacts_active_dvr_per_generation
    ON foghorn.artifacts(ingest_generation)
    WHERE ingest_generation IS NOT NULL
          AND artifact_type = 'dvr'
          AND status IN ('requested', 'starting', 'recording');

-- Durable close-before-insert evidence: a PUSH_INPUT_CLOSE observed for a connector with no ingest
-- session row yet (concurrent trigger dispatch + WAL redelivery can process a close before its own
-- PUSH_REWRITE). CreateIngestSession consults this in the mint path (under the (tenant, stream)
-- advisory lock the close also takes) and denies a late rewrite whose start is at or before a recorded
-- close for the same connector, so a dead publisher is never resurrected as an active session. Swept
-- on a TTL by the ingest session reaper. See migration
-- foghorn/v0.2.96/expand/048_ingest_close_tombstones.sql.
CREATE TABLE IF NOT EXISTS foghorn.ingest_close_tombstones (
    tenant_id            UUID NOT NULL,
    node_id              VARCHAR(100) NOT NULL,
    connector_pid        BIGINT NOT NULL,
    stream_internal_name VARCHAR(255) NOT NULL,
    close_unix_millis    BIGINT NOT NULL,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_foghorn_ingest_close_tombstones_lookup
    ON foghorn.ingest_close_tombstones(tenant_id, node_id, connector_pid, stream_internal_name, close_unix_millis);
CREATE INDEX IF NOT EXISTS idx_foghorn_ingest_close_tombstones_created
    ON foghorn.ingest_close_tombstones(created_at);

-- Monotonic revision ordering source-ownership projections for the cluster-wide registry. Each source
-- transition (ProjectSourceIfCurrent, the offline flip) draws nextval under the (tenant, stream)
-- advisory lock, so a later transition carries a strictly higher revision; the registry merges
-- source-ownership fields by it (highest wins) so a stale replica cannot clobber the real publisher via
-- a last-writer-wins location write. See migration
-- foghorn/v0.2.96/expand/049_source_projection_revision.sql.
CREATE SEQUENCE IF NOT EXISTS foghorn.source_projection_revision;

-- Durable, revision-fenced stream-offline work. The transition is enqueued under the same
-- (tenant, stream) advisory lock as admission; workers apply it only while no newer active session
-- exists. All effects are idempotent, so a lease loss or process crash safely retries the row.
CREATE TABLE IF NOT EXISTS foghorn.ingest_offline_effects (
    id                   BIGSERIAL PRIMARY KEY,
    tenant_id            UUID NOT NULL,
    stream_internal_name VARCHAR(255) NOT NULL,
    source_node_id       VARCHAR(100) NOT NULL,
    source_generation    UUID,
    source_revision      BIGINT NOT NULL CHECK (source_revision > 0),
    set_node_offline     BOOLEAN NOT NULL DEFAULT FALSE,
    teardown_stream      BOOLEAN NOT NULL DEFAULT FALSE,
    broadcast_offline    BOOLEAN NOT NULL DEFAULT FALSE,
    decklog_trigger      BYTEA,
    state                VARCHAR(16) NOT NULL DEFAULT 'pending'
                             CHECK (state IN ('pending', 'applied', 'superseded')),
    attempts             INTEGER NOT NULL DEFAULT 0,
    next_attempt_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    leased_until         TIMESTAMPTZ,
    lease_token          UUID,
    -- Durable authority routing: the instance owning this row's outstanding authority-bound work,
    -- recorded on deferral; the claim admits only that instance until the affinity goes stale (10s).
    claim_affinity       VARCHAR(100),
    last_error           TEXT,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    applied_at           TIMESTAMPTZ,
    UNIQUE (tenant_id, stream_internal_name, source_revision)
);
CREATE INDEX IF NOT EXISTS idx_foghorn_ingest_offline_effects_pending
    ON foghorn.ingest_offline_effects(next_attempt_at, id)
    WHERE state = 'pending';

CREATE INDEX IF NOT EXISTS idx_foghorn_ingest_offline_effects_terminal
    ON foghorn.ingest_offline_effects(updated_at)
    WHERE state IN ('applied', 'superseded');

-- Durable, leased application of the once-only ADMISSION effects (mirror of the offline outbox
-- above). The obligation row is inserted in the SAME transaction that confirms the source
-- projection (pending→active), so exactly one obligation exists per admitted generation and a
-- worker proves and settles each obligation under the stream advisory lock, with bounded push-target,
-- drain, federation, and Decklog I/O between those locked transactions. Only the obligation worker
-- executes these effects; trigger retries do not run them inline. The Decklog ingest
-- event is stamped with the deterministic event_id (the generation) so worker re-deliveries
-- deduplicate downstream.
CREATE TABLE IF NOT EXISTS foghorn.ingest_admission_effects (
    id                  BIGSERIAL PRIMARY KEY,
    tenant_id           UUID NOT NULL,
    stream_internal_name VARCHAR(255) NOT NULL,
    node_id             VARCHAR(100) NOT NULL,
    source_generation   UUID NOT NULL UNIQUE,
    source_revision     BIGINT NOT NULL CHECK (source_revision > 0),
    prior_owner_node_id VARCHAR(100) NOT NULL DEFAULT '',
    -- Receiver fence for delayed name-scoped drain commands.
    prior_owner_source_generation UUID,
    -- Serialized ipcpb.ActivatePushTargets (stream name + target specs); NULL when the stream has
    -- no configured push targets.
    push_targets        BYTEA,
    -- JSON array of complete federation peer hints (cluster ID, internal Foghorn address, and
    -- lifecycle). The leader establishes stream tracking from this durable payload before the
    -- broadcast leg can settle.
    peer_clusters       TEXT,
    broadcast_live      BOOLEAN NOT NULL DEFAULT FALSE,
    -- Serialized, enriched ipcpb.MistTrigger for the admission's Decklog ingest event, stamped with
    -- the deterministic event_id (the generation). NULL when the event needs no ledgered delivery.
    decklog_trigger     BYTEA,
    -- Per-leg completion. The legs have DIFFERENT supersession rules: the Decklog event remains
    -- OWED after the admitted generation ends (the admission still happened), while the drain,
    -- activation and live broadcast become moot — a late drain destroys by runtime NAME and could
    -- kill a successor session's buffer. Remote legs (drain, activation) are completed by
    -- Helmsman's generation-correlated acknowledgements (DrainStreamResponse /
    -- ActivatePushTargetsResult), NOT by dispatch: delivery is not completion. Legs not applicable
    -- to an obligation are inserted TRUE.
    drain_done          BOOLEAN NOT NULL DEFAULT FALSE,
    activation_done     BOOLEAN NOT NULL DEFAULT FALSE,
    broadcast_done      BOOLEAN NOT NULL DEFAULT FALSE,
    decklog_done        BOOLEAN NOT NULL DEFAULT FALSE,
    state               VARCHAR(16) NOT NULL DEFAULT 'pending'
                            CHECK (state IN ('pending', 'applied', 'superseded')),
    attempts            INTEGER NOT NULL DEFAULT 0,
    next_attempt_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    leased_until        TIMESTAMPTZ,
    lease_token         UUID,
    -- Durable authority routing: the instance owning this row's outstanding authority-bound work,
    -- recorded on deferral; the claim admits only that instance until the affinity goes stale (10s).
    claim_affinity      VARCHAR(100),
    last_error          TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    applied_at          TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_foghorn_ingest_admission_effects_pending
    ON foghorn.ingest_admission_effects(next_attempt_at, id)
    WHERE state = 'pending';

-- Tombstone cleanup proves that no pending admission callback at or below a membership revision can
-- still restore it. Keep that bounded proof on the pending working set.
CREATE INDEX IF NOT EXISTS idx_foghorn_ingest_admission_effects_pending_fence
    ON foghorn.ingest_admission_effects(tenant_id, stream_internal_name, source_revision)
    WHERE state = 'pending';

-- Terminal rows are retained briefly as diagnostics; this index keeps batched retention cleanup
-- from scanning the pending working set.
CREATE INDEX IF NOT EXISTS idx_foghorn_ingest_admission_effects_terminal
    ON foghorn.ingest_admission_effects(updated_at)
    WHERE state IN ('applied', 'superseded');

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

-- Durable retry queue for deleting garbage S3 objects that a freeze produces: STAGING objects (the node's
-- attempt-scoped upload target), superseded PUBLISHED versions (the previous active_object_key a re-publish
-- replaces), and orphaned candidate objects (a publish that then lost the CAS / rolled back). S3 deletion is
-- not transactional with Postgres, so a delete is ENQUEUED (idempotently) inside the same transaction that
-- makes the object garbage — a completion commit, a stale-recovery reset, or the terminal-clear trigger — and
-- a background worker (StagingCleanupJob) drains it with retries. This worker is the ONLY collector; a
-- failed/crashed delete is retried from the durable row instead of leaking unbilled provider storage.
CREATE TABLE IF NOT EXISTS foghorn.staging_cleanup_queue (
    object_key       TEXT PRIMARY KEY,               -- S3 key of the staging object to delete (idempotent on conflict)
    enqueued_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    next_attempt_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),  -- due time; bumped with backoff on each failed delete
    leased_until     TIMESTAMPTZ,                     -- in-flight lease; a worker claims a row past next_attempt_at and unleased, so HA replicas do not double-process
    lease_token      TEXT,                            -- fences the lease: settlement matches object_key AND this token, so a worker whose lease expired and was re-claimed cannot settle another worker's row
    attempts         INTEGER NOT NULL DEFAULT 0,
    last_error       TEXT,
    backend_id       TEXT                            -- physical backend the object was written to; the worker deletes ONLY on an exact match to the cell's current store and fails closed otherwise (empty/legacy or repoint → retained, never a guessed-store delete). Fresh enqueues attribute it; legacy rows are adopted once at boot.
);
CREATE INDEX IF NOT EXISTS idx_foghorn_staging_cleanup_due ON foghorn.staging_cleanup_queue(next_attempt_at);

-- Durable keyset cursor for control.ReconcileBillingAttribution: it processes a BOUNDED batch of DISTINCT
-- (tenant, authoritative-cluster) pairs past the cursor each pass (bounding the external per-tenant resolver
-- calls for non-local clusters) and persists progress, so a slow/large prefix can never starve later locally
-- owned pairs. When a pass reaches the end (fewer than a batch remain) the cursor wraps to the start, so
-- pairs whose tenant access changed are eventually re-reviewed.
CREATE TABLE IF NOT EXISTS foghorn.billing_attribution_cursor (
    id           BOOLEAN PRIMARY KEY DEFAULT true CHECK (id),  -- single-row guard
    last_tenant  TEXT NOT NULL DEFAULT '',
    last_cluster TEXT NOT NULL DEFAULT ''
);
INSERT INTO foghorn.billing_attribution_cursor (id) VALUES (true) ON CONFLICT (id) DO NOTHING;

-- Durable keyset cursor for jobs.ArtifactReconciler.backfillActiveObjectKey: it processes a bounded batch of
-- LEGACY synced clip/vod rows PAST last_hash each pass (populating active_object_key prefix-aware from a
-- LOCAL-bucket s3_url) and persists progress, so an anomalous skipped row can never starve later rows; wraps
-- to the start on a short page.
CREATE TABLE IF NOT EXISTS foghorn.active_object_key_backfill_cursor (
    id        BOOLEAN PRIMARY KEY DEFAULT true CHECK (id),  -- single-row guard
    last_hash TEXT NOT NULL DEFAULT ''
);
INSERT INTO foghorn.active_object_key_backfill_cursor (id) VALUES (true) ON CONFLICT (id) DO NOTHING;

-- Durable publication-attempt ledger: records every object an in-flight freeze attempt will produce (staging,
-- guarded=false, always garbage once superseded; and its published candidate, guarded=true, garbage only when
-- not the live active_object_key/active_dtsh_key) BEFORE the object is promoted, so a completion that publishes
-- and then fails its guarded-CAS transaction cannot leak an unreferenced object. A committing completion
-- deletes its own rows; survivors are reconciled by jobs.reconcileFreezePublicationLedger, which skips any
-- attempt still on the artifact (request_id) so a retry never races the sweep into deleting a live object.
CREATE TABLE IF NOT EXISTS foghorn.freeze_publication_ledger (
    object_key    TEXT PRIMARY KEY,
    artifact_hash TEXT NOT NULL,
    tenant_id     TEXT NOT NULL,
    request_id    TEXT NOT NULL,
    guarded       BOOLEAN NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    backend_id    TEXT                                -- physical backend the candidate/staging object was written to; carried onto the staging_cleanup_queue row when the reconciler collects an orphan, so the delete routes to (or fails closed on) the recorded store. NULL = legacy.
);
CREATE INDEX IF NOT EXISTS idx_foghorn_freeze_pub_ledger_age ON foghorn.freeze_publication_ledger(created_at);

-- Durable keyset cursor for jobs.reconcileFreezePublicationLedger: it advances by object_key past every
-- reviewed ledger row each pass (wrapping on a short page), so rows SKIPPED because their attempt is still
-- retrying never block later orphans from being inspected.
CREATE TABLE IF NOT EXISTS foghorn.freeze_publication_ledger_cursor (
    id       BOOLEAN PRIMARY KEY DEFAULT true CHECK (id),  -- single-row guard
    last_key TEXT NOT NULL DEFAULT ''
);
INSERT INTO foghorn.freeze_publication_ledger_cursor (id) VALUES (true) ON CONFLICT (id) DO NOTHING;

-- The cell's committed local S3 backend descriptor, enforced immutable at startup. Backend
-- REPOINTING (changing bucket/endpoint/region/prefix) is not supported — historical cleanup routes by
-- the object's recorded backend and this cell wires only its current store, so a silent repoint would target the wrong
-- backend. Foghorn records its descriptor here on first boot and refuses to start if it later differs (credentials are
-- NOT part of the identity, so key rotation is fine). Makes unchanged-S3 a code-enforced invariant. Single row.
CREATE TABLE IF NOT EXISTS foghorn.cell_storage_identity (
    id           BOOLEAN PRIMARY KEY DEFAULT true CHECK (id),  -- single-row guard
    backend_id   TEXT NOT NULL,                                -- BackendFingerprint(kind,bucket,endpoint,region,prefix)
    bucket       TEXT NOT NULL,
    endpoint     TEXT NOT NULL,
    region       TEXT NOT NULL,
    prefix       TEXT NOT NULL,
    committed_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Single-row durable cutoff for the ONE-TIME attribution of legacy NULL-owner rows across every backend-owned table:
-- artifacts, thumbnail_task_assignment, stream_cleanup_obligation, staging_cleanup_queue, and freeze_publication_ledger.
-- A boot that finds no marker claims it and, in the same transaction, stamps the proven cell fingerprint onto those
-- rows' NULL backend_id; the claim makes it run exactly once, so a NULL backend appearing later is a genuine ownership
-- regression that cleanup fails closed on rather than re-attributing.
CREATE TABLE IF NOT EXISTS foghorn.backend_adoption (
    id         BOOLEAN PRIMARY KEY DEFAULT true,
    adopted_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT backend_adoption_singleton CHECK (id)
);

-- Crash-safe thumbnail publication. Server-minted, node-bound assignments; per-attempt staging + immutable
-- versioned objects; an active pointer (keyed by the globally-unique asset_key) switched transactionally. Live
-- streams have no artifact row and one attempt owns multiple files, so these are dedicated tables — not columns
-- on foghorn.artifacts. attempt_id is crypto-rand and echoed by the node at completion, binding a Foghorn-ASSIGNED
-- operation; a durable `publishing` state blocks a newer attempt from racing; a stuck attempt past `expiry` is
-- re-driven by the recovery reconciler. The pointer CAS advances on the server-owned strictly-monotonic
-- claim_seq (below), not created_at, so equal timestamps / cross-instance clock skew can never yield a non-total
-- order in which two attempts each replace the other.
CREATE SEQUENCE IF NOT EXISTS foghorn.thumbnail_attempt_seq;
CREATE TABLE IF NOT EXISTS foghorn.thumbnail_task_assignment (
    attempt_id          TEXT PRIMARY KEY,
    tenant_id           TEXT NOT NULL,
    asset_key           TEXT NOT NULL,             -- stream_id (live) or artifact_hash (VOD/clip/DVR)
    node_id             TEXT NOT NULL,             -- assigned (canonical) producing node
    destination_cluster TEXT NOT NULL,             -- official-durable destination cluster
    status              TEXT NOT NULL CHECK (status IN ('assigned','uploading','verifying','publishing','published','failed')),
    version             TEXT NOT NULL DEFAULT '',  -- immutable version segment once promoted
    -- Write-time backend evidence (I2): the strict resolver's StorageMintLocal verdict for this attempt's official
    -- destination cluster. Cleanup routes local when true — even for a locally-backed official ALIAS whose
    -- cluster id differs from this cell's — instead of misrouting to (disabled) federation and leaking the bytes.
    durable_backend_local BOOLEAN NOT NULL DEFAULT false,
    claim_seq           BIGINT NOT NULL DEFAULT nextval('foghorn.thumbnail_attempt_seq'), -- monotonic pointer-CAS rank
    superseded_at       TIMESTAMPTZ,               -- set when a newer version displaces this one; GC horizon anchor
    expiry              TIMESTAMPTZ NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- HA-safe crash-recovery lease + poison backoff for re-driving lost completions (see thumbnail_recovery.go).
    -- A worker LEASES a batch of due, unleased non-terminal attempts (fencing token) so replicas never double-drive
    -- the same one, and a not-progressed attempt is BACKED OFF (recovery_next_attempt_at) instead of re-selected at
    -- the head of every pass — so a poison attempt cannot starve other lost completions.
    recovery_leased_until    TIMESTAMPTZ,          -- in-flight recovery lease; a worker skips a leased row until it expires
    recovery_lease_token     TEXT,                 -- fences settlement against a stolen/expired lease
    recovery_attempts        INTEGER NOT NULL DEFAULT 0,
    recovery_next_attempt_at TIMESTAMPTZ,          -- due time; a backed-off attempt is not re-selected until this passes
    recovery_last_error      TEXT,
    -- Publication lease: a completion acquires this BEFORE it HEAD-verifies + promotes the staging objects. It is
    -- fenced by publish_lease_token: AcquireThumbnailPublishLease mints a token, and every settlement CASes it, so a
    -- STALE holder (lease expired, peer re-acquired) matches zero rows and cannot publish. The recovery fail-sweep
    -- HONORS the live lease (publish_leased_until). The promoted object is a per-token candidate (v/{token}/…), so a
    -- stale holder can only overwrite its OWN candidate, never the winner's.
    publish_leased_until     TIMESTAMPTZ,
    publish_lease_token      TEXT,
    -- Deterministic fingerprint (BackendFingerprint) of the immutable local store this attempt's objects were written
    -- to, captured at claim. Cleanup compares THIS recorded id against the cell's current store and deletes ONLY on an
    -- exact match; an empty/legacy id or a mismatch (forbidden repoint) fails closed (retained), never a guessed store.
    backend_id               TEXT,
    -- Durable-projection marker: stamped when the winner's objects have been projected to the DETERMINISTIC served key
    -- (thumbnails/{asset}/{file}). has_thumbnails is exposed only after this is set; recovery re-drives 'published'
    -- attempts still NULL here (a crash between the pointer CAS and the deterministic copy). NULL = not yet projected.
    deterministic_projected_at TIMESTAMPTZ,
    -- Bounded-reassert clock: the deterministic copy is a non-transactional S3 op whose destination write is
    -- unconditional, so a late straggler copy from an earlier loser can overwrite the winner's bytes. Set at settle to
    -- NOW() + max-copy-window; the reassert reconciler re-copies the still-active winner once past that window (correcting
    -- any straggler) and clears it to NULL. NULL = converged / superseded. The contract is EVENTUAL, not strict.
    deterministic_reassert_at TIMESTAMPTZ,
    -- A row may only be in a token-gated state ('publishing'/'published') if it carries a nonblank lease token. The
    -- token-fenced path (EnterThumbnailPublishingToken / PublishThumbnailAttemptToken) is the only way to reach those
    -- states in code; this makes the invariant a DB fact, so no path can create tokenless publication state.
    CONSTRAINT chk_foghorn_thumbnail_publishing_requires_token
        CHECK (status NOT IN ('publishing','published') OR (publish_lease_token IS NOT NULL AND publish_lease_token <> ''))
);
CREATE INDEX IF NOT EXISTS idx_foghorn_thumb_task_resource ON foghorn.thumbnail_task_assignment(asset_key, tenant_id);
CREATE INDEX IF NOT EXISTS idx_foghorn_thumb_task_recovery ON foghorn.thumbnail_task_assignment(status, expiry);
CREATE INDEX IF NOT EXISTS idx_foghorn_thumb_recovery_due
    ON foghorn.thumbnail_task_assignment(recovery_next_attempt_at)
    WHERE status IN ('assigned', 'uploading', 'verifying', 'publishing');
CREATE INDEX IF NOT EXISTS idx_foghorn_thumb_unprojected
    ON foghorn.thumbnail_task_assignment(updated_at)
    WHERE status = 'published' AND deterministic_projected_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_foghorn_thumb_reassert_due
    ON foghorn.thumbnail_task_assignment(deterministic_reassert_at)
    WHERE deterministic_reassert_at IS NOT NULL;

-- One row per file an attempt owns (allowlisted: poster.jpg / sprite.jpg / sprite.vtt). staging_key is the
-- per-attempt upload target; version_key is the immutable published object; etag/size are provider-observed at
-- verify. Cascades with its assignment.
CREATE TABLE IF NOT EXISTS foghorn.thumbnail_task_object (
    attempt_id  TEXT NOT NULL REFERENCES foghorn.thumbnail_task_assignment(attempt_id) ON DELETE CASCADE,
    file_name   TEXT NOT NULL,
    staging_key TEXT NOT NULL,             -- thumbnails/{asset}/.staging/{attempt}/{file}
    version_key TEXT NOT NULL DEFAULT '',  -- thumbnails/{asset}/v/{version}/{file}, set on promote
    etag        TEXT NOT NULL DEFAULT '',
    size_bytes  BIGINT NOT NULL DEFAULT 0,
    verified    BOOLEAN NOT NULL DEFAULT false,
    PRIMARY KEY (attempt_id, file_name)
);

-- The atomic active pointer: which immutable version each resource currently serves. A Foghorn-owned DB row (NOT
-- a mutable S3 object) switched by a guarded monotonic CAS, so there is no fixed mutable object to race. asset_key
-- is the GLOBALLY-UNIQUE public resource identity: a stream_id UUID or an opaque, randomly-minted clip/dvr/vod
-- hash (NOT a content hash — never recurs across tenants; Commodore enforces uniqueness and
-- foghorn.artifacts.artifact_hash is a global primary key). tenant_id is mandatory OWNERSHIP/authorization
-- attribution, never part of the identity: every mutation proves tenant ownership, but public resolution
-- (Chandler's opaque-key URL, the S3 namespace, the cache) is keyed by asset_key alone.
CREATE TABLE IF NOT EXISTS foghorn.thumbnail_active_pointer (
    asset_key      TEXT PRIMARY KEY,
    tenant_id      TEXT NOT NULL,
    -- FK to the active attempt: deleting that assignment CASCADES the pointer, so purge can never leave a
    -- dangling pointer that outlives its backing objects (the pointer and its assignment are removed together).
    active_version TEXT NOT NULL REFERENCES foghorn.thumbnail_task_assignment(attempt_id) ON DELETE CASCADE,
    -- The winning completion's per-token PRIVATE candidate segment (v/{token}/…). It is the projection SOURCE, not a
    -- served address: the winner is copied from v/{token}/… to the DETERMINISTIC served key (thumbnails/{asset}/{file})
    -- that Chandler serves; Chandler never resolves this token. A publish REQUIRES a non-empty lease token, so a
    -- published pointer ALWAYS has active_token set — the CHECK makes that a DB fact. active_version above holds the
    -- attempt_id PURELY for the FK cascade + monotonic CAS/GC anchor.
    active_token   TEXT,
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_foghorn_active_token_present CHECK (active_token IS NOT NULL)
);

-- Durable stream tombstone + thumbnail-cleanup obligation. A LIVE stream's thumbnails are keyed by
-- asset_key = stream_id, which has NO foghorn.artifacts row — so the purge job never reaches it, version GC
-- never reclaims its final version, and the terminal-status fences (which read foghorn.artifacts) never fire.
-- On stream deletion Foghorn records ONE row here inside a guarded transaction; its EXISTENCE is the durable
-- tombstone that fences claim/publish/projection for the (rowless) live stream (a projection re-check sees it and
-- skips; the delayed second sweep reclaims any straggler copy), and its drain state (status + lease/backoff) is the
-- fenced queue a background worker (StreamCleanupJob) works until the S3 bytes are confirmed gone and the control
-- rows dropped, then marks it 'cleaned'. asset_key/stream_id are globally-unique and never reused, so the tombstone
-- may persist indefinitely without colliding a future asset.
--
-- One Foghorn database belongs to ONE cell and ONE immutable S3 backend (cell_storage_identity), so an obligation has
-- a SINGLE sweep target: this cell's local store. backend_id snapshots that store's recorded fingerprint; the sweep
-- fails closed if it no longer matches the cell's current store (a forbidden repoint). Cross-cell / multi-backend
-- placement is out of scope: obligations never target another cell's store.
CREATE TABLE IF NOT EXISTS foghorn.stream_cleanup_obligation (
    asset_key            TEXT PRIMARY KEY,                         -- deleted asset's globally-unique key (live stream_id). Existence = tombstone.
    tenant_id            TEXT NOT NULL,                            -- ownership attribution: the tenant the deletion was authorized for
    backend_id           TEXT,                                     -- recorded fingerprint of the cell's immutable store; sweep deletes ONLY on an exact match and fails closed otherwise (empty/legacy or mismatch → retained). Recorded at record time; legacy rows adopted once at boot
    status               TEXT NOT NULL DEFAULT 'pending'
                         CHECK (status IN ('pending','cleaned')),  -- pending = bytes not yet swept; cleaned = bytes gone + control rows dropped. Row PERSISTS as the tombstone either way.
    enqueued_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),       -- window anchor: the delayed second sweep fires at enqueued_at + DeterministicCopyWindow
    first_swept_at       TIMESTAMPTZ,                              -- set on the first sweep; arms the delayed resurrection second sweep
    next_attempt_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),       -- drainer due time; bumped with backoff on a failed sweep, and to enqueued_at + window after the first sweep
    leased_until         TIMESTAMPTZ,                              -- in-flight lease; a worker claims a pending, due, unleased row so HA replicas do not double-sweep
    lease_token          TEXT,                                     -- fences the lease: settlement matches asset_key AND this token
    attempts             INTEGER NOT NULL DEFAULT 0,               -- capped-linear backoff counter on failing sweeps
    last_error           TEXT,
    cleaned_at           TIMESTAMPTZ                               -- set when the sweep confirmed the bytes gone + control rows dropped
);
-- Drainer scan: only pending rows are due for a sweep (cleaned rows persist purely as the tombstone).
CREATE INDEX IF NOT EXISTS idx_foghorn_stream_cleanup_due ON foghorn.stream_cleanup_obligation(next_attempt_at) WHERE status = 'pending';

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
    processes_json TEXT,                    -- Requested processing spec captured at CompleteVodUpload; read by the completing-VOD recovery job so a recovered upload reproduces the SAME requested outputs (not default processing)
    -- Full multipart completion descriptor (S3 key, upload id, ordered [{part_number, etag}]),
    -- persisted ATOMICALLY with the 'uploading'->'completing' claim in CompleteVodUpload. The
    -- completing-VOD recovery job uses it to RETRY S3 CompleteMultipartUpload (idempotent) so a crash
    -- before the client's completion call still converges a valid multipart upload to the finished
    -- object instead of failing it. A row is never 'completing' without this descriptor persisted.
    vod_completion_descriptor JSONB,

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
CREATE INDEX IF NOT EXISTS idx_foghorn_processing_jobs_queued_attempt ON foghorn.processing_jobs(status, updated_at, created_at) WHERE status = 'queued';
CREATE INDEX IF NOT EXISTS idx_foghorn_processing_jobs_node ON foghorn.processing_jobs(processing_node_id) WHERE processing_node_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_foghorn_processing_jobs_parent ON foghorn.processing_jobs(parent_job_id) WHERE parent_job_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_foghorn_processing_jobs_artifact_status ON foghorn.processing_jobs(artifact_hash, status);

-- ============================================================================
-- ARTIFACT EVENT OUTBOX
-- ============================================================================
-- Monotonic revision for artifact node-copy events. nextval() is assigned inside the
-- emitting transaction and used as the ClickHouse ReplacingMergeTree version so
-- concurrent updates converge deterministically (wall-clock ms would tie). Gaps from
-- rolled-back transactions are harmless — only strict increase matters.
CREATE SEQUENCE IF NOT EXISTS foghorn.artifact_node_copy_version_seq AS BIGINT;

-- Durable outbox for Foghorn artifact-lifecycle (DVR / VOD / Clip),
-- federation peer-registry, and artifact node-copy events. A drain worker
-- dispatches pending rows to Decklog with exponential backoff.

CREATE TABLE IF NOT EXISTS foghorn.artifact_event_outbox (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- event_kind discriminates the typed payload (clip_lifecycle,
    -- dvr_lifecycle, vod_lifecycle, federation_event, artifact_node_copy).
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
    -- Backoff gate: a failed row is not re-eligible until NOW() >= next_retry_at. Without this
    -- a permanently-failing (poison) row resets claimed_at and is immediately re-claimed every
    -- poll, and since the claim is oldest-first + LIMIT batch, a cluster of poison rows would
    -- head-of-line starve every newer event. NULL = never failed, immediately eligible.
    next_retry_at TIMESTAMPTZ,
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

-- Durable command ledger for artifact creation attempts, keyed by the Commodore
-- creation-intent request_id. A create handler writes 'accepted' FIRST — before any
-- fallible precheck (storage cap, node selection, routing, idempotency probe) — so the
-- row exists the moment the RPC runs. On success 'committed' (with the artifact's
-- catalog_revision) is written atomically with the foghorn.artifacts insert; on any
-- pre-commit error exit the handler's deferred finalizer CAS-flips the still-'accepted'
-- row to 'rejected'. GetArtifactCreationStatus is a PURE READ of this ledger so
-- Commodore's convergence sweep resolves a lost/ambiguous create by the attempt's OWN
-- recorded outcome rather than inferring rejection from artifact-row absence (a
-- still-running create has no artifact row yet). The 'accepted' INSERT is idempotent on
-- request_id (ON CONFLICT DO NOTHING) so a retry never bumps updated_at, which
-- therefore marks the accept time; every terminal write bumps it. A row left stranded
-- 'accepted' (handler crashed between the accept and its terminal write) is terminalized
-- 'rejected' by the CreationCommandExpiryJob background worker past a hard deadline,
-- never from the status read path, so a read never races a concurrent commit.
CREATE TABLE IF NOT EXISTS foghorn.artifact_creation_commands (
    request_id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    kind VARCHAR(16) NOT NULL,          -- 'clip' | 'dvr' | 'vod'
    artifact_hash VARCHAR(32) NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'accepted', -- accepted | committed | rejected
    catalog_revision BIGINT NOT NULL DEFAULT 0,     -- foghorn.artifacts.catalog_revision at commit
    updated_at TIMESTAMP NOT NULL DEFAULT NOW(),
    -- Durable consumer ack: Commodore sets consumed_at (AckArtifactCreationCommand) once it
    -- has read this terminal (committed/rejected) outcome and terminalized its intent. The
    -- retention GC deletes a terminal row ONLY when consumed_at IS NOT NULL; an UNCONSUMED
    -- terminal outcome is NEVER time-deleted, because erasing a committed outcome Commodore
    -- has not yet read would make it read as MISSING and trip the bounded abort against a
    -- live artifact. The invariant is retention, not convergence: an unconsumed terminal row
    -- is retained until a durable ack consumes it. That ack is not guaranteed to arrive —
    -- Commodore may be unable to reach Foghorn, and a persistently-failing ack (e.g. an
    -- identity mismatch) backs off indefinitely without converging — so a stuck obligation is
    -- surfaced via the unconsumed-backlog WARN (CreationCommandExpiryJob), not silently
    -- cleared; clearing it needs operational intervention. NULL while 'accepted' or while the
    -- terminal outcome is unconsumed.
    consumed_at TIMESTAMP,
    CONSTRAINT artifact_creation_commands_kind_check CHECK (kind IN ('clip', 'dvr', 'vod')),
    CONSTRAINT artifact_creation_commands_status_check CHECK (status IN ('accepted', 'committed', 'rejected'))
);

-- Expiry-worker scan index: 'accepted' rows oldest-first by updated_at. Partial so the
-- committed/rejected terminal rows (kept for the status read + retained until the
-- retention horizon) never widen the bounded scan the CreationCommandExpiryJob runs
-- each minute.
CREATE INDEX IF NOT EXISTS idx_foghorn_creation_commands_accepted
    ON foghorn.artifact_creation_commands(updated_at)
    WHERE status = 'accepted';

-- Retention-GC scan index: consumed terminal ('committed'/'rejected') rows oldest-first by
-- consumed_at. Partial on the exact GC predicate (terminal AND consumed_at IS NOT NULL) so
-- the bounded delete pass — which orders and range-filters on consumed_at, the retention
-- anchor — never scans the unconsumed rows it must retain, nor degrades with total
-- historical volume. The window starts at consumption, not the terminal transition, so a
-- row terminalized long ago but only just consumed survives a full horizon past its ack.
CREATE INDEX IF NOT EXISTS idx_foghorn_creation_commands_terminal_gc
    ON foghorn.artifact_creation_commands(consumed_at)
    WHERE status IN ('committed', 'rejected') AND consumed_at IS NOT NULL;

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
    SELECT 'v0.2.96' WHERE NOT EXISTS (SELECT 1 FROM public._schema_baseline);

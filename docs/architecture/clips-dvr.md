# Clip/DVR Registry Architecture

This document describes the architecture for clips and DVR recordings in the FrameWorks platform.

## Architecture Overview

```
┌─────────────┐     ┌─────────────┐     ┌─────────────┐     ┌─────────────┐
│  Commodore  │     │  Periscope  │     │  Signalman  │     │   Foghorn   │
│  (Control)  │     │ (Analytics) │     │ (Real-time) │     │   (Media)   │
├─────────────┤     ├─────────────┤     ├─────────────┤     ├─────────────┤
│ Business    │     │ Lifecycle   │     │ Live Kafka  │     │ Artifact    │
│ Registry    │     │ State       │     │ Events      │     │ Operations  │
│ - ownership │     │ - status    │     │ - progress  │     │ - storage   │
│ - titles    │     │ - size      │     │ - stage     │     │ - S3 sync   │
│ - stream    │     │ - file path │     │ - errors    │     │ - routing   │
│ - retention │     │ - s3_url    │     │             │     │             │
│             │     │            │     │             │     │             │
└─────────────┘     └─────────────┘     └─────────────┘     └─────────────┘
       │                   │                   │                   │
       └───────────┬───────┴───────────────────┴───────────────────┘
                   │
            ┌──────▼──────┐
            │   Gateway   │
            │  (GraphQL)  │
            │ Field-level │
            │  Resolvers  │
            └─────────────┘
                   │
            ┌──────▼──────┐
            │  Frontend   │
            │  1 Query    │
            └─────────────┘
```

## Service Responsibilities

| Service       | Role          | Data                                                     | Query Pattern                                                           |
| ------------- | ------------- | -------------------------------------------------------- | ----------------------------------------------------------------------- |
| **Commodore** | Control Plane | Business registry (ownership, titles, stream, retention) | GraphQL queries for clip/DVR listings                                   |
| **Periscope** | Analytics     | Lifecycle state (stage, size, file path, s3_url)         | GraphQL lifecycle field resolvers (ArtifactLifecycleLoader) batch-fetch |
| **Signalman** | Real-time     | Live Kafka events                                        | GraphQL subscriptions                                                   |
| **Foghorn**   | Media Plane   | Artifact operations (storage, S3 sync, routing)          | Internal gRPC for mutations                                             |

## GraphQL Data Flow

### Queries (Initial Load)

Frontend executes a single GraphQL query:

```graphql
query GetClips($streamId: ID, $first: Int, $after: String) {
  clipsConnection(streamId: $streamId, page: { first: $first, after: $after }) {
    edges {
      node {
        # Business metadata → Commodore
        id
        clipHash
        playbackId
        streamId
        title
        createdAt
        expiresAt

        # Lifecycle state → Periscope (field resolvers via ArtifactLifecycleLoader)
        status
        sizeBytes
        storageLocation
        isFrozen
      }
    }
  }
}
```

Gateway handles this as:

1. **Parent resolver** calls Commodore `GetClips` → returns business metadata
2. **Lifecycle field resolvers** call `ArtifactLifecycleLoader`, which batch-calls Periscope `GetArtifactStates(request_ids: [artifact hashes])`
3. Gateway merges results and returns unified response

### Subscriptions (Live Updates)

```graphql
subscription {
  liveClipLifecycle(streamId: "stream-id-here") {
    clipHash
    stage
    progressPercent
    sizeBytes
    error
  }
}
```

Gateway → Signalman → Kafka events → Frontend

### Mutations (Create/Delete)

Mutations still route through Commodore → Foghorn:

```
Frontend → Gateway → Commodore.CreateClip → Foghorn.CreateClip
```

## Key Design Decisions

### Why Field Resolvers?

- **Single query**: Frontend doesn't need to know about service boundaries
- **Efficient**: Gateway batches all lifecycle lookups into one Periscope call
- **Flexible**: Frontend selects only the fields it needs
- **Transparent**: Adding new data sources doesn't change frontend queries

### Why Separate Commodore and Periscope?

- **Commodore** = PostgreSQL (transactional, authoritative for business data)
- **Periscope** = ClickHouse (optimized for time-series, analytics, lifecycle events)
- Different query patterns, different scaling characteristics

### Why Foghorn Still Exists?

Foghorn handles **operations**, not queries:

- Node orchestration (which node stores what)
- S3 freeze/defrost coordination
- Helmsman communication
- Playback URL resolution

Gateway queries go to Commodore + Periscope, not Foghorn.

## State Classification

| State Type              | Description                                                        | Owner     | Storage                             | Query Source            |
| ----------------------- | ------------------------------------------------------------------ | --------- | ----------------------------------- | ----------------------- |
| **Business Registry**   | tenant_id, user_id, stream_id, title, description, retention       | Commodore | PostgreSQL                          | GraphQL parent resolver |
| **Lifecycle State**     | stage, size_bytes, file_path, s3_url, manifest_path, error_message | Periscope | ClickHouse `artifact_state_current` | GraphQL field resolvers |
| **Artifact Operations** | storage_location, node assignments, sync status                    | Foghorn   | PostgreSQL                          | Internal gRPC only      |
| **Real-time Events**    | Live progress updates, stage changes                               | Signalman | Kafka passthrough                   | GraphQL subscriptions   |

Note: `storageLocation`/`isFrozen` are derived in GraphQL from Periscope `s3_url` (not stored directly in ClickHouse).

### Data Flow During Processing

```
Helmsman (progress update)
    │
    ▼
Foghorn (updates foghorn.artifacts)
    │
    ▼
Decklog (Kafka event with tenant_id, user_id, internal_name)
    │
    ├──▶ Periscope Ingest (writes to ClickHouse artifact_state_current)
    │
    └──▶ Signalman (broadcasts to WebSocket subscribers)
```

### Foghorn Context Storage

Foghorn stores denormalized context for event emission fallbacks:

```sql
foghorn.artifacts (
  artifact_hash VARCHAR(32) PRIMARY KEY,
  internal_name VARCHAR(255),  -- Stream identifier
  artifact_internal_name VARCHAR(64), -- Artifact routing name (vod+<name>)
  tenant_id UUID,              -- Denormalized fallback
  user_id UUID,                -- Denormalized fallback
  ...
)
```

Foghorn prefers Commodore for canonical context and falls back to these fields when Commodore is unavailable.

## Database Schema

### Commodore (Control Plane) - Business Registry

```sql
-- Clip business registry
commodore.clips (
  id UUID PRIMARY KEY,
  tenant_id UUID NOT NULL,
  user_id UUID NOT NULL,
  stream_id UUID NOT NULL,
  clip_hash VARCHAR(32) UNIQUE NOT NULL,  -- Generated by Commodore
  artifact_internal_name VARCHAR(64) UNIQUE NOT NULL, -- Artifact routing name
  playback_id CITEXT UNIQUE NOT NULL,     -- Public playback ID
  title VARCHAR(255),
  description TEXT,
  start_time BIGINT NOT NULL,             -- Unix timestamp (ms)
  duration BIGINT NOT NULL,               -- Duration (ms)
  clip_mode VARCHAR(20),                  -- absolute, relative, duration, clip_now
  requested_params JSONB,                 -- Original request for audit
  retention_until TIMESTAMP,
  created_at, updated_at
)

-- DVR recording business registry
commodore.dvr_recordings (
  id UUID PRIMARY KEY,
  tenant_id UUID NOT NULL,
  user_id UUID NOT NULL,
  stream_id UUID,                         -- Optional for legacy DVRs
  dvr_hash VARCHAR(32) UNIQUE NOT NULL,   -- Generated by Commodore
  artifact_internal_name VARCHAR(64) UNIQUE NOT NULL, -- Artifact routing name
  playback_id CITEXT UNIQUE NOT NULL,     -- Public playback ID
  internal_name VARCHAR(255) NOT NULL,    -- MistServer stream name
  retention_until TIMESTAMP,
  created_at, updated_at
)
```

### Foghorn (Media Plane) - Unified Artifact Model

```sql
-- Unified artifact lifecycle table (cold storage = S3 is authoritative)
foghorn.artifacts (
  artifact_hash VARCHAR(32) PRIMARY KEY,
  artifact_type VARCHAR(10) NOT NULL,     -- 'clip', 'dvr', 'upload'

  -- Denormalized fields (authoritative source: Commodore)
  internal_name VARCHAR(255),             -- Stream identifier for routing
  artifact_internal_name VARCHAR(64),     -- Artifact routing name (vod+<name>)
  tenant_id UUID,                         -- Fallback when Commodore unavailable
  user_id UUID,                           -- Fallback for event emission

  -- Lifecycle state
  status VARCHAR(50) DEFAULT 'requested', -- requested, processing, ready, failed, deleted
  error_message TEXT,
  request_id UUID,                        -- Original request tracking

  -- Storage metrics
  size_bytes BIGINT,
  manifest_path VARCHAR(500),             -- HLS/DASH manifest
  format VARCHAR(20),                     -- mp4, m3u8, etc.

  -- Cold storage (S3 = authoritative)
  storage_location VARCHAR(20),           -- pending, local, freezing, s3, defrosting
  s3_url VARCHAR(500),
  sync_status VARCHAR(20),                -- pending, in_progress, synced, failed
  sync_error TEXT,
  last_sync_attempt TIMESTAMP,
  frozen_at TIMESTAMP,
  dtsh_synced BOOLEAN DEFAULT FALSE,

  -- DVR-specific timing
  started_at TIMESTAMP,
  ended_at TIMESTAMP,
  duration_seconds INTEGER,

  -- Access tracking
  access_count INTEGER DEFAULT 0,
  last_accessed_at TIMESTAMP,

  -- Retention
  retention_until TIMESTAMP,              -- When artifact should be soft-deleted

  created_at, updated_at
)

-- Warm storage distribution (node caches)
foghorn.artifact_nodes (
  artifact_hash VARCHAR(32) REFERENCES foghorn.artifacts ON DELETE CASCADE,
  node_id VARCHAR(100) NOT NULL,

  -- Node-specific storage
  file_path VARCHAR(500),
  base_url VARCHAR(500),                  -- Node base URL for routing
  size_bytes BIGINT,

  -- DVR segment tracking (per-node)
  segment_count INT DEFAULT 0,
  segment_bytes BIGINT DEFAULT 0,

  -- Health tracking
  access_count BIGINT DEFAULT 0,
  last_accessed TIMESTAMP,
  last_seen_at TIMESTAMP DEFAULT NOW(),
  is_orphaned BOOLEAN DEFAULT false,
  cached_at TIMESTAMP,                    -- For warm duration tracking

  PRIMARY KEY (artifact_hash, node_id)
)
```

### Storage Model

- **`artifacts`** = cold storage state (S3 is authoritative, 1 row per artifact)
- **`artifact_nodes`** = warm storage cache (which nodes have copies, N rows per artifact)

## gRPC API

### Commodore InternalService

```protobuf
// Register a new clip in the business registry
rpc RegisterClip(RegisterClipRequest) returns (RegisterClipResponse);

// Register a new DVR recording in the business registry
rpc RegisterDVR(RegisterDVRRequest) returns (RegisterDVRResponse);

// Resolve clip hash to tenant context (for analytics enrichment and playback)
rpc ResolveClipHash(ResolveClipHashRequest) returns (ResolveClipHashResponse);

// Resolve DVR hash to tenant context (for analytics enrichment and playback)
rpc ResolveDVRHash(ResolveDVRHashRequest) returns (ResolveDVRHashResponse);

// Resolve artifact playback ID to artifact identity (clip/dvr/vod)
rpc ResolveArtifactPlaybackID(ResolveArtifactPlaybackIDRequest) returns (ResolveArtifactPlaybackIDResponse);

// Resolve artifact internal routing name to artifact identity (clip/dvr/vod)
rpc ResolveArtifactInternalName(ResolveArtifactInternalNameRequest) returns (ResolveArtifactInternalNameResponse);
```

## Data Flows

### CreateClip Flow

```
Gateway -> Commodore.CreateClip(tenant_id, stream_id, timing)
  |
  +-- 1. Validate tenant owns stream
  +-- 2. Generate clip_hash
  +-- 3. INSERT into commodore.clips
  |
  +-> Foghorn.CreateClip(clip_hash, timing, internal_name)
        +-- 4. INSERT into foghorn.artifacts (status='requested')
        +-- 5. Select storage node
        +-- 6. Send ClipPullRequest to Helmsman
```

### ResolveViewerEndpoint Flow

```
Gateway -> Commodore (proxy) -> Foghorn.ResolveViewerEndpoint
  |
  +-- LIVE: In-memory state lookup
  |
  +-- CLIP:
  |     +-- 1. Query foghorn.artifacts for storage info
  |     +-- 2. Query foghorn.artifact_nodes for available nodes
  |     +-- 3. Return playback URL
  |
  +-- DVR: Same pattern as CLIP
```

### Analytics Enrichment Flow

```
Helmsman -> Foghorn (ClipLifecycle event with request_id + clip_hash)
  |
  +-- 1. Lookup clip context by request_id in foghorn.artifacts
  +-- 2. Resolve canonical context via Commodore.ResolveClipHash(clip_hash)
  +-- 3. If Commodore unavailable, fall back to denormalized tenant_id/user_id
  +-- 4. Emit enriched event to Decklog with tenant_id/user_id/internal_name
```

### Freeze/Defrost Flow (S3 Sync)

```
FREEZE (warm -> cold):
  1. Foghorn sends FreezeRequest(artifact_hash) to Helmsman
  2. Helmsman uploads to S3, returns s3_url
  3. Foghorn updates: artifacts.storage_location='s3', artifacts.s3_url=url
  4. Foghorn updates: artifact_nodes SET is_orphaned=true (local copy stale)

DEFROST (cold -> warm):
  1. Foghorn sends DefrostRequest(artifact_hash, target_node_id) to Helmsman
  2. Helmsman downloads from S3 to local storage
  3. Helmsman reports artifact via NodeLifecycleUpdate
  4. Foghorn upserts: artifact_nodes (artifact_hash, node_id)
  5. Foghorn updates: artifacts.storage_location='local'
```

## Service Responsibilities

| Service       | Responsibilities                                                                                                            |
| ------------- | --------------------------------------------------------------------------------------------------------------------------- |
| **Gateway**   | GraphQL orchestrator. Field-level resolvers merge Commodore + Periscope data.                                               |
| **Commodore** | Business registry (clips, dvr_recordings tables). Hash generation. GraphQL parent resolvers.                                |
| **Periscope** | Lifecycle state queries (artifact_state_current ClickHouse table). Batch lookup by request_ids via ArtifactLifecycleLoader. |
| **Signalman** | Real-time WebSocket hub. Passes Kafka events to subscriptions.                                                              |
| **Foghorn**   | Artifact operations (artifacts, artifact_nodes tables). Node orchestration. S3 sync. NOT queried for listings.              |
| **Helmsman**  | Local storage operations. Freeze/defrost execution. Artifact reporting.                                                     |
| **Decklog**   | Event ingestion. Events keyed by artifact_hash with tenant_id, user_id, internal_name.                                      |

## Service Events Audit (service_events)

- **Commodore** emits `artifact_registered` ServiceEvents when clip/DVR/VOD registry records are created.
- **Foghorn** emits clip/DVR/VOD lifecycle analytics events (MistTrigger) **and** an `artifact_lifecycle` ServiceEvent (via Decklog client) for audit visibility.
- ServiceEvents are metadata-only; lifecycle analytics flow through Periscope.

## Resilience Considerations

### Commodore Unavailable During Playback

- Artifact playback ID resolution depends on Commodore (ResolveArtifactPlaybackID/ResolveClipHash/ResolveDVRHash).
- If Commodore is unavailable, playback resolution returns an error (no cache fallback).

### Commodore Unavailable During Creation

- Commodore registers the business record first, then calls Foghorn.
- If Foghorn fails, the Commodore record remains; client receives an error and can retry.

### Data Consistency

The system uses an **eventual consistency** pattern between Commodore and Foghorn:

1. **Commodore registers first** - Business registry (tenant, user, title) is written first
2. **Foghorn creates artifact** - Lifecycle state created with provided hash
3. **If Foghorn fails** - Commodore record persists for audit/billing purposes
4. **Cleanup** - Foghorn retention jobs only apply to artifacts that exist; Commodore registry cleanup is still manual/TODO

This pattern is acceptable because:

- Historical Commodore records are useful for billing and audit
- Failed artifacts are marked `failed` and retained for troubleshooting
- Retention jobs ensure storage cleanup for artifacts that exist

### Tenant Context Fallback

When Commodore is unavailable during analytics enrichment:

1. Foghorn stores `tenant_id`/`user_id` denormalized in `foghorn.artifacts`
2. Analytics handlers first attempt `ResolveClipHash`/`ResolveDVRHash` to get tenant context
3. If Commodore unavailable, fall back to local `tenant_id`/`user_id` from artifacts table
4. Events emitted with tenant context even when Commodore is down (user_id is best-effort)

```go
// In handlers.go - getClipContextByRequestID
if resp.Found {
    return resp.TenantId, resp.InternalName
}
// Fallback to denormalized tenant_id if Commodore unavailable
if fallbackTenantID.Valid {
    return fallbackTenantID.String, internalName
}
```

## Retention and Cleanup Jobs

Three background jobs manage artifact lifecycle:

| Job                | Interval | Action                                                 |
| ------------------ | -------- | ------------------------------------------------------ |
| `RetentionJob`     | 1 hour   | Soft-delete expired artifacts (status='deleted')       |
| `OrphanCleanupJob` | 5 min    | Send delete requests to Helmsman for deleted artifacts |
| `PurgeDeletedJob`  | 24 hours | Hard-delete from DB + S3 (when no active node copies)  |

### RetentionJob

Uses `retention_until` field (set to 30 days from creation by default):

```sql
UPDATE foghorn.artifacts
SET status = 'deleted', updated_at = NOW()
WHERE status NOT IN ('deleted', 'failed')
  AND (
    (retention_until IS NOT NULL AND retention_until < NOW())
    OR
    (retention_until IS NULL AND created_at < NOW() - make_interval(days => 30))
  )
```

### OrphanCleanupJob

Detects deleted artifacts with local node copies and sends cleanup requests:

```sql
SELECT a.artifact_hash, n.node_id, n.file_path
FROM foghorn.artifacts a
JOIN foghorn.artifact_nodes n ON a.artifact_hash = n.artifact_hash
WHERE a.status = 'deleted' AND NOT n.is_orphaned
```

### PurgeDeletedJob

Final cleanup after local files are confirmed deleted:

```sql
DELETE FROM foghorn.artifacts
WHERE status = 'deleted'
  AND NOT EXISTS (
    SELECT 1 FROM foghorn.artifact_nodes
    WHERE artifact_hash = foghorn.artifacts.artifact_hash
    AND NOT is_orphaned
  )
  AND updated_at < NOW() - INTERVAL '30 days'
```

## Error Handling Patterns

### Helmsman Command Failures

When SendClipPull or SendDVRStart fails:

1. Mark artifact as `status='failed'` with `error_message`
2. Emit FAILED lifecycle event to Decklog
3. Return error to user with clear message

This provides immediate user feedback rather than leaving artifacts in 'requested' state indefinitely.

### Analytics Event Failures

When Decklog send fails:

1. Log error with full context (clip_hash/dvr_hash, tenant_id, stage)
2. Continue processing (don't block clip/DVR creation)
3. Accept potential analytics data loss as trade-off for reliability

## VOD Uploads (Implemented)

The unified artifact model also covers VOD uploads (direct file uploads, not derived from live streams):

```sql
artifact_type = 'upload'
```

**Flow (current):**

1. `createVodUpload` → Gateway → Commodore registers in `commodore.vod_assets` and calls Foghorn to create an S3 multipart upload.
2. Client uploads parts to S3 using presigned URLs.
3. `completeVodUpload` → Gateway → Commodore → Foghorn finalizes upload and updates `foghorn.artifacts` (`artifact_type='upload'`).
4. Same freeze/defrost/distribution model applies.

## Critical Files

### Schema & Proto

- `pkg/proto/commodore.proto` - Clip/DVR registry RPCs
- `pkg/proto/shared.proto` - ClipInfo, DVRInfo, CreateClip/DVR requests (includes user_id)
- `pkg/proto/periscope.proto` - GetArtifactStates with request_ids batch lookup
- `pkg/database/sql/schema/commodore.sql` - clips, dvr_recordings tables
- `pkg/database/sql/schema/foghorn.sql` - artifacts (with user_id), artifact_nodes tables
- `pkg/database/sql/clickhouse/periscope.sql` - artifact_state_current, artifact_events, storage_events tables

### Gateway (api_gateway) - GraphQL Orchestration

- `api_gateway/gqlgen.yml` - Field resolver configuration for lifecycle fields
- `api_gateway/graph/schema.resolvers.go` - Parent resolvers (Commodore) + field resolvers (Periscope)
- `api_gateway/internal/loaders/artifact_lifecycle.go` - Batch loader for Periscope lifecycle data
- `api_gateway/internal/resolvers/streams.go` - GetClipsConnection, GetDVRRecordingsConnection

### Commodore (api_control) - Business Registry

- `api_control/internal/grpc/server.go` - GetClips, ListDVRRequests (query own tables, NOT Foghorn)

### Periscope (api_analytics_query) - Lifecycle State

- `api_analytics_query/internal/grpc/server.go` - GetArtifactStates with request_ids filter

### Foghorn (api_balancing) - Artifact Operations

- `api_balancing/internal/grpc/server.go` - CreateClip, StartDVR (stores user_id)
- `api_balancing/internal/handlers/handlers.go` - Clip/DVR lifecycle event handlers
- `api_balancing/internal/control/server.go` - SendClipPull, SendDVRStart, Helmsman communication
- `api_balancing/internal/jobs/` - Retention, orphan cleanup, purge jobs

### Signalman (api_realtime) - Real-time Events

- `api_realtime/internal/grpc/server.go` - WebSocket subscriptions for liveClipLifecycle, liveDvrLifecycle

### Analytics Ingest (api_analytics_ingest)

- `api_analytics_ingest/internal/handlers/handlers.go` - processClipLifecycle, processDVRLifecycle → ClickHouse

### Frontend (website_application)

- `pkg/graphql/operations/queries/GetClipsConnection.gql` - Clip queries with lifecycle fields
- `pkg/graphql/operations/queries/GetDVRRequests.gql` - DVR queries with lifecycle fields
- `pkg/graphql/operations/subscriptions/ClipLifecycle.gql` - Real-time updates
- `pkg/graphql/operations/subscriptions/DvrLifecycle.gql` - Real-time updates

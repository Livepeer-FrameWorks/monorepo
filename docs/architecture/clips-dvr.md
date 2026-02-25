# Clip/DVR Registry Architecture

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
        isFrozen # Boolean (nullable — null when lifecycle state unavailable)
        isExpired # Boolean! (derived from retention_until)
      }
    }
  }
}
```

Gateway handles this as:

1. **Parent resolver** calls Commodore `GetClips` → returns business metadata
2. **Parent resolver** prefetches lifecycle data via `ArtifactLifecycleLoader.LoadMany`, which batch-calls Periscope `GetArtifactStates(request_ids: [clip_hash/dvr_hash])`
3. **Lifecycle field resolvers** read from the request-scoped loader cache (`Load`) to avoid duplicate lookups
4. Gateway merges results and returns unified response

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

## State Classification

| State Type              | Description                                                        | Owner     | Storage                             | Query Source            |
| ----------------------- | ------------------------------------------------------------------ | --------- | ----------------------------------- | ----------------------- |
| **Business Registry**   | tenant_id, user_id, stream_id, title, description, retention       | Commodore | PostgreSQL                          | GraphQL parent resolver |
| **Lifecycle State**     | stage, size_bytes, file_path, s3_url, manifest_path, error_message | Periscope | ClickHouse `artifact_state_current` | GraphQL field resolvers |
| **Artifact Operations** | storage_location, node assignments, sync status                    | Foghorn   | PostgreSQL                          | Internal gRPC only      |
| **Real-time Events**    | Live progress updates, stage changes                               | Signalman | Kafka passthrough                   | GraphQL subscriptions   |

Note: `storageLocation`/`isFrozen` are derived in GraphQL from Periscope `s3_url` (not stored directly in ClickHouse). `isFrozen` is nullable (returns null when lifecycle state is unavailable). `isExpired` is derived from `retention_until`.

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
  tenant_id UUID NOT NULL,     -- Required; denormalized for routing
  user_id UUID,                -- Denormalized fallback
  ...
)
```

Foghorn prefers Commodore for canonical context and falls back to these fields when Commodore is unavailable.

## Database Schema

Schemas: `pkg/database/sql/schema` (clips, dvr_recordings), `pkg/database/sql/schema` (artifacts, artifact_nodes), `pkg/database/sql/clickhouse` (artifact_state_current, artifact_events).

**Storage model**: `artifacts` = cold storage state (S3 is authoritative, 1 row per artifact). `artifact_nodes` = warm storage cache (which nodes have copies, N rows per artifact).

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

## Service Events Audit (service_events)

- **Commodore** emits `artifact_registered` ServiceEvents when clip/DVR/VOD registry records are created.
- **Foghorn** emits clip/DVR/VOD lifecycle analytics events (MistTrigger) **and** an `artifact_lifecycle` ServiceEvent (via Decklog client) for audit visibility.
- ServiceEvents are metadata-only; lifecycle analytics flow through Periscope.

## Cross-Cluster Artifact Access

When a viewer requests an artifact (clip/DVR/VOD) that lives on a remote cluster, Foghorn uses the `PrepareArtifact` FoghornFederation RPC to obtain time-limited presigned S3 URLs without sharing S3 credentials across clusters.

### Flow

```
Viewer → Foghorn A (artifact not on local nodes, not in local S3)
  → ArtifactAdvertisement from PeerChannel: Cluster B has the artifact
  → PrepareArtifact RPC → Foghorn B
      1. Lookup foghorn.artifacts by hash + tenant_id
      2. If not yet in S3: trigger async freeze, return Ready=false + est_ready_seconds
      3. If in S3: generate presigned GET URL(s) (15-min expiry for clips/VOD, 30-min for DVR segments)
      4. Return PrepareArtifactResponse with URL(s), size, format
  → Foghorn A returns presigned URL to viewer via STREAM_SOURCE trigger chain
```

### PrepareArtifact Request/Response

```protobuf
message PrepareArtifactRequest {
  string artifact_id = 1;        // Artifact hash
  string clip_hash = 2;          // Legacy alias
  string requesting_cluster = 3;
  string artifact_type = 4;      // "clip", "dvr", "vod"
  string tenant_id = 5;
}

message PrepareArtifactResponse {
  string url = 1;                     // Presigned S3 GET URL (clip/vod single file)
  uint64 size_bytes = 2;
  bool ready = 3;                     // Immediately available?
  uint32 est_ready_seconds = 4;       // Async prep time estimate
  string error = 5;
  map<string, string> segment_urls = 6; // DVR: segment filename → presigned GET URL
  string format = 7;                  // mp4, m3u8, etc.
  string internal_name = 8;           // Stream internal name for routing
}
```

Key design choice: artifacts must be S3-synced before cross-cluster access works. If an artifact is only on local disk (not yet frozen to S3), `PrepareArtifact` triggers an async freeze and returns `ready=false`. The requesting Foghorn can retry after `est_ready_seconds`.

See `docs/architecture/federation.md` for the broader FoghornFederation protocol and `docs/architecture/stream-replication-topology.md` for how STREAM_SOURCE routes to PrepareArtifact for VOD/artifacts.

### Cross-Cluster Artifact Command Routing

Artifact **read** operations (playback) use `PrepareArtifact` (described above).
Artifact **write** operations (delete, stop) use a hybrid push+forward model:

**Push (Commodore → correct Foghorn):**

- `origin_cluster_id` in `commodore.clips` / `commodore.dvr_recordings` / `commodore.vod_assets`
  determines which cluster's Foghorn receives the command.
- Commodore resolves Foghorn address from `GetClusterRouting` peer list
  (`foghorn_grpc_addr` field on `TenantClusterPeer`).

**Forward (Foghorn → federation peer):**

- If a Foghorn receives a delete/stop for an artifact not in `foghorn.artifacts`,
  it forwards via `ForwardArtifactCommand` to federation peers.
- This handles stale `origin_cluster_id` (race between artifact migration and command).

Related source files:

- Command routing: `api_control/internal/grpc` (`resolveFoghornForArtifact`)
- Forward handler: `api_balancing/internal/federation` (`ForwardArtifactCommand`)
- Forward trigger: `api_balancing/internal/grpc` (`forwardArtifactToFederation`)

### Related Source Files

- Federation server handler: `api_balancing/internal/federation` (`PrepareArtifact`)
- Proto definitions: `pkg/proto`
- STREAM_SOURCE → artifact resolution: `api_balancing/internal/triggers` (`handleStreamSource`)
- Artifact advertisement: `api_balancing/internal/federation` (`ArtifactAdvertisement`)

## Resilience

- **Playback when Commodore is down**: No cache fallback — playback resolution returns an error.
- **Creation when Foghorn fails**: Commodore record remains (useful for billing/audit); client can retry.
- **Tenant context fallback**: Foghorn stores denormalized `tenant_id`/`user_id` in `foghorn.artifacts`. Analytics handlers try Commodore first, fall back to local fields if unavailable.

## Local Storage Management

### Edge Node Disk Pressure Eviction

Helmsman nodes manage local storage pressure independently to avoid disk exhaustion:

| Parameter           | Default | Description                                                   |
| ------------------- | ------- | ------------------------------------------------------------- |
| `cleanupThreshold`  | 90%     | Start eviction when disk usage exceeds this                   |
| `targetThreshold`   | 80%     | Evict until disk usage falls below this                       |
| `MinFreeBytes`      | 1 GiB   | Eviction also triggers when free space falls below this floor |
| `minRetentionHours` | 1 hour  | Never evict artifacts younger than this                       |

> **Important:** Local storage retention is **best-effort**. Under disk pressure,
> artifacts may be evicted from edge nodes before the 30-day retention window.
> This does not change S3-backed copies or central database records.

## Retention and Cleanup Jobs

Three background jobs manage artifact lifecycle:

| Job                | Interval | Action                                                 |
| ------------------ | -------- | ------------------------------------------------------ |
| `RetentionJob`     | 1 hour   | Soft-delete expired artifacts (status='deleted')       |
| `OrphanCleanupJob` | 5 min    | Send delete requests to Helmsman for deleted artifacts |
| `PurgeDeletedJob`  | 24 hours | Hard-delete from DB + S3 (when no active node copies)  |

### RetentionJob

Uses `retention_until` field (set to 30 days from creation by default). This
controls when artifacts are soft-deleted in the registry, but does not prevent
early eviction on edge nodes under disk pressure (see above):

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

Final cleanup after local files are confirmed deleted. Runs every 24 hours.

**Database cleanup** (always performed):

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

**S3 cleanup** (conditional):

- Requires S3 client configured in Foghorn
- Requires tenant context resolvable via Commodore

If either condition is not met, S3 cleanup is skipped and only the database
records are deleted.

## VOD Uploads

The unified artifact model also covers VOD uploads (direct file uploads, not derived from live streams):

```sql
artifact_type = 'vod'
```

**Flow (current):**

1. `createVodUpload` → Gateway → Commodore registers in `commodore.vod_assets` and calls Foghorn to create an S3 multipart upload.
2. Client uploads parts to S3 using presigned URLs.
3. `completeVodUpload` → Gateway → Commodore → Foghorn finalizes upload and updates `foghorn.artifacts` (`artifact_type='vod'`).
4. Same freeze/defrost/distribution model applies.

## Critical Files

### Schema & Proto

- `pkg/proto` - Clip/DVR registry RPCs
- `pkg/proto` - ClipInfo, DVRInfo, CreateClip/DVR requests (includes user_id)
- `pkg/proto` - GetArtifactStates with request_ids batch lookup
- `pkg/database/sql/schema` - clips, dvr_recordings tables
- `pkg/database/sql/schema` - artifacts (with user_id), artifact_nodes tables
- `pkg/database/sql/clickhouse` - artifact_state_current, artifact_events, storage_events tables

### Gateway (api_gateway) - GraphQL Orchestration

- `api_gateway` - Field resolver configuration for lifecycle fields
- `api_gateway/graph` - Parent resolvers (Commodore) + field resolvers (Periscope)
- `api_gateway/internal/loaders` - Batch loader for Periscope lifecycle data
- `api_gateway/internal/resolvers` - GetClipsConnection, GetDVRRecordingsConnection

### Commodore (api_control) - Business Registry

- `api_control/internal/grpc` - GetClips, ListDVRRequests (query own tables, NOT Foghorn)

### Periscope (api_analytics_query) - Lifecycle State

- `api_analytics_query/internal/grpc` - GetArtifactStates with request_ids filter

### Foghorn (api_balancing) - Artifact Operations

- `api_balancing/internal/grpc` - CreateClip, StartDVR (stores user_id)
- `api_balancing/internal/handlers` - Clip/DVR lifecycle event handlers
- `api_balancing/internal/control` - SendClipPull, SendDVRStart, Helmsman communication
- `api_balancing/internal/jobs/` - Retention, orphan cleanup, purge jobs

### Signalman (api_realtime) - Real-time Events

- `api_realtime/internal/grpc` - WebSocket subscriptions for liveClipLifecycle, liveDvrLifecycle

### Analytics Ingest (api_analytics_ingest)

- `api_analytics_ingest/internal/handlers` - processClipLifecycle, processDVRLifecycle → ClickHouse

### Frontend (website_application)

- `pkg/graphql/operations/queries/GetClipsConnection.gql` - Clip queries with lifecycle fields
- `pkg/graphql/operations/queries/GetDVRRequests.gql` - DVR queries with lifecycle fields
- `pkg/graphql/operations/subscriptions/ClipLifecycle.gql` - Real-time updates
- `pkg/graphql/operations/subscriptions/DvrLifecycle.gql` - Real-time updates

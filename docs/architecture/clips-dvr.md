# Clip/DVR Registry Architecture

## Architecture Overview

```
┌─────────────┐     ┌─────────────┐     ┌─────────────┐     ┌─────────────┐
│  Commodore  │     │  Periscope  │     │  Signalman  │     │   Foghorn   │
│  (Control)  │     │ (Analytics) │     │ (Real-time) │     │   (Media)   │
├─────────────┤     ├─────────────┤     ├─────────────┤     ├─────────────┤
│ Business +  │     │ Transient   │     │ Live Kafka  │     │ Artifact    │
│ Catalog     │     │ Overlay     │     │ Events      │     │ Operations  │
│ - ownership │     │ - hasLocal  │     │ - progress  │     │ - storage   │
│ - titles    │     │   Copy      │     │ - stage     │     │ - S3 sync   │
│ - stream    │     │ - live      │     │ - errors    │     │ - routing   │
│ - retention │     │   progress  │     │             │     │             │
│ - sync/s3   │     │             │     │             │     │             │
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

| Service       | Role          | Data                                                                                                                                | Query Pattern                                                     |
| ------------- | ------------- | ----------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------- |
| **Commodore** | Control Plane | Business registry (ownership, titles, stream, retention) + durable lifecycle catalog (sync/finalize/freeze, size, duration, tracks) | `ListStorageArtifacts` — one unified catalog query for every kind |
| **Periscope** | Analytics     | Transient overlay only (`hasLocalCopy` — present full local node copy, live progress)                                               | Gap-fill on top of the durable catalog                            |
| **Signalman** | Real-time     | Live Kafka events                                                                                                                   | GraphQL subscriptions                                             |
| **Foghorn**   | Media Plane   | Artifact operations (storage, S3 sync, routing)                                                                                     | Internal gRPC for mutations                                       |

## GraphQL Data Flow

### Queries (Initial Load)

Frontend executes a single GraphQL query:

```graphql
query GetClips($streamId: ID, $first: Int, $offset: Int) {
  storageArtifactsConnection(
    input: { kinds: [CLIP], streamId: $streamId, first: $first, offset: $offset }
  ) {
    nodes {
      # Business metadata → Commodore
      id
      hash
      playbackId
      streamId
      title
      createdAt
      expiresAt

      # Durable lifecycle state → Commodore catalog (projected from Foghorn)
      status
      sizeBytes
      storageLocation
      isSynced # Boolean (nullable — null when lifecycle state unavailable)
      isFinalized # Boolean (nullable — null when lifecycle state unavailable)
      # Observed placement overlay → Periscope (present full local node copy: origin or cache)
      hasLocalCopy # Boolean (nullable — null when placement overlay unavailable)
    }
  }
}
```

Gateway handles this as:

1. **Single resolver** calls Commodore `ListStorageArtifacts` → returns the unified catalog row
   for every kind (clip/DVR/chapter/VOD), carrying both the business metadata AND the durable
   lifecycle facts (`syncStatus`/`isSynced`/`isFinalized`/`storageLocation`/duration/tracks). These
   lifecycle facts are the durable catalog projection written by the Foghorn artifact reconciler
   (`UpdateArtifactCatalogSnapshot`), so they survive a Periscope rebuild.
2. **Transient overlay** — the observed placement signal (`hasLocalCopy` = a full local node copy
   exists on at least one node, origin or cache, plus live progress) is gap-filled from Periscope's
   node-copy placement on top of the durable catalog; it is never sourced from the catalog's
   `frozen_at`, and is **nullable** — null means the placement overlay is unavailable (unknown), NOT
   `false`. The durable fields (`isSynced`/`isFinalized`) are never overwritten by the overlay. The
   former overloaded `isFrozen` field is removed, and so is the duplicate `isHot` field (`hasLocalCopy`
   is now the single canonical placement field): the durable "in S3" fact is `isSynced`, and the
   S3-only / read-through-relay state is **derived** by consumers as `isSynced && hasLocalCopy === false`
   (only claimed when sync is confirmed AND the overlay reports no local node copy; never claimed while
   `hasLocalCopy` is null/unknown).
3. Gateway returns the unified response.

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

| State Type                  | Description                                                                    | Owner                                                        | Storage                             | Query Source                           |
| --------------------------- | ------------------------------------------------------------------------------ | ------------------------------------------------------------ | ----------------------------------- | -------------------------------------- |
| **Business Registry**       | tenant_id, user_id, stream_id, title, description, retention                   | Commodore                                                    | PostgreSQL                          | `ListStorageArtifacts` (unified)       |
| **Durable Lifecycle State** | sync_status, is_synced, is_finalized, storage_location, size, duration, tracks | Commodore catalog (projected from Foghorn by the reconciler) | PostgreSQL                          | `ListStorageArtifacts` (same row)      |
| **Artifact Operations**     | node assignments, S3 sync orchestration                                        | Foghorn                                                      | PostgreSQL (`foghorn.artifacts`)    | Internal gRPC only (never read live)   |
| **Transient Overlay**       | `hasLocalCopy` (present full local node copy: origin or cache), live progress  | Periscope                                                    | ClickHouse `artifact_state_current` | Gap-fill on top of the durable catalog |
| **Real-time Events**        | Live progress updates, stage changes                                           | Signalman                                                    | Kafka passthrough                   | GraphQL subscriptions                  |

Note: the durable lifecycle fields (`syncStatus`/`isSynced`/`isFinalized`/`storageLocation`) come from the Commodore catalog row, written by the single-writer reconciler projection from `foghorn.artifacts` — so they survive a Periscope rebuild. `isExpired` is derived from `retention_until`. Only the ephemeral placement overlay — `hasLocalCopy` (present full local node copy on at least one node, origin or cache; nullable = unknown), plus live progress — is sourced from Periscope. There is no `isFrozen` field and no duplicate `isHot` field; `hasLocalCopy` is the single canonical placement field. S3-only / read-through-relay is derived as `isSynced && hasLocalCopy === false`.

### Data Flow During Processing

```
Helmsman (progress update)
    │
    ▼
Foghorn (updates foghorn.artifacts)
    │
    ▼
Decklog (Kafka event with tenant_id, user_id, stream_internal_name)
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
  internal_name VARCHAR(64),           -- Artifact routing name (MistServer stream: vod+{this})
  stream_internal_name VARCHAR(255),   -- Source stream routing name (the live stream this was clipped/recorded from)
  tenant_id UUID NOT NULL,             -- Required; denormalized for routing
  user_id UUID,                        -- Denormalized fallback
  ...
)
```

Foghorn prefers Commodore for canonical context and falls back to these fields when Commodore is unavailable.

## Database Schema

Schemas: `pkg/database/sql/schema` (clips, dvr_recordings), `pkg/database/sql/schema` (artifacts, artifact_nodes), `pkg/database/sql/clickhouse` (artifact_state_current, artifact_events).

**Storage model**: `artifacts` = canonical artifact lifecycle and cold-sync state (1 row per artifact). `artifact_nodes` = warm storage cache (which nodes have local copies, N rows per artifact).

## gRPC API

### Commodore InternalService

```protobuf
// Register a new DVR recording in the business registry (Foghorn → Commodore
// during StartDVR; mints the DVR hash, business row, and creation intent).
// Clips and VOD register their business rows directly in Commodore's CreateClip /
// CreateVodUpload; there is no RegisterClip/RegisterVod RPC.
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
  +-> Foghorn.CreateClip(clip_hash, timing, stream_internal_name)
        +-- 4. INSERT into foghorn.artifacts (status='requested')
        +-- 5. Resolve source kind (live, rolling DVR, or finalized chapter)
        +-- 6. Queue processing job for canonical MKV output
        +-- 7. Processing result finalizes foghorn artifact state
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
  +-- 4. Emit enriched event to Decklog with tenant_id/user_id/stream_internal_name
```

### Freeze Flow (S3 Sync) and Cold-Artifact Playback

```
FREEZE (warm -> cold, S3 upload):
  1. Foghorn claims the freeze attempt, binding the exact canonical object key into
     artifacts.sync_object_key, SERVER-MINTS the attempt id, and mints a presigned PUT for an ATTEMPT-SCOPED
     STAGING key (sync_object_key + ".staging." + attempt_id) — NOT the canonical key; then sends
     FreezeRequest(artifact_hash, attempt_id) to Helmsman. Only a lifecycle-ready (status='ready') artifact is claimable.
  2. Helmsman uploads to that presigned STAGING URL (it never holds a PUT to the canonical object) and echoes
     the attempt id at completion
  3. On completion Foghorn HEAD-verifies the staged object, PUBLISHES it (server-side conditional copy) to a
     FRESH immutable version key (sync_object_key + ".att-" + attempt_id) OUTSIDE the transaction, then the
     guarded transaction atomically FLIPS the authoritative pointer artifacts.active_object_key (and s3_url /
     vod_metadata.s3_key) to it and sets sync_status='synced'. Publishing to a fresh key means the copy never
     overwrites a served object, so a rollback can never expose uncommitted bytes; a superseded previous
     version and the staging object are durably enqueued for cleanup. The node's reported URL is not trusted.
  4. While the reporting node still has a warm copy, artifacts.storage_location remains 'local'
  5. If a remote-origin warm copy is evicted later, Foghorn removes that node cache and may mark the artifact S3-resident

COLD PLAYBACK (no local copy, read-through from S3):
  1. Foghorn picks any storage-capable edge for the cold artifact
  2. Mist STREAM_SOURCE points at Helmsman's local relay URL
  3. Helmsman's read-through relay resolves the block via Foghorn's
     RelayResolve (presigned S3 GET URL minted by the origin/storage cluster)
  4. Relay streams bytes block-by-block; nothing is bulk-copied to disk
```

## Service Events Audit (service_events)

- **Commodore** emits `artifact_registered` ServiceEvents when clip/DVR/VOD registry records are created.
- **Commodore** emits `media.retention_policy.changed`, `media.retention.override_applied`, and `media.retention.override_reset` ServiceEvents for retention policy changes and per-asset override changes.
- **Foghorn** emits the typed clip/DVR/VOD lifecycle event (MistTrigger analytics); it does not emit a derived `artifact_lifecycle` ServiceEvent. The analytics ingest service fans that event out into the `artifact_state_current` overlay and the `artifact_events` history.
- ServiceEvents are metadata-only; lifecycle analytics flow through Periscope.

## Cross-Cluster Artifact Access

When a viewer requests a clip or VOD that lives on a remote cluster, Foghorn uses the `PrepareArtifact` FoghornFederation RPC to obtain either a time-limited presigned S3 URL (when the bytes are synced to S3) or a node-specific peer-relay URL with an opaque capability grant (when an origin node still holds the canonical full file locally but S3 sync is pending). The grant carries no signing key — the origin node's Helmsman authorizes each pull online with its own Foghorn. Peer-relay reads do not require sharing S3 credentials across clusters and do not wait on S3 sync. DVR archive playback uses chapter VOD artifacts: each finalized chapter is a regular VOD-shaped artifact (`origin_type='dvr_chapter'`, `library_visible=false`) addressed by a Commodore-minted, shareable `playbackId` and protected by the parent DVR policy snapshotted at registration, so cross-cluster chapter playback follows the same `PrepareArtifact` rules as any other VOD.

### Flow

```
Viewer → Foghorn A (clip/VOD not on local nodes, not in local S3)
  → ArtifactAdvertisement from PeerChannel: Cluster B has the artifact
  → PrepareArtifact RPC → Foghorn B
      1. Lookup foghorn.artifacts by hash + tenant_id
      2. If sync_status='synced': mint presigned S3 GET URL
      3. Else if a local origin node has role='origin', is_complete=true:
         mint an opaque peer-relay grant and return peer_relay_url +
         peer_relay_grant_id pointing at that node's Helmsman
      4. Else: return Ready=false (Foghorn A surfaces 503; freeze
         pipeline lands bytes independently)
  → Foghorn A returns a local Helmsman relay URL to Mist via the
    STREAM_SOURCE trigger chain. Helmsman's read-through relay then calls
    RelayResolve to obtain the S3-presigned or peer-relay+grant URL,
    attaching the grant as Authorization: Bearer when it's a peer URL.
```

DVR chapter listing (read-only metadata, not playback bytes) flows through:

```
Viewer/Webapp → Gateway GraphQL dvrChapter/dvrChapters
→ Commodore validates tenant ownership (assertDVRTenant accepts dvr_hash, UUID, or playback_id)
→ Origin Foghorn reads foghorn.dvr_chapters for the requested range
→ Returns chapter metadata + Commodore-minted playbackId for each playable chapter
→ Viewer player resolves each chapter playbackId through the standard VOD playback path
```

### PrepareArtifact Request/Response

```protobuf
message PrepareArtifactRequest {
  string artifact_id = 1;        // Artifact hash
  string clip_hash = 2;          // Clip hash fallback alias
  string requesting_cluster = 3;
  string artifact_type = 4;      // "clip", "vod", or "dvr_chapter" — chapters are VOD-shaped
  string tenant_id = 5;
}

message PrepareArtifactResponse {
  string url = 1;                     // Presigned S3 GET URL (clip/vod single file)
  uint64 size_bytes = 2;
  bool ready = 3;                     // Immediately available?
  reserved 4;                         // retired est_ready_seconds — callers fail-fast on Ready=false
  reserved "est_ready_seconds";
  string error = 5;
  map<string, string> segment_urls = 6; // segmented non-DVR artifacts
  string format = 7;                  // mp4, m3u8, etc.
  string internal_name = 8;           // Artifact routing name (vod+{this})
  string stream_internal_name = 9;   // Source stream routing name
}
```

Key design choice: cross-cluster access works in two flavors. (a) When the artifact is on S3 (`sync_status='synced'`), `PrepareArtifact` returns a presigned GET URL. (b) When it's not yet on S3 but an origin node still holds the canonical full file on disk (`role='origin'`, `is_complete=true`, recently-seen), `PrepareArtifact` returns a node-specific `peer_relay_url` + opaque `peer_relay_grant_id` (authorized online by the origin Foghorn, no key on the edge) — same viewer UX, just block-fetched from the origin node's Helmsman instead of S3, no S3-sync wait. Only when neither qualifies does `PrepareArtifact` return `ready=false`; the requesting Foghorn surfaces a 503 (no polling, no async-prep ceremony). The freeze pipeline (driven by the artifact reconciler, kicked on VOD processing-complete and per-segment DVR sync, not by `PrepareArtifact`) lands the bytes asynchronously; the next viewer attempt picks up where the failed one left off. Chapter VOD artifacts follow the same rule — each finalized chapter is registered as an origin row at finalize time and is immediately eligible for peer-relay even before its own S3 sync completes.

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
| `MinFreeBytes`      | 1 GiB   | Write-path free-space guard used before local artifact writes |
| `minRetentionHours` | 1 hour  | Never evict artifacts younger than this                       |

> **Important:** Local storage retention is **best-effort**. Under disk pressure,
> artifacts may be evicted from edge nodes before central retention expires.
> This does not change S3-backed copies or central database records.

The cold-storage manager also has separate freeze thresholds: by default it starts
freezing at 85% disk usage and targets 70% after freeze operations.

## Retention and Cleanup Jobs

Three background jobs manage artifact lifecycle:

| Job                | Interval | Action                                                 |
| ------------------ | -------- | ------------------------------------------------------ |
| `RetentionJob`     | 1 hour   | Soft-delete expired artifacts (status='deleted')       |
| `OrphanCleanupJob` | 5 min    | Send delete requests to Helmsman for deleted artifacts |
| `PurgeDeletedJob`  | 24 hours | Hard-delete from DB + S3 (when no active node copies)  |

**Library visibility vs. byte recovery.** An asset disappears from `/library` at
**soft-delete**, not at hard-delete: when `RetentionJob` sets `status='deleted'`,
the catalog reconciler projects a _deletion_ to Commodore (`UpdateArtifactCatalogSnapshot`
with `deleted=true`) that removes the durable catalog row, so the asset stops
showing as Ready on the very next reconcile pass (≤1h retention sweep + projection
latency). The subsequent windows — `OrphanCleanupJob` evicting node copies and the
`PurgeDeletedJob` **30-day** row/S3 purge below — are a media-plane **byte-recovery
window**, not a library-visibility grace period; the asset is already gone from the
user's library before any bytes are reclaimed. `'expired'` is only the brief
transient state between the retention deadline passing and the sweep that soft-deletes
the row.

### RetentionJob

Uses `retention_until` to decide when terminal artifacts are soft-deleted.
For DVR, `retention_until` is written by `FinalizeDVR` as
`ended_at + dvr_retention_days*24h`; active DVR artifacts are invisible to
retention and are not expired based on `started_at`. The full continuous
archive model is documented in
[`docs/architecture/dvr-continuous-archive.md`](./dvr-continuous-archive.md).
For clips and VOD, callers may still set `retention_until` at creation time;
rows without an explicit value use the retention job's fallback.

```sql
UPDATE foghorn.artifacts
SET status = 'deleted', updated_at = NOW()
WHERE status IN ('completed', 'completed_partial', 'ready', 'failed')
  AND (
    (retention_until IS NOT NULL AND retention_until < NOW())
    OR
    (retention_until IS NULL AND created_at < NOW() - make_interval(days => $retention_days))
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

Final cleanup of `status IN ('deleted','failed')` artifacts past the retention age,
once no non-orphaned node copy remains. Runs every 24 hours.

**Fail-closed — bytes first, then the row.** The sweep is gated on the artifact
cleaner being wired: if it is absent, the job does **nothing** this cycle (it does
NOT delete rows), because an origin Foghorn that delegates storage to peer clusters
has no local S3 and still needs the federation delegate to free remote bytes.
Deleting the row while bytes remain elsewhere would strand them. S3 (or the
cross-cluster delegate) is deleted first; the DB row is hard-deleted only after that
cleanup **definitively succeeds**, so a failure leaves the row for the next cycle to
retry. Tenant identity comes from Foghorn's denormalized `artifacts.tenant_id` (no
Commodore round-trip on the purge path).

```sql
SELECT a.artifact_hash, a.artifact_type, a.tenant_id, ...
FROM foghorn.artifacts a
LEFT JOIN foghorn.vod_metadata v ON v.artifact_hash = a.artifact_hash
WHERE a.artifact_type IN ('clip', 'dvr', 'vod')
  AND a.status IN ('deleted', 'failed')
  AND a.updated_at < NOW() - INTERVAL '30 days'
  AND NOT EXISTS (
    SELECT 1 FROM foghorn.artifact_nodes an
    WHERE an.artifact_hash = a.artifact_hash AND an.is_orphaned = false
  )
-- per row: delete S3/remote bytes via the cleaner, then DELETE the artifacts row
-- only on confirmed cleanup success.
```

## VOD Uploads

The unified artifact model also covers VOD uploads (direct file uploads, not derived from live streams):

```sql
artifact_type = 'vod'
```

**Flow (current):**

1. `createVodUpload` → Gateway → Commodore registers in `commodore.vod_assets` and calls Foghorn to create an S3 multipart upload.
2. Client uploads parts to S3 using presigned URLs.
3. `completeVodUpload` → Gateway → Commodore → Foghorn finalizes upload and updates `foghorn.artifacts` (`artifact_type='vod'`).
4. Same freeze + relay read-through model applies.

## Critical Files

### Schema & Proto

- `pkg/proto` - Clip/DVR registry RPCs
- `pkg/proto` - ClipInfo, DVRInfo, CreateClip/DVR requests (includes user_id)
- `pkg/proto` - GetArtifactStates with request_ids batch lookup
- `pkg/database/sql/schema` - clips, dvr_recordings tables
- `pkg/database/sql/schema` - artifacts (with user_id), artifact_nodes tables
- `pkg/database/sql/clickhouse` - artifact_state_current, artifact_events, storage_events tables

### Gateway (api_gateway) - GraphQL Orchestration

- `api_gateway/internal/resolvers/storage_artifacts.go` - `storageArtifactsConnection` resolver:
  one `ListStorageArtifacts` call returns the unified catalog (business + durable lifecycle) for
  every kind; a transient overlay gap-fills only `hasLocalCopy`/live progress
- `api_gateway/graph` - the unified `StorageArtifact` type (durable fields off the catalog)

### Commodore (api_control) - Business Registry + Durable Catalog

- `api_control/internal/grpc` - `ListStorageArtifacts` (unified query over own clips/
  dvr_recordings/vod_assets tables, NOT Foghorn) and `UpdateArtifactCatalogSnapshot` (the sole
  revision-guarded projection writer, called by the Foghorn reconciler)

### Periscope (api_analytics_query) - Transient Overlay Only

- `api_analytics_query/internal/grpc` - GetArtifactStates with request_ids filter (present
  full-local-node-copy `hasLocalCopy`/live progress overlay; NOT the durable lifecycle source)

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

- `pkg/graphql/operations/queries/GetStorageArtifactsConnection.gql` - unified catalog query
  (all kinds) with durable lifecycle + duration + tracks
- `pkg/graphql/operations/subscriptions/ClipLifecycle.gql` - Real-time updates
- `pkg/graphql/operations/subscriptions/DvrLifecycle.gql` - Real-time updates

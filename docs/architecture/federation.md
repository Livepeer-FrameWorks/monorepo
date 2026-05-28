# Foghorn Federation - Cross-Cluster Stream Delivery

Direct Foghorn-to-Foghorn gRPC protocol for cross-cluster stream replication, artifact access, and real-time telemetry exchange. Enables viewers to be served from the best edge regardless of which cluster hosts the stream.

## Architecture

```
Cluster A (tenant's preferred)              Cluster B (origin)
┌─────────────────────────┐                ┌─────────────────────────┐
│ Foghorn A (leader)      │                │ Foghorn B (leader)      │
│  ├─ PeerManager ────────│── PeerChannel ─│── FederationServer      │
│  ├─ FederationClient    │── QueryStream ─│── LoadBalancer (score)  │
│  ├─ FederationServer    │── NotifyOrigin │── ActiveReplication     │
│  └─ RemoteEdgeCache ◄───│── Telemetry ───│── PrepareArtifact       │
│         │(Redis)         │                │         │(Redis)        │
│  Foghorn A (replica)    │                │  Foghorn B (replica)    │
│  └─ reads RemoteEdgeCache                │  └─ reads shared state  │
│                         │                │                         │
│  Helmsman A1 ── Edge A1 │                │  Helmsman B1 ── Edge B1 │
│  Helmsman A2 ── Edge A2 │                │  Helmsman B2 ── Edge B2 │
└─────────────────────────┘                └─────────────────────────┘
         ↕ (DTSC replication between MistServer instances)
```

## Service Responsibilities

| Component        | Role                                                                                                                                                                                                                                                                                                           | Data                                                                                                                                                                                                                          |
| ---------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| FederationServer | Handles inbound gRPC RPCs (QueryStream, NotifyOriginPull, PrepareArtifact, PeerChannel, CreateRemoteClip, CreateRemoteDVR, ListTenantArtifacts, MigrateArtifactMetadata, ForwardArtifactCommand)                                                                                                               | Reads local LoadBalancer scores; records outbound pulls on StreamRegistry's Location[local].OutboundPullers (NotifyOriginPull); writes federation telemetry to RemoteEdgeCache                                                |
| FederationClient | Pool wrapper for outbound unary RPCs to peer Foghorns                                                                                                                                                                                                                                                          | Uses FoghornPool lazy connections                                                                                                                                                                                             |
| PeerManager      | Manages PeerChannel lifecycles, peer discovery, telemetry push/recv, leader election                                                                                                                                                                                                                           | Redis leader lease, peer address map                                                                                                                                                                                          |
| StreamRegistry   | Unified per-stream identity + replication + admission state (control package). Federated peer ads upsert here as `Locations[peer_cluster]`; local in-flight pulls land as `Locations[local].ReplicatingFrom + PullDTSCURL + DestNodeID`; source-side outbound pulls land as `Locations[local].OutboundPullers` | Redis backing (`{cluster_id}:registry:source:*`, `{cluster_id}:registry:artifact:*`) with cross-instance pubsub fanout; SweepStaleLocations (30s tick / 5-min maxAge) ages stale federated entries + per-OutboundPull entries |
| RemoteEdgeCache  | Federation telemetry cache (Redis). Scope narrowed: stream identity / playback index / active-replication moved to StreamRegistry                                                                                                                                                                              | remote_edges (30s), remote_replications (5m), edge_summary (60s), remote_live_streams (30s), remote_artifacts (90s), stream_peers (60s), peer_heartbeats (30s)                                                                |
| Quartermaster    | Peer discovery via `ListPeers(cluster_id)`                                                                                                                                                                                                                                                                     | Returns peer cluster addresses and shared tenant lists                                                                                                                                                                        |

## Data Flows

### Cross-Cluster Viewer Routing

```
Viewer → Foghorn A (tenant's cluster)

1. Resolve playback_id → stream_name + origin_cluster_id (Commodore, cached)
2. Score local edges (sub-ms, in-memory)
3. Score remote edges from EdgeSummary in Redis (sub-ms, from PeerChannel data)
4. If remote wins and no in-flight replication on StreamRegistry:
   a. QueryStream → Foghorn B: returns scored EdgeCandidates with DTSC URLs
   b. Score remote candidates vs local (CrossClusterPenalty=200)
   c. If origin-pull: NotifyOriginPull → StreamRegistry.MarkReplicating on A + RecordOutboundPull on B → tell Helmsman DTSC source
   d. If redirect: 307 to remote cluster's play endpoint
5. PeerChannel opens (if not already): B pushes EdgeTelemetry (5s), A writes to Redis
6. Steady state: all edges (local + remote) scored on every viewer request from Redis
```

### PeerChannel Telemetry Exchange

PeerChannel is a bidirectional gRPC stream carrying 8 payload types via `oneof`:

| Message               | Interval                 | Direction | Purpose                                                                                                               |
| --------------------- | ------------------------ | --------- | --------------------------------------------------------------------------------------------------------------------- |
| EdgeTelemetry         | 5s                       | Both      | Per-edge BW/CPU/RAM/geo for scoring remote edges                                                                      |
| ReplicationEvent      | On change                | Both      | Origin-pull started/stopped (prevents redirect loops)                                                                 |
| ClusterEdgeSummary    | 15s                      | Both      | Smoothed 30s-avg per-edge data for cheap cluster comparison                                                           |
| StreamLifecycleEvent  | On change + 5s heartbeat | Both      | Stream live/offline (cross-cluster ingest dedup)                                                                      |
| StreamAdvertisement   | 5s                       | Both      | Push-based stream directory with per-edge scoring; builds Adj-RIB-In, eliminates Commodore dependency in steady state |
| ArtifactAdvertisement | 30s                      | Both      | Hot artifact locations on peer edges (avoids S3 round-trips)                                                          |
| PeerHeartbeat         | 10s                      | Both      | Cluster liveness, protocol version, capabilities                                                                      |
| CapacitySummary       | —                        | Both      | Cluster-wide aggregate capacity (proto shell for dCDN bidding)                                                        |

### Peer Discovery

1. **Demand-driven** (fast): Stream validation (ValidateStreamKey, ResolvePlaybackID) returns `cluster_peers[]` from Quartermaster. PeerManager.NotifyPeers registers addresses and opens PeerChannel connections (leader only).
2. **Reconciliation** (5-min polling): PeerManager.refreshPeers calls `Quartermaster.ListPeers(cluster_id)` to catch topology changes.
3. Federation address convention: `TenantClusterPeer.foghorn_grpc_addr` is the internal Foghorn listener for the peer cluster, normally a mesh address on `:18019` with `foghorn.internal` TLS identity. `PeerManager.NotifyPeers` consumes that Quartermaster-provided address. Missing peer addresses are a control-plane discovery problem; federation must not silently fall back to the public edge-bootstrap listener.

### Peer Lifecycle Types

| Type          | When                              | Example                                                                         |
| ------------- | --------------------------------- | ------------------------------------------------------------------------------- |
| Always-on     | Official ↔ preferred cluster pair | Coverage PeerChannel for ClusterEdgeSummary                                     |
| Stream-scoped | Other subscribed clusters         | PeerChannel opens on first stream, closes when last stream ends (UntrackStream) |

### Cross-Cluster Artifact Access

```
Viewer requests clip/VOD on Cluster A, artifact lives on Cluster B:

1. Foghorn A: PrepareArtifact(artifact_hash, tenant_id) → Foghorn B
2. Foghorn B: queries foghorn.artifacts
3. If sync_status='synced': mints presigned S3 GET URL → returns to A
4. Else if a local origin node has the canonical full file on disk
   (foghorn.artifact_nodes row with role='origin', is_complete=true,
   recently-seen): Foghorn B mints a short-lived artifact_relay JWT
   (5min TTL, bound to {origin node id, artifact_hash, request path})
   and returns peer_relay_url + peer_relay_token → A. Cluster A's
   Helmsman block cache fetches blocks directly from origin node's
   Helmsman with the token as Authorization: Bearer. No S3 sync wait.
5. Else: returns Ready=false; Foghorn A surfaces 503 to the viewer.
   The freeze pipeline lands the bytes asynchronously; the next viewer
   attempt picks up where the failed one left off.
6. Foghorn A: redirects viewer (or hands relay URL to its Helmsman).

For clip/VOD: returns a single URL — either S3-presigned or peer-relay.
```

The peer-relay JWT is signed with origin Foghorn's own service key.
Only same-cluster Helmsmans validate the token; the requesting cluster
treats it as opaque and forwards it through to its local block cache.
This keeps trust boundaries intact (no cross-cluster key distribution)
while letting hot-but-unsynced artifacts serve viewers immediately
across the federation.

DVR archive playback does not use whole-artifact `PrepareArtifact`. A DVR can
run for months; replay is sliced into finalized chapter VOD artifacts:

1. Gateway calls Commodore `RetrieveDVRChapter` / `ListDVRChapters`.
2. Commodore validates tenant ownership, routes to the DVR's
   `origin_cluster_id`, and returns the chapter metadata. Each chapter
   carries a Commodore-minted public `playbackId` (in
   `commodore.dvr_chapter_playback`) once the chapter has been dispatched
   for finalization. Active-but-unfinalized chapters carry no `playbackId`;
   the rolling DVR's own `playbackId` (`dvr+<dvr_internal_name>` surface)
   serves the in-flight portion.
3. Chapter playback flows through the standard artifact playback path —
   the chapter `.mkv` is a regular `vod`-shaped artifact (with
   `origin_type='dvr_chapter'`, `library_visible=false`). Edges resolve
   the chapter playback_id through Commodore exactly the way they resolve
   any VOD playback_id, and serve it via the relay/block-cache path.

Federation `PrepareArtifact` rejects DVR. Chapter replay uses the chapter
API + normal artifact playback; cross-cluster requests for the chapter
artifact follow the same federation rules as any other VOD.

### Cross-Cluster Artifact Command Routing

When Commodore needs to delete/stop an artifact, it routes to the cluster that
owns it (push model). If the command arrives at the wrong Foghorn (stale cache,
race condition), Foghorn forwards it via ForwardArtifactCommand (safety net).

#### Push Model (Commodore → Foghorn)

1. Foghorn sends `cluster_id` in `ValidateStreamKey` during ingest
2. Commodore records `active_ingest_cluster_id` on the stream
3. On CreateClip, Commodore routes to the ingest cluster (not primary)
4. Clip/DVR/VOD DB records store `origin_cluster_id`
5. On DeleteClip/StopDVR/DeleteDVR/DeleteVodAsset:
   - Query `origin_cluster_id` from business registry
   - Resolve Foghorn address via `GetClusterRouting` peer list
   - Call the correct cluster directly

#### Forward Model (Foghorn → Federation Peer)

If Foghorn receives an artifact command for an artifact not in its local DB:

1. Try local handler (existing logic)
2. If `ErrNoRows` → iterate known federation peers
3. Call `ForwardArtifactCommand(command, hash, tenant_id)` on each peer
4. First peer that returns `handled=true` wins
5. If no peer handles → return NotFound to caller

#### Tenant Operations Fan-Out

`TerminateTenantStreams` and `InvalidateTenantCache` fan out to ALL clusters
the tenant has access to (via `clusterPeers`), not just the primary cluster.
Results are aggregated; partial failures are logged but don't block the response.

### Artifact Migration

```
Tenant moves preferred cluster from B to A:

1. Foghorn A: MigrateArtifactMetadata(tenant_id, source_cluster=B)
2. Foghorn A → Foghorn B: ListTenantArtifacts(tenant_id)
3. Foghorn B: returns all artifact metadata records
4. Foghorn A: INSERT ... ON CONFLICT DO NOTHING with origin_cluster_id = B
5. Playback requests for migrated artifacts use PrepareArtifact to fetch from B's S3
```

## HA Model

In multi-replica Foghorn deployments:

- **Unary RPCs** (QueryStream, NotifyOriginPull, PrepareArtifact): LB round-robin. Any instance handles them via shared Redis state.
- **PeerChannel**: Leader-only. Redis-based leader election (SET NX, 15s TTL, renewed every 5s on telemetry tick). Leader opens and maintains all PeerChannel connections. If leader dies, lease expires, another instance acquires and reconnects.
- **Non-leader replicas**: Read remote edge data from Redis (written by leader's PeerChannel). GetPeerAddr populated from Redis sync (syncPeerAddressesToRedis/loadPeerAddressesFromRedis).

```
Peer B ──PeerChannel──→ [LB] ──→ Leader Instance ──writes──→ Redis
                                                               ↑ reads
                                  Replica Instance ──reads────┘
```

## Federation Telemetry & Geo Enrichment

Federation events are emitted by Foghorn for every cross-cluster operation (peering, replication, artifact access, redirect) and ingested into ClickHouse (`periscope.federation_events`) via the standard analytics pipeline.

### Self-Geo Resolution

Each Foghorn resolves its own geographic coordinates at bootstrap:

1. Foghorn reads `NODE_ID` from env (set by CLI provisioning)
2. Sends `NodeId` in `BootstrapServiceRequest` to Quartermaster
3. Quartermaster JOINs `infrastructure_nodes`, returns full `InfrastructureNode` in response
4. Foghorn reads `ExternalIp` → GeoIP lookup → caches lat/lon/location in `handlers.SetSelfGeo()`

If `NODE_ID` is unset or the node has no `external_ip`, self-geo stays zero (graceful degradation).

### Geo Exchange via PeerHeartbeat

PeerHeartbeat messages (10s interval) carry `foghorn_lat`, `foghorn_lon`, and `foghorn_location`. Each peer caches the remote foghorn's geo in `peerState`. This enables:

- Geo-aware federation topology visualization in the UI
- Per-flow distance calculation for cross-cluster routing analytics
- `GetPeerGeo(clusterID)` for enriching outbound federation events

### Auto-Enrichment

`emitFederationEvent()` in federation handlers automatically sets `local_lat`, `local_lon` from self-geo and `remote_lat`, `remote_lon` from peer geo cache before emitting. All call sites (peering, replication, artifact, redirect events) get geo enrichment without per-site changes.

### ClickHouse Columns

Federation events carry `local_lat`, `local_lon`, `remote_lat`, `remote_lon` (all `Float64`). Periscope Ingest writes these in `processFederationEvent()`.

## Key Files

- `pkg/proto` - Service definition (9 RPCs, 8 PeerMessage payload types)
- `api_balancing/internal/federation` - FederationServer: all RPC handlers
- `api_balancing/internal/federation` - FederationClient: pool wrapper for outbound RPCs
- `api_balancing/internal/federation` - PeerManager: lifecycle, discovery, telemetry, leader election
- `api_balancing/internal/federation` - RemoteEdgeCache: Redis CRUD with TTLs and Lua-scripted lease ops (federation telemetry only — stream identity / playback index / active replication moved to control.StreamRegistry)
- `api_balancing/internal/control` - StreamRegistry: unified per-stream identity + per-peer Locations + admission state; consumes StreamAdvertisement ingest via UpsertFederatedSource; records dest-side pulls via MarkReplicating and source-side outbound pulls via RecordOutboundPull
- `api_balancing/cmd/foghorn` - Wiring: FederationServer, FederationClient, PeerManager, RemoteEdgeCache, StreamRegistry (with NewRedisRegistryStore + StartSweeper)

## Gotchas

- **Leader-only PeerChannel**: Only one Foghorn instance per cluster runs persistent PeerChannel connections. Loss of leadership triggers `disconnectAllPeers`; peers reconnect to the new leader via LB. Non-leaders still serve unary RPCs.
- **Demand-driven discovery is the fast path**: Peers are usually discovered from stream validation responses (sub-second), not from 5-min polling. `NotifyPeers` is called on every `ValidateStreamKey`/`ResolvePlaybackID` with non-empty `cluster_peers`.
- **StreamAdvertisement eliminates control plane in steady state**: Once PeerChannel is open, peers build a local stream directory (playback_id reverse-index) from StreamAdvertisement messages. Viewer routing can skip Commodore resolve entirely. The directory lives in `control.StreamRegistry` as per-peer `Locations[peer_cluster]` entries; `withdrawFederatedSource` (IsLive=false in the next ad) drops the peer's Location and, if no Locations remain, the whole entry plus its playback_id reverse-index. `SweepStaleLocations` provides a 5-min fallback expiry for peers that stop advertising without a clean withdrawal.
- **Tenant filtering in shared-lb**: `QueryStream` filters EdgeCandidates by `tenant_id` so tenants on shared clusters only see their own edges.
- **CapacitySummary is a proto shell**: Received but not stored yet. Reserved for dCDN marketplace capacity trading.

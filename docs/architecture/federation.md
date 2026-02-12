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

| Component        | Role                                                                                                                                                                                             | Data                                                                                                                                    |
| ---------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | --------------------------------------------------------------------------------------------------------------------------------------- |
| FederationServer | Handles inbound gRPC RPCs (QueryStream, NotifyOriginPull, PrepareArtifact, PeerChannel, CreateRemoteClip, CreateRemoteDVR, ListTenantArtifacts, MigrateArtifactMetadata, ForwardArtifactCommand) | Reads local LoadBalancer scores, writes ActiveReplication records                                                                       |
| FederationClient | Pool wrapper for outbound unary RPCs to peer Foghorns                                                                                                                                            | Uses FoghornPool lazy connections                                                                                                       |
| PeerManager      | Manages PeerChannel lifecycles, peer discovery, telemetry push/recv, leader election                                                                                                             | Redis leader lease, peer address map                                                                                                    |
| RemoteEdgeCache  | Redis-backed cache for all cross-cluster state                                                                                                                                                   | remote_edges (30s TTL), remote_replications (5m), active_replications (5m), edge_summary (60s), stream_ads (15s), peer_heartbeats (30s) |
| Quartermaster    | Peer discovery via `ListPeers(cluster_id)`                                                                                                                                                       | Returns peer cluster addresses and shared tenant lists                                                                                  |

## Data Flows

### Cross-Cluster Viewer Routing

```
Viewer → Foghorn A (tenant's cluster)

1. Resolve playback_id → stream_name + origin_cluster_id (Commodore, cached)
2. Score local edges (sub-ms, in-memory)
3. Score remote edges from EdgeSummary in Redis (sub-ms, from PeerChannel data)
4. If remote wins and no ActiveReplication:
   a. QueryStream → Foghorn B: returns scored EdgeCandidates with DTSC URLs
   b. Score remote candidates vs local (CrossClusterPenalty=200)
   c. If origin-pull: NotifyOriginPull → store ActiveReplication → tell Helmsman DTSC source
   d. If redirect: 307 to remote cluster's play endpoint
5. PeerChannel opens (if not already): B pushes EdgeTelemetry (5s), A writes to Redis
6. Steady state: all edges (local + remote) scored on every viewer request from Redis
```

### PeerChannel Telemetry Exchange

PeerChannel is a bidirectional gRPC stream carrying 9 message types via `oneof`:

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
3. Federation address convention: `foghorn.{cluster_slug}.{base_url}:18019`

### Peer Lifecycle Types

| Type          | When                              | Example                                                                         |
| ------------- | --------------------------------- | ------------------------------------------------------------------------------- |
| Always-on     | Official ↔ preferred cluster pair | Coverage PeerChannel for ClusterEdgeSummary                                     |
| Stream-scoped | Other subscribed clusters         | PeerChannel opens on first stream, closes when last stream ends (UntrackStream) |

### Cross-Cluster Artifact Access

```
Viewer requests clip/DVR/VOD on Cluster A, artifact lives on Cluster B:

1. Foghorn A: PrepareArtifact(artifact_hash, tenant_id) → Foghorn B
2. Foghorn B: queries foghorn.artifacts, verifies S3 sync
3. If synced: generates presigned S3 GET URL(s) → returns to A
4. If local-only: triggers async freeze, returns est_ready_seconds
5. Foghorn A: redirects viewer to presigned URL (or retries after delay)

For DVR: returns map of segment filename → presigned URL.
For clip/VOD: returns single presigned URL.
```

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

`emitFederationEvent()` in both `handlers.go` and `peer_manager.go` automatically sets `local_lat`, `local_lon` from self-geo and `remote_lat`, `remote_lon` from peer geo cache before emitting. All call sites (peering, replication, artifact, redirect events) get geo enrichment without per-site changes.

### ClickHouse Columns

Federation events carry `local_lat`, `local_lon`, `remote_lat`, `remote_lon` (all `Float64`). Periscope Ingest writes these in `processFederationEvent()`.

## Key Files

- `pkg/proto/foghorn_federation.proto` - Service definition (9 RPCs, 9 PeerMessage types)
- `api_balancing/internal/federation/server.go` - FederationServer: all RPC handlers
- `api_balancing/internal/federation/client.go` - FederationClient: pool wrapper for outbound RPCs
- `api_balancing/internal/federation/peer_manager.go` - PeerManager: lifecycle, discovery, telemetry, leader election
- `api_balancing/internal/federation/cache.go` - RemoteEdgeCache: Redis CRUD with TTLs and Lua-scripted lease ops
- `api_balancing/cmd/foghorn/main.go` - Wiring: FederationServer, FederationClient, PeerManager, RemoteEdgeCache

## Gotchas

- **Leader-only PeerChannel**: Only one Foghorn instance per cluster runs persistent PeerChannel connections. Loss of leadership triggers `disconnectAllPeers`; peers reconnect to the new leader via LB. Non-leaders still serve unary RPCs.
- **Demand-driven discovery is the fast path**: Peers are usually discovered from stream validation responses (sub-second), not from 5-min polling. `NotifyPeers` is called on every `ValidateStreamKey`/`ResolvePlaybackID` with non-empty `cluster_peers`.
- **StreamAdvertisement eliminates control plane in steady state**: Once PeerChannel is open, peers build a local stream directory (playback_id reverse-index) from StreamAdvertisement messages. Viewer routing can skip Commodore resolve entirely.
- **Tenant filtering in shared-lb**: `QueryStream` filters EdgeCandidates by `tenant_id` so tenants on shared clusters only see their own edges.
- **CapacitySummary is a proto shell**: Received but not stored yet. Reserved for dCDN marketplace capacity trading.

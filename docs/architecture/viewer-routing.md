# Viewer Routing (Foghorn)

This document describes how Foghorn selects edge nodes for viewer playback requests. It is a contributor reference for understanding and modifying the routing algorithm.

For operator-level documentation, see `website_docs/.../operators/architecture.mdx` (Viewer Routing section).

## Related Source Files

- Load balancer core: `api_balancing/internal/balancer`
- State management: `api_balancing/internal/state`
- HTTP handlers: `api_balancing/internal/handlers`
- gRPC server: `api_balancing/internal/grpc`
- Playback resolution: `api_balancing/internal/control`
- Geo bucketing: `api_balancing/internal/geo`
- Weight config: `api_balancing/cmd/foghorn/main.go:168-172`

## Request Paths

```
┌────────────────────────────────────────────────────────────────────────┐
│ GraphQL (Primary) - SDK/Player integrations                            │
│ Player → Bridge (GraphQL) → Commodore (gRPC) → Foghorn (gRPC)          │
│          resolveViewerEndpoint   ResolvePlayback   ResolveViewerEndpoint│
└────────────────────────────────────────────────────────────────────────┘

┌────────────────────────────────────────────────────────────────────────┐
│ HTTP (Direct) - CLI tools, direct URL access                           │
│ Client → Foghorn /play/{viewkey}[/hls/index.m3u8|/webrtc]             │
│          Returns JSON or 307 redirect                                  │
└────────────────────────────────────────────────────────────────────────┘
```

GraphQL resolution is viewer-local only when the caller is the viewer's browser
or playback device. If an application backend calls `resolveViewerEndpoint`,
Gateway forwards the backend/proxy IP and Foghorn scores against that location.
Custom players that do not call Gateway from the viewer should use the
tenant/global/cluster playback DNS name and `/play` URL directly.

### Authority before placement

All entry paths resolve playback identity, tenant billing/lifecycle, serving
grants, and public/JWT/webhook policy from Foghorn's verified durable media
authority before node scoring. HTTP, gRPC, `PLAY_REWRITE`, and `USER_NEW` share
the same object+tenant semantics. Once a projection is marked ready, signed
denial, tombstone, hard expiry, or missing required sealed policy material never
falls back to a central allow. A transient local read failure or inconsistent
row is not an authority decision; the request may use the connected evaluator
when it is available and otherwise returns unavailable.

Artifact placement receives the already-resolved signed hash, origin, tenant,
and grant envelope. It does not call Commodore again for optional analytics or
catalog metadata. Runtime node availability remains Foghorn state and may
change independently of the signed business decision. See
[Media-cluster authority and autonomy](media-authority.md).

Connected responses keep those two projections separate. `cluster_peers` is
health-filtered routing input; `authority_cluster_peers` contains the static
tenant grants used only to shadow-compare signed authority. Promotion never
compares cell-local health, addresses, or object-storage endpoints as though
they were tenant policy. During a mixed-version rollout, absence of the
authority projection prevents promotion and leaves the connected path active.

## Scoring Algorithm

Foghorn ranks eligible nodes using a weighted scoring system. **Higher score = better node.**

### Score Components

```go
score := cpuScore + ramScore + bwScore + geoScore + streamBonus
```

| Component    | Default Weight | Calculation                                               |
| ------------ | -------------- | --------------------------------------------------------- |
| CPU          | 500            | `WEIGHT - (cpu_pct * WEIGHT / 1000)`                      |
| RAM          | 500            | `WEIGHT - (ram_used * WEIGHT / ram_max)`                  |
| Bandwidth    | 1000           | `WEIGHT - ((up_speed + reserved_bw) * WEIGHT / bw_limit)` |
| Geo          | 1000           | `WEIGHT - (WEIGHT * normalized_distance)`                 |
| Stream bonus | +50            | If node already has the stream                            |

### Weight Configuration

Environment variables (defaults in parentheses):

```bash
CPU_WEIGHT=500        # CPU utilization weight
RAM_WEIGHT=500        # RAM utilization weight
BANDWIDTH_WEIGHT=1000 # Bandwidth utilization weight
GEO_WEIGHT=1000       # Geographic proximity weight
```

### Geographic Distance

Distance is normalized to [0, 1] using haversine formula:

- `distance = 0` → viewer and node are co-located → max geo score
- `distance = 1` → opposite sides of the globe → zero geo score

Geographic coordinates use H3 bucketing (resolution 5, ~253 km² cells) for privacy. See `docs/architecture/analytics-pipeline.md` for details.

#### Coordinate Sources

Foghorn resolves viewer coordinates using the following priority order:

1. **Cloudflare headers** (when behind Cloudflare): `CF-IPLatitude` / `CF-IPLongitude` for coordinates, `CF-Connecting-IP` for the real client IP.
2. **GeoIP MMDB lookup**: MaxMind database configured via `GEOIP_MMDB_PATH`. Resolves IP → lat/lon.
3. **Disabled**: If neither source is available, geo scoring is skipped (all nodes get equal geo score).

Related source: `pkg/geoip`, `api_balancing/internal/handlers` (Cloudflare header extraction).

### Stream Bonus

Nodes already serving the requested stream get a +50 bonus (configurable via `STREAM_BONUS` env var). This reduces origin fetches and improves cache efficiency.

### Score Caching

CPU and RAM scores are pre-computed on node state updates (`recomputeNodeScoresLocked`) to avoid recalculating on every request. Bandwidth and geo scores are computed at request time.

## Node Selection Flow

```go
func GetTopNodesWithScores(streamName, lat, lon, ...) ([]NodeWithScore, error) {
    // 1. Filter eligible nodes (online, not in maintenance, has stream if required)
    // 2. For each node: compute score
    // 3. Sort by score descending
    // 4. Return top N nodes
}
```

### Eligibility Filters

A node must pass all filters:

1. **Online**: Has recent heartbeat
2. **Not in maintenance**: Maintenance flag not set
3. **Capacity**: Below bandwidth limit
4. **Stream availability**: For playback, node must have the stream (or be able to pull it)

### Fallback Behavior

If no node has the stream:

- Source selection mode: Return best node for pulling from origin
- Viewer mode: Return error (stream not available)

## npm_player Integration

The player SDK uses Gateway GraphQL to resolve endpoints from the viewer's browser:

```graphql
query ResolveViewer($contentId: String!) {
  resolveViewerEndpoint(contentId: $contentId) {
    primary {
      nodeId
      baseUrl
      protocol
      url
      outputs
    }
    fallbacks {
      nodeId
      baseUrl
      protocol
      url
      outputs
    }
    metadata {
      contentType
      contentId
      status
      isLive
    }
  }
}
```

Implementation: `npm_player/packages/core/src/core`

### Response Shape

```typescript
interface ViewerEndpoint {
  primary: NodeEndpoint; // Best node
  fallbacks: NodeEndpoint[]; // Backup nodes (up to 4)
  outputs: ProtocolOutputs; // URLs for each protocol
}
```

The player:

1. Receives endpoint list from Gateway
2. Selects best protocol using its own scoring (`npm_player/packages/core/src/core`)
3. Falls back to next node/protocol on failure

## Cross-Cluster Routing

When a viewer's local cluster doesn't have the requested stream, Foghorn checks peer clusters before returning an error. This extends the single-cluster scoring with a two-phase remote lookup.

### Phase 1: EdgeSummary Candidate Scoring (Cheap)

PeerChannel exchanges `ClusterEdgeSummary` messages every 15 seconds. These are cached in Redis (`RemoteEdgeCache`, 60s TTL). Foghorn uses this cache to score candidate remote edges without a per-request RPC. The summary is capacity/topology data, not a per-stream availability proof.

### Phase 2: QueryStream RPC (On Demand)

If a remote candidate wins the summary-level comparison, Foghorn confirms it by sending `QueryStream` to that peer's FoghornFederation service. On cold start, when no EdgeSummary cache is available but peers exist, Foghorn fans out `QueryStream` directly. The peer scores its local nodes (using the same weight algorithm) and returns `EdgeCandidate` entries with DTSC URLs, capacity data, and `IsOrigin` flags.

### Remote Edge Scoring

Remote candidates are scored with `ScoreRemoteEdges` using the same weight components (CPU, RAM, BW, GEO) but with two differences:

| Difference                  | Detail                                                                                                                                                                                    |
| --------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **CrossClusterPenalty**     | 200 points subtracted from every remote score. Local edges win unless a remote edge is meaningfully better on GEO or BW. Equivalent to giving local edges a +200 `LocalPreference` bonus. |
| **No StreamAffinity bonus** | Remote edges don't get the +50 stream bonus (they already have the stream — that's why they're candidates).                                                                               |

Remote edges with a final score ≤ 200 (the penalty) are discarded entirely.

```go
const crossClusterPenalty = uint64(200)

// ScoreRemoteEdges scores remote edge candidates using the same weight system as local
// edges but applies a CrossClusterPenalty. Remote edges only win when significantly
// better on GEO or BW than local edges.
func (lb *LoadBalancer) ScoreRemoteEdges(candidates []RemoteEdgeCandidate, viewerLat, viewerLon float64) []NodeWithScore
```

### RemoteEdgeCandidate

```go
type RemoteEdgeCandidate struct {
    ClusterID   string
    NodeID      string
    BaseURL     string
    GeoLat      float64
    GeoLon      float64
    BWAvailable uint64   // absolute bytes/s, normalized against 10 Gbps reference
    CPUPercent  float64
    RAMUsed     uint64
    RAMMax      uint64
}
```

### Decision: Origin-Pull vs Redirect

After scoring, Foghorn merges remote and local `NodeWithScore` entries (remote entries have `ClusterID` set) and sorts by score descending. The top result determines the action:

| Top result               | Action                                                                                                                                               |
| ------------------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------- |
| Local edge with capacity | **Origin-pull**: Foghorn arranges a DTSC pull from the remote origin to a local edge via `arrangeOriginPull`. Subsequent viewers are served locally. |
| Remote edge              | **Redirect**: Viewer is redirected (307) to the remote cluster's edge. No local replication.                                                         |

See `docs/architecture/stream-replication-topology.md` for the full origin-pull lifecycle and loop prevention.

### Related Source Files

- Remote scoring: `api_balancing/internal/balancer` (`ScoreRemoteEdges`, `crossClusterPenalty`)
- Remote edge cache: `api_balancing/internal/federation` (`RemoteEdgeCache`, `EdgeSummaryEntry`, `RemoteEdgeEntry`)
- Federation client: `api_balancing/internal/federation` (`QueryStream` RPC)
- Origin-pull arrangement: `api_balancing/internal/handlers` (`arrangeOriginPull`)

## Routing Events (Analytics)

Every routing decision emits a `load_balancing` event to Kafka:

```go
type LoadBalancingPayload struct {
    StreamName        string
    SelectedNode      string
    Score             uint64
    ClientLatitude    float64  // H3 centroid
    ClientLongitude   float64  // H3 centroid
    NodeLatitude      float64
    NodeLongitude     float64
    Status            string   // "success", "redirect", "error"
    DurationMs        float32  // Decision latency
    ClusterID         string   // Emitting cluster context
    SelectedClusterID string   // Authenticated cluster of SelectedNode
    ControlCellID     string   // Foghorn/media-authority cell
    OriginClusterID   string   // Stream origin when known
    RemoteClusterID   string   // Set when viewer was routed cross-cluster
}
```

Stored in: `periscope.routing_decisions` (ClickHouse)

Tenant routing analytics require `stream_tenant_id` to equal the authenticated content tenant. If
stream ownership enrichment is missing, the row is excluded rather than falling back to the
infrastructure-owner `tenant_id`; this deliberately trades partial retained coverage for isolation.
Unattributed decisions expire under the routing table's 90-day TTL and are not backfilled because
routing decisions are temporary diagnostic telemetry rather than durable delivery facts.

`SelectedClusterID` is the placement answer: it identifies the cluster containing the node returned to the viewer. It is derived from authenticated node state, not from the Foghorn process. `OriginClusterID` answers where the content came from, while `ControlCellID` answers which control plane made the decision. `RemoteClusterID` records a cross-cluster target during federation. These fields are deliberately independent; missing values remain empty rather than being copied from one another.

## Ingest Routing

Publishers are routed by the same scorer, through a separate front door.

|                   | Viewers                                    | Publishers                         |
| ----------------- | ------------------------------------------ | ---------------------------------- |
| Entry point       | `GET /play/{viewKey}`                      | `GET`/`POST` `/ingest/{streamKey}` |
| Capability filter | `edge`                                     | `ingest`                           |
| Redirect          | 307 per protocol                           | 307 to WHIP (`POST` only)          |
| Fallback path     | cross-cluster origin-pull or peer redirect | geo-DNS `edge-ingest` name         |

Both resolve in `api_balancing/internal/control` (`ResolveLivePlayback` and
`ResolveIngestEndpoints`) and filter candidates by capability. Ingest then
keeps candidates inside the cluster-peer envelope Commodore returned with the
resolve — the same envelope playback authorizes cross-cluster candidates
against. It is Quartermaster's cluster↔tenant grant already narrowed by the
tenant's plan classes and by cluster health, so neither side re-derives
entitlement. `NodeState.TenantID` is ownership metadata, not routing authority.

One physical Foghorn serves many virtual media clusters and accepts any valid
stream key, so nothing about the listener, the hostname, or its own
`CLUSTER_ID` bounds the candidate set. It names no cluster on the resolve.

A live ingest claim is the one thing that narrows it: while a publisher holds
the stream, `active_ingest_cluster_id` pins reconnects to the cluster already
ingesting, because `PUSH_REWRITE` would refuse anywhere else as
`DUPLICATE_INGEST`.

Ready signed ingest authority does not replace that steady-state placement
lookup. The HTTP and gRPC ingest front doors first request the connected live
claim; only when it is unavailable do they use the signed projection and pin to
the bundle's deterministic outage-owner cluster. Thus a healthy claim in
cluster B wins over an older origin/outage owner in cluster A, while a media
cell can still route an encoder during a control-plane outage.

Tenant stream capacity is enforced in the same PostgreSQL transaction that
creates the durable ingest session. All admissions take a tenant advisory lock;
capped admissions count active sessions and insert only when a slot remains.
Session close or fenced reap releases the slot by ending that row. Valkey loss therefore
cannot revoke a valid publisher or admit a second publisher from a torn lease.

Viewer capacity remains an atomic member lease in shared Valkey. Its identity
prefers Mist's stable connection ID; Foghorn durably maps
`(node_id, session_id)` to that capacity ID so `USER_END` may arrive on another
replica or after a restart. Helmsman's authoritative ten-second client
inventory renews live viewer leases. Exact close releases promptly; missed
closes converge by lease expiry, and runtime reconciliation never deletes
members from a process-local partial view.

Two things differ:

- **Only WHIP can be redirected.** RTMP and SRT have no redirect mechanism, so
  those protocols fall back to the geo-DNS `edge-ingest` name, which steers on
  location alone. The JSON `GET` form exists so encoder tooling can still read a
  load-aware URL.
- **Resolution never claims placement.** Admission goes through Commodore's
  `ResolveStreamContext`, not `ValidateStreamKey`: the latter writes
  `active_ingest_cluster_id` under a 30-second lease and rejects other clusters
  with `DUPLICATE_INGEST`. A `GET` resolve or an abandoned WHIP attempt must not
  lock out the cluster that actually ingests. `PUSH_REWRITE` stays the
  enforcement gate and the only path that arbitrates contention for a claim,
  and still applies the gates the front door deliberately skips (free-tier load
  admission, per-tenant stream caps, duplicate-ingest detection). Placement is
  afterwards re-asserted from Foghorn's live source-presence, under the same
  contention rule, for as long as the publisher stays connected.

Ingest decisions emit `ingest_resolve` routing events to the same
`periscope.routing_decisions` table.

## Modifying the Algorithm

### Adding a new weight

1. Add env var + weight field in `api_balancing/cmd/foghorn`
2. Pass to `StreamStateManager` in state initialization
3. Add to score calculation in the balancer module.

### Changing eligibility filters

Edit `GetTopNodesWithScores` in `api_balancing/internal/balancer`.

### Adding node metadata

1. Add field to `NodeState` in the stream-state module.
2. Populate from Helmsman heartbeats (in `api_balancing/internal/triggers`)
3. Use in scoring or filtering as needed

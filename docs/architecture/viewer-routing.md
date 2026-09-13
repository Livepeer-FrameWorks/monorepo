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
- Weight config: `api_balancing/cmd/foghorn/main.go` (`CPU_WEIGHT`, `RAM_WEIGHT`, `BANDWIDTH_WEIGHT`, `GEO_WEIGHT`)

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

Commodore selects the resolving Foghorn from the content's owner/active-ingest
routing identity for both anonymous and authenticated viewers. A viewer's own
tenant is not the content owner and cannot choose that authority store. This is
control-plane entry routing, not edge placement: the resolving EU Foghorn can
return a prepared US edge. Viewer IP, playback token, explicit protocol and
payment context remain attached to the request through the proxy.

Gateway's viewer and ingest resolvers use the client identity stamped by trusted-proxy
middleware. If no middleware identity exists, only the direct peer is used; the resolvers
do not independently trust `X-Forwarded-For` or `X-Real-IP`. Foghorn's HTTP media handlers
use the shared proxy parser with `TRUSTED_PROXY_CIDRS` and retain that identity for the
request's geo lookup, payment settlement, and routing telemetry. IPv6 addresses are
preserved. An absent identity means unknown geography, not an invented location.

The shared [media placement path](media-placement-policy.md) resolves every live
viewer and publisher request. Before listeners start, Foghorn installs the ingest
and viewer placement preparers on both the HTTP and gRPC front doors and the
final publisher and viewer admission adapters on the Mist triggers. No legacy fallback survives
alongside them: live viewer resolution runs only through
`control.ResolvePreparedLiveViewerEndpoint`, ingest resolution fails closed
without a preparer, and `PUSH_REWRITE`, `PLAY_REWRITE` and `USER_NEW` deny a
connection when no adapter is installed rather than admitting it unchecked. The
weighted scoring and `QueryStream` federation described below now serve
stored-media destination selection, node-bound source lookup, and the answers this
cell returns to peers — not live viewer or publisher selection.

The older hostname-returning Mist balancer route is gone, and the bare
`/<stream>` viewer route with it. What remains of that surface is the node-bound
source lookup (`/?source=` and the signed by-node path) and read-only
diagnostics, served by `MistSourceHandler` and `AuthorizedMistSourceHandler`
behind `RequireInternalSourceAccess`. Neither answers a viewer with a playback
destination.

The player scores only the sources returned by resolution; it never synthesizes
a Mist embed URL from a media URL. After exhausting players for the selected
format, a Gateway-backed player requests another authorized format and loads
its matching player modules. Switching from WebRTC to HLS therefore performs
placement again rather than deriving an HLS or `player.js` URL on the old node.

Stored-media origin access is checked against current signed tenant grants, not
the list of currently connected federation peers. A disconnected origin does
not revoke access to an already warm local copy. Candidate selection and cold
acquisition still use runtime availability, and removed or expired authority
still denies new playback/source opens. HTTP, gRPC and Mist source admission
enforce this separation.

On the prepared HTTP path, the manifest format is resolved before selection:
`/play/{id}/cmaf/index.mpd` requests DASH, while `cmaf/index.m3u8` requests HLS-CMAF.
WHEP remains distinct from WebSocket WebRTC. A conflicting protocol/manifest is a
client error, not permission to return another format. Public query/header geo
hints do not override the trusted client-location lookup. Tests exercise the full
HTTP billing/resolve/redirect path and authenticated Commodore-to-Foghorn proxy;
they do not establish actual-media or fleet-activation readiness.

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

Stored-media routing filters warm/read-through candidates through signed serving
policy. If the permitted serving cell has no candidate in the coordinator's local
inventory, it prepares that destination through global placement instead of
returning a refused local edge. The selected edge's existing artifact relay reads
the bytes from their authorized storage location; serving placement does not move
the durable copy or change storage placement.

Mist's HTTP stream-info/page requests also run pre-source placement admission,
using the reported HTTP listener (`mist_html`). They are not audience sessions.
Actual media connections are admitted independently against their connector's
protocol, including Mist's `/WS` WebSocket labels. Query parameters cannot supply
listener capability evidence or turn a media connection into metadata.

Active DVR uses the recording's signed artifact ID, hash, internal name and
playback ID for viewer preparation, not the parent live-stream identity. HTTP
and gRPC read recording lifecycle from its owning cell; missing local runtime
state does not mean the recording has stopped. The bounded federation
`PrepareArtifact` request with type `dvr` returns lifecycle and recording-node
metadata only, bound to the tenant and artifact hash. It does not issue storage
URLs. Source admission still authorizes the exact recording before a selected
edge pulls it. Finalized chapters use their own VOD artifact playback IDs.

DVR source pulls keep the `dvr+` registry/credential namespace and are bound to
the active recording's unique storage owner, not a live publisher generation.
Both same-cell and cross-cell edge pulls present an attempt- and
destination-bound source credential. Origin admission rechecks tenant, recording
status and owner; warm source resolution rechecks artifact authority and renews
the accepted pull. An existing inbound pull cannot substitute for recording
authority or borrow the parent live stream's placement receipt.

HTTP DVR requests resolve the requested manifest before preparing a destination,
as live requests do. Query-carried viewer credentials remain attached to the
selected playback URL; header/cookie credentials are never converted into URLs.

Connected responses keep those two projections separate. `cluster_peers` is
health-filtered routing input; `authority_cluster_peers` contains the tenant
grants used to compare signed authority and authorize locally served stored
media. Promotion never
compares cell-local health, addresses, or object-storage endpoints as though
they were tenant policy. During a mixed-version rollout, absence of the
authority projection prevents promotion and leaves the connected path active.

## Scoring Algorithm

Foghorn ranks eligible nodes using a weighted scoring system. **Higher score = better node.**
This scorer ranks stored-media and source candidates and produces the candidate list a peer
receives from `QueryStream`. Live viewer and publisher destinations come from the placement
evaluator instead, which uses its own ordering (`pkg/placement`).

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

Public HTTP viewer coordinates come from a GeoIP MMDB lookup of the trusted client
identity (`GEOIP_MMDB_PATH`). Public `lat`/`lon` query parameters and coordinate/country
headers do not override that identity, including when a configured reverse proxy forwards
the request. Proxy trust authenticates the forwarding chain; it does not authenticate a
caller's chosen coordinates. Without a location, geo scoring is skipped.

Authenticated node-to-node source requests retain explicit coordinate hints for source
selection. Those values are bounded to finite latitude/longitude ranges. This is separate
from a public viewer asserting where they are. The gRPC path receives the viewer identity
through the authenticated Gateway/Commodore path rather than public HTTP headers.

Related source: `pkg/geoip`, `pkg/middleware/clientip.go`, and
`api_balancing/internal/handlers`.

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

When a local cluster doesn't have the requested content, Foghorn checks peer clusters before returning an error. This extends the single-cluster scoring with a two-phase remote lookup. It backs stored-media resolution, node-bound source lookup, and the candidates this cell returns to a querying peer. A live viewer's cross-cell destination comes from placement's own destination discovery, which observes peer cells directly rather than through this lookup.

Stream-specific advertisements also provide a warm registry view. Every five seconds,
the sender reports each node's own buffer state and original-versus-replicated input status;
an aggregate full stream cannot make a dry replica full. Confirmed push publishers include
their generation/revision, and each edge identifies its virtual media cluster independently
of the sending Foghorn cell. Both receive directions preserve these fields and RAM metrics
through a shared projection. Old peers without generation fields remain unbound. This warm
view is not a reservation or a policy capability; source admission still rechecks the publisher.

### Phase 1: Federated Locations (Cheap)

Peers push `StreamAdvertisement` every 5 seconds, and the StreamRegistry files each
one as a federated `Location` keyed by the sending control cell. That directory says
which cells hold a stream and on which edges, so Foghorn picks a candidate peer
without a per-request RPC. Every federated edge is gated on advertisement freshness,
so a cell that stops advertising disappears rather than going stale.

### Phase 2: QueryStream RPC (On Demand)

Foghorn confirms a candidate by sending `QueryStream` to that peer's
FoghornFederation service. On cold start, when no advertisement has arrived yet but
peers exist, Foghorn fans out `QueryStream` directly. The peer scores its local nodes
(using the same weight algorithm) and returns `EdgeCandidate` entries with DTSC URLs,
capacity data, and `IsOrigin` flags.

### Remote Edge Scoring

Remote candidate scoring applies CPU, RAM, bandwidth and viewer-distance weights. A
candidate receives no penalty for belonging to another Foghorn or cluster, and a
positive score is not discarded merely because it is below 200. With otherwise
equivalent facts, a US edge is geographically local to a US viewer even when an EU
Foghorn handles the request.

There is no separate remote scorer. The peer answering `QueryStream` runs its own
ordinary local scorer over its own nodes, against each node's native bandwidth
limit, and returns the result. Symmetric discovery, comparable capacity scoring and
exact-destination preparation are properties of the
[media placement path](media-placement-policy.md), which live routing uses instead
of this scorer.

### Decision: Always Origin-Pull

A viewer receives the destination selected by global serving policy, which may
belong to a different cluster from the Foghorn resolving the request. The
resolving cluster is not a geographic preference. For a live source on another
edge, placement arranges the DTSC origin pull into the selected serving edge;
the browser connects to that edge, not the ingest node. Stored media uses the
selected destination's artifact relay instead of an arranged live-origin pull.

See `docs/architecture/stream-replication-topology.md` for the full origin-pull lifecycle and loop prevention.

### Related Source Files

- Placement destination discovery: `api_balancing/internal/federation` (`placement_*_paths.go`)
- Federated stream directory: `api_balancing/internal/control` (`StreamRegistry`, `UpsertFederatedSource`)
- Federation client: `api_balancing/internal/federation` (`QueryStream` RPC)
- Origin-pull arrangement: `api_balancing/internal/federation` (`ArrangeOriginPull`)

## Routing Events (Analytics)

Every routing decision emits a `load_balancing` event to Kafka:

```go
// api_balancing/internal/handlers.RoutingEvent, converted to ipcpb.LoadBalancingData
type RoutingEvent struct {
    StreamName        string
    SelectedNode      string
    Score             uint64
    ClientLatitude    float64  // H3 centroid
    ClientLongitude   float64  // H3 centroid
    NodeLatitude      float64
    NodeLongitude     float64
    Status            string   // see below
    DurationMs        float32  // Decision latency
    ClusterID         string   // Emitting cluster context
    SelectedClusterID string   // Authenticated cluster of SelectedNode
    ControlCellID     string   // Foghorn/media-authority cell
    OriginClusterID   string   // Stream origin when known — a MEDIA cluster, never a cell
    RemoteClusterID   string   // Set on cross-cluster DTSC source resolution, not on viewer routing
}
```

Emitted `Status` values: `success`, `redirect` (intra-cluster, to a local node),
`failed`, `pull_federated`, `remote_source`, `pull_upstream`, `active_replication`.
`RemoteClusterID` is set only on `pull_federated` and `remote_source` — both are
Mist DTSC source resolution, not a viewer request, and neither is a redirect to
another cluster.

Stored in: `periscope.routing_decisions` (ClickHouse)

Tenant routing analytics require `stream_tenant_id` to equal the authenticated content tenant. If
stream ownership enrichment is missing, the row is excluded rather than falling back to the
infrastructure-owner `tenant_id`; this deliberately trades partial retained coverage for isolation.
Unattributed decisions expire under the routing table's 90-day TTL and are not backfilled because
routing decisions are temporary diagnostic telemetry rather than durable delivery facts.

`SelectedClusterID` is the placement answer: it identifies the cluster containing the node returned to the viewer. It is derived from authenticated node state, not from the Foghorn process. `OriginClusterID` answers where the content came from, while `ControlCellID` answers which control plane made the decision. `RemoteClusterID` records a cross-cluster target during federation. These fields are deliberately independent; missing values remain empty rather than being copied from one another.

## Ingest Routing

Publishers are routed by the same scorer, through a separate front door.

|                   | Viewers                                           | Publishers                         |
| ----------------- | ------------------------------------------------- | ---------------------------------- |
| Entry point       | `GET /play/{viewKey}`                             | `GET`/`POST` `/ingest/{streamKey}` |
| Capability filter | `edge`                                            | `ingest`                           |
| Redirect          | 307 per protocol                                  | 307 to WHIP (`POST` only)          |
| Fallback path     | cross-cluster origin-pull (never a peer redirect) | geo-DNS `edge-ingest` name         |

Both resolve in `api_balancing/internal/control` (`ResolvePreparedLiveViewerEndpoint` and
`ResolveIngestEndpoints`) and filter candidates by capability. Ingest then
keeps candidates inside the cluster-peer envelope Commodore returned with the
resolve — the same envelope playback authorizes cross-cluster candidates
against. It is Quartermaster's cluster↔tenant grant already narrowed by the
tenant's plan classes and by cluster health, so neither side re-derives
entitlement. `NodeState.TenantID` is ownership metadata, not routing authority.

One physical Foghorn serves many virtual media clusters and accepts any valid
stream key, so nothing about the listener, the hostname, or its own
`CLUSTER_ID` bounds the candidate set. It names no cluster on the resolve.

Resolved ingest URLs come from that node's current Mist listener report. RTMP/SRT
keep the reported ports and public overrides; Foghorn's fleet-wide port settings
do not manufacture node-specific endpoints. Missing protocols remain absent.
WHIP POST filters for a usable WHIP endpoint before truncating candidates, so
nearby RTMP-only nodes cannot hide an available WHIP node farther down the ranking.
Listener reports expire after 30 seconds independently of heartbeats and capacity
metrics. A successful empty report withdraws previously advertised listeners.

Mist can advertise WebRTC with its UDP port in a `ws` URL. WHIP signalling uses
the separately reported HTTP listener, including its public host, port and proxy
prefix. Helmsman configures its single managed HTTP `pubaddr` as a scalar because
Mist's metrics reporter serializes array-form public addresses into malformed URLs.
Protocol reconciliation repairs this form while preserving unrelated listener settings.
`MIST_CONTRACT_IMAGE=<immutable-image-id> make verify-mist-protocols` exercises this
contract on an isolated Mist container with nonstandard ports and listener withdrawal.

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

The `USER_NEW` hook checks policy before reserving viewer capacity, including cached public
playback, and denies when no admission adapter is installed. Its deadline-aware reservation checks
the coordination engine clock before mutating or renewing capacity; local reservations check under
the capacity lock. A successful reservation consumes the placement decision at that boundary. The
adapter derives object identity, source generation, protocol and geography server-side; it does not
treat a client URL as policy or source authority. Dispatch to the push, configured-input or
artifact source reader follows the signed object kind and ingest mode, so a runtime stream name
cannot select a different path; see [media placement policy](media-placement-policy.md) for the
per-kind checks and the attestation barrier that gates policy issuance.

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

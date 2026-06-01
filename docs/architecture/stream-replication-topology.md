# Stream Replication Topology - How Streams Reach Viewers

How live streams and artifacts replicate from origin to edges, both within a single cluster and across clusters. Foghorn orchestrates; MistServer executes the actual DTSC media transport.

## Architecture

```
Producer                            Cluster A                                Cluster B (peer)
   │
   │ RTMP/E-RTMP/SRT/WHIP push
   ▼
┌──────────┐
│ Edge A1  │ ← origin (Inputs > 0, Replicated = false)
│ MistServer│
└────┬─────┘
     │ DTSC pull
     ▼
┌──────────┐         ┌──────────┐                      ┌──────────┐
│ Edge A2  │         │ Edge A3  │                      │ Edge B1  │
│ MistServer│◄─DTSC──│ MistServer│                      │ MistServer│
│ (replica)│         │ (replica)│                      │ (replica)│
└──────────┘         └──────────┘                      └──────────┘
                                                            ▲
                                                            │ DTSC pull
                                     Foghorn B: arrangeOriginPull
                                     NotifyOriginPull → Foghorn A
                                     Foghorn A returns dtsc://A1:4200/...
```

## Service Responsibilities

| Component              | Role                                                                                                          | Data                                                                                                                                                          |
| ---------------------- | ------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| MistServer             | Media transport: receives push ingest, serves DTSC pulls, delivers to viewers (HLS/DASH/WebRTC)               | Raw media data; reports stream metrics via triggers                                                                                                           |
| Helmsman (api_sidecar) | MistServer control sidecar: forwards triggers to Foghorn, applies configuration                               | Trigger payloads, MistServer API                                                                                                                              |
| Foghorn                | Orchestrator: decides which node pulls from which source, builds DTSC URIs, tracks replication state          | StreamState, NodeState, StreamRegistry                                                                                                                        |
| Foghorn federation     | Cross-cluster: QueryStream for candidate discovery, NotifyOriginPull for handshake, PeerChannel for telemetry | StreamRegistry per-peer Locations (federated identity, replicating-now, outbound pullers); RemoteEdgeCache (edge telemetry, peer heartbeat, remote artifacts) |

## Data Flows

### Ingest: Producer → Origin Node

```
Producer pushes RTMP/E-RTMP/SRT/WHIP to edge-ingest.{cluster}.{base}:1935
  → DNS resolves to an edge node in the cluster
  → MistServer accepts the push
  → MistServer fires PUSH_REWRITE trigger → Helmsman → Foghorn
  → Foghorn: ValidateStreamKey via Commodore → gets tenant_id, stream_id
  → Foghorn: state.UpdateStreamInstanceInfo(stream, node, {Inputs: 1, Replicated: false})
  → Node is now the origin for this stream
```

**Origin identification**: A node is treated as an origin candidate when `StreamInstanceState.Inputs > 0` and `StreamInstanceState.Replicated == false`. Duplicate ingest protection is intended to keep one active origin per stream, but source selection still treats origin as state-derived rather than a separate topology record.

### Intra-Cluster Replication: Origin → Edges

Live stream replication within a cluster is demand-driven. Edges pull via DTSC only when viewers need the stream.

```
Viewer requests stream on Edge A2 (doesn't have it yet)
  → MistServer on A2: "I need this stream" → HTTP /?source=<stream> → Foghorn
  → Foghorn handleGetSource():
      1. GetBestNodeWithScore(stream, isSourceSelection=true)
         - Scans all nodes, finds A1 has Inputs > 0 and !Replicated
         - Rejects replicated nodes (rejectStreamReplicated)
      2. Build source response → "dtsc://edge-a1.cluster.base:4200"
      3. Returns DTSC URL to MistServer
  → MistServer on A2 opens DTSC connection to A1, begins pulling
  → A2 starts serving viewers; state updated: Replicated=true on A2
```

**Key path**: `handleGetSource` at `api_balancing/internal/handlers`. This is an HTTP endpoint, not a gRPC trigger. MistServer calls it directly when it needs to pull a stream.

**Two source resolution mechanisms**: MistServer has two ways to resolve sources — the HTTP `/?source=` load balancer endpoint (above) and the `STREAM_SOURCE` blocking trigger (below). `STREAM_SOURCE` is the primary entry for every stream type; the HTTP `/?source=` endpoint is the balancer-template fallback for cold cases that need geo-aware load balancing (mostly pull/native local-edge selection). See the "Source Resolution" sections for details.

### Cross-Cluster Replication: Origin-Pull

When a viewer's cluster doesn't have the stream, Foghorn orchestrates a cross-cluster DTSC pull.

```
Viewer → Foghorn A (stream not on any local edge)

1. Score local edges: no stream found
2. Check EdgeSummary from PeerChannel: Cluster B has edges with this stream
3. If no ActiveReplication exists for this stream:

   a. QueryStream → Foghorn B
      - B scores its local nodes with isSourceSelection for source, or all nodes for viewer
      - Returns EdgeCandidates with DTSC URLs, IsOrigin flags, capacity data

   b. Foghorn A: score remote candidates vs local edges
      - CrossClusterPenalty(200) applied to remote scores
      - If local edge has capacity, prefer origin-pull (serve locally)
      - If no local capacity, redirect viewer to remote cluster

   c. arrangeOriginPull():
      - Loop check: verify no circular replication via RemoteReplicationEntry
      - Select local edge with capacity
      - NotifyOriginPull → Foghorn B (stream, source_node, dest_cluster, dest_node)
      - Foghorn B validates, records the outbound pull on its StreamRegistry (Location[B-local].OutboundPullers), returns DTSC URL
      - Foghorn A calls StreamRegistry.MarkReplicating(internal_name, peer=B, pullDTSCURL, destNodeID, destNodeBaseURL, sourceNodeID) so /balance, /source, and `/debug/stream-registry` all see the in-flight pull on Location[A-local]
      - The SweepStaleLocations ticker (30s tick / 5-min maxAge) ages out the mark if the pull never completes — same expiry budget the old federation cache TTL used

4. MistServer on A2 pulls from B1 via DTSC over public internet
5. Stream appears in A's local state → `checkReplicationCompletion` calls StreamRegistry.ClearReplicating on A and ClearOutboundPull on B
6. ReplicationEvent broadcast to all peers: "stream now available on Cluster A"
7. Subsequent viewers served from local edge A2
```

### Source Selection: `handleGetSource`

MistServer asks Foghorn "where should I pull this stream from?" via HTTP `/?source=<stream>`.

| Step | Logic                                                                                                                                                                                                      | Fallback |
| ---- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------- |
| 1    | Active replication: `StreamRegistry.LocalReplication` — if an origin-pull is arranged from a peer, return the peer's DTSC URL                                                                              | → step 2 |
| 2    | Local origin: `GetBestNodeWithScore(isSourceSelection=true)` — finds node with `Inputs > 0`, `!Replicated`; the HTTP handler returns `dtsc://<host>:4200`                                                  | → step 3 |
| 3    | Cross-cluster: `resolveRemoteSource()` — looks up origin_cluster_id (from streamContext cache or Commodore), calls `QueryStream(is_source_selection=true)` on origin Foghorn, returns the peer's DTSC URL  | → step 4 |
| 4    | Terminal answer per stream type: `push://` for live+ (publishers boot ingest, viewers get clean OFFLINE via mist-side pre-check); `offline:<reason>` for pull+/native/vod/dvr/processing when not servable | —        |

### Source Resolution: `STREAM_SOURCE` trigger

`STREAM_SOURCE` is a general-purpose MistServer blocking trigger that fires when a stream's source setting is loaded — for any stream type (live, VOD, or otherwise). A non-empty response overrides the stream's source; an empty response tells MistServer to use its configured default. See [MistServer trigger docs](https://docs.mistserver.org/category/list-of-triggers/).

The trigger chain: MistServer → Helmsman webhook (`/webhooks/mist/stream_source`, blocking) → Helmsman parses and forwards via gRPC → Foghorn `processor.handleStreamSource()`.

Helmsman mostly acts as a passthrough: it parses the raw webhook body into protobuf and forwards to Foghorn. The current exception is `processing+` streams, where Helmsman can return a local rewritten HLS manifest before forwarding if an active processing job has already produced one. On abort or error, Helmsman returns empty string to MistServer (use default source).

Foghorn's handler routes by stream type. Every branch starts with
`federationOriginPullDTSC`, the shared hook that returns the peer DTSC
URL when an inbound origin-pull is arranged on this cluster. Only when
the federation hook misses does the branch run its type-specific
resolution.

```
Foghorn processor.handleStreamSource(trigger):

  federationOriginPullDTSC fast-path (live+, pull+, dvr+, bare native):
    → If StreamRegistry.LocalReplication has a PullDTSCURL → return peer DTSC

  If live+:
    → Empty response → MistServer uses the balance:<foghorn> template;
      /source resolves DTSC if any node has the input, push:// otherwise

  If pull+:
    → Empty response → balance: template → /source returns upstream URI
      (allowed cluster) or federation DTSC (non-allowed)

  If bare native (mist-native):
    → Federation hook above, then balance: template → /source local LB

  If dvr+:
    → Recording-node DTSC (intra-cluster) or local manifest if this node
      is the recording origin
    → If neither resolves locally AND federation cache has a peer
      cluster's `Location[peer].RecordingNodeID` for this stream's source,
      `tryArrangeDVRCrossCluster` calls `federation.DefaultArrange` to
      set up `dvr+<hash>` origin-pull from the peer's recording node
    → offline:not_recorded otherwise

  If vod+ / processing+:
    → Helmsman read-through relay URL (local-or-S3 transparent);
      offline:not_uploaded if the artifact doesn't exist
```

### Cross-Cluster Federation Coverage

Every cross-cluster stream-source resolution path is tracked and uses
the same handshake primitives. No type has an ad-hoc fast path that
bypasses `NotifyOriginPull` / `MarkReplicating` for live source streams
or `PrepareArtifact` for artifacts.

| Type          | Cross-cluster mechanism                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                          | Tracked via                                                                                           |
| ------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------- |
| `live+`       | gRPC viewer routing: `arrangeOriginPull`. HTTP `/source`: `arrangeRemoteOriginPullFromSource` (identifies caller via `state.NodeIDByClientIP`). Both call shared `federation.ArrangeOriginPull` → NotifyOriginPull + MarkReplicating.                                                                                                                                                                                                                                                                                                                                                                            | StreamRegistry `LocalReplication`; source-cluster `OutboundPullers`                                   |
| `pull+`       | Same shared helper. `handleGetPullSource` placement-fail path also federates so non-allowed clusters can serve viewers via DTSC from an allowed cluster.                                                                                                                                                                                                                                                                                                                                                                                                                                                         | Same registry tracking                                                                                |
| bare native   | Same shared helper via federation hook in STREAM_SOURCE.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         | Same registry tracking                                                                                |
| `dvr+`        | `StreamAdvertisement.dvr_recording_node_id` advertises the source-cluster recording node. Receiver's STREAM_SOURCE dvr+ → `tryArrangeDVRCrossCluster` → `federation.DefaultArrange` (sourceNode = peer's recording node, stream = `dvr+<hash>`).                                                                                                                                                                                                                                                                                                                                                                 | Same registry tracking — `Location[peer].RecordingNodeID` + `LocalReplication.PullDTSCURL`            |
| `vod+`        | Mist → Helmsman relay → RelayResolve (gRPC control message) → Foghorn. Foghorn checks local artifact, falls back to `ResolveCrossClusterArtifactURL` → `PrepareArtifact` against origin cluster. Origin returns either a presigned S3 URL (synced) or a peer-relay URL + opaque capability grant pointing at a local origin node that still holds the canonical file on disk (hot-but-unsynced; the origin Foghorn authorizes each pull online). Relay's block cache reads from either upstream transparently — the Authorization header (the grant id) is attached only for peer URLs. No bytes copied locally. | Adopted `foghorn.artifacts` row (storage_cluster_id=peer); RelayResolve refreshes URL on cache expiry |
| `processing+` | Same as vod+. Processing input reads cross-cluster artifacts through the same RelayResolve federation path.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      | Same                                                                                                  |
| Clips         | Same as vod+. A cross-cluster clip resolves through the front-door adopt path (STREAM_SOURCE / `/play` → `ResolveAndAdoptRemoteArtifact`) and is then served from the adopted row via RelayResolve federation — presigned S3 (synced) or peer-relay URL + grant (hot-but-unsynced). No pull/download/local copy.                                                                                                                                                                                                                                                                                                 | Adopted `foghorn.artifacts` row (nested clip route carries `stream_internal_name`)                    |

The relay's federation entry point lives in `control/relay_resolve.go`.
For an **adopted** vod/clip row (the front door wrote it before any byte
GET) that has no `s3_url`, the resolver first tries
`fillPeerRelayFromLocalOrigin` (a local origin node may hold the
canonical full file even though S3 sync is pending), then federates via
`PrepareArtifact` against the artifact's origin/storage cluster. Origin
returns whichever URL is authoritative for its current state — synced
rows mint presigned S3, hot-but-unsynced rows mint a node-specific
peer-relay URL + opaque capability grant (authorized online by the
origin Foghorn) without waiting on S3 sync.

RelayResolve deliberately does **not** federate-by-hash for a missing
vod/clip row: it has no requesting-tenant context to enforce the peer
allowlist, so resolution+authorization+adoption belong at the front door
(STREAM_SOURCE / `/play`), and a missing row means 404. The one
direct-dial (no pre-adopted row) federation is the **processing-input
upload** path: `fillCrossClusterArtifactFromCommodore` resolves the
uploaded source by hash via Commodore (`ResolveVodHash`), which returns
the tenant's cluster peers so the allowlist is enforced there before
`PrepareArtifact`.

## Origin Tracking

| Field                            | Location    | Meaning                                                                 |
| -------------------------------- | ----------- | ----------------------------------------------------------------------- |
| `StreamInstanceState.Inputs`     | `state`     | Number of active ingest inputs. `> 0` = origin                          |
| `StreamInstanceState.Replicated` | `state`     | `true` = pulling via DTSC (replica), `false` = receiving push (origin)  |
| `StreamState.NodeID`             | `state`     | Primary node for this stream (usually origin)                           |
| `EdgeCandidate.IsOrigin`         | `pkg/proto` | Set when `ss.Status == "live" && ss.Inputs > 0` in federation responses |

## Loop Prevention

Three layers prevent circular replication between clusters:

### Layer 1: Pre-Arrangement Check

Before calling `NotifyOriginPull`, check `RemoteReplicationEntry` records in Redis. If the target cluster is already replicating this stream from us, skip.

```
arrangeOriginPull():
  replications = cache.GetRemoteReplications(stream)
  for r in replications:
    if r.ClusterID == targetCluster → abort (would create loop)
```

### Layer 2: ReplicationEvent Broadcast

When `checkReplicationCompletion()` detects a stream is now live locally (pulled successfully), it:

1. Calls `StreamRegistry.ClearReplicating(internal_name)` on the local Location (drops ReplicatingFrom + PullDTSCURL + DestNodeID + DestNodeBaseURL + PullSourceNodeID)
2. Broadcasts `ReplicationEvent(available=true)` to all peers via PeerChannel
3. Peers store this in `remote_replications` — subsequent viewers at the peer can redirect to us instead of pulling again

### Layer 3: StreamAdvertisement Directory

`StreamAdvertisement` messages (pushed every 5s) build a local stream directory on each peer. Foghorn can check "does Cluster A already have this stream?" before even considering a QueryStream RPC.

## Topology Model

The topology is **implicit and dynamic** — there is no fixed origin/hub/edge hierarchy.

| Concept                   | Implementation                                                                                                  |
| ------------------------- | --------------------------------------------------------------------------------------------------------------- |
| **Origin**                | Whichever node first receives the producer's push. Identified by `Inputs > 0, Replicated = false`               |
| **Replica**               | Any node pulling via DTSC. Identified by `Replicated = true`                                                    |
| **Hub nodes**             | Not implemented. All replicas pull directly from origin (star topology within cluster)                          |
| **Cascade replication**   | Not implemented within a cluster. Cross-cluster uses a single hop (peer edge → local edge). No multi-hop chains |
| **max_replicas**          | Not enforced. Load balancer naturally limits by rejecting overloaded nodes (BW exhaustion, high CPU)            |
| **Region policies**       | Not implemented as explicit policy. Geo-aware scoring (`GEO_WEIGHT`) naturally creates regional affinity        |
| **Stream-level controls** | `federated` flag (true/false) controls cross-cluster visibility. No per-stream replication policy               |

### What this means in practice

- Within a cluster: star topology. Origin → N replicas, each pulling directly from origin.
- Across clusters: single-hop. Origin cluster → requesting cluster. No relay chains.
- Scaling: more viewers → Foghorn selects edges with capacity → MistServer pulls from origin → star widens.
- No proactive replication. All pulls are demand-driven (viewer or source request triggers them).

### Not yet implemented (from RFC)

- Hub-based inter-region replication (multi-hop cascade)
- `max_replicas_total`, `max_replicas_per_region` policy fields
- Explicit topology graph with observability
- Region metadata sourcing and validation

## Key Files

- `api_balancing/internal/handlers` - `handleGetSource`: live stream source selection (HTTP)
- `api_balancing/internal/handlers` - `resolveRemoteSource`: cross-cluster DTSC URL lookup
- `api_balancing/internal/handlers` - `arrangeOriginPull`: cross-cluster origin-pull lifecycle
- `api_sidecar/internal/handlers` - `HandleStreamSource`: Helmsman STREAM_SOURCE webhook handler (with `processing+` local manifest shortcut)
- `api_sidecar/internal/config` - STREAM_SOURCE trigger registration (`sync: true`, no stream filter)
- `api_balancing/internal/triggers` - `handleStreamSource`: Foghorn STREAM_SOURCE handler with shared `federationOriginPullDTSC` fast-path; resolves live/pull/native/dvr/vod/processing sources via per-prefix branches
- `api_balancing/internal/control` - `BuildDTSCURI`: uses the node's DTSC output template and appends whatever runtime name the caller passes (`live+<x>`, `pull+<x>`, `dvr+<x>`, or bare for mist-native) for federation/origin-pull URLs
- `api_balancing/internal/balancer` - `rateNodeWithReason`: `isSourceSelection` filtering, `rejectStreamReplicated`
- `api_balancing/internal/state` - `StreamInstanceState`: `Inputs`, `Replicated` fields
- `api_balancing/internal/federation` - `checkReplicationCompletion`: walks `StreamRegistry.AllLocalReplications()`, calls ClearReplicating + ClearOutboundPull, broadcasts ReplicationEvent
- `api_balancing/internal/control` - `StreamRegistry` per-stream `Locations[cluster].{ReplicatingFrom, PullDTSCURL, DestNodeID, DestNodeBaseURL, PullSourceNodeID, OutboundPullers}` — replaces the federation cache's per-stream `ActiveReplicationRecord` / `StreamAdRecord` / `PlaybackIndex` (deleted)
- `api_balancing/internal/control` - `SweepStaleLocations` (30s tick / 5-min maxAge) — ages out stale Locations + per-OutboundPull entries; replaces the federation cache's TTL-based expiry
- `api_balancing/internal/federation` - `RemoteEdgeCache` (still in use) — edge telemetry, peer heartbeat, remote-artifact locations, edge summary; stream identity and per-stream replication state moved to StreamRegistry
- `pkg/proto/ipc.proto` - `DVRUpdateSourceRequest` (control message 164) — Foghorn → Helmsman, refreshes the DVR storage node's source override when the publisher takes over to a different ingest node

## Gotchas

- **STREAM_SOURCE is a general MistServer trigger**. It fires when any stream's source setting is loaded — for every stream type. Helmsman forwards it to Foghorn except for the local `processing+` manifest shortcut. Foghorn's handler runs `federationOriginPullDTSC` first for live+/pull+/dvr+/native, returning the peer DTSC URL when an inbound origin-pull is arranged; otherwise per-prefix resolution runs. For live+ with no federation, the handler returns empty so MistServer uses the `balance:<foghorn>` template, which calls `/source` and gets back either DTSC (if any edge has the input) or `push://` (publisher safety net; viewers get a clean OFFLINE via the mist-side input-balancer pre-check).
- **DTSC port handling differs by path**. The HTTP `/?source=` handler returns `dtsc://<host>:4200` directly. Federation/origin-pull URLs use `BuildDTSCURI`, which derives the DTSC base from the node's advertised `DTSC` output template.
- **No cascade within a cluster**. If origin goes down, replicas lose their source. There's no automatic promotion of a replica to "relay" for other replicas.
- **The registry's replication mark bridges a timing gap**. Between `NotifyOriginPull` (arrangement) and the stream actually appearing in local state (MistServer pulls and reports metrics), `Location[local].ReplicatingFrom + PullDTSCURL + DestNodeID` on the StreamRegistry tells subsequent viewers "a pull is in progress, serve from expected local edge." `SweepStaleLocations` ages this out at the same 5-min budget the prior federation cache TTL used.
- **Sweeper preserves active state**. `SweepStaleLocations` evicts Locations only when SourceActive=false, OwnerNodeID empty, ReplicatingFrom empty, and OutboundPullers empty. A long-running publisher with no admission events still has its admission state retained — only explicit clearing edges (PUSH_INPUT_CLOSE, ClearReplicating, ClearOutboundPull) zero those fields, and only then can the sweeper claim the Location.
- **Registry hydration preserves Locations**. `store()` (called from `hydrate` on TTL refresh) merges identity into the existing cachedEntry rather than replacing it, so duplicate-ingest protection and origin-pull state survive cache refreshes.
- **Publisher takeover propagates to in-flight DVRs**. When `AdmitAndReserve` returns `AcceptTakeover` for a stream that has an active DVR row, Foghorn dispatches `DVRUpdateSourceRequest` to the DVR's storage node so its push override is refreshed to the new ingest node's DTSC URL. Without this, the recording's pull would keep targeting the now-empty old source until retry budget exhausted.
- **Drain on takeover uses `nuke_stream`, not `deletestream`**. `live+<x>` is a wildcard-derived runtime instance — `deletestream` only removes configured stream entries (`Controller::Storage["streams"]`), so it no-ops on the lingering runtime buffer. Helmsman's `handleDrainStream` calls `nuke_stream` after `StopSessions` to actually clear the buffer + viewer sessions.
- **Cross-cluster replication is over public internet**. DTSC between MistServer nodes on different clusters traverses the public internet. No WireGuard mesh for edges. TLS on Foghorn gRPC; DTSC itself is unencrypted (media-only, no auth data).

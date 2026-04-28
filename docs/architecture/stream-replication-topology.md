# Stream Replication Topology - How Streams Reach Viewers

How live streams and artifacts replicate from origin to edges, both within a single cluster and across clusters. Foghorn orchestrates; MistServer executes the actual DTSC media transport.

## Architecture

```
Producer                            Cluster A                                Cluster B (peer)
   │
   │ RTMP/SRT/WHIP push
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

| Component              | Role                                                                                                          | Data                                                |
| ---------------------- | ------------------------------------------------------------------------------------------------------------- | --------------------------------------------------- |
| MistServer             | Media transport: receives push ingest, serves DTSC pulls, delivers to viewers (HLS/DASH/WebRTC)               | Raw media data; reports stream metrics via triggers |
| Helmsman (api_sidecar) | MistServer control sidecar: forwards triggers to Foghorn, applies configuration                               | Trigger payloads, MistServer API                    |
| Foghorn                | Orchestrator: decides which node pulls from which source, builds DTSC URIs, tracks replication state          | StreamState, NodeState, ActiveReplication           |
| Foghorn federation     | Cross-cluster: QueryStream for candidate discovery, NotifyOriginPull for handshake, PeerChannel for telemetry | RemoteEdgeCache, ReplicationEvent                   |

## Data Flows

### Ingest: Producer → Origin Node

```
Producer pushes RTMP/SRT/WHIP to edge-ingest.{cluster}.{base}:1935
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

**Two source resolution mechanisms**: MistServer has two ways to resolve sources — the HTTP `/?source=` load balancer endpoint (above) and the `STREAM_SOURCE` blocking trigger (below). For live streams, the load balancer handles it. For VOD/artifacts, STREAM_SOURCE handles it. See the "Source Resolution" sections for details.

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
      - Foghorn B validates, stores ActiveReplicationRecord, returns DTSC URL
      - Foghorn A records the in-flight pull and returns a local endpoint; MistServer starts the DTSC pull when its playback/source path asks for the stream
      - Store local ActiveReplicationRecord in Redis (5-min TTL bridge)

4. MistServer on A2 pulls from B1 via DTSC over public internet
5. Stream appears in A's local state → checkReplicationCompletion clears ActiveReplication
6. ReplicationEvent broadcast to all peers: "stream now available on Cluster A"
7. Subsequent viewers served from local edge A2
```

### Source Selection: `handleGetSource`

MistServer asks Foghorn "where should I pull this stream from?" via HTTP `/?source=<stream>`.

| Step | Logic                                                                                                                                                                                                     | Fallback |
| ---- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------- |
| 1    | Local origin: `GetBestNodeWithScore(isSourceSelection=true)` — finds node with `Inputs > 0`, `!Replicated`; the HTTP handler returns `dtsc://<host>:4200`                                                 | → step 2 |
| 2    | Cross-cluster: `resolveRemoteSource()` — looks up origin_cluster_id (from streamContext cache or Commodore), calls `QueryStream(is_source_selection=true)` on origin Foghorn, returns the peer's DTSC URL | → step 3 |
| 3    | Fallback: `dtsc://localhost:4200` or the request's `fallback` query parameter — MistServer will accept a push or use local source                                                                         | —        |

### Source Resolution: `STREAM_SOURCE` trigger

`STREAM_SOURCE` is a general-purpose MistServer blocking trigger that fires when a stream's source setting is loaded — for any stream type (live, VOD, or otherwise). A non-empty response overrides the stream's source; an empty response tells MistServer to use its configured default. See [MistServer trigger docs](https://docs.mistserver.org/category/list-of-triggers/).

The trigger chain: MistServer → Helmsman webhook (`/webhooks/mist/stream_source`, blocking) → Helmsman parses and forwards via gRPC → Foghorn `processor.handleStreamSource()`.

Helmsman mostly acts as a passthrough: it parses the raw webhook body into protobuf and forwards to Foghorn. The current exception is `processing+` streams, where Helmsman can return a local rewritten HLS manifest before forwarding if an active processing job has already produced one. On abort or error, Helmsman returns empty string to MistServer (use default source).

Foghorn's handler routes by stream type:

```
Foghorn processor.handleStreamSource(trigger):

  If live stream (live+ prefix):
    → Abort (empty response → MistServer uses configured source / load balancer /?source= endpoint)

  If processing+:
    → Resolve to a local rewritten HLS manifest in Helmsman when present
    → Otherwise Foghorn resolves a presigned S3 GET URL for the process input artifact

  If VOD/artifact:
    → ResolveArtifactInternalName via Commodore → get artifact_hash, origin_cluster_id
    → Check local state: FindNodeByArtifactHash(hash)
      → If found locally: return file path on the storage node
    → If remote cluster has the artifact metadata: trigger async defrost and return empty so MistServer retries/defaults
    → Otherwise return empty
```

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

1. Deletes the `ActiveReplicationRecord`
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
- `api_balancing/internal/triggers` - `handleStreamSource`: Foghorn STREAM_SOURCE handler (skips live, resolves process/artifact sources)
- `api_balancing/internal/control` - `BuildDTSCURI`: uses the node's DTSC output template and appends `live+<stream>` for federation/origin-pull URLs
- `api_balancing/internal/balancer` - `rateNodeWithReason`: `isSourceSelection` filtering, `rejectStreamReplicated`
- `api_balancing/internal/state` - `StreamInstanceState`: `Inputs`, `Replicated` fields
- `api_balancing/internal/federation` - `checkReplicationCompletion`: clears ActiveReplication, broadcasts ReplicationEvent
- `api_balancing/internal/federation` - `ActiveReplicationRecord`, `RemoteReplicationEntry` with TTLs

## Gotchas

- **STREAM_SOURCE is a general MistServer trigger**. It fires when any stream's source setting is loaded — not just VOD. Helmsman forwards it to Foghorn except for the local `processing+` manifest shortcut. Foghorn's stream-source handler skips `live+` streams and resolves process/artifact sources. For live streams, MistServer falls back to its configured source (the load balancer's HTTP `/?source=` endpoint).
- **DTSC port handling differs by path**. The HTTP `/?source=` handler returns `dtsc://<host>:4200` directly. Federation/origin-pull URLs use `BuildDTSCURI`, which derives the DTSC base from the node's advertised `DTSC` output template.
- **No cascade within a cluster**. If origin goes down, replicas lose their source. There's no automatic promotion of a replica to "relay" for other replicas.
- **ActiveReplication bridges a timing gap**. Between `NotifyOriginPull` (arrangement) and the stream actually appearing in local state (MistServer pulls and reports metrics), `ActiveReplicationRecord` in Redis (5-min TTL) tells subsequent viewers "a pull is in progress, serve from expected local edge."
- **Cross-cluster replication is over public internet**. DTSC between MistServer nodes on different clusters traverses the public internet. No WireGuard mesh for edges. TLS on Foghorn gRPC; DTSC itself is unencrypted (media-only, no auth data).

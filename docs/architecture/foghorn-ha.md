# Foghorn HA - Redis State Externalization

Foghorn can run as multiple instances per cluster with Redis as the shared state backend. Local stream/node/artifact state mutations write through to Redis, and each instance maintains an in-memory cache synced via an ordered, replayable **changelog** (a Redis Stream per cache) for sub-millisecond local routing reads. Federation, relay ownership, startup rehydration, and peer-address lookup also use Redis directly.

Single-Foghorn cells have no Redis at all: the in-memory layer is the only layer, and all sync machinery stays behind `if redisStore != nil`.

## Architecture

```
                     ┌─────────────────────────────────────┐
                     │          foghorn-redis               │
                     │  (appendonly, shared by all instances)│
                     │                                      │
                     │  {cluster_id}:streams:*              │
                     │  {cluster_id}:nodes:*                │
                     │  {cluster_id}:artifacts:*            │
                     │  {cluster_id}:remote_edges:*         │
                     │  {cluster_id}:remote_artifacts:*     │
                     │  {cluster_id}:stream_ads:*           │
                     │  {cluster_id}:active_replications:*  │
                     │  {cluster_id}:leader:peer_manager    │
                     │                                      │
                     │  changelogs (Redis Streams):         │
                     │  {cluster_id}:state_changelog        │
                     │  {cluster_id}:registry_changelog     │
                     └────────┬─────────────┬───────────────┘
                              │             │
                    ┌─────────┴───┐   ┌─────┴───────────┐
                    │  Foghorn 1  │   │  Foghorn 2      │
                    │  (leader)   │   │  (replica)      │
                    │             │   │                  │
                    │  In-memory  │   │  In-memory      │
                    │  cache ◄────│───│──► cache         │
                    │  (changelog │   │  (changelog     │
                    │   replay)   │   │   replay)       │
                    └──────┬──────┘   └──────┬──────────┘
                           │                  │
                    ┌──────┴──────┐    ┌──────┴──────┐
                    │ Helmsman A1 │    │ Helmsman A2 │
                    │ Helmsman A2 │    │ Helmsman A3 │
                    └─────────────┘    └─────────────┘
```

## Service Responsibilities

| Component                   | Role                                                                                                                              | Data                                                                                                                                                                                                              |
| --------------------------- | --------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| StreamStateManager          | In-memory state + Redis write-through. Singleton accessed via `state.DefaultManager()`                                            | Stream states, node states, artifacts, viewer sessions                                                                                                                                                            |
| RedisStateStore             | Redis CRUD operations, changelog appender/reader (`pkg/redis.Changelog`)                                                          | All `{cluster_id}:*` keys + `{cluster_id}:state_changelog`                                                                                                                                                        |
| PeerManager leader election | Redis SET NX for `{cluster_id}:leader:peer_manager`                                                                               | Only leader runs PeerChannel connections                                                                                                                                                                          |
| RemoteEdgeCache             | Federation telemetry cache (Redis). Scope narrowed: stream identity / playback index / active-replication moved to StreamRegistry | `remote_edges`, `remote_replications`, `edge_summary`, `remote_live_streams`, `remote_artifacts`, `stream_peers`, `peer_heartbeat`                                                                                |
| StreamRegistry              | Unified per-stream identity + per-peer Locations + admission state. Redis-backed with cross-instance changelog replay             | `registry:source:{internal_name}`, `registry:artifact:{hash}` — federation-fed via UpsertFederatedSource; admission state via MarkSourceActive/Inactive; replication state via MarkReplicating/RecordOutboundPull |
| identity.Resolver           | Single front door for stream/artifact → tenant/cluster attribution; layered state → registry → Commodore (see below)              | Instance-local negative cache only; positive caching lives in the layers it consults                                                                                                                              |

## Data Flows

### Write Path

```
Helmsman heartbeat → Foghorn instance
  → StreamStateManager.UpdateNodeState(nodeID, state)
  → Write to in-memory map (immediate, sub-ms)
  → Write to Redis: SET {cluster_id}:nodes:{nodeID} → JSON
  → XADD {cluster_id}:state_changelog → StateChange entry (server-assigned ID)
  → Writer records the entry ID as the entity's watermark
  → All other instances XREAD the changelog in order → apply to their in-memory cache
```

State mutation helpers follow this pattern: update in-memory, write-through to Redis (the rehydration source), append to the changelog (the live sync transport).

Identity fields (`NodeID`, `TenantID`, `StreamID`, `PlaybackID`, node `ClusterID`) merge **monotonically** on every path — local writers, replicated changelog entries, and rehydration alike fill blanks but never replace a non-empty value with an empty one. A cold-enrichment write that produced an identity-less entry heals on the next identified event instead of being replicated cluster-wide.

### Read Path

```
Viewer request → Foghorn instance (any)
  → LoadBalancer.GetTopNodesWithScores()
  → Reads from in-memory StreamStateManager (sub-ms)
  → Scores nodes using CPU/RAM/BW/GEO weights
  → Returns ranked node list
```

The local viewer-routing hot path reads the in-memory cache. Redis is still read directly for startup rehydration, HA command relay ownership, and federation caches such as remote edge summaries and stream advertisements.

### Command Relay (HA Forwarding)

Each Helmsman's bidirectional control stream is pinned to one Foghorn instance. The in-memory connection registry (`registry.conns`) is local — a command targeting a node connected to another instance would silently fail with `ErrNotConnected`.

The command relay solves this: on `ErrNotConnected`, the sending instance looks up who owns the node's stream (from Redis) and forwards the command via gRPC.

```
Control-plane RPC → Foghorn 1
  → Send*(nodeID, cmd)
  → Try local registry → ErrNotConnected (node on Foghorn 2)
  → Redis GET {cluster_id}:conn_owner:{nodeID} → "foghorn-2|10.0.0.5:18019"
  → gRPC ForwardCommand to 10.0.0.5:18019
  → Foghorn 2: SendLocal*(nodeID, cmd) → stream.Send() → Helmsman
```

Loop prevention: the relay handler always calls `SendLocal*` (never `Send*`), so it cannot re-relay.

#### Affected Operations

All 11 push commands that depend on the specific Helmsman stream:

| Severity  | Operation                                       | Impact if unreachable            |
| --------- | ----------------------------------------------- | -------------------------------- |
| Critical  | SendStopSessions                                | Billing: sessions keep running   |
| Critical  | SendDVRStop                                     | Disk fills, recording won't stop |
| Critical  | PushOperationalMode                             | Node ignores drain/maintenance   |
| Critical  | SendConfigSeed (TLS)                            | Stale certs, eventual expiry     |
| Important | SendDVRStart, SendClipPull, SendDtshSyncRequest | Feature doesn't work             |
| Low       | SendClipDelete, SendDVRDelete, SendVodDelete    | Orphan cleanup retries later     |

#### Connection Ownership Keys

| Key                                | Value                                                      | TTL |
| ---------------------------------- | ---------------------------------------------------------- | --- |
| `{cluster_id}:conn_owner:{nodeID}` | `instanceID\|grpcAddr` (e.g., `foghorn-1\|10.0.0.5:18019`) | 60s |

Lifecycle: set on Helmsman connect, refreshed on every heartbeat, deleted on disconnect. If Foghorn crashes, keys expire within 60s.

#### Address Discovery

Foghorn publishes two addresses with different audiences:

- Quartermaster service registration advertises the internal Foghorn gRPC listener, normally the mesh address on `:18019`, for service-to-service control RPCs, federation, and gRPC health checks.
- Quartermaster token validation can also return the public edge listener, normally `foghorn.<cluster>.<root>:18029`, for edge bootstrap before the node has joined the mesh. Bridge may proxy only that `PreRegisterEdge` bootstrap RPC; tenant/media control flows use Commodore and the internal listener.
- HA relay ownership stores `FOGHORN_RELAY_ADVERTISE_ADDR` in Redis, normally the mesh host on `:18019`. Relay traffic uses the internal gRPC listener and internal CA identity `foghorn.internal`.

Provisioning should set `FOGHORN_RELAY_ADVERTISE_ADDR` from the node's mesh DNS or mesh IP. If it is absent, Foghorn falls back to `FOGHORN_RELAY_ADVERTISE_HOST`, then `FOGHORN_HOST`, then the external advertise host with the internal port.

#### Cross-Cluster Interaction

Federation commands (e.g., PrepareArtifact from Cluster A) land on a random Foghorn instance in Cluster B via DNS load balancing. If that instance doesn't hold the target node's stream, the relay transparently forwards to the correct instance within Cluster B. Federation callers are unaware of the relay hop.

### Delivery Semantics

A successful `Send*` return does not mean the Helmsman or the underlying MistServer received the command. `SendLocal*` (e.g., `SendLocalDVRStop` at `api_balancing/internal/control/server.go:1642`) returns whatever `c.stream.Send(msg)` returns — gRPC accepted the message into its send buffer. The bytes may still be lost if the underlying connection dies before flush.

The HA forward layer (`commandRelay.forward` at `server.go:539`) adds one more layer of confirmation: peer A returns `ForwardCommandResponse{Delivered: true}` (`api_balancing/internal/grpc/relay_server.go:108`) only if its `SendLocal*` dispatch succeeded. This still represents buffer-accept on the peer, not Mist confirmation.

Three patterns exist in the codebase for getting stronger guarantees. Each command-type should be classified into one of them.

#### Pattern 1 — Reverse-direction ack with `RequestId` correlation

The bidirectional control stream is already used for request/response on commands that return data. Helmsman issues the controller call, then sends a response message back through the same stream with the original `RequestId` (`pb.ControlMessage` payload variants). Foghorn waits on a per-`RequestId` channel.

Current users:

- `ValidateEdgeToken` — response at `server.go:2679`
- `EdgeMistAdminSession` — response at `server.go:2810`
- `ThumbnailUpload` — response at `server.go:5493`

Most other commands carry a `RequestId` field (see `RelayRequestID` at `server.go:665`) but no response handler exists for them; the field is used only for logs.

#### Pattern 2 — Intent in Redis with atomic consume

Desired action is written to a Redis key with TTL. The instance that observes the completion event consumes it atomically. Any instance can write, any instance can consume; no in-process registry is involved.

Current users:

- Pending DVR stop: `{cluster_id}:pending_dvr_stop:{internal_name}` with TTL `pendingDVRStopTTL = 30 * time.Minute` (`api_balancing/internal/state/redis_store.go:17`); consumed via the `getAndDelete` Lua script (`redis_store.go:32-38`) when `StartDVR` arrives.
- Origin-pull arbitration: `{cluster_id}:origin_pull_lock:{stream_name}` via `SET NX EX` with the holder's `instanceID`; released via owner-checked Lua (`releaseLeaseScript`, `redis_store.go:48-54`).

#### Pattern 3 — Periodic reconcile loop

A loop on each instance re-derives desired state from authoritative storage and re-sends to current `conn_owner`. Transient failures heal on the next tick without per-command retry logic.

Current users:

- TLS certificate distribution: `StartCertRefreshLoop` (`server.go:5915`) re-issues `ConfigSeed` to every node every interval; a single drop is healed on the next tick.

#### Known Delivery Gaps

These are control commands where the current "fire once, return buffer-accept" semantic is the only convergence mechanism. They are tracked here as backlog; they are not addressed in code yet.

| Command                                         | Current semantic                                         | Why a single drop matters                                                                                              | Proposed pattern                                                                                                                                                           |
| ----------------------------------------------- | -------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `commandRelay.forward` stale-owner path         | Evict and return error (`server.go:580-583, 595-598`)    | During connection failover the new owner has already written its key; the caller never sees it                         | One re-`GetConnOwner` and retry after `evictStale()`                                                                                                                       |
| `SendStopSessions` (tenant kill)                | Per-node loop, log on error, continue                    | No observer event corresponds to "sessions stopped"; tenant keeps streaming through delinquency                        | Pattern 1: Helmsman acks with terminated count                                                                                                                             |
| `SendDVRStop` on a running stream               | One-shot send via relay (`server.go:1656-1674`)          | Disk continues filling until the publisher disconnects                                                                 | Pattern 2: write `{cluster_id}:dvr_stop_intent:{internal_name}:{dvr_hash}` with TTL; `RECORDING_END` handler does GETDEL; sweep retries unmatched intents older than grace |
| `ActivatePushTargets` / `DeactivatePushTargets` | One-shot send via relay                                  | Desired-state by nature; a drop leaves the node in the opposite state until the next user action triggers a fresh send | Pattern 3: per-node reconcile against the desired push-target set                                                                                                          |
| `ApplyManagedStream` / `RetractManagedStream`   | One-shot send via relay                                  | Same as above; desired-state managed-stream configs                                                                    | Pattern 3: per-node reconcile against the desired managed-stream set                                                                                                       |
| `ClipDelete` / `DvrDelete` / `VodDelete`        | One-shot send; populates `RequestId` that is never acked | Orphan cleanup is the failure mode; eventually retried by cleanup loops                                                | Drop the unused `RequestId` for explicitness, or wire pattern 1 if cleanup confirmation becomes load-bearing                                                               |

Send-buffer race (`c.stream.Send` returns nil, TCP dies before flush) is only fully solved by an end-to-end ack — pattern 1 for commands that need it. For pattern-2 and pattern-3 commands the race is benign: the observer event or the next reconcile tick re-converges. gRPC keepalive eventually marks the dead stream so future sends to that node go through the relay path with a fresh `conn_owner`.

### Rehydration on Startup (consistent cut)

```
Foghorn instance starts
  → Connects to Redis
  → Capture changelog tail ID (XREVRANGE ... COUNT 1)     ← FIRST
  → SCAN {cluster_id}:streams:* → bulk load into in-memory map
  → SCAN {cluster_id}:nodes:* → bulk load into in-memory map
  → SCAN {cluster_id}:artifacts:* → bulk load into in-memory map
  → XREAD {cluster_id}:state_changelog from the captured tail  ← replay
  → Ready to serve (in-memory cache fully populated)
```

Capture-then-load makes snapshot + replay a **consistent cut**: a change appended before the capture is fully reflected in the write-through keys the SCAN loads; a change appended after it is replayed from the log. Nothing can fall between the snapshot and the live sync (the startup lost-update window the old pub/sub transport had).

Merge-not-replace: rehydration merges Redis data with any state already received from Helmsman heartbeats during startup, with identity fields merged ignore-empty. Score recomputation runs after deserialization.

### Ordering and replay semantics

The changelog (one Redis Stream per cache, `pkg/redis.Changelog`) replaces the earlier fire-and-forget pub/sub fanout, which delivered at-most-once with no ordering — a restarting instance silently missed changes forever, and two instances writing the same entity raced on arrival order. The previous mitigation compared cross-machine wall clocks, which broke under clock skew and after restarts.

With the log, ordering and replay come from the data structure itself:

- **Entry IDs are the versions.** Redis assigns each appended entry a monotonically increasing ID (`<ms>-<seq>`). Comparing two IDs orders the writes they carry with no reference to any wall clock, regardless of which instance produced them.
- **Per-entity watermarks.** Each instance tracks, per entity (`pkg/redis.Watermarks`), the highest entry ID it has published (its own writes) or applied (peer writes). The apply path skips any entry at or below the watermark — so a peer change that was logged before a later local write can never roll it back, and replayed entries apply idempotently.
- **Deletes are ordered like everything else.** A delete that reaches the apply path is by construction newer than anything local; a stale delete is dropped by the watermark. No wall-clock tombstone guards remain.
- **Self-originated entries** are skipped by `InstanceID` and only advance the watermark.
- **Bounded retention, gap-checked.** Streams are trimmed (`XADD MAXLEN ~ 100000`). A _restarting_ instance is always correct regardless of downtime: the write-through keys are the rehydration source and the consistent cut re-anchors replay at the current tail. A _live_ reader can fall behind retention (partitioned from Redis while peers keep appending); after any read failure the reader compares its cursor against the oldest retained entry and, on a gap, re-runs the consistent cut (capture tail → reload keys → resume) instead of silently skipping the trimmed range.

The registry's per-Location CRDT merge is unchanged: `Locations` is per-cluster state with one authoritative writer per cluster, so incoming snapshots still merge Location-by-Location (newest `UpdatedAt` wins, locally-known Locations preserved) — that is about merge meaning, not transport ordering.

Acceptance specs live in `api_balancing/internal/state/ha_ordering_spec_test.go` (stale snapshot / stale delete) and `api_balancing/internal/control/ha_ordering_spec_test.go` (clock-skewed delete / post-restart delete), plus the two-instance replay tests in `api_balancing/internal/state/changelog_sync_test.go`.

### Identity resolution facade

`api_balancing/internal/identity` is the single front door for "who does this stream/artifact belong to, and where does it live". Every trigger handler, gRPC surface, and federation path resolves through it instead of hand-rolling a lookup chain, so a consumer reading only a cold layer (the bug class behind empty-tenant/cluster attribution on HA replicas) can only be fixed once, centrally.

Resolution layers, in order, each filling blanks monotonically (never erasing earlier layers):

| Kind     | Chain                                                                                                          |
| -------- | -------------------------------------------------------------------------------------------------------------- |
| stream   | in-memory state union (serving NodeID + its cluster) → stream registry (which hydrates from Commodore on miss) |
| artifact | stream registry (cache → `foghorn.artifacts` / processing jobs SQL) → Commodore `Resolve*Hash`                 |

Only an authoritative not-found (the system of record answered "does not exist", `identity.ErrNotFound` from the adapters) is negative-cached, briefly (30s), so an unknown name arriving with every Mist trigger can't become a Commodore RPC firehose. Transient failures — RPC errors, DB outages, context expiry — are never cached: a dependency flap must retry on the next trigger, not harden into a 30s hard unknown for freeze/mint/thumbnails. Per-layer consults are counted in `foghorn_identity_resolutions_total{kind,layer,outcome}` so the next siloing bug shows up on a dashboard.

Wiring lives in `cmd/foghorn/main.go` (`identity.SetDefault`); the facade itself imports no other internal package, with layers injected as narrow adapters — it works in every deployment shape, including registry-less tests and single-instance cells.

## Key Schema

### Local State (StreamStateManager)

| Key Pattern                                             | Value                                                                         | TTL                  |
| ------------------------------------------------------- | ----------------------------------------------------------------------------- | -------------------- |
| `{cluster_id}:streams:{stream_name}`                    | JSON: StreamState (node_id, tenant_id, status, tracks, viewers, buffer_state) | None (authoritative) |
| `{cluster_id}:stream_instances:{stream_name}:{node_id}` | JSON: StreamInstanceState (per-node stream data)                              | None                 |
| `{cluster_id}:nodes:{node_id}`                          | JSON: NodeState (base_url, geo, cpu, ram, bw, artifacts)                      | None                 |
| `{cluster_id}:artifacts:{node_id}`                      | JSON: list of artifacts stored on that node                                   | None                 |
| `{cluster_id}:conn_owner:{node_id}`                     | String: `instanceID\|grpcAddr`                                                | 60s                  |

### Federation Telemetry (RemoteEdgeCache)

| Key Pattern                                                     | Value                                                   | TTL |
| --------------------------------------------------------------- | ------------------------------------------------------- | --- |
| `{cluster_id}:remote_edges:{peer_cluster}:{node_id}`            | JSON: EdgeTelemetry (BW, CPU, RAM, geo)                 | 30s |
| `{cluster_id}:remote_replications:{stream_name}:{peer_cluster}` | JSON: ReplicationEvent (available, DTSC URL)            | 5m  |
| `{cluster_id}:edge_summary:{peer_cluster}`                      | JSON: EdgeSummaryRecord (smoothed per-edge data)        | 60s |
| `{cluster_id}:remote_live_streams:{tenant_id}:{internal_name}`  | JSON: RemoteLiveStreamEntry                             | 30s |
| `{cluster_id}:remote_artifacts:{peer}:{artifact_hash}:{node}`   | JSON: RemoteArtifactEntry                               | 90s |
| `{cluster_id}:stream_peers:{peer_cluster}`                      | JSON: active stream names for a stream-scoped peer      | 60s |
| `{cluster_id}:leader:{role}`                                    | String: instance_id                                     | 15s |
| `{cluster_id}:peer_addresses`                                   | Hash: cluster_id → addr                                 | 30s |
| `{cluster_id}:peer_heartbeat:{peer_cluster}`                    | JSON: PeerHeartbeatRecord (version, streams, BW, edges) | 30s |

### Stream Registry (control.StreamRegistry)

Per-stream identity + per-peer Locations + admission state. Replaces the federation cache's per-stream entries (StreamAdRecord, PlaybackIndex, ActiveReplicationRecord — all deleted). Expiry is operational, not TTL-keyed: `SweepStaleLocations` runs every 30s and ages out Locations whose `UpdatedAt` is older than `maxAge` (default 5m), plus per-OutboundPull entries by their `CreatedAt`.

| Key Pattern                                    | Value                                                                                                              |
| ---------------------------------------------- | ------------------------------------------------------------------------------------------------------------------ |
| `{cluster_id}:registry:source:{internal_name}` | JSON: StreamEntry (TenantID, PlaybackID, IngestMode, RuntimeName, OriginClusterID, Locations[cluster_id]→Location) |
| `{cluster_id}:registry:artifact:{hash}`        | JSON: ArtifactEntry (Kind, InternalName, StreamID, TenantID, Status, RuntimeName, OriginClusterID, StorageCluster) |

Location fields (per cluster, per stream):

- **Federated (peer cluster)**: `IsOrigin`, `IsLiveNow`, `EdgeCandidates`, `AdTimestamp`
- **Local (this cluster)**: `IsOrigin`, `IsLiveNow`, `SourceNodes`, `SourceActive`, `SourceInactiveAt`, `OwnerNodeID` (admission), `ReplicatingFrom` + `PullDTSCURL` + `DestNodeID` + `DestNodeBaseURL` + `PullSourceNodeID` (dest-side pull), `OutboundPullers[]` (source-side pulls)

### Changelog Streams

- `{cluster_id}:state_changelog` — StreamStateManager `StateChange` entries (entity, operation, key, full payload). Receivers apply in log order, gated by per-entity watermarks; self-published entries are skipped by `instance_id`.
- `{cluster_id}:registry_changelog` — StreamRegistry `RegistryChange` entries, same semantics. Sources still merge per-Location on apply.

Both are normal persistent keys (trimmed with `MAXLEN ~ 100000`), so they survive Sentinel failover like the state keys do.

## docker-compose Topology

```yaml
# foghorn-redis: shared state backend for all Foghorn instances
foghorn-redis:
  image: valkey/valkey:8.1-alpine
  command: valkey-server --appendonly yes

# foghorn (instance 1): FOGHORN_INSTANCE_ID=foghorn-1
foghorn:
  environment:
    CLUSTER_ID: ${CLUSTER_ID}
    REDIS_URL: redis://foghorn-redis:6379
    FOGHORN_INSTANCE_ID: foghorn-1
    FOGHORN_INTERNAL_GRPC_BIND_ADDR: 0.0.0.0:18019
    FOGHORN_EXTERNAL_GRPC_BIND_ADDR: 0.0.0.0:18029
    FOGHORN_EXTERNAL_GRPC_PORT: 18029
    FOGHORN_RELAY_ADVERTISE_ADDR: foghorn:18019
    NODE_ID: regional-1 # used by BootstrapService for service registration
  ports: [18008, 18019, 18029]

# foghorn-2 (instance 2): FOGHORN_INSTANCE_ID=foghorn-2
foghorn-2:
  environment:
    CLUSTER_ID: ${CLUSTER_ID}
    REDIS_URL: redis://foghorn-redis:6379
    FOGHORN_INSTANCE_ID: foghorn-2
    FOGHORN_INTERNAL_GRPC_BIND_ADDR: 0.0.0.0:18019
    FOGHORN_EXTERNAL_GRPC_BIND_ADDR: 0.0.0.0:18029
    FOGHORN_EXTERNAL_GRPC_PORT: 18029
    FOGHORN_RELAY_ADVERTISE_ADDR: foghorn-2:18019
    NODE_ID: regional-2
  ports: [18018, "18039:18029"]
```

Both instances register independently with Quartermaster via `BootstrapService` using the internal gRPC port for service-to-service discovery. HA relay addressing is not taken from Quartermaster; it is the internal `FOGHORN_RELAY_ADVERTISE_ADDR` value stored in Redis connection ownership. An upstream load balancer (Nginx, Caddy, or cloud LB) distributes public HTTP and public edge-control requests across instances.

### Local HA Relay Validation Matrix

Use this matrix when validating relay behavior locally with `docker-compose`.

| Scenario                              | Setup                                                                            | Expected result                                                                              |
| ------------------------------------- | -------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------- |
| Local ownership                       | Helmsman stream connected to same Foghorn instance handling RPC                  | Command is delivered via `SendLocal*`; no relay hop                                          |
| Mixed ownership (same cluster)        | RPC lands on `foghorn-1`, node stream owned by `foghorn-2` in Redis `conn_owner` | `foghorn-1` relays via `ForwardCommand`; `foghorn-2` delivers locally                        |
| Stale ownership drift                 | Local stream exists but `stream.Send` fails (transport error)                    | Command returns local error and **must not relay**                                           |
| Cross-cluster remote artifact command | Remote peer receives command for artifact it does not own                        | Peer returns `handled=false`; caller continues trying other peers                            |
| Cross-cluster mixed ownership         | Remote command lands on non-owner Foghorn instance in destination cluster        | Destination cluster uses intra-cluster relay and returns `handled=true` to federation caller |

Deterministic automated coverage:

- `api_balancing/internal/control`
  - `TestSendWithRelay_LocalSuccess`
  - `TestSendWithRelay_LocalFailRelay`
  - `TestSendWithRelay_LocalSendErrorDoesNotRelay`
- `api_balancing/internal/grpc`
  - `TestForwardCommand_AllCommandTypes`
  - `TestForwardCommand_NodeNotConnected`
- `api_balancing/internal/grpc`
  - `TestForwardArtifact_NoPeerHandles`
  - `TestForwardArtifact_PeerError_ContinuesToNext`

Manual smoke check with compose topology:

1. `docker compose up -d foghorn foghorn-2 foghorn-redis`
2. Ensure both relay endpoints are healthy: `curl -sf http://localhost:18008/health` and `curl -sf http://localhost:18028/health`
3. Verify ownership keys use distinct instance IDs:
   - `redis-cli GET '{<cluster_id>}:conn_owner:<node_id>'`
   - Expect `foghorn-1|...` or `foghorn-2|...`, never blank instance IDs.

## Key Files

- `api_balancing/internal/state` - StreamStateManager: in-memory state + Redis write-through
- `api_balancing/cmd/foghorn` - Wiring: Redis connection, CLUSTER_ID, FOGHORN_INSTANCE_ID
- `api_balancing/internal/control` - CommandRelay, Send*/SendLocal* wrappers, connection lifecycle hooks
- `api_balancing/internal/grpc` - FoghornRelay gRPC handler (dispatches to SendLocal\*)
- `api_balancing/internal/federation` - RemoteEdgeCache: federation telemetry cache + leader lease
- `api_balancing/internal/control` - StreamRegistry: per-stream identity + per-peer Locations + admission/replication state, with Redis backing (RedisRegistryStore) and cross-instance changelog replay
- `api_balancing/internal/identity` - identity resolver facade (state → registry → Commodore), wired in `cmd/foghorn/main.go`
- `pkg/redis/changelog.go` - Changelog (Redis Stream append/tail/read) + Watermarks (per-key ordering)
- `pkg/proto` - FoghornRelay service definition
- dev compose configuration - foghorn + foghorn-2 + foghorn-redis topology

## Redis Topology

Foghorn's Redis client uses `go-redis/v9` `UniversalClient`, which supports three deployment topologies selected at runtime via environment variables. All key patterns use `{cluster_id}:` hash tags, ensuring Lua scripts and multi-key operations (SCAN + MGET) remain slot-safe across all topologies.

### Single Node (development default)

```
REDIS_URL=redis://foghorn-redis:6379
```

No `REDIS_MODE` set — falls back to `NewClientFromURL`. This is the docker-compose default.

### Sentinel (production recommended)

```
REDIS_MODE=sentinel
REDIS_ADDRS=sentinel-1:26379,sentinel-2:26379,sentinel-3:26379
REDIS_MASTER_NAME=foghorn-master
REDIS_USERNAME=foghorn-cluster-abc    # optional, Redis ACL
REDIS_PASSWORD=secret                 # optional
```

Eliminates Redis as a single point of failure. Automatic failover in ~15-30s.

```
          sentinel-1    sentinel-2    sentinel-3
              │              │              │
              └──────┬───────┘──────┬───────┘
                     │              │
               ┌─────┴─────┐ ┌─────┴─────┐
               │   Master   │ │  Replica   │
               │ (writes)   │ │ (standby)  │
               └────────────┘ └────────────┘
                     │              │
              ┌──────┴──────┬───────┴──────┐
              │ Foghorn 1   │  Foghorn 2   │
              │ (leader)    │  (replica)   │
              └─────────────┘──────────────┘
```

### Cluster (future: co-located multi-cluster at scale)

```
REDIS_MODE=cluster
REDIS_ADDRS=node-1:6379,node-2:6379,node-3:6379
REDIS_USERNAME=foghorn-cluster-abc
REDIS_PASSWORD=secret
```

For operators running many foghorn clusters in the same region on a shared Redis Cluster. Each cluster's data lands on one shard (via `{cluster_id}:` hash tags). Per-shard replicas provide HA. Use Redis ACLs to isolate clusters: `ACL SETUSER foghorn-abc on >pass ~{abc}:* &foghorn:{abc}:* +@all`.

Redis Cluster is designed for single-region deployments (<10ms inter-node latency). Cross-region state distribution is handled by the federation gRPC layer, not Redis topology.

## Gotchas

- **No short TTL on authoritative state**: Stream and node state in Redis has no TTL. Convergence is handled by heartbeat freshness and explicit lifecycle updates (stream offline, node disconnect). Federation state has TTLs because it originates from remote clusters.
- **FOGHORN_INSTANCE_ID is required per instance**: Used for leader election. If two instances share the same ID, leader lease contention will cause PeerChannel flapping.
- **Marshal-under-lock**: State mutations hold the write lock through JSON marshaling and Redis SET to prevent concurrent reads from seeing partial state.
- **Merge-not-replace rehydration**: On startup, Redis data is merged with any state already received from Helmsman heartbeats. This prevents a slow Redis SCAN from overwriting fresher data.
- **Score recomputation after deserialization**: Rehydrated NodeState objects need their composite scores recomputed (scores aren't persisted; they're derived from CPU/RAM/BW/GEO).

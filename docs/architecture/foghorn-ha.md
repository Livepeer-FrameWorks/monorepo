# Foghorn HA - Redis State Externalization

Foghorn runs as multiple instances per cluster with Redis as the shared state backend. All state mutations write through to Redis; each instance maintains an in-memory cache synced via pub/sub for sub-millisecond read performance.

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
                     │  {cluster_id}:active_replications:*  │
                     │  {cluster_id}:leader:peer_manager    │
                     │                                      │
                     │  pub/sub: foghorn:{cluster_id}:*     │
                     └────────┬─────────────┬───────────────┘
                              │             │
                    ┌─────────┴───┐   ┌─────┴───────────┐
                    │  Foghorn 1  │   │  Foghorn 2      │
                    │  (leader)   │   │  (replica)      │
                    │             │   │                  │
                    │  In-memory  │   │  In-memory      │
                    │  cache ◄────│───│──► cache         │
                    │  (pub/sub   │   │  (pub/sub       │
                    │   sync)     │   │   sync)         │
                    └──────┬──────┘   └──────┬──────────┘
                           │                  │
                    ┌──────┴──────┐    ┌──────┴──────┐
                    │ Helmsman A1 │    │ Helmsman A2 │
                    │ Helmsman A2 │    │ Helmsman A3 │
                    └─────────────┘    └─────────────┘
```

## Service Responsibilities

| Component                   | Role                                                                                   | Data                                                                                       |
| --------------------------- | -------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------ |
| StreamStateManager          | In-memory state + Redis write-through. Singleton accessed via `state.DefaultManager()` | Stream states, node states, artifacts, viewer sessions                                     |
| RedisStateStore             | Redis CRUD operations, pub/sub publisher                                               | All `{cluster_id}:*` keys                                                                  |
| PeerManager leader election | Redis SET NX for `{cluster_id}:leader:peer_manager`                                    | Only leader runs PeerChannel connections                                                   |
| RemoteEdgeCache             | Federation-specific Redis cache (separate key namespace)                               | `remote_edges`, `remote_replications`, `active_replications`, `edge_summary`, `stream_ads` |

## Data Flows

### Write Path

```
Helmsman heartbeat → Foghorn instance
  → StreamStateManager.UpdateNodeState(nodeID, state)
  → Write to in-memory map (immediate, sub-ms)
  → Write to Redis: SET {cluster_id}:nodes:{nodeID} → JSON
  → Publish to Redis: foghorn:{cluster_id}:state_updates → {type: "node", id: nodeID}
  → All other instances receive pub/sub → update their in-memory cache
```

All 10 state mutation methods follow this pattern: update in-memory, write-through to Redis, publish notification.

### Read Path

```
Viewer request → Foghorn instance (any)
  → LoadBalancer.GetTopNodesWithScores()
  → Reads from in-memory StreamStateManager (sub-ms)
  → Scores nodes using CPU/RAM/BW/GEO weights
  → Returns ranked node list
```

Reads never hit Redis directly. The in-memory cache is kept fresh by pub/sub sync from other instances' writes.

### Rehydration on Startup

```
Foghorn instance starts
  → Connects to Redis
  → SCAN {cluster_id}:streams:* → bulk load into in-memory map
  → SCAN {cluster_id}:nodes:* → bulk load into in-memory map
  → SCAN {cluster_id}:artifacts:* → bulk load into in-memory map
  → Subscribe to foghorn:{cluster_id}:state_updates
  → Ready to serve (in-memory cache fully populated)
```

Merge-not-replace: rehydration merges Redis data with any state already received from Helmsman heartbeats during startup. Score recomputation runs after deserialization.

## Key Schema

### Local State (StreamStateManager)

| Key Pattern                                             | Value                                                                         | TTL                  |
| ------------------------------------------------------- | ----------------------------------------------------------------------------- | -------------------- |
| `{cluster_id}:streams:{stream_name}`                    | JSON: StreamState (node_id, tenant_id, status, tracks, viewers, buffer_state) | None (authoritative) |
| `{cluster_id}:stream_instances:{stream_name}:{node_id}` | JSON: StreamInstanceState (per-node stream data)                              | None                 |
| `{cluster_id}:nodes:{node_id}`                          | JSON: NodeState (base_url, geo, cpu, ram, bw, artifacts)                      | None                 |
| `{cluster_id}:artifacts:{content_id}`                   | JSON: ArtifactState (node_id, size, type, cached_at)                          | None                 |

### Federation State (RemoteEdgeCache)

| Key Pattern                                                     | Value                                                   | TTL |
| --------------------------------------------------------------- | ------------------------------------------------------- | --- |
| `{cluster_id}:remote_edges:{peer_cluster}:{node_id}`            | JSON: EdgeTelemetry (BW, CPU, RAM, geo)                 | 30s |
| `{cluster_id}:remote_replications:{stream_name}:{peer_cluster}` | JSON: ReplicationEvent (available, DTSC URL)            | 5m  |
| `{cluster_id}:active_replications:{stream_name}`                | JSON: ActiveReplicationRecord (source, dest, DTSC URL)  | 5m  |
| `{cluster_id}:edge_summary:{peer_cluster}`                      | JSON: EdgeSummaryRecord (smoothed per-edge data)        | 60s |
| `{cluster_id}:stream_ad:{peer_cluster}:{internal_name}`         | JSON: StreamAdRecord (edges, playback_id, origin)       | 15s |
| `{cluster_id}:playback_index:{playback_id}`                     | String: internal_name                                   | 30s |
| `{cluster_id}:leader:{role}`                                    | String: instance_id                                     | 15s |
| `{cluster_id}:peer_addresses`                                   | Hash: cluster_id → addr                                 | 30s |
| `{cluster_id}:remote_live:{internal_name}`                      | JSON: RemoteLiveStreamEntry (cluster_id, tenant_id)     | 30s |
| `{cluster_id}:peer_heartbeat:{peer_cluster}`                    | JSON: PeerHeartbeatRecord (version, streams, BW, edges) | 30s |

### Pub/Sub Channel

`foghorn:{cluster_id}:state_updates` — JSON notification with `{type, id}` fields. Receivers fetch the updated key from their local Redis connection.

## docker-compose Topology

```yaml
# foghorn-redis: shared state backend for all Foghorn instances
foghorn-redis:
  image: redis:7-alpine
  command: redis-server --appendonly yes

# foghorn (instance 1): FOGHORN_INSTANCE_ID=foghorn-1
foghorn:
  environment:
    CLUSTER_ID: ${CLUSTER_ID}
    REDIS_URL: redis://foghorn-redis:6379
    FOGHORN_INSTANCE_ID: foghorn-1
  ports: [18008, 18019]

# foghorn-2 (instance 2): FOGHORN_INSTANCE_ID=foghorn-2
foghorn-2:
  environment:
    CLUSTER_ID: ${CLUSTER_ID}
    REDIS_URL: redis://foghorn-redis:6379
    FOGHORN_INSTANCE_ID: foghorn-2
  ports: [18018, 18029]
```

Both instances register independently with Quartermaster via `BootstrapService`. An upstream load balancer (Nginx, Caddy, or cloud LB) distributes requests across instances.

## Key Files

- `api_balancing/internal/state/stream_state.go` - StreamStateManager: in-memory state + Redis write-through
- `api_balancing/cmd/foghorn/main.go` - Wiring: Redis connection, CLUSTER_ID, FOGHORN_INSTANCE_ID
- `api_balancing/internal/federation/cache.go` - RemoteEdgeCache: federation Redis state, leader lease
- `docker-compose.yml` - foghorn + foghorn-2 + foghorn-redis topology

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

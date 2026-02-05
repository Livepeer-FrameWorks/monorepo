# Viewer Routing (Foghorn)

This document describes how Foghorn selects edge nodes for viewer playback requests. It is a contributor reference for understanding and modifying the routing algorithm.

For operator-level documentation, see `website_docs/.../operators/architecture.mdx` (Viewer Routing section).

## Related Source Files

- Load balancer core: `api_balancing/internal/balancer/balancer.go`
- State management: `api_balancing/internal/state/stream_state.go`
- HTTP handlers: `api_balancing/internal/handlers/handlers.go`
- gRPC server: `api_balancing/internal/grpc/server.go`
- Playback resolution: `api_balancing/internal/control/playback.go`
- Geo bucketing: `api_balancing/internal/geo/bucket.go`
- Weight config: `api_balancing/cmd/foghorn/main.go` (lines 53-56)

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

## Scoring Algorithm

Foghorn ranks eligible nodes using a weighted scoring system. **Higher score = better node.**

### Score Components

```go
score := cpuScore + ramScore + bwScore + geoScore + streamBonus
```

| Component    | Default Weight | Calculation                                 |
| ------------ | -------------- | ------------------------------------------- |
| CPU          | 500            | `WEIGHT - (cpu_pct * WEIGHT / 1000)`        |
| RAM          | 500            | `WEIGHT - (ram_used * WEIGHT / ram_max)`    |
| Bandwidth    | 1000           | `WEIGHT - (current_bw * WEIGHT / bw_limit)` |
| Geo          | 1000           | `WEIGHT - (WEIGHT * normalized_distance)`   |
| Stream bonus | +50            | If node already has the stream              |

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

Related source: `pkg/geoip/geoip.go`, `api_balancing/internal/handlers/handlers.go` (Cloudflare header extraction).

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

The player SDK uses Gateway GraphQL to resolve endpoints:

```graphql
query ResolveViewerEndpoint($playbackId: String!, $contentType: ContentType) {
  resolveViewerEndpoint(playbackId: $playbackId, contentType: $contentType) {
    primary { host port protocol ... }
    fallbacks { host port protocol ... }
    outputs { hls whep dash ... }
  }
}
```

Implementation: `npm_player/packages/core/src/core/GatewayClient.ts`

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
2. Selects best protocol using its own scoring (`npm_player/packages/core/src/core/scorer.ts`)
3. Falls back to next node/protocol on failure

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
}
```

Stored in: `periscope.routing_decisions` (ClickHouse)

## Modifying the Algorithm

### Adding a new weight

1. Add env var + weight field in `api_balancing/cmd/foghorn/main.go`
2. Pass to `StreamStateManager` in state initialization
3. Add to score calculation in `balancer.go`

### Changing eligibility filters

Edit `GetTopNodesWithScores` in `api_balancing/internal/balancer/balancer.go`.

### Adding node metadata

1. Add field to `NodeState` struct in `stream_state.go`
2. Populate from Helmsman heartbeats (in `api_balancing/internal/triggers/processor.go`)
3. Use in scoring or filtering as needed

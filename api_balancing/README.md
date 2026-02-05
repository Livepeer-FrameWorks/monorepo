# Foghorn (Load Balancer)

Go implementation of MistServer's load balancer, replacing the original C++ MistUtilLoad binary. **No external load balancer required**—Foghorn handles all media routing.

## Why Foghorn?

- **No cloud dependency**: Replace AWS ALB, Cloudflare LB, or any external load balancer with Foghorn
- **Self-hosted routing**: Run your own regional orchestration without vendor lock-in
- **Tenant-aware**: Routes traffic with full tenant context for multi-tenant deployments

## Architecture Role

Foghorn is the **regional orchestration hub** for the media pipeline. It sits between edge nodes (Helmsman/MistServer) and central services (Commodore, Decklog, Quartermaster).

## Overview

Routes streaming traffic to the best available media nodes based on:

- Geographic proximity
- Node performance (CPU, RAM, bandwidth)
- Stream availability
- Configurable weights

## Integration

### Edge Nodes (Helmsman)

- Maintains persistent gRPC streams with all connected Helmsman instances
- Receives all MistServer triggers forwarded by Helmsman
- Sends responses for blocking triggers (stream key validation, viewer auth)
- Dispatches commands: ClipPullRequest, DVRStartRequest, DVRStopRequest, ConfigSeed
- Tracks node health, capabilities, and stream state

### Control Plane (Commodore, Quartermaster)

- Validates stream keys via Commodore gRPC
- Resolves playback IDs (view keys) via Commodore
- Resolves node fingerprints to tenants via Quartermaster
- Handles edge node enrollment via Quartermaster bootstrap tokens

### Data Plane (Decklog)

- Geo-enriches all events before forwarding
- Batches and sends analytics events to Decklog gRPC
- Event types: stream lifecycle, viewer connections, buffer states, DVR/clip lifecycle

### MistServer Compatibility

- Provides 100% compatible load balancer API for MistServer nodes
- Handles stream routing, origin lookup, ingest selection

## Interfaces

### HTTP (viewer playback + ops)

Generic viewer playback endpoints for any HLS/DASH/WebRTC player:

```
GET /play/:viewkey                    → Full JSON with all protocols
GET /play/:viewkey/:protocol          → 307 redirect to edge node
GET /play/:viewkey.:protocol          → Alternative syntax
GET /resolve/:viewkey                 → Alias to /play
```

**Examples:**

```bash
# HLS playback (works with VLC, Safari, etc.)
GET /play/abc123def/index.m3u8
→ 307 Redirect to: https://edge-7.example.com/live+stream-id/index.m3u8

# WebRTC (WHEP)
GET /play/abc123def.webrtc
→ 307 Redirect to: https://edge-7.example.com/live+stream-id.webrtc

# Full JSON (custom players)
GET /play/abc123def
→ Returns all protocols and fallbacks
```

**Supported protocols:** HLS (`.m3u8`), DASH (`.mpd`), WebRTC (`.webrtc`), SRT, RTMP

Ops/diagnostics:

```
GET /nodes/overview              → List all nodes with capabilities
GET /dashboard                   → Minimal status page
GET /debug/cache/stream-context  → Cache inspection
```

MistServer compatibility endpoints (internal to MistServer nodes):

```
GET /<stream>?proto=<protocol>   → Stream routing (MistServer replication)
GET /?source=<stream>            → Origin lookup (DTSC)
GET /?ingest=<cpu>               → Find ingest node
GET /?lstserver=1                → List all servers
GET /?streamstats=<stream>       → Stream statistics
GET /?viewers=<stream>           → Viewer count
GET /?host=<hostname>            → Node status
GET /?weights=<json>             → Get/set balancer weights
```

### gRPC (control plane)

All control-plane APIs are gRPC (viewer/ingest resolution, clips, DVR, processing). Use the Foghorn gRPC service definitions in `pkg/proto`.

## Run (dev)

- Start the full stack from repo root: `docker-compose up -d`
- Or run just Foghorn: `cd api_balancing && go run ./cmd/foghorn`

## Health & ports

- Health: `GET /health`
- HTTP: 18008 (routing API)
- gRPC control: 18019

Configuration is sourced from `config/env/base.env` + `config/env/secrets.env`. Generate `.env` with `make env` (or `frameworks config env generate`) before starting the stack. Adjust `config/env/secrets.env` for credentials. Do not commit secrets.

## View Key Validation

Generic viewer endpoints validate view keys via Commodore gRPC (ResolvePlaybackID):

- Cached for 60 seconds (30s Stale-While-Revalidate)
- Returns `internal_name`, `tenant_id`, `status`
- Invalid keys return HTTP 404

## Configuration

### GeoIP

Foghorn can determine geography from either:

- Proxy-injected geo headers (e.g., Cloudflare's CF-IPCountry or similar), or
- A local MMDB file (any vendor providing a compatible City/Country database).

It is recommended to point it to a local MMDB file, which ensures all events are enriched with Geo data. Only events originating from the Load Balancer can be enriched via geo headers.

To use a local database, set `GEOIP_MMDB_PATH` to the path of your MMDB file. If neither headers nor MMDB are available, Foghorn operates without geo routing data.

### Storage

Foghorn reconstructs local file paths when defrosting artifacts from S3. It uses the node's registered `StorageLocal` path when available; if not, it falls back to `FOGHORN_DEFAULT_STORAGE_BASE`:

| Variable                       | Default                          | Description                                                                                                                                       |
| ------------------------------ | -------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------- |
| `FOGHORN_DEFAULT_STORAGE_BASE` | `/var/lib/mistserver/recordings` | Fallback storage path for artifact defrost when node's StorageLocal is unavailable. Must be absolute. Should match `HELMSMAN_STORAGE_LOCAL_PATH`. |

## Related

- Root `README.md` (ports, stack overview)
- `website_docs/` (DNS, viewer endpoints, balancing strategy)

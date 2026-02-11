# Helmsman (Edge Sidecar)

Edge sidecar that turns a MistServer node into a fully managed, hands-off media node. **Deploy on your infrastructure or ours**—Helmsman enables customer-managed edge nodes without cloud dependency.

## Why Helmsman?

- **Your hardware, our automation**: Run MistServer on your own servers while FrameWorks handles orchestration
- **No cloud lock-in**: Edge nodes communicate with Foghorn over gRPC—swap control planes without re-deploying nodes
- **Selfhosted deployments**: Mix self-hosted edge nodes with FrameWorks-managed infrastructure seamlessly

## What it does

- Intercepts MistServer triggers (PUSH_REWRITE, PLAY_REWRITE, USER_NEW, STREAM_BUFFER, etc.)
- Forwards all triggers to Foghorn for validation and routing decisions
- Receives responses from Foghorn for blocking triggers (stream key validation, viewer auth)
- Periodic health/metrics collection and reporting to Foghorn
- Receives configuration (tenant_id, stream templates, geo info) from Foghorn via ConfigSeed
- Executes storage operations on Foghorn's behalf:
  - Clip generation (ClipPullRequest -> download from MistServer -> store locally)
  - DVR recording (DVRStartRequest/DVRStopRequest -> HLS segment capture)
  - Artifact cleanup and deletion notifications

## Node capabilities

Helmsman registers with Foghorn announcing its capabilities:

- `CapIngest` - Can accept incoming streams (RTMP/SRT/WHIP)
- `CapEdge` - Can serve viewers (HLS/WebRTC/DASH)
- `CapStorage` - Has local/S3 storage for clips and DVR
- `CapProcessing` - Can run transcoding/AI workloads

## Communication

- **MistServer** (local): HTTP triggers, metrics scraping, clip downloads
- **Foghorn** (regional): Persistent bidirectional gRPC stream (HelmsmanControl)

## Event types (forwarded to Foghorn)

- `stream-ingest`, `stream-view`, `stream-lifecycle`, `stream-buffer`, `stream-end`
- `user-connection`, `push-lifecycle`, `recording-lifecycle`, `track-list`, `client-lifecycle`

## Deployment model

- One instance per MistServer node
- Configured with node identity and regional Foghorn address

## Run (dev)

- Typically runs alongside MistServer. For local stack: `docker-compose up -d`
- Or run just Helmsman: `cd api_sidecar && go run ./cmd/helmsman`

Configuration is shared via the repo-level `config/env` files. Run `make env` / `frameworks config env generate` to create `.env`, then adjust `config/env/secrets.env` as needed. Do not commit secrets.

## Health & port

- Health: `GET /health`
- HTTP: 18007

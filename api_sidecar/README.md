# Helmsman (Edge Sidecar)

Edge sidecar that turns a MistServer node into a fully managed, hands-off media node. **Deploy on your infrastructure or ours**—Helmsman enables customer-managed edge nodes without cloud dependency.

## Why Helmsman?

- **Your hardware, our automation**: Run MistServer on your own servers while FrameWorks handles orchestration
- **No cloud lock-in**: Edge nodes communicate with Foghorn over gRPC—swap control planes without re-deploying nodes
- **Selfhosted deployments**: Mix self-hosted edge nodes with FrameWorks-managed infrastructure seamlessly

## What it does

- Installs and handles MistServer triggers such as `PUSH_REWRITE`, `PLAY_REWRITE`, `STREAM_SOURCE`, `USER_NEW`, `USER_END`, `STREAM_BUFFER`, `STREAM_END`, `LIVE_TRACK_LIST`, recording, processing, and thumbnail triggers
- Forwards configured MistServer triggers and synthetic lifecycle/storage/processing events to Foghorn for validation, routing, analytics, and orchestration decisions
- Receives responses from Foghorn for blocking triggers (stream key validation, viewer auth)
- Periodic health/metrics collection and reporting to Foghorn
- Receives configuration from Foghorn via `ConfigSeed`: canonical node ID, geo placement, tenant ID, stream templates, processing config, operational mode, TLS/CA material, public site config, telemetry remote-write config, and balancer base URL
- Executes storage operations on Foghorn's behalf:
  - Clip generation (ClipPullRequest -> download from MistServer -> store locally)
  - DVR recording (DVRStartRequest/DVRStopRequest -> HLS segment capture)
  - Clip/DVR/VOD deletion notifications
  - Freeze/defrost and incremental `.dtsh` sync for S3-backed storage
  - Thumbnail upload requests for `poster.jpg`, `sprite.jpg`, and `sprite.vtt`
  - Processing jobs for VOD/transcode workloads
  - Session termination and push-target activation/deactivation commands

## Node capabilities

Helmsman registers with Foghorn announcing boolean capability fields and role labels:

- `cap_ingest` / `HELMSMAN_CAP_INGEST` - Can accept incoming streams (RTMP/SRT/WHIP)
- `cap_edge` / `HELMSMAN_CAP_EDGE` - Can serve viewers (HLS/WebRTC/DASH)
- `cap_storage` / `HELMSMAN_CAP_STORAGE` - Has local/S3 storage for clips, DVR, and VOD artifacts
- `cap_processing` / `HELMSMAN_CAP_PROCESSING` - Can run processing workloads

## Communication

- **MistServer** (local): HTTP triggers, metrics scraping, clip downloads
- **Foghorn** (regional): Persistent bidirectional gRPC stream (HelmsmanControl)

## Event types (forwarded to Foghorn)

- MistServer trigger events: `PUSH_REWRITE`, `PLAY_REWRITE`, `STREAM_SOURCE`, `PUSH_OUT_START`, `PUSH_END`, `USER_NEW`, `USER_END`, `STREAM_BUFFER`, `STREAM_END`, `LIVE_TRACK_LIST`, `RECORDING_END`, `RECORDING_SEGMENT`, `STREAM_PROCESS`, `PROCESS_EXIT`, Livepeer/process segment completion, and `THUMBNAIL_UPDATED`
- Synthetic events from polling and storage/process managers: `STREAM_LIFECYCLE_UPDATE`, `NODE_LIFECYCLE_UPDATE`, `CLIENT_LIFECYCLE_UPDATE`, `storage_lifecycle`, `storage_snapshot`, freeze/defrost progress/completion, processing job progress/results, and push-target status reports

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

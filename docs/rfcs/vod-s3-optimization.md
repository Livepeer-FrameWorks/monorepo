# RFC: VOD Ingest & Warmup Optimization

## Status

Draft

## TL;DR

- Current VOD warmup blocks playback until full asset is downloaded locally.
- VOD uploads are accepted as-is without preprocessing.
- Move to S3 pull playback and a pre-processing pipeline.

## Current State (as of 2026-01-13)

- Helmsman downloads full VOD assets to local disk via presigned URLs before Mist can serve.
- Foghorn generates presigned URLs for VOD uploads; assets are marked ready after upload.
- No VOD processing pipeline is enforced before playback.

Evidence:

- `api_sidecar/internal/handlers/storage_manager.go`
- `api_balancing/internal/grpc/server.go`
- `api_balancing/internal/storage/s3_client.go`

## Problem / Motivation

Large VOD assets cause long time-to-first-frame because playback waits for full download. Raw uploads can be inefficient (non-streamable containers, missing `.dtsh`, no optimization).

## Goals

- Start playback without full local download.
- Ensure VOD assets are processed before being served.
- Keep edge nodes untrusted (use presigned URLs).

## Non-Goals

- Full transcoding ladder in v1.
- Changing live-stream processing behavior.

## Proposal

- Playback: configure Mist to pull directly from S3 (or a proxy) on-demand.
- Ingest: add a processing stage that remuxes and generates `.dtsh` before marking ready.

## Impact / Dependencies

- Helmsman storage manager + MistServer integration.
- Foghorn VOD upload flow.
- Processing orchestration (see processing RFC).

## Alternatives Considered

- CDN prewarm only (still blocks until complete download).
- Partial download thresholds without processing pipeline.

## Risks & Mitigations

- Risk: S3 auth/expiry for Mist pulls. Mitigation: proxy or long-lived signed URLs.
- Risk: increased S3 egress. Mitigation: caching and hot-path policies.

## Migration / Rollout

1. Add optional S3 pull playback for VOD.
2. Add pre-processing stage (remux + `.dtsh`).
3. Make processed outputs the default playback target.

## Open Questions

- Where should S3 pull authentication live (proxy vs signed URLs)?
- Should we support hybrid cache for popular assets?

## References, Sources & Evidence

- `api_sidecar/internal/handlers/storage_manager.go`
- `api_balancing/internal/grpc/server.go`
- `api_balancing/internal/storage/s3_client.go`

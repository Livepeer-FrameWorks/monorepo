# RFC: VOD Ingest & Warmup Optimization

## Status

Draft

## TL;DR

- Current VOD warmup blocks playback until full asset is downloaded locally.
- VOD uploads now enter a processing pipeline after upload completion.
- Move playback warmup toward S3 pull/proxy playback so first frame does not wait for
  a full local download.

## Current State

- Helmsman downloads full VOD assets to local disk via presigned URLs before Mist can serve.
- Foghorn generates presigned URLs for multipart VOD uploads.
- After multipart completion, Foghorn marks the asset `processing`, creates a processing
  job, and does not serve it as ready until the processing pipeline completes.
- S3 pull/proxy playback is not implemented yet.

Evidence:

- `api_sidecar/internal/handlers`
- `api_balancing/internal/grpc`
- `api_balancing/internal/storage`
- `api_balancing/internal/jobs/processing_dispatcher.go`

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

- `api_sidecar/internal/handlers`
- `api_balancing/internal/grpc`
- `api_balancing/internal/storage`

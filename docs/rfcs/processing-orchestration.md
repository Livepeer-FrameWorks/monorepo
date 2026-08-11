# RFC: Processing Orchestration (Live + VOD)

> **Note:** The _implemented_ artifact job pipeline (clip / VOD-upload / DVR-chapter →
> `processing+<hash>` → `vod+<internal_name>`, and where tracks/duration/readiness are captured)
> is now documented canonically in
> [`docs/architecture/processing-pipeline.md`](../architecture/processing-pipeline.md).
>
> This RFC remains OPEN for processing as a first-class orchestrated _capability_, which is still
> a grey area: capacity- and GPU-aware scheduling, a native transcode path alongside the Livepeer
> network, AI/GPU task types and batching (e.g. VOD), and live-stream processing policy (today
> live transcode is implicit Mist/Helmsman config, not job-orchestrated). This is pre-work before
> processing can be exposed as a product surface, parallel to the storage/policy-engine track.
> For how the pipeline works _today_, read the architecture doc; for where it's _going_, this RFC.

## Status

Partially implemented

## TL;DR

- A VOD processing orchestration path now exists.
- Foghorn coordinates VOD job routing and dispatch to processing-capable Helmsman nodes.
- Processing jobs are a first-class workflow (`queued` -> `dispatched`/`processing` -> `completed`/`failed`).
- Live processing policy is still mostly outside this pipeline.

## Owning services / modules

Foghorn (`api_balancing`) owns VOD job scheduling and state (`foghorn.processing_jobs`); Helmsman (`api_sidecar`) reports node processing capability and executes dispatched jobs; usage events flow through Decklog/Periscope.

## Current State

- Live processing is configured via Mist/Helmsman and remains mostly implicit.
- `foghorn.processing_jobs` exists and is used by the VOD processing pipeline.
- `CompleteVodUpload` marks assets `processing` and queues jobs rather than marking them ready immediately.
- Foghorn runs a processing dispatcher that routes jobs to processing-capable nodes; Helmsman handles `ProcessingJobRequest` and reports progress/results.

Evidence:

- `pkg/database/sql/schema/foghorn.sql`
- `api_balancing/internal/grpc/server.go`
- `api_balancing/internal/grpc/vod_pipeline.go`
- `api_balancing/internal/jobs/processing_dispatcher.go`
- `api_sidecar/internal/handlers/processing.go`
- `pkg/proto/ipc.proto`

## Problem / Motivation

Processing decisions are implicit and inconsistent across live and VOD. A unified orchestration model is needed for capacity-aware routing, retries, and observability.

## Goals

- One job model for live + VOD processing.
- Capacity-aware dispatch to native processing nodes.
- Clear lifecycle events for billing and monitoring.

## Non-Goals

- Implementing worker binaries in this RFC.
- Rewriting billing or UI flows.

## Proposal

- Keep Foghorn as the processing coordinator for VOD.
- Continue using `foghorn.processing_jobs` for durable job state.
- Extend routing policy and lifecycle coverage beyond the current VOD path, including live
  processing policies where needed.

## Impact / Dependencies

- Foghorn job scheduling + state.
- Helmsman node capability reporting.
- Periscope/Decklog usage events.

## Alternatives Considered

- Keep current implicit behavior (status quo).
- Separate orchestrator service (more moving parts).

## Risks & Mitigations

- Risk: job queue stalls. Mitigation: TTL + retries + monitoring.
- Risk: routing misconfiguration. Mitigation: staged rollout + feature flags.

## Migration / Rollout

1. Keep explicit job creation for VOD uploads.
2. Keep the dispatcher for processing-capable nodes.
3. Harden processing validation, retry, and output metadata.
4. Extend to live processing policies.

## Open Questions

- Should gateway processing be allowed for VOD?
- Where do we store processing outputs (S3 vs local)?

## References, Sources & Evidence

- `pkg/database/sql/schema`
- `api_balancing/`
- `api_sidecar/`

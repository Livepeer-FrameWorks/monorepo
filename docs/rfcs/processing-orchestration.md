# RFC: Processing Orchestration (Live + VOD)

## Status

Draft

## TL;DR

- Add a unified orchestration layer for live + VOD processing.
- Foghorn coordinates job routing and capacity-aware dispatch.
- Processing jobs become a first-class workflow (queued → dispatched → completed).

## Current State (as of 2026-01-13)

- Live processing is configured via Mist/Helmsman and is mostly implicit.
- `foghorn.processing_jobs` exists but orchestration is minimal.
- VOD uploads are marked ready without a processing pipeline.

Evidence:

- `pkg/database/sql/schema/foghorn.sql`
- `api_balancing/internal/handlers/handlers.go`
- `api_sidecar/`

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

- Treat Foghorn as the processing coordinator.
- Use `foghorn.processing_jobs` for durable job state.
- Add routing policy: gateway vs native nodes per codec and capacity.

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

1. Add explicit job creation for VOD uploads.
2. Add minimal dispatcher for native processing nodes.
3. Extend to live processing policies.

## Open Questions

- Should gateway processing be allowed for VOD?
- Where do we store processing outputs (S3 vs local)?

## References, Sources & Evidence

- `pkg/database/sql/schema/foghorn.sql`
- `api_balancing/`
- `api_sidecar/`

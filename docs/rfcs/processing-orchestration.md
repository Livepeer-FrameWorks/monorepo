# RFC: Processing Orchestration (Live + VOD)

> **Note:** The _implemented_ artifact job pipeline (clip / VOD-upload / DVR-chapter →
> `processing+<hash>` → `vod+<internal_name>`, and where tracks/duration/readiness are captured)
> is now documented canonically in
> [`docs/architecture/processing-pipeline.md`](../architecture/processing-pipeline.md).
>
> Job routing is capacity-aware today, and live/DVR processing policy is resolved and signed by
> Commodore rather than left implicit. What this RFC still covers: a per-job processing class
> (the queue has none), GPU/ONNX-profile-aware placement (the profile is reported but not used
> for scheduling), live processing expressed as a job rather than as a signed process config, and
> processing as a product surface. For how the pipeline works _today_, read the architecture doc;
> for where it's _going_, this RFC.

## Status

Partially implemented

## TL;DR

- Artifact processing (clip / VOD upload / DVR chapter) is a first-class job workflow
  (`queued` -> `dispatched`/`processing` -> `completed`/`failed`) routed by advertised per-class capacity.
- Live and DVR processing is config-orchestrated, not job-orchestrated: Commodore resolves a
  tier-gated process config and signs it into the media-authority envelope.
- The queue has no per-job processing class, and the reported ONNX profile is not a placement input.

## Owning services / modules

Foghorn (`api_balancing`) owns VOD job scheduling and state (`foghorn.processing_jobs`); Helmsman (`api_sidecar`) reports node processing capability and executes dispatched jobs; usage events flow through Decklog/Periscope.

## Current State

**Artifact jobs are orchestrated.** `foghorn.processing_jobs`
(`pkg/database/sql/schema/foghorn.sql` ~1482) is the durable job ledger, and
`api_balancing/internal/jobs/processing_dispatcher.go` dispatches from it to processing-capable
Helmsman nodes. `routeProcessingJob` in `api_balancing/internal/jobs/job_router.go` matches the
job's processing class against each node's advertised per-class capacity (`CanRunClass`) and picks
the node with the lowest in-flight load for that class, behind a fail-closed tenant boundary:
`nodeEligibleForJobTenant` resolves `control.ClusterAccessibleForTenant`, so a candidate node's
virtual cluster must hold Quartermaster cluster↔tenant entitlement, and an unproven cluster is
skipped rather than allowed.

**But there is no per-job class.** `jobProcessingClass` returns `video_transcode`
unconditionally, because `processing_jobs` has no class column. Every queued job is therefore
matched against the same class regardless of what it actually does.

**GPU/ONNX placement is not wired.** `onnx_profile` exists on `DesiredComponent` and
`NodeLifecycleUpdate` in `pkg/proto/ipc.proto`, and Helmsman reports it
(`api_sidecar/internal/handlers/poller.go`) into node runtime info
(`api_balancing/internal/state/stream_state.go`). Nothing in routing reads it: placement is class
capacity and load only.

**Live and DVR processing is config-orchestrated, not job-orchestrated.** Commodore's
`resolveProcessesJSON(ctx, tenantID, streamID, clusterID, lifecycle)`
(`api_control/internal/grpc/server.go` ~1271-1331) resolves a process config per lifecycle
(`live`, `dvr`, `clip`, `dvr_finalize`, `vod`) in the order per-stream override → tenant override
→ tier default. The per-stream override always wins; the tenant-wide override is gated on
`tier.processing_customizable`. Overrides live in `commodore.stream_processing_config` and
`commodore.tenant_processing_config` (`pkg/database/sql/schema/commodore.sql` ~1051, ~1067) and
are revalidated at read, so a stale or hand-edited row falls through to the next source instead of
reaching MistServer. The resolved `live` and `dvr` configs are signed into the Ed25519
media-authority envelope (`api_control/internal/grpc/media_authority.go` ~303), and a rolling DVR
session gets its own snapshot stamped in `foghorn.artifacts.dvr_processes_json`. So live
processing policy is authoritative and durable, but it is a signed configuration, not a queued
unit of work with its own placement, retries, and lifecycle events.

**Transcode is Livepeer-then-local.** The dispatcher rewrites a job's process config before
delivery (`processing_dispatcher.go` ~370-430): `MaskLivepeerSourceForVOD`,
`DisableProcessRestarts`, then `ApplyLivepeerBroadcasters` and `ApplyLivepeerWorkload` from the
gateway resolver. The durable/cached copy is kept token-free via `StripLivepeerJobToken`; the
delivery copy is stamped with a per-dispatch capability only after the exact node assignment is
durable, so a capability cannot be replayed across retries.

## Problem / Motivation

Placement is capacity-aware but class-blind and hardware-blind: every job declares the same class,
and GPU capability is reported but never scheduled against. Live processing is authoritative as
policy but is not a unit of work, so it has no placement decision, no retry semantics, and no job
lifecycle events to bill or observe against.

## Goals

- A per-job processing class on `foghorn.processing_jobs`, so routing matches real work to real capacity.
- Placement that reads the reported ONNX/GPU profile.
- A path for live processing to become a job rather than only a signed process config.
- Processing exposed as a product surface.

## Non-Goals

- Implementing worker binaries in this RFC.
- Rewriting billing or UI flows.
- Re-deciding how live process config is resolved or signed; that path is shipped.

## Proposal

- Add a processing class column to `foghorn.processing_jobs` and have `jobProcessingClass` read it
  instead of returning a constant.
- Extend `routeProcessingJob` eligibility with the node's reported ONNX profile so GPU-class work
  lands on GPU-capable nodes.
- Define what a live processing job would own that the signed config does not (placement, retry,
  lifecycle events) before moving live onto the queue.
- Keep Foghorn as the coordinator and `foghorn.processing_jobs` as durable job state throughout.

## Impact / Dependencies

- Foghorn job scheduling + state.
- Helmsman node capability reporting.
- Periscope/Decklog usage events.

## Alternatives Considered

- Keep the single hardcoded class (status quo): simplest, but a node advertising only
  non-transcode capacity can never be matched.
- Separate orchestrator service (more moving parts).

## Risks & Mitigations

- Risk: job queue stalls. Mitigation: TTL + retries + monitoring.
- Risk: a new class column strands existing queued rows. Mitigation: default the column to
  `video_transcode`, which is exactly today's behavior.

## Migration / Rollout

1. Add the class column with a `video_transcode` default; routing behavior is unchanged.
2. Populate the class at job creation per job kind, and read it in `jobProcessingClass`.
3. Add ONNX/GPU profile to node eligibility.
4. Decide the live-processing-as-a-job model before moving live off the signed config path.

## Open Questions

- Should gateway processing be allowed for VOD?
- Where do we store processing outputs (S3 vs local)?
- Does a live processing job replace the signed process config, or sit alongside it as the placement record?

## References, Sources & Evidence

- [Evidence] `pkg/database/sql/schema/foghorn.sql` - `foghorn.processing_jobs`
- [Evidence] `pkg/database/sql/schema/commodore.sql` - `stream_processing_config`, `tenant_processing_config`
- [Evidence] `api_balancing/internal/jobs/job_router.go` - class matching, load selection, tenant gate
- [Evidence] `api_balancing/internal/jobs/processing_dispatcher.go` - Livepeer-then-local config rewrite, node assignment
- [Evidence] `api_control/internal/grpc/server.go` - `resolveProcessesJSON` lifecycle resolution
- [Evidence] `api_control/internal/grpc/media_authority.go` - live/DVR config signed into the authority envelope
- [Evidence] `pkg/proto/ipc.proto` - `onnx_profile` on `DesiredComponent` / `NodeLifecycleUpdate`

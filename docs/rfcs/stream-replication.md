# RFC: Stream Replication Topology (Multi-Region)

## Status

Not implemented. Federation (see `docs/architecture/federation.md`) provides cross-cluster
stream discovery and origin-pull, but there is no replication topology. All replication is
direct origin→edge regardless of geography. The proposed topology model (origin/hub/edge
roles, region-aware replication policy, per-stream controls like `max_replicas` and
`allowed_regions`, loop prevention) has not been built.

## TL;DR

- Today replication is implicit: Foghorn tracks a "replicated" flag and avoids using replicated nodes as sources.
- Define an explicit replication topology model (origin, hubs, edges) to improve stability and observability.
- Add policy inputs (regions, max replicas) and loop prevention.

## Current State

- Foghorn tracks per-node stream state including `replicated` and uses it to exclude replicated nodes from source selection.
- Replication topology is implicit; no explicit graph or policy model.

Evidence:

- `api_balancing/internal/balancer`
- `api_balancing/internal/state`
- `api_sidecar/internal/handlers`
- `pkg/proto`

## Problem / Motivation

Implicit replication rules lead to unstable behavior under churn, unclear origin selection, and limited observability. Multi-region replication needs explicit controls to avoid loops and support predictable failover.

## Goals

- Explicit origin selection and stable topology.
- Region-aware replication policy.
- Loop prevention.
- Observable replication state.

## Non-Goals

- Replacing MistServer replication mechanics.
- CDN or edge cache design.

## Proposal

Introduce a topology model:

- One origin per stream.
- Optional region hubs for inter-region replication.
- Edges replicate from hubs or origin.

Add policy fields per stream/tenant:

- `max_replicas_total`, `max_replicas_per_region`, allowed regions.

## Impact / Dependencies

- Foghorn state + routing logic.
- Node metadata (region/roles) from Quartermaster or node lifecycle updates.
- Optional GraphQL exposure for observability.

## Alternatives Considered

- Keep implicit replication (status quo).
- DNS-only region steering (insufficient for replication topology).

## Risks & Mitigations

- Risk: added complexity in routing logic. Mitigation: phased rollout + feature flag.
- Risk: stale topology info. Mitigation: TTL + health checks.

## Migration / Rollout

1. Add topology fields and defaults without behavior change.
2. Enable explicit origin selection.
3. Add hub-based inter-region replication.

## Open Questions

- Where should region metadata be sourced and validated?
- Do we need per-stream override vs per-tenant default?

## References, Sources & Evidence

- `api_balancing/internal/balancer`
- `api_balancing/internal/state`
- `api_sidecar/internal/handlers`
- `pkg/proto`

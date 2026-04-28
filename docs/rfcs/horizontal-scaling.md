# RFC: Horizontal Scaling & High Availability Strategy

## Status

Partially implemented. Phase 0 (failsafe wiring into the shared gRPC clients) and
Foghorn HA (Redis state externalization + pub/sub sync) are done. See
`docs/architecture/foghorn-ha.md`.

Bridge state externalization and Signalman scaling — the other two stateful services
identified in this RFC — have not been addressed. No HA strategy exists beyond Foghorn.

## TL;DR

- Platform services vary in statefulness; scaling needs targeted strategies per service.
- Retry/circuit breaker utilities are wired into the shared gRPC clients.
- This RFC defines high-level HA priorities and proposes follow-up RFCs per domain.

## Current State

- Circuit breaker/retry helpers exist in `pkg/clients` and are wired into shared
  Commodore, Quartermaster, Purser, Foghorn, Periscope, Signalman, Deckhand, and
  Skipper gRPC clients.
- Services still have mixed statefulness and dependencies; HA behavior beyond Foghorn
  remains inconsistent.

Evidence:

- `pkg/clients/failsafe_grpc.go`
- `pkg/clients/*/grpc_client.go`

## Problem / Motivation

Scaling requirements are increasing, but there is no unified HA strategy. Some services are stateful and require different approaches (Redis, Kafka groups, coordination), while client resiliency is not consistently applied.

## Goals

- Establish a platform-wide HA approach.
- Prioritize quick wins (client resiliency) and critical stateful services.
- Provide a roadmap split into focused RFCs.

## Non-Goals

- Implementing all HA changes in this RFC.
- Re-architecting every service immediately.

## Proposal

- Keep the current gRPC failsafe wiring as Phase 0 and extend similar coverage where
  custom HTTP clients still bypass the shared executor.
- Produce separate RFCs for:
  - Foghorn HA (state replication + failover)
  - Bridge state externalization (Redis for caches)
  - Signalman scaling (Kafka consumer groups)

## Impact / Dependencies

- `pkg/clients/*` usage across services.
- Redis, Kafka, and database coordination.
- Deployment/ops for HA components.

## Alternatives Considered

- Service-by-service ad hoc fixes (status quo).
- Full service mesh (high complexity).

## Risks & Mitigations

- Risk: partial rollout leads to inconsistent behavior. Mitigation: phased plan + service priorities.
- Risk: over-indexing on infra before product needs. Mitigation: focus on critical paths.

## Migration / Rollout

1. Keep shared gRPC client failsafe wiring in place.
2. Extend shared retry/circuit-breaker coverage to remaining custom HTTP clients where useful.
3. Tackle stateful services in priority order (Foghorn, Bridge, Signalman).
4. Add coordination mechanisms (Redis, Kafka groups, advisory locks).

## Open Questions

- Which service is the highest immediate HA risk?
- Do we need cross-region HA in the near term?

## References, Sources & Evidence

- `pkg/clients/failsafe_grpc.go`
- `pkg/clients/*/grpc_client.go`
- `api_balancing/`
- `api_gateway/`
- `api_realtime/`

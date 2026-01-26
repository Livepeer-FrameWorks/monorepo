# RFC: Horizontal Scaling & High Availability Strategy

## Status
Draft

## TL;DR
- Platform services vary in statefulness; scaling needs targeted strategies per service.
- Retry/circuit breaker utilities exist but are not wired into clients.
- This RFC defines high-level HA priorities and proposes follow-up RFCs per domain.

## Current State (as of 2026-01-13)
- Circuit breaker/retry helpers exist in `pkg/clients` but are unused.
- Services have mixed statefulness and dependencies; HA behavior is inconsistent.

Evidence:
- `pkg/clients/failsafe.go`
- `pkg/clients/failsafe_grpc.go`

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
- Wire failsafe retry/circuit breaker into gRPC/HTTP clients as Phase 0.
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
1. Adopt failsafe in all gRPC/HTTP clients.
2. Tackle stateful services in priority order (Foghorn, Bridge, Signalman).
3. Add coordination mechanisms (Redis, Kafka groups, advisory locks).

## Open Questions
- Which service is the highest immediate HA risk?
- Do we need cross-region HA in the near term?

## References, Sources & Evidence
- `pkg/clients/failsafe.go`
- `pkg/clients/failsafe_grpc.go`
- `api_balancing/`
- `api_gateway/`
- `api_realtime/`

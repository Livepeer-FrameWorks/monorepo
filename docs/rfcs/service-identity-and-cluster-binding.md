# RFC: Service Identity and Cluster Binding

## Status
Draft

## TL;DR
- Current service-to-service auth uses a shared static `SERVICE_TOKEN`.
- Introduce a service JWT (or similar) to make `cluster_id` non-forgeable.
- Quartermaster issues and validates service identity for registration.

## Current State (as of 2026-01-13)
- gRPC/HTTP middleware accepts either a shared `SERVICE_TOKEN` or a user JWT.
- Services register with Quartermaster using shared token semantics.

Evidence:
- `pkg/middleware/grpc.go`
- `pkg/auth/middleware.go`

## Problem / Motivation
A shared static token cannot carry trustworthy identity or cluster claims. Multi-cluster setups require verifiable service identity and cluster binding to prevent accidental or malicious mis-registration.

## Goals
- Verifiable service identity for internal calls.
- Non-forgeable `cluster_id` attribution.
- Safe rotation without coordinated downtime.

## Non-Goals
- Replacing user JWTs.
- Full OIDC or SPIFFE integration in this RFC.

## Proposal
- Introduce a service token class (JWT or similar) with claims:
  - `sub` (service type)
  - `cluster_id`
  - `aud` (internal)
  - `iat`/`exp`
- Use asymmetric signing with a rotating key set.
- Quartermaster becomes the issuer and validates registration against desired state.

## Impact / Dependencies
- Middleware in `pkg/middleware` and `pkg/auth`.
- Quartermaster gRPC APIs and schema for service inventory.
- Service client auth in `pkg/clients/*`.

## Alternatives Considered
- Continue shared static token (status quo).
- mTLS with SPIFFE/SPIRE (stronger, higher complexity).

## Risks & Mitigations
- Risk: issuer outage blocks new tokens. Mitigation: cached tokens + TTL.
- Risk: rotation errors. Mitigation: `kid` + multi-key verification.

## Migration / Rollout
1. Add service-JWT support alongside static token.
2. Quartermaster issues service JWTs.
3. Require service JWT for registration.
4. Deprecate static token for S2S.

## Open Questions
- Should issuance be centralized in Quartermaster or separate auth service?
- Long-term: migrate to mTLS/SPIFFE?

## References, Sources & Evidence
- `pkg/middleware/grpc.go`
- `pkg/auth/middleware.go`
- `api_tenants/`
- `pkg/proto/quartermaster.proto`

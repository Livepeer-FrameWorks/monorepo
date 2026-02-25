# RFC: Optional gRPC TLS for Mesh-Only Traffic

## Status

Partially implemented. Transport TLS works on the Foghorn↔Helmsman path via FQDN
auto-detect and Navigator-backed wildcard cert in
`api_balancing/internal/control`. The proposed platform-wide config pattern
(`GRPC_USE_TLS` toggle in `pkg/config` with shared helpers) was not adopted — TLS setup
is ad-hoc per service. mTLS is not implemented. See `docs/architecture/edge-auth.md`.

## TL;DR

- gRPC traffic is mostly plaintext and relies on the WireGuard mesh for transport security.
- Add an opt-in TLS/mTLS layer for gRPC with per-service rollout.
- Keep insecure mode as default until multi-cluster or compliance requires TLS.

## Current State

- Many gRPC clients explicitly use `insecure.NewCredentials()`.
- Foghorn control server has an env-gated TLS toggle, but most clients remain insecure.
- Helmsman has TLS-related env vars but uses insecure creds today.

Evidence:

- `pkg/clients` gRPC client implementations
- `api_balancing/internal/control`
- `api_sidecar/internal/config`

## Problem / Motivation

Mesh-only encryption is acceptable for single-cluster deployments, but gRPC traffic becomes a security risk if any service communication leaves the mesh (multi-cluster, public transport, or regulated environments). We need a controlled path to TLS without breaking current deployments.

## Goals

- Optional TLS for gRPC servers and clients (opt-in per service).
- Maintain current insecure default for mesh-only deployments.
- Allow mTLS as a follow-up step.
- Support phased rollout and rollback.

## Non-Goals

- Full PKI automation (issuance/rotation) in this RFC.
- SPIFFE/SPIRE integration.
- Changes to HTTP service security.

## Proposal

Introduce a common gRPC TLS config pattern in `pkg/config` and enforce it in all gRPC servers and clients:

- `GRPC_USE_TLS=true|false` (default: false)
- `GRPC_TLS_CERT_PATH=/path/server.crt`
- `GRPC_TLS_KEY_PATH=/path/server.key`
- `GRPC_TLS_CA_PATH=/path/ca.crt`
- (Optional) `GRPC_MTLS_ENABLED=true|false`
- (Optional) `GRPC_TLS_CLIENT_CERT_PATH=/path/client.crt`
- (Optional) `GRPC_TLS_CLIENT_KEY_PATH=/path/client.key`

## Impact / Dependencies

- All gRPC servers and clients in `pkg/clients/*` and `api_*/internal`.
- Env config generation (`config/env/*` and `pkg/config`).
- Deployment automation for cert distribution.

## Alternatives Considered

- Rely on WireGuard only (status quo).
- Full mTLS + SPIFFE/SPIRE (more secure, higher complexity).
- Service mesh (overkill for current stack).

## Risks & Mitigations

- Risk: misconfigured certs cause outage. Mitigation: per-service opt-in + canary rollout.
- Risk: split-brain between TLS/non-TLS services. Mitigation: clear per-service toggles and compatibility checks.

## Migration / Rollout

1. Add shared config helpers for TLS/mTLS.
2. Update one non-critical service pair (e.g., Bridge → Periscope) as a pilot.
3. Roll through remaining services, with env toggles and rollback paths.

## Open Questions

- Should mTLS be required in multi-cluster mode?
- Where are certs provisioned (CLI, deploy pipeline, external PKI)?

## References, Sources & Evidence

- `pkg/clients` gRPC client implementations
- `api_balancing/internal/control`
- `api_sidecar/internal/config`

# RFC: Optional gRPC TLS for Mesh-Only Traffic

## Status

Partially implemented. Shared gRPC TLS helpers now exist in `pkg/grpcutil`, and
most service clients/servers use `GRPC_TLS_CERT_PATH`, `GRPC_TLS_KEY_PATH`,
`GRPC_TLS_CA_PATH`, `GRPC_TLS_SERVER_NAME`, and `GRPC_ALLOW_INSECURE`. The
original `GRPC_USE_TLS` toggle proposal was not adopted. Foghorn↔Helmsman
transport TLS also supports file-based certificates and Navigator-backed
wildcard certificates. mTLS is not implemented. See
`docs/architecture/edge-auth.md`.

## TL;DR

- gRPC traffic can now use shared TLS helpers, but deployments may still allow insecure
  transport via `GRPC_ALLOW_INSECURE`.
- Continue hardening the rollout and add mTLS later if required.
- Keep explicit insecure mode for trusted dev/mesh deployments.

## Current State

- Shared server/client TLS helpers live in `pkg/grpcutil`.
- Most service clients pass `GRPC_ALLOW_INSECURE`, `GRPC_TLS_CA_PATH`, and
  `GRPC_TLS_SERVER_NAME` into their generated client config.
- Most gRPC servers use `GRPC_TLS_CERT_PATH`, `GRPC_TLS_KEY_PATH`, and
  `GRPC_ALLOW_INSECURE`.
- Foghorn control gRPC can use file-based certificates or Navigator-issued wildcard
  certificates, and Helmsman can connect with TLS when configured.
- mTLS/client certificate verification is not implemented.

Evidence:

- `pkg/grpcutil`
- `pkg/clients/*/grpc_client.go`
- `api_*/*/main.go` gRPC server setup
- `api_balancing/internal/control/server.go`
- `api_sidecar/internal/control/client.go`

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

The common gRPC TLS config pattern that exists today is:

- `GRPC_TLS_CERT_PATH=/path/server.crt`
- `GRPC_TLS_KEY_PATH=/path/server.key`
- `GRPC_TLS_CA_PATH=/path/ca.crt`
- `GRPC_TLS_SERVER_NAME=service.internal.example`
- `GRPC_ALLOW_INSECURE=true|false`

Future mTLS work would need additional client certificate settings and server-side
client cert verification.

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

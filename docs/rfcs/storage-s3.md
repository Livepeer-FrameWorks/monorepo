# RFC: S3-Compatible Object Storage for Edge Clusters

## Status
Draft

## TL;DR
- Add a unified storage interface for clips/DVR with local, S3, and hybrid modes.
- Use presigned URLs so edge nodes never hold S3 credentials.
- Enable durable, shareable artifacts across edge nodes.

## Current State (as of 2026-01-13)
- Helmsman uses presigned URLs for clip/DVR uploads and downloads.
- Storage is still effectively local-first; there is no shared `pkg/storage` abstraction.

Evidence:
- `api_sidecar/internal/handlers/storage_manager.go`
- `api_balancing/internal/storage/s3_client.go`

## Problem / Motivation
Local-only storage is ephemeral and limits durability and sharing. We need a consistent storage layer that supports S3-compatible backends without exposing credentials to edge nodes.

## Goals
- Durable storage for clips/DVR.
- Support local-only, S3-only, and hybrid modes.
- Keep edge nodes untrusted (presigned URLs).

## Non-Goals
- Forcing a specific S3 vendor.
- Redesigning MistServer storage layout.

## Proposal
- Introduce a shared storage interface (local/s3/hybrid) and consistent bucket layout.
- Keep presigned URL flow from Foghorn to Helmsman.
- Allow dev/self-hosted S3 via a compatible provider.

## Impact / Dependencies
- Helmsman storage manager.
- Foghorn S3 client + presigned URL APIs.
- Env config + deployment automation.

## Alternatives Considered
- Keep local-only storage.
- Use a full external media storage service.

## Risks & Mitigations
- Risk: inconsistent cache behavior. Mitigation: define clear source-of-truth rules per mode.
- Risk: S3 latency. Mitigation: hybrid caching mode.

## Migration / Rollout
1. Define storage interface + config.
2. Keep local default; add optional S3 mode.
3. Add hybrid mode with async upload.

## Open Questions
- Where should bucket layout live (Foghorn vs Helmsman config)?
- Do we need lifecycle policies for retention/cleanup?

## References, Sources & Evidence
- `api_sidecar/internal/handlers/storage_manager.go`
- `api_balancing/internal/storage/s3_client.go`

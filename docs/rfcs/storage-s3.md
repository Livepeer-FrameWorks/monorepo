# RFC: S3-Compatible Object Storage for Edge Clusters

## Status

Draft

## TL;DR

- Add a unified storage interface for clips/DVR with local, S3, and hybrid modes.
- Use presigned URLs so edge nodes never hold S3 credentials.
- Enable durable, shareable artifacts across edge nodes.

## Current State

- Foghorn owns S3 credentials and generates presigned URLs for clip, DVR, thumbnail, and
  VOD flows; Helmsman uses those URLs and does not hold S3 credentials.
- Foghorn has an S3-compatible client and stable key helpers, but there is still no
  provider-agnostic local/S3/hybrid storage abstraction package.
- Edge playback/cache behavior remains local-first for warm assets.

Evidence:

- `api_sidecar/internal/handlers`
- `api_sidecar/internal/control/client.go`
- `api_balancing/internal/storage`
- `api_balancing/internal/grpc/server.go`

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

## Self-Hosted S3-Compatible Storage

For sovereign operators who don't want AWS/GCP/Azure dependency, several S3-compatible object storage servers can run on bare metal or any Linux host.

The existing `s3_client.go` already supports S3-compatible endpoints via configurable endpoint URLs. Any compatible provider needs:

- `S3_ENDPOINT` pointed at the storage instance (e.g., `https://s3.cluster.example.com:9000`)
- `S3_ACCESS_KEY` and `S3_SECRET_KEY` for credentials
- `S3_BUCKET` for the target bucket
- `S3_FORCE_PATH_STYLE=true` (most self-hosted providers use path-style, not virtual-hosted-style URLs)

This aligns with the sovereignty thesis: operators who run their own edges should also be able to run their own object storage without cloud dependencies.

### Options

| Provider      | License               | Notes                                                                                                                                                                                                                                             |
| ------------- | --------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **SeaweedFS** | Apache 2.0            | Lightweight, S3-compatible, designed for distributed/edge deployments. Low resource overhead makes it a strong fit for single-node or small-cluster operators.                                                                                    |
| **Garage**    | AGPL-3.0              | Built for self-hosted geo-distributed S3. Designed to run across unreliable nodes with low resource usage. AGPL is fine here since operators run it for themselves, not as a service to third parties.                                            |
| **Ceph RGW**  | LGPL-2.1              | Mature S3 gateway on top of Ceph. Heavier to operate but battle-tested at scale. Best suited for operators already running Ceph or needing enterprise-grade durability.                                                                           |
| **MinIO**     | AGPL-3.0 + commercial | Well-known S3-compatible server. Recent licensing enforcement and commercial-only enterprise features make it risky to recommend or bundle. Operators who already have a MinIO license can use it, but it should not be a default recommendation. |

We do not mandate or bundle any specific provider. The storage interface is provider-agnostic by design — operators bring their own S3-compatible backend.

## Open Questions

- Where should bucket layout live (Foghorn vs Helmsman config)?
- Do we need lifecycle policies for retention/cleanup?

## References, Sources & Evidence

- `api_sidecar/internal/handlers`
- `api_balancing/internal/storage`

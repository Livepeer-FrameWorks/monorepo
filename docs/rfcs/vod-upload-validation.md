# RFC: VOD Upload Validation & Presigned URL Hardening

## Status

Proposed

## TL;DR

- The declared upload size is validated and quota-checked at presign, but the actual uploaded object is never measured against it.
- VOD multipart part URLs are signed for 2 hours; the generic PUT/GET presigns default to 15 minutes.
- Presigned part URLs carry no Content-Type condition.
- Container validity is established by the processing pipeline, not by a dedicated probe step.

## Owning services / modules

Foghorn (`api_balancing`) owns the remaining fixes: `internal/storage` (presign expiry, HeadObject) and `internal/grpc` (validation in CompleteVodUpload / the VOD pipeline). Container/track validation already lives in the processing pipeline. Bucket lifecycle rules live in infrastructure.

## Current State

User-facing VOD upload flow: GraphQL → Commodore → Foghorn → S3 presign.

1. **Declared size is enforced at presign, not at completion.** `CreateVodUpload` rejects
   `SizeBytes <= 0` (`api_balancing/internal/grpc/server.go` ~3156) and runs the declared size
   through `checkStorageEntitlement` (~3205), so an upload that would push the tenant over their
   storage cap is refused before any URL is minted. What is missing is the after-the-fact check:
   `CompleteVodUpload` (~3696) never HEADs the final object, so the actual byte count is never
   compared against the declared `sizeBytes`. A client that declares a small size and uploads a
   larger object is not detected here.
2. **Presign expiry is split.** `GeneratePresignedPUT` and `GeneratePresignedGET` default to
   15 minutes (`api_balancing/internal/storage/s3_client.go` ~154, ~206). Only
   `GeneratePresignedUploadPart` defaults to 2 hours (~586), and the VOD callers pass
   `2*time.Hour` explicitly (`server.go` ~3130 for re-signing an existing multipart, ~3288 for a
   fresh one). The 2-hour window is therefore specific to VOD multipart part URLs.
3. **No Content-Type conditions**: the `PresignUploadPart` call sets only
   bucket/key/uploadId/partNumber.
4. **Container validation happens in the processing pipeline, not via ffprobe.** ffprobe is not
   invoked anywhere in the services (it appears only in the real-MistServer test harnesses under
   `pkg/mist/`). A completed upload is booted as `processing+<hash>` in Mist; the header parse
   returns tracks and duration in `ProcessingJobResult`, and an invalid or unreadable container
   fails the job rather than producing an asset. See
   [`docs/architecture/processing-pipeline.md`](../architecture/processing-pipeline.md) and
   `api_balancing/internal/grpc/vod_pipeline.go`.
5. **No bucket lifecycle rule for incomplete multiparts.** Nothing in `infrastructure/` or
   `ansible/` configures `AbortIncompleteMultipartUpload`. In-app reconciliation compensates:
   `api_balancing/internal/jobs/aborting_vod_recovery.go` re-runs a stranded abort and
   `completing_vod_recovery.go` retries a stranded completion, so a crashed lifecycle converges
   without an operator. A multipart abandoned by a client that never calls complete or abort is
   still not reclaimed by the store itself.

## Problem / Motivation

- **Storage abuse**: the quota check runs on the declared size, so a client that uploads more than it declared exceeds its cap undetected.
- **URL leakage**: the 2-hour multipart window gives attackers time to exploit leaked part URLs.
- **Billing mismatch**: declared vs actual size affects metering and billing accuracy.
- **Abandoned multiparts**: a client that neither completes nor aborts leaves multipart parts the store never reclaims.

## Goals

- Enforce declared file size at upload completion
- Reduce the VOD multipart presign expiry window
- Reject uploads that violate constraints

## Non-Goals

- Full transcoding pipeline (separate RFC: processing-orchestration)
- Content moderation/scanning
- Container/track validation: the processing pipeline already establishes it (Current State, item 4)
- Replacing presigned direct-to-S3 upload. This RFC hardens the shipped path;
  [`edge-terminated-vod-upload.md`](edge-terminated-vod-upload.md) drafts the replacement separately.

## Proposal

### 1. HEAD-verify the actual size at completion (medium effort)

In `CompleteVodUpload`, after `CompleteMultipartUpload` succeeds and before/alongside
processing enqueue:

- Call `HeadObject` (`HeadObjectInfo` already returns size and ETag in one call) to get the
  actual S3 object size
- Compare against the declared `sizeBytes` from `foghorn.vod_metadata`
- Reject if actual size exceeds declared size by >10% (allow small variance for encoding)
- Delete the S3 object and mark the artifact failed if rejected

### 2. Content-Type conditions on presigns (low effort)

Bind the presigned part URLs to the declared container type so a part URL cannot be reused to
store unrelated content.

### 3. Shorten the multipart expiry (low effort)

Reduce the explicit `2*time.Hour` passed by the VOD callers. The store client's own PUT/GET
default is already 15 minutes; only the multipart path is long-lived.

### 4. Bucket lifecycle rule for incomplete multiparts (low effort)

Add an `AbortIncompleteMultipartUpload` lifecycle rule (24h) to the media buckets, as a
store-side backstop under the in-app abort/complete recovery jobs, plus a max object size limit.

## Impact / Dependencies

- `api_balancing/internal/storage` - expiry change, Content-Type conditions
- `api_balancing/internal/grpc` - HEAD verification in CompleteVodUpload
- Bucket lifecycle rules in `infrastructure/` / `ansible/`

## Alternatives Considered

- **Proxy uploads through a control-plane service**: puts media bytes on Foghorn or the Gateway. Terminating uploads on a processing-capable edge node is drafted in [`edge-terminated-vod-upload.md`](edge-terminated-vod-upload.md).
- **S3 condition keys on presign**: AWS SDK v2's `PresignUploadPart` doesn't support Content-Length conditions for multipart. Only works for single-part PutObject.
- **Client-side validation only**: Easily bypassed, provides no security.

## Risks & Mitigations

- Risk: Size validation rejects legitimate uploads with encoding variance. Mitigation: 10% tolerance threshold.
- Risk: A shorter multipart expiry breaks a slow client mid-upload. Mitigation: the re-sign path (`server.go` ~3130) already re-issues part URLs for an owned in-flight multipart.

## Migration / Rollout

1. Deploy expiry reduction (no migration needed)
2. Add HEAD size verification, monitor rejection rates
3. Add Content-Type conditions
4. Apply the bucket lifecycle rule

## Open Questions

- Should the size tolerance be configurable per tenant?
- Does the HEAD verification delete the object, or leave it for the existing recovery/purge jobs?

## References, Sources & Evidence

- [Evidence] `api_balancing/internal/storage/s3_client.go` - 15-minute PUT/GET defaults, 2-hour multipart default, `HeadObjectInfo`
- [Evidence] `api_balancing/internal/grpc/server.go` - declared-size rejection, `checkStorageEntitlement`, explicit 2-hour multipart presigns, multipart completion without HEAD
- [Evidence] `api_balancing/internal/grpc/vod_pipeline.go` - processing lifecycle that establishes container validity
- [Evidence] `api_balancing/internal/jobs/aborting_vod_recovery.go`, `completing_vod_recovery.go` - in-app compensation for stranded multiparts
- [Reference] AWS presigned URL best practices: short expiry, post-upload validation

# RFC: VOD Upload Validation & Presigned URL Hardening

## Status

Proposed

## TL;DR

- Presigned URLs for VOD uploads have no size/type enforcement
- 2-hour expiry window is excessive for the threat model
- No post-upload validation before marking assets ready

## Current State

User-facing VOD upload flow: GraphQL → Commodore → Foghorn → S3 presign.

Issues identified:

1. **No size enforcement**: User declares `sizeBytes` in GraphQL input, Foghorn calculates parts, but nothing enforces the declared size. Users can upload arbitrarily large files.
2. **2-hour presign expiry**: the S3 storage client defaults to 2 hours. Industry standard is 5-15 minutes.
3. **No Content-Type conditions**: `PresignUploadPart` call only sets bucket/key/uploadId/partNumber.
4. **No post-upload validation**: `CompleteVodUpload` marks assets ready immediately. TODO at `grpc` for ffprobe validation is unimplemented.

Evidence:

- `api_gateway/internal/resolvers` - sizeBytes passed without enforcement
- `api_balancing/internal/storage` - 2-hour expiry
- `api_balancing/internal/storage` - no conditions in presign
- `api_balancing/internal/grpc` - TODO for validation

## Problem / Motivation

- **Storage abuse**: Malicious users can exhaust storage quota by uploading larger files than declared
- **URL leakage**: 2-hour window gives attackers time to exploit leaked URLs
- **Invalid content**: Non-video files can be uploaded and marked ready, causing playback failures
- **Billing mismatch**: Declared vs actual size could affect metering/billing accuracy

## Goals

- Enforce declared file size at upload completion
- Reduce presign expiry window
- Validate uploaded content is playable video before marking ready
- Reject uploads that violate constraints

## Non-Goals

- Full transcoding pipeline (separate RFC: processing-orchestration)
- Content moderation/scanning
- Changing the presigned URL architecture (it's correct for this use case)

## Proposal

### 1. Reduce presign expiry (low effort)

Change default from 2 hours to 30 minutes in the S3 storage client. Still sufficient for large multipart uploads.

### 2. Post-upload size validation (medium effort)

In `CompleteVodUpload`, after `CompleteMultipartUpload` succeeds:

- Call `HeadObject` to get actual S3 object size
- Compare against declared `sizeBytes` from `foghorn.vod_metadata`
- Reject if actual size exceeds declared size by >10% (allow small variance for encoding)
- Delete the S3 object and mark artifact failed if rejected

### 3. Post-upload content validation (medium effort)

Before marking ready:

- Run ffprobe on the uploaded object (via S3 URL or presigned download)
- Verify it's a valid video container with video stream
- Extract and store metadata (duration, resolution, codecs)
- Mark failed if not valid video

### 4. Bucket-level safeguards (low effort)

Add S3 bucket policies:

- Max object size limit as backstop
- Lifecycle rule to auto-delete incomplete multipart uploads after 24h

## Impact / Dependencies

- `api_balancing/internal/storage` - expiry change, add HeadObject
- `api_balancing/internal/grpc` - validation in CompleteVodUpload
- New: ffprobe integration (could use existing processing infrastructure)
- Bucket policies via Terraform/infrastructure

## Alternatives Considered

- **Proxy uploads through backend**: Higher cost, latency, scaling burden. Presigned URLs are correct for video.
- **S3 condition keys on presign**: AWS SDK v2's `PresignUploadPart` doesn't support Content-Length conditions for multipart. Only works for single-part PutObject.
- **Client-side validation only**: Easily bypassed, provides no security.

## Risks & Mitigations

- Risk: ffprobe adds latency before ready state. Mitigation: async processing, show "processing" status to users.
- Risk: Size validation rejects legitimate uploads with encoding variance. Mitigation: 10% tolerance threshold.

## Migration / Rollout

1. Deploy expiry reduction (no migration needed)
2. Add size validation with feature flag
3. Add ffprobe validation with feature flag
4. Enable flags progressively, monitor rejection rates

## Open Questions

- Should size tolerance be configurable per tenant?
- Where should ffprobe run (Foghorn inline vs separate processing worker)?

## References, Sources & Evidence

- [Evidence] `api_balancing/internal/storage` - 2-hour expiry
- [Evidence] `api_balancing/internal/grpc` - TODO for validation
- [Reference] AWS presigned URL best practices: short expiry, post-upload validation

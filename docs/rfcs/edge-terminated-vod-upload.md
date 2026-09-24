# RFC: Edge-Terminated VOD Upload

## Status

Draft. Not scheduled; no target release. The shipped presigned direct-to-S3 upload remains the
only upload path. Hardening of that path is tracked separately in
[`vod-upload-validation.md`](vod-upload-validation.md).

## TL;DR

- Terminate VOD part uploads on a processing-capable edge node that Foghorn selects at
  `CreateVodUpload`, instead of presigning the cell's S3 multipart upload to the client.
- Run the upload transcode on that node from local bytes, and freeze the source and the processed
  output to the cell backend through Foghorn-minted, attempt-fenced operations.
- The public contract (create → part URLs → complete, status, abort) keeps its shape; the part URL
  points at the edge instead of the store.
- Gains: no S3 → node read-back of the source, exact byte enforcement, no client-held store write
  capability, uploads to backends that are not browser-reachable. Costs: edge disk for the whole
  source, a single-copy window until the source freeze completes, a new public write route on edges,
  and a multipart node-to-store push that Helmsman does not have today.

## Current State

### Upload lifecycle

1. GraphQL `createVodUpload` → Commodore → the origin cell's Foghorn `createVodUploadImpl`
   (`api_balancing/internal/grpc/server.go`). It checks storage entitlement against the declared
   size, requires the accepting (origin) cluster's storage to be this cell's backend, fences on the
   local backend fingerprint, creates an S3 multipart upload at `vod/<tenant>/<hash>/<hash>.<ext>`
   (`BuildVodS3Key`, `api_balancing/internal/storage/s3_client.go`), and presigns every part for
   2 hours. Parts default to 20 MiB (minimum 5 MiB, at most 10,000 parts; `CalculatePartSize`). The
   artifact row is inserted `status='uploading'` with `upload_expires_at` two hours out.
2. The browser uploader (`website_application/src/lib/uploads/engine.ts`) PUTs 4 parts concurrently,
   reads each part's `ETag` response header, persists the session for reload recovery, and resumes
   from server-reconciled completed parts.
3. `GetVodUploadStatus` reports database state; for a live session it lists uploaded parts from S3,
   behind the same backend-ownership fence. An expired session reports `EXPIRED` without an S3 call.
4. `CompleteVodUpload` persists a completion descriptor (key, upload id, ordered part ETags) and
   claims `completing` in one transaction before calling `CompleteMultipartUpload`.
   `completing_vod_recovery.go` retries a stranded completion, `aborting_vod_recovery.go` a
   stranded abort, and `purgeStaleUploadingVODs` (`jobs/purge_deleted.go`) claims expired sessions.
5. `VodPipeline.StartPipeline` (`grpc/vod_pipeline.go`) queues one `process` job, which transcodes.

### Processing placement and input

- `routeProcessingJob` (`api_balancing/internal/jobs/job_router.go`) honours `preferred_node_id`
  exclusively: when that node is not healthy, processing-capable, class-capable and
  tenant-eligible, the job is not dispatched ("preferred source node unavailable"). Without a
  preferred node it picks the lowest class load among alive nodes, with no locality. Upload
  transcodes set no preferred node.
- `recoverStale` (`jobs/processing_dispatcher.go`) terminally fails a queued job only when it has a
  preferred node and a node-local source kind (`live`, `dvr_rolling`); load-routed upload
  transcodes stay queued until capacity returns.
- Wrapper-safe uploads (mp4, mov, mkv, webm, ts) are read by Mist through the relay's
  `/internal/artifact/upload/` route, streamed from S3 memory-only. Wrapper-unsafe uploads (avi,
  flv, m4v) are staged to local disk first
  (`website_docs/src/content/docs/operators/media-storage.mdx`).

### Node-to-store writes

- Processing output is synced by the freeze flow: Foghorn claims an attempt and mints a single
  presigned PUT to a per-attempt staging key (`control/server.go`, freeze assignment), and Helmsman
  uploads the whole file with `UploadFileToPresignedURL` (`api_sidecar/internal/handlers/storage_manager.go`).
- Helmsman has no multipart upload client (`api_sidecar/internal/storage/presigned_client.go`), so
  every node-to-store object is bound by the S3 single-PUT limit of 5 GiB.

### Edge ingress and relay auth

- The activated edge Caddy config is rendered by Helmsman (`api_sidecar/internal/config/caddyfile.go`)
  and pushed through the Caddy admin API. The bootstrap templates (`cli/internal/templates/edge/`,
  the Ansible `edge` role) only answer health and 503 until activation.
- Public routes to Helmsman are `/webhooks/*`, `/_mist*`, `/health`, and `/internal/artifact/*`
  scoped to the per-node edge domain, with no read/write timeouts and no CORS headers.
- `peerAuthMiddleware` (`api_sidecar/internal/relay/server.go`) requires an opaque Foghorn grant,
  checked online through `AuthorizeRelayPull`, for any proxy-forwarded request. Grant-holding
  callers are read-only (non-GET/HEAD returns 405); PUT exists only for Mist's local write-back.

### Admission

Helmsman storage intents (`api_sidecar/internal/admission/admission.go`) cover DVR recording,
processing output, processing source staging, chapter finalization, unsafe import staging,
playback cache, processing input, and warm cache. None represents a client upload in progress.

## Problem / Motivation

1. **Source bytes cross the network twice before processing starts.** Client → S3, then S3 → the
   processing node. The read-back is billed egress on most S3 providers and adds time-to-ready
   proportional to file size.
2. **The client holds store write capability.** Part URLs are 2-hour presigned writes against the
   cell bucket, and the uploaded byte count is never compared with the declared size.
3. **The cell backend must be browser-reachable.** A backend on a private network, or one whose
   credentials live only on nodes, cannot accept a browser multipart upload. A browser also reads
   the part `ETag` only when the store's CORS policy exposes it.
4. **Upload throughput is bound to the bucket's region**, not to the uploader's nearest edge.

## Goals

- Clients upload parts to a Foghorn-selected edge node; the GraphQL, MCP, and proto shapes for
  create, status, complete, and abort are unchanged.
- The upload transcode runs on the receiving node from local bytes; the normal path has no S3
  read of the source.
- The actual byte count equals the declared size, enforced while bytes arrive.
- Source and output reach the cell's durable backend through Foghorn-minted, attempt-fenced
  operations; backend fingerprint ownership (invariant I2) is unchanged.
- Loss of the upload node before the source is durable becomes a visible terminal upload state,
  never a job queued forever.

## Non-Goals

- Keeping presigned direct-to-S3 as a permanent second upload path (see Alternatives).
- Cross-cell upload delegation; VOD upload stays local to the origin cell.
- Choosing a durable backend per upload ([`placement-policy-engine.md`](placement-policy-engine.md)).
- A new client upload protocol (tus or similar), content moderation, or container probing beyond
  what the processing boot already validates.

## Proposal

### 1. Node selection and reservation at create

`CreateVodUpload` keeps its existing checks (entitlement, local-mint, backend fingerprint) and
replaces the S3 multipart create with upload-node selection inside the origin cell:

- Candidates: alive, healthy, processing-capable, able to run the transcode class, and eligible
  for the tenant's cluster scope (the same predicates `routeProcessingJob` uses), excluding nodes
  in `draining` or `maintenance` mode.
- Ranking: geographic proximity to the client IP (the shared client-IP primitive used by the
  `/ingest` front door), then free disk, then class load.
- Reservation: Foghorn asks the chosen node over the control stream to admit an upload
  reservation sized to the declared bytes. A refusal moves to the next candidate; no candidate
  returns a typed `upload_capacity_unavailable` error.
- Persistence: `upload_node_id`, part size, and part count are written to the VOD metadata row in
  the existing create transaction. No store-side multipart upload exists at this point.

Each returned part URL is `https://<edge_domain>/internal/upload/<grant_id>/<part_number>`. The
grant is opaque, stored by Foghorn, and bound to (node, tenant, artifact hash, part count, part
size, declared size, expiry). The browser uploader keeps treating the URL as opaque.

### 2. Edge upload route

- A new Helmsman route group `/internal/upload/*`, separate from `/internal/artifact/*` so the
  read-only peer relay grant model is not widened.
- `caddyfile.go` adds an `@vod_upload` matcher scoped to the edge domain, reverse-proxied without
  timeouts, with a CORS preflight that allows `PUT` and exposes `ETag`.
- Helmsman authorizes each grant online through a new `AuthorizeUploadPart` control-stream call,
  failing closed on deny, error, or timeout; an allow is cached briefly per (grant, part).
- A part body must match the expected length (the last part carries the remainder). Bytes stream
  to `storage/upload/<hash>/parts/<n>` under the reservation, are fsynced, and are hashed with
  SHA-256; the hex digest is returned as `ETag`.
- A per-session part ledger in `HELMSMAN_STATE_DIR` survives a Helmsman restart. Re-sending a
  completed part replaces it until the session completes.

### 3. Status, complete, and abort

- **Status:** for a row with `upload_node_id`, Foghorn reconciles completed parts by asking the
  node over the control stream instead of listing S3 parts. An unreachable node reports database
  state with `last_error_code = upload_node_unavailable`, never an empty part list.
- **Complete:** the completion descriptor records ordered (part, digest) pairs. Foghorn claims
  `completing`, then sends `CompleteUpload` to the node. The node verifies every digest,
  assembles `upload/<hash>.<ext>`, confirms the total equals the declared size exactly, and
  converts the reservation into a processing source hold. Foghorn then moves the artifact to
  `processing` and queues the `process` job with `preferred_node_id = upload_node_id`.
  `completing_vod_recovery.go` retries the node call idempotently.
- **Abort and expiry:** the node deletes parts and releases the reservation.
  `aborting_vod_recovery.go` and `purgeStaleUploadingVODs` drive the node call instead of
  `AbortMultipartUpload`.

### 4. Processing on the receiving node

- The job carries a new node-local source kind `upload_local`. Helmsman's `STREAM_SOURCE` resolves
  the assembled local file for every container, so safe and unsafe wrappers take the same path
  and neither reads through the relay.
- When the preferred node is unavailable:
  - If the source freeze (§5) has published, the dispatcher clears `preferred_node_id`, switches
    the source to the relay upload read, and routes by load, as uploads do today.
  - If the source is not durable, `recoverStale` treats `upload_local` like the existing
    node-local kinds. The job and artifact fail with `upload_node_lost`, and the client creates a
    new upload.

### 5. Source durability and multipart freeze

- At complete, the node starts freezing the source to the key the presigned path uses today
  (`BuildVodS3Key(tenant, hash, filename)`), so relay resolution, purge, and billing attribution
  keep working. Processing runs concurrently.
- Freeze gains a multipart mode for objects above the single-PUT limit. Foghorn claims the attempt,
  creates the store multipart upload, and mints part PUTs to the node over the control stream.
  The node uploads them and reports part ETags; Foghorn completes the upload and HEAD-verifies the
  size before publishing the attempt. The same mode serves processing output and DVR chapters
  larger than 5 GiB, which today cannot be synced. The storage-placement transfer engine has the
  same single-PUT ceiling and should use this primitive.
- Local source parts and the assembled file become cleanup-eligible only when the source freeze
  has published and the processing job has completed. Artifact readiness keeps its existing rule:
  processed output synced.

### 6. Admission

A new `upload_source` intent is disk-required and ranks with processing output: below DVR
recording, above every cache intent. It is reserved at create for the declared size and released
on abort, expiry, or cleanup eligibility. A node refuses a reservation that could only fit by
evicting DVR recording.

## Impact / Dependencies

- **Foghorn:** create/status/complete/abort branches in `grpc/server.go`; `upload_local` source
  kind and preferred-node fallback in `jobs/job_router.go` and `jobs/processing_dispatcher.go`;
  grant store and `AuthorizeUploadPart`; multipart freeze mint and completion in
  `control/server.go`; the completing, aborting, and stale-upload recovery jobs.
- **Helmsman:** `/internal/upload` handler and part ledger, the `upload_source` admission intent,
  a multipart freeze client, and local source resolution in the processing handler.
- **Edge ingress:** `api_sidecar/internal/config/caddyfile.go` only; the bootstrap templates need
  no route.
- **Schema:** additive columns on Foghorn VOD metadata (`upload_node_id`, part size) and the
  processing source kind, in the pending catalog version once scheduled.
- **Proto:** `ipc.proto` control-stream messages (reserve, authorize part, part status, complete,
  abort, multipart freeze); comment changes on `VodUploadPart` and `CreateVodUploadResponse` in
  `shared.proto`. The GraphQL schema changes only description text ("presigned URLs").
- **Clients:** `engine.ts` retry classification for node errors (it currently treats a 403 as a
  non-retryable expired signature); MCP tool descriptions.
- **Docs:** `docs/architecture/processing-pipeline.md`, `operators/media-storage.mdx`,
  `builders/recordings.mdx`.
- **Other RFCs:** replaces items 1–3 of [`vod-upload-validation.md`](vod-upload-validation.md) for
  uploads. Item 4 (bucket lifecycle rule for incomplete multiparts) still applies to multipart
  freezes.

## Alternatives Considered

- **Status quo plus `vod-upload-validation.md` hardening.** Lowest cost; keeps the source
  read-back, client-held store writes, and the browser-reachable backend requirement.
- **Node as write-through proxy to the store multipart.** No edge disk and no single-copy window;
  keeps the read-back, adds a hop, and delivers only byte enforcement and private-backend support.
- **Both paths, chosen per upload.** Doubles the status, complete, abort, and recovery lifecycles
  for one feature. Rejected.
- **Terminate uploads on a control-plane service.** Puts media bytes on Foghorn or the Gateway.
  Rejected.
- **Pin the transcode to a node near the bucket without changing upload.** Shortens the read-back
  but does not remove it or address problems 2–4.
- **tus.** A new client protocol and server library while the part contract already resumes.
  Rejected.

## Risks & Mitigations

- **Edge disk exhaustion from concurrent large uploads.** Reservation at create, sized to the
  declared bytes; admission refuses rather than evicting DVR; selection ranks free disk.
- **Largest accepted upload shrinks.** S3 accepts objects up to 5 TiB; this path is bounded by the
  largest free reservation in the cell. Such uploads get a typed error at create, not a failure
  mid-upload.
- **Single-copy window.** The source freeze starts at complete. At 1 Gbit/s a 50 GiB source is
  durable in about 7 minutes. Node loss inside the window is a visible `upload_node_lost` failure.
- **Public write route on edges.** A separate route group from the read-only relay; online
  fail-closed grant checks; per-grant part count, part length, and total byte bounds.
- **Upload ingress competes with live egress.** Selection deprioritizes nodes by current egress
  load, and uploads never use nodes in `draining` or `maintenance` mode.

## Migration / Rollout

- The multipart freeze mode (§5) is independently useful and ships first; it removes the 5 GiB
  sync ceiling for processing output and DVR chapters.
- Nodes advertise upload-termination support in their capability report. During the fleet
  upgrade, Foghorn selects edge-terminated upload only when a capable candidate exists; each
  session keeps the mode recorded on its row (`upload_node_id` or `s3_upload_id`). Once the
  release's upgrade gate requires the capability, presigned part minting and its S3 part
  reconciliation are removed.
- Existing sessions are unaffected: presigned sessions complete or expire under the current
  recovery jobs.

## Open Questions

- Should the source object be retained once the processed output is ready (reprocessing, audit),
  or deleted to cut storage cost?
- Should uploads larger than any single reservation be rejected, or fall back to write-through
  spooling of parts to the store as they arrive?
- Should edge upload ingress appear in tenant usage, or only in node capacity telemetry?

## References, Sources & Evidence

- [Evidence] `api_balancing/internal/grpc/server.go` — `createVodUploadImpl`, `GetVodUploadStatus`,
  `CompleteVodUpload`, `AbortVodUpload`
- [Evidence] `api_balancing/internal/storage/s3_client.go` — `BuildVodS3Key`, `CalculatePartSize`,
  part size constants, presign helpers
- [Evidence] `api_balancing/internal/jobs/job_router.go` — `routeProcessingJob` preferred-node
  handling
- [Evidence] `api_balancing/internal/jobs/processing_dispatcher.go` — `recoverStale` queued-terminal
  predicate
- [Evidence] `api_balancing/internal/control/server.go` — freeze assignment with single presigned
  staging PUT
- [Evidence] `api_sidecar/internal/storage/presigned_client.go`,
  `api_sidecar/internal/handlers/storage_manager.go` — whole-file presigned PUT, no multipart client
- [Evidence] `api_sidecar/internal/relay/server.go` — route groups and `peerAuthMiddleware`
- [Evidence] `api_sidecar/internal/config/caddyfile.go` — activated edge routes
- [Evidence] `api_sidecar/internal/admission/admission.go` — storage intents
- [Evidence] `website_application/src/lib/uploads/engine.ts` — part concurrency, ETag handling,
  resume
- [Reference] [`docs/architecture/processing-pipeline.md`](../architecture/processing-pipeline.md)
- [Reference] [`website_docs/src/content/docs/operators/media-storage.mdx`](../../website_docs/src/content/docs/operators/media-storage.mdx)
- [Reference] [`vod-upload-validation.md`](vod-upload-validation.md),
  [`placement-policy-engine.md`](placement-policy-engine.md)

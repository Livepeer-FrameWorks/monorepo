# Artifact Processing Pipeline

> How every stored artifact — clip, uploaded VOD, and finalized DVR chapter — is produced,
> where its identity comes from, and where its A/V tracks / duration / readiness are
> **authoritatively** captured.

This is the canonical description of the flow. The older `docs/rfcs/processing-orchestration.md`
is a forward-looking RFC (live-processing policy is still partly out of scope) and must not be
read as the authoritative shape of the _implemented_ pipeline. Where the two disagree, this
document and the code win.

## The one-sentence model

**Every produced artifact is muxed by Helmsman under `processing+<artifact_hash>`, validated by
Helmsman's processing loop, published as `vod+<internal_name>`, and finalized in Foghorn from a
`ProcessingJobResult` keyed by the already-resolved `artifact_hash`.**

There are two phases:

1. **Origination** — a request creates a `foghorn.artifacts` row and enqueues a processing job.
   Three entry kinds: **clip-from-live**, **VOD-upload**, **DVR-chapter-finalize**.
2. **Processing & finalization** — the dispatcher sends the job to a processing-capable Helmsman
   node; Helmsman produces the output file and returns a validated `ProcessingJobResult`; Foghorn
   marks the artifact `ready`, registers its origin, and persists size/duration/**tracks**.

```
                          ┌────────── Phase 1: origination ──────────┐   ┌──── Phase 2: processing & finalization ────┐

  live stream ──clip──▶  Commodore.CreateClip ─▶ Foghorn.CreateClip ─┐
  upload ──────────────▶ Foghorn.CompleteVodUpload ─────────────────┤─▶ foghorn.processing_jobs ─▶ dispatcher ─▶ Helmsman
  live DVR ─chapter────▶ ChapterFinalizationQueue ──────────────────┘        (queued→dispatched      (processing+<hash>,
                                                                              →processing)             validate, mux, publish vod+)
                                                                                                              │
                                                                                     ProcessingJobResult{completed, tracks, …}
                                                                                                              │
                                                                          Foghorn processProcessingJobResult / handleChapterFinalizeResult
                                                                          → artifacts.status='ready', origin registered, tracks persisted
```

## Identity model (read this before anything else)

Two independent identifiers live on every `foghorn.artifacts` row:

| Field           | What it is                                                                    | Generated where                                                                                                                                                                                                                                                                                           |
| --------------- | ----------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `artifact_hash` | Stable content/registry key. **The join key** for jobs, node copies, catalog. | Commodore (authoritative) mints it — clip: `generateClipHash` in `api_control/internal/grpc/server.go`; VOD: `generateVodHash` in `api_balancing/internal/grpc/server.go`; chapter: deterministic `sha256("dvr_chapter:"+chapterID)[:32]` in `api_balancing/internal/jobs/chapter_finalization_queue.go`. |
| `internal_name` | Random routing name used to address the artifact at playback.                 | Commodore `generateArtifactInternalName` (32-char random) in `api_control/internal/grpc/server.go`.                                                                                                                                                                                                       |

**`artifact_hash` and `internal_name` are independently generated — neither is derived from the
other.** Any code that resolves one from the other must go through the DB / Commodore, never by
string transformation.

### Runtime prefixes are ROUTING classification, not a "finalization taxonomy"

`pkg/streamident/parse.go` classifies Mist runtime stream names by prefix. The **concrete token
after the prefix differs by kind** — this is the single most error-prone fact in the pipeline:

| Prefix            | Kind     | Concrete token           | Meaning                                                          |
| ----------------- | -------- | ------------------------ | ---------------------------------------------------------------- |
| `live+` / `pull+` | source   | source `internal_name`   | the live ingest stream                                           |
| `dvr+`            | artifact | artifact `internal_name` | the **rolling live DVR** playback surface (not a finalized file) |
| `vod+`            | artifact | artifact `internal_name` | playback token that resolves to a stored artifact file           |
| `processing+`     | artifact | **`artifact_hash`**      | the transient stream a job muxes its output from                 |

`streamident.IsArtifact()` groups `vod+`/`dvr+`/`processing+` together **for routing only**. Do
**not** read that grouping as "three equivalent finalization kinds":

- `dvr+` is the _live_ rolling surface; a finalized DVR chapter becomes an ordinary `vod+`
  artifact (`artifact_type='vod'`, `origin_type='dvr_chapter'`).
- `vod+<internal_name>` is a **playback resolution token**, not a second stored artifact — the
  only DB row is the single `clip`/`vod` artifact keyed by `artifact_hash`. Once signed media
  authority is marked ready, playback and `STREAM_SOURCE` resolve
  `vod+<internal_name>` → `artifact_hash` from Foghorn's durable verified index; the connected
  Commodore resolver remains only for unmarked mixed-version authority.
- `processing+<hash>` carries the **hash**, not a routing name.

`STREAM_PROCESS` is intentionally one-shot. The dispatch path persists the exact
process JSON under `HELMSMAN_STATE_DIR` before activating
`processing+<hash>`, and Helmsman reloads it before serving triggers after a
restart. Live ingest stamps the same policy and authority versions onto its
durable session, and rolling DVR stores its own snapshot. Foghorn restart
therefore reads local job/session state rather than polling Commodore or waiting
for a later trigger retry. Expired override records are reconciled with Mist at
startup and every minute: an active processing stream retains its policy, an
authoritatively absent stream loses the record, and a Mist API failure preserves
the last-good record for the next pass.

## Phase 1 — Origination

### Clip from a live stream

1. GraphQL `createClip` → `DoCreateClip` in `api_gateway/internal/resolvers/streams.go` → Commodore.
2. Commodore `CreateClip` in `api_control/internal/grpc/server.go` resolves the source
   stream's active ingest cluster, mints `artifact_hash` and an independent
   `internal_name`/`playback_id`, and forwards to that cluster's Foghorn.
3. Foghorn `CreateClip` in `api_balancing/internal/grpc/server.go` inserts the `clip` artifact
   row with both identifiers and enqueues a **`process`** job carrying `source_params`
   (source kind/stream/time span).
4. A live clip is a **passthrough/remux, never a fresh transcode**: the cut comes from the live
   shm buffer via a Mist `/view` request, and the job selects the complete renditions already in
   the cut (or the source), stripping any Livepeer transcode config
   (`api_sidecar/internal/handlers/processing_clip.go`). The staged cut carries no Mist track
   lineage, so Helmsman records the source tracks when it cuts: it reads the Tracks header of the
   same cut narrowed to `video=source` (Mist writes each track as stream index + 1) and of the staged
   file, and maps them onto the processing stream's tracks in file order
   (`processing_source_identity.go`). A cut declares every selected track, also one that holds none
   of its frames (a rendition replaced just before the cut); the processing stream does not
   register those, so Helmsman scans the staged file's blocks and leaves them out by the same rule
   (no frames, or video without a keyframe). A mapping that still does not line up fails the clip.

### Rendition completeness

An original can satisfy a ladder rung only when its pixels are upright and unreflected.
A rotated or mirrored original remains eligible as the decode source, but its rung must be
rendered. Helmsman carries both orientation fields through readiness, memory planning and
recording selection. Cached readiness without either field is not used to reduce the memory
reservation; the source must be inspected again.

`mist.ExpectedVideoRenditions` models both local MistProcAV and Livepeer/LPMS output sizes.
Height-only ladder profiles name the short side for portrait video. Named ladders use
`video=<0xN` inhibitors to compare the source short side with rung N, including square and
ultrawide sources. Explicit `video=<WxH` selectors retain their pixel-area comparison.
The model applies those inhibitors before matching each expected rung to a distinct recorded process
output; source tracks and thumbnails cannot satisfy a rung. Codec rounding is tolerated
within `max(32, shortSide/20)` pixels.

For VOD, Helmsman retains the largest source end timestamp observed at readiness, on each
five-second progress tick, and at completion. Fallback never lowers that baseline. Every
expected rendition must end within two seconds of it; an unknown source end fails closed.
This proof works when the recording masks the source and when a source sits between ladder
sizes. Clip and DVR selectors use the same geometry model, selecting the complete ladder
or the source passthrough, with their requested-span coverage checked separately.

### Rotated sources

Helmsman reads clockwise `rotation` from Mist health metadata. Missing metadata means zero,
so the same Helmsman works with Mist builds that do not report orientation. Width, height,
and resolution in VOD metadata describe display dimensions; `source_rotation` retains the
coded source orientation for diagnostics. The sizing model restores the coded dimensions
and applies `SourceMediaInfo.DisplaySize()` once.

A `mirror` flag reflects left-right after clockwise rotation and does not change display
dimensions. `source_mirror` retains that flag for the source-size rendition fallback.

`SOURCE_ROTATED` routes Livepeer work to local MistProcAV because MPEG-TS cannot carry the
orientation. Mirror-only sources take the same local route. This is normal routing and emits no transcode-degraded event. A rotated or mirrored source
below every rung gets a source-size AV entry before recording begins. The platform bundle
includes matching Helmsman and Mist support for source decoding and upright pixel processing.

Mist stores `rotation` as an unsigned 16-bit clockwise multiple of 90 and `mirror` as an
unsigned 8-bit boolean. It always writes both keys for video on the DTSC wire, with integer
mirror values 0/1. Track JSON omits zero rotation and false mirror. A change to either field
makes a resumed track incompatible. Older metadata without the fields reads as identity;
process-controlled inputs rebuild cached headers lacking either explicit key.

MP4/MOV track and movie matrices compose in track-then-movie order, including reflection.
Matroska converts its flip-first projection into rotate-then-mirror order; its roll stays
within (-180, 180]. FFmpeg input decomposes the full display matrix, preserving mirrored
0° and 180° sources.

Local AV and thumbnails apply orientation once at source resolution, before scaling.
Software decoding uses FFmpeg CPU transpose/flip filters. On macOS, the actual transcoder
uses VideoToolbox decoding, `transpose_vt`/`scale_vt`, and hardware encoding only
when `accel=hw` is selected. The default `accel=auto` retains software decoding
and encoding on macOS. CUDA may download
before CPU filtering. VAAPI is not a selectable ProcAV backend or a mandatory release dependency;
the generic filter helper supports caller-supplied VAAPI frames. Processed outputs carry upright
pixels and identity orientation. Progressive MP4 and Matroska passthrough retain full
orientation. CMAF and fragmented MP4 signal rotation only; reflected sources need a baked
rendition. Safari native HLS ignores the track matrix, and TS, FLV and SRT have no orientation
field, so those paths depend on the upright transcode. Mist does not inject orientation SEI
or negotiate WebRTC CVO.

A hardware decoder refusal before its first frame is permanent only in forced
hardware mode. Later decoder/device failures are retryable; the process does not
silently switch permanently to software or replay a cached GOP. Corrupt packets
are dropped, with a bounded consecutive-invalid-data guard to stop a silent stall.
Unconfigured live AV restarts use exponential backoff from one to thirty seconds;
explicit restart policy and controlled VOD retain their separate lifecycle rules.

### VOD upload

Commodore returns an opaque upload session ID containing the artifact locator
and the owner's multipart ID. Completion, status/resume and abort resolve the
tenant-owned VOD catalog row and contact that artifact's recorded origin, never
the tenant's current primary cluster. Missing ownership or an unavailable origin
fails closed. Foghorn independently validates the tenant-scoped multipart ID.
Clients retain the returned session ID unchanged; raw storage upload IDs are not
accepted by the public control API. No schema migration is needed for this route:
the VOD catalog already records `origin_cluster_id` at creation.

An S3 multipart lifecycle, both RPCs on Foghorn's gRPC server:

Upload and import container admission comes from `pkg/vodupload/formats.go`. Uploads allow
MP4, MOV, MKV, WebM, TS, M2TS, MTS, AVI, FLV and M4V. Imports allow the relay-readable subset
(MP4 through MTS); AVI/FLV/M4V uploads stage to local disk. Playlists are refused before any
upload row or multipart session. The dashboard's extensions, precheck and help text are
generated from that source by `make vod-formats`, checked by `verify-generated-contracts`.
Container admission does not supply a decoder: the installed Mist/FFmpeg build owns codec
support. An AC3/E-AC3 or other non-browser audio source requires a recorded AAC output when
the processing policy includes AAC; optional enrichment cannot publish silent browser video.
AAC output preserves supported source channel layouts, including 5.1; Opus output caps
channels at stereo and preserves mono.
The pinned FFmpeg build includes the MPEG-4 Part 2, MPEG-1/2, AC3, E-AC3 and MP2 decoders,
and local AV and thumbnails select the source decoder explicitly, including HEVC.

Existing `.dtsh` indexes rebuild lazily when their source is next opened. MP4 cache version 4
and Matroska version 4 retain the corrected sample timing, cue durations and display metadata;
accepting an older index would retain the old interpretation. MP4 rebuilds read sample tables,
while a Matroska rebuild reads the whole source object. Previously unopened library files are
not scanned, and a successfully saved rebuilt index is reused on later requests.

SubRip and WebVTT Matroska cues and MP4 tx3g cues carry their explicit duration through DTSC
and recording. Matroska writes subtitle BlockGroups with BlockDuration; WebM excludes subtitle
tracks. Native transport input detects 188-byte TS or 192-byte M2TS framing and indexes physical
packet boundaries, including HTTP range seeks. DVB E-AC3 descriptors and Blu-ray HDMV evidence
control interpretation of private audio stream types. Subtitle viewing uses its own `webvtt`
protocol in analytics.

The codec and orientation changes ship together in Mist. Validate the combined source in the
remotedev stack before publishing Mist. The v0.3.16 platform release workflow resolves the
published Mist once and bundles it into the edge artifacts and release manifest. No manual Mist
pin or separate platform release is needed. Stack scenarios 78 and 79 are required validation.
Scenario 79 also pushes a live Matroska source with a clockwise 90-degree projection. It requires
`source_rotated` routing for the active ingest runtime and upright HLS renditions and decoded
pixels through every Foghorn replica. The live publisher pushes to the edge's public URL: the
edge proxy on port 8082 on the stack (Mist's HTTP listener is loopback-only) and the public edge
HTTPS endpoint for a requested deployed smoke run; `STACK_LIVE_MKV_INGEST_URL` can override that
endpoint.

1. `CreateVodUpload` in `api_balancing/internal/grpc/server.go` creates the S3 multipart,
   mints/accepts the `vod` `artifact_hash`, and inserts the artifact row
   `status='uploading'`.
2. The client uploads parts directly to S3.
3. `CompleteVodUpload` finalizes the S3 object, flips the artifact to
   **`status='processing'`**, and queues a **`process`** job via
   `VodPipeline.StartPipeline` in `api_balancing/internal/grpc/vod_pipeline.go`. This job **is**
   a transcode (the sidecar's default branch).

### VOD import from a URL

An import is a VOD upload whose processing input is a tenant http(s) URL instead of an S3 object.
Foghorn never fetches it.

1. Commodore `ImportVodAsset` (`api_control/internal/grpc/vod_import.go`) validates the URL
   (http/https, no credentials, public destination via `validatePublicDestinationHost`) and the
   filename extension (mp4, mov, mkv, webm, ts, m2ts, mts: the relay-safe formats; avi/flv/m4v need a local
   file and are refused), registers the asset and its creation intent like an upload, and calls
   Foghorn.
2. Foghorn `ImportVodAsset` (`api_balancing/internal/grpc/vod_import.go`) writes, in one
   transaction, the `vod` artifact in **`status='processing'`** with no `s3_url`, the
   `vod_metadata` row with `source_url`, a normal **`process`** job with no job `source_url`,
   `upload.created`, and the ledger commit.
3. Mist's `STREAM_SOURCE` for `processing+<hash>` gets the node's relay URL
   `/internal/artifact/upload/<hash>.<ext>` (`resolveProcessSource`; `UploadedArtifactFormat`
   accepts an import's `source_url`). The job carries no source because a job source is handed to
   Mist directly and would bypass the relay.
4. On a relay miss, `RelayResolve` for the upload answers with `tenant_source_url`
   (`fillImportSourceResolve`) instead of a presign. The relay fetches it in block-aligned `Range`
   requests with its tenant-source client, which dials only public addresses on every connection
   and redirect (`api_sidecar/internal/relay/server.go`, `newTenantSourceClient`). The source must
   therefore support range requests. The relay records the source size it admitted to its block
   cache (`TakeProcessingInputSize`).
5. Once Mist has opened the source, Helmsman's first `ProcessingJobProgress` carries
   `source_size_bytes`. Foghorn sizes the import from it (`RecordImportSourceStaged`, first report
   only) and emits `upload.completed` with that size in the same transaction. A source that fails
   before this point (a 404, a non-media file) ends in `upload.failed` alone.
6. Processing and finalization are the upload path's: the processed output is the artifact's
   first stored copy.

### DVR (rolling) and chapter finalization

DVR has a _live_ phase and a _finalization_ phase; only the second enters the processing pipeline.

**Live phase (no processing job):**

1. `StartDVR` gateway → Commodore in `api_control/internal/grpc/server.go` → Foghorn
   `startDVR` in `api_balancing/internal/grpc/server.go`, which inserts the parent
   `artifact_type='dvr'` row with the retention/chapter policy snapshot and sends a
   `DVRStartRequest` to the storage Helmsman.
2. Helmsman records the source as rolling TS segments (`api_sidecar/internal/control/dvr_manager.go`).
   `dvr+<internal_name>` is the live playback surface (`api_balancing/internal/triggers/processor.go`).
   Its `STREAM_PROCESS` config is the `dvr_processes_json` snapshot stamped on the artifact row at
   `StartDVR` and read back by `resolveRollingDVRProcessConfig`, so DVR-specific Thumbs tracks survive a
   Foghorn restart and cache expiry. `safeRollingDVRProcessConfig` rejects any config on this lane that
   carries a Livepeer process: a Livepeer transcode requires an assigned signed job, which the rolling
   DVR lane does not have.
3. Each `RECORDING_SEGMENT` webhook appends a row to the **segment ledger**
   `foghorn.dvr_segments` (`status='pending'` → `uploaded`)
   (`api_balancing/internal/control/dvr_segments_repo.go`).

**Chapter finalization (enters the pipeline):**

4. A **chapter** is a bounded `[start_ms, end_ms)` slice tracked in
   `foghorn.dvr_chapters` (`open → closed → finalizing → finalized → frozen → reclaimed`).
   The **ChapterSweeper** (60s, `api_balancing/internal/jobs/chapter_sweeper.go`) rotates
   boundaries, closing a chapter (`open → closed`) — that is the finalize _trigger_.
5. The **ChapterFinalizationQueue** (30s + immediate wake,
   `api_balancing/internal/jobs/chapter_finalization_queue.go`) picks up `state='closed'`
   chapters, allocates the deterministic playback `artifact_hash`, inserts the **chapter VOD**
   row (`artifact_type='vod'`, `origin_type='dvr_chapter'`, `library_visible=false`,
   `status='finalizing'`), and dispatches a **`dvr_chapter_finalize`** job.
   Commodore registers that derived VOD only by joining the tenant-owned parent
   DVR and snapshots its `requires_auth`, playback-policy document, and encrypted
   webhook secret. A missing parent aborts registration; it can never manufacture
   a public chapter policy from a permissive column default.

> `api_balancing/internal/control/dvr_chapter_finalize_hook.go` is the **completion** handler
> (Phase 2), NOT the trigger. The trigger is the sweeper + queue above.

## Phase 2 — Processing & finalization

### The processing job (dispatcher + lifecycle)

Jobs live in `foghorn.processing_jobs`. The dispatcher is
`api_balancing/internal/jobs/processing_dispatcher.go`.

- **Lifecycle:** `queued` (insert) → `dispatched` (atomic claim, `FOR UPDATE SKIP LOCKED`) →
  `processing` (after the request is sent) → `completed`/`failed` (written by
  the result callback in `api_balancing/internal/control/server.go`, not the dispatcher). Stale
  `dispatched`/`processing` rows are requeued with backoff and eventually failed by
  `recoverStale()`.
- **`job_id`** is an independent UUID; **at most one _active_ job per `(artifact_hash, job_type)`**
  (advisory-lock dedup). Retries reuse the row. Results join back
  `processing_jobs.job_id → artifact_hash`.
- **Node selection:** `job_router.go` picks the lowest-loaded alive node with `CapProcessing`
  and per-class capacity among the nodes of the artifact's `origin_cluster_id`, whose storage
  receives the output. A job whose artifact has no recorded origin stays queued.
  Processing requires Helmsman control protocol 13, which echoes the dispatched tenant in
  every result. Placement skips known older sessions, and the owning connection checks the
  protocol again before sending work, including HA relays. A relayed protocol refusal
  preserves the connection owner and returns the same refusal code, so dispatch defers the
  job. Other control operations remain available while an edge waits for its upgrade.
- **Request:** `ProcessingJobRequest{job_id, tenant_id, artifact_hash, source_url, job_type,
internal_name, output_runtime_name, …}`. Crucially,
  **`output_runtime_name = "vod+" + internal_name`** (`processing_dispatcher.go`).
- **Retention:** `HistoryRetentionJob` (`api_balancing/internal/jobs/history_retention.go`)
  deletes `completed`/`failed` jobs 30 days after their last write, except a job another
  job names as parent and a clip job whose creation outcome Commodore has not consumed
  yet (its source params carry the clip's fulfilled range).

#### Thumbnail jobs

A clip or VOD gets its thumbnails from the one run that publishes it, where the Thumbs
process is inconsequential: a lost thumbnail (failed Thumbs process, refused mint, upload
that never landed) leaves the asset ready without thumbnails. `ThumbnailBackfillJob`
(`api_balancing/internal/jobs/thumbnail_backfill.go`) finds such assets every 10 minutes:
ready, locally stored and synced, at least an hour old, `has_thumbnails` false, no thumbnail
attempt other than a failed one, and no active job. It queues a `thumbnail` job carrying
only the Thumbs entries of the policy Commodore resolves for the lifecycle the asset was
published under (`clip`, `dvr_finalize` for chapter VODs, `vod`); a policy without Thumbs
records a failed job instead. An asset gets at most three thumbnail jobs inside the job
retention window, six hours apart, and at most two run cluster-wide at a time.

The dispatcher claims a thumbnail job although its asset is `ready` (and fails a queued one
whose asset is no longer ready). Helmsman stages the stored media, runs it through
`processing+<hash>` with only the Thumbs processes into a scratch recording, and waits until
the thumbnails Mist last announced were uploaded, because Foghorn mints a processing
stream's thumbnail upload only while its job is active. The result carries no output path;
completing the job, its progress and its failure never touch the asset's media, placement
or lifecycle.

Thumbnail publication recovery separates SQL settlement from object-store
projection and deletion. The partial ascending
`superseded_at` index lets superseded-version selection find a small GC batch
without scanning and sorting all published history before checking active pointers. The transaction still rechecks eligibility
under the asset lock before deleting a version or queuing its objects for cleanup.
Recovery failures identify the publishing, expired or superseded phase and whether
selection or settlement failed. `frameworks cluster diagnose thumbnails` measures
the selection plans and database waits per cell with bounded read-only probes;
these database results do not measure S3 projection latency.

### Helmsman execution

`ProcessingJobHandler.Handle` (`api_sidecar/internal/handlers/processing.go`) branches by
work kind:

| Branch    | Condition                                                         | Handler                                           | Output                                         |
| --------- | ----------------------------------------------------------------- | ------------------------------------------------- | ---------------------------------------------- |
| Chapter   | `job_type == "dvr_chapter_finalize"`                              | `handleChapterFinalize` (`processing_chapter.go`) | remux TS segments → `vod/<hash>.mkv`           |
| Thumbnail | `job_type == "thumbnail"`                                         | `handleThumbnail` (`processing_thumbnail.go`)     | thumbnails only; scratch recording removed     |
| Clip      | `isClipProcessingSource(req)` (source ∈ live/dvr_rolling/chapter) | `handleClip` (`processing_clip.go`)               | passthrough push → `clips/<stream>/<hash>.mkv` |
| VOD       | else (default)                                                    | inline in `Handle`                                | transcode → processed file                     |

The chapter, clip and VOD branches mux under `processing+<artifact_hash>`, then **validate the `RECORDING_END`** (reject
stale / failed / retired-generation events, verify output completeness against the authoritative
source span), generate the `vod+<…>` DTSH sidecar, and return the result via `sendCompletedResult`.

While the push runs, all three measure the recording, not the processing stream's source: every
five seconds Helmsman reads the push's own status from Mist's `push_list` (`mediatime`, `bytes`,
refreshed by the recorder about every five seconds) and the output file's size on disk. The
progress it reports carries the recorder's `mediatime`, and an attempt fails as stalled only when
none of the three advanced for three minutes. The source's `lastms` is no measure of the job:
the input can buffer the whole source while the buffer holds its feed for a recorder that is still
writing, so a 30-minute chapter's source can reach its end minutes before its recording does.

Processing progress and lease renewals are ephemeral: Helmsman sends them only while the Foghorn
control stream is connected, because replaying stale progress cannot renew current ownership. A
terminal `ProcessingJobResult` is a durable transition. The `cache_update` status is instead an
ephemeral observation because replaying an old override can regress the live job. If a terminal
result's immediate send fails, Helmsman fsyncs it to the local
`HELMSMAN_STATE_DIR/control-outbox` and replays it after reconnect. The same outbox carries the
other replay-safe terminal media transitions listed in
[Trigger durability](trigger-durability.md#separate-media-control-completion-outbox). Progress and
cache observations therefore cannot fill the bounded in-memory retry queue or evict a completion
during a control-plane outage; container recreation preserves pending results when the required
state volume is mounted.

Generic processing-job progress is made monotonic by persisting `GREATEST(current, reported)` and
emitting the returned value. Chapter-finalization progress has no persisted progress column; it is
attempt-fenced liveness telemetry and may move backward if reports arrive out of order. Completion
and failure remain attempt-fenced authoritative transitions.

### Finite-source recording readiness

Process-controlled realtime streams retain their buffered input after the finite
feeder reaches EOF, until processing readers and recording outputs have drained.
Internal processing readers can attach to an already-active buffer in `WAIT`;
requiring viewer-style `READY` would deadlock a late-starting processor against
the buffer that is waiting for it. Ordinary live viewers keep their normal
startup and admission path.

**Stable tracks.** A processing stream's track set is fixed before any data
flows. The feeder of a finished local file indexes the whole file first (the
`.dtsh` cache records that the index is complete) and registers exactly the
tracks that will carry data: tracks with at least one frame, and video tracks
with at least one keyframe. Every declared track it leaves out is logged with
the reason ("no frames", "no keyframe"), so a cut over a stopped rendition no
longer turns into an original the recording waits for. This is race-free for
very short sources, audio that starts after video, and one-frame tracks. Before
starting a process, the buffer reserves a track index for each output it can
derive from the configuration (Livepeer profiles that the source does not
inhibit, AV encodes, the thumbnail sprite, VTT and preview); the process claims
its reservations by output key. Reserved tracks are invisible to every reader,
`LIVE_TRACK_LIST` and stream info until claimed, and are released when their
process retires.

**Output identity and resumption.** Every process output carries a stable key:
a hash of the configured process (the configuration the buffer starts it with)
plus the output's name (Livepeer rendition/profile name, AV `video`/`audio`,
Thumbs `sprite`/`vtt`/`preview`, ONNX `output_id` and role, FFmpeg/exec track
number). A restarted process continues the tracks with its keys, and only those:
same index and ID, data after a gap, no `LIVE_TRACK_LIST` change. A process
whose output changed (another init or resolution, e.g. another orchestrator)
registers a replacement track under the same key, and the buffer removes the old
track at once. A replacement configuration (`PROCESS_REPLACE`, Livepeer → CPU
fallback) has its own identity: it never takes the replaced producer's tracks,
whose data stays in the stream. A live Livepeer process gives up a segment its
gateway had no result for within the segment's budget (its duration plus one
second), as it does a segment every gateway rejected, and continues with the
next; it exits unrecoverably, which brings the replacement, only after five
consecutive segments failed. A VOD or processing job cannot leave that hole and
falls back at the first. In a processing stream a restarted Livepeer run
starts at the first source keyframe after what its renditions already hold.
On live streams the buffer keeps a process output whose process is being
restarted, instead of idle-erasing it, until the restarted process claims it,
for at most the restart delay plus 30 seconds after the loss; the outputs of a
retired process, or of one configured without restarts, follow the normal idle
rule. The buffer publishes this hold per track (`resumeuntil`, already while the
producer runs), and so does a publisher's track kept for resume, so a bounded
read (a cut with a stop position, a scheduled recording) waits for a returning
producer instead of ending the track.

**Header rule.** Before writing a recording header, Mist's input buffer
publishes the number of existing push-visible derived tracks plus each eligible
producer's missing outputs, counted by output key (so a restarted producer's
resumed tracks count, and a reserved output without data is still missing).
Completed thumbnail tracks remain part of this total; they cannot satisfy the
pending output count of another video rendition. Process inhibition uses the
supervisor's track-ownership rules, and retired or inhibited producers no longer
contribute missing outputs. Thumbnail processing contributes sprite JPEG, VTT,
and preview JPEG tracks; the recording format still determines which of those
tracks can be embedded. Original video and audio tracks gate the header until
they have data; once the source has ended, the originals with data are exactly
the ones the header needs. No stream state releases the gate early.

**Completion rule.** A processing recording ends only once it played out the
whole buffer: every track in its header can receive no more data (an original
once the source ended, a derived track once its producer finished after the
source ended or was retired) and the recording has read each track to its last
packet. A buffer that disappears, signals or disconnects the recording before
that is a failure, reported as `SHM_LOST` (retried by Helmsman), also while
the recording still waits for its header; a killed buffer, which changes no
state, is noticed by its heartbeat on the stream state page stopping for 30 s.

**Memory admission.** Helmsman first reads source metadata with all process
inputs inhibited. Mist pauses that source feed while admission is pending, then
re-resolves the process override and starts the admitted ladder on the same input.
A refused admission stops the probe before the job returns to its queue. Helmsman reserves the work
that will run on the edge: Livepeer request buffers, local audio, thumbnails
and any local video encoders. A Livepeer job reserves its local fallback only
when that fallback is needed, before publishing or starting it. The reservation
is persisted with the job's process override, survives a Helmsman restart and
is released only after the processing stream stops.

The planner assigns explicit decoder and encoder thread counts within the
node's memory budget. It retains the encoder preset's lookahead wherever it
fits, reducing it for the largest renditions only when necessary. CPU scaling
uses bilinear filtering, including conversions inserted before transpose. Linux admission uses host available
memory and cgroup working-set pressure, excluding reclaimable inactive file
cache. On macOS it uses installed capacity and the reservation ledger.

The metadata probe is cached per job and source identity, so memory deferrals do
not repeatedly open the source. Every rendition's decoder concurrency follows the
source dimensions, independently of its output size. Known source-size inhibitors
are removed before publishing local encoders; they cannot start transiently outside
the estimate. A fallback drains the retiring generation and releases its reservation
before measuring available memory.

Helmsman distinguishes an undersized node from temporarily occupied memory and
reports minimum required bytes with the intended policy. Its processing budget,
after host/container limits and the system reserve, is authoritative for placement.
Foghorn retains each registered edge's last reported budget, including disconnected
nodes. Known non-processing edges contribute no processing budget; retired and evicted
nodes are excluded. An unknown budget or a warming
inventory cannot establish permanent incapacity. A previously refused node becomes
eligible when its reported budget grows enough to fit. Only a fully known cluster
that cannot fit the ladder fails the job and asset with a capacity error.

Deferred work stays queued with a reason: `memory`, `slots`,
`fitting_node_unavailable`, or `cluster_unavailable`. Deferral has no separate timeout
and spends no failure retries. Every processing job has a 24-hour deadline. The initial
VOD transcode uses upload creation time; clips, DVR, chapters, thumbnails and other
follow-up jobs use their own creation time. A new thumbnail job on an older ready VOD
gets its own deadline. A live-source clip or DVR job pinned to a node known to be gone
fails immediately with a "source unavailable" reason.
Completion, memory release, node recovery/undrain and budget increases wake the queue;
the periodic sweep recovers missed notifications. Busy-node replanning lowers threads
while preserving the quality selected against that node's full budget.

Placement is best-effort FIFO, with smaller jobs allowed to pass a blocked one.
After one minute the oldest memory waiter protects room on its best-fitting node.
Thumbnail jobs whose source is already ready retain the same reservation eligibility
and ordering. A disconnected hold moves when a healthy fitting node is available.
Other work must leave those bytes free. Protection survives dispatch until Helmsman
acknowledges actual durable memory admission, including admission on another node.
Versioned clears fence delayed updates, and persisted edge protection survives a
Helmsman restart. Waiting work holds no source or encoder processes. A fallback's
local policy survives redispatch, avoiding another paid Livepeer attempt.
Corrupt and unreadable sources fail immediately; source stalls and actual resource
failures retain their separate retry classifications.

At source EOF, Livepeer's drain deadline includes every remaining segment's
allowed resend budgets and sink flush time. A recording must still pass the
same full-source duration and rendition checks before publication.

**Buffer lifetime.** After source EOF the buffer stays up while a recording
reading it makes progress; a recording that made no progress for the stale
window (60 s) no longer keeps it alive. Tracks are not idle-erased while
processing readers are connected.

The EBML file recorder rejects packets from tracks absent from its header.
Helmsman's terminal validation remains authoritative for whether the artifact
is complete and publishable. Local regressions are available through
`make verify-mist-processing-recording`, `make verify-mist-finite-source`, and
`make verify-mist-hls-realtime`, with `MIST_CONTRACT_IMAGE` identifying the exact
full-feature build being tested.

Ordinary finite-file playback does not impose an implicit stop at the last video
timestamp: each selected track drains its own final sample. Nonfragmented MP4
durations include that final sample's duration so edit lists do not hide an audio
tail. Explicit clip stop times remain authoritative; unequal audio/video tails
are valid when the selected samples decode cleanly.

The finite-source contract also checks HTTP delivery of MP4 and MKV: one-byte,
suffix and open-ended ranges must match the corresponding full-file bytes, HEAD
must leave no response body on a reused connection, and rejected ranges return
416 with the total representation size. EBML enables a socket byte budget only
after sending its headers; other connections remain unbounded by default. MP4
uses its connector's remaining-byte counter. These bounds prevent bytes beyond
the declared response from corrupting a subsequent HTTP response.

### Completion authority — this is where tracks/duration/readiness are captured

Clip catalog rows retain their requested duration while processing or failed, until
a measured duration is available. An absent measurement must not erase that required
value or prevent lifecycle/failure projection. DVR and VOD durations remain nullable
until measured; catalog lifecycle, not duration alone, determines playback readiness.

The catalog reconciler projects artifacts for every media cluster currently
assigned to its Foghorn cell. Its control-cell ID is not a substitute for an
artifact's origin cluster: snapshot and deletion requests carry that media
origin, and Commodore enforces it alongside the per-artifact revision. Assignment
removal stops that origin's subsequent projection passes. A multi-cluster cell
does not guess ownership for an unattributed artifact from its own cell ID.

The validated result carries the accepted A/V track set:
`ProcessingJobResult{status:"completed", tracks, media_duration_ms, output_size_bytes, output_path}`
(`pkg/proto/ipc.proto` `ProcessingJobResult.tracks`).

Foghorn's `processProcessingJobResult` (`api_balancing/internal/control/server.go`):

- Routes chapter results (`chapter-finalize-v2-<attempt>-<chapter_id>` job_id) to
  `handleChapterFinalizeResult`; progress, retry, failure, and completion are
  fenced by the attempt, while node-originated writes additionally require the
  assigned node. Helmsman renews the durable ownership clock during every quiet
  staging/readiness phase and reports five-second progress while pushing; a
  newer attempt supersedes an older local owner before reusing the processing
  stream.
- For clip/VOD, the whole terminal transition is **one transaction**: it locks the job
  `FOR UPDATE` (a cancelled/deleted/duplicate job is a no-op — no resurrection), then the guarded
  readiness UPDATE keyed by `artifact_hash` (`status='ready'`, `format`, `size_bytes`,
  `duration_*`, **`tracks`**, `sync_status='pending'`, `storage_location='local'`), the
  `vod_metadata` fill, the origin node-copy registration, and the lifecycle outbox enqueue — with
  the job marked `completed` **last** — all commit together. Any failure rolls the transaction
  back, leaving the job dispatched/processing so stale recovery retries; a completed job is never
  left with an unready or unregistered artifact. In-memory placement + the reconciler wake are the
  only post-commit (best-effort) side effects.
- A `retryable` result requeues the job while its retry budget (3, shared with stale-lease and
  lost-job recovery) lasts. A `retryable` result whose `retry_cause` output is `source_stall` (the
  stream did not boot while the relay could not read the source from storage or upstream) leaves
  the budget alone: the run of stalls is bounded by the 20-minute no-progress window the progress
  watchdog applies to a running attempt, measured from the first stalled attempt's start
  (`source_stalled_since`), and each requeue waits as long as the run has lasted
  (`next_attempt_at`), never past the window. Media progress ends the run; a run past the window
  fails the job as timed out.
- A malformed completion (no `output_path`) and a `failed` result are BOTH driven to the terminal
  failed state atomically (`failProcessingJobAtomic`): the job and — for clip/VOD — the artifact
  flip to failed and the failure lifecycle enqueues in one committed transaction, so a failed job
  is never left with an artifact still "processing".

`handleChapterFinalizeResult` (`api_balancing/internal/control/dvr_chapter_finalize_hook.go`) does
the equivalent for the chapter VOD as one transaction (`finalizeChapterArtifactTx`): it locks the
chapter + its playback artifact, persists readiness/`vod_metadata`/origin, transitions the chapter
`finalizing → finalized` (requiring exactly one row), and enqueues the completion lifecycle,
committed together. A duplicate/late completion is an ignored no-op; a transient failure rolls back
and bounces the chapter `finalizing → closed` for re-dispatch.

**Track/duration/readiness capture happens ONLY here — never in the generic Foghorn
`handleRecordingEnd` trigger.** A raw `RECORDING_END` can be stale, from a failed or
retired-generation push, and fires _before_ Helmsman accepts the job; persisting from it would
stamp the catalog with unvalidated or truncated data. See the explicit note in
`api_balancing/internal/triggers/processor.go` `handleRecordingEnd`.

**Track set is captured with a presence bit.** `ProcessingJobResult.tracks_present` distinguishes
"this completed job captured the track set (replace the stored summary, even if empty)" from "no
track info (leave the prior summary untouched)" — so an authoritative empty set can clear stale
tracks while a track-less result doesn't clobber. The completion path sets it true (it carries a
validated `RECORDING_END`).

**Known limitation — parent-DVR tracks.** The rolling _parent_ DVR (`artifact_type='dvr'`) is a
segment/chapter ledger, not a processed file, so it never flows through a `ProcessingJobResult`
and its catalog **tracks** are not populated (its `duration` _is_, aggregated by `FinalizeDVR`).
A parent-DVR track summary would require an explicit chapter/source-track aggregation model,
which is deliberately out of scope for this release. Clips, VODs, and finalized DVR chapters all
carry tracks normally.

### Durable projection

The persisted `foghorn.artifacts.tracks` (and lifecycle fields) are projected onto the durable
Commodore catalog by the **artifact reconciler** (the sole catalog writer,
`api_balancing/internal/jobs/artifact_reconciler.go`) via `UpdateArtifactCatalogSnapshot`. See
[`clips-dvr.md`](./clips-dvr.md) for the catalog / read path.

A committed lifecycle mutation (delete, sync, finalize, processing exhaustion) triggers an
immediate pass on the replica that committed it. Projection runs under one cell-wide advisory
lock, so when a peer Foghorn replica holds it, the triggered replica retries the lock every two
seconds until it runs a pass; the change reaches the catalog seconds after the peer's pass ends
rather than on the five-minute fallback pass.

## Playback resolution

`vod+<internal_name>` and `dvr+<internal_name>` are resolved lazily at playback in the
STREAM_SOURCE trigger (`api_balancing/internal/triggers/processor.go`): strip the prefix →
`ResolveArtifactInternalName` (`internal_name → artifact_hash`) → build a Helmsman relay URL to
the stored file. No `vod+` artifact row is ever created; it is purely a resolution token.

## Critical files

| Concern                        | File                                                                                                                                                                     |
| ------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Clip create (identity minting) | `api_control/internal/grpc/server.go` (`CreateClip`), `api_balancing/internal/grpc/server.go` (`CreateClip`)                                                             |
| VOD upload                     | `api_balancing/internal/grpc/server.go` (`CreateVodUpload`/`CompleteVodUpload`), `api_balancing/internal/grpc/vod_pipeline.go`                                           |
| DVR live + segments            | `api_balancing/internal/grpc/server.go` (`startDVR`), `api_sidecar/internal/control/dvr_manager.go`, `api_balancing/internal/control/dvr_segments_repo.go`               |
| Chapter trigger                | `api_balancing/internal/jobs/chapter_sweeper.go`, `api_balancing/internal/jobs/chapter_finalization_queue.go`, `api_balancing/internal/control/dvr_chapter_generator.go` |
| Job dispatch + lifecycle       | `api_balancing/internal/jobs/processing_dispatcher.go`, `api_balancing/internal/jobs/job_router.go`                                                                      |
| Helmsman execution             | `api_sidecar/internal/handlers/processing.go`, `processing_clip.go`, `processing_chapter.go`                                                                             |
| Completion authority           | `api_balancing/internal/control/server.go` (`processProcessingJobResult`), `api_balancing/internal/control/dvr_chapter_finalize_hook.go` (`handleChapterFinalizeResult`) |
| Identity / prefixes            | `pkg/streamident/parse.go`                                                                                                                                               |
| Durable catalog projection     | `api_balancing/internal/jobs/artifact_reconciler.go`, [`clips-dvr.md`](./clips-dvr.md)                                                                                   |

## Original upload retention

The processed recording masks source A/V tracks and proves every expected rendition independently.
Original quality is retained as a separate opaque object, with no track identity or lineage.
Upload retention resolves upload override → tenant policy → keep. Foghorn snapshots that decision;
an omitted field from an older Commodore means false. URL imports and failed processing never stamp
an original. At successful processing completion, HEAD verification and original key/size stamping
precede the processed-size update on the same transaction. The original is counted once while the
processed output is local, then both objects after sync. Named ladders resolve per stream/upload →
tenant → tier and modify only tier-derived video profiles; operator overrides stay independent.

Published output uses an attempt-specific key even for an uploaded `.mkv`. Sync cleanup excludes the
retained original. Full delete, retention, and purge free it; original-only deletion clears metadata,
bumps the catalog revision and enqueues durable object cleanup together. It refuses to remove the
original while it is still the active key. Download grants require tenant ownership, recorded backend
ownership and an object size check, last 15 minutes, and use attachment disposition without playback
metering. Upgrades leave original retention inactive until the v0.3.16 Foghorn postdeploy
migration runs, after service upgrades and retirement of stale placements. Before activation, uploads
are accepted with original retention disabled for that upload. The upload response reports the accepted
`keepOriginal` policy and the dashboard warns if requested retention is unavailable. Once activated,
retention does not depend on capability heartbeats; a database read error returns a retryable failure.
Processing-completion stamping still
requires durable activation; a replica aging out of the heartbeat window cannot activate retention. Fresh baseline installs start activated because no older worker exists.
Rollback to older Foghorn cleanup code is prohibited by the release catalog.

Commodore enforces original-download grants at 20 per minute with a burst of five in a
database bucket shared across replicas and gateways. The gateway also rejects excess requests
locally. Original-download metadata reads do not lock the artifact or VOD metadata rows;
original deletion takes the artifact lock before rechecking and changing retention metadata.

The bundled Mist accepts explicit `encode_threads`, `decode_threads` and
`lookahead` resource controls. Helmsman budgets those controls directly.
The estimator is calibrated against twelve completed 30-second recordings: each catalog
ladder at 1080p and 2160p, under four- and sixteen-CPU quotas. The versioned corpus in
`pkg/mist/testdata/processing_memory_20261120.json` records the exact Mist source, native
artifact hash, process policies and per-process anonymous/shared peaks. Every recording
contains all planned renditions at full duration. The regression test requires estimates
to retain a 10–50% margin; file-cache pages are excluded from measured process memory.
Transient memory-read or reservation-persistence errors remain retryable; a missing
`active_streams` response cannot prove that an old generation has stopped.

Processing results echo the dispatched tenant in their outputs. Foghorn checks that tenant
against the assigned job before accepting a memory-policy update, capacity deferral or
terminal result. An empty or missing tenant is rejected; authenticated node identity
also has to match the job assignment.
Capacity-wait and stale-lease cutoffs use UTC when sent to the database timestamp columns.

A confirmed processor OOM kill (SIGKILL/137 plus a new kernel `oom_kill` count)
requeues the job as a memory wait rather than spending a tenant retry. Helmsman
samples RssAnon plus RssShmem for AV/Thumbs processes belonging to that exact source
and reads each observed PID's own cgroup memory.events, including native systemd services;
the next admission retains the larger of the observed peak and the admitted
reservation, plus 25% and 64 MiB. The required floor survives dispatch, replanning
and further deferrals. An ordinary SIGKILL still follows the normal retry policy.

Live fallback admission distinguishes a deferred replacement from an empty replacement. A
`PROCESS_REPLACE` object with `wait_reason` (`memory` or `node_unavailable`) and a positive
`retry_after_ms` keeps the failed process disabled while Mist retries local admission. An empty
array is terminal for that process. Helmsman announces released reservations through Mist's
`processing_capacity_released` API; waiting buffers recheck immediately. A one-second retry
also covers missed notifications and inventory recovery. Neither the notification nor the
waiting response authorizes an encoder: the durable ledger and current memory must admit the
replacement before an array of process configurations is returned.

A hardware-frame upload or device-to-device transfer failure ends the current AV process with
a process-specific error before encoder submission. It does not silently skip that frame.
The processing supervisor applies the existing bounded retry policy to the failed attempt.
CUDA decoding remains disabled until the NVIDIA lifecycle gate passes; hardware encoding and
forced VideoToolbox decoding retain their existing selection rules.

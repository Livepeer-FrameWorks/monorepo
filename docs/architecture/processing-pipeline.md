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

| Field           | What it is                                                                    | Generated where                                                                                                                                                                                                                                                                                                                   |
| --------------- | ----------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `artifact_hash` | Stable content/registry key. **The join key** for jobs, node copies, catalog. | Commodore (authoritative) mints it — clip: `generateClipHash` (timestamp+random) `api_control/internal/grpc/server.go:8002`; VOD: `generateVodHash` `api_balancing/internal/grpc/server.go:2901`; chapter: deterministic `sha256("dvr_chapter:"+chapterID)[:32]` `api_balancing/internal/jobs/chapter_finalization_queue.go:298`. |
| `internal_name` | Random routing name used to address the artifact at playback.                 | Commodore `generateArtifactInternalName` (32-char random) `api_control/internal/grpc/server.go:8045`.                                                                                                                                                                                                                             |

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
  only DB row is the single `clip`/`vod` artifact keyed by `artifact_hash`. Playback resolves
  `vod+<internal_name>` → `artifact_hash` via Commodore in the STREAM_SOURCE trigger
  (`api_balancing/internal/triggers/processor.go:2057`).
- `processing+<hash>` carries the **hash**, not a routing name.

## Phase 1 — Origination

### Clip from a live stream

1. GraphQL `createClip` → `DoCreateClip` (`api_gateway/internal/resolvers/streams.go:351`) → Commodore.
2. Commodore `CreateClip` (`api_control/internal/grpc/server.go:8128`) resolves the source
   stream's active ingest cluster, mints `artifact_hash` (`:8199`) and an independent
   `internal_name`/`playback_id` (`:8204`), and forwards to that cluster's Foghorn.
3. Foghorn `CreateClip` (`api_balancing/internal/grpc/server.go:771`) inserts the `clip` artifact
   row with both identifiers (`:1001`) and enqueues a **`process`** job carrying `source_params`
   (source kind/stream/time span) (`:1053`).
4. A live clip is a **passthrough/remux, never a fresh transcode**: the cut comes from the live
   shm buffer via a Mist `/view` request, and the job selects the complete renditions already in
   the cut (or the source), stripping any Livepeer transcode config
   (`api_sidecar/internal/handlers/processing_clip.go:60`).

### VOD upload

An S3 multipart lifecycle, both RPCs on Foghorn's gRPC server:

1. `CreateVodUpload` (`api_balancing/internal/grpc/server.go:2909`) creates the S3 multipart,
   mints/accepts the `vod` `artifact_hash` (`:2947`), and inserts the artifact row
   `status='uploading'` (`:3000`).
2. The client uploads parts directly to S3.
3. `CompleteVodUpload` (`:3240`) finalizes the S3 object, flips the artifact to
   **`status='processing'`** (`:3319`), and queues a **`process`** job via
   `VodPipeline.StartPipeline` (`api_balancing/internal/grpc/vod_pipeline.go:45`). This job **is**
   a transcode (the sidecar's default branch).

### DVR (rolling) and chapter finalization

DVR has a _live_ phase and a _finalization_ phase; only the second enters the processing pipeline.

**Live phase (no processing job):**

1. `StartDVR` gateway → Commodore (`api_control/internal/grpc/server.go:2381`) → Foghorn
   `startDVR` (`api_balancing/internal/grpc/server.go:1284`), which inserts the parent
   `artifact_type='dvr'` row with the retention/chapter policy snapshot (`:1413`) and sends a
   `DVRStartRequest` to the storage Helmsman (`:1563`).
2. Helmsman records the source as rolling TS segments (`api_sidecar/internal/control/dvr_manager.go:667`).
   `dvr+<internal_name>` is the live playback surface (`api_balancing/internal/triggers/processor.go:1979`).
3. Each `RECORDING_SEGMENT` webhook appends a row to the **segment ledger**
   `foghorn.dvr_segments` (`status='pending'` → `uploaded`)
   (`api_balancing/internal/control/dvr_segments_repo.go:224`).

**Chapter finalization (enters the pipeline):** 4. A **chapter** is a bounded `[start_ms, end_ms)` slice tracked in `foghorn.dvr_chapters`
(`open → closed → finalizing → finalized → frozen → reclaimed`). The **ChapterSweeper**
(60s, `api_balancing/internal/jobs/chapter_sweeper.go:52`) rotates boundaries, closing a
chapter (`open → closed`) — that is the finalize _trigger_. 5. The **ChapterFinalizationQueue** (30s + immediate wake,
`api_balancing/internal/jobs/chapter_finalization_queue.go:40`) picks up `state='closed'`
chapters, allocates the deterministic playback `artifact_hash`, inserts the **chapter VOD**
row (`artifact_type='vod'`, `origin_type='dvr_chapter'`, `library_visible=false`,
`status='finalizing'`) (`:540`), and dispatches a **`dvr_chapter_finalize`** job (`:381`).

> `api_balancing/internal/control/dvr_chapter_finalize_hook.go` is the **completion** handler
> (Phase 2), NOT the trigger. The trigger is the sweeper + queue above.

## Phase 2 — Processing & finalization

### The processing job (dispatcher + lifecycle)

Jobs live in `foghorn.processing_jobs`. The dispatcher is
`api_balancing/internal/jobs/processing_dispatcher.go`.

- **Lifecycle:** `queued` (insert) → `dispatched` (atomic claim, `FOR UPDATE SKIP LOCKED`,
  `:94`) → `processing` (after the request is sent, `:371`) → `completed`/`failed` (written by
  the result callback in `api_balancing/internal/control/server.go`, not the dispatcher). Stale
  `dispatched`/`processing` rows are requeued with backoff and eventually failed by
  `recoverStale()` (`:552`).
- **`job_id`** is an independent UUID; **at most one _active_ job per `(artifact_hash, job_type)`**
  (advisory-lock dedup, `:755`). Retries reuse the row. Results join back
  `processing_jobs.job_id → artifact_hash`.
- **Node selection:** `job_router.go:21` picks the lowest-loaded alive node with `CapProcessing`
  and per-class capacity.
- **Request:** `ProcessingJobRequest{job_id, tenant_id, artifact_hash, source_url, job_type,
internal_name, output_runtime_name, …}`. Crucially,
  **`output_runtime_name = "vod+" + internal_name`** (`processing_dispatcher.go:337`).

### Helmsman execution

`ProcessingJobHandler.Handle` (`api_sidecar/internal/handlers/processing.go:465`) branches by
work kind:

| Branch  | Condition                                                         | Handler                                           | Output                                         |
| ------- | ----------------------------------------------------------------- | ------------------------------------------------- | ---------------------------------------------- |
| Chapter | `job_type == "dvr_chapter_finalize"`                              | `handleChapterFinalize` (`processing_chapter.go`) | remux TS segments → `vod/<hash>.mkv`           |
| Clip    | `isClipProcessingSource(req)` (source ∈ live/dvr_rolling/chapter) | `handleClip` (`processing_clip.go`)               | passthrough push → `clips/<stream>/<hash>.mkv` |
| VOD     | else (default)                                                    | inline in `Handle`                                | transcode → processed file                     |

All three mux under `processing+<artifact_hash>`, then **validate the `RECORDING_END`** (reject
stale / failed / retired-generation events, verify output completeness against the authoritative
source span), generate the `vod+<…>` DTSH sidecar, and return the result via `sendCompletedResult`.

### Completion authority — this is where tracks/duration/readiness are captured

The validated result carries the accepted A/V track set:
`ProcessingJobResult{status:"completed", tracks, media_duration_ms, output_size_bytes, output_path}`
(`pkg/proto/ipc.proto` `ProcessingJobResult.tracks`).

Foghorn's `processProcessingJobResult` (`api_balancing/internal/control/server.go`):

- Routes chapter results (`chapter-finalize-<id>` job_id) to `handleChapterFinalizeResult`.
- For clip/VOD, the whole terminal transition is **one transaction**: it locks the job
  `FOR UPDATE` (a cancelled/deleted/duplicate job is a no-op — no resurrection), then the guarded
  readiness UPDATE keyed by `artifact_hash` (`status='ready'`, `format`, `size_bytes`,
  `duration_*`, **`tracks`**, `sync_status='pending'`, `storage_location='local'`), the
  `vod_metadata` fill, the origin node-copy registration, and the lifecycle outbox enqueue — with
  the job marked `completed` **last** — all commit together. Any failure rolls the transaction
  back, leaving the job dispatched/processing so stale recovery retries; a completed job is never
  left with an unready or unregistered artifact. In-memory placement + the reconciler wake are the
  only post-commit (best-effort) side effects.
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

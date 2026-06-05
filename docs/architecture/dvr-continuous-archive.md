# DVR Continuous Archive — Architecture

## What this delivers

24/7 DVR with no per-artifact lifetime cap. One DVR artifact spans the entire stream session, regardless of whether the stream ran for 10 minutes or 10 months. Live viewers see a tier-bounded rolling window (Mist `targetAge` + `maxEntries` + `nounlink=1`). Replay viewers navigate the recording sliced into **chapter VOD artifacts** — the chapter finalization queue remuxes each closed chapter's TS segment range into a canonical `.mkv` with `.dtsh` + (optional) Chandler thumbnail tracks, and chapter playback uses the same path as any other VOD artifact.

Live DVR is designed as a continuous archive rather than a short rolling buffer or per-broadcast asset rotation. Chapter finalization makes long sessions navigable without splitting the source recording into separate DVR artifacts.

This file is the canonical engineering reference. `docs/architecture/clips-dvr.md` covers the broader artifact storage model; this file is the DVR-specific overlay. Standards (chapter ID stability, mode validation, public addressing) live in `docs/standards/dvr-chapters.md`.

## Three timescales — separated

| Concept             | What it controls                                               | Where it lives                                                              |
| ------------------- | -------------------------------------------------------------- | --------------------------------------------------------------------------- |
| **Live DVR window** | how far back live viewers can seek while recording             | Mist `targetAge` / `maxEntries`, sized per tier                             |
| **Stream session**  | start→end of one logical recording = one DVR artifact lifetime | `foghorn.artifacts` row, `recording → finalizing → completed` state machine |
| **Retention**       | how long a terminal artifact's bytes/rows are kept             | `retention_until` set at FinalizeDVR as `ended_at + dvr_retention_days*24h` |

These never share a clock. A 24/7 stream lasting 90 days is **one** active artifact. Retention only ticks after the artifact reaches a terminal state — it never kills an active recording.

## Source of truth: `foghorn.dvr_segments`

The per-segment ledger is the durable timeline. Every Mist `RECORDING_SEGMENT` becomes one row, written by Foghorn via the helmsman control stream:

```
artifact_hash    VARCHAR(32)   -- = dvr_hash
segment_name     TEXT
sequence         BIGINT        -- Foghorn-assigned, monotonic per artifact
media_start_ms   BIGINT
media_end_ms     BIGINT
duration_ms      BIGINT
size_bytes       BIGINT
s3_key           TEXT
status           VARCHAR(20)   -- pending | uploaded | failed_upload | deleted_local | orphan_unreachable | lost_local | reclaimed
drop_reason      VARCHAR(32)   -- disk_pressure | retention_expired | operator_cleanup | upload_failed
```

There are **no chapter manifests in S3**. The per-segment freeze exists as a temporary recovery bridge for chapter finalization (so the remux can recover from local segment loss) and is deleted by the reclaim sweep once the chapter artifact + `.dtsh` are durable on S3.

## Stable physical S3 layout

```
s3://bucket/dvr/{tenant_id}/{stream_internal_name}/{dvr_artifact_id}/segments/{segment_name}   -- temp recovery
s3://bucket/vod/{tenant_id}/{chapter_artifact_hash}.mkv                                        -- chapter artifact
s3://bucket/vod/{tenant_id}/{chapter_artifact_hash}.mkv.dtsh                                   -- chapter .dtsh
```

DVR segment objects are recovery-only — they're never read for playback. Once every chapter overlapping a segment reaches `state='frozen'`, the reclaim sweep deletes the local TS file and the S3 segment object together.

## Chapter pipeline

A chapter is a `(start_ms, end_ms)` slice of the artifact's ledger, finalized into its own VOD artifact. Mode is configured per-stream and snapshotted at StartDVR:

```
window_sized_chapters    sequential fixed-length chapters of size tier.MaxWindowSeconds
fixed_interval           UTC-only interval_seconds buckets (≥3600s) anchored at unix epoch 0
```

`explicit_range` is retired. `setDVRChapterPolicy` is retired. Mode changes take effect at the next recording.

**UTC-only API surface.** Civil-time (e.g. "yesterday in Europe/Amsterdam") is resolved by the webapp to UTC `(start_ms, end_ms)` before submission. Foghorn stores and operates on UTC epoch ms only.

### Chapter state machine

```
open → closed → finalizing → finalized → frozen → reclaimed
                     ↓
                     └→ failed_source_missing | failed_permanent
```

| State                   | Meaning                                                                                          |
| ----------------------- | ------------------------------------------------------------------------------------------------ |
| `open`                  | Recording in progress; rolling-DVR surface serves viewers.                                       |
| `closed`                | Boundary reached; the finalization queue will pick it up.                                        |
| `finalizing`            | Processing job in flight (Mist remuxes TS → canonical `.mkv` via processing+&lt;hash&gt;).       |
| `finalized`             | PUSH_END fired; chapter VOD artifact exists locally. Waiting on freeze + `.dtsh` sync.           |
| `frozen`                | Artifact + `.dtsh` durable on S3. Safe to reclaim source segments.                               |
| `reclaimed`             | Source TS + temp S3 segments deleted; the chapter row remains as range metadata.                 |
| `failed_source_missing` | Recovery exhausted — at least one overlapping segment was missing from both local and S3 freeze. |
| `failed_permanent`      | Unrecoverable input.                                                                             |

A chapter is **playable** at `finalized` or later — the canonical `.mkv` is the playback target.

### Pipeline workers (Foghorn)

| Worker                          | Cadence | Job                                                                                                                                            |
| ------------------------------- | ------- | ---------------------------------------------------------------------------------------------------------------------------------------------- |
| `chapter_sweeper.go`            | 60s     | Rotates chapter boundaries on active DVRs: closes the current chapter, opens the next.                                                         |
| `chapter_finalization_queue.go` | 30s     | Picks up `state='closed'` chapters; allocates the playback artifact hash; mints the Commodore public playback ID; dispatches the finalize job. |
| `chapter_reclaim_sweep.go`      | 60s     | Once a chapter reaches `frozen`, runs the two-phase reclaim (Helmsman local delete, then S3 temp-segment delete).                              |

### Finalize dispatch

`chapter_finalization_queue.dispatchChapter`:

1. Read parent DVR (tenant, stream, origin cluster, recording node).
2. Range-query `foghorn.dvr_segments`; abort with `failed_source_missing` if the range is empty.
3. Build per-segment refs. Uploaded / deleted_local rows carry a presigned recovery URL minted by Foghorn; lost_local rows carry one too when an S3 object survives the local-loss. A presign error is fail-retryable — the chapter rolls back to `closed`.
4. Resolve the tenant's `dvr_finalize` `processes_json` via Commodore, then fill the Livepeer `hardcoded_broadcasters` list from the cluster's gateway instances (`ApplyLivepeerBroadcasters`) + cache for the STREAM_PROCESS trigger. A miss here is fail-retryable; the chapter stays `closed` rather than finalizing without the tenant's configured pipeline.
5. `MarkChapterFinalizing` — transitions `closed → finalizing` with the chapter's playback artifact hash.
6. Pick the dispatch target: recording origin if alive, otherwise an alternate processing-capable node selected via `routeProcessingJob` (works whenever every ref has a recovery URL — the alternate Mist reads from S3).
7. Dispatch `ProcessingJobRequest{job_type='dvr_chapter_finalize'}` with the resolved processes_json. On send error, `RetryChapterFinalize` rolls back to `closed`.

### Helmsman finalize handler

`processing_chapter.go::handleChapterFinalize`:

1. Reserve disk via `admission.Decide(IntentDVRChapterFinalization, estBytes)` — sum of source segment sizes is the floor.
2. For each segment, prefer the local TS file at `storage/dvr/<stream>/<dvr_hash>/segments/<name>`; fall back to the presigned recovery URL when the local file is gone.
3. Build a temp HLS VOD playlist at `storage/processing/<chapter_artifact_hash>.m3u8`. Each entry carries `#EXT-X-PROGRAM-DATE-TIME` rendered from `media_start_ms` directly (absolute Unix ms), so Mist's `input_hls → UTCOffset → output_ebml` chain preserves wall-clock end-to-end into the `.mkv`.
4. Register a STREAM_SOURCE override mapping `processing+<chapter_artifact_hash>` → the local temp HLS path, and a STREAM_PROCESS override carrying the thumbs-only `processes_json`. Mist boots `processing+<hash>`; MistProcThumbs generates fresh poster/sprite tracks for the chapter timeline during this boot.
5. Push to `storage/vod/<chapter_artifact_hash>.mkv`. Wait for `PUSH_END` (success) or `PROCESS_EXIT` on a critical process (terminal). Non-critical exits, retries, and clean exits don't break the wait loop.
6. Validate the output via `waitForProcessingOutput`. Send `ProcessingJobResult{status='completed', output_path}`.
7. Trigger DTSH generation: boot `vod+<chapter_artifact_hash>` so Mist's input writes the `.dtsh` sidecar that the freeze pipeline uploads alongside the `.mkv`. This boot is for DTSH only — it does NOT generate thumbnails (that's the processing pipeline's job above).

### Foghorn result handler

`dvr_chapter_finalize_hook.go::handleChapterFinalizeResult`:

1. Update `foghorn.artifacts.status='ready'`, format/size/sync_status, warm-cache registration on the producing node.
2. `MarkChapterFinalized` — transitions `finalizing → finalized` with `segment_count` and `has_gaps`.
3. Upsert `foghorn.vod_metadata` from Helmsman's stream-info outputs (duration, resolution, codecs, fps) so the chapter behaves like any other VOD on the player side.

When the freeze pipeline observes the chapter artifact's `sync_status='synced' AND dtsh_synced=true`, the chapter advances to `state='frozen'` and `frozen_at=NOW()` is set.

## Reclaim sweep (Foghorn)

Two phases; both gated by `MarkChapterReclaimStarted` (5-min freshness) so concurrent workers don't issue duplicate orders:

- **Phase A — Helmsman-side local delete.** Foghorn sends `ReclaimDVRSegment(dvr_hash, segment_names)` to Helmsman. Helmsman deletes the local TS file (working both with an active DVR job and post-stop via a deterministic `storage/dvr/*/<dvr_hash>/segments` scan) and emits `DVRSegmentDropped(was_uploaded=true)`, which moves the ledger row to `deleted_local`.
- **Phase B — Foghorn S3 delete.** For each segment now in `deleted_local` or `orphan_unreachable`, Foghorn deletes the temporary S3 segment object and transitions the row to `reclaimed`.

When the recording origin is gone past the abandoned-node grace (`chapterReclaimAbandonNodeGrace`, anchored on `frozen_at`), Foghorn marks non-terminal segments `orphan_unreachable` — a separate authority from `deleted_local` (which only means Helmsman acknowledged the delete via `DVRSegmentDropped`). Phase B accepts both for S3 delete. On node rejoin, startup reconcile sees `orphan_unreachable + present file`, deletes the file, and emits `DVRSegmentDropped` so the row reconciles to `deleted_local` ahead of Phase B.

When every segment overlapping the chapter is `reclaimed` (or `lost_local`), the chapter advances to `state='reclaimed'`.

## Public addressing — `playbackId`

Chapter VOD artifacts are addressed by the Commodore-minted public `playback_id` stored in `commodore.dvr_chapter_playback` and cached on `foghorn.dvr_chapters.playback_id`. The cache is non-authoritative; `commodore.dvr_chapter_playback` is the single source of truth.

- Raw artifact hashes are never accepted as chapter playback IDs on the public surface. Foghorn's `ResolveContent` only accepts Commodore-minted public keys.
- `dvr+<chapter_id>` is retired. The only legal `dvr+` token is `dvr+<dvr_internal_name>` (rolling-DVR surface for active recordings).
- Policy inheritance: protected chapter playback resolves through `DVRChapterPolicyPlaybackID` → parent DVR's playback policy.

## Bounded operations — invariant for unbounded artifact lifetime

A DVR artifact may run indefinitely. **All operational paths must be bounded.** No code path enumerates an entire DVR artifact except admin/export jobs with explicit limits.

| Path                           | Bound                                                                                          |
| ------------------------------ | ---------------------------------------------------------------------------------------------- |
| Foghorn ledger queries         | always `WHERE artifact_hash = $1 AND media_start_ms < $end AND media_end_ms > $start`, indexed |
| Chapter sweeper                | one boundary close per active DVR per tick                                                     |
| Chapter finalization queue     | per-DVR mutex; per-tick batch capped at `chapterFinalizationDispatchBatchMax`                  |
| Chapter reclaim sweep          | per-artifact cap (`chapterReclaimPerArtifact`); per-tick batch (`chapterReclaimBatchMax`)      |
| Sidecar restart reconciliation | walks local `dvr/` tree, batches names into pages of 500                                       |
| FinalizeDVR                    | bounded retry of pending/failed_upload via keyset cursor; classification via `COUNT(*) FILTER` |
| Retention soft-delete          | acts on terminal artifacts only                                                                |
| Chapter listing for player UI  | paginated; default 200, max 1000                                                               |

`dvr_segments` sizing: 1 year × 6s segments × 1 stream ≈ 5.26M rows. At 100 concurrent always-on streams, ~526M rows. Existing indexes (`(artifact_hash, sequence)` unique; `(artifact_hash, media_start_ms, sequence)` for ledger walks; partials on status) handle it. Partitioning by `artifact_hash` is partition-compatible if vacuum cost ever becomes material.

## Sidecar (Helmsman) responsibilities

- Forwards Mist triggers (`RECORDING_SEGMENT`, `RECORDING_END`, etc.) to Foghorn over the control stream.
- Uploads segment bytes to S3 against presigned URLs minted by Foghorn (recovery bridge, not playback).
- Emits `DVRSegmentDropped(was_uploaded=…)` for forced evictions and post-stop reclaim orders.
- Maintains a per-segment local cache index (`api_sidecar/internal/control/local_segment_index.go`); rebuilds it from disk on restart via `RestoreLocalSegmentIndex` RPC.
- Owns the chapter finalize processing path (`handleChapterFinalize`): temp HLS, MistProc, push, validate, DTSH boot.
- **Never writes chapter manifests.** Chapter playback is the chapter VOD artifact's own playback path.

## Foghorn responsibilities

- Owns `foghorn.dvr_segments` (per-segment ledger), `foghorn.dvr_chapters` (range + state + playback_id cache), and `foghorn.artifacts` rows for both the parent DVR and the chapter artifact.
- Mints presigned PUT URLs for segment uploads, presigned GETs for chapter-finalize recovery.
- Runs the chapter pipeline workers (sweeper, finalize queue, reclaim sweep) and dispatches finalize jobs.
- Mints chapter playback IDs via Commodore at finalize dispatch (the mint is the dispatch contract — fail-retryable, no fallback).
- Drives `FinalizeDVR` for the parent DVR: bounded retry of pending uploads, classification, retention computation, terminal-chapter close.
- Federation: public chapter playback goes through Commodore (which holds the playback ID registry) and then through normal artifact playback routing — there's no chapter-specific federation surface anymore.

## Stream session semantics

One stream session = one DVR artifact (one `dvr_hash`). A stream that genuinely ends and resumes later is a **new** DVR artifact. Chapter views are scoped to one artifact.

Mist's `append=1 + noendlist=1` keeps the local rolling playlist appendable across sidecar restarts; segments accumulate in S3 and the ledger across the artifact's lifetime regardless of restart count.

## Retention model (post-end only)

- An active DVR is **never killed** by retention. While `status IN ('starting', 'recording', 'finalizing')` the row is invisible to the retention job.
- `retention_until` for a DVR is computed at FinalizeDVR as `ended_at + dvr_retention_days*24h`. The persisted `dvr_retention_days` column on `foghorn.artifacts` (snapshotted at DVR start from the tier policy) drives the days. The live tier is **never re-resolved at end**.
- Chapter retention follows the chapter VOD artifact's own retention horizon (resolved per VOD class).

## Pointers

- Chapter standards (ID stability, mode validation, lost-segment semantics, public addressing): `docs/standards/dvr-chapters.md`
- Operator runbook (`DVR_CLUSTER_MAX_*` envs, sweeper tuning, `completed_partial` triage): `website_docs/src/content/docs/operators/dvr.md`
- Public chapter API: `website_docs/src/content/docs/builders/dvr-chapters.mdx`
- Tier policy resolver: `pkg/dvrpolicy/resolve.go`
- Segment ledger repo: `api_balancing/internal/control/dvr_segments_repo.go`
- Chapter row repo: `api_balancing/internal/control/dvr_chapters_repo.go`
- Chapter sweeper: `api_balancing/internal/jobs/chapter_sweeper.go`
- Chapter finalization queue: `api_balancing/internal/jobs/chapter_finalization_queue.go`
- Chapter reclaim sweep: `api_balancing/internal/jobs/chapter_reclaim_sweep.go`
- Chapter finalize Helmsman path: `api_sidecar/internal/handlers/processing_chapter.go`
- Chapter finalize Foghorn hook: `api_balancing/internal/control/dvr_chapter_finalize_hook.go`
- Commodore chapter playback registry: `commodore.dvr_chapter_playback` (RPCs `MintChapterPlaybackID`, `ResolveChapterPlaybackID`)

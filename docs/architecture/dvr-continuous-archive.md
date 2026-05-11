# DVR Continuous Archive — Architecture

## What this delivers

24/7 DVR with no per-artifact lifetime cap. One DVR artifact spans the entire stream session, regardless of whether the stream ran for 10 minutes or 10 months. Live viewers see a tier-bounded rolling window (Mist `targetAge` + `maxEntries` + `nounlink=1`); replay viewers navigate the full archive sliced into chapter manifests Foghorn generates from the per-segment ledger.

This is the differentiator. Most platforms cap live DVR hard (Mux 4h, Cloudflare ~3h, Vimeo 4h) or force per-broadcast asset rotation. FrameWorks ships an unbounded continuous archive with bounded, cacheable chapter manifests on top.

This doc is the canonical engineering reference. `docs/architecture/clips-dvr.md` covers the broader artifact storage model; this file is the DVR-specific overlay. Replace any "Final DVR manifest" language elsewhere with a pointer here.

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
status           VARCHAR(20)   -- pending | uploaded | failed_upload | deleted_local | lost_local
drop_reason      VARCHAR(32)   -- disk_pressure | retention_expired | operator_cleanup | upload_failed
```

There is **no** final per-artifact manifest in S3. Archive playback is chapter-only.

## Stable physical S3 layout

```
s3://bucket/dvr/{tenant_id}/{stream_internal_name}/{dvr_artifact_id}/segments/{segment_name}
s3://bucket/dvr/{tenant_id}/{stream_internal_name}/{dvr_artifact_id}/chapters/{chapter_id}.m3u8
```

Segment objects are shared across all chapter views — chapter manifests reference the same segment URIs. One physical DVR artifact, many virtual views.

## Virtual chapter views

A chapter is a `(start_ms, end_ms)` slice of an artifact's ledger, rendered as an HLS manifest. Three modes (v1):

```
window_sized_chapters    sequential fixed-length chunks of size tier.MaxWindowSeconds
                         since started_at; e.g. Production tier = 1d chapters
fixed_interval           UTC-only interval_seconds buckets anchored at unix epoch 0
explicit_range           caller supplies start_ms/end_ms; no recurrence semantics
```

**No timezone, no offset.** Civil-time chapters (e.g. "yesterday in Europe/Amsterdam") are produced by the webapp resolving local time → UTC `(start_ms, end_ms)` _before_ calling the API, then submitting as `explicit_range`. DST and IANA rules live entirely at the edge that knows the user's locale. Foghorn stores and operates on UTC epoch ranges only. See `docs/standards/dvr-chapters.md` for the full chapter-ID derivation rules.

### Two manifest shapes

| State                                      | `is_current` | Playlist type | `#EXT-X-ENDLIST` | Reader semantics                      |
| ------------------------------------------ | ------------ | ------------- | ---------------- | ------------------------------------- |
| Active (still recording into this chapter) | true         | `EVENT`       | absent           | Live-shaped: player polls for updates |
| Closed (boundary crossed; replay)          | false        | `VOD`         | present          | VOD-shaped: fixed bounded playlist    |

The active shape exists so a viewer who joins a 24/7 stream replay _while it's still recording_ gets a usable playlist for the current chapter without depending on the rolling Mist manifest.

### Materialization

Foghorn owns three triggers:

1. **Active current-chapter rolling update** (chapter sweeper, every 60s). For each active DVR with a chapter mode set on the artifact, identify the current chapter and re-materialize its EVENT-shaped canonical manifest from the ledger. Debounced — skip if `last_rebuilt_at` is younger than `RebuildIntervalSeconds`.
2. **Boundary close** (chapter sweeper, policy change, or `FinalizeDVR`). When the current chapter closes, Foghorn writes one final VOD manifest with `#EXT-X-ENDLIST`, flips `is_current=false`, then the sweeper materializes the next current chapter as EVENT if the DVR is still recording.
3. **Backfill / cache-on-request** (chapter retrieval RPC). Policy change or chapter request for a historical range that isn't in `foghorn.dvr_chapters` triggers materialization on demand.

Viewer playback resolves `dvr+{chapter_id}` through the normal viewer endpoint path. The selected edge defrosts the bounded chapter into `{artifact}/chapters/{chapter_id}.m3u8` and the shared `{artifact}/segments/` bucket, then MistServer serves the chapter through the same auth, routing, analytics, and billing path as other playback.

`has_gaps` invalidation: when a `lost_local` row arrives late (sidecar reports `DVRSegmentDropped(was_uploaded=false)` after the chapter was already finalized), Foghorn flips `has_gaps=true` and drops `last_rebuilt_at`; the sweeper rebuilds dirty materialized chapters in bounded batches so the cached manifest gets `#EXT-X-GAP` markers (HLS v8). Bounded via `idx_foghorn_dvr_chapters_overlap`.

## Bounded operations — invariant for unbounded artifact lifetime

A DVR artifact may run indefinitely. **All operational paths must be bounded.** No code path enumerates an entire DVR artifact except admin/export jobs with explicit limits.

Concrete checks:

| Path                           | Bound                                                                                                               |
| ------------------------------ | ------------------------------------------------------------------------------------------------------------------- |
| Foghorn ledger queries         | always `WHERE artifact_hash = $1 AND media_start_ms < $end AND media_end_ms > $start`, index-backed                 |
| Chapter materialization        | per-chapter range query                                                                                             |
| Federation for DVR playback    | Commodore routes `dvrChapter` to the origin Foghorn; edges play `dvr+{chapter_id}` through chapter-bounded defrost  |
| Sidecar restart reconciliation | bounded by _local disk inventory_: walks `dvr/` tree, batches names into pages of 500 to `RestoreLocalSegmentIndex` |
| `FinalizeDVR`                  | bounded retry of pending/failed_upload via keyset cursor; classification via `COUNT(*) FILTER` aggregates only      |
| Retention soft-delete          | acts on terminal artifacts only                                                                                     |
| Chapter listing for player UI  | paginated; default page size 200                                                                                    |
| Webapp/UI                      | "play full DVR" defaults to a chapter index, not a full-archive manifest                                            |

`dvr_segments` table sizing: 1 year × 6s segments × 1 stream ≈ 5.26M rows. At 100 concurrent always-on streams, ~526M rows. Postgres handles it with the existing indexes (`(artifact_hash, sequence)` unique; `(artifact_hash, media_start_ms, sequence)` for manifest order; partial on `(artifact_hash, status, media_end_ms) WHERE status='uploaded'` for eviction). Partitioning is not required for this implementation; if table bloat or vacuum cost becomes material, use hash partitioning by `artifact_hash`. The schema is partition-compatible.

## Sidecar (Helmsman) responsibilities

- Forwards Mist triggers (`RECORDING_SEGMENT`, `RECORDING_END`, etc.) to Foghorn over the control stream.
- Uploads segment bytes to S3 against presigned URLs minted by Foghorn.
- Emits `DVRSegmentDropped` for any forced eviction (with `was_uploaded` distinguishing safe local cleanup from data loss).
- Maintains a per-segment local cache index (`api_sidecar/internal/control/local_segment_index.go`); rebuilds it from disk on restart via `RestoreLocalSegmentIndex` RPC.
- **Never writes archive playlists.** The local rolling Mist `.m3u8` stays local; chapter manifests are Foghorn's job.
- On `dvr_terminal` rejection from Foghorn, hard-stops the local Mist push immediately (`mistClient.PushStop`) — no further loss-path segments.

## Foghorn responsibilities

- Owns `foghorn.dvr_segments` (per-segment ledger) and `foghorn.dvr_chapters` (chapter materialization metadata).
- Mints presigned PUT URLs for segment uploads (`RecordDVRSegment` ControlMessage).
- Generates and uploads canonical chapter manifests (the chapter generator + sweeper).
- Drives `FinalizeDVR`: bounded retry of pending uploads, classification via aggregate counts, retention computation from the persisted `dvr_retention_days` column, close the active current chapter.
- Federation: public DVR replay goes through the GraphQL chapter API. Commodore validates tenant ownership, routes the request to the DVR's origin Foghorn, and returns `dvr+{chapter_id}`. The selected playback edge asks the origin Foghorn to defrost only that bounded chapter; whole-DVR `PrepareArtifact` is rejected.

## Stream session semantics

One stream session = one DVR artifact (one `dvr_hash`). A stream that genuinely ends and resumes later is a **new** DVR artifact. v1 chapter views are scoped to one artifact; cross-artifact "stream archive" views can come later if a customer needs them.

Mist's `append=1 + noendlist=1` keeps the local rolling playlist appendable across sidecar restarts; segments accumulate in S3 and the ledger across the artifact's lifetime regardless of restart count.

## Retention model (post-end only)

- An active DVR is **never killed** by retention. While `status IN ('starting', 'recording', 'finalizing')` the row is invisible to the retention job.
- `retention_until` for a DVR is computed at FinalizeDVR as `ended_at + dvr_retention_days*24h`. The persisted `dvr_retention_days` column on `foghorn.artifacts` (snapshotted at DVR start from the tier policy) drives the days. The live tier is **never re-resolved at end** — the tenant's plan may have changed during a months-long stream.
- Future hook: chapter-level rolling-archive retention (delete closed chapters older than N days while the active artifact keeps recording). Not in v1.

## Audit findings — disposition

| #   | Finding                                              | Resolution                                                                                                                                                                                                                                                          |
| --- | ---------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 1   | Retention finalizes active DVR                       | **Dissolved.** Retention only acts on terminal artifacts.                                                                                                                                                                                                           |
| 2   | Terminal segment rejection silently drops accounting | **Fixed.** Sidecar emits `DVRSegmentDropped(was_uploaded=false)` AND calls `mistClient.PushStop`. Foghorn's `MarkDVRSegmentDropped` inserts a `lost_local` placeholder when no row exists yet (carries media_start/end/duration from the request).                  |
| 3   | Eviction window from retention not DVR window        | **Dissolved.** `dvrEffectiveWindowSeconds` reads `dvr_window_seconds` from the artifact row.                                                                                                                                                                        |
| 4   | Disk-pressure cleanup unreachable for active DVR     | **Fixed.** `fallbackCleanup` scans `control.GetActiveDVRHashes()` first; sidecar disk-monitor also tries `RequestEvictableSegments` + `EvictUploadedSegments` before killing the push.                                                                              |
| 5   | `RetryDVRSegmentUpload` handler never wired          | **Fixed.** `helmsman/main.go` registers a handler that re-uploads via `RecordDVRSegment`/`MarkDVRSegmentUploaded` or emits `DVRSegmentDropped(was_uploaded=false)` if local file is missing.                                                                        |
| 6   | Defrost strips final-manifest gaps                   | **Dissolved.** Chapter-aware `DefrostRequest.chapter_segments` carries per-segment `DVRSegmentRef` with `status`; sidecar's `defrostDVRFromChapterRefs` builds the local manifest via `pkg/hls.BuildVOD` rendering `#EXT-X-GAP` for `lost_local`.                   |
| 7   | GAP manifests advertise HLS v6                       | **Fixed.** `pkg/hls.BuildVOD` bumps to `#EXT-X-VERSION:8` when any segment is `Lost` or `BuildVODOptions.HasGaps` is set.                                                                                                                                           |
| 8   | Cluster DVR policy is process-global env             | **Accepted + documented.** One Foghorn process per cluster; process env IS the per-cluster surface. `cluster_id` parameter dropped (was never read).                                                                                                                |
| 9   | `syncNewSegments` bypasses the ledger                | **Fixed.** Reconciliation backstop (10s tick + final-flush) parses the local Mist manifest via `pkg/hls.Parse` and routes through `RecordDVRSegment` + `MarkDVRSegmentUploaded`. RECORDING_SEGMENT remains primary; this catches the rare missed-trigger case.      |
| 10  | Chapter manifest URIs resolve to 404                 | **Fixed.** `BuildVODOptions.SegmentURIPrefix` lets the chapter generator emit `../segments/{name}` (chapter playlists live at `chapters/{chapter_id}.m3u8`). Default `segments/` keeps non-chapter manifests unchanged.                                             |
| 11  | DefrostDVR ignores `chapter_segments`                | **Fixed.** `defrostDVRFromChapterRefs` is the chapter-aware path; reads per-segment metadata from the request, builds the local manifest with gaps rendered, downloads only `uploaded` segments, refcounts via `LocalSegmentIndex.AcquireView`/`ReleaseView`.       |
| 12  | Retention still start-time + hardcoded 30d           | **Fixed.** `Foghorn.StartDVR` no longer computes `retentionUntil` at start. Live window resolved without retention clamp. `dvr_retention_days` snapshotted from `DVRPolicy.recording_retention_days` (Purser tier entitlement). FinalizeDVR sets `retention_until`. |
| 13  | Local PrepareArtifact DVR path still S3-lists        | **Fixed.** Whole-archive DVR defrost is rejected; bounded chapter playback goes through `dvrChapter` and `dvr+{chapter_id}` edge routing. Federation `PrepareArtifact` rejects DVR rather than exposing a second DVR playback surface.                              |
| 14  | LocalSegmentIndex unused for eviction                | **Fixed.** `MarkUploaded` called on every successful upload (RECORDING_SEGMENT + reconciliation paths); `EvictionEligible` consulted before deletion; chapter playback brackets segments with `AcquireView`/`ReleaseView`; `Forget` on eviction.                    |
| 15  | No HTTP/gateway routes for chapter RPCs              | **Fixed.** Public path: api_gateway (GraphQL) → Commodore (validates tenant ownership) → Foghorn (owns ledger + materialization). Three GraphQL ops: `dvrChapter`, `dvrChapters`, `setDVRChapterPolicy`. Houdini operations included for the webapp.                |

## Pointers

- Chapter ID stability + mode-change semantics: `docs/standards/dvr-chapters.md`
- Operator runbook (`DVR_CLUSTER_MAX_*` envs, sweeper tuning, `completed_partial` triage): `website_docs/src/content/docs/operators/dvr.md`
- Public API reference for chapter retrieval: `website_docs/src/content/docs/builders/recordings.mdx#archive-chapters`
- Tier policy resolver: `pkg/dvrpolicy/resolve.go` — pure, table-tested
- Manifest builder: `pkg/hls/manifest.go` — handles GAP + DISCONTINUITY rendering
- Segment ledger repo: `api_balancing/internal/control/dvr_segments_repo.go`
- Chapter generator: `api_balancing/internal/control/dvr_chapter_generator.go`
- Chapter sweeper: `api_balancing/internal/jobs/chapter_sweeper.go`
- Chapter RPCs: `api_balancing/internal/grpc/dvr_chapters.go`
- FinalizeDVR: `api_balancing/internal/control/dvr_finalize.go`
- Helmsman local segment index: `api_sidecar/internal/control/local_segment_index.go`

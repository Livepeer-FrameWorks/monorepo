# DVR Chapters — Standards

## Chapter ID stability

```
chapter_id = sha256_hex(
  dvr_artifact_id || mode || interval_seconds || start_ms || end_ms
)[0:32]
```

**Invariants:**

- Same `(dvr_artifact_id, mode, interval_seconds, start_ms, end_ms)` always produces the same `chapter_id`. The chapter sweeper writes the row idempotently on `chapter_id`.
- `stream_id` is intentionally **not** in the hash. `dvr_artifact_id` already namespaces uniquely; including `stream_id` would destabilize the ID across the artifact's `stream_internal_name` rename edge case.
- Historical chapter mode changes that yield different `(start_ms, end_ms)` boundaries produce **new** chapter IDs. Old chapter IDs from prior modes remain valid as long as their finalized VOD artifacts haven't been deleted by retention.

## Mode parameter validation

```
window_sized_chapters    interval_seconds = 0       sequential since started_at, length = tier.MaxWindowSeconds (or dvr_window_seconds)
fixed_interval           interval_seconds >= 3600   UTC-only, anchored at unix epoch 0
```

- `fixed_interval` below 3600 seconds is rejected at the API layer (`InvalidArgument`). Automatic chapters are for keeping the chapter list bounded on long archives; sub-hour granularity belongs to per-clip workflows.
- `explicit_range` is retired. Historical chapter mode is configured per-stream (`commodore.streams.dvr_chapter_mode`) and snapshotted onto the DVR artifact at StartDVR; mid-recording mode changes are not supported.

## UTC-only API surface

The backend stores and operates on UTC epoch milliseconds only. **No timezone columns, no `offset_seconds`, no IANA timezone identifiers anywhere in Foghorn or Commodore.**

Civil-time chapters (e.g. "yesterday in Europe/Amsterdam") are produced by the webapp resolving local time → UTC `(start_ms, end_ms)` _before_ calling the API. DST and IANA rules live entirely at the edge that knows the user's locale.

## Chapter state machine

```
open → closed → finalizing → finalized → frozen → reclaimed
                     ↓
                     └→ failed_source_missing | failed_permanent
```

| State                   | Meaning                                                                                             |
| ----------------------- | --------------------------------------------------------------------------------------------------- |
| `open`                  | Recording in progress; the rolling-DVR surface serves viewers.                                      |
| `closed`                | Boundary reached; the finalization queue will pick up this row on its next tick.                    |
| `finalizing`            | Processing job in flight on the recording origin (Mist remuxes TS → canonical `.mkv`).              |
| `finalized`             | PUSH_END fired and the chapter VOD artifact exists locally. Waiting on freeze + `.dtsh` sync.       |
| `frozen`                | Chapter artifact + `.dtsh` durably on S3. Safe to reclaim source TS segments.                       |
| `reclaimed`             | Source segments deleted; the chapter row remains as range metadata. Playback uses the VOD artifact. |
| `failed_source_missing` | Recovery exhausted — at least one overlapping segment was missing from both local and S3 freeze.    |
| `failed_permanent`      | Unrecoverable input (max retries exceeded, ledger invariants violated).                             |

A chapter is **playable** (`playableNow=true`) when its state is one of `finalized`, `frozen`, or `reclaimed` — in all three the canonical `.mkv` is the playback target. Earlier / terminal-failure states return no `playbackId`.

## Lost-segment handling

Foghorn's sidecar reports `DVRSegmentDropped(was_uploaded=false)` for a segment that was force-evicted before the S3 freeze. Foghorn marks the row `status='lost_local'`. The chapter finalization queue then sees the lost row in its segment ledger and:

1. Tries to recover the segment from its temporary S3 freeze object. Transient S3 errors retry on the next tick.
2. If the segment is missing from both local _and_ S3 (the row is `lost_local` with no recovery URL), the chapter transitions to `failed_source_missing` and produces no VOD artifact. Manual operator triage decides whether to delete the chapter row or partial-salvage out of band.

Chapters never produce partial MKVs with `#EXT-X-GAP` markers. The chapter contract is all-or-nothing.

## Bounded operations invariant

For unbounded artifact lifetime (24/7 streams), every chapter operation must stay bounded:

| Operation                        | Bound                                                                                     |
| -------------------------------- | ----------------------------------------------------------------------------------------- |
| `BuildChapterID`                 | constant time per chapter                                                                 |
| `ChapterSweeper.processArtifact` | one boundary close per active DVR per tick                                                |
| `chapter_finalization_queue`     | per-DVR mutex serializes finalize jobs; bounded by `chapterFinalizationDispatchBatchMax`  |
| `chapter_reclaim_sweep`          | per-artifact cap (`chapterReclaimPerArtifact`); per-tick batch (`chapterReclaimBatchMax`) |
| `ListDVRChapters`                | paginated; default 200, max 1000 per page                                                 |
| `RetrieveDVRChapter`             | single-row lookup                                                                         |

No code path calls `SELECT * FROM foghorn.dvr_segments WHERE artifact_hash=$1` without a range filter. Admin/export jobs that need the whole artifact use explicit cursors with `--max-rows` flags.

## Public addressing — `playbackId`

Chapter VOD artifacts are addressed by their Commodore-minted public `playback_id`, the same way ordinary VOD uploads are. The mapping is stored in `commodore.dvr_chapter_playback(chapter_id, tenant_id, playback_id, artifact_hash)` and cached on `foghorn.dvr_chapters.playback_id` for the chapter-list resolver hot path.

- Raw artifact hashes are **not** accepted as chapter playback IDs anywhere on the public surface. Foghorn's `ResolveContent` only accepts the Commodore-minted public key.
- Mint failure during finalization dispatch is hard-fail: the chapter stays in `closed` and retries on the next tick. There is no fallback that would expose a chapter without a public playback ID.
- `dvr+<chapter_id>` no longer resolves anywhere. The only legal `dvr+` token is `dvr+<dvr_internal_name>` (the rolling-DVR surface of an actively recording stream).

## What Foghorn promises about chapters

- A finalized chapter's `playbackId` resolves through the same `resolveViewerEndpoint` path as any other VOD artifact. No chapter-only relay routes.
- Chapter IDs are stable across retries; finalization is idempotent on `chapter_id` (unique partial index on `foghorn.artifacts(origin_id) WHERE origin_type='dvr_chapter'`).
- Chapter listing pages are deterministic for a given `(artifact_hash, mode, interval_seconds, range_start_ms, range_end_ms, page_token)` tuple.
- A chapter that reaches `frozen` survives recording-node loss — the `.mkv` + `.dtsh` are durable on S3.

## What Foghorn does NOT promise

- A whole-artifact manifest. Long archives use chapters; ad-hoc views use clips against the live or rolling-DVR surfaces.
- Mid-recording chapter policy changes. Mode is per-stream config, snapshotted at StartDVR; updates take effect on the next recording.
- Partial chapter artifacts. A chapter either finalizes into a complete `.mkv` or moves to a `failed_*` terminal state.
- Civil-time semantics on the backend. Resolve timezones at the edge.

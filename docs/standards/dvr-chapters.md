# DVR Chapters — Standards

## Chapter ID stability

```
chapter_id = sha256_hex(
  dvr_artifact_id || mode || interval_seconds || start_ms || end_ms
)[0:32]
```

**Invariants:**

- Same `(dvr_artifact_id, mode, interval_seconds, start_ms, end_ms)` always produces the same `chapter_id`. Materialization is idempotent on `chapter_id`.
- `stream_id` is intentionally **not** in the hash. `dvr_artifact_id` already namespaces uniquely; including `stream_id` would destabilize the ID across the artifact's `stream_internal_name` rename edge case.
- Chapter mode changes that yield different `(start_ms, end_ms)` boundaries produce **new** chapter IDs. Old chapter IDs from prior modes remain valid until cache expiry / retention cleanup, so in-flight viewers see no interruption.

## Mode parameter validation

```
window_sized_chapters    interval_seconds = 0       sequential since started_at, length = tier.MaxWindowSeconds (or dvr_window_seconds)
fixed_interval           interval_seconds >= 3600   UTC-only, anchored at unix epoch 0
explicit_range           interval_seconds = 0    caller-supplied; no recurrence
```

- `fixed_interval` below 3600 seconds is rejected at the API layer (`InvalidArgument`). Automatic chapters are for keeping long-archive manifests bounded; short ad-hoc views should use `explicit_range`.
- `explicit_range` with `end_ms <= start_ms` is rejected.
- `mode = ""` on `RetrieveDVRChapter` defaults to `explicit_range`.

## UTC-only API surface

The backend stores and operates on UTC epoch milliseconds only. **No timezone columns, no `offset_seconds`, no IANA timezone identifiers anywhere in Foghorn or Commodore.**

Civil-time chapters (e.g. "yesterday in Europe/Amsterdam") are produced by the webapp resolving local time → UTC `(start_ms, end_ms)` _before_ calling the API. DST and IANA rules live entirely at the edge that knows the user's locale.

DST consequences are correct _because_ the caller did the math:

- Spring DST boundary: a "local day" submits as a 23-hour UTC range.
- Autumn DST boundary: 25-hour UTC range.
- Foghorn just materializes whatever range it was given.

Timezone-aware chapter cycling is not a backend parameter. Callers resolve civil time to UTC before calling Foghorn.

## `has_gaps` invalidation

When the sidecar reports `DVRSegmentDropped(was_uploaded=false)` for a segment, Foghorn:

1. Marks the ledger row `status='lost_local'`.
2. Runs `FlagChaptersOverlappingSegment(artifact_hash, segment_start_ms, segment_end_ms)` — finds materialized chapters whose `(start_ms, end_ms)` overlaps the segment via `idx_foghorn_dvr_chapters_overlap` and sets `has_gaps=true`, drops `last_rebuilt_at`.
3. The next chapter sweeper tick re-materializes those chapters with `#EXT-X-GAP` markers and bumps the manifest to `#EXT-X-VERSION:8`.

The cache is invalidated cleanly, not stale. Players that fetched the manifest before invalidation continue to play their cached version (ignorant of the now-known gap); players that fetch after the rebuild see the gap.

## Bounded operations invariant

For unbounded artifact lifetime (24/7 streams), every chapter operation must stay bounded:

| Operation                        | Bound                                                                                            |
| -------------------------------- | ------------------------------------------------------------------------------------------------ |
| `BuildChapterID`                 | constant time per chapter                                                                        |
| `GenerateChapter`                | range query over `dvr_segments` for the chapter's `(start_ms, end_ms)`; never the whole artifact |
| `ChapterSweeper.processArtifact` | one current-chapter rebuild per active DVR per tick                                              |
| `ListDVRChapters`                | paginated; default 200, max 1000 per page                                                        |
| `RetrieveDVRChapter`             | bounded by chapter range                                                                         |
| `FlagChaptersOverlappingSegment` | bounded by overlap (typically 0–2 chapters per segment)                                          |

No code path calls `SELECT * FROM foghorn.dvr_segments WHERE artifact_hash=$1` without a range filter. Admin/export jobs that need the whole artifact use explicit cursors with `--max-rows` flags.

## Active vs. closed manifest semantics

| Field                  | Active chapter                                       | Closed chapter                                                           |
| ---------------------- | ---------------------------------------------------- | ------------------------------------------------------------------------ |
| `is_current`           | `true`                                               | `false`                                                                  |
| `#EXT-X-PLAYLIST-TYPE` | `EVENT`                                              | `VOD`                                                                    |
| `#EXT-X-ENDLIST`       | absent                                               | present                                                                  |
| Reader behavior        | poll for updates; manifest grows                     | fixed; player fetches once                                               |
| Sweeper behavior       | rebuild every `RebuildIntervalSeconds` (default 60s) | written once at boundary close; only re-built on `has_gaps` invalidation |

The active shape exists so a viewer who joins a 24/7 stream replay _while it's still recording_ gets a usable playlist for the current chapter without depending on the rolling Mist manifest.

## Policy change semantics (`SetDVRChapterPolicy`)

When the artifact's chapter policy changes mid-recording (Gateway/Commodore call `SetDVRChapterPolicy`):

1. Foghorn writes the in-flight current chapter under the old policy as VOD with `#EXT-X-ENDLIST`, then flips `is_current=false`.
2. The next sweep tick starts the new current chapter under the new policy.
3. Old chapter manifests stay readable in S3 until cache expiry / retention cleanup. Viewers playing them see no interruption.
4. New viewers using the new mode see fresh chapters generated on demand (cache-on-request) until the sweeper materializes them on the next tick.

## What Foghorn promises about chapters

- Chapter manifests at the canonical S3 key `dvr/{tenant}/{stream}/{artifact}/chapters/{chapter_id}.m3u8` are HLS-valid: `#EXTM3U`, `#EXT-X-VERSION`, `#EXT-X-TARGETDURATION`, `#EXT-X-MEDIA-SEQUENCE` set to the first ledger segment sequence, `#EXT-X-PLAYLIST-TYPE:VOD|EVENT`, and `#EXT-X-PROGRAM-DATE-TIME` when the ledger has absolute media timestamps.
- A closed chapter manifest's segment URIs all point to objects that exist in S3 (or render `#EXT-X-GAP` for `lost_local` rows). No 404 on a real segment URI.
- Chapter IDs are stable across re-materialization; the same chapter URL always returns the same chapter (modulo `has_gaps` invalidation, which preserves the URI).
- Chapter listing pages are deterministic for a given `(artifact_hash, mode, interval_seconds, range_start_ms, range_end_ms, page_token)` tuple.

## What Foghorn does NOT promise

- A whole-artifact manifest. There isn't one. Use chapters or `explicit_range`.
- Civil-time semantics on the backend. Resolve timezones at the edge.
- Real-time updates within the active chapter's rebuild interval. Default debounce is 60s; viewers may see segments appear in the active chapter manifest with up to one minute of lag. Tunable via `RebuildIntervalSeconds`.

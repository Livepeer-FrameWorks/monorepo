# Thumbnails

Two systems. Single-frame preview images (poster, stream cards) and sprite sheets (seek-bar scrubbing). Both backed by MistServer JPEG tracks, no shared code.

## Source Files

MistServer internals are upstream source files, not vendored in this monorepo:

- Single-frame + MJPEG output: `mistserver/src/output/output_jpg.cpp`
- Sprite sheet generator: `mistserver/src/process/process_thumbs.cpp`
- ThumbVTT output: `mistserver/src/output/output_thumbvtt.cpp`

FrameWorks integration points in this repo:

- Connector provisioning: `api_sidecar/internal/config/manager.go` (ThumbVTT protocol)
- Process provisioning: `api_sidecar/internal/config/manager.go` (STREAM_PROCESS trigger for live+/processing+; vod+ runs without MistProc tracks)
- Player sprite manager: `npm_player/packages/core/src/core/ThumbnailSpriteManager.ts`
- VTT parser: `npm_player/packages/core/src/core/ThumbnailVttParser.ts`
- Track detection: `npm_player/packages/core/src/core/PlayerController.ts` (`detectThumbnailVttUrl`, `detectPreviewUrl`)
- Poster overlay: `npm_player/packages/{react,wc}/.../{ThumbnailOverlay,fw-thumbnail-overlay}.*`
- SeekBar sprite rendering: `npm_player/packages/{react,svelte,wc}/.../SeekBar.*`

---

## Single-Frame Preview

`output_jpg.cpp` serves any JPEG-codec track:

| URL              | Content-Type                | Behavior                                     |
| ---------------- | --------------------------- | -------------------------------------------- |
| `/{stream}.jpg`  | `image/jpeg`                | Latest keyframe, closes connection           |
| `/{stream}.mjpg` | `multipart/x-mixed-replace` | Keeps connection open, pushes each new frame |

`?track={idx}` selects a specific JPEG track by index. Prefer language-based selectors: `?video=pre` selects the preview track (`lang="pre"`), `?video=thu` selects the sprite sheet track (`lang="thu"`). See [track selectors](https://docs.mistserver.org/mistserver/concepts/track_selectors) — MistServer's `pickTracks` matches language codes directly.

MistServer advertises both `.jpg` and `.mjpg` as `html5/image/jpeg` sources — distinguished by URL extension. LSP shows `.jpg` by default and swaps to `.mjpg` on hover for live motion preview.

The player auto-detects the preview track: `PlayerController.detectPreviewUrl()` finds a JPEG track with `lang === "pre"` in the metadata and constructs `{mistBaseUrl}/{streamName}.jpg?video=pre` as the poster image. The explicit `poster` config prop takes precedence if set.

---

## Sprite Sheets

`process_thumbs.cpp` decodes video keyframes, scales them, and composes a grid as a JPEG sprite sheet plus a WebVTT timing manifest. Both are buffered as new tracks on the stream. MistProcThumbs is provisioned dynamically via the STREAM_PROCESS trigger for `live+` (live capture), `dvr+` (rolling DVR capture), and `processing+` (post-ingest pipeline). Commodore resolves lifecycle-specific process snapshots for live, DVR, clip, DVR finalization, and uploaded VOD; Foghorn stores and applies those snapshots without deriving local subsets. The later `vod+` boot is for `.dtsh` generation only and does NOT generate thumbnails.

The source video track is selected via MistServer's [track selector](https://docs.mistserver.org/mistserver/concepts/track_selectors) syntax (default: `video=lowres` — picks the lowest resolution track to minimize CPU).

Configurable per-process:

| Parameter      | Default                  |
| -------------- | ------------------------ |
| `thumb_width`  | 160 px                   |
| `thumb_height` | 90 px                    |
| `grid_cols`    | 10                       |
| `grid_rows`    | 10                       |
| `jpeg_quality` | 75                       |
| `interval`     | 5000 ms (regen for live) |

Full grid: 10x10 = 100 thumbnails, 1600x900 total.

### Output tracks

| Track  | Type  | Codec      | Lang  | Content                            |
| ------ | ----- | ---------- | ----- | ---------------------------------- |
| Sprite | video | `JPEG`     | `thu` | Grid JPEG, regenerated on interval |
| VTT    | meta  | `thumbvtt` | —     | WebVTT with `#xywh=` coordinates   |

VTT cues reference the sprite via relative URL:

```
WEBVTT

00:00:00.000 --> 00:00:05.000
/mystream.jpg?track=42#xywh=0,0,160,90

00:00:05.000 --> 00:00:10.000
/mystream.jpg?track=42#xywh=160,0,160,90
```

When the keyframe cache exceeds grid capacity (100 cells), thumbnails are sampled evenly across the time range.

---

## Track Disambiguation

A stream can have multiple JPEG tracks: the sprite sheet (`lang="thu"`) and the preview image (`lang="pre"`). Without a selector, `/{stream}.jpg` serves the first JPEG track found. Use [language-based track selectors](https://docs.mistserver.org/mistserver/concepts/track_selectors) to target a specific track:

| URL                        | Selects                                       |
| -------------------------- | --------------------------------------------- |
| `/{stream}.jpg?video=pre`  | Preview track (latest keyframe, single image) |
| `/{stream}.jpg?video=thu`  | Sprite sheet track (10x10 grid)               |
| `/{stream}.mjpg?video=pre` | Preview track as MJPEG stream                 |

The same selectors work on `.mjpg`. MistServer's `pickTracks` matches `video=pre` against tracks where `lang == "pre"`.

The VTT cues generated by `process_thumbs` reference the sprite track by raw index (`?track={idx}`), not by language selector — the VTT is generated server-side where the index is known.

---

## ThumbVTT Output

`output_thumbvtt.cpp` serves sprite sheet tracks over HTTP.

```
/{stream}.thumbvtt[?track={idx}][&mode=push]
```

`?track=` selects a specific thumbvtt track index. Default: first valid thumbvtt track. The JPEG sprite track is found automatically (`codec == "JPEG"`, `lang == "thu"`).

### Default mode

Selects only the thumbvtt track. Responds `Content-Type: text/vtt`, sends the first VTT data packet, closes the connection. VOD: fetch once. Live: poll on interval. VTT cue URLs point to `/{stream}.jpg?track={spriteIdx}` — client fetches the sprite separately.

Works with any player that understands WebVTT thumbnail sprites.

### Push mode (`?mode=push`)

Selects both thumbvtt and JPEG tracks. Responds `Content-Type: multipart/mixed; boundary={random}`. Connection stays open.

On each sprite regeneration, `sendNext()` buffers VTT into `pendingVtt` and JPEG into `pendingJpeg`. When both are populated, `pushPair()` writes a boundary pair:

```
\r\n--{boundary}\r\n
Content-Type: text/vtt; charset=utf-8\r\n
Content-Length: {n}\r\n
\r\n
{complete VTT manifest}
\r\n--{boundary}\r\n
Content-Type: image/jpeg\r\n
Content-Length: {n}\r\n
\r\n
{sprite sheet binary}
```

Each VTT is complete (starts with `WEBVTT`), not a delta. Each JPEG is the full sprite sheet. New pairs arrive on each regeneration.

FrameWorks player only.

### HLS/DASH

HLS manifests (`output_hls.cpp`) reference the sprite track via `EXT-X-IMAGE-STREAM-INF` with `EXT-X-TILES:LAYOUT=10x10`. DASH manifests (`output_cmaf.cpp`) use `EssentialProperty` with `http://dashif.org/thumbnail_tile`. Both reference `/{stream}.jpg?track={spriteIdx}`. Independent of ThumbVTT.

### Notes

- No `?mode=poll` parameter. Anything other than `"push"` is default.
- `pushPair()` requires both buffers populated. Missing track = nothing sent.
- CORS headers on all responses including OPTIONS/HEAD.

---

## Player Integration

### Sprite sheets (automatic)

`PlayerController.detectThumbnailVttUrl()` scans MistServer track metadata for `codec === "thumbvtt"`, constructs `{mistBaseUrl}/{streamName}.thumbvtt?track={idx}`, creates a `ThumbnailSpriteManager`. Cues exposed via `getThumbnailCues()` and the `thumbnailCuesChange` event.

SeekBar components receive cues as a prop. On hover, `findCueAtTime()` binary-searches to resolve hover position → cue, renders the crop via `background-image` + `background-position` from `#xywh`.

### Poster (auto-detected)

`PlayerController.detectPreviewUrl()` finds a JPEG track with `lang === "pre"`, constructs `{mistBaseUrl}/{streamName}.jpg?video=pre`, uses it as the poster image for `ThumbnailOverlay`. Shown before playback starts.

The explicit `poster` config prop (or `thumbnailUrl` on framework components) overrides auto-detection. When neither exists, no poster is shown.

---

## S3 Push Pipeline (Stream Listing Thumbnails)

The player-side thumbnails above require an active viewer connection to a MistServer edge. Stream listing pages need thumbnails without per-stream edge resolution — that's the S3 push pipeline.

### Flow (crash-safe publication state machine)

A node uploads thumbnails OFTEN (every few seconds for a live stream), so the pipeline must never let a
half-finished or crashed upload overwrite or corrupt the served object. It doesn't publish to a fixed mutable
key; instead Foghorn MINTS each publication attempt and switches a per-tenant active pointer atomically once the
new version is fully staged and verified. A stale, partial, or crashed attempt leaves the live version untouched.

```
MistServer (process_thumbs)
  → writes poster.jpg, sprite.jpg, sprite.vtt to /tmp/mist_thumbs/{streamName}/
  → fires THUMBNAIL_UPDATED trigger

Helmsman (webhook handler)
  → receives trigger payload (stream name + file paths)
  → sends ThumbnailUploadRequest to Foghorn via gRPC control stream

Foghorn (mint + assign)
  → resolves identity to the asset_key: stream_id (live) or artifact_hash (DVR/VOD/clip)
  → MINTS a 128-bit crypto-random attempt_id (this IS the version segment)
  → in ONE tx, persists the attempt (status='assigned') + a per-file row per allowlisted file, each with its
    per-attempt STAGING key — BEFORE the node holds any PUT URL
  → presigns a PUT for EVERY object (all-or-nothing; a single presign failure fails the whole attempt so the
    node never gets a partial assignment) and returns ThumbnailUploadResponse{attempt_id, presigned URLs}

Helmsman
  → uploads each file to its STAGING key via presigned PUT (no edge credentials needed)
  → echoes attempt_id back in the ThumbnailUploaded confirmation (binds a Foghorn-ASSIGNED operation, not a
    node-chosen id); a swallowed send is surfaced, not dropped (recovery sweeps on lease expiry otherwise)

Foghorn (verify → promote → publish, all guarded)
  → drops immediately if the attempt's lease already expired (recovery will fail it)
  → for EACH object: HEAD-verifies the staged upload (provider etag/size authoritative) and PROMOTES it to its
    immutable version key; a missing/failed object leaves the attempt for retry/sweep (no partial publish)
  → guarded monotonic CAS on the tenant-scoped active pointer: advances only if this attempt is at least as new
    as the pointer's current attempt — a stale attempt that lost the race is settled 'failed' and its promoted
    objects enqueued for cleanup, never leaked
  → on activation, IN THE SAME TX: flips has_thumbnails (no-op for a live stream_id) and enqueues the now-
    superseded staging objects for cleanup — so a crash right after the CAS can't skip them
  → best-effort (self-healing) after commit: Chandler cache invalidation carrying the new activeVersion, plus
    the origin-cluster backfill / Commodore projection
```

Stuck attempts (crash mid-publish) are re-driven by a recovery reconciler: a still-`publishing` attempt within
its lease is re-published (idempotent), an expired non-terminal attempt is failed and its staged/promoted objects
swept, and a superseded version is GC'd only after a reader-safety horizon so an in-flight read of the old version
can't 404.

### S3 Key Layout

The asset_key is a stable, immutable identifier (stream_id UUID or artifact_hash) — never `playback_id` (which
can be rotated). Within an asset, objects are version-addressed by attempt_id, so a new attempt writes DISTINCT
keys and can never overwrite the live object:

```
thumbnails/{asset}/.staging/{attempt_id}/poster.jpg   # per-attempt upload target (garbage once promoted)
thumbnails/{asset}/v/{attempt_id}/poster.jpg          # immutable published version object (never overwritten)
thumbnails/{asset}/poster.jpg                         # legacy fixed-key fallback (pre-state-machine rows)
```

The `asset_key` is the **globally-unique** public resource identity: a `stream_id` UUID or an opaque,
randomly-minted `artifact_hash` (Commodore enforces uniqueness; `foghorn.artifacts.artifact_hash` is a global
primary key). It is **not** a content hash, so it never recurs across tenants — there is exactly one owner per
asset_key. Which version an asset currently serves is decided NOT by a mutable S3 object but by a Foghorn-owned DB
row, `foghorn.thumbnail_active_pointer`, keyed by `asset_key` alone. `tenant_id` rides on that row (and on every
attempt) as **ownership/authorization attribution — never part of the identity**: publish and purge prove tenant
ownership, but public resolution (the URL, the S3 namespace, the resolver, Chandler's cache) is by asset_key only.
There is no fixed mutable object to race.

Chandler treats the asset_key as an opaque path component — no format validation.

### Lifecycle cleanup

When an artifact is hard-purged (`PurgeDeletedJob`, after its main bytes are freed), the same sweep deletes the
`thumbnails/{artifact_hash}/` prefix (every version + staging + legacy object) — routed by the thumbnail's own
recorded `destination_cluster`, not the parent artifact's backend, though this is only a cluster-id match today
(an official alias backed by THIS cell but with a different cluster id would misroute — persisting the backend
fact on the assignment is open) — and drops the asset's thumbnail control rows (`thumbnail_active_pointer` +
`thumbnail_task_assignment`, whose object rows cascade). Publication and resolution are fenced against a terminal
parent (`status IN ('deleted','failed','expired','aborted')`) at claim, publish, AND resolve.

A soft-deleted/failed **artifact** parent stops resolving on the next Foghorn cold-miss (the resolver returns a
distinct `GONE` state → Chandler 404s + evicts its cached mapping). This is NOT instantaneous, and NOT even
strictly TTL-bounded: (a) a Chandler entry still fresh within its `thumbnailVersionTTL` (≈60s) serves the
versioned object until it expires (the object still exists until hard-purge); (b) worse, when the entry has
expired but Chandler's in-cell Foghorn is UNREACHABLE, Chandler currently keeps serving the last-known mapping
(fail-OPEN) until the resolver recovers — so during a resolver outage revocation is unbounded. Closing this needs
BOTH a pushed `GONE` invalidation on soft-delete AND a decision to fail CLOSED on an expired-and-unrevalidated
mapping (a real availability-vs-correctness trade-off); both are OPEN, part of the durable-cleanup design
increment — not yet built.

Version-supersession GC only reclaims **superseded** versions, never an asset's **final** active version (never
superseded). For an artifact, hard-purge removes the final version. For a **live stream** (no artifact row, so no
purge and no `GONE` — the resolver has no parent to consult), the final pointer + object are cleaned on stream
deletion: Commodore, AFTER committing the delete, calls Foghorn's `DeleteStreamThumbnails`. This call is
**best-effort and NOT durable** — a Foghorn failure is only logged; it is NOT retried, and a later `DeleteStream`
for the same stream_id returns `NotFound` (the row is already gone), so it cannot retry either — the objects then
leak until a manual/out-of-band cleanup. Making this a transactional outbox obligation (recorded in the
stream-deletion transaction) drained by an idempotent Foghorn cleanup queue, with a durable stream tombstone that
also fences claim/publish, is OPEN — the live-stream deletion path is NOT yet durable or race-safe. High-frequency
live churn is bounded by supersession GC (bounded oldest-first passes per recovery tick).

### Chandler (Static Asset Server)

`api_assets/` — HTTP-only service (port 18020) that caches and serves thumbnail assets from S3. That is its **entire** scope.

**Exactly three files, nothing else.** The public surface is
`GET|HEAD|OPTIONS /assets/{assetKey}/{file}` where `{file}` must be one of
`poster.jpg`, `sprite.jpg`, `sprite.vtt` (a hardcoded allowlist in
`internal/handlers/assets.go`); any other filename is a 404. There is **no
VOD-metadata endpoint** — VOD playback metadata lives in Foghorn
(`foghorn.vod_metadata`), and any README/ports-table note suggesting
Chandler serves it is imprecise. The only other route is the internal cache
invalidation endpoint below.

Version-resolving URL-to-S3 mapping: the public URL stays STABLE per asset (`/assets/{key}/poster.jpg`) even
though the backing object is version-addressed — thumbnails update often, so a URL that changed every publish
would be uncacheable. Chandler maps `{key}` → the current `active_version` and serves
`thumbnails/{key}/v/{active_version}/poster.jpg`, falling back to the legacy fixed key `thumbnails/{key}/poster.jpg`
for pre-state-machine assets. It learns the active version two ways, both IN-CELL (Chandler never talks to the
control plane — a media cell is self-sustaining):

- **Push (fast path)**: the cache invalidation below carries the new `activeVersion`, so Chandler updates its
  `asset_key → version` map without any lookup.
- **Cold-miss pull**: on a first/evicted read with no known version, Chandler resolves it from its LOCAL Foghorn
  over the Privateer mesh (`foghorn.internal:18008` or `FOGHORN_INTERNAL_URL`, service-token authenticated) —
  since the version IS the attempt_id and the objects are globally unique, no tenant is needed in the public URL.

- **LRU + S3 read-through**: in-memory LRU cache (~50MB, 30s TTL; `CACHE_MAX_BYTES`/`CACHE_TTL_SECONDS`); miss → S3 `GetObject` → cache fill → serve. S3 fetch failure serves 404.
- `poster.jpg` uses `Cache-Control: public, max-age=30`; `sprite.jpg` and `sprite.vtt`
  use `Cache-Control: public, no-cache` and are purged from Chandler's in-memory
  cache after thumbnail uploads complete
- `POST /internal/assets/cache/invalidate` is service-token authenticated
  (registered only when `SERVICE_TOKEN` is set) and is the endpoint the
  S3-push pipeline hits: after an attempt PUBLISHES, Foghorn
  calls it per instance (`CHANDLER_INTERNAL_URL` mesh URLs) with
  `{assetKey, files, activeVersion}` so the next viewer read serves the freshly
  published version instead of a stale cached copy inside the TTL window.
- No auth on asset reads (public assets; tenant isolation enforced at Commodore query layer)
- Prometheus metrics: cache hits/misses, S3 fetch errors, request latency

**Per-cluster S3 config from Quartermaster at boot**: when `CLUSTER_ID` is
set, `cmd/chandler/main.go` calls Quartermaster `GetCluster` and takes
`s3_bucket`/`s3_endpoint`/`s3_region` from the cluster row — the same
inline storage columns the rest of the cluster uses. Credentials stay
env-only (`STORAGE_S3_ACCESS_KEY`/`SECRET_KEY`); a failed cluster lookup is
fatal unless `CHANDLER_ALLOW_ENV_S3_FALLBACK=true`, in which case the
`STORAGE_S3_*` env values (same bucket as clips/DVR/VOD) are used as-is.
With no bucket at all, Chandler starts but serves 503 until configured.
Read-only S3 access. Chandler also registers itself with Quartermaster via
the standard `BootstrapService` retry loop.

### Key Files

| File                                                      | Purpose                                                                |
| --------------------------------------------------------- | ---------------------------------------------------------------------- |
| `api_assets/cmd/chandler/main.go`                         | HTTP server, S3 client, LRU cache                                      |
| `api_assets/internal/handlers/assets.go`                  | GET /assets/{assetKey}/{file}; active-version resolve + serve          |
| `api_assets/internal/cache/lru.go`                        | Thread-safe size-bounded LRU cache                                     |
| `api_sidecar/internal/config/manager.go`                  | THUMBNAIL_UPDATED trigger registration                                 |
| `api_sidecar/internal/handlers/handlers.go`               | HandleThumbnailUpdated webhook                                         |
| `api_sidecar/internal/control/client.go`                  | SendThumbnailUploadRequest, handleThumbnailUploadResponse              |
| `api_balancing/internal/control/server.go`                | processThumbnailUploadRequest, completeThumbnailPublication            |
| `api_balancing/internal/control/thumbnail_publication.go` | Publication state machine: claim/verify/publish CAS, recovery, cleanup |
| `api_balancing/internal/handlers/thumbnail_resolve.go`    | In-cell active-version resolver endpoint (:18008)                      |
| `api_balancing/internal/jobs/purge_deleted.go`            | Hard-purge sweep: thumbnail prefix + control-row cleanup               |
| `pkg/proto/ipc.proto`                                     | ThumbnailUpload\* control messages (attempt_id)                        |

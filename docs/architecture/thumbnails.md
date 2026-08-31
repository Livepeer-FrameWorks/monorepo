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

A node uploads thumbnails OFTEN (every few seconds for a live stream), so the PUBLICATION must never let a
half-finished or crashed upload become the live version. The publication state machine therefore does NOT write the
served key directly: Foghorn MINTS each attempt, verifies + promotes it to a PRIVATE per-token candidate object, and
switches an active pointer (keyed by the globally-unique asset_key; tenant_id is ownership attribution, not part of the
identity) atomically via a monotonic CAS once the new version is fully staged and verified. A stale, partial, or
crashed attempt leaves the live version untouched. The winner is then PROJECTED (copied) to the deterministic served
key `thumbnails/{asset}/{file}` — that copy is EVENTUAL, not strictly serial (see "Deterministic projection" below).

```
MistServer (process_thumbs)
  → writes poster.jpg, sprite.jpg, sprite.vtt to /tmp/mist_thumbs/{streamName}/
  → fires THUMBNAIL_UPDATED trigger

Helmsman (webhook handler)
  → receives trigger payload (stream name + file paths)
  → sends ThumbnailUploadRequest to Foghorn via gRPC control stream

Foghorn (mint + assign)
  → resolves identity to the asset_key: stream_id (live) or artifact_hash (DVR/VOD/clip)
  → MINTS a 128-bit crypto-random attempt_id (the attempt identity + its staging segment)
  → in ONE tx, persists the attempt (status='assigned') + a per-file row per allowlisted file, each with its
    per-attempt STAGING key — BEFORE the node holds any PUT URL
  → presigns a PUT for EVERY object (all-or-nothing; a single presign failure fails the whole attempt so the
    node never gets a partial assignment) and returns ThumbnailUploadResponse{attempt_id, presigned URLs}

Helmsman
  → uploads each file to its STAGING key via presigned PUT (no edge credentials needed)
  → echoes attempt_id back in the ThumbnailUploaded confirmation (binds a Foghorn-ASSIGNED operation, not a
    node-chosen id); a swallowed send is surfaced, not dropped (recovery sweeps on lease expiry otherwise)

Foghorn (verify → promote → publish, all guarded, token-fenced)
  → acquires a publication lease that mints a per-completion TOKEN; the token names the private candidate segment
    (`v/{token}/…`, the projection SOURCE — never served directly), and every post-claim settlement CASes it so a
    stale holder (lease re-acquired by a peer) cannot publish
  → drops immediately if the attempt's lease already expired (recovery will fail it)
  → resolves the attempt's immutable asset key without a row lock, acquires the
    per-asset advisory lock, and only then row-locks and revalidates the attempt;
    every cleanup path uses the same asset-before-row order
  → for EACH object: HEAD-verifies the staged upload (provider etag/size authoritative) and PROMOTES it to its
    per-token candidate key `v/{token}/…` (private to this completion, so a stale holder can only ever write its own
    candidate, never overwrite the winner's object); a missing/failed object leaves the attempt for retry/sweep
  → guarded monotonic CAS on the tenant-scoped active pointer: replaying the same attempt is idempotent, while a
    distinct attempt advances only with a strictly greater claim. A stale or equal-claim distinct attempt that lost
    the race is settled 'failed' and its promoted objects are enqueued for cleanup, never leaked
  → on activation, IN THE SAME TX: marks the attempt 'published' and enqueues the now-superseded staging objects
    for cleanup — so a crash right after the CAS can't skip them. has_thumbnails is NOT flipped here: it stays
    false until the deterministic key is durably in place (see next step)
  → PROJECTS the winning candidate `v/{token}/…` to the deterministic served key `thumbnails/{asset}/…` in three
    bounded steps — CLAIM (short per-asset lock: gate on still-active + not-tombstoned/terminal), COPY (a
    non-transactional S3 copy, NO lock held across it), CAS-SETTLE (re-verify still-active, record
    `deterministic_projected_at`, arm the reassert clock, and only THEN flip has_thumbnails) — so the API never
    advertises a thumbnail Chandler cannot yet serve; a published-but-unprojected attempt is re-driven by recovery
  → best-effort (self-healing) after commit: Chandler cache invalidation for the asset, plus the origin-cluster
    backfill / Commodore projection
```

Control-row deletion follows publication's row order as well. It deletes the
tenant-scoped assignment first; foreign-key cascades remove its attempt objects
and active pointer in the same transaction. Cleanup never locks the active
pointer first and then waits for an assignment row held by publication.

**Deterministic projection (eventual, not strict).** The copy to the shared served key CANNOT be made strictly serial:
the destination write is unconditional and a copy the store has accepted can complete after the client context is
cancelled, so a PostgreSQL lock across it would be a false guarantee (and would hold a DB connection across S3 I/O). The
contract is therefore EVENTUAL and converges by two mechanisms, both sized to `DeterministicCopyWindow` (the max time a
straggler object can still land after the anchoring event): (1) the winning attempt arms a one-shot **reassert** (`deterministic_reassert_at`) — past the window
the recovery reconciler re-copies the still-active winner once, correcting a straggler overwrite that landed WITHIN the
window; (2) on deletion the cleanup drainer performs a **delayed second sweep** past the window, reclaiming a straggler
copy that resurrected the object within the window. A superseded loser can never expose `has_thumbnails` (the
CAS-settle rejects it).

`DeterministicCopyWindow` is `presigned upload TTL (15m) + provider-ambiguity (≈5m)` = 20m. The bounding operation
lifetime is the staging-upload PUT — the longest an object issued just before the anchoring event can still land — not
the 2m copy completion deadline; finalizing before then would strand a late-landing upload. The provider-ambiguity tail (≈5m) is an
**explicitly accepted ASSUMPTION, not a store guarantee** (object stores do not publish a bound on when they may
finish a copy accepted just before its context was cancelled). The convergence is therefore not strictly bounded: a
straggler copy that completes LATER than the window is NOT corrected by these one-shot mechanisms — it can overwrite the
winner or resurrect a deleted object. This is the **accepted, rare residual risk** for the beta: it degrades a cosmetic
thumbnail (a stale image, or a revocation gap where Chandler serves the deterministic key regardless of control rows),
never the durability of primary media.

Stuck attempts (crash mid-publish) are re-driven by a recovery reconciler: a still-`publishing` attempt within
its lease is re-published (idempotent), an expired non-terminal attempt is failed and its staged/promoted objects
swept, a `published`-but-unprojected attempt (crash between CAS and copy) is re-projected — via the leased/backoff
recovery phase so a persistently-failing source can't starve healthy projections — and a superseded version is GC'd only
after a reader-safety horizon so an in-flight read of the old version can't 404.

### S3 Key Layout

The asset_key is a stable, immutable identifier (stream_id UUID or artifact_hash) — never `playback_id` (which
can be rotated). Staging objects are per-attempt; published objects are version-addressed by the publication
TOKEN, so every completion writes DISTINCT keys and can never overwrite another's object:

```
thumbnails/{asset}/.staging/{attempt_id}/poster.jpg   # per-attempt upload target (garbage once promoted)
thumbnails/{asset}/v/{token}/poster.jpg               # private per-token candidate object (the projection SOURCE)
thumbnails/{asset}/poster.jpg                          # the DETERMINISTIC served key Chandler serves (projection TARGET)
```

The active pointer records the winning `token` (`active_token`). Once an attempt wins, Foghorn **projects** its
version object (`thumbnails/{asset}/v/{token}/…`) to the **deterministic served key** `thumbnails/{asset}/…` — an S3
copy recorded durably (`deterministic_projected_at`) — and only then exposes `has_thumbnails`. The version object is
the private candidate and monotonic-CAS + GC anchor (`active_version`); the deterministic key is what Chandler serves.
A legacy row with no token is already at the deterministic key.

The `asset_key` is the **globally-unique** public resource identity: a `stream_id` UUID or an opaque,
randomly-minted `artifact_hash` (Commodore enforces uniqueness; `foghorn.artifacts.artifact_hash` is a global
primary key). It is **not** a content hash, so it never recurs across tenants — there is exactly one owner per
asset_key. Which version is AUTHORITATIVE is decided by a Foghorn-owned DB row, `foghorn.thumbnail_active_pointer`,
keyed by `asset_key` alone — NOT by the S3 object. The deterministic served key IS a shared mutable object, but it is
only ever a PROJECTION of whichever version the pointer names; a straggler that overwrites it is corrected back to the
pointer's winner by the reassert when it lands within the copy window (a later straggler is the accepted residual risk
noted under "Deterministic projection"). `tenant_id` rides on that row (and on every attempt) as **ownership/authorization
attribution — never part of the identity**: publish and purge prove tenant ownership, but public resolution (the URL,
the S3 namespace, the deterministic served key, Chandler's cache) is by asset_key only.

Chandler treats the asset_key as an opaque path component — no format validation.

### Lifecycle cleanup

When an artifact is hard-purged (`PurgeDeletedJob`, after its main bytes are freed), the same sweep deletes the
`thumbnails/{artifact_hash}/` prefix (every version + staging + legacy object) — routed by the thumbnail's own
recorded `destination_cluster`, and by the persisted `durable_backend_local` fact when set, so an official alias
backed by THIS cell but with a different cluster id deletes locally instead of misrouting to a peer — and drops
the asset's thumbnail control rows (`thumbnail_active_pointer` + `thumbnail_task_assignment`, whose object rows
cascade). Publication and projection are fenced against a terminal parent (`status IN
('deleted','failed','expired','aborted')`) at claim, publish, AND projection.

A soft-deleted/failed **artifact** parent stops serving: the deletion tombstone fences claim/publish/projection
(a completion racing the delete never projects to the deterministic key), the API stops returning the asset's URL,
and Foghorn invalidates Chandler's cache for the asset so any cached bytes are evicted immediately. The deterministic
object itself is swept with the prefix at hard-purge; the served-after-delete window is the cache TTL (≈30s) for a
viewer holding a warm entry. The tombstone stops any NEW projection, but a straggler copy already issued before it —
completing later than `DeterministicCopyWindow` — can still resurrect the object past the delayed second sweep: the
same accepted, rare residual risk described under "Deterministic projection" above.

Version-supersession GC only reclaims **superseded** versions, never an asset's **final** active version (never
superseded). For an artifact, hard-purge removes the final version. For a **live stream** (no artifact row, so no
purge and version GC never reaches its final pointer), the pointer + object are cleaned by a **durable
cross-service saga** on stream deletion: Commodore records a cleanup obligation in `commodore.stream_cleanup_outbox`
**inside the stream-deletion transaction** (atomic — a rolled-back delete records no obligation), and an outbox
worker delivers it to Foghorn's `DeleteStreamThumbnails`, retried until acked. Foghorn records a durable
**tombstone** in `foghorn.stream_cleanup_obligation` (whose existence fences claim/publish/projection for the
rowless stream, and which triggers the Chandler cache invalidation above), then a
fenced drainer (`StreamCleanupJob`, lease/token-fenced like the staging cleanup queue) sweeps the bytes per the
snapshotted destination clusters and drops the control rows, retried from the durable row until confirmed gone.
A completion racing the deletion is fenced (settled `failed`, no pointer flip) and its objects are swept by the
drainer's full-prefix delete. High-frequency live churn is bounded by supersession GC (bounded oldest-first passes
per recovery tick).

### Chandler (Static Asset Server)

`api_assets/` — HTTP-only service (port 18020) that caches and serves thumbnail assets from S3. That is its **entire** scope.

**Boundary by asset class (why Chandler stays dumb).** Durable customer media (clips/VOD/DVR/playback indexes) keep the
strong guarantees — transactional publication, stale-writer fencing at the served key, immediate hard revocation, a
stable URL. Thumbnails are **regenerable public derivatives**, so they take the other trade: a **deterministic served
key** (`thumbnails/{id}/{file}`) plus **best-effort eventual** invalidation. You cannot have a stable URL, a stateless
Chandler, AND immediate active-version/deletion enforcement at once — pick two, per asset class. What that buys:
Chandler makes **no per-request Foghorn call** (no cold-miss resolver) and needs **no Foghorn capability/publish
handshake at serving time** (Chandler never registers with or waits on Foghorn to serve a request), and Foghorn
publishes regardless of Chandler availability. At **serving** time the two are fully decoupled. At **install** time
there is one ordering edge: Chandler's `/ready` (below) reads a readiness sentinel that the in-cell Foghorn
establishes, so the planner deploys Chandler **after** that Foghorn (a Foghorn also still requires an in-cell Chandler
— the cell-topology rule). This is a deploy-order edge, not request-time coupling. What it costs: immediate hard-revocation of already-served
bytes is traded for eventual convergence — normally the object-cache TTL plus the delayed delete sweep, with the
explicitly accepted rare residual risk (a straggler copy completing past the assumed provider tail) described under
"Deterministic projection" above. This is acceptable precisely because thumbnails are regenerable derivatives, not
primary media. All publication safety (private per-token candidate,
lease/token fencing, the winner CAS, the deterministic projection, recovery, the delayed sweep) stays **inside Foghorn**;
Chandler never sees tenants, attempts, versions, or federation. Serving-cluster placement is recorded as durable
evidence at publication (`thumbnail_serving_cluster_id` for artifacts; the ingest cell for live), never re-resolved per
request. (This section is the canonical record of the accepted static-asset-boundary design.)

**Beta limitation — cross-cell artifacts are unsupported (bytes AND thumbnails), uniformly.** A durable artifact's
bytes can only be written to a cell that owns the storage locally: VOD upload to a remote official cluster returns
`storage_delegation_unsupported_for_vod`, and a freeze authorizes ONLY a local official cluster. So in a supported
deployment an artifact is stored on its origin cell, and its thumbnail is minted there too — no remote destination
ever arises. The thumbnail publication path mirrors the byte paths: if it resolves a REMOTE official cluster (an
unsupported cross-cell topology), it drops the produced bytes fail-closed before minting, so the asset gets no
`has_thumbnails` / `thumbnail_serving_cluster_id` and no URL. This is defensive and CONSISTENT with byte storage — not
a thumbnail-specific gap, and nothing to "suppress upstream." (Live thumbnails are the exception that DOES work
cross-cell: they are minted locally on the ingest cell, `thumbnail_serving_cluster_id` = the ingest cell.) Genuine
cross-cell durable storage — and the federated thumbnail mint a storage-less self-hosted cluster will need — is
deferred to the storage-placement / cross-cluster work; the `StorageMintViaFederation` drop here is that extension
point, not dead code (see [placement-policy-engine.md](../rfcs/placement-policy-engine.md), "Seams already in place for storage-less / self-hosted clusters").

**Exactly three files, nothing else.** The public surface is
`GET|HEAD|OPTIONS /assets/{assetKey}/{file}` where `{file}` must be one of
`poster.jpg`, `sprite.jpg`, `sprite.vtt` (a hardcoded allowlist in
`internal/handlers/assets.go`); any other filename is a 404. There is **no
VOD-metadata endpoint** — VOD playback metadata lives in Foghorn
(`foghorn.vod_metadata`), and any README/ports-table note suggesting
Chandler serves it is imprecise. The only other route is the internal cache
invalidation endpoint below.

Deterministic URL-to-S3 mapping: the public URL (`/assets/{key}/poster.jpg`) maps DETERMINISTICALLY to the served
object `thumbnails/{key}/poster.jpg`. Chandler does **no** version resolution and makes **no** per-request call to
Foghorn — it is a dumb path→S3-key cache. Stability across publishes is provided upstream: Foghorn projects each new
winning version to this one deterministic key (recorded durably before `has_thumbnails` is exposed), so the URL never
changes and there is no per-read lookup to fail. Serving needs no `SERVICE_TOKEN` and no `FOGHORN_INTERNAL_URL`. The
objects are globally unique, so no tenant is needed in the public URL. There is no resolver call and no publish-time
handshake, so serving is fully decoupled from Foghorn; installation carries one deploy-order edge (Chandler after the
in-cell Foghorn, which establishes the `/ready` sentinel).

- **LRU + S3 read-through**: in-memory LRU cache (~50MB, 30s TTL; `CACHE_MAX_BYTES`/`CACHE_TTL_SECONDS`); miss → S3 `GetObject` → cache fill → serve. A missing object (`NoSuchKey`/404) serves 404; any other S3/backend failure serves 503 (fail closed, don't cache-poison a transient outage as a 404).
- `poster.jpg` uses `Cache-Control: public, max-age=30`; `sprite.jpg` and `sprite.vtt`
  use `Cache-Control: public, no-cache` and are purged from Chandler's in-memory
  cache after thumbnail uploads complete
- `POST /internal/assets/cache/invalidate` is service-token authenticated
  (registered only when `SERVICE_TOKEN` is set) and is the endpoint the
  S3-push pipeline hits: after an attempt PROJECTS to the deterministic key, Foghorn
  calls it per instance (`CHANDLER_INTERNAL_URL` mesh URLs) with
  `{assetKey, files}` so the next viewer read serves the freshly
  projected object instead of a stale cached copy inside the TTL window.
- No auth on asset reads (public assets; tenant isolation enforced at Commodore query layer)
- Prometheus metrics: cache hits/misses, S3 fetch errors, request latency

**Per-cluster S3 config from Quartermaster at boot**: when `CLUSTER_ID` is
set, `cmd/chandler/main.go` calls Quartermaster `GetCluster` and adopts the
FULL immutable descriptor tuple `s3_bucket`/`s3_endpoint`/`s3_region`/`s3_prefix`
from the cluster row VERBATIM (including a legitimately empty endpoint/prefix;
region defaults to `us-east-1`). Quartermaster is the SOLE authority — there is
no env-serving fallback, so the descriptor Chandler serves from always matches
the one Foghorn established. Credentials stay env-only
(`STORAGE_S3_ACCESS_KEY`/`SECRET_KEY`). A cluster row with **no** descriptor
means S3 is not configured: Chandler starts but serves 503 until the
control-plane desired-state bootstrap establishes the descriptor. A cluster **lookup failure** (Quartermaster
unreachable) is fatal — Chandler cannot serve without its authority and a
restart re-attempts. Read-only S3 access. Chandler also registers itself with
Quartermaster via the standard `BootstrapService` retry loop.

**Liveness vs readiness.** `/health` is process liveness — the process is up. `/ready` is the store-backed readiness
probe: it proves this instance can READ its immutable backend by **fully reading a known readiness sentinel**
(`thumbnails/.readiness`) — a real, provisioned object under the served namespace, read to completion so a mid-body
transport failure is caught too. Reading a real object (not a missing one) is what makes the check honest: an
absent-object or AccessDenied response can no longer masquerade as ready, and it needs only `s3:GetObject` (no
`ListBucket`), so a least-privilege Chandler credential still passes. The sentinel is written by **Foghorn** (the cell's
storage authority and the only writer of this bucket) at boot, CONVERGENTLY — bounded per-attempt with retry, and
Foghorn fails closed if it cannot establish it — before Foghorn serves; the object persists in S3, and Chandler stays
read-only. Because a fresh cell's Chandler cannot be ready until that sentinel exists, the planner deploys Chandler
after the in-cell Foghorn. `/ready` returns 200 only on a successful sentinel read and 503 otherwise. Readiness — not liveness — is what gates deployment
and service discovery (the rollout gate, the doctor probe, and the endpoint Quartermaster advertises all use `/ready`),
so an instance that is up but cannot serve its bucket is never rolled out or advertised as serviceable. There is no
Foghorn capability handshake at request time — Chandler only reads a static object.

### Key Files

| File                                                      | Purpose                                                                            |
| --------------------------------------------------------- | ---------------------------------------------------------------------------------- |
| `api_assets/cmd/chandler/main.go`                         | HTTP server, S3 client, LRU cache                                                  |
| `api_assets/internal/handlers/assets.go`                  | GET /assets/{assetKey}/{file}; deterministic key → S3 read-through                 |
| `api_assets/internal/cache/lru.go`                        | Thread-safe size-bounded LRU cache                                                 |
| `api_sidecar/internal/config/manager.go`                  | THUMBNAIL_UPDATED trigger registration                                             |
| `api_sidecar/internal/handlers/handlers.go`               | HandleThumbnailUpdated webhook                                                     |
| `api_sidecar/internal/control/client.go`                  | SendThumbnailUploadRequest, handleThumbnailUploadResponse                          |
| `api_balancing/internal/control/server.go`                | processThumbnailUploadRequest, completeThumbnailPublication                        |
| `api_balancing/internal/control/thumbnail_publication.go` | Publication state machine: claim/verify/publish CAS, projection, recovery, cleanup |
| `api_balancing/internal/control/server.go`                | Deterministic-key projection (versionKey→deterministic S3 copy)                    |
| `api_balancing/internal/jobs/purge_deleted.go`            | Hard-purge sweep: thumbnail prefix + control-row cleanup                           |
| `pkg/proto/ipc.proto`                                     | ThumbnailUpload\* control messages (attempt_id)                                    |

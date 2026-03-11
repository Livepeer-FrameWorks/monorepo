# Thumbnails

Two systems. Single-frame preview images (poster, stream cards) and sprite sheets (seek-bar scrubbing). Both backed by MistServer JPEG tracks, no shared code.

## Source Files

- Single-frame + MJPEG output: `mistserver/src/output/output_jpg.cpp`
- Sprite sheet generator: `mistserver/src/process/process_thumbs.cpp`
- ThumbVTT output: `mistserver/src/output/output_thumbvtt.cpp`
- Connector provisioning: `api_sidecar/internal/config/manager.go` (ThumbVTT protocol)
- Process provisioning: `api_sidecar/internal/config/manager.go` (STREAM_PROCESS trigger for live+/processing+, static config for vod+)
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

`process_thumbs.cpp` decodes video keyframes, scales them, and composes a grid as a JPEG sprite sheet plus a WebVTT timing manifest. Both are buffered as new tracks on the stream. For live+ and processing+ streams, MistProcThumbs is provisioned dynamically via the STREAM_PROCESS trigger (Foghorn returns per-stream process config). For DVR artifacts played back as vod+, STREAM_PROCESS adds MistProcThumbs on first playback if thumbnails haven't been generated yet.

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

### Flow

```
MistServer (process_thumbs)
  → writes poster.jpg, sprite.jpg, sprite.vtt to /tmp/mist_thumbs/{streamName}/
  → fires THUMBNAIL_UPDATED trigger

Helmsman (webhook handler)
  → receives trigger payload (stream name + file paths)
  → sends ThumbnailUploadRequest to Foghorn via gRPC control stream

Foghorn
  → resolves identity to stable S3 key:
    - Live streams: internal_name → stream_id (UUID) from StreamStateManager
    - Artifacts (DVR/VOD): artifact_internal_name → artifact_hash from foghorn.artifacts
  → generates presigned PUT URLs via S3 client
  → returns ThumbnailUploadResponse with presigned URLs + local paths

Helmsman
  → uploads files to S3 via presigned PUT (no edge credentials needed)
  → sends ThumbnailUploaded confirmation to Foghorn

Foghorn
  → Live streams: no DB update (frontend resolves via deterministic Chandler URL)
  → Artifacts: marks has_thumbnails=true on foghorn.artifacts
```

### S3 Key Layout

All keys use stable, immutable identifiers — never `playback_id` (which can be rotated).

```
thumbnails/{streamId}/poster.jpg       # live stream (keyed by stream_id UUID)
thumbnails/{streamId}/sprite.jpg
thumbnails/{streamId}/sprite.vtt

thumbnails/{artifactHash}/poster.jpg   # artifact (keyed by artifact_hash, 32-char hex)
thumbnails/{artifactHash}/sprite.jpg
thumbnails/{artifactHash}/sprite.vtt
```

Chandler treats the key as an opaque path component — no format validation.

### Chandler (Static Asset Server)

`api_assets/` — HTTP-only service (port 18020) that caches and serves thumbnail assets from S3.

Deterministic URL-to-S3 mapping: `/assets/{key}/poster.jpg` → S3 key `thumbnails/{key}/poster.jpg`. No database or Commodore contact needed.

- In-memory LRU cache (~50MB, 30s TTL)
- `Cache-Control: public, max-age=30` on responses
- No auth (public assets; tenant isolation enforced at Commodore query layer)
- Prometheus metrics: cache hits/misses, S3 fetch errors, request latency

Config: reuses `STORAGE_S3_*` env vars (same bucket as clips/DVR/VOD). Read-only S3 access.

### Key Files

| File                                        | Purpose                                                   |
| ------------------------------------------- | --------------------------------------------------------- |
| `api_assets/cmd/chandler/main.go`           | HTTP server, S3 client, LRU cache                         |
| `api_assets/internal/handlers/assets.go`    | GET /assets/{assetKey}/{file} handler                     |
| `api_assets/internal/cache/lru.go`          | Thread-safe size-bounded LRU cache                        |
| `api_sidecar/internal/config/manager.go`    | THUMBNAIL_UPDATED trigger registration                    |
| `api_sidecar/internal/handlers/handlers.go` | HandleThumbnailUpdated webhook                            |
| `api_sidecar/internal/control/client.go`    | SendThumbnailUploadRequest, handleThumbnailUploadResponse |
| `api_balancing/internal/control/server.go`  | processThumbnailUploadRequest, processThumbnailUploaded   |
| `pkg/proto/ipc.proto`                       | ThumbnailUpload\* control messages                        |

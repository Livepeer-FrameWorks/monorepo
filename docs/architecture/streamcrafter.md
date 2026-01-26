# StreamCrafter (`npm_studio`) — Architecture + Engineering Guide

StreamCrafter is FrameWorks’ browser-based **WHIP** publisher SDK, developed in the `npm_studio/` workspace.

Despite the folder name (`npm_studio`), the published packages are named `@livepeer-frameworks/streamcrafter-*`.

This doc is for engineers working on the SDK itself (not just consuming it).

## What it does

StreamCrafter lets a browser user publish live video/audio to a **WHIP endpoint** (typically MistServer) with:

- Capture from **camera/mic** and/or **screen share**
- Optional **multi-source composition** (scenes/layers/layouts) into a single outgoing video track
- Optional **audio mixing** (multiple sources → one mixed track)
- **Quality profiles** with runtime switching
- **Auto-reconnect** on connection loss
- Optional **WebCodecs + RTCRtpScriptTransform** encoding path to avoid background-tab throttling in browsers that support it
- Optional **Gateway mode** to resolve ingest endpoints from a FrameWorks GraphQL gateway using a `streamKey`

## What it supports (and what it doesn’t)

### Supported
- **Publishing protocol:** WHIP (WebRTC-HTTP Ingest Protocol)
- **Capture APIs:** `getUserMedia`, `getDisplayMedia`, `enumerateDevices`
- **Output path:** WebRTC via `RTCPeerConnection`
- **Encoding:**
  - Default: browser WebRTC encoders (works everywhere WebRTC works)
  - Optional: WebCodecs encoding + `RTCRtpScriptTransform` injection (Chrome/Chromium-family first)
- **Compositor (optional):** OffscreenCanvas worker with `webgpu` / `webgl` / `canvas2d` renderers
- **Framework wrappers:** React + Svelte 5
- **Quality profiles:** runtime switching + encoder overrides

### Not supported / non-goals
- RTMP ingest (StreamCrafter is WHIP-first; Gateway resolution may return RTMP/SRT URLs for display, but publishing is WHIP)
- Server-side components (everything here is client-side)
- Full “OBS replacement” feature set (no full scene editor UI; only the primitives + basic controls)

## Repo / package layout

```
npm_studio/
  packages/
    core/     # @livepeer-frameworks/streamcrafter-core
    react/    # @livepeer-frameworks/streamcrafter-react
    svelte/   # @livepeer-frameworks/streamcrafter-svelte (Svelte 5)
  playground -> ../npm_player/playground  # shared Vite playground
```

### `@livepeer-frameworks/streamcrafter-core`
Framework-agnostic engine + types:

- Orchestration: `npm_studio/packages/core/src/core/IngestControllerV2.ts`
- WHIP/WebRTC client: `npm_studio/packages/core/src/core/WhipClient.ts`
- Capture helpers: `npm_studio/packages/core/src/core/DeviceManager.ts`, `npm_studio/packages/core/src/core/ScreenCapture.ts`
- Audio mixing: `npm_studio/packages/core/src/core/AudioMixer.ts`
- Reconnect loop: `npm_studio/packages/core/src/core/ReconnectionManager.ts`
- Compositor coordinator: `npm_studio/packages/core/src/core/SceneManager.ts` (+ `npm_studio/packages/core/src/core/renderers/*`)
- WebCodecs: `npm_studio/packages/core/src/core/EncoderManager.ts` (+ worker)
- Gateway endpoint resolution: `npm_studio/packages/core/src/core/IngestClient.ts`
- Styling: `npm_studio/packages/core/src/styles/streamcrafter.css`

Worker bundles are emitted into `npm_studio/packages/core/dist/workers/*.js` by Rollup (see `npm_studio/packages/core/rollup.config.js`).

### `@livepeer-frameworks/streamcrafter-react`
React integration:

- Main hook: `npm_studio/packages/react/src/hooks/useStreamCrafterV2.ts`
- Drop-in UI: `npm_studio/packages/react/src/components/StreamCrafter.tsx`
- Optional extras:
  - Compositor controls: `npm_studio/packages/react/src/hooks/useCompositor.ts`
  - Gateway resolution: `npm_studio/packages/react/src/hooks/useIngestEndpoints.ts`

### `@livepeer-frameworks/streamcrafter-svelte`
Svelte 5 integration:

- Main store: `npm_studio/packages/svelte/src/stores/streamCrafterContextV2.ts`
- Drop-in UI: `npm_studio/packages/svelte/src/StreamCrafter.svelte`
- Optional extras:
  - Compositor store: `npm_studio/packages/svelte/src/stores/compositor.ts`
  - Gateway resolution store: `npm_studio/packages/svelte/src/stores/ingestEndpoints.ts`

## Architecture (mental model)

At a high level, StreamCrafter builds a single “output” `MediaStream` from some number of sources, then publishes it over WHIP.

### 1) Capture and sources

- Camera/mic: `DeviceManager.getUserMedia(...)`
- Screen share: `ScreenCapture.start(...)`
- Each capture becomes a `MediaSource` in `IngestControllerV2` (`sources: Map<string, MediaSource>`)
- One source is considered the **primary video** when compositor is not enabled
- Sources can be **muted**, **inactive**, and have per-source **volume** (when audio mixing is enabled)

### 2) Building the output stream

`IngestControllerV2` maintains `outputStream`:

- **Video**
  - If compositor enabled: `SceneManager.getOutputTrack()` (canvas capture stream)
  - Else: primary source’s video track (or first available video source)
- **Audio**
  - If `audioMixing`: `AudioMixer.getOutputTrack()`
  - Else: first non-muted audio track from active sources

Any source change calls `updateOutputStreamFromSources()`. If already streaming, it hot-swaps tracks via `RTCRtpSender.replaceTrack(...)`.

### 3) WHIP / WebRTC transport

Publishing is handled by `WhipClient`:

- Creates an `RTCPeerConnection`
- Adds audio/video tracks
- Creates an SDP offer and **POSTs** it to the WHIP endpoint
- Applies the SDP answer from the response
- On stop, **DELETEs** the WHIP resource URL

### 4) Reconnection

When connected and streaming, if the WHIP/WebRTC connection fails:

- `IngestControllerV2` switches to `reconnecting`
- `ReconnectionManager` runs an exponential backoff loop
- Each attempt creates a new `WhipClient` and reconnects using the current `outputStream`

### 5) Optional: Compositor (multi-source video)

When enabled (`IngestControllerV2.enableCompositor(...)`):

- `SceneManager` creates an output `HTMLCanvasElement`, transfers it to an `OffscreenCanvas`, and spawns `compositor.worker.js`
- Each media source’s video track is read using `MediaStreamTrackProcessor` and forwarded to the worker as `VideoFrame`s
- The worker renders layers using the selected renderer (`webgpu`/`webgl`/`canvas2d`)
- The final canvas is exposed as a single video track via `canvas.captureStream(frameRate)`

This is intentionally separated from publishing: compositor output is “just another video track”.

**Requirements:** `MediaStreamTrackProcessor` + `OffscreenCanvas` (Chromium-family today). If either is missing, the compositor warns and won’t initialize.

### 6) Optional: WebCodecs encoding path (“Path C”)

When `useWebCodecs` is enabled *and* `RTCRtpScriptTransform` is supported:

- `EncoderManager` spawns `encoder.worker.js`, reads frames/audio from the output stream, and encodes them with WebCodecs
- Encoded chunks are forwarded to `rtcTransform.worker.js`
- `WhipClient.attachEncoderTransform(...)` attaches `RTCRtpScriptTransform` instances to the RTP senders so the worker can inject the encoded chunks into the outbound WebRTC stream

If `RTCRtpScriptTransform` is missing, StreamCrafter falls back to browser WebRTC encoders even if WebCodecs APIs exist.

WebCodecs injection also requires codec alignment (`WhipClient.canUseEncodedInsertion()`); if negotiation doesn’t match, it falls back to browser encoding.

## Worker asset loading (important in real apps)

Core relies on workers for compositor + (optional) WebCodecs + RTP transforms:

- `npm_studio/packages/core/dist/workers/compositor.worker.js`
- `npm_studio/packages/core/dist/workers/encoder.worker.js`
- `npm_studio/packages/core/dist/workers/rtcTransform.worker.js`

Workers are loaded using `new URL('../workers/<name>.js', import.meta.url)` first, with fallbacks like:

- `/node_modules/@livepeer-frameworks/streamcrafter-core/dist/workers/<name>.js`
- `/workers/<name>.js`
- `./workers/<name>.js`

If you see runtime errors like “Failed to initialize compositor worker” in a consuming app, you typically need to ensure the worker files are reachable at one of the fallback paths (or adjust bundling to preserve `import.meta.url` worker URLs).

You can also override worker URLs in code:
- `EncoderManager` accepts `workerUrl` (or a preconstructed `Worker`)
- `WhipClient.attachEncoderTransform(encoderManager, workerUrl)` can override the RTP transform worker

## Gateway mode (ingest endpoint resolution)

When consumers pass `gatewayUrl` + `streamKey` (instead of a direct `whipUrl`):

- `IngestClient` executes `resolveIngestEndpoint(streamKey)` against the GraphQL gateway (adds `Authorization: Bearer <authToken>` if provided)
- The resolved `primary.whipUrl` becomes the effective WHIP target
- The resolved `rtmpUrl` / `srtUrl` are exposed via hooks/stores for UI display (but StreamCrafter still publishes via WHIP)
- Resolution happens in the wrappers/hooks/stores; `IngestControllerV2` still requires a `whipUrl` (direct or resolved)
- There is **no default gateway**; resolution only happens when `gatewayUrl` + `streamKey` are provided (and `whipUrl` overrides when set)

## Development workflow (local)

### Local WHIP endpoint (FrameWorks dev stack)

If you’re using the monorepo dev stack, `docker-compose.yml` runs MistServer with HTTP exposed on `http://localhost:8080`.

The shared playground (see `npm_player/playground/src/lib/mist-utils.ts`) assumes MistServer WHIP ingest URLs of the form:

- `http://localhost:8080/webrtc/<streamName>` (default stream name is `live`)

### Playground
The StreamCrafter playground is the shared Vite playground in `npm_player/playground` (symlinked into `npm_studio/playground`).

From `npm_studio/`:

```bash
pnpm install
pnpm run dev
```

This runs:

- `@livepeer-frameworks/streamcrafter-core` in watch mode
- the Vite playground dev server

### Build and typecheck

```bash
pnpm run build
pnpm run type-check
```

### Clean

```bash
pnpm run clean
```

## Where to start when changing things

- **State machine / publishing behavior:** `npm_studio/packages/core/src/core/IngestControllerV2.ts`
- **WHIP handshake / WebRTC issues:** `npm_studio/packages/core/src/core/WhipClient.ts`
- **Multi-source video composition:** `npm_studio/packages/core/src/core/SceneManager.ts` and `npm_studio/packages/core/src/workers/compositor.worker.ts`
- **WebCodecs path:** `npm_studio/packages/core/src/core/EncoderManager.ts`, `npm_studio/packages/core/src/workers/encoder.worker.ts`, `npm_studio/packages/core/src/workers/rtcTransform.worker.ts`
- **React UI:** `npm_studio/packages/react/src/components/StreamCrafter.tsx`
- **Svelte UI (Svelte 5 runes):** `npm_studio/packages/svelte/src/StreamCrafter.svelte`

## Common debugging checklist

- **Permissions:** `getUserMedia`/`getDisplayMedia` require secure context (HTTPS or localhost) and user gesture
- **Endpoint:** verify the WHIP URL is correct and accepts cross-origin requests (CORS)
- **ICE:** if stuck “connecting”, try supplying `iceServers` and inspect `chrome://webrtc-internals`
- **Workers not found:** check browser console for 404s on `/workers/*.js` or `/node_modules/.../dist/workers/*.js`
- **Background throttling:** if encoding stalls in background tabs, check whether `RTCRtpScriptTransform` is supported and whether StreamCrafter attached the encoder transform (`WhipClient.hasEncoderTransform()`)

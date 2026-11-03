# @livepeer-frameworks/streamcrafter-wc

Web Components wrapper for StreamCrafter. Registers `<fw-streamcrafter>` and composable sub-elements via Lit — camera/screen capture, scene composition, and WHIP streaming.

> **Beta.** StreamCrafter is on 0.x and its API will change a lot while cloud production and guests are built. Expect breaking changes in minor releases: pin an exact version and read the changelog before you upgrade.

`<fw-streamcrafter>` includes the producer panel (scenes, transitions, layers, logo and text overlays, media files) whenever the compositor runs; turn it off with `show-producer-panel="false"`. It also has Add Media File in the action bar, an Audio section (noise gate, de-esser, EQ, ducking) and a connection health pill while live.

**Docs:** https://logbook.frameworks.network

## Install

```bash
pnpm add @livepeer-frameworks/streamcrafter-wc
# or
npm i @livepeer-frameworks/streamcrafter-wc
```

## Usage

### Auto-register elements (side-effect import)

```js
import "@livepeer-frameworks/streamcrafter-wc/define";
```

```html
<fw-streamcrafter
  whip-url="https://edge-ingest.example.com/webrtc/your-stream-key"
  initial-profile="broadcast"
  enable-compositor
  auto-start-camera
></fw-streamcrafter>
```

### Class import (no auto-registration)

```js
import { FwStreamCrafter } from "@livepeer-frameworks/streamcrafter-wc";
```

### CDN / Script Tag

```html
<script src="https://unpkg.com/@livepeer-frameworks/streamcrafter-wc/dist/fw-streamcrafter.iife.js"></script>

<fw-streamcrafter
  whip-url="https://edge-ingest.example.com/webrtc/your-stream-key"
></fw-streamcrafter>
```

### Gateway Mode (Stream Key + Gateway URL)

```html
<fw-streamcrafter
  gateway-url="https://bridge.example.com/graphql"
  stream-key="sk_live_..."
  initial-profile="broadcast"
></fw-streamcrafter>
```

Notes:

- There is **no default gateway**; pass either `whip-url` or (`gateway-url` + `stream-key`).
- If both are provided, `whip-url` takes priority.

## Attributes

| Attribute                  | Type      | Default       | Description                                       |
| -------------------------- | --------- | ------------- | ------------------------------------------------- |
| `whip-url`                 | `string`  | `""`          | Direct WHIP streaming endpoint                    |
| `gateway-url`              | `string`  | `""`          | Gateway GraphQL endpoint                          |
| `stream-key`               | `string`  | `""`          | Stream key (used with `gateway-url`)              |
| `initial-profile`          | `string`  | `"broadcast"` | `"broadcast"` · `"professional"` · `"conference"` |
| `layout-key`               | `string`  | —             | Stream the layout belongs to; see below           |
| `enable-compositor`        | `boolean` | `true`        | Scene composition; `"false"` turns it off         |
| `show-producer-panel`      | `boolean` | `true`        | Producer panel; `"false"` turns it off            |
| `auto-start-camera`        | `boolean` | `false`       | Start camera on load                              |
| `dev-mode`                 | `boolean` | `false`       | Show advanced debug panel                         |
| `show-settings`            | `boolean` | `false`       | Open settings on load                             |
| `debug`                    | `boolean` | `false`       | Debug logging                                     |
| `theme`                    | `string`  | `""`          | Theme preset name                                 |
| `locale`                   | `string`  | `"en"`        | UI language                                       |
| `controls`                 | `string`  | `""`          | `"false"` renders only the default slot           |
| `compositor-worker-url`    | `string`  | `""`          | Custom compositor worker URL                      |
| `encoder-worker-url`       | `string`  | `""`          | Custom encoder worker URL                         |
| `rtc-transform-worker-url` | `string`  | `""`          | Custom RTC transform worker URL                   |

Set as properties: `compositorConfig` (a new `renderer` switches the running compositor), `layoutKey` (also the `layout-key` attribute: the stream the layout belongs to), `initialLayout` (a layout from `exportLayout()` for that key) and `onLayoutChange(layout, layoutKey)`. The element also fires `fw-sc-layout-change` with `{ layout, layoutKey }` shortly after the scenes, overlays or layout preset change, and has `exportLayout()` and `importLayout(layout)` methods.

To switch streams, set `whipUrl`, `layoutKey` and `initialLayout` together. The running studio keeps its camera, screen and media sources, shows the new stream's `initialLayout` (or the default layout when it is `null`), and sends the next broadcast to the new `whipUrl`; a broadcast that is live when `whipUrl` changes is stopped. A report exported for the previous key is dropped, so save each layout under the key it comes with. For the same `layoutKey`, the element keeps its current layout, also when it builds a new controller for a new profile, `debug` or worker URL. Wrap `localStorage` reads and writes in `try`/`catch`: storage can be blocked, and a layout holding a picked image can be larger than the quota.

```js
function showStream(streamId, whipUrl) {
  let saved = null;
  try {
    saved = JSON.parse(localStorage.getItem(`layout:${streamId}`) ?? "null");
  } catch {}
  studio.whipUrl = whipUrl;
  studio.layoutKey = streamId;
  studio.initialLayout = saved;
}

studio.addEventListener("fw-sc-layout-change", (e) => {
  const { layout, layoutKey } = e.detail;
  try {
    if (layoutKey) localStorage.setItem(`layout:${layoutKey}`, JSON.stringify(layout));
  } catch {}
});
```

## Methods

```js
const studio = document.querySelector("fw-streamcrafter");

// Capture
await studio.startCamera();
await studio.startScreenShare();
await studio.stopCapture();

// Streaming
await studio.startStreaming();
await studio.stopStreaming();

// Sources
studio.addCustomSource(mediaStream, "Label");
studio.removeSource(id);
studio.setSourceVolume(id, 0.8);
studio.setSourceMuted(id, true);
studio.setSourceActive(id, false);
studio.setPrimaryVideoSource(id);

// Audio
studio.setMasterVolume(0.75);
studio.getMasterVolume();

// Quality & encoding
await studio.setQualityProfile("professional");
await studio.getStats();
studio.setUseWebCodecs(true);
studio.setEncoderOverrides({ video: { bitrate: 6_000_000 } });

// Devices
await studio.getDevices();
await studio.switchVideoDevice(deviceId);
await studio.switchAudioDevice(deviceId);

// Cleanup
studio.destroy();
```

## Events

| Event                | Detail                                                   |
| -------------------- | -------------------------------------------------------- |
| `fw-sc-state-change` | `{ state, context }` — idle, capturing, streaming, error |
| `fw-sc-error`        | `{ error }`                                              |

Also supports `onStateChange` and `onError` property callbacks.

## Sub-elements

These bind to the `<fw-streamcrafter>` they sit in, or to the one whose id is in `for`, and render again whenever the studio does:

| Element                     | Description                                                  |
| --------------------------- | ------------------------------------------------------------ |
| `<fw-sc-producer-panel>`    | Scenes, layers, overlays and media while the compositor runs |
| `<fw-sc-overlay-controls>`  | Logo and text overlays of the active scene                   |
| `<fw-sc-media-controls>`    | Play, pause and loop for media files in the mix              |
| `<fw-sc-camera-picker>`     | Add Camera, with a list when there is more than one camera   |
| `<fw-sc-media-button>`      | Add Media File                                               |
| `<fw-sc-audio-controls>`    | Noise gate, de-esser, EQ and ducking                         |
| `<fw-sc-meter>`             | Level meter of the mixed audio                               |
| `<fw-sc-connection-health>` | Good, unstable or poor while live                            |

```html
<fw-streamcrafter id="studio" whip-url="..." controls="false">
  <fw-sc-producer-panel></fw-sc-producer-panel>
  <fw-sc-camera-picker></fw-sc-camera-picker>
</fw-streamcrafter>
<fw-sc-meter for="studio"></fw-sc-meter>
```

These are presentational: set their data through JS properties and handle their events or callbacks.

| Element                  | Description                                          |
| ------------------------ | ---------------------------------------------------- |
| `<fw-sc-compositor>`     | Layout controls — 14+ presets, scaling modes         |
| `<fw-sc-scene-switcher>` | Scene management with transitions (cut, fade, slide) |
| `<fw-sc-layer-list>`     | Compositor layer ordering, visibility, opacity       |
| `<fw-sc-volume>`         | Volume slider with snap-to-100% and boost mode       |
| `<fw-sc-advanced>`       | Dev panel — encoder stats, capture format, info      |

## Notes

- CSS is bundled inside Shadow DOM; no external stylesheet import needed.
- The IIFE bundle includes Web Workers for compositor, encoder, and RTC transforms.
- WebCodecs + Web Workers are used when available for background-safe encoding.

# @livepeer-frameworks/streamcrafter-wc

Web Components wrapper for StreamCrafter. Registers `<fw-streamcrafter>` and composable sub-elements via Lit — camera/screen capture, scene composition, and WHIP streaming.

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
| `enable-compositor`        | `boolean` | `true`        | Enable scene composition                          |
| `auto-start-camera`        | `boolean` | `false`       | Start camera on load                              |
| `dev-mode`                 | `boolean` | `false`       | Show advanced debug panel                         |
| `show-settings`            | `boolean` | `false`       | Open settings on load                             |
| `debug`                    | `boolean` | `false`       | Debug logging                                     |
| `theme`                    | `string`  | `""`          | Theme preset name                                 |
| `locale`                   | `string`  | `"en"`        | UI language                                       |
| `controls`                 | `string`  | `""`          | `"false"` to hide, `"stock"` for minimal          |
| `compositor-worker-url`    | `string`  | `""`          | Custom compositor worker URL                      |
| `encoder-worker-url`       | `string`  | `""`          | Custom encoder worker URL                         |
| `rtc-transform-worker-url` | `string`  | `""`          | Custom RTC transform worker URL                   |

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
studio.setEncoderOverrides({ videoBitrate: 6000000 });

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

| Element                  | Description                                          |
| ------------------------ | ---------------------------------------------------- |
| `<fw-sc-compositor>`     | Layout controls — 14+ presets, scaling modes         |
| `<fw-sc-scene-switcher>` | Scene management with transitions (cut, fade, slide) |
| `<fw-sc-layer-list>`     | Compositor layer ordering, visibility, opacity       |
| `<fw-sc-volume>`         | Volume slider with snap-to-100% and boost mode       |
| `<fw-sc-advanced>`       | Dev panel — audio processing, encoder stats, info    |

## Notes

- CSS is bundled inside Shadow DOM; no external stylesheet import needed.
- The IIFE bundle includes Web Workers for compositor, encoder, and RTC transforms.
- WebCodecs + Web Workers are used when available for background-safe encoding.

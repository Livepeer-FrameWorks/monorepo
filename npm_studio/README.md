# @livepeer-frameworks/streamcrafter

![NPM](https://img.shields.io/badge/npm-%40livepeer--frameworks%2Fstreamcrafter-blue)
![License](https://img.shields.io/badge/license-Unlicense-lightgrey)

Browser-based WHIP streaming SDK for **FrameWorks**. Camera, screen, or multi-source streaming to MistServer WHIP endpoints with WebCodecs encoding, compositor, auto-reconnection, and full customization.

## Packages

| Package                                     | Description                                                          |
| ------------------------------------------- | -------------------------------------------------------------------- |
| `@livepeer-frameworks/streamcrafter-core`   | Core streaming logic, WHIP client, WebCodecs encoder, vanilla facade |
| `@livepeer-frameworks/streamcrafter-react`  | React component, composable sub-components, and hooks                |
| `@livepeer-frameworks/streamcrafter-svelte` | Svelte 5 component, composable sub-components, and stores            |
| `@livepeer-frameworks/streamcrafter-wc`     | Lit Web Components (Shadow DOM, slots, CDN-ready)                    |

## Install

```bash
# React
npm install @livepeer-frameworks/streamcrafter-react

# Svelte
npm install @livepeer-frameworks/streamcrafter-svelte

# Vanilla / Headless
npm install @livepeer-frameworks/streamcrafter-core

# Web Components
npm install @livepeer-frameworks/streamcrafter-wc
```

CDN (no bundler):

```html
<script src="https://unpkg.com/@livepeer-frameworks/streamcrafter-wc/dist/fw-streamcrafter.iife.js"></script>
```

## Quick Start

### React

```tsx
import { StreamCrafter } from "@livepeer-frameworks/streamcrafter-react";
import "@livepeer-frameworks/streamcrafter-react/streamcrafter.css";

<StreamCrafter
  whipUrl="https://edge-ingest.example.com/webrtc/your-stream-key"
  initialProfile="broadcast"
  onStateChange={(state) => console.log(state)}
/>;
```

### Svelte

```svelte
<script lang="ts">
  import { StreamCrafter } from "@livepeer-frameworks/streamcrafter-svelte";
  import "@livepeer-frameworks/streamcrafter-svelte/streamcrafter.css";
</script>

<StreamCrafter
  whipUrl="https://edge-ingest.example.com/webrtc/your-stream-key"
  initialProfile="broadcast"
/>
```

### Web Components

```html
<fw-streamcrafter
  whip-url="https://edge-ingest.example.com/webrtc/your-stream-key"
  initial-profile="broadcast"
></fw-streamcrafter>
```

### Vanilla

```ts
import { createStreamCrafter } from "@livepeer-frameworks/streamcrafter-core";

const studio = createStreamCrafter({
  target: "#studio",
  whipUrl: "https://edge-ingest.example.com/webrtc/your-stream-key",
  profile: "broadcast",
  theme: "dracula",
  locale: "es",
});

await studio.startCamera();
await studio.goLive();
```

---

## API Reference -- createStreamCrafter()

Property-based facade for vanilla and headless usage. Follows the Q/M/S (Queries / Mutations / Subscriptions) pattern.

```ts
import { createStreamCrafter } from "@livepeer-frameworks/streamcrafter-core";

const studio = createStreamCrafter({
  target: "#studio", // optional — headless if omitted
  whipUrl: "...",
  profile: "broadcast",
  theme: "dracula",
  locale: "es",
  keyMap: { toggleStream: ["Shift+Enter", "Shift+G"] },
});
```

### Queries (read state)

| Property            | Type                           | Description                                        |
| ------------------- | ------------------------------ | -------------------------------------------------- |
| `state`             | `IngestState`                  | Current lifecycle state                            |
| `stateContext`      | `IngestStateContextV2`         | Extended state context (error, reconnection, etc.) |
| `streaming`         | `boolean`                      | Whether actively streaming                         |
| `capturing`         | `boolean`                      | Whether capturing media                            |
| `reconnecting`      | `boolean`                      | Whether reconnecting                               |
| `sources`           | `MediaSource[]`                | Active media sources                               |
| `primaryVideo`      | `MediaSource \| null`          | Primary video source                               |
| `stats`             | `Promise<IngestStats \| null>` | Current streaming stats                            |
| `devices`           | `Promise<DeviceInfo[]>`        | Available media devices                            |
| `masterVolume`      | `number`                       | Master volume (0--2)                               |
| `profile`           | `QualityProfile`               | Active quality profile                             |
| `mediaStream`       | `MediaStream \| null`          | The mixed output stream                            |
| `compositorEnabled` | `boolean`                      | Whether compositor is active                       |
| `webCodecsActive`   | `boolean`                      | Whether WebCodecs encoding is active               |
| `encoderOverrides`  | `EncoderOverrides`             | Current encoder overrides                          |

### Mutations (change state)

| Mutation                | Signature                                      | Description                          |
| ----------------------- | ---------------------------------------------- | ------------------------------------ |
| `masterVolume =`        | `set masterVolume(n: number)`                  | Set master volume (0--2)             |
| `profile =`             | `set profile(p: QualityProfile)`               | Switch quality profile               |
| `theme =`               | `set theme(t: string \| StudioThemeOverrides)` | Switch theme preset or set overrides |
| `useWebCodecs =`        | `set useWebCodecs(b: boolean)`                 | Toggle WebCodecs encoding            |
| `startCamera()`         | `(opts?) => Promise<MediaSource>`              | Start camera capture                 |
| `startScreenShare()`    | `(opts?) => Promise<MediaSource \| null>`      | Start screen share                   |
| `addCustomSource()`     | `(stream, label) => MediaSource`               | Add custom MediaStream source        |
| `goLive()`              | `() => Promise<void>`                          | Start streaming                      |
| `stop()`                | `() => Promise<void>`                          | Stop streaming                       |
| `stopCapture()`         | `() => Promise<void>`                          | Stop all capture                     |
| `removeSource()`        | `(id: string) => void`                         | Remove a media source                |
| `setSourceVolume()`     | `(id, vol) => void`                            | Set source volume (0--2)             |
| `setSourceMuted()`      | `(id, muted) => void`                          | Mute/unmute source                   |
| `setSourceActive()`     | `(id, active) => void`                         | Activate/deactivate source           |
| `setPrimaryVideo()`     | `(id) => void`                                 | Set primary video source             |
| `switchVideoDevice()`   | `(deviceId) => Promise<void>`                  | Switch video input device            |
| `switchAudioDevice()`   | `(deviceId) => Promise<void>`                  | Switch audio input device            |
| `setEncoderOverrides()` | `(overrides) => void`                          | Override encoder settings            |
| `t()`                   | `(key, vars?) => string`                       | Translate a string key               |
| `destroy()`             | `() => void`                                   | Clean up all resources               |

### Subscriptions -- Events

`on(event, handler)` returns an unsubscribe function.

```ts
const unsub = studio.on("stateChange", ({ state }) => console.log(state));
unsub();
```

| Event                 | Payload                        | Fires when                              |
| --------------------- | ------------------------------ | --------------------------------------- |
| `stateChange`         | `{ state, context? }`          | Lifecycle state transition              |
| `error`               | `{ error, recoverable }`       | Streaming error                         |
| `sourceAdded`         | `SourceAddedEvent`             | New media source added                  |
| `sourceRemoved`       | `SourceRemovedEvent`           | Media source removed                    |
| `sourceUpdated`       | `SourceUpdatedEvent`           | Source properties changed               |
| `statsUpdate`         | `IngestStats`                  | Stats snapshot updated                  |
| `deviceChange`        | `{ devices }`                  | Media devices changed                   |
| `qualityChanged`      | `{ profile, previousProfile }` | Quality profile switched                |
| `reconnectionAttempt` | `{ attempt, maxAttempts }`     | Reconnection attempt started            |
| `reconnectionSuccess` | --                             | Reconnection succeeded                  |
| `reconnectionFailed`  | `{ error }`                    | All reconnection attempts exhausted     |
| `webCodecsActive`     | `{ active }`                   | WebCodecs encoder activated/deactivated |

### Subscriptions -- Reactive State

Per-property subscriptions with immediate invocation and change tracking.

```ts
// Fires immediately with current value, then on every change
const unsub = studio.reactiveState.on("streaming", (isLive) => {
  goLiveBtn.textContent = isLive ? "Stop" : "Go Live";
});

// Read current value
const isLive = studio.reactiveState.get("streaming");
```

| Property            | Type                             | Source events                                   |
| ------------------- | -------------------------------- | ----------------------------------------------- |
| `state`             | `IngestState`                    | `stateChange`                                   |
| `stateContext`      | `IngestStateContextV2`           | `stateChange`                                   |
| `streaming`         | `boolean`                        | `stateChange`                                   |
| `capturing`         | `boolean`                        | `stateChange`                                   |
| `reconnecting`      | `boolean`                        | `stateChange`, `reconnection*`                  |
| `sources`           | `MediaSource[]`                  | `sourceAdded`, `sourceRemoved`, `sourceUpdated` |
| `primaryVideo`      | `MediaSource \| null`            | `source*`                                       |
| `masterVolume`      | `number`                         | `stateChange`                                   |
| `profile`           | `QualityProfile`                 | `qualityChanged`                                |
| `error`             | `{ error, recoverable } \| null` | `error`, `stateChange`                          |
| `compositorEnabled` | `boolean`                        | `stateChange`                                   |
| `webCodecsActive`   | `boolean`                        | `webCodecsActive`                               |

---

## Options

| Option         | Type                                    | Default             | Description                                  |
| -------------- | --------------------------------------- | ------------------- | -------------------------------------------- |
| `whipUrl`      | `string`                                | --                  | Direct WHIP endpoint URL                     |
| `whipUrls`     | `string[]`                              | --                  | Multiple WHIP endpoints (failover)           |
| `gatewayUrl`   | `string`                                | --                  | Gateway URL for endpoint resolution          |
| `streamKey`    | `string`                                | --                  | Stream key for gateway mode                  |
| `target`       | `string \| HTMLElement`                 | --                  | Mount target (optional, headless if omitted) |
| `profile`      | `QualityProfile`                        | `"broadcast"`       | Initial quality profile                      |
| `theme`        | `FwThemePreset \| StudioThemeOverrides` | `"default"`         | Theme preset or custom overrides             |
| `locale`       | `StudioLocale`                          | `"en"`              | UI language                                  |
| `translations` | `Partial<StudioTranslationStrings>`     | --                  | Custom translation overrides                 |
| `keyMap`       | `Partial<StudioKeyMap>`                 | --                  | Custom keyboard shortcuts                    |
| `reconnection` | `Partial<ReconnectionConfig>`           | `{ enabled: true }` | Reconnection settings                        |
| `audioMixing`  | `boolean`                               | `false`             | Enable Web Audio mixer                       |
| `compositor`   | `Partial<CompositorConfig>`             | --                  | Compositor configuration                     |
| `debug`        | `boolean`                               | `false`             | Enable debug logging                         |

---

## Features

### Quality Profiles

| Profile        | Resolution | Video Bitrate | Audio Bitrate |
| -------------- | ---------- | ------------- | ------------- |
| `professional` | 1920x1080  | 8 Mbps        | 192 kbps      |
| `broadcast`    | 1920x1080  | 4.5 Mbps      | 128 kbps      |
| `conference`   | 1280x720   | 2.5 Mbps      | 96 kbps       |

### Multi-Source Streaming

Camera + screen share simultaneously with individual volume/mute controls.

```ts
await studio.startCamera();
await studio.startScreenShare({ audio: true });
console.log(studio.sources); // [camera, screen]
studio.removeSource("camera-1");
```

### Audio Mixing

Web Audio API mixer for multiple sources with per-source and master volume.

```ts
studio.setSourceVolume("camera-1", 0.8);
studio.setSourceMuted("screen-1", true);
studio.masterVolume = 0.5;
```

### Compositor

Scene-based multi-source composition with layouts, transitions, and GPU renderers. **Enabled by default** — works with a single source (solo layout) and scales to multi-source layouts automatically.

```ts
const studio = createStreamCrafter({
  whipUrl: "...",
  compositor: { renderer: "auto", width: 1920, height: 1080 },
});
```

Available renderers: `canvas2d`, `webgl`, `webgpu`, `auto` (default — auto-selects WebGPU → WebGL → Canvas2D).

### WebCodecs Encoder

Hardware-accelerated encoding that continues in background tabs. **Auto-enabled on Chromium** (Chrome, Edge) where `RTCRtpScriptTransform` is available. Firefox and Safari gracefully fall back to standard WebRTC encoding.

```ts
// Force disable if needed
studio.useWebCodecs = false;
```

### Reconnection

Exponential backoff with configurable retries and endpoint rotation.

```ts
const studio = createStreamCrafter({
  whipUrl: "...",
  reconnection: { enabled: true, maxAttempts: 5, baseDelay: 1000 },
});
```

### Gateway Integration

Automatic ingest endpoint resolution from FrameWorks Gateway.

```ts
const studio = createStreamCrafter({
  gatewayUrl: "https://bridge.example.com/graphql",
  streamKey: "sk_live_...",
});
```

---

## Runtime Theming

### Preset Themes

16 built-in presets sharing the FrameWorks design system.

| Theme               | Style                  |
| ------------------- | ---------------------- |
| `default`           | Tokyo Night (built-in) |
| `light`             | Clean light background |
| `neutral-dark`      | Desaturated dark       |
| `tokyo-night`       | Cool blue-purple dark  |
| `tokyo-night-light` | Cool blue-purple light |
| `dracula`           | Purple-green dark      |
| `nord`              | Arctic blue-gray       |
| `catppuccin`        | Warm pastel dark       |
| `catppuccin-light`  | Warm pastel light      |
| `gruvbox`           | Retro warm dark        |
| `gruvbox-light`     | Retro warm light       |
| `one-dark`          | Atom-style dark        |
| `github-dark`       | GitHub dark mode       |
| `rose-pine`         | Muted rose-gold dark   |
| `solarized`         | Ethan Schoonover dark  |
| `solarized-light`   | Ethan Schoonover light |
| `ayu-mirage`        | Soft blue-orange dark  |

```ts
// Switch at runtime
studio.theme = "rose-pine";

// Custom overrides
studio.theme = {
  surfaceDeep: "220 20% 10%",
  accent: "160 80% 55%",
};
```

### CSS Custom Properties

Studio tokens use the `--fw-sc-*` prefix with bare HSL triplets.

| Token                      | Purpose             |
| -------------------------- | ------------------- |
| `--fw-sc-surface-deep`     | Deepest background  |
| `--fw-sc-surface`          | Main background     |
| `--fw-sc-surface-raised`   | Elevated surfaces   |
| `--fw-sc-text`             | Primary text        |
| `--fw-sc-text-muted`       | Secondary text      |
| `--fw-sc-text-faint`       | Disabled, hints     |
| `--fw-sc-border`           | Borders, dividers   |
| `--fw-sc-accent`           | Primary actions     |
| `--fw-sc-accent-secondary` | Special actions     |
| `--fw-sc-success`          | Live, success       |
| `--fw-sc-danger`           | Errors, destructive |
| `--fw-sc-warning`          | Warnings            |
| `--fw-sc-info`             | Info states         |
| `--fw-sc-live`             | Live badge          |

---

## Internationalization

Five built-in locale packs: `en`, `es`, `fr`, `de`, `nl`.

```ts
const studio = createStreamCrafter({
  whipUrl: "...",
  locale: "de",
  translations: {
    goLive: "Transmitir",
    stopStreaming: "Detener",
  },
});

studio.t("goLive"); // "Transmitir"
```

---

## Configurable Hotkeys

| Key           | Action                            |
| ------------- | --------------------------------- |
| `Shift+Enter` | Toggle streaming (go live / stop) |
| `M`           | Toggle mute                       |
| `C`           | Add camera                        |
| `S`           | Share screen                      |
| `,`           | Toggle settings                   |
| `]`           | Next scene                        |
| `[`           | Previous scene                    |
| `A`           | Toggle advanced panel             |

```ts
createStreamCrafter({
  whipUrl: "...",
  keyMap: {
    toggleStream: ["Shift+Enter", "Shift+G"],
    toggleMute: ["m"],
  },
});
```

---

## Framework Integration

### React

#### Monolithic (default)

```tsx
<StreamCrafter whipUrl="..." initialProfile="broadcast" />
```

#### Composable (children + sub-components)

When `children` is provided, the default UI is replaced. Sub-components auto-connect via context.

```tsx
import {
  StreamCrafter,
  StudioPreview,
  StudioMixer,
  StudioActionBar,
  StudioStatusBadge,
} from "@livepeer-frameworks/streamcrafter-react";

<StreamCrafter whipUrl="...">
  <StudioStatusBadge />
  <StudioPreview />
  <StudioMixer />
  <StudioActionBar whipUrl="..." />
</StreamCrafter>;
```

#### Hooks-only (fully custom UI)

```tsx
import {
  useStreamCrafterV2,
  useStreamCrafterContext,
} from "@livepeer-frameworks/streamcrafter-react";

function CustomBroadcaster() {
  const { state, isStreaming, startCamera, startStreaming, mediaStream } = useStreamCrafterV2({
    whipUrl: "...",
    profile: "broadcast",
    reconnection: { enabled: true, maxAttempts: 5 },
    audioMixing: true,
  });

  return (
    <div>
      <video
        ref={(el) => {
          if (el) el.srcObject = mediaStream;
        }}
        autoPlay
        muted
      />
      <button onClick={() => startCamera()}>Camera</button>
      <button onClick={() => startStreaming()}>Go Live</button>
    </div>
  );
}
```

### Svelte

#### Monolithic (default)

```svelte
<StreamCrafter whipUrl="..." initialProfile="broadcast" />
```

#### Composable (snippets + sub-components)

```svelte
<script>
  import {
    StreamCrafter,
    StudioPreview,
    StudioMixer,
    StudioActionBar,
    StudioStatusBadge,
  } from "@livepeer-frameworks/streamcrafter-svelte";
</script>

<StreamCrafter whipUrl="...">
  {#snippet children()}
    <StudioStatusBadge />
    <StudioPreview />
    <StudioMixer />
    <StudioActionBar whipUrl="..." />
  {/snippet}
</StreamCrafter>
```

Sub-components read from `getContext("fw-sc-controller")` automatically.

#### Stores-only (fully custom UI)

```svelte
<script>
  import { createStreamCrafterContextV2 } from "@livepeer-frameworks/streamcrafter-svelte";
  import { onMount, onDestroy } from "svelte";

  const crafter = createStreamCrafterContextV2();
  let state = $state({});

  onMount(() => {
    crafter.initialize({ whipUrl: "...", profile: "broadcast" });
    return crafter.subscribe((s) => (state = s));
  });
  onDestroy(() => crafter.destroy());
</script>

{#if state.isStreaming}<span>LIVE</span>{/if}
<button onclick={() => crafter.startCamera()}>Camera</button>
<button onclick={() => crafter.startStreaming()}>Go Live</button>
```

### Web Components

#### Monolithic (default)

```html
<fw-streamcrafter whip-url="..." initial-profile="broadcast"></fw-streamcrafter>
```

#### Headless (`controls="false"`)

Hides all built-in UI. Access the controller via the `.controller` property.

```html
<fw-streamcrafter id="studio" whip-url="..." controls="false"></fw-streamcrafter>

<script>
  const el = document.getElementById("studio");
  await el.startCamera();
  await el.startStreaming();

  // Or access the full controller host:
  const ctrl = el.controller;
</script>
```

#### Standalone sub-components with `for` attribute

Sub-components can be placed anywhere in the DOM and bind to a studio by ID.

```html
<fw-streamcrafter id="studio" whip-url="..." controls="false"></fw-streamcrafter>

<div class="my-custom-layout">
  <fw-sc-volume for="studio" value="1"></fw-sc-volume>
  <fw-sc-compositor for="studio"></fw-sc-compositor>
  <fw-sc-scene-switcher for="studio"></fw-sc-scene-switcher>
</div>
```

### Vanilla (`createStreamCrafter`)

Full Q/M/S API with reactive state and headless mode.

```ts
import { createStreamCrafter } from "@livepeer-frameworks/streamcrafter-core";

const studio = createStreamCrafter({
  whipUrl: "...",
  profile: "broadcast",
});

// Reactive subscriptions
studio.reactiveState.on("streaming", (isLive) => {
  document.getElementById("status").textContent = isLive ? "LIVE" : "Offline";
});

studio.reactiveState.on("sources", (sources) => {
  renderSourceList(sources);
});

await studio.startCamera();
await studio.goLive();
```

---

## States

```
idle -> requesting_permissions -> capturing -> connecting -> streaming
  ^                                  |              |            |
  |                                  v              v            v
  +-- destroyed <--- error <--------+--------------+-- reconnecting
```

| State                    | Description                           |
| ------------------------ | ------------------------------------- |
| `idle`                   | Ready to capture                      |
| `requesting_permissions` | Waiting for camera/mic access         |
| `capturing`              | Capturing media, not streaming        |
| `connecting`             | Establishing WHIP connection          |
| `streaming`              | Live and streaming                    |
| `reconnecting`           | Connection lost, attempting reconnect |
| `error`                  | Error occurred                        |
| `destroyed`              | Cleanup complete                      |

---

## TypeScript

All packages ship type declarations. Import types directly:

```ts
import type {
  IngestState,
  MediaSource,
  QualityProfile,
  StudioReactiveState,
  StudioStateMap,
  StreamCrafterInstance,
  CreateStreamCrafterConfig,
} from "@livepeer-frameworks/streamcrafter-core";
```

---

## Browser Support

| Feature                      | Required for                        |
| ---------------------------- | ----------------------------------- |
| WebRTC (`RTCPeerConnection`) | Core streaming                      |
| MediaDevices API             | Camera/screen capture               |
| WebCodecs API                | Background-safe encoding (optional) |
| Web Audio API                | Audio mixing                        |

Tested: Chrome 90+, Firefox 90+, Safari 14.1+, Edge 90+.

## License

Unlicense

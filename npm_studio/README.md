# @livepeer-frameworks/streamcrafter

![NPM](https://img.shields.io/badge/npm-%40livepeer--frameworks%2Fstreamcrafter-blue)
![License](https://img.shields.io/badge/license-Unlicense-lightgrey)

Browser-based WHIP streaming SDK for **FrameWorks**. Camera, screen, or multi-source streaming to MistServer WHIP endpoints with WebCodecs encoding, compositor, auto-reconnection, and full customization.

> **Beta.** StreamCrafter is on 0.x and its API will change a lot while cloud production and guests are built. Expect breaking changes in minor releases: pin an exact version and read the package changelog before you upgrade.

The drop-in studios (React, Svelte, Web Components) include a producer panel for scenes, transitions, layers, logo and text overlays, and media files; it is on by default whenever the compositor runs. They also have Add Media File in the action bar, an Audio section with a noise gate, de-esser, EQ and ducking, and a connection health pill while live.

## Packages

| Package                                     | Description                                                          |
| ------------------------------------------- | -------------------------------------------------------------------- |
| `@livepeer-frameworks/streamcrafter-core`   | Core streaming logic, WHIP client, WebCodecs encoder, vanilla facade |
| `@livepeer-frameworks/streamcrafter-react`  | React 17/18/19 component, composable sub-components, and hooks       |
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
| `target`       | `string \| HTMLElement`                 | --                  | Mount target (optional, headless if omitted) |
| `profile`      | `QualityProfile`                        | `"broadcast"`       | Initial quality profile                      |
| `theme`        | `FwThemePreset \| StudioThemeOverrides` | `"default"`         | Theme preset or custom overrides             |
| `locale`       | `StudioLocale`                          | `"en"`              | UI language                                  |
| `translations` | `Partial<StudioTranslationStrings>`     | --                  | Custom translation overrides                 |
| `keyMap`       | `Partial<StudioKeyMap>`                 | --                  | Custom keyboard shortcuts                    |
| `reconnection` | `Partial<ReconnectionConfig>`           | `{ enabled: true }` | Reconnection settings                        |
| `audioMixing`  | `boolean`                               | `true`              | Mix audio through the Web Audio mixer        |
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

`QualityProfile` also accepts `auto`; current encoder settings resolve that to the broadcast
profile.

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

// Noise gate, EQ and de-esser on the mix; kept until the mixer runs
studio.setAudioProcessing({
  noiseGate: { enabled: true, threshold: -45 },
  eq: { enabled: true, low: 2, mid: 0, high: -3 },
  deEsser: { enabled: true },
});

// Lower other sources while the first camera's microphone picks up sound
studio.setAudioDucking(true);
```

Audio mixing is on by default; pass `audioMixing: false` to skip the mixer.

### Compositor

Scene-based multi-source composition with layouts and GPU renderers. **Enabled by default in the React, Svelte, and Web Component drop-ins**; `createStreamCrafter()` leaves it off until you call `enableCompositor()`. It works with a single source (solo layout). The layout does not change when you add sources: apply one with the compositor controls or `applyLayout()`. `transitionTo()` switches scenes with a cut, fade, or slide.

```ts
const studio = createStreamCrafter({
  whipUrl: "...",
  compositor: { renderer: "auto", width: 1920, height: 1080 },
});

await studio.enableCompositor();
```

Available renderers: `canvas2d`, `webgl`, `webgpu`, `auto` (default — auto-selects WebGPU → WebGL → Canvas2D).

In the drop-ins, a new `compositorConfig.renderer` switches the running compositor, and turning `enableCompositor` off and on keeps the scenes and overlays.

### Saving the layout

`exportLayout()` returns the scenes, their image and text overlays and the layout preset as plain JSON; `importLayout()` applies it. Camera, screen and media-file layers are not part of it, because they belong to the captures of one session; sources added later join the active scene. A picked image is carried as a data URL, at most the size of the output frame. While the compositor is off, `exportLayout()` returns the layout from when it was last on.

```ts
const key = `studio-layout:${streamId}`;
try {
  localStorage.setItem(key, JSON.stringify(await studio.exportLayout()));
} catch {
  // Storage is blocked, or full: a layout with a picked image can pass the quota.
}

let stored = null;
try {
  stored = JSON.parse(localStorage.getItem(key) ?? "null");
} catch {}
if (stored) await studio.importLayout(stored);
```

`importLayout()` checks the layout first and refuses one it cannot apply, leaving the studio as it was.

The drop-ins take `initialLayout` and call `onLayoutChange(layout)` shortly after the scenes, overlays or preset change; `<fw-streamcrafter>` also fires `fw-sc-layout-change` with `{ layout }`. They keep the current layout when they build a new controller for the same stream (a new endpoint, profile or `debug` setting) and start from `initialLayout` again only when it changes to another object, which marks another stream.

### Encoder settings

The settings popup of every drop-in has an Encoder section with resolution, frame rate, video and audio bitrate, and the video codec when the browser can send more than one. A choice equal to the quality profile clears that override. The values apply when streaming starts. In your own UI, `encoderSettingRows()` and `withEncoderSetting()` from core give the same choices, and `setEncoderOverrides()` applies them; `video.codec` is offered to the server first.

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

The drop-in components take `gatewayUrl` and `streamKey` and resolve the ingest endpoint themselves. With `createStreamCrafter()`, resolve it with `IngestClient` and pass the WHIP URL:

```ts
import { createStreamCrafter, IngestClient } from "@livepeer-frameworks/streamcrafter-core";

const client = new IngestClient({
  gatewayUrl: "https://bridge.example.com/graphql",
  streamKey: "sk_live_...",
});
const { primary } = await client.resolve();
const studio = createStreamCrafter({ whipUrl: primary.whipUrl! });
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

The vanilla `createStreamCrafter()` facade binds all of these on the container it mounts into, and takes `keyMap` overrides. The React, Svelte, and Web Component drop-ins bind `]` and `[` for the next and previous scene while the studio has focus. No hotkey fires while typing into a field.

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

When `children` is provided, the default UI is replaced. Sub-components connect via context, including the compositor.

```tsx
import {
  StreamCrafter,
  StudioPreview,
  StudioMixer,
  StudioActionBar,
  StudioStatusBadge,
  StudioProducerPanel,
  StudioAudioControls,
} from "@livepeer-frameworks/streamcrafter-react";

<StreamCrafter whipUrl="...">
  <StudioStatusBadge />
  <StudioPreview />
  <StudioMixer />
  <StudioProducerPanel />
  <StudioAudioControls />
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

#### Studio sub-elements

`<fw-sc-producer-panel>`, `<fw-sc-overlay-controls>`, `<fw-sc-media-controls>`, `<fw-sc-camera-picker>`, `<fw-sc-media-button>`, `<fw-sc-audio-controls>`, `<fw-sc-meter>` and `<fw-sc-connection-health>` bind to the `<fw-streamcrafter>` they sit in, or to the one whose id is in `for`:

```html
<fw-streamcrafter id="studio" whip-url="..." controls="false">
  <fw-sc-producer-panel></fw-sc-producer-panel>
  <fw-sc-camera-picker></fw-sc-camera-picker>
</fw-streamcrafter>
<fw-sc-meter for="studio"></fw-sc-meter>
```

`<fw-sc-volume>`, `<fw-sc-compositor>`, `<fw-sc-scene-switcher>`, `<fw-sc-layer-list>` and `<fw-sc-advanced>` are presentational: set their data through JS properties and handle their events or callbacks. See [Slot-based Composition](https://logbook.frameworks.network/builders/ingest-wc/#slot-based-composition).

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

## Development

The player and StreamCrafter share one playground, `@livepeer-frameworks/sdk-playground` in `npm_player/playground` (`npm_studio/playground` links to it). It runs the React bindings from source:

```bash
pnpm install
pnpm --filter @livepeer-frameworks/sdk-playground dev
```

The Svelte and Web Component bindings have no playground yet; their tests run with:

```bash
pnpm --filter '@livepeer-frameworks/streamcrafter-*' test
```

## License

Unlicense

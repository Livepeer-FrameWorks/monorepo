# FrameWorks Player

Adaptive video player SDK for live and on-demand streaming. Framework-agnostic core with first-class bindings for React, Svelte 5, and Web Components.

[![npm](https://img.shields.io/npm/v/@livepeer-frameworks/player-core)](https://www.npmjs.com/package/@livepeer-frameworks/player-core)
[![TypeScript](https://img.shields.io/badge/TypeScript-5.x-blue)](https://www.typescriptlang.org/)
[![License](https://img.shields.io/npm/l/@livepeer-frameworks/player-core)](./LICENSE)

Connects to FrameWorks Gateway (GraphQL) for automatic endpoint resolution or directly to MistServer nodes. Supports HLS, DASH, WebRTC (WHEP), MP4/WebSocket, and WebCodecs playback with automatic protocol negotiation and fallback.

---

## Install

| Package                              | Use Case                                       |
| ------------------------------------ | ---------------------------------------------- |
| `@livepeer-frameworks/player-react`  | React 18+ apps                                 |
| `@livepeer-frameworks/player-svelte` | Svelte 5 apps                                  |
| `@livepeer-frameworks/player-wc`     | Web Components — Vue, Angular, CDN, plain HTML |
| `@livepeer-frameworks/player-core`   | Vanilla JS, headless, or custom integrations   |

```bash
# Pick your framework
npm install @livepeer-frameworks/player-react
npm install @livepeer-frameworks/player-svelte
npm install @livepeer-frameworks/player-wc
npm install @livepeer-frameworks/player-core
```

All framework packages peer-depend on `player-core`.

---

## Quick Start

### Vanilla / Headless

```ts
import { createPlayer } from "@livepeer-frameworks/player-core";
import "@livepeer-frameworks/player-core/player.css";

const player = createPlayer({
  target: "#player",
  contentId: "my-stream",
  contentType: "live",
  gatewayUrl: "https://gateway.example.com/graphql",
  theme: "dracula",
  autoplay: true,
  muted: true,
});

player.on("stateChange", (state) => console.log(state));
```

### React

```tsx
import { Player } from "@livepeer-frameworks/player-react";
import "@livepeer-frameworks/player-react/player.css";

<Player
  contentId="my-stream"
  contentType="live"
  options={{
    gatewayUrl: "https://gateway.example.com/graphql",
    autoplay: true,
    muted: true,
    theme: "tokyo-night",
  }}
/>;
```

### Svelte

```svelte
<script>
  import { Player } from "@livepeer-frameworks/player-svelte";
  import "@livepeer-frameworks/player-svelte/player.css";
</script>

<Player
  contentId="my-stream"
  contentType="live"
  options={{
    gatewayUrl: "https://gateway.example.com/graphql",
    autoplay: true,
    muted: true,
    theme: "nord",
  }}
/>
```

### Web Components

```html
<script src="https://cdn.jsdelivr.net/npm/@livepeer-frameworks/player-wc/dist/fw-player.iife.js"></script>

<fw-player
  content-id="my-stream"
  content-type="live"
  gateway-url="https://gateway.example.com/graphql"
  theme="catppuccin"
  autoplay
  muted
></fw-player>
```

---

## API Reference — `createPlayer()`

The primary entry point for vanilla and headless usage. Returns a `PlayerInstance` with three interaction categories: **Queries**, **Mutations**, and **Subscriptions**.

```ts
import { createPlayer } from "@livepeer-frameworks/player-core";

const player = createPlayer({
  target: "#player",
  contentId: "stream-abc",
  gatewayUrl: "https://gateway.example.com/graphql",
});
```

### Queries (Read State)

All queries are synchronous getters on the player instance.

| Property       | Type                          | Description                                        |
| -------------- | ----------------------------- | -------------------------------------------------- |
| `playerState`  | `PlayerState`                 | Current lifecycle state                            |
| `state`        | `PlayerState`                 | Alias for `playerState`                            |
| `streamState`  | `StreamState \| null`         | Upstream stream status                             |
| `endpoints`    | `ContentEndpoints \| null`    | Resolved playback endpoints                        |
| `metadata`     | `ContentMetadata \| null`     | Stream metadata from gateway                       |
| `streamInfo`   | `StreamInfo \| null`          | Active stream sources and tracks                   |
| `videoElement` | `HTMLVideoElement \| null`    | Underlying video element                           |
| `ready`        | `boolean`                     | Player initialized and ready                       |
| `currentTime`  | `number`                      | Current playback position (milliseconds)           |
| `duration`     | `number`                      | Total duration in milliseconds (Infinity for live) |
| `volume`       | `number`                      | Volume level (0-1)                                 |
| `muted`        | `boolean`                     | Whether audio is muted                             |
| `paused`       | `boolean`                     | Whether playback is paused                         |
| `playing`      | `boolean`                     | Whether actively playing                           |
| `buffering`    | `boolean`                     | Whether currently buffering                        |
| `started`      | `boolean`                     | Whether playback has started at least once         |
| `playbackRate` | `number`                      | Current playback speed                             |
| `loop`         | `boolean`                     | Whether looping is enabled                         |
| `live`         | `boolean`                     | Whether the stream is live                         |
| `nearLive`     | `boolean`                     | Whether near the live edge                         |
| `fullscreen`   | `boolean`                     | Whether in fullscreen                              |
| `pip`          | `boolean`                     | Whether in picture-in-picture                      |
| `error`        | `string \| null`              | Current error message                              |
| `quality`      | `PlaybackQuality \| null`     | Active quality level                               |
| `abrMode`      | `'auto' \| 'manual'`          | ABR selection mode                                 |
| `playerInfo`   | `{ name, shortname } \| null` | Active player engine                               |
| `sourceInfo`   | `{ url, type } \| null`       | Active source/protocol                             |
| `theme`        | `string`                      | Current theme name                                 |
| `size`         | `{ width, height }`           | Container dimensions                               |
| `capabilities` | `PlayerCapabilities`          | Runtime feature detection                          |

### Mutations (Change State)

Setters assign directly; methods are called on the instance.

| Mutation               | Signature                 | Description                          |
| ---------------------- | ------------------------- | ------------------------------------ |
| `volume`               | `set volume(n)`           | Set volume (0-1)                     |
| `muted`                | `set muted(b)`            | Set mute state                       |
| `playbackRate`         | `set playbackRate(n)`     | Set playback speed                   |
| `loop`                 | `set loop(b)`             | Enable/disable looping               |
| `abrMode`              | `set abrMode(m)`          | Switch ABR mode                      |
| `theme`                | `set theme(t)`            | Switch theme preset                  |
| `play()`               | `() => Promise<void>`     | Start playback                       |
| `pause()`              | `() => void`              | Pause playback                       |
| `seek(t)`              | `(milliseconds) => void`  | Seek to absolute time                |
| `seekBy(d)`            | `(deltaMs) => void`       | Seek relative                        |
| `jumpToLive()`         | `() => void`              | Jump to live edge                    |
| `skipForward(ms?)`     | `(milliseconds?) => void` | Skip forward (default 10000ms)       |
| `skipBack(ms?)`        | `(milliseconds?) => void` | Skip backward (default 10000ms)      |
| `togglePlay()`         | `() => void`              | Toggle play/pause                    |
| `toggleMute()`         | `() => void`              | Toggle mute                          |
| `toggleLoop()`         | `() => void`              | Toggle loop                          |
| `toggleFullscreen()`   | `() => Promise<void>`     | Toggle fullscreen                    |
| `togglePiP()`          | `() => Promise<void>`     | Toggle picture-in-picture            |
| `requestFullscreen()`  | `() => Promise<void>`     | Enter fullscreen                     |
| `requestPiP()`         | `() => Promise<void>`     | Enter PiP                            |
| `getQualities()`       | `() => Quality[]`         | List available quality levels        |
| `selectQuality(id)`    | `(id) => void`            | Lock to a specific quality           |
| `getTextTracks()`      | `() => Track[]`           | List text tracks                     |
| `selectTextTrack(id)`  | `(id \| null) => void`    | Activate a text track                |
| `getAudioTracks()`     | `() => Track[]`           | List audio tracks                    |
| `selectAudioTrack(id)` | `(id) => void`            | Switch audio track                   |
| `getTracks()`          | `() => Track[]`           | List all tracks (video, audio, text) |
| `retry()`              | `() => Promise<void>`     | Retry current connection             |
| `retryWithFallback()`  | `() => Promise<boolean>`  | Retry with next endpoint             |
| `reload()`             | `() => Promise<void>`     | Full reload                          |
| `clearError()`         | `() => void`              | Dismiss current error                |
| `getStats()`           | `() => Promise<unknown>`  | Playback statistics snapshot         |
| `setThemeOverrides(o)` | `(overrides) => void`     | Apply partial theme overrides        |
| `clearTheme()`         | `() => void`              | Reset to default theme               |
| `destroy()`            | `() => void`              | Tear down and release resources      |

### Subscriptions — Events

`on(event, listener)` returns an unsubscribe function.

```ts
const unsub = player.on("stateChange", (state) => {
  console.log("New state:", state);
});
unsub();
```

| Event               | Payload                     | Description                |
| ------------------- | --------------------------- | -------------------------- |
| `stateChange`       | `PlayerState`               | Lifecycle state transition |
| `timeUpdate`        | `{ currentTime, duration }` | Position changed (ms)      |
| `volumeChange`      | `{ volume, muted }`         | Volume or mute changed     |
| `qualityChange`     | `Quality`                   | Quality level changed      |
| `fullscreenChange`  | `boolean`                   | Fullscreen state changed   |
| `pipChange`         | `boolean`                   | PiP state changed          |
| `error`             | `PlayerError`               | Playback error             |
| `errorCleared`      | —                           | Error dismissed            |
| `streamStateChange` | `StreamState`               | Upstream status changed    |
| `endpointsResolved` | `Endpoint[]`                | Gateway returned endpoints |
| `metadataUpdate`    | `object`                    | Metadata updated           |
| `tracksChange`      | `Track[]`                   | Available tracks changed   |

### Subscriptions — Reactive State

The `subscribe` object provides per-property reactive subscriptions. Callbacks fire immediately with the current value, then on every change (deduplicated by shallow equality).

```ts
const unsub = player.subscribe.on("currentTime", (t) => {
  timeLabel.textContent = (t / 1000).toFixed(1) + "s";
});

const vol = player.subscribe.get("volume");

unsub();
player.subscribe.off(); // clear all
```

Available properties: `paused`, `playing`, `currentTime`, `duration`, `volume`, `muted`, `playbackRate`, `loop`, `buffering`, `fullscreen`, `pip`, `tracks`, `streamState`, `error`, `loading`, `ended`, `seeking`.

---

## Options

Full `CreatePlayerConfig` accepted by `createPlayer()`:

| Option           | Type                                | Default     | Description                                           |
| ---------------- | ----------------------------------- | ----------- | ----------------------------------------------------- |
| `target`         | `string \| HTMLElement`             | —           | Mount target (CSS selector or element)                |
| `contentId`      | `string`                            | —           | Stream or asset identifier                            |
| `contentType`    | `string`                            | `"live"`    | `live`, `dvr`, `clip`, or `vod`                       |
| `gatewayUrl`     | `string`                            | —           | FrameWorks Gateway GraphQL URL                        |
| `mistUrl`        | `string`                            | —           | Direct MistServer base URL                            |
| `endpoints`      | `ContentEndpoints`                  | —           | Pre-resolved endpoints (skip gateway)                 |
| `authToken`      | `string`                            | —           | Auth token for private streams                        |
| `autoplay`       | `boolean`                           | `true`      | Auto-start playback                                   |
| `muted`          | `boolean`                           | `true`      | Start muted                                           |
| `controls`       | `boolean`                           | `true`      | Show built-in controls                                |
| `poster`         | `string`                            | —           | Poster image URL                                      |
| `theme`          | `FwThemePreset`                     | `"default"` | Theme preset name                                     |
| `themeOverrides` | `FwThemeOverrides`                  | —           | CSS token overrides                                   |
| `playbackMode`   | `string`                            | `"auto"`    | `auto`, `low-latency`, `quality`, `vod`               |
| `locale`         | `string`                            | `"en"`      | UI language (`en`, `es`, `fr`, `de`, `nl`)            |
| `skin`           | `string \| SkinDefinition \| false` | `"default"` | Skin name, inline definition, or `false` for headless |
| `debug`          | `boolean`                           | `false`     | Debug logging                                         |

### Source Resolution

The player resolves playback sources through one of three modes. They are mutually exclusive — the first one set wins.

| Priority | Option       | Resolution                                             | When to Use                                                                       |
| -------- | ------------ | ------------------------------------------------------ | --------------------------------------------------------------------------------- |
| 1        | `endpoints`  | None — uses the endpoints as-is                        | You already have resolved edge node URLs (e.g. from your own orchestration layer) |
| 2        | `mistUrl`    | Fetches `json_{contentId}.js` from MistServer directly | Standalone / playground setups pointing at a known MistServer node                |
| 3        | `gatewayUrl` | Queries the FrameWorks Gateway GraphQL API             | Production deployments with multi-node routing                                    |

**`gatewayUrl` (recommended for production)**

The Gateway resolves the best edge node for the viewer, returns structured endpoints, and handles failover across clusters. This is the standard path for the FrameWorks dashboard and self-hosted multi-node deployments.

```ts
createPlayer({
  target: "#player",
  contentId: "pk_abc123",
  gatewayUrl: "https://gateway.example.com/graphql",
});
```

**`mistUrl` (direct MistServer)**

Connects directly to a MistServer node without Gateway involvement. The player fetches `json_{contentId}.js` to get the full source list and codec metadata, then runs the scoring algorithm locally. MistServer is the authority for available protocols and codecs — the player preserves the raw source types (including `ws/video/raw` for WebCodecs).

```ts
createPlayer({
  target: "#player",
  contentId: "my-stream",
  mistUrl: "https://mist.example.com:8080",
});
```

**`endpoints` (pre-resolved)**

Bypasses all resolution. You provide the endpoint structure directly. The player builds a synthetic source list from the `outputs` map. Use this only when you have your own service discovery and don't want the player to contact MistServer or Gateway at all.

```ts
createPlayer({
  target: "#player",
  contentId: "my-stream",
  endpoints: {
    primary: {
      nodeId: "edge-1",
      baseUrl: "https://edge1.example.com",
      outputs: {
        HLS: { url: "https://edge1.example.com/hls/stream/index.m3u8" },
        WHEP: { url: "https://edge1.example.com/webrtc/stream" },
      },
    },
    fallbacks: [],
  },
});
```

> When using `endpoints`, the player cannot discover MistServer-specific source types like `ws/video/raw`. Only the protocols present in `outputs` are available to the scorer.

---

## Features

### Playback Modes

| Mode          | Preference                 | Use Case              |
| ------------- | -------------------------- | --------------------- |
| `low-latency` | WebRTC → MP4/WS → HLS/DASH | Real-time interaction |
| `quality`     | MP4/WS → HLS/DASH → WebRTC | Stable, high quality  |
| `vod`         | HLS/MP4 (penalize WHEP)    | Pre-recorded content  |
| `auto`        | Balanced score-based       | Default               |

### Content Types

| Type   | Behavior                              |
| ------ | ------------------------------------- |
| `live` | Shows live badge, penalizes seeking   |
| `dvr`  | Live with DVR buffer, enables seeking |
| `clip` | Short clip, seekable, may loop        |
| `vod`  | Full seeking, duration display        |

### Multi-Engine Architecture

| Engine       | Protocols      | Notes                           |
| ------------ | -------------- | ------------------------------- |
| hls.js       | HLS (TS, CMAF) | Primary HLS for non-Safari      |
| Video.js     | HLS, DASH      | Fallback multi-protocol         |
| MewsWsPlayer | WebSocket MP4  | Custom MSE ultra-low-latency    |
| Native WHEP  | WebRTC         | Browser-native WebRTC           |
| WebCodecs    | WebSocket      | Frame-accurate, background-safe |
| Native HLS   | HLS            | Safari native                   |

### Keyboard Shortcuts

| Key                 | Action              |
| ------------------- | ------------------- |
| Space / K           | Play/pause          |
| J / ArrowLeft       | Skip back 10s       |
| L / ArrowRight      | Skip forward 10s    |
| ArrowUp / ArrowDown | Volume +/-          |
| M                   | Mute/unmute         |
| F                   | Fullscreen          |
| C                   | Captions            |
| 0-9                 | Seek to 0-90%       |
| , / .               | Frame step (paused) |

Mouse: double-click for fullscreen, click-and-hold for 2x speed, double-tap left/right for skip. Live-only streams disable seeking.

### Capabilities

```ts
if (player.capabilities.pip) player.togglePiP();
if (player.capabilities.qualitySelection) player.selectQuality("720p");
```

| Property           | Description                  |
| ------------------ | ---------------------------- |
| `fullscreen`       | Fullscreen API available     |
| `pip`              | Picture-in-picture available |
| `seeking`          | Arbitrary seeking supported  |
| `playbackRate`     | Speed control supported      |
| `audio`            | Audio track selection        |
| `qualitySelection` | Manual quality selection     |
| `textTracks`       | Subtitle/caption support     |

### Player States

`booting` → `gateway_loading` → `gateway_ready` → `selecting_player` → `connecting` → `buffering` → `playing` ↔ `paused` → `ended` / `error` → `destroyed`

---

## Theming

### Preset Themes

| Theme               | Mode  | Accent      |
| ------------------- | ----- | ----------- |
| `default`           | Dark  | Blue        |
| `light`             | Light | Blue        |
| `neutral-dark`      | Dark  | Gray        |
| `tokyo-night`       | Dark  | Purple      |
| `tokyo-night-light` | Light | Purple      |
| `dracula`           | Dark  | Pink        |
| `nord`              | Dark  | Frost blue  |
| `catppuccin`        | Dark  | Mauve       |
| `catppuccin-light`  | Light | Mauve       |
| `gruvbox`           | Dark  | Orange      |
| `gruvbox-light`     | Light | Orange      |
| `one-dark`          | Dark  | Blue        |
| `github-dark`       | Dark  | Blue        |
| `rose-pine`         | Dark  | Rose        |
| `solarized`         | Dark  | Yellow      |
| `solarized-light`   | Light | Yellow      |
| `ayu-mirage`        | Dark  | Blue-orange |

### Design Tokens (`--fw-*`)

All tokens use bare HSL triplets. Consume via `hsl(var(--fw-accent) / 0.8)`.

| Token                   | Purpose                       |
| ----------------------- | ----------------------------- |
| `--fw-accent`           | Primary interactive color     |
| `--fw-accent-secondary` | Secondary accent              |
| `--fw-success`          | Success indicators            |
| `--fw-danger`           | Error and destructive actions |
| `--fw-warning`          | Warning indicators            |
| `--fw-live`             | Live badge and pulse          |
| `--fw-surface`          | Default background            |
| `--fw-surface-deep`     | Deepest background layer      |
| `--fw-surface-raised`   | Elevated elements             |
| `--fw-surface-active`   | Active/pressed state          |
| `--fw-text`             | Primary text                  |
| `--fw-text-bright`      | High-emphasis text            |
| `--fw-text-muted`       | Secondary text                |
| `--fw-text-faint`       | Disabled and hint text        |
| `--fw-shadow-color`     | Shadow HSL base               |
| `--fw-radius`           | Border radius                 |

### Runtime Theming

```ts
player.theme = "tokyo-night";

player.setThemeOverrides({
  accent: "262 80% 60%",
  surface: "230 15% 12%",
  radius: "12px",
});

player.clearTheme();
```

```css
.fw-player-root {
  --fw-accent: 280 70% 60%;
  --fw-surface: 0 0% 10%;
}
```

---

## Framework Integration

### React — Composable Controls

```tsx
import {
  Player,
  PlayButton,
  SkipButton,
  VolumeControl,
  TimeDisplay,
  LiveBadge,
  FullscreenButton,
  ControlBar,
  SettingsMenu,
} from "@livepeer-frameworks/player-react";

<Player contentId="pk_..." contentType="live" options={{ gatewayUrl: "..." }}>
  <ControlBar>
    <PlayButton />
    <SkipButton direction="back" />
    <SkipButton direction="forward" />
    <TimeDisplay />
    <LiveBadge />
    <VolumeControl />
    <SettingsMenu />
    <FullscreenButton />
  </ControlBar>
</Player>;
```

Sub-components auto-connect via React context. For fully custom UI, use `usePlayerController`:

```tsx
import { usePlayerController } from "@livepeer-frameworks/player-react";

const { state, controller } = usePlayerController({
  contentId: "pk_...",
  contentType: "live",
  gatewayUrl: "...",
});
```

### Svelte — Composable Controls

```svelte
<script>
  import {
    Player,
    PlayButton,
    SkipButton,
    VolumeControl,
    TimeDisplay,
    LiveBadge,
    FullscreenButton,
    SettingsMenu,
  } from "@livepeer-frameworks/player-svelte";
</script>

<Player contentId="pk_..." contentType="live" options={{ gatewayUrl: "..." }}>
  {#snippet children()}
    <PlayButton />
    <SkipButton direction="back" />
    <VolumeControl />
    <TimeDisplay />
    <LiveBadge />
    <SettingsMenu />
    <FullscreenButton />
  {/snippet}
</Player>
```

Controls connect via Svelte 5 context (`getContext("fw-player-controller")`). Custom controls access the same context:

```svelte
<script>
  import { getContext } from "svelte";
  const pc = getContext("fw-player-controller");
</script>

<button onclick={() => pc?.togglePlay()}>
  {pc?.isPlaying ? "Pause" : "Play"}
</button>
```

### Web Components — Slots + `for` Attribute

```html
<!-- Slotted controls inside the player -->
<fw-player content-id="pk_..." content-type="live" gateway-url="...">
  <div slot="controls">
    <fw-play-button></fw-play-button>
    <fw-skip-button direction="back"></fw-skip-button>
    <fw-skip-button direction="forward"></fw-skip-button>
    <fw-volume-control></fw-volume-control>
    <fw-time-display></fw-time-display>
    <fw-live-badge></fw-live-badge>
    <fw-fullscreen-button></fw-fullscreen-button>
  </div>
</fw-player>

<!-- Or standalone controls anywhere in the DOM -->
<fw-player id="myplayer" content-id="pk_..." gateway-url="..."></fw-player>
<fw-play-button for="myplayer"></fw-play-button>
<fw-fullscreen-button for="myplayer"></fw-fullscreen-button>
```

Programmatic access: `document.getElementById("myplayer").controller`.

### Vanilla — Headless

```ts
const player = createPlayer({
  target: "#player",
  contentId: "pk_...",
  gatewayUrl: "...",
  skin: false, // no UI
});

player.subscribe.on("playing", (val) => {
  myButton.textContent = val ? "Pause" : "Play";
});
myButton.onclick = () => player.togglePlay();
```

---

## Advanced

### Custom Skins (Blueprints)

The vanilla player renders its UI through a **skin system**: structure descriptors (JSON layout trees) + blueprint factories (DOM builders). Override individual controls without rewriting the entire UI.

```ts
import { registerSkin, createPlayer } from "@livepeer-frameworks/player-core";

registerSkin("mybrand", {
  inherit: "default",
  blueprints: {
    settings: () => null, // hide settings
    play: (ctx) => {
      const btn = document.createElement("button");
      ctx.subscribe.on("playing", (v) => {
        btn.textContent = v ? "Pause" : "Play";
      });
      btn.onclick = () => ctx.api.togglePlay();
      return btn;
    },
  },
  tokens: { "--fw-accent": "142 70% 50%" },
});

const player = createPlayer({ target: "#player", contentId: "pk_...", skin: "mybrand" });
```

Inline skins (no registration): `skin: { inherit: "default", blueprints: { pip: () => null } }`.

Default blueprint slots: `container`, `videocontainer`, `controls`, `controlbar`, `play`, `seekBackward`, `seekForward`, `live`, `currentTime`, `spacer`, `totalTime`, `speaker`, `volume`, `settings`, `pip`, `fullscreen`, `progress`, `loading`, `error`.

Each blueprint receives a `BlueprintContext` with: `ctx.subscribe` (reactive state), `ctx.api` (full player API), `ctx.fullscreen`, `ctx.pip`, `ctx.translate()`, `ctx.timers`, `ctx.container`, `ctx.video`, `ctx.info`, `ctx.options`, `ctx.log()`.

### Custom Protocol Players

```ts
import { registerPlayer } from "@livepeer-frameworks/player-core";

registerPlayer("myproto", {
  name: "My Protocol Player",
  priority: 5,
  mimeTypes: ["application/x-myproto"],
  isBrowserSupported: () => typeof RTCPeerConnection !== "undefined",
  async build(source, video, container) {
    video.src = source.url;
    await video.play();
  },
  destroy() {
    /* clean up */
  },
});
```

Registered players participate in the scoring algorithm and are selected automatically when a matching MIME type is available.

---

## Packages

| Package                                                   | Description                                                            |
| --------------------------------------------------------- | ---------------------------------------------------------------------- |
| [`@livepeer-frameworks/player-core`](./packages/core)     | Framework-agnostic core: PlayerController, engines, CSS, themes, skins |
| [`@livepeer-frameworks/player-react`](./packages/react)   | React components and hooks                                             |
| [`@livepeer-frameworks/player-svelte`](./packages/svelte) | Svelte 5 components                                                    |
| [`@livepeer-frameworks/player-wc`](./packages/wc)         | Lit Web Components with Shadow DOM                                     |

---

## Playground

```bash
cd npm_player/playground
pnpm install
pnpm dev
```

Runs at `http://localhost:5173` with live configuration editing, theme switching, and stream connection testing.

---

## License

See [LICENSE](./LICENSE) for details.

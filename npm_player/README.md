# FrameWorks Player
![NPM](https://img.shields.io/badge/npm-%40livepeer--frameworks%2Fplayer--react-blue)
![License](https://img.shields.io/badge/license-Unlicense-lightgrey)

A player component library for **FrameWorks** with Gateway integration and intelligent protocol selection. Supports MistPlayer, DirectSource (MP4/WEBM), and WHEP (WebRTC-HTTP Egress Protocol).

The `Player` component resolves optimal endpoints via the FrameWorks Gateway, while raw components accept URIs directly for custom implementations.

> **Looking for browser-based streaming (ingest)?** See [StreamCrafter](../npm_studio/README.md) - the companion library for WHIP publishing from camera/screen.

## Packages

This library is split into three packages:

| Package | Description |
|---------|-------------|
| `@livepeer-frameworks/player-core` | Framework-agnostic core logic, player implementations, and CSS |
| `@livepeer-frameworks/player-react` | React components and hooks |
| `@livepeer-frameworks/player-svelte` | Svelte 5 components |

## Vite playground

We ship a local Vite + React playground (`npm_player/playground`) that mirrors the ShadCN/Tailwind styling used on the marketing site. It renders the published player package via a file link, provides safe mock fixtures, and only talks to real infrastructure when you explicitly opt in.

```bash
cd npm_player/playground
npm install
npm run dev
```

- Networking is disabled by default; toggle it on inside the UI before connecting to a Mist/Gateway endpoint.
- Use the “Safe presets” tab to exercise vetted public demo streams that stay off the FrameWorks balancers.
- Switch to the “Mist workspace” tab to derive RTMP/SRT/WHIP ingest URLs and WHEP/HLS/DASH playback endpoints from a single Mist base URL. Profiles, copy-to-clipboard helpers, and quick reachability checks are built in.

### Build notes

- The ESM build now ships chunked transport bundles under `dist/esm/chunks/player-*.js`. `PlayerManager` only requests the chunk that matches the selected transport (`player-hls`, `player-dash`, `player-video`, etc.), so apps avoid downloading every playback stack up front.
- The published bundle keeps heavy runtime players (`video.js`, `dashjs`, `hls.js`) and their helper utilities external. Each transport loads its vendor dependency with `import()` the moment it is actually needed, preventing double-bundling.
- The ShadCN/Tailwind surface compiles to `dist/player.css`. Publishing automatically runs `pnpm run build:css`, but if you are consuming straight from the repo run it once locally and then import the stylesheet:

```ts
// Import from the wrapper package you're using:
import '@livepeer-frameworks/player-react/player.css';  // React
import '@livepeer-frameworks/player-svelte/player.css'; // Svelte
```

- If you prefer to tree-shake the utilities directly, add `node_modules/@livepeer-frameworks/player-core/src/**/*.{ts,tsx}` to your Tailwind content array and skip the prebuilt CSS instead.

## Installation

### React

```bash
npm install --save @livepeer-frameworks/player-react
```

### Svelte 5

```bash
npm install --save @livepeer-frameworks/player-svelte
```

### Vanilla JS / Other Frameworks

```bash
npm install --save @livepeer-frameworks/player-core
```

### Local development

Run the dedicated playground (with live rebuilds of the library and CSS) via:

```bash
pnpm start
# equivalent: pnpm run dev
```

This concurrently watches the Rollup build, Tailwind stylesheet, and Vite playground so changes in `src/` hot-reload immediately.

### Styles

The player ships with a precompiled stylesheet. In most setups the CSS is auto-injected when you import the library. If you prefer to manage styles manually, add:

```ts
// Import from the wrapper package you're using:
import '@livepeer-frameworks/player-react/player.css';  // React
import '@livepeer-frameworks/player-svelte/player.css'; // Svelte
// optional: ensurePlayerStyles() forces injection when running in micro-frontends.
```

## Usage

## Controls & Shortcuts

The player ships with built-in keyboard/mouse shortcuts when the player container is focused (click/tap once to focus).

**Keyboard**
| Shortcut | Action | Notes |
|---|---|---|
| Space | Play/Pause | Hold = 2× speed (when seekable) |
| K | Play/Pause | YouTube-style |
| J / ← | Skip back 10s | Disabled on live-only |
| L / → | Skip forward 10s | Disabled on live-only |
| ↑ / ↓ | Volume ±10% | — |
| M | Mute/Unmute | — |
| F | Fullscreen toggle | — |
| C | Captions toggle | — |
| 0–9 | Seek to 0–90% | Disabled on live-only |
| , / . | Prev/Next frame (paused) | WebCodecs = true step; others = buffered-only |

**Mouse / Touch**
| Gesture | Action | Notes |
|---|---|---|
| Double‑click | Fullscreen toggle | Desktop |
| Double‑tap (left/right) | Skip ±10s | Touch only, disabled on live-only |
| Click/Tap & Hold | 2× speed | Disabled on live-only |

**Constraints**
- **Live-only** streams disable seeking/skip/2× hold and frame-step.
- **Live with DVR buffer** enables the same shortcuts as VOD.
- Frame stepping only moves within **already buffered** ranges (no network seek). WebCodecs supports true frame stepping when paused.

### Basic Usage (React)

Import the components you need:

```jsx
import { Player, MistPlayer, DirectSourcePlayer, WHEPPlayer } from '@livepeer-frameworks/player-react';
```

### Player Component (Recommended)

The `Player` component resolves viewing endpoints via the FrameWorks Gateway and renders the appropriate sub-player:

```jsx
import React from 'react';
import { Player } from '@livepeer-frameworks/player-react';

function App() {
  return (
    <div style={{ width: '100%', height: '500px' }}>
      <Player
        contentType="live"            // 'live' | 'dvr' | 'clip'
        contentId="internal-or-hash"  // stream internal name, DVR hash, or clip hash
        options={{ gatewayUrl: 'https://your-bridge/graphql' /* authToken optional */ }}
      />
    </div>
  );
}
```

Notes:
- Endpoint resolution (`resolveViewerEndpoint`) is public in the Gateway; no JWT or tenant header is required when using playback IDs.
- For private or non‑public operations, pass `authToken` to authorize Gateway queries.

### Lazy loading & prefetching

The player pulls in heavy transport stacks (`hls.js`, `dashjs`, `video.js`) on demand, so it’s best to lazy‑load the component in your app and start the import while your UI animation or skeleton plays.

#### React example

```tsx
// preload once per module
const heroPlayerLoader = () => import('@livepeer-frameworks/player-react');
const HeroPlayer = React.lazy(heroPlayerLoader);

useEffect(() => {
  heroPlayerLoader(); // begin downloading while the hero animation runs
}, []);

return (
  <React.Suspense fallback={<Spinner />}>
    <HeroPlayer contentId="live+demo" contentType="live" />
  </React.Suspense>
);
```

#### Svelte / Vanilla JS example

For non-React frameworks, use the `FrameWorksPlayer` class:

```svelte
<script lang="ts">
  import { Player } from '@livepeer-frameworks/player-svelte';
</script>

<Player
  contentId="live+demo"
  contentType="live"
  gatewayUrl="https://your-gateway/graphql"
  autoplay={true}
  muted={true}
/>
```

Or using the vanilla JS class for more control:

```svelte
<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import type { FrameWorksPlayer as FrameWorksPlayerType } from '@livepeer-frameworks/player-core';

  let container: HTMLDivElement;
  let player: FrameWorksPlayerType | null = null;

  onMount(async () => {
    const { FrameWorksPlayer } = await import('@livepeer-frameworks/player-core');

    player = new FrameWorksPlayer(container, {
      contentId: 'live+demo',
      contentType: 'live',
      gatewayUrl: 'https://your-gateway/graphql',
      autoplay: true,
      muted: true,
    });
  });

  onDestroy(() => player?.destroy());
</script>

<div bind:this={container}></div>
```

This pattern keeps the main bundle lean, while ensuring the video libraries are already in flight when the user expects playback.

### Manual PlayerManager usage

When you work with the registry yourself (e.g. for headless controls or server-driven embeds), make sure transports are loaded once per manager:

```ts
import {
  createPlayerManager,
  ensurePlayersRegistered,
  type StreamInfo,
  type PlayerOptions,
} from '@livepeer-frameworks/player-core';

const manager = createPlayerManager({ debug: true });

await ensurePlayersRegistered(manager); // idempotent, downloads transport chunks as needed

const container = document.getElementById('player-root')!;
const streamInfo: StreamInfo = { /* Gateway-derived sources */ };
const options: PlayerOptions = { autoplay: true, controls: true };

const videoElement = await manager.initializePlayer(container, streamInfo, options);
```

`ensurePlayersRegistered` caches the asynchronous imports per manager, so subsequent calls are free and any lazily fetched chunks stay warm in the browser cache.

### FrameWorksPlayer (Vanilla JS Class)

For non-React environments (Svelte, Vue, Angular, plain HTML), use the `FrameWorksPlayer` class. It provides the same functionality as the React `<Player />` component but with a constructor-based API.

```ts
import { FrameWorksPlayer } from '@livepeer-frameworks/player-core';
import '@livepeer-frameworks/player-core/player.css'; // or import from wrapper package

const player = new FrameWorksPlayer('#player-container', {
  contentId: 'my-stream',
  contentType: 'live',
  gatewayUrl: 'https://your-gateway/graphql',
  autoplay: true,
  muted: true,
  controls: true,
  onStateChange: (state, context) => {
    console.log('Player state:', state, context);
  },
  onReady: (videoElement) => {
    console.log('Player ready!', videoElement);
  },
  onError: (error) => {
    console.error('Player error:', error);
  },
});

// Playback control
player.play();
player.pause();
player.seek(30);
player.setVolume(0.5);
player.setMuted(false);
player.jumpToLive();

// State queries
player.getState();        // 'playing' | 'paused' | 'buffering' | ...
player.getCurrentTime();  // number (seconds)
player.getDuration();     // number (seconds)
player.isReady();         // boolean

// Event subscription
const unsub = player.on('timeUpdate', ({ currentTime, duration }) => {
  console.log(`${currentTime} / ${duration}`);
});
unsub(); // unsubscribe

// Cleanup
player.destroy();
```

#### FrameWorksPlayerOptions

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `contentId` | string | required | Stream name, DVR hash, or clip hash |
| `contentType` | 'live' \| 'dvr' \| 'clip' | required | Content type |
| `gatewayUrl` | string | - | Gateway GraphQL endpoint |
| `authToken` | string | - | Bearer token for private streams |
| `endpoints` | ContentEndpoints | - | Pre-resolved endpoints (skip gateway) |
| `autoplay` | boolean | true | Auto-start playback |
| `muted` | boolean | true | Start muted |
| `controls` | boolean | true | Show player controls |
| `poster` | string | - | Poster/thumbnail image URL |
| `debug` | boolean | false | Enable debug logging |
| `onStateChange` | function | - | Called on player state changes |
| `onStreamStateChange` | function | - | Called on stream state changes (live) |
| `onTimeUpdate` | function | - | Called on time updates |
| `onReady` | function | - | Called when player is ready |
| `onError` | function | - | Called on errors |

#### Player States

The `onStateChange` callback receives one of these states:

- `booting` – Initializing
- `gateway_loading` – Resolving endpoints from gateway
- `gateway_ready` – Endpoints resolved
- `gateway_error` – Gateway resolution failed
- `no_endpoint` – No endpoints available
- `selecting_player` – Choosing best player/protocol
- `connecting` – Connecting to stream
- `buffering` – Buffering data
- `playing` – Playing
- `paused` – Paused
- `ended` – Playback ended
- `error` – Playback error
- `destroyed` – Player destroyed

### Thumbnail Support

The `Player` component supports thumbnail images for all player types:

#### MistPlayer Poster Override
For MistPlayer, the `thumbnailUrl` overrides the default poster image:

```jsx
<Player 
  streamName="your-stream-name" 
  playerType="mist"
  thumbnailUrl="https://example.com/thumbnail.jpg"
/>
```

#### Canvas/WHEP Player Overlays
For Canvas and WHEP players, you can use interactive thumbnail overlays:

##### Click-to-Play Mode
Shows a thumbnail image with a play button until the user clicks to start:

```jsx
<Player 
  streamName="your-stream-name" 
  playerType="canvas" // or "whep"
  thumbnailUrl="https://example.com/thumbnail.jpg"
  clickToPlay={true}
/>
```

##### Autoplay Muted Mode
Starts playing muted with a "Click to unmute" overlay:

```jsx
<Player 
  streamName="your-stream-name" 
  playerType="canvas" // or "whep"
  thumbnailUrl="https://example.com/thumbnail.jpg"
  autoplayMuted={true}
/>
```

**Note:** MistPlayer uses `thumbnailUrl` as a poster image override, while Canvas and WHEP players support interactive thumbnail overlays with click-to-play and autoplay-muted functionality.

### Raw MistPlayer Component

Use MistPlayer directly when you already have resolved URIs:

```jsx
import React from 'react';
import { MistPlayer } from '@livepeer-frameworks/player-react';

function App() {
  const htmlUrl = "https://edge.example.com/view/your-stream-name.html";
  const playerJsUrl = "https://edge.example.com/view/player.js";

  return (
    <div style={{ width: '100%', height: '500px' }}>
      <MistPlayer 
        streamName="your-stream-name"
        htmlUrl={htmlUrl}
        playerJsUrl={playerJsUrl}
        developmentMode={false}
      />
    </div>
  );
}
```

### DirectSourcePlayer (Raw Component)

Use DirectSourcePlayer when you have a direct MP4/WEBM URL (small VOD):

```jsx
import React from 'react';
import { DirectSourcePlayer } from '@livepeer-frameworks/player-react';

function App() {
  const mp4Url = "https://edge.example.com/videos/your-clip.mp4";
  
  return (
    <div style={{ width: '100%', height: '500px' }}>
      <DirectSourcePlayer src={mp4Url} controls poster="https://example.com/poster.jpg" />
    </div>
  );
}
```

### Raw WHEPPlayer Component

Use WHEPPlayer directly for WHEP (WebRTC-HTTP Egress Protocol) streaming:

```jsx
import React from 'react';
import { WHEPPlayer } from '@livepeer-frameworks/player-react';

function App() {
  const whepUrl = "https://edge.example.com/view/webrtc/your-stream-name";
  
  return (
    <div style={{ width: '100%', height: '500px' }}>
      <WHEPPlayer whepUrl={whepUrl} />
    </div>
  );
}
```

## Component Props

### Player

| Prop | Type | Required | Description |
|------|------|----------|-------------|
| `contentType` | 'live' \| 'dvr' \| 'clip' | Yes | Content category |
| `contentId` | string | Yes | Internal name or DVR/clip hash |
| `endpoints` | ContentEndpoints | No | Pre-resolved endpoints (skips Gateway) |
| `thumbnailUrl` | string | No | Poster/overlay image |
| `options` | PlayerOptions | No | See Options below |

Options (PlayerOptions):
- `gatewayUrl?`: string – Gateway GraphQL endpoint
- `authToken?`: string – Bearer token (if required)
- `autoplay?`: boolean
- `muted?`: boolean
- `controls?`: boolean
- `preferredProtocol?`: 'auto' | 'whep' | 'mist' | 'native'
- `analytics?`: { enabled: boolean; endpoint?: string; sessionTracking: boolean }
- `branding?`: { logoUrl?; showLogo?; position?: 'top-left' | 'top-right' | 'bottom-left' | 'bottom-right'; width?; height?; clickUrl? }
- `debug?`: boolean
- `verboseLogging?`: boolean

### MistPlayer (Raw Component)

| Prop | Type | Required | Description |
|------|------|----------|-------------|
| `streamName` | string | Yes | Stream name |
| `htmlUrl` | string | No | Full viewer HTML url |
| `playerJsUrl` | string | No | Full player.js url |
| `developmentMode` | boolean | No | Use Mist 'dev' skin |
| `muted` | boolean | No | Start muted |
| `poster` | string | No | Poster image |

### DirectSourcePlayer (Raw Component)

| Prop | Type | Required | Description |
|------|------|----------|-------------|
| `src` | string | Yes | MP4 or WEBM URL |
| `muted` | boolean | No | Start muted |
| `controls` | boolean | No | Show native controls |
| `poster` | string | No | Poster image |

### WHEPPlayer (Raw Component)

| Prop | Type | Required | Description |
|------|------|----------|-------------|
| `whepUrl` | string | Yes | WHEP endpoint URL (e.g., "https://server.com/view/webrtc/streamName") |
| `autoPlay` | boolean | No | Whether to auto-play the stream (defaults to true) |
| `muted` | boolean | No | Whether to start muted (defaults to true) |
| `onError` | function | No | Callback function for error events |
| `onConnected` | function | No | Callback function when connection is established |
| `onDisconnected` | function | No | Callback function when connection is lost |

## Player Types

Recommended: use the `Player` component. It selects the most suitable underlying player/protocol for the current environment based on available endpoints.

Advanced: raw players are exposed if you must force a specific path.

### Raw Players (advanced)
- `WHEPPlayer`: WebRTC (WHEP). Use when low latency must be forced.
- `Html5NativePlayer` / `HlsJsPlayer` / `DashJsPlayer` / `VideoJsPlayer`: Force a specific HTML5/MSE stack.
- `MistPlayer`: Embedded Mist viewer. Typically a last resort; the `Player` already integrates Mist behavior when appropriate.

## How It Works

### Player Resolution
The `Player` component uses backend-provided viewer endpoints and an internal selection algorithm to choose an underlying player/protocol for the current environment. Selection heuristics may evolve without breaking the public API.

### Profiles (planned)
We plan to add selector presets (e.g., `low-latency-live`, `standard-live`, `vod`) to bias latency vs. quality decisions. Until then, prefer the default `Player` without forcing a raw player unless you have a specific reason.

### Raw Components (Direct URIs)
The raw player components accept URIs directly, giving you full control over which servers to use. This is useful when:

- You want to implement your own load balancing logic
- You're connecting to a specific known server
- You're building a custom player interface
- You need to bypass the FrameWorks load balancer (Foghorn)

## FrameWorks Gateway

This library is designed to work with the FrameWorks Gateway and Foghorn services:

- **Viewer resolution**: GraphQL `resolveViewerEndpoint`
- **Outputs**: Backend provides protocol endpoints; WHEP derived only from HTML when missing
- **Strict consumption**: Player uses backend URLs verbatim

## Release / Publishing

1) Verify version and metadata
- Update `package.json` version (semver: patch/minor/major)
- Ensure fields are correct:
  - `main: dist/index.cjs.js`
  - `module: dist/index.esm.js`
  - `types: dist/index.d.ts`
  - `files: ["dist"]`
  - Optional first publish: `publishConfig: { "access": "public" }`
  - Optional: add `"type": "module"` to silence Node warning

2) Install and build
```bash
npm install
npm run type-check
npx rollup -c
npm pack --dry-run
```

3) Publish to npm
```bash
npm whoami           # or npm login
# First publish for scoped package:
npm publish --access public
# Subsequent publishes:
npm publish
# If 2FA enabled:
npm publish --otp <code>
```

4) Tag and verify
```bash
git tag v<version>
git push --tags

# Smoke test in a clean project
mkdir /tmp/fw-player-test && cd /tmp/fw-player-test
npm init -y
npm install @frameworks/player
```

Tips
- Use `npm version patch|minor|major -m "release %s"` to bump, tag, and commit in one step
- Ensure dist/ contains CJS, ESM, and .d.ts files before publishing

# FrameWorks Player (`npm_player`) — Architecture + Engineering Guide

FrameWorks Player is the platform’s **playback SDK**: a set of npm packages that render video and intelligently choose the best playback transport (HLS/DASH/WebRTC/etc.) for a given browser and stream.

It is designed to work both:

- **inside FrameWorks** (the dashboard uses it), and
- **outside FrameWorks** (third-party apps can embed the player and use the Gateway for endpoint discovery).

If you’re looking for browser-based ingest/publishing, this is _not_ it — see StreamCrafter (`npm_studio`) instead (`docs/architecture/streamcrafter.md`).

---

## What it does

At a high level, the player:

1. **Discovers viewing endpoints** (either via the FrameWorks Gateway GraphQL API, or from pre-resolved inputs).
2. **Builds a “stream info” model** (sources + codec/track metadata) from MistServer outputs and/or Gateway metadata.
3. **Selects the best transport + implementation** for the current browser using a score-based system (with optional mode hints like “low-latency”).
4. **Manages lifecycle and fallbacks** (attach, play, retry, fallback to another protocol/player on failure).
5. **Optionally monitors and reports** quality/telemetry, and exposes debug panels for engineers.

---

## Repo / package layout

```
npm_player/
  packages/
    core/     # @livepeer-frameworks/player-core (framework-agnostic engine + CSS)
    react/    # @livepeer-frameworks/player-react (React wrapper + UI)
    svelte/   # @livepeer-frameworks/player-svelte (Svelte 5 wrapper + UI)
    wc/       # @livepeer-frameworks/player-wc (Lit Web Components, Shadow DOM)
  playground/ # local Vite app for developing/testing the player packages
```

### `@livepeer-frameworks/player-core`

Framework-agnostic engine + transports:

- Orchestration state machine: `npm_player/packages/core/src/core`
- Player selection (scoring + caching): `npm_player/packages/core/src/core`, `npm_player/packages/core/src/core`
- Gateway endpoint discovery (GraphQL): `npm_player/packages/core/src/core`
- MistServer stream state polling (WS/HTTP): `npm_player/packages/core/src/core`
- Transport implementations: `npm_player/packages/core/src/players/*`
- Styles: `npm_player/packages/core/src/styles/player.css` → copied to `dist/player.css` on build (`tailwind.css` is an internal utility source, not shipped)

### `@livepeer-frameworks/player-react`

React integration + UI:

- Drop-in component: `npm_player/packages/react/src/components`
- Headless hook (wraps core controller): `npm_player/packages/react/src/hooks`
- Optional hooks: endpoints, stream state, quality, telemetry (`npm_player/packages/react/src/hooks/*`)

### `@livepeer-frameworks/player-svelte`

Svelte 5 integration + UI:

- Drop-in component: `npm_player/packages/svelte/src`
- Stores wrapping the core controller: `npm_player/packages/svelte/src/stores/*`

### `@livepeer-frameworks/player-wc`

Lit Web Components integration + UI (Shadow DOM encapsulation):

- Drop-in element: `npm_player/packages/wc/src/components`
- ReactiveController (wraps core controller): `npm_player/packages/wc/src/controllers`
- 16 child components matching React/Svelte UI parity
- Three build outputs: ESM (bundlers), CJS (Node/SSR), IIFE (CDN `<script>` tag)

---

## Public API surface (what consumers import)

### React (recommended in React apps)

- Install: `npm i @livepeer-frameworks/player-react`
- Import CSS once (if your setup doesn't auto-inject): `import '@livepeer-frameworks/player-react/player.css'`
- Use:

```tsx
import { Player } from "@livepeer-frameworks/player-react";

<Player
  contentType="live" // 'live' | 'dvr' | 'clip' | 'vod'
  contentId="pk_..." // playbackId for live, or artifact playbackId for clip/dvr/vod
  options={{ gatewayUrl: "https://your-bridge/graphql" }}
/>;
```

### Svelte 5

- Install: `npm i @livepeer-frameworks/player-svelte`
- Use:

```svelte
<script lang="ts">
  import { Player } from "@livepeer-frameworks/player-svelte";
  import "@livepeer-frameworks/player-svelte/player.css";
</script>

<Player
  contentType="live"
  contentId="pk_..."
  options={{ gatewayUrl: "https://your-bridge/graphql" }}
/>
```

### Web Components (CDN / any framework)

- Install: `npm i @livepeer-frameworks/player-wc`
- Or IIFE via CDN: `<script src="https://cdn.jsdelivr.net/npm/@livepeer-frameworks/player-wc/dist/fw-player.iife.js"></script>`
- Use:

```html
<fw-player
  content-type="live"
  content-id="pk_..."
  gateway-url="https://your-bridge/graphql"
  autoplay
  muted
  controls
></fw-player>
```

### Vanilla / other frameworks

Use the vanilla wrapper entrypoint from core:

- Install: `npm i @livepeer-frameworks/player-core`
- Import: `import { FrameWorksPlayer } from '@livepeer-frameworks/player-core/vanilla'`
- Mount:

```ts
import { FrameWorksPlayer } from "@livepeer-frameworks/player-core/vanilla";
import "@livepeer-frameworks/player-core/player.css"; // vanilla uses core directly

const player = new FrameWorksPlayer("#target", {
  contentType: "live",
  contentId: "pk_...",
  gatewayUrl: "https://your-bridge/graphql",
});
```

---

## How Gateway endpoint discovery works

In “Gateway mode” the player calls the public GraphQL query (contentId is the playbackId for live and artifacts; contentType is optional):

- Schema: `pkg/graphql` (`Query.resolveViewerEndpoint`)
- Client: `npm_player/packages/core/src/core`

The query returns:

- `primary`: the best node + URL for the current viewer
- `fallbacks`: additional candidate nodes
- `outputs`: protocol-specific URLs (HLS/WHEP/DASH/etc.) for that node

This allows:

- geo-aware routing (viewer IP → closest/healthiest edge)
- fast protocol switching/fallbacks without doing another routing request

---

## Runtime architecture (mental model)

The majority of “business logic” lives in `PlayerController` (core). React/Svelte primarily provide UI + wiring.

### 1) Inputs

`PlayerController` can be booted with either:

- **Pre-resolved endpoints** (`ContentEndpoints`): bypasses the Gateway
- **Gateway config** (`gatewayUrl` + `contentType` + `contentId`): resolves endpoints via GraphQL
- **Direct MistServer** (`mistUrl` + `contentId`): fetches Mist JSON metadata from a specific node

### 2) Resolution + stream model

Flow (Gateway mode):

```
Player (React/Svelte/Vanilla)
  -> PlayerController
    -> GatewayClient.resolve()            (GraphQL: resolveViewerEndpoint)
    -> buildStreamInfoFromEndpoints()     (convert outputs -> stream sources)
```

For live streams, the controller also uses MistServer APIs to poll stream state (online/offline, tracks, buffer window, etc.) via `StreamStateClient`.

### 3) Selection (players + protocols)

Selection is done by `PlayerManager`:

- registers a set of transport adapters (players) via `ensurePlayersRegistered()` (`npm_player/packages/core/src/core`)
- computes scores for `(player × source)` combinations via `scorePlayer()` (`npm_player/packages/core/src/core`)
- caches results by _content shape_ (source types + codecs), not by object identity, to avoid rerunning scoring every UI render

### 4) Lifecycle + fallback

Once a winner is chosen, `PlayerController` initializes the selected `IPlayer` implementation, attaches it to a container, and manages:

- play/pause/seek/volume/fullscreen/PiP
- retries and fallbacks to alternate `(player, source)` combinations
- quality monitoring and ABR (where supported)

---

## Supported protocols / transports

The source-of-truth mapping for MistServer `source[].type` → “human name / preferred player / supported” is in:

- `npm_player/packages/core/src/core` (`MIST_SOURCE_TYPES`)

In practice, the player can handle:

### “Normal” playback

- **HLS** (`html5/application/vnd.apple.mpegurl`) via the HLS player adapter or Video.js (VHS) (`HlsJsPlayerImpl` / `VideoJsPlayerImpl`)
- **DASH** (`dash/video/mp4`) via the DASH player adapter (`DashJsPlayerImpl`)
- **MP4/WebM progressive** (`html5/video/mp4`, `html5/video/webm`) via native `<video>` (`NativePlayerImpl`)

### Low latency / realtime

- **WHEP (WebRTC-HTTP Egress Protocol)** (`whep`) via browser WebRTC (handled by the native/webrtc path)
- **Mist WebRTC** (`webrtc`) via `MistWebRTCPlayerImpl`
- **WebSocket MP4 (MEWS)** (`ws/video/mp4`, `wss/video/mp4`) via `MewsWsPlayerImpl`
- **WebSocket raw/H264** (`ws/video/raw`, `ws/video/h264`) via `WebCodecsPlayerImpl` (includes a dedicated worker bundle)

### Track-like side channels (not primary playback)

- WebVTT/SRT subtitle sources are managed as tracks (not standalone transports).

### Legacy fallback

- **MistServer player.js** (`mist/legacy`) via `MistPlayerImpl` when other players fail.

### Important scoring notes

Some transports are intentionally **penalized** (deprioritized) even if supported (for reliability/UX reasons). The current penalties/blacklist are defined in:

- `npm_player/packages/core/src/core` (`PROTOCOL_BLACKLIST`, `PROTOCOL_PENALTIES`, `MODE_PROTOCOL_BONUSES`)

---

## Styling and theming

The player ships a prebuilt stylesheet (plain CSS, copied to `dist/player.css`):

- Import from wrapper: `@livepeer-frameworks/player-react/player.css` or `@livepeer-frameworks/player-svelte/player.css`
- Vanilla/core direct: `@livepeer-frameworks/player-core/player.css`
- Build source: `npm_player/packages/core/src/styles/player.css`

Key design constraints:

- Styles are wrapped in a dedicated CSS layer: `@layer fw-player` (host app styles win by default).
- Tokens are scoped to `.fw-player-surface` (no `:root` pollution).
- UI follows the same “slabs / seams” philosophy as the dashboard (`docs/standards/design-system.md`).

---

## Local development workflow

From repo root:

- Start the player dev loop + playground: `pnpm -C npm_player dev`
- Build once: `pnpm -C npm_player build`
- Typecheck: `pnpm -C npm_player type-check`

The playground is a Vite app in `npm_player/playground` and is the fastest way to iterate on UX, protocols, and debug tooling.

---

## Making changes safely (common tasks)

### Add a new transport/player implementation

1. Implement `IPlayer` (core) under `npm_player/packages/core/src/players/`.
2. Register it in `npm_player/packages/core/src/core`.
3. Add/adjust scoring rules in `npm_player/packages/core/src/core` (and confirm it’s not accidentally blacklisted).
4. If it depends on a heavy third-party library, keep it as a peer dependency and use runtime `import()` inside the player implementation.
5. Add/update the Mist source type mapping in `npm_player/packages/core/src/core` if MistServer will advertise a new `source[].type`.

### Change what the Gateway returns

If `resolveViewerEndpoint` adds/removes fields, confirm the player’s GraphQL clients still match:

- `npm_player/packages/core/src/core`
- `npm_player/packages/react/src/hooks` (legacy hook used by some UI paths)

---

## Troubleshooting checklist

- “It builds but playback is blank”: check browser console for missing peer deps (HLS/DASH/Video player adapters) and CORS errors on the playback URL.
- “WebRTC/WHEP fails locally”: many browsers require HTTPS + valid certs for some WebRTC paths; also confirm ICE servers if needed.
- “Wrong protocol picked”: enable `debug`/`devMode` and inspect the selection breakdown from `PlayerManager` (scores + penalties).

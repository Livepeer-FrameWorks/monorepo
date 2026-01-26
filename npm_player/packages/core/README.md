# @livepeer-frameworks/player-core

Core (headless) player engine used by the React/Svelte wrappers. Provides `PlayerController`, selection logic, and player implementations (WebCodecs, HLS, etc).

**Docs:** `docs.frameworks.network`

## Install

```bash
pnpm add @livepeer-frameworks/player-core
# or
npm i @livepeer-frameworks/player-core
```

## Basic Usage

```ts
import { PlayerController } from '@livepeer-frameworks/player-core';

const controller = new PlayerController({
  contentId: 'pk_...', // playbackId
  contentType: 'live',
  gatewayUrl: 'https://your-bridge/graphql',
  debug: true,
});

const container = document.getElementById('player')!;
await controller.attach(container);
```

Notes:
- There is **no default gateway**; provide `gatewayUrl` unless you pass `endpoints` or `mistUrl`.

### Direct MistServer Node (mistUrl)

```ts
const controller = new PlayerController({
  contentId: 'pk_...',
  contentType: 'live',
  mistUrl: 'https://edge.example.com',
});
```

### Styles

```ts
import '@livepeer-frameworks/player-core/player.css';
```

## Controls & Shortcuts

The player ships with keyboard/mouse shortcuts when the player is focused (click/tap once).

**Keyboard**
| Shortcut | Action | Notes |
|---|---|---|
| Space | Play/Pause | Hold = 2x speed (when seekable) |
| K | Play/Pause | YouTube-style |
| J / Left | Skip back 10s | Disabled on live-only |
| L / Right | Skip forward 10s | Disabled on live-only |
| Up / Down | Volume +/-10% | - |
| M | Mute/Unmute | - |
| F | Fullscreen toggle | - |
| C | Captions toggle | - |
| 0-9 | Seek to 0-90% | Disabled on live-only |
| , / . | Prev/Next frame (paused) | WebCodecs = true step; others = buffered-only |

**Mouse / Touch**
| Gesture | Action | Notes |
|---|---|---|
| Double-click | Fullscreen toggle | Desktop |
| Double-tap (left/right) | Skip +/-10s | Touch only, disabled on live-only |
| Click/Tap and hold | 2x speed | Disabled on live-only |

**Constraints**
- Live-only streams disable seeking/skip/2x hold and frame-step.
- Live with DVR buffer enables the same shortcuts as VOD.
- Frame stepping only moves within already buffered ranges (no network seek). WebCodecs supports true frame stepping when paused.

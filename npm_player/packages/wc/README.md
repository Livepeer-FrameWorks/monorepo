# @livepeer-frameworks/player-wc

Web Components wrapper for the FrameWorks player. Registers `<fw-player>` and composable sub-elements via Lit.

**Docs:** https://logbook.frameworks.network

## Install

```bash
pnpm add @livepeer-frameworks/player-wc
# or
npm i @livepeer-frameworks/player-wc
```

## Usage

### Auto-register elements (side-effect import)

```js
import "@livepeer-frameworks/player-wc/define";
```

```html
<fw-player
  content-id="pk_..."
  content-type="live"
  gateway-url="https://your-bridge/graphql"
  autoplay
  muted
></fw-player>
```

### Class import (no auto-registration)

```js
import { FwPlayer } from "@livepeer-frameworks/player-wc";
```

### CDN / Script Tag

```html
<script src="https://unpkg.com/@livepeer-frameworks/player-wc/dist/fw-player.iife.js"></script>

<fw-player content-id="pk_..." content-type="live"></fw-player>
```

Notes:

- There is **no default gateway**; provide `gatewayUrl` unless you pass `endpoints` or `mistUrl`.

### Direct MistServer Node (mistUrl)

```html
<fw-player
  content-id="pk_..."
  content-type="live"
  mist-url="https://edge-egress.example.com"
></fw-player>
```

## Attributes

| Attribute        | Type      | Default  | Description                             |
| ---------------- | --------- | -------- | --------------------------------------- |
| `content-id`     | `string`  | `""`     | Playback ID                             |
| `content-type`   | `string`  | —        | `"live"` · `"dvr"` · `"clip"` · `"vod"` |
| `gateway-url`    | `string`  | —        | Gateway GraphQL endpoint                |
| `mist-url`       | `string`  | —        | Direct MistServer endpoint              |
| `auth-token`     | `string`  | —        | Auth token                              |
| `autoplay`       | `boolean` | `true`   | Auto-start playback                     |
| `muted`          | `boolean` | `true`   | Start muted                             |
| `playback-mode`  | `string`  | `"auto"` | `"auto"` · `"hls"` · `"webrtc"` · etc.  |
| `stock-controls` | `boolean` | `false`  | Use native browser controls             |
| `thumbnail-url`  | `string`  | —        | Poster / thumbnail URL                  |
| `theme`          | `string`  | —        | Theme preset name                       |
| `locale`         | `string`  | `"en"`   | UI language                             |
| `debug`          | `boolean` | `false`  | Debug logging                           |
| `dev-mode`       | `boolean` | `false`  | Show dev panel                          |

## Methods

```js
const player = document.querySelector("fw-player");

player.play();
player.pause();
player.togglePlay();
player.seek(30000);
player.seekBy(-10000);
player.jumpToLive();
player.setVolume(0.5);
player.toggleMute();
player.toggleLoop();
player.toggleFullscreen();
player.togglePiP();
player.toggleSubtitles();
player.getQualities();
player.selectQuality(id);
player.retry();
player.reload();
player.destroy();
```

## Events

| Event                  | Detail                              |
| ---------------------- | ----------------------------------- |
| `fw-state-change`      | Player state updated                |
| `fw-ready`             | Player attached, `{ videoElement }` |
| `fw-stream-state`      | Stream health state changed         |
| `fw-time-update`       | `{ currentTime, duration }` (ms)    |
| `fw-volume-change`     | `{ volume, muted }`                 |
| `fw-fullscreen-change` | `{ isFullscreen }`                  |
| `fw-pip-change`        | `{ isPiP }`                         |
| `fw-error`             | `{ error }`                         |
| `fw-protocol-swapped`  | Transport protocol changed          |
| `fw-playback-failed`   | Playback failed after retries       |

## Theming

Set a preset via the `theme` attribute, or pass a CSS variable object to the `themeOverrides` JS property:

```js
player.themeOverrides = {
  "--fw-color-accent": "#ff6600",
};
```

## Sub-elements

`<fw-player>` bundles a full UI. All sub-elements are also exported for custom layouts:

`<fw-player-controls>` · `<fw-seek-bar>` · `<fw-volume-control>` · `<fw-settings-menu>` · `<fw-idle-screen>` · `<fw-loading-spinner>` · `<fw-loading-screen>` · `<fw-stream-state-overlay>` · `<fw-thumbnail-overlay>` · `<fw-title-overlay>` · `<fw-error-overlay>` · `<fw-toast>` · `<fw-stats-panel>` · `<fw-dev-mode-panel>` · `<fw-subtitle-renderer>` · `<fw-skip-indicator>` · `<fw-speed-indicator>` · `<fw-context-menu>` · `<fw-play-button>` · `<fw-skip-button>` · `<fw-time-display>` · `<fw-live-badge>` · `<fw-fullscreen-button>` · `<fw-dvd-logo>`

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

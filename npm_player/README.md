# FrameWorks Player
![NPM](https://img.shields.io/badge/npm-%40frameworks%2Fplayer-blue)
![License](https://img.shields.io/badge/license-Unlicense-lightgrey)

A player component library for **FrameWorks** with Gateway integration and intelligent protocol selection. Supports MistPlayer, DirectSource (MP4/WEBM), and WHEP (WebRTC-HTTP Egress Protocol).

The `Player` component resolves optimal endpoints via the FrameWorks Gateway, while raw components accept URIs directly for custom implementations.

## Installation

```bash
npm install --save @livepeer-frameworks/player
```

## Usage

### Basic Usage

Import the components you need:

```jsx
import { Player, MistPlayer, DirectSourcePlayer, WHEPPlayer } from '@livepeer-frameworks/player';
```

### Player Component (Recommended)

The `Player` component resolves viewing endpoints via the FrameWorks Gateway and renders the appropriate sub-player:

```jsx
import React from 'react';
import { Player } from '@livepeer-frameworks/player';

function App() {
  return (
    <div style={{ width: '100%', height: '500px' }}>
      <Player 
        contentType="live"            // 'live' | 'dvr' | 'clip'
        contentId="internal-or-hash"  // stream internal name, DVR hash, or clip hash
        options={{ gatewayUrl: 'https://your-bridge/graphql', authToken: '...optional...' }}
      />
    </div>
  );
}
```

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
import { MistPlayer } from '@livepeer-frameworks/player';

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
import { DirectSourcePlayer } from '@livepeer-frameworks/player';

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
import { WHEPPlayer } from '@livepeer-frameworks/player';

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

### MistPlayer ⭐ **Recommended for Most Use Cases**
- **Technology**: Intelligent wrapper that automatically selects optimal protocol + player combo
- **Best for**: Universal compatibility with automatic optimization
- **Pros**: Chooses best protocol (WebRTC, HLS, DASH, etc.) per device, handles adaptive bitrate, maximum compatibility, automatic fallbacks
- **Cons**: Less direct control over specific playback technology

### WHEPPlayer ⭐ **Recommended for Low Latency**
- **Technology**: HTTP signaling + WebRTC (WHEP standard)
- **Best for**: Universal compatibility with guaranteed low latency
- **Pros**: Works on all devices, standardized protocol, no WebSocket needed, just HTTP requests, ultra-low latency
- **Cons**: WebRTC limitations (CPU intensive, battery drain, network sensitivity), no adaptive bitrate

### DirectSourcePlayer  
- **Technology**: HTML5 video (direct MP4/WEBM)
- **Best for**: Small VOD files/preview clips
- **Pros**: Simple, widely supported
- **Cons**: No ABR; not for live

## How It Works

### Player Resolution (Gateway)
The `Player` component queries the Gateway GraphQL `resolveViewerEndpoint` to obtain ready-to-use endpoints:

- Uses backend-provided URLs and capabilities only; no local URL synthesis
- Live defaults to WHEP; DVR/Clip use Mist HTML; MP4/WEBM direct sources go to DirectSourcePlayer

### Raw Components (Direct URIs)
The raw `MistPlayer`, `DirectSourcePlayer`, and `WHEPPlayer` components accept URIs directly, giving you full control over which servers to use. This is useful when:

- You want to implement your own load balancing logic
- You're connecting to a specific known server
- You're building a custom player interface
- You need to bypass the Stronk Tech load balancer

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

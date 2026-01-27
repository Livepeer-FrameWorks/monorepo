# FrameWorks StreamCrafter

![NPM](https://img.shields.io/badge/npm-%40livepeer--frameworks%2Fstreamcrafter-blue)
![License](https://img.shields.io/badge/license-Unlicense-lightgrey)

A browser-based WHIP streaming library for **FrameWorks**. Stream from your camera, screen, or both directly to a MistServer WHIP endpoint with WebCodecs encoding and automatic reconnection.

The `StreamCrafter` component is self-contained - pass a WHIP endpoint and get a full streaming UI with camera/screen controls, quality selection, and live status indicators.

## Packages

| Package                                     | Description                                          |
| ------------------------------------------- | ---------------------------------------------------- |
| `@livepeer-frameworks/streamcrafter-core`   | Core streaming logic, WHIP client, WebCodecs encoder |
| `@livepeer-frameworks/streamcrafter-react`  | React component and hooks                            |
| `@livepeer-frameworks/streamcrafter-svelte` | Svelte 5 component and stores                        |

## Features

- **Self-contained UI** - Drop-in component with built-in controls
- **Multi-source streaming** - Camera + screen share simultaneously
- **Audio mixing** - Web Audio API mixer for multiple audio sources
- **Quality profiles** - Professional (1080p 8Mbps), Broadcast (1080p 4.5Mbps), Conference (720p 2.5Mbps)
- **Auto-reconnection** - Exponential backoff with configurable retries
- **Background-safe** - WebCodecs encoding continues in background tabs
- **WHIP protocol** - Standard WebRTC-HTTP Ingest Protocol

## Documentation

Full docs and guides: https://docs.frameworks.network

## Installation

```bash
npm install @livepeer-frameworks/streamcrafter-react
# or
npm install @livepeer-frameworks/streamcrafter-svelte
```

## Usage

### React Component (Recommended)

The simplest way to add streaming - just pass your WHIP endpoint:

```tsx
import { StreamCrafter } from "@livepeer-frameworks/streamcrafter-react";
import "@livepeer-frameworks/streamcrafter-react/streamcrafter.css";

function BroadcastPage() {
  return (
    <StreamCrafter
      whipUrl="https://ingest.example.com/webrtc/your-stream-key"
      initialProfile="broadcast"
      onStateChange={(state) => console.log("State:", state)}
    />
  );
}
```

The component includes:

- Video preview
- Camera/Screen share buttons
- Quality profile selector
- Go Live / Stop button
- Source list with mute/remove controls
- Connection status indicators
- Auto-reconnection on failure

### Gateway Mode (Stream Key + Gateway URL)

If you want the SDK to resolve ingest endpoints for you, pass a **Gateway GraphQL URL** and a **stream key** instead of a direct WHIP URL:

```tsx
<StreamCrafter
  gatewayUrl="https://api.example.com/graphql"
  streamKey="sk_live_..."
  initialProfile="broadcast"
/>
```

Notes:

- There is **no default gateway**; you must provide either `whipUrl` or (`gatewayUrl` + `streamKey`).
- If both are provided, `whipUrl` takes priority.

### Svelte Component

```svelte
<script lang="ts">
  import { StreamCrafter } from "@livepeer-frameworks/streamcrafter-svelte";
  import "@livepeer-frameworks/streamcrafter-svelte/streamcrafter.css";
</script>

<StreamCrafter
  whipUrl="https://ingest.example.com/webrtc/your-stream-key"
  initialProfile="broadcast"
/>
```

### Using Hooks/Stores (Advanced)

For custom UI, use the hooks or stores directly:

#### React Hook

```tsx
import { useStreamCrafterV2 } from "@livepeer-frameworks/streamcrafter-react";

function CustomBroadcaster() {
  const {
    state,
    isStreaming,
    isCapturing,
    sources,
    mediaStream,
    qualityProfile,
    startCamera,
    startScreenShare,
    startStreaming,
    stopStreaming,
    setQualityProfile,
  } = useStreamCrafterV2({
    whipUrl: "https://ingest.example.com/webrtc/stream-key",
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
      <button onClick={() => startCamera()}>Add Camera</button>
      <button onClick={() => startScreenShare()}>Share Screen</button>
      <button onClick={() => startStreaming()} disabled={!isCapturing}>
        Go Live
      </button>
    </div>
  );
}
```

#### Svelte Store

```svelte
<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { createStreamCrafterContextV2 } from "@livepeer-frameworks/streamcrafter-svelte";

  const crafter = createStreamCrafterContextV2();

  onMount(() => {
    crafter.initialize({
      whipUrl: "https://ingest.example.com/webrtc/stream-key",
      profile: "broadcast",
    });
  });

  onDestroy(() => crafter.destroy());
</script>

{#if $crafter.isStreaming}
  <span>LIVE</span>
{/if}

<video srcObject={$crafter.mediaStream} autoplay muted />

<button on:click={() => crafter.startCamera()}>Camera</button>
<button on:click={() => crafter.startStreaming()}>Go Live</button>
```

### Vanilla JS

```ts
import { StreamCrafterV2 } from "@livepeer-frameworks/streamcrafter-core";
import "@livepeer-frameworks/streamcrafter-core/streamcrafter.css";

const crafter = new StreamCrafterV2({
  whipUrl: "https://ingest.example.com/webrtc/stream-key",
  profile: "broadcast",
  debug: true,
});

await crafter.startCamera();
await crafter.startStreaming();

// Later
await crafter.stopStreaming();
crafter.destroy();
```

## Component Props

### StreamCrafter

| Prop              | Type                                                | Default       | Description                          |
| ----------------- | --------------------------------------------------- | ------------- | ------------------------------------ |
| `whipUrl`         | string                                              | -             | WHIP endpoint URL                    |
| `gatewayUrl`      | string                                              | -             | Gateway URL (alternative to whipUrl) |
| `streamKey`       | string                                              | -             | Stream key for gateway mode          |
| `initialProfile`  | `'professional'` \| `'broadcast'` \| `'conference'` | `'broadcast'` | Quality profile                      |
| `autoStartCamera` | boolean                                             | `false`       | Start camera on mount                |
| `showSettings`    | boolean                                             | `false`       | Show settings panel initially        |
| `devMode`         | boolean                                             | `false`       | Show debug info                      |
| `debug`           | boolean                                             | `false`       | Enable console logging               |
| `onStateChange`   | function                                            | -             | State change callback                |
| `onError`         | function                                            | -             | Error callback                       |

### States

The `onStateChange` callback receives one of these states:

- `idle` - Ready to capture
- `requesting_permissions` - Waiting for camera/mic access
- `capturing` - Capturing media, not streaming
- `connecting` - Establishing WHIP connection
- `streaming` - Live and streaming
- `reconnecting` - Connection lost, attempting reconnect
- `error` - Error occurred
- `destroyed` - Cleanup complete

## Quality Profiles

| Profile        | Resolution | Video Bitrate | Audio Bitrate |
| -------------- | ---------- | ------------- | ------------- |
| `professional` | 1920x1080  | 8 Mbps        | 192 kbps      |
| `broadcast`    | 1920x1080  | 4.5 Mbps      | 128 kbps      |
| `conference`   | 1280x720   | 2.5 Mbps      | 96 kbps       |

## Multi-Source Streaming

StreamCrafter supports multiple sources simultaneously:

```tsx
const { startCamera, startScreenShare, sources, removeSource } = useStreamCrafterV2({ ... });

// Add camera
await startCamera();

// Add screen share with audio
await startScreenShare({ audio: true });

// sources now contains both
// [{ id: 'camera-1', type: 'camera', ... }, { id: 'screen-1', type: 'screen', ... }]

// Remove a source
removeSource('camera-1');
```

Audio from multiple sources is automatically mixed using the Web Audio API.

## Development

### Playground

The SDK playground (shared with the Player) provides a testing environment:

```bash
cd npm_studio
pnpm install
pnpm run dev
```

This starts the playground at `http://localhost:5173` where you can:

- Configure WHIP endpoints
- Test camera/screen capture
- Stream to local MistServer

### Building

```bash
pnpm run build
```

Builds all three packages (core, react, svelte) with TypeScript declarations.

### Engineering docs (for contributors)

- See `docs/architecture/streamcrafter.md` for internal architecture, worker/bundling details, and where to start when modifying the SDK.

## Architecture

```
Browser                                Worker
───────                                ──────
getUserMedia()
     │
     ▼
MediaStream ──► <video> (preview)
     │
     ▼
MediaStreamTrackProcessor
     │
     ▼
VideoFrame ───────postMessage────────► VideoEncoder
                                            │
                                            ▼
                                    MediaStreamTrackGenerator
                                            │
                                            ▼
                                    RTCPeerConnection
                                            │
                                            ▼
                                       WHIP Endpoint
```

**Why this works in background tabs:**

1. MediaStreamTrackProcessor is media-pipeline driven (not requestAnimationFrame)
2. Web Workers aren't throttled like main thread timers
3. WebRTC connection stays active

## WHIP Protocol

StreamCrafter implements [WebRTC-HTTP Ingest Protocol (WHIP)](https://datatracker.ietf.org/doc/draft-ietf-wish-whip/):

1. Creates RTCPeerConnection
2. POSTs SDP offer to WHIP endpoint
3. Receives SDP answer in response
4. Establishes WebRTC connection
5. DELETE to WHIP resource URL on stop

## Browser Support

Requires:

- WebRTC (RTCPeerConnection)
- MediaDevices API (getUserMedia, getDisplayMedia)
- WebCodecs API (optional, for background-safe encoding)

Tested on:

- Chrome 90+
- Firefox 90+
- Safari 14.1+
- Edge 90+

## License

Unlicense

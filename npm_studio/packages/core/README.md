# @livepeer-frameworks/streamcrafter-core

Framework-agnostic core for StreamCrafter: WHIP client, WebRTC transport, WebCodecs encoder, audio mixing, and compositor.

## Documentation

Docs: https://docs.frameworks.network

## Install

```bash
npm install @livepeer-frameworks/streamcrafter-core
```

## Usage (Vanilla)

```ts
import { StreamCrafterV2 } from '@livepeer-frameworks/streamcrafter-core';
import '@livepeer-frameworks/streamcrafter-core/streamcrafter.css';

const crafter = new StreamCrafterV2({
  whipUrl: 'https://ingest.example.com/webrtc/your-stream-key',
  profile: 'broadcast',
});

await crafter.startCamera();
await crafter.startStreaming();

// Later
await crafter.stopStreaming();
crafter.destroy();
```

### Gateway Resolution (Advanced)

The vanilla `StreamCrafterV2` constructor requires a **direct WHIP URL**. If you want gateway resolution, use the `IngestClient` (or the React/Svelte wrappers).

```ts
import { IngestClient, StreamCrafterV2 } from '@livepeer-frameworks/streamcrafter-core';

const ingest = new IngestClient({
  gatewayUrl: 'https://api.example.com/graphql',
  streamKey: 'sk_live_...',
});

const endpoints = await ingest.resolve();
const whipUrl = endpoints?.primary?.whipUrl;

if (!whipUrl) throw new Error('No WHIP URL resolved');

const crafter = new StreamCrafterV2({ whipUrl, profile: 'broadcast' });
```

Notes:
- There is **no default gateway**; you must supply `whipUrl` or resolve it yourself.

## Notes

- WebCodecs + Web Workers are used when available for background-safe encoding.
- For custom UIs, build on the core APIs or use the React/Svelte wrappers.
- CSS export: `@livepeer-frameworks/streamcrafter-core/streamcrafter.css`

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
  whipUrl: 'https://ingest.example.com/whip/your-stream-key',
  profile: 'broadcast',
});

await crafter.startCamera();
await crafter.startStreaming();

// Later
await crafter.stopStreaming();
crafter.destroy();
```

## Notes

- WebCodecs + Web Workers are used when available for background-safe encoding.
- For custom UIs, build on the core APIs or use the React/Svelte wrappers.
- CSS export: `@livepeer-frameworks/streamcrafter-core/streamcrafter.css`

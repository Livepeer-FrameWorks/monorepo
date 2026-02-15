# @livepeer-frameworks/streamcrafter-core

Headless WHIP streaming engine for FrameWorks. Provides `IngestControllerV2`, `WhipClient`, WebCodecs encoder, audio mixing, compositor, and CSS.

> **Most users should install a wrapper instead of core directly:**
>
> | Package                                                                                                                | Use case                                                      |
> | ---------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------- |
> | [`@livepeer-frameworks/streamcrafter-react`](https://www.npmjs.com/package/@livepeer-frameworks/streamcrafter-react)   | React apps                                                    |
> | [`@livepeer-frameworks/streamcrafter-svelte`](https://www.npmjs.com/package/@livepeer-frameworks/streamcrafter-svelte) | Svelte 5 apps                                                 |
> | [`@livepeer-frameworks/streamcrafter-wc`](https://www.npmjs.com/package/@livepeer-frameworks/streamcrafter-wc)         | Web Components — Vue, Angular, CDN `<script>` tag, plain HTML |
>
> The wrappers include core as a dependency and provide a full UI (preview, camera/screen controls, quality selector, connection status, etc.). Install core directly only if you need **headless programmatic control** with a completely custom UI.

**Docs:** https://logbook.frameworks.network

## Install

```bash
npm install @livepeer-frameworks/streamcrafter-core
```

## Headless Usage

```ts
import { StreamCrafterV2 } from "@livepeer-frameworks/streamcrafter-core";
import "@livepeer-frameworks/streamcrafter-core/streamcrafter.css";

const crafter = new StreamCrafterV2({
  whipUrl: "https://edge-ingest.example.com/webrtc/your-stream-key",
  profile: "broadcast",
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
import { IngestClient, StreamCrafterV2 } from "@livepeer-frameworks/streamcrafter-core";

const ingest = new IngestClient({
  gatewayUrl: "https://bridge.example.com/graphql",
  streamKey: "sk_live_...",
});

const endpoints = await ingest.resolve();
const whipUrl = endpoints?.primary?.whipUrl;

if (!whipUrl) throw new Error("No WHIP URL resolved");

const crafter = new StreamCrafterV2({ whipUrl, profile: "broadcast" });
```

Notes:

- There is **no default gateway**; you must supply `whipUrl` or resolve it yourself.

## Notes

- WebCodecs + Web Workers are used when available for background-safe encoding.
- For custom UIs, build on the core APIs or use the React/Svelte/Web Component wrappers.
- CSS export: `@livepeer-frameworks/streamcrafter-core/streamcrafter.css`

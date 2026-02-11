# @livepeer-frameworks/streamcrafter-svelte

Svelte 5 wrapper for StreamCrafter with a ready-to-use component and stores for custom UIs.

## Documentation

Docs: https://logbook.frameworks.network

## Install

```bash
npm install @livepeer-frameworks/streamcrafter-svelte
```

## Usage (Component)

```svelte
<script lang="ts">
  import { StreamCrafter } from "@livepeer-frameworks/streamcrafter-svelte";
  import "@livepeer-frameworks/streamcrafter-svelte/streamcrafter.css";
</script>

<StreamCrafter
  whipUrl="https://edge-ingest.example.com/webrtc/your-stream-key"
  initialProfile="broadcast"
/>
```

### Gateway Mode (Stream Key + Gateway URL)

```svelte
<StreamCrafter
  gatewayUrl="https://bridge.example.com/graphql"
  streamKey="sk_live_..."
  initialProfile="broadcast"
/>
```

Notes:

- There is **no default gateway**; pass either `whipUrl` or (`gatewayUrl` + `streamKey`).
- If both are provided, `whipUrl` takes priority.

## Usage (Stores)

```svelte
<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { createStreamCrafterContextV2 } from "@livepeer-frameworks/streamcrafter-svelte";

  const crafter = createStreamCrafterContextV2();

  onMount(() => {
    crafter.initialize({
      whipUrl: "https://edge-ingest.example.com/webrtc/your-stream-key",
      profile: "broadcast",
    });
  });

  onDestroy(() => crafter.destroy());
</script>

<video srcObject={$crafter.mediaStream} autoplay muted />
<button on:click={() => crafter.startCamera()}>Camera</button>
<button on:click={() => crafter.startStreaming()}>Go Live</button>
```

## Notes

- CSS export: `@livepeer-frameworks/streamcrafter-svelte/streamcrafter.css`
- Peer dep: `svelte` (v5)

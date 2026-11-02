# @livepeer-frameworks/streamcrafter-svelte

Svelte 5 wrapper for StreamCrafter with a ready-to-use component and stores for custom UIs.

> **Beta.** StreamCrafter is on 0.x and its API will change a lot while cloud production and guests are built. Expect breaking changes in minor releases: pin an exact version and read the changelog before you upgrade.

The drop-in `<StreamCrafter>` includes the producer panel (scenes, transitions, layers, logo and text overlays, media files) whenever the compositor runs; turn it off with `showProducerPanel={false}`. It also has Add Media File in the action bar, an Audio section (noise gate, de-esser, EQ, ducking) and a connection health pill while live. Composable children read the compositor from context, so `<StudioProducerPanel />` and `<StudioAudioControls />` need no props.

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

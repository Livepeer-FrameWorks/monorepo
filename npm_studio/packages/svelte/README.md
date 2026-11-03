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

### Keeping the layout per stream

`onLayoutChange(layout, layoutKey)` reports the scenes, overlays and layout preset as JSON, with the `layoutKey` they belong to; give the layout back as `initialLayout`. Changing `enableCompositor` or `compositorConfig.renderer` takes effect without a remount, so no `{#key}` block is needed.

`layoutKey` names the stream the layout belongs to. To switch streams, change `whipUrl`, `layoutKey` and `initialLayout` together. The running studio keeps its camera, screen and media sources, shows the new stream's `initialLayout` (or the default layout when it is `null`), and sends the next broadcast to the new `whipUrl`. A broadcast that is live when `whipUrl` changes is stopped. A report exported for the previous key is dropped, so save each layout under the key it comes with. For the same `layoutKey`, the studio keeps its current layout, also when it builds a new controller for a new `initialProfile` or `debug`.

```svelte
<script lang="ts">
  function readLayout(streamId: string): StudioLayout | null {
    try {
      return JSON.parse(localStorage.getItem(`layout:${streamId}`) ?? "null");
    } catch {
      return null;
    }
  }

  function saveLayout(streamId: string, layout: StudioLayout) {
    try {
      localStorage.setItem(`layout:${streamId}`, JSON.stringify(layout));
    } catch {
      // Storage is blocked, or full: a picked image is carried as a data URL.
    }
  }
</script>

<StreamCrafter
  {whipUrl}
  layoutKey={streamId}
  initialLayout={readLayout(streamId)}
  onLayoutChange={(layout, key) => key && saveLayout(key, layout)}
/>
```

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

# @livepeer-frameworks/streamcrafter-svelte

Svelte 5 wrapper for StreamCrafter with a ready-to-use component and stores for custom UIs.

## Documentation

Docs: https://docs.frameworks.network

## Install

```bash
npm install @livepeer-frameworks/streamcrafter-svelte
```

## Usage (Component)

```svelte
<script lang="ts">
  import { StreamCrafter } from '@livepeer-frameworks/streamcrafter-svelte';
  import '@livepeer-frameworks/streamcrafter-svelte/streamcrafter.css';
</script>

<StreamCrafter
  whipUrl="https://ingest.example.com/whip/your-stream-key"
  initialProfile="broadcast"
/>
```

## Usage (Stores)

```svelte
<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { createStreamCrafterContextV2 } from '@livepeer-frameworks/streamcrafter-svelte';

  const crafter = createStreamCrafterContextV2();

  onMount(() => {
    crafter.initialize({
      whipUrl: 'https://ingest.example.com/whip/your-stream-key',
      profile: 'broadcast',
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

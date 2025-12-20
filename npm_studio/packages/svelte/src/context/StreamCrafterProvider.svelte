<script lang="ts">
  import { setContext } from 'svelte';
  import { createStreamCrafterContextV2 as createStreamCrafterContext, type StreamCrafterContextV2Store as StreamCrafterContextStore } from '../stores/streamCrafterContextV2';

  // Define prop types (similar to React's StreamCrafterProviderProps)
  let { children, ...options }: {
    children?: any;
  } = $props();

  const streamCrafterContext = createStreamCrafterContext();
  if (Object.keys(options).length > 0) {
    streamCrafterContext.initialize(options as any);
  }

  // Set the context for descendant components
  setContext('StreamCrafterContext', streamCrafterContext);
</script>

<slot />

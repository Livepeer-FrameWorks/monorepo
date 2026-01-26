<script lang="ts">
  import { setContext, type Snippet } from 'svelte';
  import { createStreamCrafterContextV2 as createStreamCrafterContext } from '../stores/streamCrafterContextV2';

  // Define prop types (similar to React's StreamCrafterProviderProps)
  let { children, ...options }: {
    children?: Snippet;
  } = $props();

  const streamCrafterContext = createStreamCrafterContext();
  if (Object.keys(options).length > 0) {
    streamCrafterContext.initialize(options as any);
  }

  // Set the context for descendant components
  setContext('StreamCrafterContext', streamCrafterContext);
</script>

{@render children?.()}

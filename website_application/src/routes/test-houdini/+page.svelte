<script lang="ts">
  import { GetStreamsStore } from '$houdini';

  const store = new GetStreamsStore();

  // Fetch on mount
  $effect(() => {
    store.fetch();
  });
</script>

<div class="p-8">
  <h1 class="text-2xl font-bold mb-4">Houdini Test Page</h1>

  {#if $store.fetching}
    <p class="text-muted-foreground">Loading streams...</p>
  {:else if $store.errors}
    <div class="text-destructive">
      <p>Error:</p>
      <pre>{JSON.stringify($store.errors, null, 2)}</pre>
    </div>
  {:else if $store.data?.streams}
    <div class="space-y-2">
      <p class="text-sm text-muted-foreground">
        Found {$store.data.streams.length} streams
      </p>
      {#each $store.data.streams as stream}
        <div class="border rounded p-4">
          <p class="font-medium">{stream.name}</p>
          <p class="text-sm text-muted-foreground">{stream.description || 'No description'}</p>
          <p class="text-xs mt-2">
            Status: {stream.metrics?.status ?? 'unknown'} |
            Live: {stream.metrics?.isLive ? 'Yes' : 'No'} |
            Viewers: {stream.metrics?.currentViewers ?? 0}
          </p>
        </div>
      {/each}
    </div>
  {:else}
    <p class="text-muted-foreground">No streams found</p>
  {/if}
</div>

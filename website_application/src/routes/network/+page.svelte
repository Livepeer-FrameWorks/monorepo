<script lang="ts">
  import { page } from "$app/state";
  import { goto, replaceState } from "$app/navigation";
  import { resolve } from "$app/paths";
  import NetworkObservations from "$lib/components/NetworkObservations.svelte";
  import NetworkTopology from "$lib/components/NetworkTopology.svelte";
  import { auth } from "$lib/stores/auth";
  import {
    networkTabKey,
    networkView,
    networkViewURL,
    type NetworkView,
    type ExplorerViewport,
  } from "../../../../packages/map-core/network-explorer";
  let diagnostics = $state(false);
  const viewport: ExplorerViewport = {};
  let view = $derived(networkView(page.url.searchParams));
  function choose(next: NetworkView) {
    void goto(resolve(networkViewURL(page.url.searchParams, next)), {
      noScroll: true,
      keepFocus: true,
    });
  }
  function onTabKey(event: KeyboardEvent) {
    const next = networkTabKey(view, event.key);
    if (!next) return;
    event.preventDefault();
    choose(next);
    document.getElementById(`network-tab-${next}`)?.focus();
  }
  function onQueryChange(query: URLSearchParams) {
    replaceState(resolve(`/network?${query}`), page.state);
  }
</script>

<svelte:head><title>Network explorer | FrameWorks</title></svelte:head>
<div class="p-4 md:p-6">
  <h1 class="text-2xl font-semibold mb-3">Network explorer</h1>
  <p class="text-muted-foreground mb-6">
    Explore our infrastructure and the Livepeer compute network.
  </p>
  <div role="tablist" aria-label="Network view" class="flex border border-border mb-6">
    {#each ["frameworks", "livepeer"] as key (key)}
      <button
        type="button"
        role="tab"
        id="network-tab-{key}"
        aria-selected={view === key}
        aria-controls="network-panel"
        tabindex={view === key ? 0 : -1}
        onkeydown={onTabKey}
        onclick={() => choose(key as NetworkView)}
        class="flex-1 px-4 py-3 border-r border-border last:border-r-0 {view === key
          ? 'bg-accent text-foreground'
          : 'text-muted-foreground'}">{key === "frameworks" ? "FrameWorks" : "Livepeer"}</button
      >
    {/each}
  </div>
  <div role="tabpanel" id="network-panel" aria-labelledby="network-tab-{view}" tabindex="0">
    {#if view === "frameworks"}
      <NetworkTopology {viewport} />
    {:else}
      {#if $auth.user?.platform_operator}
        <label class="flex items-center gap-2 mb-4"
          ><input type="checkbox" bind:checked={diagnostics} />Operator diagnostics</label
        >
      {/if}
      {#key diagnostics}<NetworkObservations {diagnostics} {viewport} {onQueryChange} />{/key}
    {/if}
  </div>
</div>

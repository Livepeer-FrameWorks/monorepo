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
  <header class="explorer-heading">
    <h1>Network explorer</h1>
    <p>Infrastructure, compute and the paths between them.</p>
  </header>
  <div role="tablist" aria-label="Network view" class="explorer-tabs">
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
        class="explorer-tab">{key === "frameworks" ? "FrameWorks" : "Livepeer"}</button
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

<style>
  .explorer-heading {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 1rem;
    padding-bottom: 1.25rem;
  }
  .explorer-heading h1 {
    font-size: 1.5rem;
    font-weight: 600;
  }
  .explorer-heading p {
    color: hsl(var(--muted-foreground));
    font-size: 0.8125rem;
  }
  .explorer-tabs {
    display: flex;
    border: 1px solid hsl(var(--border));
    border-bottom: 0;
    background: hsl(var(--brand-surface-strong));
  }
  .explorer-tab {
    padding: 0.875rem 1.5rem;
    border-right: 1px solid hsl(var(--border));
    color: hsl(var(--muted-foreground));
    font-size: 0.8125rem;
  }
  .explorer-tab[aria-selected="true"] {
    color: hsl(var(--brand-cyan));
    background: hsl(var(--brand-surface));
    box-shadow: inset 0 -2px hsl(var(--brand-cyan));
  }
  .explorer-tab:focus-visible {
    outline: 2px solid hsl(var(--brand-cyan));
    outline-offset: -3px;
  }
  @media (max-width: 640px) {
    .explorer-heading {
      display: block;
    }
    .explorer-heading p {
      margin-top: 0.375rem;
    }
    .explorer-tab {
      flex: 1;
    }
  }
</style>

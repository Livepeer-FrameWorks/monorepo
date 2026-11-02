<script lang="ts">
  import { page } from "$app/state";
  import { goto, replaceState } from "$app/navigation";
  import { resolve } from "$app/paths";
  import NetworkObservations from "$lib/components/NetworkObservations.svelte";
  import NetworkTopology from "$lib/components/NetworkTopology.svelte";
  import { auth } from "$lib/stores/auth";
  import { getDocsSiteUrl, getMarketingSiteUrl } from "$lib/config";
  import {
    networkExplorerActions,
    networkTabKey,
    networkView,
    networkViewURL,
    type NetworkView,
    type ExplorerViewport,
  } from "../../../../packages/map-core/network-explorer";
  import "../../../../packages/map-core/network-explorer.css";
  let diagnostics = $state(false);
  const viewport: ExplorerViewport = {};
  const marketing = getMarketingSiteUrl().replace(/\/+$/, "");
  const actions = networkExplorerActions({
    startStreaming: resolve("/go-live"),
    docsURL: getDocsSiteUrl(),
    showcase: marketing ? `${marketing}/showcase` : "",
  });
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
  <header class="fw-explorer-heading">
    <h1>Network explorer</h1>
    <p>Infrastructure, compute and the paths between them.</p>
  </header>
  <div role="tablist" aria-label="Network view" class="fw-explorer-tabs">
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
        class="fw-explorer-tab">{key === "frameworks" ? "FrameWorks" : "Livepeer"}</button
      >
    {/each}
  </div>
  <div role="tabpanel" id="network-panel" aria-labelledby="network-tab-{view}" tabindex="0">
    {#if view === "frameworks"}
      <div class="explorer-slab"><NetworkTopology {viewport} /></div>
    {:else}
      {#key diagnostics}<NetworkObservations
          {diagnostics}
          operator={Boolean($auth.user?.platform_operator)}
          onDiagnosticsChange={(enabled) => (diagnostics = enabled)}
          {viewport}
          {onQueryChange}
        />{/key}
    {/if}
  </div>
  <nav aria-label="Next steps" class="fw-explorer-actions">
    {#each actions as action (action.id)}
      <!-- eslint-disable-next-line svelte/no-navigation-without-resolve -- external docs and marketing URLs; the in-app link is resolved above -->
      <a href={action.href} class="fw-explorer-action">{action.label}</a>
    {/each}
  </nav>
</div>

<style>
  /* The slab draws the panel's one border, as .fw-network does on the Livepeer tab; the topology's text rows sit
     flush inside it as seamed rows and its map frame loses its own border. */
  .explorer-slab {
    border: 1px solid hsl(var(--border));
    background: hsl(var(--brand-surface-strong));
  }
  .explorer-slab > :global(p) {
    margin: 0;
    padding: 0.75rem 1rem;
    border-bottom: 1px solid hsl(var(--border));
    font-size: 0.8125rem;
  }
  .explorer-slab > :global(div) {
    border-width: 0;
  }
</style>

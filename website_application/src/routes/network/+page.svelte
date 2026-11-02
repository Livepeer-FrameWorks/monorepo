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
  <nav aria-label="Next steps" class="explorer-actions">
    {#each actions as action (action.id)}
      <!-- eslint-disable-next-line svelte/no-navigation-without-resolve -- external docs and marketing URLs; the in-app link is resolved above -->
      <a href={action.href} class="explorer-action">{action.label}</a>
    {/each}
  </nav>
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
  .explorer-slab {
    padding: 1rem;
    border: 1px solid hsl(var(--border));
    background: hsl(var(--brand-surface));
  }
  .explorer-actions {
    display: flex;
    flex-wrap: wrap;
    border: 1px solid hsl(var(--border));
    border-top: 0;
    background: hsl(var(--brand-surface-strong));
  }
  .explorer-action {
    padding: 0.875rem 1.5rem;
    border-right: 1px solid hsl(var(--border));
    color: hsl(var(--brand-cyan));
    font-size: 0.8125rem;
  }
  .explorer-action:first-child {
    color: hsl(var(--background));
    background: hsl(var(--brand-cyan));
  }
  .explorer-action:hover,
  .explorer-action:focus-visible {
    text-decoration: underline;
  }
  .explorer-action:focus-visible {
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
    .explorer-action {
      flex: 1 1 50%;
      border-bottom: 1px solid hsl(var(--border));
    }
  }
</style>

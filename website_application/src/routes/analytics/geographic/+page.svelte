<script>
  import { onMount } from "svelte";
  import { base } from "$app/paths";
  import { auth } from "$lib/stores/auth";
  import { infrastructureService } from "$lib/graphql/services/infrastructure.js";

  let isAuthenticated = false;
  let loading = true;
  let nodes = [];
  let error = null;

  // Subscribe to auth store
  auth.subscribe((authState) => {
    isAuthenticated = authState.isAuthenticated;
  });

  onMount(async () => {
    if (!isAuthenticated) {
      await auth.checkAuth();
    }
    await loadNodeData();
    loading = false;
  });

  async function loadNodeData() {
    try {
      nodes = await infrastructureService.getNodes();
    } catch (err) {
      error = err.message;
      console.error("Failed to load node data:", err);
    }
  }

</script>

<svelte:head>
  <title>Regional Analytics - FrameWorks</title>
</svelte:head>

<div class="space-y-8 page-transition">
  <!-- Page Header -->
  <div class="flex justify-between items-start">
    <div>
      <h1 class="text-3xl font-bold text-tokyo-night-fg mb-2">
        🌍 Regional Infrastructure
      </h1>
      <p class="text-tokyo-night-fg-dark">
        View your infrastructure nodes by region
      </p>
    </div>
  </div>

  {#if loading}
    <div class="flex items-center justify-center min-h-64">
      <div class="loading-spinner w-8 h-8" />
    </div>
  {:else if error}
    <div class="card border-tokyo-night-red/30">
      <div class="text-center py-12">
        <div class="text-6xl mb-4">❌</div>
        <h3 class="text-xl font-semibold text-tokyo-night-red mb-2">
          Failed to Load Node Data
        </h3>
        <p class="text-tokyo-night-fg-dark mb-6">{error}</p>
        <button class="btn-primary" on:click={loadNodeData}>
          Try Again
        </button>
      </div>
    </div>
  {:else}
    <!-- Regional Node Distribution -->
    <div class="glow-card p-6">
      <h2 class="text-xl font-semibold text-tokyo-night-fg mb-6">
        Node Distribution by Region
      </h2>
      
      {#if nodes.length > 0}
        <div class="grid md:grid-cols-2 lg:grid-cols-3 gap-4">
          {#each nodes as node}
            <div class="bg-tokyo-night-bg-highlight p-4 rounded-lg border border-tokyo-night-fg-gutter">
              <div class="flex items-center justify-between mb-3">
                <h3 class="font-semibold text-tokyo-night-fg">{node.name}</h3>
                <span class="text-sm px-2 py-1 rounded-full {node.status === 'HEALTHY' ? 'bg-green-500/20 text-green-500' : 'bg-red-500/20 text-red-500'}">
                  {node.status}
                </span>
              </div>
              
              <div class="space-y-2 text-sm">
                <div class="flex justify-between">
                  <span class="text-tokyo-night-comment">Region:</span>
                  <span class="text-tokyo-night-fg">{node.region}</span>
                </div>
                <div class="flex justify-between">
                  <span class="text-tokyo-night-comment">Type:</span>
                  <span class="text-tokyo-night-fg">{node.type}</span>
                </div>
                {#if node.ipAddress}
                  <div class="flex justify-between">
                    <span class="text-tokyo-night-comment">IP:</span>
                    <span class="text-tokyo-night-fg font-mono text-xs">{node.ipAddress}</span>
                  </div>
                {/if}
                <div class="flex justify-between">
                  <span class="text-tokyo-night-comment">Last Seen:</span>
                  <span class="text-tokyo-night-fg text-xs">{new Date(node.lastSeen).toLocaleString()}</span>
                </div>
              </div>
            </div>
          {/each}
        </div>
      {:else}
        <div class="text-center py-12">
          <div class="text-6xl mb-4">🖥️</div>
          <h3 class="text-xl font-semibold text-tokyo-night-fg mb-2">
            No Infrastructure Nodes
          </h3>
          <p class="text-tokyo-night-fg-dark mb-6">
            Configure infrastructure nodes to see regional distribution
          </p>
          <a href="{base}/infrastructure" class="btn-primary">
            Configure Infrastructure
          </a>
        </div>
      {/if}
    </div>
  {/if}
</div> 
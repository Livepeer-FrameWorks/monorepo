<script>
  import { onMount, onDestroy } from "svelte";
  import { base } from "$app/paths";
  import { auth } from "$lib/stores/auth";
  import { streamsService } from "$lib/graphql/services/streams.js";
  import { analyticsService } from "$lib/graphql/services/analytics.js";
  import { infrastructureService } from "$lib/graphql/services/infrastructure.js";
  import { subscribeToSystemHealth } from "$lib/stores/realtime.js";
  import { toast } from "$lib/stores/toast.js";
  import { getIconComponent } from "$lib/iconUtils.js";

  let isAuthenticated = false;
  /** @type {any} */
  let user = null;
  let loading = true;
  /** @type {ReturnType<typeof setInterval> | null} */
  let refreshInterval = null;
  let unsubscribeSystemHealth = null;

  // Real streams data
  /** @type {any[]} */
  let streams = [];
  
  // Real-time metrics aggregated from actual streams
  let liveMetrics = {
    totalViewers: 0,
    activeStreams: 0,
    totalBandwidthIn: 0,
    totalBandwidthOut: 0,
    avgLatency: "N/A",
    lastUpdated: new Date()
  };

  // Real viewer activity from actual stream metrics (last 20 data points)
  /** @type {Array<{time: Date, viewers: number}>} */
  let viewerActivity = [];

  // Real node data from infrastructure service
  /** @type {Array<any>} */
  let nodeData = [];

  // Subscribe to auth store
  auth.subscribe((/** @type {any} */ authState) => {
    isAuthenticated = authState.isAuthenticated;
    user = authState.user?.user || null;
    streams = authState.user?.streams || [];
  });

  onMount(async () => {
    if (!isAuthenticated) {
      await auth.checkAuth();
    }
    await loadRealStreams();
    await loadNodeData();
    await updateRealTimeMetrics();
    startRealTimeUpdates();
    // Subscribe to system health for self-hosted infrastructure monitoring
    unsubscribeSystemHealth = subscribeToSystemHealth();
    loading = false;
  });

  onDestroy(() => {
    if (refreshInterval) {
      clearInterval(refreshInterval);
    }
    if (unsubscribeSystemHealth) {
      unsubscribeSystemHealth();
    }
  });

  async function loadRealStreams() {
    try {
      streams = await streamsService.getStreams();
    } catch (error) {
      console.error("Failed to load streams:", error);
      toast.error("Failed to load streams data. Some metrics may be unavailable.");
    }
  }

  async function updateRealTimeMetrics() {
    try {
      // Get platform overview which includes real-time data
      const platformData = await analyticsService.getPlatformOverview();
      
      // Update aggregated metrics
      liveMetrics = {
        totalViewers: platformData.totalViewers || 0,
        activeStreams: platformData.activeStreams || 0,
        totalBandwidthIn: platformData.totalBandwidth || 0,
        totalBandwidthOut: platformData.totalBandwidth || 0,
        avgLatency: "N/A", // Latency tracking not implemented
        lastUpdated: new Date()
      };

      // Update viewer activity history (sliding window)
      viewerActivity = [
        ...viewerActivity.slice(-19),
        {
          time: new Date(),
          viewers: liveMetrics.totalViewers
        }
      ];

    } catch (error) {
      console.error("Failed to update realtime metrics:", error);
      toast.warning("Failed to update real-time metrics. Data may be outdated.");
    }
  }


  async function loadNodeData() {
    try {
      nodeData = await infrastructureService.getNodes();
      console.log("Node data loaded:", nodeData);
    } catch (error) {
      console.error("Error loading node data:", error);
      toast.warning("Failed to load infrastructure data. Node information may be unavailable.");
      nodeData = [];
    }
  }

  function startRealTimeUpdates() {
    refreshInterval = setInterval(async () => {
      await updateRealTimeMetrics();
    }, 5000); // Update every 5 seconds
  }

  /**
   * @param {number} bytes
   */
  function formatBandwidth(bytes) {
    if (!bytes) return "0 Kbps";
    const kbps = Math.round((bytes / 1024) * 8);
    if (kbps >= 1000000) {
      return `${(kbps / 1000000).toFixed(1)} Gbps`;
    } else if (kbps >= 1000) {
      return `${(kbps / 1000).toFixed(1)} Mbps`;
    }
    return `${kbps} Kbps`;
  }

  // Calculate max viewer count for chart scaling
  $: maxViewers = viewerActivity.length > 0 ? Math.max(...viewerActivity.map(point => point.viewers)) : 1;
</script>

<svelte:head>
  <title>Real-time Analytics - FrameWorks</title>
</svelte:head>

<div class="space-y-8 page-transition">
  <!-- Page Header -->
  <div class="flex justify-between items-start">
    <div>
      <div class="flex items-center space-x-3 mb-2">
        <svelte:component this={getIconComponent('Zap')} class="w-8 h-8 text-tokyo-night-fg" />
        <h1 class="text-3xl font-bold text-tokyo-night-fg">Real-time Analytics</h1>
      </div>
      <p class="text-tokyo-night-fg-dark">
        Live streaming metrics from your actual MistServer infrastructure
      </p>
    </div>

    <div class="flex items-center space-x-3">
      <div class="flex items-center space-x-2">
        <div class="w-2 h-2 bg-tokyo-night-green rounded-full animate-pulse"></div>
        <span class="text-sm text-tokyo-night-comment">
          Live • Updated {liveMetrics.lastUpdated.toLocaleTimeString()}
        </span>
      </div>
      <button class="btn-secondary" on:click={updateRealTimeMetrics}>
        <svelte:component this={getIconComponent('RefreshCw')} class="w-4 h-4 mr-2" />
        Refresh
      </button>
    </div>
  </div>

  {#if loading}
    <div class="flex items-center justify-center min-h-64">
      <div class="loading-spinner w-8 h-8" />
    </div>
  {:else}
    <!-- Real Live Metrics Cards -->
    <div class="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-5 gap-6">
      <div class="glow-card p-6 relative overflow-hidden">
        <div class="flex items-center justify-between">
          <div>
            <p class="text-sm text-tokyo-night-comment">Total Viewers</p>
            <p class="text-2xl font-bold text-tokyo-night-blue">
              {liveMetrics.totalViewers}
            </p>
          </div>
          <svelte:component this={getIconComponent('Users')} class="w-6 h-6 text-tokyo-night-blue" />
        </div>
        <div class="absolute bottom-0 left-0 right-0 h-1 bg-tokyo-night-blue opacity-20 animate-pulse"></div>
      </div>

      <div class="glow-card p-6 relative overflow-hidden">
        <div class="flex items-center justify-between">
          <div>
            <p class="text-sm text-tokyo-night-comment">Active Streams</p>
            <p class="text-2xl font-bold text-tokyo-night-green">
              {liveMetrics.activeStreams}
            </p>
            <p class="text-xs text-tokyo-night-comment">of {streams.length} total</p>
          </div>
          <svelte:component this={getIconComponent('Video')} class="w-6 h-6 text-tokyo-night-green" />
        </div>
        <div class="absolute bottom-0 left-0 right-0 h-1 bg-tokyo-night-green opacity-20 animate-pulse"></div>
      </div>

      <div class="glow-card p-6 relative overflow-hidden">
        <div class="flex items-center justify-between">
          <div>
            <p class="text-sm text-tokyo-night-comment">Bandwidth In</p>
            <p class="text-2xl font-bold text-tokyo-night-cyan">
              {formatBandwidth(liveMetrics.totalBandwidthIn)}
            </p>
          </div>
          <svelte:component this={getIconComponent('Download')} class="w-6 h-6 text-tokyo-night-cyan" />
        </div>
        <div class="absolute bottom-0 left-0 right-0 h-1 bg-tokyo-night-cyan opacity-20 animate-pulse"></div>
      </div>

      <div class="glow-card p-6 relative overflow-hidden">
        <div class="flex items-center justify-between">
          <div>
            <p class="text-sm text-tokyo-night-comment">Bandwidth Out</p>
            <p class="text-2xl font-bold text-tokyo-night-yellow">
              {formatBandwidth(liveMetrics.totalBandwidthOut)}
            </p>
          </div>
          <svelte:component this={getIconComponent('Upload')} class="w-6 h-6 text-tokyo-night-yellow" />
        </div>
        <div class="absolute bottom-0 left-0 right-0 h-1 bg-tokyo-night-yellow opacity-20 animate-pulse"></div>
      </div>

      <div class="glow-card p-6 relative overflow-hidden">
        <div class="flex items-center justify-between">
          <div>
            <p class="text-sm text-tokyo-night-comment">Avg Latency</p>
            <p class="text-2xl font-bold text-tokyo-night-purple">
              {liveMetrics.avgLatency}
            </p>
          </div>
          <svelte:component this={getIconComponent('Zap')} class="w-6 h-6 text-tokyo-night-purple" />
        </div>
        <div class="absolute bottom-0 left-0 right-0 h-1 bg-tokyo-night-purple opacity-20 animate-pulse"></div>
      </div>
    </div>

    <!-- Real-time Charts -->
    <div class="grid grid-cols-1 xl:grid-cols-3 gap-8">
      <!-- Live Viewer Activity Chart -->
      <div class="xl:col-span-2 card">
        <div class="card-header">
          <div class="flex items-center space-x-2 mb-2">
            <svelte:component this={getIconComponent('TrendingUp')} class="w-5 h-5 text-tokyo-night-fg" />
            <h2 class="text-xl font-semibold text-tokyo-night-fg">Live Viewer Activity</h2>
          </div>
          <p class="text-tokyo-night-fg-dark">
            Real-time viewer count aggregated from all active streams
          </p>
        </div>

        <div class="space-y-4">
          {#if viewerActivity.length > 0}
            <!-- Real Chart with actual data -->
            <div class="bg-tokyo-night-bg-highlight p-4 rounded-lg">
              <div class="flex items-end space-x-1 h-40">
                {#each viewerActivity as point, i}
                  <div 
                    class="bg-tokyo-night-blue flex-1 rounded-t transition-all duration-500 relative group"
                    style="height: {maxViewers > 0 ? (point.viewers / maxViewers) * 100 : 0}%"
                    title="{point.time.toLocaleTimeString()}: {point.viewers} viewers"
                  >
                    {#if i === viewerActivity.length - 1}
                      <div class="absolute -top-8 left-1/2 transform -translate-x-1/2 bg-tokyo-night-bg-light px-2 py-1 rounded text-xs whitespace-nowrap animate-pulse">
                        {point.viewers}
                      </div>
                    {/if}
                  </div>
                {/each}
              </div>
              <div class="flex justify-between text-xs text-tokyo-night-comment mt-2">
                <span>{viewerActivity.length > 0 ? `${Math.floor((viewerActivity.length - 1) * 5 / 60)}m ago` : ''}</span>
                <span>Now</span>
              </div>
            </div>
          {:else}
            <div class="bg-tokyo-night-bg-highlight p-4 rounded-lg h-48 flex items-center justify-center">
              <p class="text-tokyo-night-comment">Collecting real-time data...</p>
            </div>
          {/if}

          <!-- Chart Legend -->
          <div class="flex items-center justify-between text-sm">
            <div class="flex items-center space-x-4">
              <div class="flex items-center space-x-2">
                <div class="w-3 h-3 bg-tokyo-night-blue rounded"></div>
                <span class="text-tokyo-night-comment">Total Concurrent Viewers</span>
              </div>
            </div>
            <div class="text-tokyo-night-comment">
              Peak: {maxViewers}
            </div>
          </div>
        </div>
      </div>

      <!-- Node Status -->
      <div class="card">
        <div class="card-header">
          <div class="flex items-center space-x-2 mb-2">
            <svelte:component this={getIconComponent('Server')} class="w-5 h-5 text-tokyo-night-fg" />
            <h2 class="text-xl font-semibold text-tokyo-night-fg">Infrastructure Nodes</h2>
          </div>
          <p class="text-tokyo-night-fg-dark">
            Status of your infrastructure nodes
          </p>
        </div>

        <div class="space-y-3">
          {#if nodeData.length > 0}
            {#each nodeData as node}
              <div class="flex items-center justify-between bg-tokyo-night-bg-highlight p-4 rounded-lg">
                <div>
                  <h3 class="font-semibold text-tokyo-night-fg">{node.name}</h3>
                  <p class="text-sm text-tokyo-night-comment">Region: {node.region}</p>
                  {#if node.ipAddress}
                    <p class="text-xs text-tokyo-night-comment font-mono">{node.ipAddress}</p>
                  {/if}
                </div>
                <div class="text-right">
                  <span class="text-sm px-2 py-1 rounded-full {node.status === 'HEALTHY' ? 'bg-green-500/20 text-green-500' : 'bg-red-500/20 text-red-500'}">
                    {node.status}
                  </span>
                  <p class="text-xs text-tokyo-night-comment mt-1">
                    Last seen: {new Date(node.lastSeen).toLocaleTimeString()}
                  </p>
                </div>
              </div>
            {/each}
          {:else}
            <div class="text-center py-8">
              <p class="text-tokyo-night-comment">No nodes configured</p>
            </div>
          {/if}
        </div>
      </div>
    </div>

    <!-- Current Active Streams -->
    <div class="card">
      <div class="card-header">
        <div class="flex items-center space-x-2 mb-2">
          <svelte:component this={getIconComponent('Video')} class="w-5 h-5 text-tokyo-night-fg" />
          <h2 class="text-xl font-semibold text-tokyo-night-fg">Active Streams</h2>
        </div>
        <p class="text-tokyo-night-fg-dark">
          Real-time status of all your streams
        </p>
      </div>

      {#if streams.length > 0}
        <div class="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-4">
          {#each streams as stream}
            <div class="bg-tokyo-night-bg-highlight p-4 rounded-lg border border-tokyo-night-fg-gutter">
              <div class="flex items-center justify-between mb-2">
                <h3 class="font-semibold text-tokyo-night-fg">
                  {stream.name || `Stream ${stream.id.slice(0, 8)}`}
                </h3>
                <div class="flex items-center space-x-1">
                  <div class="w-2 h-2 rounded-full {stream.status === 'live' ? 'bg-tokyo-night-green animate-pulse' : 'bg-tokyo-night-red'}"></div>
                  <span class="text-xs text-tokyo-night-comment">
                    {stream.status === 'live' ? 'Live' : 'Offline'}
                  </span>
                </div>
              </div>
              
              <div class="grid grid-cols-2 gap-4 text-sm">
                <div>
                  <p class="text-tokyo-night-comment">Viewers</p>
                  <p class="font-semibold text-tokyo-night-fg">{stream.viewers || 0}</p>
                </div>
                <div>
                  <p class="text-tokyo-night-comment">Resolution</p>
                  <p class="font-semibold text-tokyo-night-fg">{stream.resolution || 'Unknown'}</p>
                </div>
              </div>

              <div class="mt-3 pt-3 border-t border-tokyo-night-fg-gutter">
                <p class="text-xs text-tokyo-night-comment">
                  Node: {nodeData.length > 0 ? nodeData[0].name : 'Not configured'}
                </p>
              </div>
            </div>
          {/each}
        </div>
      {:else}
        <div class="text-center py-8">
          <svelte:component this={getIconComponent('Video')} class="w-16 h-16 text-tokyo-night-comment mx-auto mb-4" />
          <h3 class="text-lg font-semibold text-tokyo-night-fg mb-2">
            No Streams Found
          </h3>
          <p class="text-tokyo-night-comment mb-4">
            Create your first stream to see real-time analytics
          </p>
          <a href="{base}/streams" class="btn-primary">
            <svelte:component this={getIconComponent('Plus')} class="w-4 h-4 mr-2" />
            Create Stream
          </a>
        </div>
      {/if}
    </div>
  {/if}
</div> 
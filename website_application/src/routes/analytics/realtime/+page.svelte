<script>
  import { onMount, onDestroy } from "svelte";
  import { auth } from "$lib/stores/auth";
  import { streamAPI, analyticsAPIFunctions } from "$lib/api";

  let isAuthenticated = false;
  /** @type {any} */
  let user = null;
  let loading = true;
  /** @type {number | null} */
  let refreshInterval = null;

  // Real streams data
  /** @type {any[]} */
  let streams = [];
  
  // Real-time metrics aggregated from actual streams
  let liveMetrics = {
    totalViewers: 0,
    activeStreams: 0,
    totalBandwidthIn: 0,
    totalBandwidthOut: 0,
    avgLatency: "TODO",
    lastUpdated: new Date()
  };

  // Real viewer activity from actual stream metrics (last 20 data points)
  /** @type {Array<{time: Date, viewers: number}>} */
  let viewerActivity = [];

  // Real node data from backend
  /** @type {Array<{node_id: string, latitude: string|null, longitude: string|null, location: string|null, event_count: number, last_seen: string}>} */
  let nodeData = [];

  // Geographic distribution based on actual node data
  /** @type {Array<{node: string, name: string, location: string, lat: number, lng: number, viewers: number, percentage: number}>} */
  let geographicData = [];

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
    loading = false;
  });

  onDestroy(() => {
    if (refreshInterval) {
      clearInterval(refreshInterval);
    }
  });

  async function loadRealStreams() {
    try {
      const response = await streamAPI.getStreams();
      streams = response.data || [];
    } catch (error) {
      console.error("Failed to load streams:", error);
    }
  }

  async function updateRealTimeMetrics() {
    try {
      // Get realtime stream data
      const realtimeResponse = await analyticsAPIFunctions.getRealtimeStreams();
      const realtimeData = realtimeResponse.data || {};
      
      // Update streams with realtime data
      streams = realtimeData.streams || [];
      
      // Update aggregated metrics
      liveMetrics = {
        totalViewers: realtimeData.total_viewers || 0,
        activeStreams: realtimeData.active_streams || 0,
        totalBandwidthIn: realtimeData.total_bandwidth_in || 0,
        totalBandwidthOut: realtimeData.total_bandwidth_out || 0,
        avgLatency: realtimeData.avg_latency || "N/A",
        lastUpdated: new Date()
      };

      // Get realtime viewer data
      const viewerResponse = await analyticsAPIFunctions.getRealtimeViewers();
      const viewerData = viewerResponse.data || {};

      // Update viewer activity history (sliding window)
      if (viewerData.viewer_trend) {
        viewerActivity = [
          ...viewerActivity.slice(-19),
          {
            time: new Date(),
            viewers: viewerData.total_viewers || 0
          }
        ];
      }

      // Update geographic data
      await updateGeographicDistribution();
    } catch (error) {
      console.error("Failed to update realtime metrics:", error);
    }
  }

  /** @typedef {{
    node_id: string;
    location: string;
    latitude: number;
    longitude: number;
    health_score: number;
    is_healthy: boolean;
    connections_current: number;
    last_seen: string;
  }} NodeData */

  /** @typedef {{
    selected_node: string;
    client_city: string;
    client_country: string;
    client_latitude: number;
    client_longitude: number;
  }} RoutingEvent */

  /** @typedef {{
    viewers: number;
    location: string;
    lat: number;
    lng: number;
  }} NodeViewerData */

  /** @typedef {{
    node: string;
    name: string;
    location: string;
    lat: number;
    lng: number;
    viewers: number;
    percentage: number;
  }} GeoData */

  async function loadNodeData() {
    try {
      const response = await analyticsAPIFunctions.getNodeMetrics();
      const data = response.data || [];
      
      nodeData = data.map((/** @type {any} */ node) => ({
        node_id: node.node_id,
        location: node.location,
        latitude: node.latitude,
        longitude: node.longitude,
        health_score: node.health_score,
        is_healthy: node.is_healthy,
        connections_current: node.connections_current,
        last_seen: new Date(node.timestamp).toISOString()
      }));

      console.log("Node data loaded:", nodeData);
    } catch (error) {
      console.error("Error loading node data:", error);
      nodeData = [];
    }
  }

  async function updateGeographicDistribution() {
    try {
      const response = await analyticsAPIFunctions.getRoutingEvents();
      const routingData = /** @type {RoutingEvent[]} */ (response.data || []);

      // Group routing events by node and aggregate viewer counts
      /** @type {Record<string, NodeViewerData>} */
      const nodeViewers = {};
      
      routingData.forEach((event) => {
        const nodeKey = event.selected_node;
        if (!nodeViewers[nodeKey]) {
          nodeViewers[nodeKey] = {
            viewers: 0,
            location: event.client_city ? `${event.client_city}, ${event.client_country}` : event.client_country,
            lat: Number(event.client_latitude) || 0,
            lng: Number(event.client_longitude) || 0
          };
        }
        nodeViewers[nodeKey].viewers++;
      });

      // Convert to geographic data array
      /** @type {GeoData[]} */
      const geoData = Object.entries(nodeViewers).map(([nodeKey, data]) => {
        const node = nodeData.find(n => n.node_id === nodeKey);
        return {
          node: nodeKey,
          name: node?.location || nodeKey,
          location: data.location || 'Unknown Location',
          lat: Number(node?.latitude) || data.lat || 0,
          lng: Number(node?.longitude) || data.lng || 0,
          viewers: data.viewers,
          percentage: liveMetrics.totalViewers > 0 ? Math.round((data.viewers / liveMetrics.totalViewers) * 100) : 0
        };
      });

      geographicData = geoData;
    } catch (error) {
      console.error("Failed to update geographic distribution:", error);
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
      <h1 class="text-3xl font-bold text-tokyo-night-fg mb-2">
        ⚡ Real-time Analytics
      </h1>
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
        <span class="mr-2">🔄</span>
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
          <span class="text-2xl">👥</span>
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
          <span class="text-2xl">🎥</span>
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
          <span class="text-2xl">📥</span>
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
          <span class="text-2xl">📤</span>
        </div>
        <div class="absolute bottom-0 left-0 right-0 h-1 bg-tokyo-night-yellow opacity-20 animate-pulse"></div>
      </div>

      <div class="glow-card p-6 relative overflow-hidden">
        <div class="flex items-center justify-between">
          <div>
            <p class="text-sm text-tokyo-night-comment">Avg Latency</p>
            <p class="text-2xl font-bold text-tokyo-night-purple">
              {liveMetrics.avgLatency} ms
            </p>
          </div>
          <span class="text-2xl">⚡</span>
        </div>
        <div class="absolute bottom-0 left-0 right-0 h-1 bg-tokyo-night-purple opacity-20 animate-pulse"></div>
      </div>
    </div>

    <!-- Real-time Charts -->
    <div class="grid grid-cols-1 xl:grid-cols-3 gap-8">
      <!-- Live Viewer Activity Chart -->
      <div class="xl:col-span-2 card">
        <div class="card-header">
          <h2 class="text-xl font-semibold text-tokyo-night-fg mb-2">
            📈 Live Viewer Activity
          </h2>
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

      <!-- Geographic Distribution with Node Mapping -->
      <div class="card">
        <div class="card-header">
          <h2 class="text-xl font-semibold text-tokyo-night-fg mb-2">
            🌍 Node Distribution
          </h2>
          <p class="text-tokyo-night-fg-dark">
            Viewer distribution by MistServer node location
          </p>
        </div>

        <div class="space-y-3">
          {#if geographicData.length > 0}
            {#each geographicData as nodeData}
              <div class="flex items-center justify-between">
                <div class="flex-1">
                  <div class="flex items-center justify-between mb-1">
                    <div>
                      <span class="text-tokyo-night-fg font-medium">{nodeData.name}</span>
                      <p class="text-xs text-tokyo-night-comment">{nodeData.location}</p>
                    </div>
                    <div class="text-right">
                      <span class="text-sm font-semibold text-tokyo-night-fg">{nodeData.viewers}</span>
                      <span class="text-xs text-tokyo-night-comment ml-1">viewers</span>
                    </div>
                  </div>
                  <div class="bg-tokyo-night-bg-highlight rounded-full h-2 overflow-hidden">
                    <div 
                      class="bg-tokyo-night-cyan h-full rounded-full transition-all duration-500"
                      style="width: {nodeData.percentage}%"
                    />
                  </div>
                </div>
              </div>
            {/each}
          {:else}
            <div class="text-center py-8">
              <p class="text-tokyo-night-comment">No active streams</p>
            </div>
          {/if}
        </div>

        <!-- Node Mapping Info -->
        <div class="mt-6 pt-6 border-t border-tokyo-night-fg-gutter">
          <h3 class="font-semibold text-tokyo-night-fg mb-3">Node Configuration</h3>
          <div class="space-y-2 text-sm">
            {#each nodeData as node}
              <div class="flex justify-between">
                <span class="text-tokyo-night-comment">{node.location || node.node_id}</span>
                <span class="text-xs text-tokyo-night-comment">{node.latitude}, {node.longitude}</span>
              </div>
            {/each}
          </div>
        </div>
      </div>
    </div>

    <!-- Current Active Streams -->
    <div class="card">
      <div class="card-header">
        <h2 class="text-xl font-semibold text-tokyo-night-fg mb-2">
          🎥 Active Streams
        </h2>
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
                  {stream.title || `Stream ${stream.id.slice(0, 8)}`}
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
                  Node: {nodeData.find(n => n.node_id === 'local-mistserver')?.location || 'local-mistserver'}
                </p>
              </div>
            </div>
          {/each}
        </div>
      {:else}
        <div class="text-center py-8">
          <div class="text-4xl mb-4">🎥</div>
          <h3 class="text-lg font-semibold text-tokyo-night-fg mb-2">
            No Streams Found
          </h3>
          <p class="text-tokyo-night-comment mb-4">
            Create your first stream to see real-time analytics
          </p>
          <a href="/streams" class="btn-primary">
            <span class="mr-2">➕</span>
            Create Stream
          </a>
        </div>
      {/if}
    </div>
  {/if}
</div> 
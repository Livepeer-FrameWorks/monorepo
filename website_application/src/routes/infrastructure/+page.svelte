<script>
  import { onMount, onDestroy } from "svelte";
  import { auth } from "$lib/stores/auth";
  import { infrastructureService } from "$lib/graphql/services/infrastructure.js";
  import { performanceService } from "$lib/graphql/services/performance.js";
  import { toast } from "$lib/stores/toast.js";
  import LoadingCard from "$lib/components/LoadingCard.svelte";
  import SkeletonLoader from "$lib/components/SkeletonLoader.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import { getIconComponent } from "$lib/iconUtils.js";

  let isAuthenticated = false;
  let user = null;
  let loading = true;
  
  // Infrastructure data
  /** @type {any} */
  let tenant = null;
  /** @type {any[]} */
  let clusters = [];
  /** @type {any[]} */
  let nodes = [];
  /** @type {any} */
  let systemHealthSubscription = null;
  
  // Real-time system health data
  /** @type {Record<string, any>} */
  let systemHealth = {};
  
  // Performance analytics
  /** @type {any[]} */
  let nodePerformanceMetrics = [];
  let platformMetrics = {
    totalActiveNodes: 0,
    avgCpuUsage: 0,
    avgMemoryUsage: 0,
    avgHealthScore: 0
  };
  let platformSummary = { 
    totalViewers: 0,
    avgViewers: 0,
    totalStreams: 0,
    avgConnectionQuality: 0,
    avgBufferHealth: 0,
    uniqueCountries: 0,
    uniqueCities: 0,
    nodesHealthy: 0,
    nodesDegraded: 0,
    nodesUnhealthy: 0
  };
  
  // Time range for performance metrics (last 24 hours)
  const timeRange = {
    start: new Date(Date.now() - 24 * 60 * 60 * 1000).toISOString(),
    end: new Date().toISOString()
  };

  // Subscribe to auth store
  auth.subscribe((authState) => {
    isAuthenticated = authState.isAuthenticated;
    user = authState.user;
  });

  onMount(async () => {
    if (!isAuthenticated) {
      await auth.checkAuth();
    }
    await loadInfrastructureData();
    await loadPerformanceData();
    startSystemHealthSubscription();
  });

  // Cleanup on unmount
  onDestroy(() => {
    if (systemHealthSubscription) {
      systemHealthSubscription.unsubscribe();
    }
  });

  async function loadInfrastructureData() {
    try {
      loading = true;
      
      // Load infrastructure data in parallel
      const [tenantData, clustersData, nodesData] = await Promise.all([
        infrastructureService.getTenant().catch(() => null),
        infrastructureService.getClusters().catch(() => []),
        infrastructureService.getNodes().catch(() => [])
      ]);
      
      tenant = tenantData;
      clusters = clustersData || [];
      nodes = nodesData || [];
      
    } catch (error) {
      console.error("Failed to load infrastructure data:", error);
      toast.error("Failed to load infrastructure data. Please refresh the page.");
    } finally {
      loading = false;
    }
  }

  async function loadPerformanceData() {
    try {
      const [metrics, summary] = await Promise.all([
        performanceService.getNodePerformanceMetrics(null, timeRange),
        performanceService.getPlatformSummary(timeRange)
      ]);
      
      nodePerformanceMetrics = metrics || [];
      platformSummary = summary || platformSummary;
      
      // Calculate platform metrics from node performance data
      if (nodePerformanceMetrics.length > 0) {
        const totalNodes = nodePerformanceMetrics.length;
        const avgCpu = nodePerformanceMetrics.reduce((sum, node) => sum + (node.avgCpuUsage || 0), 0) / totalNodes;
        const avgMem = nodePerformanceMetrics.reduce((sum, node) => sum + (node.avgMemoryUsage || 0), 0) / totalNodes;
        const avgHealth = nodePerformanceMetrics.reduce((sum, node) => sum + (node.avgHealthScore || 0), 0) / totalNodes;
        
        platformMetrics = {
          totalActiveNodes: totalNodes,
          avgCpuUsage: avgCpu,
          avgMemoryUsage: avgMem,
          avgHealthScore: avgHealth
        };
      } else {
        // Use platform summary node counts if no metrics available
        platformMetrics = {
          totalActiveNodes: (platformSummary.nodesHealthy || 0) + (platformSummary.nodesDegraded || 0) + (platformSummary.nodesUnhealthy || 0),
          avgCpuUsage: 0,
          avgMemoryUsage: 0,
          avgHealthScore: 0
        };
      }
    } catch (error) {
      console.error("Failed to load performance data:", error);
    }
  }

  function startSystemHealthSubscription() {
    systemHealthSubscription = infrastructureService.subscribeToSystemHealth({
      /**
       * @param {any} healthData
       */
      onSystemHealth: (healthData) => {
        // Update system health data
        systemHealth[healthData.nodeId] = {
          ...healthData,
          timestamp: new Date(healthData.timestamp)
        };
        
        // Trigger reactivity
        systemHealth = { ...systemHealth };
      },
      /**
       * @param {any} error
       */
      onError: (error) => {
        console.error('System health subscription failed:', error);
        toast.warning("Real-time health monitoring disconnected. Data may be outdated.");
      }
    });
  }

  /**
   * @param {string} nodeId
   * @returns {string}
   */
  function getNodeStatus(nodeId) {
    const health = systemHealth[nodeId];
    if (!health) return 'UNKNOWN';
    return health.status;
  }

  /**
   * @param {string} nodeId
   * @returns {number}
   */
  function getNodeHealthScore(nodeId) {
    const health = systemHealth[nodeId];
    if (!health) return 0;
    return Math.round(health.healthScore * 100);
  }

  /**
   * @param {string} nodeId
   * @returns {string}
   */
  function formatCpuUsage(nodeId) {
    const health = systemHealth[nodeId];
    if (!health) return '0%';
    return `${Math.round(health.cpuUsage)}%`;
  }

  /**
   * @param {string} nodeId
   * @returns {string}
   */
  function formatMemoryUsage(nodeId) {
    const health = systemHealth[nodeId];
    if (!health) return '0%';
    return `${Math.round(health.memoryUsage)}%`;
  }

  /**
   * @param {string | null | undefined} status
   * @returns {string}
   */
  function getStatusColor(status) {
    switch (status?.toLowerCase()) {
      case 'healthy': return 'text-green-500';
      case 'degraded': return 'text-yellow-500';
      case 'unhealthy': return 'text-red-500';
      default: return 'text-gray-500';
    }
  }
</script>

<svelte:head>
  <title>Infrastructure Dashboard - FrameWorks</title>
</svelte:head>

<div class="min-h-screen bg-tokyo-night-bg text-tokyo-night-fg">
  <div class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8">
    <div class="mb-8">
      <h1 class="text-3xl font-bold text-tokyo-night-blue mb-2">
        Infrastructure Dashboard
      </h1>
      <p class="text-tokyo-night-comment">
        Monitor your clusters, nodes, and system health in real-time
      </p>
    </div>

    {#if loading}
      <!-- Tenant Information Skeleton -->
      <div class="bg-tokyo-night-surface rounded-lg p-6 mb-8">
        <SkeletonLoader type="text-lg" className="w-40 mb-4" />
        <div class="grid grid-cols-1 md:grid-cols-3 gap-4">
          {#each Array(3) as _}
            <div>
              <SkeletonLoader type="text-sm" className="w-20 mb-1" />
              <SkeletonLoader type="text" className="w-32" />
            </div>
          {/each}
        </div>
      </div>

      <!-- Clusters Skeleton -->
      <div class="bg-tokyo-night-surface rounded-lg p-6 mb-8">
        <SkeletonLoader type="text-lg" className="w-24 mb-4" />
        <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
          {#each Array(3) as _}
            <LoadingCard variant="infrastructure" />
          {/each}
        </div>
      </div>

      <!-- Nodes Skeleton -->
      <div class="bg-tokyo-night-surface rounded-lg p-6">
        <SkeletonLoader type="text-lg" className="w-20 mb-4" />
        <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
          {#each Array(6) as _}
            <LoadingCard variant="infrastructure" />
          {/each}
        </div>
      </div>
    {:else}
      <!-- Tenant Information -->
      {#if tenant}
        <div class="bg-tokyo-night-surface rounded-lg p-6 mb-8">
          <h2 class="text-xl font-semibold mb-4 text-tokyo-night-cyan">Tenant Information</h2>
          <div class="grid grid-cols-1 md:grid-cols-3 gap-4">
            <div>
              <p class="text-sm text-tokyo-night-comment">Tenant Name</p>
              <p class="font-medium">{tenant.name}</p>
            </div>
            <div>
              <p class="text-sm text-tokyo-night-comment">Tenant ID</p>
              <p class="font-mono text-sm">{tenant.id}</p>
            </div>
            <div>
              <p class="text-sm text-tokyo-night-comment">Created</p>
              <p class="text-sm">{new Date(tenant.createdAt).toLocaleDateString()}</p>
            </div>
          </div>
        </div>
      {/if}

      <!-- Platform Performance Overview -->
      <div class="bg-tokyo-night-surface rounded-lg p-6 mb-8">
        <h2 class="text-xl font-semibold mb-6 text-tokyo-night-cyan">Platform Performance (Last 24 Hours)</h2>
        
        <div class="grid grid-cols-1 md:grid-cols-4 gap-4 mb-6">
          <div class="bg-tokyo-night-bg rounded-lg p-4 border border-tokyo-night-selection">
            <div class="text-2xl font-bold text-tokyo-night-green mb-2">
              {platformMetrics.totalActiveNodes || 0}
            </div>
            <div class="text-sm text-tokyo-night-comment">Active Nodes</div>
          </div>
          <div class="bg-tokyo-night-bg rounded-lg p-4 border border-tokyo-night-selection">
            <div class="text-2xl font-bold text-tokyo-night-blue mb-2">
              {platformMetrics.avgCpuUsage?.toFixed(1) || 0}%
            </div>
            <div class="text-sm text-tokyo-night-comment">Avg CPU Usage</div>
          </div>
          <div class="bg-tokyo-night-bg rounded-lg p-4 border border-tokyo-night-selection">
            <div class="text-2xl font-bold text-tokyo-night-purple mb-2">
              {platformMetrics.avgMemoryUsage?.toFixed(1) || 0}%
            </div>
            <div class="text-sm text-tokyo-night-comment">Avg Memory Usage</div>
          </div>
          <div class="bg-tokyo-night-bg rounded-lg p-4 border border-tokyo-night-selection">
            <div class="text-2xl font-bold text-tokyo-night-orange mb-2">
              {Math.round(platformMetrics.avgHealthScore) || 0}%
            </div>
            <div class="text-sm text-tokyo-night-comment">Avg Health Score</div>
          </div>
        </div>

        {#if nodePerformanceMetrics.length > 0}
          <div class="overflow-x-auto">
            <h3 class="text-lg font-medium mb-4 text-tokyo-night-fg">Node Performance Details</h3>
            <table class="w-full text-sm">
              <thead>
                <tr class="border-b border-tokyo-night-selection">
                  <th class="text-left py-2 text-tokyo-night-comment">Node</th>
                  <th class="text-left py-2 text-tokyo-night-comment">CPU</th>
                  <th class="text-left py-2 text-tokyo-night-comment">Memory</th>
                  <th class="text-left py-2 text-tokyo-night-comment">Health Score</th>
                  <th class="text-left py-2 text-tokyo-night-comment">Peak Streams</th>
                  <th class="text-left py-2 text-tokyo-night-comment">Avg Load</th>
                </tr>
              </thead>
              <tbody>
                {#each nodePerformanceMetrics.slice(0, 10) as metric}
                  <tr class="border-b border-tokyo-night-selection/30">
                    <td class="py-2 font-mono text-xs">{metric.nodeId}</td>
                    <td class="py-2">
                      <span class="{metric.avgCpuUsage > 80 ? 'text-red-400' : metric.avgCpuUsage > 60 ? 'text-yellow-400' : 'text-green-400'}">
                        {metric.avgCpuUsage?.toFixed(1) || 0}%
                      </span>
                    </td>
                    <td class="py-2">
                      <span class="{metric.avgMemoryUsage > 80 ? 'text-red-400' : metric.avgMemoryUsage > 60 ? 'text-yellow-400' : 'text-green-400'}">
                        {metric.avgMemoryUsage?.toFixed(1) || 0}%
                      </span>
                    </td>
                    <td class="py-2">
                      <span class="{metric.avgHealthScore > 0.8 ? 'text-green-400' : metric.avgHealthScore > 0.6 ? 'text-yellow-400' : 'text-red-400'}">
                        {Math.round(metric.avgHealthScore * 100) || 0}%
                      </span>
                    </td>
                    <td class="py-2 font-semibold">{metric.peakActiveStreams || 0}</td>
                    <td class="py-2 text-tokyo-night-comment">{metric.avgStreamLoad?.toFixed(2) || '0.00'}</td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        {:else}
          <div class="text-center py-8">
            <svelte:component this={getIconComponent('BarChart')} class="w-12 h-12 text-tokyo-night-comment mx-auto mb-4" />
            <p class="text-tokyo-night-comment">No performance data available</p>
          </div>
        {/if}
      </div>

      <!-- Viewer & Stream Metrics -->
      <div class="bg-tokyo-night-surface rounded-lg p-6 mb-8">
        <h2 class="text-xl font-semibold mb-6 text-tokyo-night-cyan">Viewer & Stream Metrics (Last 24 Hours)</h2>
        
        <div class="grid grid-cols-1 md:grid-cols-4 gap-4">
          <div class="bg-tokyo-night-bg rounded-lg p-4 border border-tokyo-night-selection">
            <div class="text-2xl font-bold text-tokyo-night-blue mb-2">
              {platformSummary.totalViewers || 0}
            </div>
            <div class="text-sm text-tokyo-night-comment">Total Viewers</div>
          </div>
          <div class="bg-tokyo-night-bg rounded-lg p-4 border border-tokyo-night-selection">
            <div class="text-2xl font-bold text-tokyo-night-purple mb-2">
              {platformSummary.totalStreams || 0}
            </div>
            <div class="text-sm text-tokyo-night-comment">Active Streams</div>
          </div>
          <div class="bg-tokyo-night-bg rounded-lg p-4 border border-tokyo-night-selection">
            <div class="text-2xl font-bold text-tokyo-night-green mb-2">
              {Math.round(platformSummary.avgConnectionQuality) || 0}%
            </div>
            <div class="text-sm text-tokyo-night-comment">Connection Quality</div>
          </div>
          <div class="bg-tokyo-night-bg rounded-lg p-4 border border-tokyo-night-selection">
            <div class="text-2xl font-bold text-tokyo-night-orange mb-2">
              {platformSummary.uniqueCountries || 0}
            </div>
            <div class="text-sm text-tokyo-night-comment">Countries Reached</div>
          </div>
        </div>
      </div>

      <!-- Clusters Overview -->
      <div class="bg-tokyo-night-surface rounded-lg p-6 mb-8">
        <h2 class="text-xl font-semibold mb-4 text-tokyo-night-cyan">Clusters</h2>
        {#if clusters.length === 0}
          <EmptyState 
            title="No clusters found"
            description="Infrastructure clusters will appear here when configured"
            size="sm"
            showAction={false}
          >
            <svelte:component this={getIconComponent('Server')} class="w-8 h-8 text-tokyo-night-fg-dark mx-auto mb-2" />
          </EmptyState>
        {:else}
          <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
            {#each clusters as cluster}
              <div class="bg-tokyo-night-bg rounded-lg p-4 border border-tokyo-night-selection">
                <div class="flex items-center justify-between mb-2">
                  <h3 class="font-semibold">{cluster.name}</h3>
                  <span class="px-2 py-1 text-xs rounded-full {getStatusColor(cluster.status)} bg-opacity-20">
                    {cluster.status}
                  </span>
                </div>
                <div class="space-y-1 text-sm text-tokyo-night-comment">
                  <p>Region: {cluster.region}</p>
                  <p>Nodes: {cluster.nodes?.length || 0}</p>
                  <p>Created: {new Date(cluster.createdAt).toLocaleDateString()}</p>
                </div>
              </div>
            {/each}
          </div>
        {/if}
      </div>

      <!-- Nodes Grid -->
      <div class="bg-tokyo-night-surface rounded-lg p-6">
        <h2 class="text-xl font-semibold mb-4 text-tokyo-night-cyan">Nodes</h2>
        {#if nodes.length === 0}
          <EmptyState 
            title="No nodes found"
            description="Infrastructure nodes will appear here when deployed"
            size="sm"
            showAction={false}
          >
            <svelte:component this={getIconComponent('HardDrive')} class="w-8 h-8 text-tokyo-night-fg-dark mx-auto mb-2" />
          </EmptyState>
        {:else}
          <div class="grid grid-cols-1 lg:grid-cols-2 gap-4">
            {#each nodes as node}
              <div class="bg-tokyo-night-bg rounded-lg p-4 border border-tokyo-night-selection">
                <div class="flex items-center justify-between mb-3">
                  <div>
                    <h3 class="font-semibold">{node.name}</h3>
                    <p class="text-sm text-tokyo-night-comment">{node.type} • {node.region}</p>
                  </div>
                  <div class="text-right">
                    <span class="px-2 py-1 text-xs rounded-full {getStatusColor(getNodeStatus(node.id))} bg-opacity-20">
                      {getNodeStatus(node.id)}
                    </span>
                    {#if systemHealth[node.id]}
                      <p class="text-xs text-tokyo-night-comment mt-1">
                        Health: {getNodeHealthScore(node.id)}%
                      </p>
                    {/if}
                  </div>
                </div>

                <div class="grid grid-cols-2 gap-4 text-sm">
                  <div>
                    <p class="text-tokyo-night-comment">CPU Usage</p>
                    <p class="font-medium">{formatCpuUsage(node.id)}</p>
                  </div>
                  <div>
                    <p class="text-tokyo-night-comment">Memory Usage</p>
                    <p class="font-medium">{formatMemoryUsage(node.id)}</p>
                  </div>
                  <div>
                    <p class="text-tokyo-night-comment">IP Address</p>
                    <p class="font-mono text-xs">{node.ipAddress || 'N/A'}</p>
                  </div>
                  <div>
                    <p class="text-tokyo-night-comment">Last Seen</p>
                    <p class="text-xs">{new Date(node.lastSeen).toLocaleString()}</p>
                  </div>
                </div>

                {#if systemHealth[node.id]}
                  <div class="mt-3 pt-3 border-t border-tokyo-night-selection">
                    <div class="grid grid-cols-3 gap-2 text-xs">
                      <div>
                        <p class="text-tokyo-night-comment">Disk</p>
                        <p>{Math.round(systemHealth[node.id].diskUsage)}%</p>
                      </div>
                      <div>
                        <p class="text-tokyo-night-comment">Updated</p>
                        <p>{systemHealth[node.id].timestamp?.toLocaleTimeString()}</p>
                      </div>
                      <div>
                        <p class="text-tokyo-night-comment">Score</p>
                        <p>{getNodeHealthScore(node.id)}%</p>
                      </div>
                    </div>
                  </div>
                {/if}
              </div>
            {/each}
          </div>
        {/if}
      </div>
    {/if}
  </div>
</div>

<style>
  /* Tokyo Night theme colors already defined globally */
</style>
<script>
  import { onMount, onDestroy } from "svelte";
  import { auth } from "$lib/stores/auth";
  import { infrastructureService } from "$lib/graphql/services/infrastructure.js";
  import { toast } from "$lib/stores/toast.js";
  import LoadingCard from "$lib/components/LoadingCard.svelte";
  import SkeletonLoader from "$lib/components/SkeletonLoader.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import { getIconComponent } from "$lib/iconUtils.js";

  let isAuthenticated = false;
  let user = null;
  let loading = true;
  
  // Infrastructure data
  let tenant = null;
  let clusters = [];
  let nodes = [];
  let systemHealthSubscription = null;
  
  // Real-time system health data
  let systemHealth = {};

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

  function startSystemHealthSubscription() {
    systemHealthSubscription = infrastructureService.subscribeToSystemHealth({
      onSystemHealth: (healthData) => {
        // Update system health data
        systemHealth[healthData.nodeId] = {
          ...healthData,
          timestamp: new Date(healthData.timestamp)
        };
        
        // Trigger reactivity
        systemHealth = { ...systemHealth };
      },
      onError: (error) => {
        console.error('System health subscription failed:', error);
        toast.warning("Real-time health monitoring disconnected. Data may be outdated.");
      }
    });
  }

  function getNodeStatus(nodeId) {
    const health = systemHealth[nodeId];
    if (!health) return 'UNKNOWN';
    return health.status;
  }

  function getNodeHealthScore(nodeId) {
    const health = systemHealth[nodeId];
    if (!health) return 0;
    return Math.round(health.healthScore * 100);
  }

  function formatCpuUsage(nodeId) {
    const health = systemHealth[nodeId];
    if (!health) return '0%';
    return `${Math.round(health.cpuUsage)}%`;
  }

  function formatMemoryUsage(nodeId) {
    const health = systemHealth[nodeId];
    if (!health) return '0%';
    return `${Math.round(health.memoryUsage)}%`;
  }

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
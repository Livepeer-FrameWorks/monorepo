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
  
  // Node management data
  let nodes = [];
  let clusters = [];
  let systemHealthSubscription = null;
  let systemHealth = {};
  
  // Filters and search
  let searchTerm = "";
  let statusFilter = "all";
  let clusterFilter = "all";
  let sortBy = "name";
  let sortOrder = "asc";

  // Subscribe to auth store
  auth.subscribe((authState) => {
    isAuthenticated = authState.isAuthenticated;
    user = authState.user;
  });

  onMount(async () => {
    if (!isAuthenticated) {
      await auth.checkAuth();
    }
    await loadNodeData();
    startSystemHealthSubscription();
  });

  onDestroy(() => {
    if (systemHealthSubscription) {
      systemHealthSubscription.unsubscribe();
    }
  });

  async function loadNodeData() {
    try {
      loading = true;
      
      const [nodesData, clustersData] = await Promise.all([
        infrastructureService.getNodes().catch(() => []),
        infrastructureService.getClusters().catch(() => [])
      ]);
      
      nodes = nodesData || [];
      clusters = clustersData || [];
      
    } catch (error) {
      console.error("Failed to load node data:", error);
      toast.error("Failed to load node data. Please refresh the page.");
    } finally {
      loading = false;
    }
  }

  function startSystemHealthSubscription() {
    systemHealthSubscription = infrastructureService.subscribeToSystemHealth({
      onSystemHealth: (healthData) => {
        systemHealth[healthData.nodeId] = {
          ...healthData,
          timestamp: new Date(healthData.timestamp)
        };
        systemHealth = { ...systemHealth };
      },
      onError: (error) => {
        console.error('System health subscription failed:', error);
        toast.warning("Real-time monitoring disconnected. Data may be outdated.");
      }
    });
  }

  // Computed properties
  $: filteredNodes = nodes
    .filter(node => {
      const matchesSearch = searchTerm === "" || 
        node.name.toLowerCase().includes(searchTerm.toLowerCase()) ||
        node.id.toLowerCase().includes(searchTerm.toLowerCase()) ||
        (node.ipAddress && node.ipAddress.includes(searchTerm));
      
      const matchesStatus = statusFilter === "all" || 
        getNodeStatus(node.id).toLowerCase() === statusFilter;
      
      const matchesCluster = clusterFilter === "all" || 
        node.cluster === clusterFilter;
      
      return matchesSearch && matchesStatus && matchesCluster;
    })
    .sort((a, b) => {
      let aVal, bVal;
      
      switch (sortBy) {
        case "name":
          aVal = a.name;
          bVal = b.name;
          break;
        case "status":
          aVal = getNodeStatus(a.id);
          bVal = getNodeStatus(b.id);
          break;
        case "health":
          aVal = getNodeHealthScore(a.id);
          bVal = getNodeHealthScore(b.id);
          break;
        case "cluster":
          aVal = a.cluster || "";
          bVal = b.cluster || "";
          break;
        case "region":
          aVal = a.region || "";
          bVal = b.region || "";
          break;
        default:
          aVal = a[sortBy];
          bVal = b[sortBy];
      }
      
      if (sortOrder === "asc") {
        return aVal < bVal ? -1 : aVal > bVal ? 1 : 0;
      } else {
        return aVal > bVal ? -1 : aVal < bVal ? 1 : 0;
      }
    });

  // Helper functions
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
      case 'healthy': return 'text-green-500 bg-green-500/20';
      case 'degraded': return 'text-yellow-500 bg-yellow-500/20';
      case 'unhealthy': return 'text-red-500 bg-red-500/20';
      default: return 'text-gray-500 bg-gray-500/20';
    }
  }

  function getStatusIcon(status) {
    switch (status?.toLowerCase()) {
      case 'healthy': return 'CheckCircle';
      case 'degraded': return 'AlertTriangle';
      case 'unhealthy': return 'XCircle';
      default: return 'HelpCircle';
    }
  }

  // Get unique clusters for filter
  $: uniqueClusters = [...new Set(nodes.map(n => n.cluster).filter(Boolean))];

  // Stats
  $: nodeStats = {
    total: nodes.length,
    healthy: nodes.filter(n => getNodeStatus(n.id).toLowerCase() === 'healthy').length,
    degraded: nodes.filter(n => getNodeStatus(n.id).toLowerCase() === 'degraded').length,
    unhealthy: nodes.filter(n => getNodeStatus(n.id).toLowerCase() === 'unhealthy').length,
    unknown: nodes.filter(n => getNodeStatus(n.id).toLowerCase() === 'unknown').length
  };
</script>

<svelte:head>
  <title>Node Management - FrameWorks</title>
</svelte:head>

<div class="min-h-screen bg-tokyo-night-bg text-tokyo-night-fg">
  <div class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8">
    <!-- Header -->
    <div class="mb-8">
      <h1 class="text-3xl font-bold text-tokyo-night-blue mb-2">
        Node Management
      </h1>
      <p class="text-tokyo-night-comment">
        Monitor and manage your Edge nodes worldwide
      </p>
    </div>

    {#if loading}
      <!-- Loading state -->
      <div class="grid grid-cols-1 md:grid-cols-4 gap-4 mb-8">
        {#each Array(4) as _}
          <div class="bg-tokyo-night-surface rounded-lg p-4">
            <SkeletonLoader type="text" className="w-16 mb-2" />
            <SkeletonLoader type="text-lg" className="w-8" />
          </div>
        {/each}
      </div>
      
      <div class="bg-tokyo-night-surface rounded-lg p-6">
        <SkeletonLoader type="text-lg" className="w-32 mb-4" />
        <div class="grid grid-cols-1 lg:grid-cols-2 xl:grid-cols-3 gap-4">
          {#each Array(6) as _}
            <LoadingCard variant="infrastructure" />
          {/each}
        </div>
      </div>
    {:else}
      <!-- Stats Cards -->
      <div class="grid grid-cols-2 md:grid-cols-4 gap-4 mb-8">
        <div class="bg-tokyo-night-surface rounded-lg p-4 border border-tokyo-night-selection">
          <div class="text-2xl font-bold text-tokyo-night-blue mb-1">
            {nodeStats.total}
          </div>
          <div class="text-sm text-tokyo-night-comment">Total Nodes</div>
        </div>
        <div class="bg-tokyo-night-surface rounded-lg p-4 border border-tokyo-night-selection">
          <div class="text-2xl font-bold text-green-500 mb-1">
            {nodeStats.healthy}
          </div>
          <div class="text-sm text-tokyo-night-comment">Healthy</div>
        </div>
        <div class="bg-tokyo-night-surface rounded-lg p-4 border border-tokyo-night-selection">
          <div class="text-2xl font-bold text-yellow-500 mb-1">
            {nodeStats.degraded}
          </div>
          <div class="text-sm text-tokyo-night-comment">Degraded</div>
        </div>
        <div class="bg-tokyo-night-surface rounded-lg p-4 border border-tokyo-night-selection">
          <div class="text-2xl font-bold text-red-500 mb-1">
            {nodeStats.unhealthy + nodeStats.unknown}
          </div>
          <div class="text-sm text-tokyo-night-comment">Issues</div>
        </div>
      </div>

      <!-- Filters and Search -->
      <div class="bg-tokyo-night-surface rounded-lg p-6 mb-8">
        <div class="grid grid-cols-1 md:grid-cols-5 gap-4">
          <!-- Search -->
          <div class="md:col-span-2">
            <label class="block text-sm font-medium text-tokyo-night-comment mb-1">
              Search Nodes
            </label>
            <div class="relative">
              <input
                type="text"
                bind:value={searchTerm}
                placeholder="Name, ID, or IP address..."
                class="w-full px-3 py-2 pl-10 bg-tokyo-night-bg border border-tokyo-night-selection rounded-lg text-sm focus:border-tokyo-night-blue focus:ring-1 focus:ring-tokyo-night-blue"
              />
              <svelte:component 
                this={getIconComponent('Search')} 
                class="absolute left-3 top-2.5 w-4 h-4 text-tokyo-night-comment" 
              />
            </div>
          </div>

          <!-- Status Filter -->
          <div>
            <label class="block text-sm font-medium text-tokyo-night-comment mb-1">
              Status
            </label>
            <select 
              bind:value={statusFilter}
              class="w-full px-3 py-2 bg-tokyo-night-bg border border-tokyo-night-selection rounded-lg text-sm focus:border-tokyo-night-blue focus:ring-1 focus:ring-tokyo-night-blue"
            >
              <option value="all">All Status</option>
              <option value="healthy">Healthy</option>
              <option value="degraded">Degraded</option>
              <option value="unhealthy">Unhealthy</option>
              <option value="unknown">Unknown</option>
            </select>
          </div>

          <!-- Cluster Filter -->
          <div>
            <label class="block text-sm font-medium text-tokyo-night-comment mb-1">
              Cluster
            </label>
            <select 
              bind:value={clusterFilter}
              class="w-full px-3 py-2 bg-tokyo-night-bg border border-tokyo-night-selection rounded-lg text-sm focus:border-tokyo-night-blue focus:ring-1 focus:ring-tokyo-night-blue"
            >
              <option value="all">All Clusters</option>
              {#each uniqueClusters as cluster}
                <option value={cluster}>{cluster}</option>
              {/each}
            </select>
          </div>

          <!-- Sort -->
          <div>
            <label class="block text-sm font-medium text-tokyo-night-comment mb-1">
              Sort By
            </label>
            <div class="flex gap-1">
              <select 
                bind:value={sortBy}
                class="flex-1 px-3 py-2 bg-tokyo-night-bg border border-tokyo-night-selection rounded-lg text-sm focus:border-tokyo-night-blue focus:ring-1 focus:ring-tokyo-night-blue"
              >
                <option value="name">Name</option>
                <option value="status">Status</option>
                <option value="health">Health</option>
                <option value="cluster">Cluster</option>
                <option value="region">Region</option>
              </select>
              <button
                on:click={() => sortOrder = sortOrder === "asc" ? "desc" : "asc"}
                class="px-2 py-2 bg-tokyo-night-bg border border-tokyo-night-selection rounded-lg hover:bg-tokyo-night-selection transition-colors"
                title={sortOrder === "asc" ? "Sort descending" : "Sort ascending"}
              >
                <svelte:component 
                  this={getIconComponent(sortOrder === "asc" ? 'ArrowUp' : 'ArrowDown')} 
                  class="w-4 h-4" 
                />
              </button>
            </div>
          </div>
        </div>
      </div>

      <!-- Nodes Grid -->
      <div class="bg-tokyo-night-surface rounded-lg p-6">
        <div class="flex items-center justify-between mb-6">
          <h2 class="text-xl font-semibold text-tokyo-night-cyan">
            Nodes ({filteredNodes.length})
          </h2>
          <button 
            on:click={loadNodeData}
            class="flex items-center gap-2 px-3 py-2 bg-tokyo-night-blue text-white rounded-lg hover:bg-tokyo-night-blue/80 transition-colors text-sm"
          >
            <svelte:component this={getIconComponent('RefreshCw')} class="w-4 h-4" />
            Refresh
          </button>
        </div>

        {#if filteredNodes.length === 0}
          <EmptyState 
            title={nodes.length === 0 ? "No nodes found" : "No matching nodes"}
            description={nodes.length === 0 ? "Infrastructure nodes will appear here when deployed" : "Try adjusting your filters or search criteria"}
            size="md"
            showAction={false}
          >
            <svelte:component this={getIconComponent('Server')} class="w-12 h-12 text-tokyo-night-fg-dark mx-auto mb-4" />
          </EmptyState>
        {:else}
          <div class="grid grid-cols-1 lg:grid-cols-2 xl:grid-cols-3 gap-6">
            {#each filteredNodes as node}
              <div class="bg-tokyo-night-bg rounded-lg p-6 border border-tokyo-night-selection hover:border-tokyo-night-blue/50 transition-colors">
                <!-- Node Header -->
                <div class="flex items-center justify-between mb-4">
                  <div class="flex items-center gap-3">
                    <div class="w-10 h-10 bg-tokyo-night-surface rounded-lg flex items-center justify-center">
                      <svelte:component this={getIconComponent('Server')} class="w-5 h-5 text-tokyo-night-blue" />
                    </div>
                    <div>
                      <h3 class="font-semibold text-tokyo-night-fg">{node.name}</h3>
                      <p class="text-sm text-tokyo-night-comment">{node.type}</p>
                    </div>
                  </div>
                  <div class="flex items-center gap-2">
                    <span class="px-2 py-1 text-xs rounded-full {getStatusColor(getNodeStatus(node.id))} flex items-center gap-1">
                      <svelte:component this={getIconComponent(getStatusIcon(getNodeStatus(node.id)))} class="w-3 h-3" />
                      {getNodeStatus(node.id)}
                    </span>
                  </div>
                </div>

                <!-- Node Details -->
                <div class="space-y-3 mb-4">
                  <div class="grid grid-cols-2 gap-4 text-sm">
                    <div>
                      <p class="text-tokyo-night-comment">Region</p>
                      <p class="font-medium">{node.region || 'N/A'}</p>
                    </div>
                    <div>
                      <p class="text-tokyo-night-comment">Cluster</p>
                      <p class="font-medium">{node.cluster || 'N/A'}</p>
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
                    <!-- Real-time Metrics -->
                    <div class="pt-3 border-t border-tokyo-night-selection">
                      <div class="grid grid-cols-3 gap-4 text-sm">
                        <div>
                          <p class="text-tokyo-night-comment">CPU</p>
                          <p class="font-medium">{formatCpuUsage(node.id)}</p>
                        </div>
                        <div>
                          <p class="text-tokyo-night-comment">Memory</p>
                          <p class="font-medium">{formatMemoryUsage(node.id)}</p>
                        </div>
                        <div>
                          <p class="text-tokyo-night-comment">Health</p>
                          <p class="font-medium">{getNodeHealthScore(node.id)}%</p>
                        </div>
                      </div>
                      
                      {#if systemHealth[node.id].diskUsage !== undefined}
                        <div class="mt-2 text-xs text-tokyo-night-comment">
                          Disk: {Math.round(systemHealth[node.id].diskUsage)}% • 
                          Updated: {systemHealth[node.id].timestamp?.toLocaleTimeString()}
                        </div>
                      {/if}
                    </div>
                  {/if}
                </div>

                <!-- Node Actions -->
                <div class="flex gap-2">
                  <button class="flex-1 px-3 py-2 bg-tokyo-night-selection hover:bg-tokyo-night-selection/80 rounded-lg text-sm transition-colors">
                    View Details
                  </button>
                  <button class="px-3 py-2 bg-tokyo-night-selection hover:bg-tokyo-night-selection/80 rounded-lg transition-colors">
                    <svelte:component this={getIconComponent('Settings')} class="w-4 h-4" />
                  </button>
                  <button class="px-3 py-2 bg-tokyo-night-selection hover:bg-tokyo-night-selection/80 rounded-lg transition-colors">
                    <svelte:component this={getIconComponent('MoreHorizontal')} class="w-4 h-4" />
                  </button>
                </div>
              </div>
            {/each}
          </div>
        {/if}
      </div>
    {/if}
  </div>
</div>
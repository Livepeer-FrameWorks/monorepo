<script>
  import { onMount, onDestroy } from "svelte";
  import { auth } from "$lib/stores/auth";
  import { infrastructureService } from "$lib/graphql/services/infrastructure.js";
  import { toast } from "$lib/stores/toast.js";
  import LoadingCard from "$lib/components/LoadingCard.svelte";
  import SkeletonLoader from "$lib/components/SkeletonLoader.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import { getIconComponent } from "$lib/iconUtils";
  import {
    Card,
    CardContent,
    CardHeader,
    CardTitle,
    CardDescription,
  } from "$lib/components/ui/card";
  import { Input } from "$lib/components/ui/input";
  import { Button } from "$lib/components/ui/button";
  import {
    Select,
    SelectTrigger,
    SelectContent,
    SelectItem,
  } from "$lib/components/ui/select";

  let isAuthenticated = false;
  let loading = $state(true);

  // Node management data
  let nodes = $state([]);
  let systemHealthSubscription = null;
  let systemHealth = $state({});

  // Filters and search
  let searchTerm = $state("");
  let statusFilter = $state("all");
  let clusterFilter = $state("all");
  let sortBy = $state("name");
  let sortOrder = $state("asc");
  const statusFilterLabels = {
    all: "All Status",
    healthy: "Healthy",
    degraded: "Degraded",
    unhealthy: "Unhealthy",
    unknown: "Unknown",
  };

  // Subscribe to auth store
  auth.subscribe((authState) => {
    isAuthenticated = authState.isAuthenticated;
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

      const nodesData = await infrastructureService.getNodes().catch(() => []);

      nodes = nodesData || [];
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
          timestamp: new Date(healthData.timestamp),
        };
        systemHealth = { ...systemHealth };
      },
      onError: (error) => {
        console.error("System health subscription failed:", error);
        toast.warning(
          "Real-time monitoring disconnected. Data may be outdated.",
        );
      },
    });
  }

  // Helper functions
  function getNodeStatus(nodeId) {
    const health = systemHealth[nodeId];
    if (!health) return "UNKNOWN";
    return health.status;
  }

  function getNodeHealthScore(nodeId) {
    const health = systemHealth[nodeId];
    if (!health) return 0;
    return Math.round(health.healthScore * 100);
  }

  function formatCpuUsage(nodeId) {
    const health = systemHealth[nodeId];
    if (!health) return "0%";
    return `${Math.round(health.cpuUsage)}%`;
  }

  function formatMemoryUsage(nodeId) {
    const health = systemHealth[nodeId];
    if (!health) return "0%";
    return `${Math.round(health.memoryUsage)}%`;
  }

  function getStatusColor(status) {
    switch (status?.toLowerCase()) {
      case "healthy":
        return "text-green-500 bg-green-500/20";
      case "degraded":
        return "text-yellow-500 bg-yellow-500/20";
      case "unhealthy":
        return "text-red-500 bg-red-500/20";
      default:
        return "text-gray-500 bg-gray-500/20";
    }
  }

  function getStatusIcon(status) {
    switch (status?.toLowerCase()) {
      case "healthy":
        return "CheckCircle";
      case "degraded":
        return "AlertTriangle";
      case "unhealthy":
        return "XCircle";
      default:
        return "HelpCircle";
    }
  }

  // Computed properties
  let filteredNodes = $derived(
    nodes
      .filter((node) => {
        const matchesSearch =
          searchTerm === "" ||
          node.name.toLowerCase().includes(searchTerm.toLowerCase()) ||
          node.id.toLowerCase().includes(searchTerm.toLowerCase()) ||
          (node.ipAddress && node.ipAddress.includes(searchTerm));

        const matchesStatus =
          statusFilter === "all" ||
          getNodeStatus(node.id).toLowerCase() === statusFilter;

        const matchesCluster =
          clusterFilter === "all" || node.cluster === clusterFilter;

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
      }),
  );
  // Get unique clusters for filter
  let uniqueClusters = $derived([
    ...new Set(nodes.map((n) => n.cluster).filter(Boolean)),
  ]);
  // Stats
  let nodeStats = $derived({
    total: nodes.length,
    healthy: nodes.filter(
      (n) => getNodeStatus(n.id).toLowerCase() === "healthy",
    ).length,
    degraded: nodes.filter(
      (n) => getNodeStatus(n.id).toLowerCase() === "degraded",
    ).length,
    unhealthy: nodes.filter(
      (n) => getNodeStatus(n.id).toLowerCase() === "unhealthy",
    ).length,
    unknown: nodes.filter(
      (n) => getNodeStatus(n.id).toLowerCase() === "unknown",
    ).length,
  });
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
        {#each Array.from({ length: 4 }) as _, index (index)}
          <div class="bg-tokyo-night-surface rounded-lg p-4">
            <SkeletonLoader type="text" className="w-16 mb-2" />
            <SkeletonLoader type="text-lg" className="w-8" />
          </div>
        {/each}
      </div>

      <div class="bg-tokyo-night-surface rounded-lg p-6">
        <SkeletonLoader type="text-lg" className="w-32 mb-4" />
        <div class="grid grid-cols-1 lg:grid-cols-2 xl:grid-cols-3 gap-4">
          {#each Array.from({ length: 6 }) as _, index (index)}
            <LoadingCard variant="infrastructure" />
          {/each}
        </div>
      </div>
    {:else}
      <!-- Stats Cards -->
      <div class="grid grid-cols-2 md:grid-cols-4 gap-4 mb-8">
        <Card class="border border-tokyo-night-selection">
          <CardContent class="p-4">
            <div class="text-2xl font-bold text-tokyo-night-blue mb-1">
              {nodeStats.total}
            </div>
            <CardDescription>Total Nodes</CardDescription>
          </CardContent>
        </Card>
        <Card class="border border-tokyo-night-selection">
          <CardContent class="p-4">
            <div class="text-2xl font-bold text-green-500 mb-1">
              {nodeStats.healthy}
            </div>
            <CardDescription>Healthy</CardDescription>
          </CardContent>
        </Card>
        <Card class="border border-tokyo-night-selection">
          <CardContent class="p-4">
            <div class="text-2xl font-bold text-yellow-500 mb-1">
              {nodeStats.degraded}
            </div>
            <CardDescription>Degraded</CardDescription>
          </CardContent>
        </Card>
        <Card class="border border-tokyo-night-selection">
          <CardContent class="p-4">
            <div class="text-2xl font-bold text-red-500 mb-1">
              {nodeStats.unhealthy + nodeStats.unknown}
            </div>
            <CardDescription>Issues</CardDescription>
          </CardContent>
        </Card>
      </div>

      <!-- Filters and Search -->
      {@const SvelteComponent = getIconComponent("Search")}
      {@const SvelteComponent_1 = getIconComponent(
        sortOrder === "asc" ? "ArrowUp" : "ArrowDown",
      )}
      <div class="bg-tokyo-night-surface rounded-lg p-6 mb-8">
        <div class="grid grid-cols-1 md:grid-cols-5 gap-4">
          <!-- Search -->
          <div class="md:col-span-2">
            <label
              for="node-search"
              class="block text-sm font-medium text-tokyo-night-comment mb-1"
            >
              Search Nodes
            </label>
            <div class="relative">
              <Input
                id="node-search"
                type="text"
                bind:value={searchTerm}
                placeholder="Name, ID, or IP address..."
                class="pl-10"
              />
              <SvelteComponent
                class="absolute left-3 top-2.5 w-4 h-4 text-tokyo-night-comment"
              />
            </div>
          </div>

          <!-- Status Filter -->
          <div>
            <label
              for="status-filter"
              class="block text-sm font-medium text-tokyo-night-comment mb-1"
            >
              Status
            </label>
            <Select bind:value={statusFilter}>
              <SelectTrigger class="w-full" id="status-filter">
                {statusFilterLabels[statusFilter] ?? "All Status"}
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">All Status</SelectItem>
                <SelectItem value="healthy">Healthy</SelectItem>
                <SelectItem value="degraded">Degraded</SelectItem>
                <SelectItem value="unhealthy">Unhealthy</SelectItem>
                <SelectItem value="unknown">Unknown</SelectItem>
              </SelectContent>
            </Select>
          </div>

          <!-- Cluster Filter -->
          <div>
            <label
              for="cluster-filter"
              class="block text-sm font-medium text-tokyo-night-comment mb-1"
            >
              Cluster
            </label>
            <Select bind:value={clusterFilter}>
              <SelectTrigger class="w-full" id="cluster-filter">
                {clusterFilter === "all" ? "All Clusters" : clusterFilter}
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">All Clusters</SelectItem>
                {#each uniqueClusters as cluster (cluster)}
                  <SelectItem value={cluster}>{cluster}</SelectItem>
                {/each}
              </SelectContent>
            </Select>
          </div>

          <!-- Sort -->
          <div>
            <label
              for="sort-select"
              class="block text-sm font-medium text-tokyo-night-comment mb-1"
            >
              Sort By
            </label>
            <div class="flex gap-1">
              <select
                id="sort-select"
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
                onclick={() =>
                  (sortOrder = sortOrder === "asc" ? "desc" : "asc")}
                class="px-2 py-2 bg-tokyo-night-bg border border-tokyo-night-selection rounded-lg hover:bg-tokyo-night-selection transition-colors"
                title={sortOrder === "asc"
                  ? "Sort descending"
                  : "Sort ascending"}
              >
                <SvelteComponent_1 class="w-4 h-4" />
              </button>
            </div>
          </div>
        </div>
      </div>

      <!-- Nodes Grid -->
      {@const SvelteComponent_2 = getIconComponent("RefreshCw")}
      <Card class="bg-tokyo-night-surface">
        <CardHeader class="pb-0">
          <div class="flex items-center justify-between">
            <CardTitle class="text-tokyo-night-cyan text-xl">
              Nodes ({filteredNodes.length})
            </CardTitle>
            <Button class="gap-2" on:click={loadNodeData} disabled={loading}>
              <SvelteComponent_2 class="w-4 h-4" />
              Refresh
            </Button>
          </div>
        </CardHeader>
        <CardContent class="mt-6">
          {#if filteredNodes.length === 0}
            <EmptyState
              title={nodes.length === 0
                ? "No nodes found"
                : "No matching nodes"}
              description={nodes.length === 0
                ? "Infrastructure nodes will appear here when deployed"
                : "Try adjusting your filters or search criteria"}
              size="md"
              showAction={false}
            >
              {@const SvelteComponent_3 = getIconComponent("Server")}
              <SvelteComponent_3
                class="w-12 h-12 text-tokyo-night-fg-dark mx-auto mb-4"
              />
            </EmptyState>
          {:else}
            <div class="grid grid-cols-1 lg:grid-cols-2 xl:grid-cols-3 gap-6">
              {#each filteredNodes as node (node.id ?? node.name ?? node.ipAddress)}
                {@const SvelteComponent_4 = getIconComponent("Server")}
                {@const SvelteComponent_5 = getIconComponent(
                  getStatusIcon(getNodeStatus(node.id)),
                )}
                {@const SvelteComponent_6 = getIconComponent("Settings")}
                {@const SvelteComponent_7 = getIconComponent("MoreHorizontal")}
                <Card
                  class="bg-tokyo-night-bg border border-tokyo-night-selection hover:border-tokyo-night-blue/50 transition-colors"
                >
                  <CardContent class="p-6 space-y-4">
                    <!-- Node Header -->
                    <div class="flex items-center justify-between mb-4">
                      <div class="flex items-center gap-3">
                        <div
                          class="w-10 h-10 bg-tokyo-night-surface rounded-lg flex items-center justify-center"
                        >
                          <SvelteComponent_4
                            class="w-5 h-5 text-tokyo-night-blue"
                          />
                        </div>
                        <div>
                          <h3 class="font-semibold text-tokyo-night-fg">
                            {node.name}
                          </h3>
                          <p class="text-sm text-tokyo-night-comment">
                            {node.type}
                          </p>
                        </div>
                      </div>
                      <div class="flex items-center gap-2">
                        <span
                          class="px-2 py-1 text-xs rounded-full {getStatusColor(
                            getNodeStatus(node.id),
                          )} flex items-center gap-1"
                        >
                          <SvelteComponent_5 class="w-3 h-3" />
                          {getNodeStatus(node.id)}
                        </span>
                      </div>
                    </div>

                    <!-- Node Details -->
                    <div class="space-y-3 mb-4">
                      <div class="grid grid-cols-2 gap-4 text-sm">
                        <div>
                          <p class="text-tokyo-night-comment">Region</p>
                          <p class="font-medium">{node.region || "N/A"}</p>
                        </div>
                        <div>
                          <p class="text-tokyo-night-comment">Cluster</p>
                          <p class="font-medium">{node.cluster || "N/A"}</p>
                        </div>
                        <div>
                          <p class="text-tokyo-night-comment">IP Address</p>
                          <p class="font-mono text-xs">
                            {node.ipAddress || "N/A"}
                          </p>
                        </div>
                        <div>
                          <p class="text-tokyo-night-comment">Last Seen</p>
                          <p class="text-xs">
                            {new Date(node.lastSeen).toLocaleString()}
                          </p>
                        </div>
                      </div>

                      {#if systemHealth[node.id]}
                        <!-- Real-time Metrics -->
                        <div class="pt-3 border-t border-tokyo-night-selection">
                          <div class="grid grid-cols-3 gap-4 text-sm">
                            <div>
                              <p class="text-tokyo-night-comment">CPU</p>
                              <p class="font-medium">
                                {formatCpuUsage(node.id)}
                              </p>
                            </div>
                            <div>
                              <p class="text-tokyo-night-comment">Memory</p>
                              <p class="font-medium">
                                {formatMemoryUsage(node.id)}
                              </p>
                            </div>
                            <div>
                              <p class="text-tokyo-night-comment">Health</p>
                              <p class="font-medium">
                                {getNodeHealthScore(node.id)}%
                              </p>
                            </div>
                          </div>

                          {#if systemHealth[node.id].diskUsage !== undefined}
                            <div class="mt-2 text-xs text-tokyo-night-comment">
                              Disk: {Math.round(
                                systemHealth[node.id].diskUsage,
                              )}% • Updated: {systemHealth[
                                node.id
                              ].timestamp?.toLocaleTimeString()}
                            </div>
                          {/if}
                        </div>
                      {/if}
                    </div>

                    <!-- Node Actions -->
                    <div class="flex gap-2">
                      <Button class="flex-1" variant="outline">
                        View Details
                      </Button>
                      <Button variant="outline" size="icon">
                        <SvelteComponent_6 class="w-4 h-4" />
                      </Button>
                      <Button variant="outline" size="icon">
                        <SvelteComponent_7 class="w-4 h-4" />
                      </Button>
                    </div>
                  </CardContent>
                </Card>
              {/each}
            </div>
          {/if}
        </CardContent>
      </Card>
    {/if}
  </div>
</div>

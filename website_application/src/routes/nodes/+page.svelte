<script lang="ts">
  import { onMount, onDestroy, untrack } from "svelte";
  import { auth } from "$lib/stores/auth";
  import { GetNodesConnectionStore, SystemHealthStore } from "$houdini";
  import type { SystemHealth$result } from "$houdini";
  import { toast } from "$lib/stores/toast.js";
  import LoadingCard from "$lib/components/LoadingCard.svelte";
  import SkeletonLoader from "$lib/components/SkeletonLoader.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import { getIconComponent } from "$lib/iconUtils";
  import { Input } from "$lib/components/ui/input";
  import { Button } from "$lib/components/ui/button";
  import {
    Select,
    SelectTrigger,
    SelectContent,
    SelectItem,
  } from "$lib/components/ui/select";

  // Icons
  const ServerIcon = getIconComponent("Server");
  const HardDriveIcon = getIconComponent("HardDrive");
  const SearchIcon = getIconComponent("Search");
  const RefreshCwIcon = getIconComponent("RefreshCw");

  // Houdini stores
  const nodesStore = new GetNodesConnectionStore();
  const systemHealthSub = new SystemHealthStore();

  // Pagination state
  let loadingMore = $state(false);

  // System health type from subscription
  type SystemHealthEvent = NonNullable<SystemHealth$result["systemHealth"]>;
  type SystemHealthWithTimestamp = Omit<SystemHealthEvent, 'timestamp'> & { timestamp: Date };

  let isAuthenticated = false;

  // Derived state from Houdini stores
  let loading = $derived($nodesStore.fetching);
  let nodes = $derived(
    $nodesStore.data?.nodesConnection?.edges?.map(e => e.node) ?? []
  );
  let hasMoreNodes = $derived(
    $nodesStore.data?.nodesConnection?.pageInfo?.hasNextPage ?? false
  );
  let totalNodeCount = $derived(
    $nodesStore.data?.nodesConnection?.totalCount ?? 0
  );
  let systemHealth = $state<Record<string, SystemHealthWithTimestamp>>({});

  // Filters and search
  let searchTerm = $state("");
  let statusFilter = $state("all");
  let clusterFilter = $state("all");
  let sortBy = $state<"nodeName" | "clusterId" | "region" | "status" | "health">("nodeName");
  let sortOrder = $state<"asc" | "desc">("asc");

  const statusFilterLabels: Record<string, string> = {
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
    systemHealthSub.listen();
  });

  onDestroy(() => {
    systemHealthSub.unlisten();
  });

  async function loadNodeData() {
    try {
      await nodesStore.fetch();

      if ($nodesStore.errors?.length) {
        console.error("Failed to load node data:", $nodesStore.errors);
        toast.error("Failed to load node data. Please refresh the page.");
      }
    } catch (error) {
      console.error("Failed to load node data:", error);
      toast.error("Failed to load node data. Please refresh the page.");
    }
  }

  async function loadMoreNodes() {
    if (!hasMoreNodes || loadingMore) return;

    loadingMore = true;
    try {
      await nodesStore.loadNextPage();
    } catch (err) {
      console.error("Failed to load more nodes:", err);
      toast.error("Failed to load more nodes");
    } finally {
      loadingMore = false;
    }
  }

  // Effect to handle system health subscription errors
  $effect(() => {
    const errors = $systemHealthSub.errors;
    if (errors?.length) {
      console.warn("System health subscription error:", errors);
      // Non-fatal: page still works, just without real-time updates
    }
  });

  // Effect to handle system health subscription updates
  // Use untrack to prevent effect loops when mutating state
  $effect(() => {
    const healthData = $systemHealthSub.data?.systemHealth;
    if (healthData) {
      untrack(() => {
        const nodeId = healthData.node;
        if (nodeId) {
          systemHealth[nodeId] = {
            ...healthData,
            timestamp: new Date(healthData.timestamp),
          };
          systemHealth = { ...systemHealth };
        }
      });
    }
  });

  // Helper functions
  function getNodeStatus(nodeId: string) {
    // First check real-time subscription data
    const health = systemHealth[nodeId];
    if (health) return health.status;

    // Fallback to database status from the node record
    const node = nodes.find((n) => n.id === nodeId);
    if (node?.status) {
      // Normalize database status to match subscription format
      const dbStatus = node.status.toLowerCase();
      if (dbStatus === "active" || dbStatus === "online") return "HEALTHY";
      if (dbStatus === "degraded") return "DEGRADED";
      if (dbStatus === "inactive" || dbStatus === "offline") return "UNHEALTHY";
      return node.status.toUpperCase();
    }

    return "UNKNOWN";
  }

  function getNodeHealthScore(nodeId: string) {
    const health = systemHealth[nodeId];
    if (!health) return 0;
    return health.isHealthy ? 100 : 0;
  }

  function formatCpuUsage(nodeId: string) {
    const health = systemHealth[nodeId];
    if (!health) return "0%";
    return `${(health.cpuTenths / 10).toFixed(1)}%`;
  }

  function formatMemoryUsage(nodeId: string) {
    const health = systemHealth[nodeId];
    if (!health || !health.ramMax) return "0%";
    const percent = (health.ramCurrent! / health.ramMax) * 100;
    return `${Math.round(percent)}%`;
  }

  function getStatusColor(status: string | undefined) {
    switch (status?.toLowerCase()) {
      case "healthy":
        return "text-success bg-success/20";
      case "degraded":
        return "text-warning bg-warning/20";
      case "unhealthy":
        return "text-error bg-error/20";
      default:
        return "text-muted-foreground bg-muted";
    }
  }

  function getStatusIcon(status: string | undefined) {
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
          node.nodeName.toLowerCase().includes(searchTerm.toLowerCase()) ||
          node.id.toLowerCase().includes(searchTerm.toLowerCase()) ||
          (node.externalIp && node.externalIp.includes(searchTerm));

        const matchesStatus =
          statusFilter === "all" ||
          getNodeStatus(node.id).toLowerCase() === statusFilter;

        const matchesCluster =
          clusterFilter === "all" || node.clusterId === clusterFilter;

        return matchesSearch && matchesStatus && matchesCluster;
      })
      .sort((a, b) => {
        let aVal: string | number | undefined, bVal: string | number | undefined;

        switch (sortBy) {
          case "nodeName":
            aVal = a.nodeName;
            bVal = b.nodeName;
            break;
          case "status":
            aVal = getNodeStatus(a.id);
            bVal = getNodeStatus(b.id);
            break;
          case "health":
            aVal = getNodeHealthScore(a.id);
            bVal = getNodeHealthScore(b.id);
            break;
          case "clusterId":
            aVal = a.clusterId || "";
            bVal = b.clusterId || "";
            break;
          case "region":
            aVal = a.region || "";
            bVal = b.region || "";
            break;
        }

        if (aVal === undefined) aVal = "";
        if (bVal === undefined) bVal = "";

        if (sortOrder === "asc") {
          return aVal < bVal ? -1 : aVal > bVal ? 1 : 0;
        } else {
          return aVal > bVal ? -1 : aVal < bVal ? 1 : 0;
        }
      }),
  );

  // Get unique clusters for filter
  let uniqueClusters = $derived([
    ...new Set(nodes.map((n) => n.clusterId).filter(Boolean)),
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

  // Sort icon
  let SortIcon = $derived(getIconComponent(sortOrder === "asc" ? "ArrowUp" : "ArrowDown"));
</script>

<svelte:head>
  <title>Node Management - FrameWorks</title>
</svelte:head>

<div class="h-full flex flex-col">
  <!-- Fixed Page Header -->
  <div class="px-4 sm:px-6 lg:px-8 py-4 border-b border-border shrink-0">
    <div class="flex items-center gap-3">
      <HardDriveIcon class="w-5 h-5 text-primary" />
      <div>
        <h1 class="text-xl font-bold text-foreground">Nodes</h1>
        <p class="text-sm text-muted-foreground">
          Monitor and manage your Edge nodes worldwide
        </p>
      </div>
    </div>
  </div>

  <!-- Scrollable Content -->
  <div class="flex-1 overflow-y-auto">
  {#if loading}
    <!-- Loading state -->
    <div class="dashboard-grid">
      <div class="slab col-span-full">
        <div class="slab-header">
          <SkeletonLoader type="text-lg" class="w-32" />
        </div>
        <div class="slab-body--padded">
          <div class="grid grid-cols-2 md:grid-cols-4 gap-4 mb-6">
            {#each Array.from({ length: 4 }) as _, index (index)}
              <div class="p-4 border border-border/50">
                <SkeletonLoader type="text" class="w-16 mb-2" />
                <SkeletonLoader type="text-lg" class="w-8" />
              </div>
            {/each}
          </div>
          <div class="grid grid-cols-1 lg:grid-cols-2 xl:grid-cols-3 gap-4">
            {#each Array.from({ length: 6 }) as _, index (index)}
              <LoadingCard variant="infrastructure" />
            {/each}
          </div>
        </div>
      </div>
    </div>
  {:else}
    <div class="dashboard-grid">
      <!-- Stats Cards -->
      <div class="slab col-span-full">
        <div class="slab-header">
          <div class="flex items-center gap-2">
            <ServerIcon class="w-4 h-4 text-info" />
            <h3>Node Overview</h3>
          </div>
        </div>
        <div class="slab-body--padded">
          <div class="grid grid-cols-2 md:grid-cols-4 gap-4">
            <div class="p-4 border border-border/50">
              <div class="text-2xl font-bold text-primary mb-1">
                {nodeStats.total}
              </div>
              <p class="text-sm text-muted-foreground">Total Nodes</p>
            </div>
            <div class="p-4 border border-border/50">
              <div class="text-2xl font-bold text-success mb-1">
                {nodeStats.healthy}
              </div>
              <p class="text-sm text-muted-foreground">Healthy</p>
            </div>
            <div class="p-4 border border-border/50">
              <div class="text-2xl font-bold text-warning mb-1">
                {nodeStats.degraded}
              </div>
              <p class="text-sm text-muted-foreground">Degraded</p>
            </div>
            <div class="p-4 border border-border/50">
              <div class="text-2xl font-bold text-destructive mb-1">
                {nodeStats.unhealthy + nodeStats.unknown}
              </div>
              <p class="text-sm text-muted-foreground">Issues</p>
            </div>
          </div>
        </div>
      </div>

      <!-- Filters and Search -->
      <div class="slab col-span-full">
        <div class="slab-header">
          <div class="flex items-center gap-2">
            <SearchIcon class="w-4 h-4 text-muted-foreground" />
            <h3>Filters</h3>
          </div>
        </div>
        <div class="slab-body--padded">
          <div class="grid grid-cols-1 md:grid-cols-5 gap-4">
            <!-- Search -->
            <div class="md:col-span-2">
              <label
                for="node-search"
                class="block text-sm font-medium text-muted-foreground mb-1"
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
                <SearchIcon
                  class="absolute left-3 top-2.5 w-4 h-4 text-muted-foreground"
                />
              </div>
            </div>

            <!-- Status Filter -->
            <div>
              <label
                for="status-filter"
                class="block text-sm font-medium text-muted-foreground mb-1"
              >
                Status
              </label>
              <Select bind:value={statusFilter} type="single">
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
                class="block text-sm font-medium text-muted-foreground mb-1"
              >
                Cluster
              </label>
              <Select bind:value={clusterFilter} type="single">
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
                class="block text-sm font-medium text-muted-foreground mb-1"
              >
                Sort By
              </label>
              <div class="flex gap-1">
                <select
                  id="sort-select"
                  bind:value={sortBy}
                  class="flex-1 px-3 py-2 bg-background border border-border/50 text-sm focus:border-primary focus:ring-1 focus:ring-primary"
                >
                  <option value="nodeName">Name</option>
                  <option value="status">Status</option>
                  <option value="health">Health</option>
                  <option value="clusterId">Cluster</option>
                  <option value="region">Region</option>
                </select>
                <button
                  onclick={() =>
                    (sortOrder = sortOrder === "asc" ? "desc" : "asc")}
                  class="px-2 py-2 bg-background border border-border/50 hover:bg-muted/50 transition-colors"
                  title={sortOrder === "asc"
                    ? "Sort descending"
                    : "Sort ascending"}
                >
                  <SortIcon class="w-4 h-4" />
                </button>
              </div>
            </div>
          </div>
        </div>
      </div>

      <!-- Nodes Grid -->
      <div class="slab col-span-full">
        <div class="slab-header">
          <div class="flex items-center justify-between w-full">
            <div class="flex items-center gap-2">
              <ServerIcon class="w-4 h-4 text-info" />
              <h3>Nodes ({filteredNodes.length}{#if hasMoreNodes} of {totalNodeCount}+{:else if totalNodeCount > 0} of {totalNodeCount}{/if})</h3>
            </div>
            <Button class="gap-2" onclick={loadNodeData} disabled={loading}>
              <RefreshCwIcon class="w-4 h-4" />
              Refresh
            </Button>
          </div>
        </div>
        <div class="slab-body--padded">
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
              <ServerIcon class="w-6 h-6 text-muted-foreground mx-auto mb-4" />
            </EmptyState>
          {:else}
            <div class="grid grid-cols-1 lg:grid-cols-2 xl:grid-cols-3 gap-4">
              {#each filteredNodes as node (node.id ?? node.nodeName ?? node.externalIp)}
                {@const StatusIcon = getIconComponent(getStatusIcon(getNodeStatus(node.id)))}
                <div class="border border-border/50 p-4">
                  <!-- Node Header -->
                  <div class="flex items-center justify-between mb-4">
                    <div class="flex items-center gap-3">
                      <div
                        class="w-10 h-10 border border-border/50 flex items-center justify-center"
                      >
                        <ServerIcon class="w-5 h-5 text-primary" />
                      </div>
                      <div>
                        <h3 class="font-semibold text-foreground">
                          {node.nodeName}
                        </h3>
                        <p class="text-sm text-muted-foreground">
                          {node.nodeType}
                        </p>
                      </div>
                    </div>
                    <div class="flex items-center gap-2">
                      <span
                        class="px-2 py-1 text-xs {getStatusColor(getNodeStatus(node.id))} flex items-center gap-1"
                      >
                        <StatusIcon class="w-3 h-3" />
                        {getNodeStatus(node.id)}
                      </span>
                    </div>
                  </div>

                  <!-- Node Details -->
                  <div class="space-y-3">
                    <div class="grid grid-cols-2 gap-4 text-sm">
                      <div>
                        <p class="text-muted-foreground">Region</p>
                        <p class="font-medium">{node.region || "N/A"}</p>
                      </div>
                      <div>
                        <p class="text-muted-foreground">Cluster</p>
                        <p class="font-medium">{node.clusterId || "N/A"}</p>
                      </div>
                      <div>
                        <p class="text-muted-foreground">IP Address</p>
                        <p class="font-mono text-xs">
                          {node.externalIp || "N/A"}
                        </p>
                      </div>
                      <div>
                        <p class="text-muted-foreground">Last Seen</p>
                        <p class="text-xs">
                          {node.lastHeartbeat ? new Date(node.lastHeartbeat).toLocaleString() : "N/A"}
                        </p>
                      </div>
                    </div>

                    {#if systemHealth[node.id]}
                      <!-- Real-time Metrics -->
                      <div class="pt-3 border-t border-border">
                        <div class="grid grid-cols-3 gap-4 text-sm">
                          <div>
                            <p class="text-muted-foreground">CPU</p>
                            <p class="font-medium">
                              {formatCpuUsage(node.id)}
                            </p>
                          </div>
                          <div>
                            <p class="text-muted-foreground">Memory</p>
                            <p class="font-medium">
                              {formatMemoryUsage(node.id)}
                            </p>
                          </div>
                          <div>
                            <p class="text-muted-foreground">Health</p>
                            <p class="font-medium">
                              {getNodeHealthScore(node.id)}%
                            </p>
                          </div>
                        </div>

                        {#if systemHealth[node.id].diskTotalBytes}
                          <div class="mt-2 text-xs text-muted-foreground">
                            Disk: {Math.round(
                              (systemHealth[node.id].diskUsedBytes! / systemHealth[node.id].diskTotalBytes!) * 100,
                            )}% • Updated: {systemHealth[
                              node.id
                            ].timestamp?.toLocaleTimeString()}
                          </div>
                        {/if}
                      </div>
                    {/if}
                  </div>
                </div>
              {/each}
            </div>

            <!-- Load More Nodes -->
            {#if hasMoreNodes}
              <div class="flex justify-center py-6">
                <Button
                  variant="outline"
                  onclick={loadMoreNodes}
                  disabled={loadingMore}
                >
                  {#if loadingMore}
                    Loading...
                  {:else}
                    Load More Nodes
                  {/if}
                </Button>
              </div>
            {/if}
          {/if}
        </div>
      </div>
    </div>
  {/if}
  </div>
</div>

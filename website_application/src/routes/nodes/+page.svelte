<script lang="ts">
  import { onMount, onDestroy, untrack } from "svelte";
  import { get } from "svelte/store";
  import { auth } from "$lib/stores/auth";
  import { fragment, GetNodesConnectionStore, GetNodePerformance5mStore, SystemHealthStore, NodeCoreFieldsStore, PageInfoFieldsStore } from "$houdini";
  import type { SystemHealth$result } from "$houdini";
  import { toast } from "$lib/stores/toast.js";
  import LoadingCard from "$lib/components/LoadingCard.svelte";
  import SkeletonLoader from "$lib/components/SkeletonLoader.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import NodeCard from "$lib/components/infrastructure/NodeCard.svelte";
  import { GridSeam, SectionDivider } from "$lib/components/layout";
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
  const CpuIcon = getIconComponent("Cpu");
  const MemoryStickIcon = getIconComponent("MemoryStick");
  const ActivityIcon = getIconComponent("Activity");
  const RadioIcon = getIconComponent("Radio");

  // Houdini stores
  const nodesStore = new GetNodesConnectionStore();
  const nodePerformanceStore = new GetNodePerformance5mStore();
  const systemHealthSub = new SystemHealthStore();

  // Fragment stores for unmasking nested data
  const nodeCoreStore = new NodeCoreFieldsStore();
  const pageInfoStore = new PageInfoFieldsStore();

  // Pagination state
  let loadingMore = $state(false);

  // System health type from subscription
  type SystemHealthEvent = NonNullable<SystemHealth$result["systemHealth"]>;
  // Match structure expected by NodeCard
  interface SystemHealthData {
    event: SystemHealthEvent;
    ts: Date;
  }

  let isAuthenticated = false;

  // Derived state from Houdini stores
  let loading = $derived($nodesStore.fetching || $nodePerformanceStore.fetching);

  // Get masked nodes from edges
  let maskedNodes = $derived(
    $nodesStore.data?.nodesConnection?.edges?.map(e => e.node) ?? []
  );

  // Unmask nodes with fragment() and get() pattern
  let nodes = $derived(
    maskedNodes.map(node => get(fragment(node, nodeCoreStore)))
  );
  // Unmask pageInfo to access hasNextPage
  let pageInfo = $derived.by(() => {
    const masked = $nodesStore.data?.nodesConnection?.pageInfo;
    return masked ? get(fragment(masked, pageInfoStore)) : null;
  });
  let hasMoreNodes = $derived(pageInfo?.hasNextPage ?? false);
  let totalNodeCount = $derived(
    $nodesStore.data?.nodesConnection?.totalCount ?? 0
  );
  let systemHealth = $state<Record<string, SystemHealthData>>({});

  // Aggregate performance stats from 5-minute node performance data
  let performanceStats = $derived.by(() => {
    const edges = $nodePerformanceStore.data?.nodePerformance5mConnection?.edges ?? [];
    if (edges.length === 0) {
      return { avgCpu: 0, maxCpu: 0, avgMemory: 0, maxMemory: 0, totalBandwidth: 0, avgStreams: 0, maxStreams: 0, dataPoints: 0 };
    }

    let sumCpu = 0, sumMemory = 0, sumStreams = 0;
    let maxCpu = 0, maxMemory = 0, maxStreams = 0;
    let totalBandwidth = 0;

    for (const edge of edges) {
      const n = edge.node;
      sumCpu += n.avgCpu ?? 0;
      sumMemory += n.avgMemory ?? 0;
      sumStreams += n.avgStreams ?? 0;
      maxCpu = Math.max(maxCpu, n.maxCpu ?? 0);
      maxMemory = Math.max(maxMemory, n.maxMemory ?? 0);
      maxStreams = Math.max(maxStreams, n.maxStreams ?? 0);
      totalBandwidth += n.totalBandwidth ?? 0;
    }

    const count = edges.length;
    return {
      avgCpu: count > 0 ? sumCpu / count : 0,
      maxCpu,
      avgMemory: count > 0 ? sumMemory / count : 0,
      maxMemory,
      totalBandwidth,
      avgStreams: count > 0 ? sumStreams / count : 0,
      maxStreams,
      dataPoints: count,
    };
  });

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
      // Fetch nodes and performance data (last 24 hours)
      const now = new Date();
      const dayAgo = new Date(now.getTime() - 24 * 60 * 60 * 1000);
      const timeRange = { start: dayAgo.toISOString(), end: now.toISOString() };

      await Promise.all([
        nodesStore.fetch(),
        nodePerformanceStore.fetch({ variables: { timeRange, first: 300 } }).catch(() => null),
      ]);

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
  // Note: nodeId is the database UUID (enriched by Foghorn), node is the logical name
  $effect(() => {
    const healthData = $systemHealthSub.data?.systemHealth;
    if (healthData) {
      untrack(() => {
        // Use nodeId (database UUID) directly if available, fallback to name lookup
        const nodeId = healthData.nodeId ?? (() => {
          const matchingNode = nodes.find((n) => n.nodeName === healthData.node);
          return matchingNode?.id ?? healthData.node;
        })();

        if (nodeId) {
          systemHealth[nodeId] = {
            event: healthData,
            ts: new Date(healthData.timestamp),
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
    if (health) return health.event.status;

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
    
    const event = health.event;
    const cpuPercent = event.cpuTenths / 10;
    const memPercent = event.ramMax ? (event.ramCurrent! / event.ramMax) * 100 : 0;
    const shmPercent = event.shmTotalBytes ? (event.shmUsedBytes! / event.shmTotalBytes) * 100 : 0;
    
    const cpuScore = Math.max(0, 100 - cpuPercent);
    const memScore = Math.max(0, 100 - memPercent);
    const shmScore = Math.max(0, 100 - shmPercent);
    
    return Math.round((cpuScore + memScore + shmScore) / 3);
  }

  function formatCpuUsage(nodeId: string) {
    const health = systemHealth[nodeId];
    if (!health) return "0%";
    return `${(health.event.cpuTenths / 10).toFixed(1)}%`;
  }

  function formatMemoryUsage(nodeId: string) {
    const health = systemHealth[nodeId];
    if (!health || !health.event.ramMax) return "0%";
    const percent = (health.event.ramCurrent! / health.event.ramMax) * 100;
    return `${Math.round(percent)}%`;
  }

  function formatDiskUsage(nodeId: string) {
    const health = systemHealth[nodeId];
    if (!health || !health.event.diskTotalBytes) return "0%";
    const percent = (health.event.diskUsedBytes! / health.event.diskTotalBytes) * 100;
    return `${Math.round(percent)}%`;
  }

  function getStatusBadgeClass(status: string | null | undefined) {
    switch (status?.toLowerCase()) {
      case "healthy":
        return "border-success/40 bg-success/10 text-success";
      case "degraded":
        return "border-warning/40 bg-warning/10 text-warning";
      case "unhealthy":
        return "border-rose-500/40 bg-rose-500/10 text-rose-300";
      default:
        return "border-muted-foreground/40 bg-muted-foreground/10 text-muted-foreground";
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
  <div class="px-4 sm:px-6 lg:px-8 py-4 border-b border-[hsl(var(--tn-fg-gutter)/0.3)] shrink-0">
    <div class="flex items-center gap-3">
      <HardDriveIcon class="w-5 h-5 text-primary" />
      <div>
        <h1 class="text-xl font-bold text-foreground">Nodes</h1>
        <p class="text-sm text-muted-foreground">
          Manage self-hosted nodes and subscribed cluster capacity
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
    <div>
      <!-- Stats Cards -->
      <GridSeam cols={4} stack="md" surface="panel" flush={true}>
        <div class="p-6 text-center">
          <div class="text-3xl font-bold text-primary mb-1">
            {nodeStats.total}
          </div>
          <p class="text-sm text-muted-foreground">Total Nodes</p>
        </div>
        <div class="p-6 text-center">
          <div class="text-3xl font-bold text-success mb-1">
            {nodeStats.healthy}
          </div>
          <p class="text-sm text-muted-foreground">Healthy</p>
        </div>
        <div class="p-6 text-center">
          <div class="text-3xl font-bold text-warning mb-1">
            {nodeStats.degraded}
          </div>
          <p class="text-sm text-muted-foreground">Degraded</p>
        </div>
        <div class="p-6 text-center">
          <div class="text-3xl font-bold text-destructive mb-1">
            {nodeStats.unhealthy + nodeStats.unknown}
          </div>
          <p class="text-sm text-muted-foreground">Issues</p>
        </div>
      </GridSeam>

      <!-- Performance Overview (from 5-minute aggregates) -->
      {#if performanceStats.dataPoints > 0}
        <div class="slab mx-4 sm:mx-6 lg:mx-8 mt-4">
          <div class="slab-header">
            <div class="flex items-center gap-2">
              <ActivityIcon class="w-4 h-4 text-primary" />
              <h3>Platform Performance (24h)</h3>
            </div>
            <span class="text-xs text-muted-foreground">{performanceStats.dataPoints} data points</span>
          </div>
          <div class="slab-body--padded">
            <div class="grid grid-cols-2 md:grid-cols-4 gap-4">
              <div class="p-4 border border-border/30 bg-muted/10 text-center">
                <CpuIcon class="w-5 h-5 text-primary mx-auto mb-2" />
                <div class="text-xl font-bold text-primary">
                  {performanceStats.avgCpu.toFixed(1)}%
                </div>
                <p class="text-xs text-muted-foreground">Avg CPU</p>
                <p class="text-xs text-muted-foreground mt-1">
                  Peak: {performanceStats.maxCpu.toFixed(1)}%
                </p>
              </div>
              <div class="p-4 border border-border/30 bg-muted/10 text-center">
                <MemoryStickIcon class="w-5 h-5 text-info mx-auto mb-2" />
                <div class="text-xl font-bold text-info">
                  {performanceStats.avgMemory.toFixed(1)}%
                </div>
                <p class="text-xs text-muted-foreground">Avg Memory</p>
                <p class="text-xs text-muted-foreground mt-1">
                  Peak: {performanceStats.maxMemory.toFixed(1)}%
                </p>
              </div>
              <div class="p-4 border border-border/30 bg-muted/10 text-center">
                <RadioIcon class="w-5 h-5 text-success mx-auto mb-2" />
                <div class="text-xl font-bold text-success">
                  {(performanceStats.totalBandwidth / (1024 * 1024 * 1024)).toFixed(2)} GB
                </div>
                <p class="text-xs text-muted-foreground">Total Bandwidth</p>
              </div>
              <div class="p-4 border border-border/30 bg-muted/10 text-center">
                <ServerIcon class="w-5 h-5 text-accent-purple mx-auto mb-2" />
                <div class="text-xl font-bold text-accent-purple">
                  {performanceStats.avgStreams.toFixed(1)}
                </div>
                <p class="text-xs text-muted-foreground">Avg Streams</p>
                <p class="text-xs text-muted-foreground mt-1">
                  Peak: {performanceStats.maxStreams}
                </p>
              </div>
            </div>
          </div>
        </div>
      {/if}

      <SectionDivider class="my-8" />

      <div class="dashboard-grid border-t border-[hsl(var(--tn-fg-gutter)/0.3)]">
        <!-- Filters and Search -->
        <div class="slab col-span-full">
          <div class="slab-header">
            <div class="flex items-center gap-2">
              <SearchIcon class="w-4 h-4 text-muted-foreground" />
              <h3 class="font-semibold text-xs uppercase tracking-wide text-muted-foreground">Filters</h3>
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
                    class="flex-1 px-3 py-2 bg-background border border-border/50 text-sm focus:border-primary focus:ring-1 focus:ring-primary rounded-md"
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
                    class="px-2 py-2 bg-background border border-border/50 hover:bg-muted/50 transition-colors rounded-md"
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
            <div class="flex items-center gap-2">
              <ServerIcon class="w-4 h-4 text-info" />
              <h3 class="font-semibold text-xs uppercase tracking-wide text-muted-foreground">Nodes ({filteredNodes.length}{#if hasMoreNodes} of {totalNodeCount}+{:else if totalNodeCount > 0} of {totalNodeCount}{/if})</h3>
            </div>
          </div>
          <div class="slab-body--padded">
            {#if filteredNodes.length === 0}
              <EmptyState
                title={nodes.length === 0
                  ? "No nodes found"
                  : "No matching nodes"}
                description={nodes.length === 0
                  ? "Connect your own Edge nodes or subscribe to clusters to expand capacity."
                  : "Try adjusting your filters or search criteria"}
                size="md"
                showAction={false}
              >
                <ServerIcon class="w-6 h-6 text-muted-foreground mx-auto mb-4" />
              </EmptyState>
            {:else}
              <div class="grid grid-cols-1 lg:grid-cols-2 xl:grid-cols-3 gap-4">
                {#each filteredNodes as node (node.id ?? node.nodeName ?? node.externalIp)}
                  <NodeCard
                    {node}
                    {systemHealth}
                    {getNodeStatus}
                    {getNodeHealthScore}
                    {formatCpuUsage}
                    {formatMemoryUsage}
                    {formatDiskUsage}
                    {getStatusBadgeClass}
                  />
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
    </div>
  {/if}
  </div>
</div>

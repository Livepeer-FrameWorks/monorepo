<script>
  import { onMount, onDestroy } from "svelte";
  import { auth } from "$lib/stores/auth";
  import { infrastructureService } from "$lib/graphql/services/infrastructure.js";
  import { performanceService } from "$lib/graphql/services/performance.js";
  import { toast } from "$lib/stores/toast.js";
  import LoadingCard from "$lib/components/LoadingCard.svelte";
  import SkeletonLoader from "$lib/components/SkeletonLoader.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import InfrastructureMetricCard from "$lib/components/shared/InfrastructureMetricCard.svelte";
  import NodePerformanceTable from "$lib/components/infrastructure/NodePerformanceTable.svelte";
  import ClusterCard from "$lib/components/infrastructure/ClusterCard.svelte";
  import NodeCard from "$lib/components/infrastructure/NodeCard.svelte";
  import {
    Card,
    CardContent,
    CardDescription,
    CardHeader,
    CardTitle,
  } from "$lib/components/ui/card";
  import { Badge } from "$lib/components/ui/badge";
  import { Band } from "$lib/components/layout";
  import { getIconComponent } from "$lib/iconUtils";
  /** @typedef { import('$lib/graphql/generated/apollo-helpers').Tenant } Tenant */
  /** @typedef { import('$lib/graphql/generated/apollo-helpers').Cluster } Cluster */
  /** @typedef { import('$lib/graphql/generated/apollo-helpers').Node } GqlNode */
  /** @typedef { import('$lib/graphql/generated/apollo-helpers').SystemHealthEvent } SystemHealthEvent */
  /** @typedef { import('zen-observable-ts').Subscription } ObservableSubscription */

  let isAuthenticated = false;
  let loading = $state(true);

  // Infrastructure data
  /** @type {Tenant | null} */
  let tenant = $state(null);
  /** @type {Cluster[]} */
  let clusters = $state([]);
  /** @type {GqlNode[]} */
  let nodes = $state([]);
  /** @type {ObservableSubscription | null} */
  let systemHealthSubscription = null;

  // Real-time system health data
  /** @type {Record<string, { event: SystemHealthEvent, ts: Date }>} */
  let systemHealth = $state({});

  // Performance analytics
  /** @type {Array<Record<string, unknown>>} */
  let nodePerformanceMetrics = $state([]);
  let platformMetrics = $state({
    totalActiveNodes: 0,
    avgCpuUsage: 0,
    avgMemoryUsage: 0,
    avgHealthScore: 0,
  });
  let platformSummary = $state({
    totalViewers: 0,
    avgViewers: 0,
    totalStreams: 0,
    avgConnectionQuality: 0,
    avgBufferHealth: 0,
    uniqueCountries: 0,
    uniqueCities: 0,
    nodesHealthy: 0,
    nodesDegraded: 0,
    nodesUnhealthy: 0,
  });

  // Time range for performance metrics (last 24 hours)
  const timeRange = {
    start: new Date(Date.now() - 24 * 60 * 60 * 1000).toISOString(),
    end: new Date().toISOString(),
  };

  // Subscribe to auth store
  auth.subscribe((authState) => {
    isAuthenticated = authState.isAuthenticated;
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
        infrastructureService.getNodes().catch(() => []),
      ]);

      tenant = tenantData;
      clusters = clustersData || [];
      nodes = nodesData || [];
    } catch (error) {
      console.error("Failed to load infrastructure data:", error);
      toast.error(
        "Failed to load infrastructure data. Please refresh the page.",
      );
    } finally {
      loading = false;
    }
  }

  async function loadPerformanceData() {
    try {
      const [metrics, summary] = await Promise.all([
        performanceService.getNodePerformanceMetrics(null, timeRange),
        performanceService.getPlatformSummary(timeRange),
      ]);

      nodePerformanceMetrics = metrics || [];
      platformSummary = summary || platformSummary;

      // Calculate platform metrics from node performance data
      if (nodePerformanceMetrics.length > 0) {
        const totalNodes = nodePerformanceMetrics.length;
        const avgCpu =
          nodePerformanceMetrics.reduce(
            (sum, node) => sum + (node.avgCpuUsage || 0),
            0,
          ) / totalNodes;
        const avgMem =
          nodePerformanceMetrics.reduce(
            (sum, node) => sum + (node.avgMemoryUsage || 0),
            0,
          ) / totalNodes;
        const avgHealth =
          nodePerformanceMetrics.reduce(
            (sum, node) => sum + (node.avgHealthScore || 0),
            0,
          ) / totalNodes;

        platformMetrics = {
          totalActiveNodes: totalNodes,
          avgCpuUsage: avgCpu,
          avgMemoryUsage: avgMem,
          avgHealthScore: avgHealth,
        };
      } else {
        // Use platform summary node counts if no metrics available
        platformMetrics = {
          totalActiveNodes:
            (platformSummary.nodesHealthy || 0) +
            (platformSummary.nodesDegraded || 0) +
            (platformSummary.nodesUnhealthy || 0),
          avgCpuUsage: 0,
          avgMemoryUsage: 0,
          avgHealthScore: 0,
        };
      }
    } catch (error) {
      console.error("Failed to load performance data:", error);
    }
  }

  function startSystemHealthSubscription() {
    systemHealthSubscription = infrastructureService.subscribeToSystemHealth({
      /**
       * @param {SystemHealthEvent & { nodeId?: string, clusterId?: string }} healthData
       */
      onSystemHealth: (healthData) => {
        // Normalize node/cluster keys to match codegen types
        const nodeKey = healthData.node ?? healthData.nodeId ?? "";
        const eventNormalized = /** @type {SystemHealthEvent} */ ({
          ...healthData,
          node: healthData.node ?? healthData.nodeId ?? "",
          cluster: healthData.cluster ?? healthData.clusterId ?? "",
        });
        // Update system health data
        systemHealth[nodeKey] = {
          event: eventNormalized,
          ts: new Date(healthData.timestamp),
        };

        // Trigger reactivity
        systemHealth = { ...systemHealth };
      },
      /**
       * @param {unknown} error
       */
      onError: (error) => {
        console.error("System health subscription failed:", error);
        toast.warning(
          "Real-time health monitoring disconnected. Data may be outdated.",
        );
      },
    });
  }

  /**
   * @param {string} nodeId
   * @returns {string}
   */
  function getNodeStatus(nodeId) {
    const health = systemHealth[nodeId];
    if (!health) return "UNKNOWN";
    return health.event.status;
  }

  /**
   * @param {string} nodeId
   * @returns {number}
   */
  function getNodeHealthScore(nodeId) {
    const health = systemHealth[nodeId];
    if (!health) return 0;
    return Math.round((health.event.healthScore || 0) * 100);
  }

  /**
   * @param {string} nodeId
   * @returns {string}
   */
  function formatCpuUsage(nodeId) {
    const health = systemHealth[nodeId];
    if (!health) return "0%";
    return `${Math.round(health.event.cpuUsage)}%`;
  }

  /**
   * @param {string} nodeId
   * @returns {string}
   */
  function formatMemoryUsage(nodeId) {
    const health = systemHealth[nodeId];
    if (!health) return "0%";
    return `${Math.round(health.event.memoryUsage)}%`;
  }

  /**
   * @param {string | null | undefined} status
   * @returns {string}
   */
  function getStatusBadgeClass(status) {
    switch (status?.toLowerCase()) {
      case "healthy":
        return "border-emerald-500/40 bg-emerald-500/10 text-emerald-300";
      case "degraded":
        return "border-amber-400/40 bg-amber-500/10 text-amber-200";
      case "unhealthy":
        return "border-rose-500/40 bg-rose-500/10 text-rose-300";
      default:
        return "border-slate-500/40 bg-slate-500/10 text-slate-200";
    }
  }

  const platformPerformanceCards = $derived([
    {
      key: "activeNodes",
      label: "Active Nodes",
      value: platformMetrics.totalActiveNodes ?? 0,
      tone: "text-tokyo-night-green",
    },
    {
      key: "avgCpu",
      label: "Avg CPU Usage",
      value: `${(platformMetrics.avgCpuUsage ?? 0).toFixed(1)}%`,
      tone: "text-tokyo-night-blue",
    },
    {
      key: "avgMemory",
      label: "Avg Memory Usage",
      value: `${(platformMetrics.avgMemoryUsage ?? 0).toFixed(1)}%`,
      tone: "text-tokyo-night-purple",
    },
    {
      key: "avgHealth",
      label: "Avg Health Score",
      value: `${Math.round(platformMetrics.avgHealthScore ?? 0)}%`,
      tone: "text-tokyo-night-orange",
    },
  ]);

  const viewerSummaryCards = $derived([
    {
      key: "totalViewers",
      label: "Total Viewers",
      value: platformSummary.totalViewers ?? 0,
      tone: "text-tokyo-night-blue",
    },
    {
      key: "totalStreams",
      label: "Active Streams",
      value: platformSummary.totalStreams ?? 0,
      tone: "text-tokyo-night-purple",
    },
    {
      key: "connectionQuality",
      label: "Connection Quality",
      value: `${Math.round(platformSummary.avgConnectionQuality ?? 0)}%`,
      tone: "text-tokyo-night-green",
    },
    {
      key: "countries",
      label: "Countries Reached",
      value: platformSummary.uniqueCountries ?? 0,
      tone: "text-tokyo-night-orange",
    },
  ]);
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
          {#each Array(3) as _, index (index)}
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
          {#each Array(3) as _, index (index)}
            <LoadingCard variant="infrastructure" />
          {/each}
        </div>
      </div>

      <!-- Nodes Skeleton -->
      <div class="bg-tokyo-night-surface rounded-lg p-6">
        <SkeletonLoader type="text-lg" className="w-20 mb-4" />
        <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
          {#each Array(6) as _, index (index)}
            <LoadingCard variant="infrastructure" />
          {/each}
        </div>
      </div>
    {:else}
      <!-- Tenant Information -->
      {#if tenant}
        <Card class="mb-8">
          <CardHeader>
            <CardTitle class="text-tokyo-night-cyan"
              >Tenant Information</CardTitle
            >
            <CardDescription>
              Key identifiers for your organization’s infrastructure.
            </CardDescription>
          </CardHeader>
          <CardContent>
            <div class="grid grid-cols-1 md:grid-cols-3 gap-4">
              <div class="space-y-2">
                <Badge
                  variant="outline"
                  class="uppercase tracking-wide text-[0.65rem]"
                >
                  Tenant Name
                </Badge>
                <p class="text-lg font-semibold">{tenant.name}</p>
              </div>
              <div class="space-y-2">
                <Badge
                  variant="outline"
                  class="uppercase tracking-wide text-[0.65rem]"
                >
                  Tenant ID
                </Badge>
                <p class="font-mono text-sm">{tenant.id}</p>
              </div>
              <div class="space-y-2">
                <Badge
                  variant="outline"
                  class="uppercase tracking-wide text-[0.65rem]"
                >
                  Created
                </Badge>
                <p class="text-sm">
                  {new Date(tenant.createdAt).toLocaleDateString()}
                </p>
              </div>
            </div>
          </CardContent>
        </Card>
      {/if}

      <!-- Platform Performance Overview -->
      <Card class="mb-8">
        <CardHeader>
          <CardTitle class="text-tokyo-night-cyan">
            Platform Performance (Last 24 Hours)
          </CardTitle>
          <CardDescription>
            Snapshot of capacity and health across your deployed nodes.
          </CardDescription>
        </CardHeader>
        <CardContent class="space-y-6">
          <Band emphasis="high" class="p-4">
            <div class="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-4 gap-4">
              {#each platformPerformanceCards as stat (stat.key)}
                <InfrastructureMetricCard
                  label={stat.label}
                  value={stat.value}
                  tone={stat.tone}
                />
              {/each}
            </div>
          </Band>

          <NodePerformanceTable {nodePerformanceMetrics} />
        </CardContent>
      </Card>

      <!-- Viewer & Stream Metrics -->
      <Card class="mb-8">
        <CardHeader>
          <CardTitle class="text-tokyo-night-cyan">
            Viewer & Stream Metrics (Last 24 Hours)
          </CardTitle>
          <CardDescription>
            Engagement insights aggregated from your live and on-demand streams.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <Band emphasis="medium" class="p-4">
            <div class="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-4 gap-4">
              {#each viewerSummaryCards as stat (stat.key)}
                <InfrastructureMetricCard
                  label={stat.label}
                  value={stat.value}
                  tone={stat.tone}
                />
              {/each}
            </div>
          </Band>
        </CardContent>
      </Card>

      <!-- Clusters Overview -->
      <Card class="mb-8">
        <CardHeader>
          <CardTitle class="text-tokyo-night-cyan">Clusters</CardTitle>
          <CardDescription>
            Track cluster health, capacity, and deployment regions.
          </CardDescription>
        </CardHeader>
        <CardContent>
          {#if clusters.length === 0}
            <EmptyState
              title="No clusters found"
              description="Infrastructure clusters will appear here when configured"
              size="sm"
              showAction={false}
            >
              {@const SvelteComponent_1 = getIconComponent("Server")}
              <SvelteComponent_1
                class="w-8 h-8 text-tokyo-night-fg-dark mx-auto mb-2"
              />
            </EmptyState>
          {:else}
            <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
              {#each clusters as cluster (cluster.id ?? cluster.name)}
                <ClusterCard {cluster} {getStatusBadgeClass} />
              {/each}
            </div>
          {/if}
        </CardContent>
      </Card>

      <!-- Nodes Grid -->
      <Card>
        <CardHeader>
          <CardTitle class="text-tokyo-night-cyan">Nodes</CardTitle>
          <CardDescription>
            Inspect resource usage and recent health updates for each node.
          </CardDescription>
        </CardHeader>
        <CardContent>
          {#if nodes.length === 0}
            <EmptyState
              title="No nodes found"
              description="Infrastructure nodes will appear here when deployed"
              size="sm"
              showAction={false}
            >
              {@const SvelteComponent_2 = getIconComponent("HardDrive")}
              <SvelteComponent_2
                class="w-8 h-8 text-tokyo-night-fg-dark mx-auto mb-2"
              />
            </EmptyState>
          {:else}
            <div class="grid grid-cols-1 lg:grid-cols-2 gap-4">
              {#each nodes as node (node.id ?? node.nodeId ?? node.name)}
                <NodeCard
                  {node}
                  {systemHealth}
                  {getNodeStatus}
                  {getNodeHealthScore}
                  {formatCpuUsage}
                  {formatMemoryUsage}
                  {getStatusBadgeClass}
                />
              {/each}
            </div>
          {/if}
        </CardContent>
      </Card>
    {/if}
  </div>
</div>

<style>
  /* Tokyo Night theme colors already defined globally */
</style>

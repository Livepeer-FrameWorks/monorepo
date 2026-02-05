<script lang="ts">
  import { onMount, onDestroy, untrack } from "svelte";
  import { get } from "svelte/store";
  import { SvelteMap } from "svelte/reactivity";
  import { auth } from "$lib/stores/auth";
  import {
    fragment,
    GetInfrastructureOverviewStore,
    GetNodesConnectionStore,
    GetInfrastructureMetricsStore,
    SystemHealthStore,
    GetServiceInstancesConnectionStore,
    GetServiceInstancesHealthStore,
    NodeListFieldsStore,
  } from "$houdini";
  import type { SystemHealth$result } from "$houdini";
  import { toast } from "$lib/stores/toast.js";
  import LoadingCard from "$lib/components/LoadingCard.svelte";
  import SkeletonLoader from "$lib/components/SkeletonLoader.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import InfrastructureMetricCard from "$lib/components/shared/InfrastructureMetricCard.svelte";
  import NodePerformanceTable from "$lib/components/infrastructure/NodePerformanceTable.svelte";
  import ClusterCard from "$lib/components/infrastructure/ClusterCard.svelte";
  import { Badge } from "$lib/components/ui/badge";
  import { getIconComponent } from "$lib/iconUtils";
  import { resolveTimeRange, TIME_RANGE_OPTIONS } from "$lib/utils/time-range";
  import { Select, SelectContent, SelectItem, SelectTrigger } from "$lib/components/ui/select";
  import { Tooltip, TooltipContent, TooltipTrigger } from "$lib/components/ui/tooltip";

  // Icons
  const ServerIcon = getIconComponent("Server");
  const HardDriveIcon = getIconComponent("HardDrive");
  const ActivityIcon = getIconComponent("Activity");
  const NetworkIcon = getIconComponent("Globe");
  const BuildingIcon = getIconComponent("Building2");
  const PackageIcon = getIconComponent("Package");
  const CalendarIcon = getIconComponent("Calendar");

  // Houdini stores
  const infrastructureStore = new GetInfrastructureOverviewStore();
  const nodesStore = new GetNodesConnectionStore();
  const metricsStore = new GetInfrastructureMetricsStore();
  const systemHealthSub = new SystemHealthStore();
  const serviceInstancesStore = new GetServiceInstancesConnectionStore();
  const serviceInstancesHealthStore = new GetServiceInstancesHealthStore();

  // Fragment stores for unmasking nested data
  const nodeCoreStore = new NodeListFieldsStore();

  // Pagination state
  let loadingMoreInstances = $state(false);

  let isAuthenticated = false;

  // Derived state from Houdini stores
  let hasInfrastructureData = $derived(!!$infrastructureStore.data);
  let loading = $derived($infrastructureStore.fetching && !hasInfrastructureData);
  let tenant = $derived($infrastructureStore.data?.tenant ?? null);
  let clusters = $derived(
    $infrastructureStore.data?.clustersConnection?.edges?.map((e) => e.node) ?? []
  );

  // Get masked nodes from edges and unmask using fragment store
  let maskedNodes = $derived($nodesStore.data?.nodesConnection?.edges?.map((e) => e.node) ?? []);

  // Unmask nodes with fragment() and get() pattern
  let nodes = $derived(maskedNodes.map((node) => get(fragment(node, nodeCoreStore))));
  let serviceInstances = $derived(
    $serviceInstancesStore.data?.analytics?.infra?.serviceInstancesConnection?.edges?.map(
      (e) => e.node
    ) ?? []
  );
  let serviceHealth = $derived(
    $serviceInstancesHealthStore.data?.analytics?.infra?.serviceInstancesHealth ?? []
  );
  let hasMoreInstances = $derived(
    $serviceInstancesStore.data?.analytics?.infra?.serviceInstancesConnection?.pageInfo
      ?.hasNextPage ?? false
  );

  // Real-time system health data
  type SystemHealthEvent = NonNullable<SystemHealth$result["liveSystemHealth"]>;
  let systemHealth = $state<Record<string, { event: SystemHealthEvent; ts: Date }>>({});

  // Recent system health events (for the live feed)
  let recentHealthEvents = $state<{ event: SystemHealthEvent; ts: Date }[]>([]);

  // Performance analytics types
  interface NodePerformanceMetric {
    nodeId: string;
    avgCpuUsage: number;
    avgMemoryUsage: number;
    avgDiskUsage: number;
    avgShmUsage: number;
  }

  interface NetworkIOMetric {
    nodeId: string;
    totalBandwidthIn: number;
    totalBandwidthOut: number;
  }

  // Derived performance metrics from the metrics store
  let nodePerformanceMetrics = $derived.by(() => {
    const aggregated = $metricsStore.data?.analytics?.infra?.nodeMetricsAggregated ?? [];
    if (aggregated.length === 0) return [] as NodePerformanceMetric[];
    return aggregated.map((metric) => ({
      nodeId: metric.nodeId,
      avgCpuUsage: metric.avgCpu ?? 0,
      avgMemoryUsage: metric.avgMemory ?? 0,
      avgDiskUsage: metric.avgDisk ?? 0,
      avgShmUsage: metric.avgShm ?? 0,
    }));
  });

  let networkIOMetrics = $derived.by(() => {
    const aggregated = $metricsStore.data?.analytics?.infra?.nodeMetricsAggregated ?? [];
    if (aggregated.length === 0) return [] as NetworkIOMetric[];
    return aggregated.map((metric) => ({
      nodeId: metric.nodeId,
      totalBandwidthIn: metric.totalBandwidthIn ?? 0,
      totalBandwidthOut: metric.totalBandwidthOut ?? 0,
    }));
  });

  let platformMetrics = $derived.by(() => {
    const totalNodes = nodePerformanceMetrics.length;
    if (totalNodes === 0) return { totalActiveNodes: 0, avgCpuUsage: 0, avgMemoryUsage: 0 };
    return {
      totalActiveNodes: totalNodes,
      avgCpuUsage: nodePerformanceMetrics.reduce((sum, n) => sum + n.avgCpuUsage, 0) / totalNodes,
      avgMemoryUsage:
        nodePerformanceMetrics.reduce((sum, n) => sum + n.avgMemoryUsage, 0) / totalNodes,
    };
  });

  let serviceHealthSummary = $derived.by(() => {
    const summary = {
      healthy: 0,
      degraded: 0,
      unhealthy: 0,
      unknown: 0,
    };
    for (const item of serviceHealth) {
      const status = item?.status?.toLowerCase() ?? "unknown";
      if (status === "healthy") summary.healthy += 1;
      else if (status === "degraded") summary.degraded += 1;
      else if (status === "unhealthy") summary.unhealthy += 1;
      else summary.unknown += 1;
    }
    return summary;
  });

  // Time range for performance metrics
  let timeRange = $state("24h");
  let currentRange = $derived(resolveTimeRange(timeRange));
  const timeRangeOptions = TIME_RANGE_OPTIONS.filter((option) =>
    ["24h", "7d", "30d"].includes(option.value)
  );

  // Subscribe to auth store
  auth.subscribe((authState) => {
    isAuthenticated = authState.isAuthenticated;
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
    systemHealthSub.unlisten();
  });

  async function loadInfrastructureData() {
    try {
      const range = resolveTimeRange(timeRange);
      const metricsFirst = Math.min(range.days * 24, 150);
      const timeRangeInput = { start: range.start, end: range.end };
      // Fetch infrastructure overview, nodes, metrics, and service instances in parallel
      await Promise.all([
        infrastructureStore.fetch(),
        nodesStore.fetch(),
        metricsStore.fetch({
          variables: { timeRange: timeRangeInput, first: metricsFirst, noCache: false },
        }),
        serviceInstancesStore.fetch(),
        serviceInstancesHealthStore.fetch().catch(() => null),
      ]);

      if ($infrastructureStore.errors?.length) {
        console.error("Failed to load infrastructure data:", $infrastructureStore.errors);
        toast.error("Failed to load infrastructure data. Please refresh the page.");
        return;
      }

      // Initialize systemHealth - prefer liveState (real-time) over historical metrics
      const initialHealth: Record<string, { event: SystemHealthEvent; ts: Date }> = {};

      // First: Use liveState from nodes (real-time data from live_nodes table)
      const nodesData = $nodesStore.data?.nodesConnection?.edges ?? [];
      for (const edge of nodesData) {
        const maskedNode = edge?.node;
        if (!maskedNode) continue;

        // Unmask the fragment to access nodeId and liveState
        const unmaskedNode = get(fragment(maskedNode, nodeCoreStore));
        const nodeId = unmaskedNode?.nodeId;
        if (!nodeId) continue;

        const liveState = unmaskedNode?.liveState;

        if (liveState) {
          // Use real-time data from live_nodes table (accurate absolute values)
          initialHealth[nodeId] = {
            event: {
              node: nodeId,
              location: liveState.location ?? "",
              status: liveState.isHealthy ? "HEALTHY" : "UNHEALTHY",
              cpuTenths: Math.round(liveState.cpuPercent * 10),
              isHealthy: liveState.isHealthy,
              ramMax: liveState.ramTotalBytes,
              ramCurrent: liveState.ramUsedBytes,
              diskTotalBytes: liveState.diskTotalBytes,
              diskUsedBytes: liveState.diskUsedBytes,
              shmTotalBytes: null,
              shmUsedBytes: null,
              timestamp: liveState.updatedAt,
            } as SystemHealthEvent,
            ts: new Date(liveState.updatedAt),
          };
        }
      }

      // Second: Fall back to historical metrics for nodes without liveState
      const metricsData =
        $metricsStore.data?.analytics?.infra?.nodeMetrics1hConnection?.edges ?? [];
      if (metricsData.length > 0) {
        // Get most recent metric for each node
        const latestByNode = new SvelteMap<string, (typeof metricsData)[0]>();
        for (const edge of metricsData) {
          if (!edge?.node) continue;
          const nodeId = edge.node.nodeId;
          // Skip if we already have liveState for this node
          if (initialHealth[nodeId]) continue;

          const existing = latestByNode.get(nodeId);
          if (!existing || new Date(edge.node.timestamp) > new Date(existing.node!.timestamp)) {
            latestByNode.set(nodeId, edge);
          }
        }

        // Add historical data only for nodes without liveState
        for (const [nodeId, edge] of latestByNode) {
          if (initialHealth[nodeId]) continue; // Skip if we have liveState

          const metric = edge.node!;
          // Historical metrics only provide percentages; keep absolute values unset.
          const cpuTenths = Math.round((metric.avgCpu ?? 0) * 10);

          initialHealth[nodeId] = {
            event: {
              node: nodeId,
              location: "", // Not available in historical
              status: (metric.wasHealthy ?? true) ? "HEALTHY" : "UNHEALTHY",
              cpuTenths,
              isHealthy: metric.wasHealthy ?? true,
              // Historical metrics are percentages (0-100). Use a synthetic max so UI percent calcs work.
              ramMax: 100,
              ramCurrent: metric.avgMemory ?? 0,
              diskTotalBytes: 100,
              diskUsedBytes: metric.avgDisk ?? 0,
              shmTotalBytes: 100,
              shmUsedBytes: metric.avgShm ?? 0,
              timestamp: metric.timestamp,
            } as SystemHealthEvent,
            ts: new Date(metric.timestamp),
          };
        }
      }

      // Initialize state (use untrack to avoid reactive loops)
      untrack(() => {
        if (Object.keys(systemHealth).length === 0 && Object.keys(initialHealth).length > 0) {
          systemHealth = initialHealth;
        }
      });
    } catch (error) {
      console.error("Failed to load infrastructure data:", error);
      toast.error("Failed to load infrastructure data. Please refresh the page.");
    }
  }

  function handleTimeRangeChange(value: string) {
    timeRange = value;
    loadInfrastructureData();
  }

  function startSystemHealthSubscription() {
    systemHealthSub.listen();
  }

  async function loadMoreInstances() {
    if (!hasMoreInstances || loadingMoreInstances) return;

    loadingMoreInstances = true;
    try {
      await serviceInstancesStore.loadNextPage();
    } catch (err) {
      console.error("Failed to load more service instances:", err);
      toast.error("Failed to load more service instances");
    } finally {
      loadingMoreInstances = false;
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
    const healthData = $systemHealthSub.data?.liveSystemHealth;
    if (healthData) {
      // Untrack the state mutations to prevent effect loops
      untrack(() => {
        const nodeKey = healthData.node || "";
        const eventEntry = {
          event: healthData,
          ts: new Date(healthData.timestamp),
        };

        // Check for status changes and log events
        const prevHealth = systemHealth[nodeKey];
        if (prevHealth && prevHealth.event.status !== healthData.status) {
          // Status changed
          if (healthData.status === "HEALTHY") {
            // Previously logged via addInfraEvent, now only affects systemHealth map and recentHealthEvents
          } else if (healthData.status === "UNHEALTHY" || healthData.status === "DEGRADED") {
            // Previously logged via addInfraEvent, now only affects systemHealth map and recentHealthEvents
          }
        }

        // Update system health data
        if (nodeKey) {
          systemHealth[nodeKey] = eventEntry;
          // Trigger reactivity
          systemHealth = { ...systemHealth };
        }

        // Add to recent events feed (keep last 20)
        recentHealthEvents = [eventEntry, ...recentHealthEvents.slice(0, 19)];
      });
    }
  });

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

  function getHealthBadgeClass(healthStatus: string | null | undefined) {
    switch (healthStatus?.toLowerCase()) {
      case "healthy":
        return "border-success/40 bg-success/10 text-success";
      case "unhealthy":
        return "border-rose-500/40 bg-rose-500/10 text-rose-300";
      default:
        return "border-muted-foreground/40 bg-muted-foreground/10 text-muted-foreground";
    }
  }

  function formatServiceName(serviceId: string) {
    // Map service IDs to friendly names
    const serviceNames: Record<string, string> = {
      api_gateway: "Bridge",
      api_control: "Commodore",
      api_tenants: "Quartermaster",
      api_billing: "Purser",
      api_analytics_ingest: "Periscope Ingest",
      api_analytics_query: "Periscope Query",
      api_firehose: "Decklog",
      api_balancing: "Foghorn",
      api_sidecar: "Helmsman",
      api_realtime: "Signalman",
      api_forms: "Forms",
      api_dns: "Navigator",
      api_mesh: "Privateer",
    };
    return serviceNames[serviceId] || serviceId;
  }

  function formatTimeAgo(dateStr: string | null | undefined) {
    if (!dateStr) return "Never";
    const date = new Date(dateStr);
    const now = new Date();
    const diffMs = now.getTime() - date.getTime();
    const diffSec = Math.floor(diffMs / 1000);

    if (diffSec < 60) return `${diffSec}s ago`;
    if (diffSec < 3600) return `${Math.floor(diffSec / 60)}m ago`;
    if (diffSec < 86400) return `${Math.floor(diffSec / 3600)}h ago`;
    return `${Math.floor(diffSec / 86400)}d ago`;
  }

  const platformPerformanceCards = $derived([
    {
      key: "activeNodes",
      label: "Active Nodes",
      value: platformMetrics.totalActiveNodes ?? 0,
      tone: "text-success",
    },
    {
      key: "avgCpu",
      label: "Avg CPU Usage",
      value: `${(platformMetrics.avgCpuUsage ?? 0).toFixed(1)}%`,
      tone: "text-primary",
    },
    {
      key: "avgMemory",
      label: "Avg Memory Usage",
      value: `${(platformMetrics.avgMemoryUsage ?? 0).toFixed(1)}%`,
      tone: "text-accent-purple",
    },
  ]);
</script>

<svelte:head>
  <title>Infrastructure Dashboard - FrameWorks</title>
</svelte:head>

<div class="h-full flex flex-col">
  <!-- Fixed Page Header -->
  <div class="px-4 sm:px-6 lg:px-8 py-4 border-b border-[hsl(var(--tn-fg-gutter)/0.3)] shrink-0">
    <div class="flex items-center justify-between gap-4">
      <div class="flex items-center gap-3">
        <ServerIcon class="w-5 h-5 text-primary" />
        <div>
          <h1 class="text-xl font-bold text-foreground">Infrastructure</h1>
          <p class="text-sm text-muted-foreground">
            Monitor your clusters, nodes, and system health in real-time
          </p>
        </div>
      </div>
      <Select value={timeRange} onValueChange={handleTimeRangeChange} type="single">
        <SelectTrigger class="min-w-[150px]">
          <CalendarIcon class="w-4 h-4 mr-2 text-muted-foreground" />
          {currentRange.label}
        </SelectTrigger>
        <SelectContent>
          {#each timeRangeOptions as option (option.value)}
            <SelectItem value={option.value}>{option.label}</SelectItem>
          {/each}
        </SelectContent>
      </Select>
    </div>
  </div>

  <!-- Scrollable Content -->
  <div class="flex-1 overflow-y-auto">
    {#if loading}
      <!-- Loading Skeletons -->
      <div class="dashboard-grid">
        <div class="slab col-span-full">
          <div class="slab-header">
            <SkeletonLoader type="text-lg" class="w-40" />
          </div>
          <div class="slab-body--padded">
            <div class="grid grid-cols-1 md:grid-cols-3 gap-4">
              {#each Array(3) as _, index (index)}
                <div>
                  <SkeletonLoader type="text-sm" class="w-20 mb-1" />
                  <SkeletonLoader type="text" class="w-32" />
                </div>
              {/each}
            </div>
          </div>
        </div>

        <div class="slab col-span-full">
          <div class="slab-header">
            <SkeletonLoader type="text-lg" class="w-24" />
          </div>
          <div class="slab-body--padded">
            <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
              {#each Array(3) as _, index (index)}
                <LoadingCard variant="infrastructure" />
              {/each}
            </div>
          </div>
        </div>

        <div class="slab col-span-full">
          <div class="slab-header">
            <SkeletonLoader type="text-lg" class="w-20" />
          </div>
          <div class="slab-body--padded">
            <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
              {#each Array(6) as _, index (index)}
                <LoadingCard variant="infrastructure" />
              {/each}
            </div>
          </div>
        </div>
      </div>
    {:else}
      <div class="dashboard-grid">
        <!-- Tenant Information -->
        {#if tenant}
          <div class="slab col-span-full">
            <div class="slab-header">
              <div class="flex items-center gap-2">
                <BuildingIcon class="w-4 h-4 text-info" />
                <h3>Tenant Information</h3>
              </div>
              <p class="text-sm text-muted-foreground mt-1">
                Key identifiers for your organization's infrastructure.
              </p>
            </div>
            <div class="slab-body--padded">
              <div class="grid grid-cols-1 md:grid-cols-3 gap-4">
                <div class="space-y-2">
                  <Badge variant="outline" class="uppercase tracking-wide text-[0.65rem]">
                    Tenant Name
                  </Badge>
                  <p class="text-lg font-semibold">{tenant.name}</p>
                </div>
                <div class="space-y-2">
                  <Badge variant="outline" class="uppercase tracking-wide text-[0.65rem]">
                    Tenant ID
                  </Badge>
                  <p class="font-mono text-sm">{tenant.id}</p>
                </div>
                <div class="space-y-2">
                  <Badge variant="outline" class="uppercase tracking-wide text-[0.65rem]">
                    Created
                  </Badge>
                  <p class="text-sm">
                    {new Date(tenant.createdAt).toLocaleDateString()}
                  </p>
                </div>
              </div>
            </div>
          </div>
        {/if}

        <!-- Platform Performance Overview -->
        <div class="slab col-span-full">
          <div class="slab-header">
            <div class="flex items-center gap-2">
              <ActivityIcon class="w-4 h-4 text-info" />
              <h3>Platform Performance ({currentRange.label})</h3>
            </div>
            <p class="text-sm text-muted-foreground mt-1">
              Snapshot of capacity and health across your deployed nodes.
            </p>
          </div>
          <div class="slab-body--padded">
            <div class="grid grid-cols-1 sm:grid-cols-3 gap-4">
              {#each platformPerformanceCards as stat (stat.key)}
                <InfrastructureMetricCard label={stat.label} value={stat.value} tone={stat.tone} />
              {/each}
            </div>
          </div>
        </div>

        <!-- Service Health -->
        <div class="slab col-span-full">
          <div class="slab-header flex items-start justify-between gap-4">
            <div>
              <div class="flex items-center gap-2">
                <PackageIcon class="w-4 h-4 text-info" />
                <h3>Service Health</h3>
              </div>
              <p class="text-sm text-muted-foreground mt-1">
                Live health checks for Periscope, Signalman, and core services.
              </p>
            </div>
            <Tooltip>
              <TooltipTrigger
                class="text-[10px] uppercase tracking-wide text-muted-foreground border border-border/50 px-2 py-1"
              >
                Admin/Owner
              </TooltipTrigger>
              <TooltipContent>
                Host and health endpoint details are visible only to cluster owners or admins.
              </TooltipContent>
            </Tooltip>
          </div>
          <div class="slab-body--padded">
            <div class="grid grid-cols-2 sm:grid-cols-4 gap-3 mb-4">
              <InfrastructureMetricCard
                label="Healthy"
                value={serviceHealthSummary.healthy}
                tone="text-success"
              />
              <InfrastructureMetricCard
                label="Degraded"
                value={serviceHealthSummary.degraded}
                tone="text-warning"
              />
              <InfrastructureMetricCard
                label="Unhealthy"
                value={serviceHealthSummary.unhealthy}
                tone="text-destructive"
              />
              <InfrastructureMetricCard
                label="Unknown"
                value={serviceHealthSummary.unknown}
                tone="text-muted-foreground"
              />
            </div>

            {#if serviceHealth.length > 0}
              <div class="grid grid-cols-1 md:grid-cols-2 gap-3">
                {#each serviceHealth.slice(0, 12) as instance (instance.instanceId)}
                  <div class="p-3 border border-border/50">
                    <div class="flex items-center justify-between mb-2">
                      <div>
                        <p class="text-sm font-medium text-foreground">{instance.serviceId}</p>
                        <p class="text-xs text-muted-foreground">{instance.clusterId}</p>
                      </div>
                      <Badge
                        variant="outline"
                        class="uppercase text-[0.6rem] {instance.status?.toLowerCase() === 'healthy'
                          ? 'text-success'
                          : instance.status?.toLowerCase() === 'degraded'
                            ? 'text-warning'
                            : instance.status?.toLowerCase() === 'unhealthy'
                              ? 'text-destructive'
                              : 'text-muted-foreground'}"
                      >
                        {instance.status ?? "unknown"}
                      </Badge>
                    </div>
                    {#if instance.host}
                      <div class="text-xs text-muted-foreground font-mono">
                        {instance.host}:{instance.port}
                      </div>
                    {/if}
                    {#if instance.lastHealthCheck}
                      <p class="text-[10px] text-muted-foreground mt-2">
                        Last check: {new Date(instance.lastHealthCheck).toLocaleTimeString()}
                      </p>
                    {/if}
                  </div>
                {/each}
              </div>
            {:else}
              <p class="text-sm text-muted-foreground">No service health data available.</p>
            {/if}
          </div>
        </div>

        <!-- Node Performance Details -->
        <div class="slab col-span-full">
          <div class="slab-header">
            <div class="flex items-center justify-between w-full">
              <div class="flex items-center gap-2">
                <HardDriveIcon class="w-4 h-4 text-info" />
                <h3>Node Performance Details</h3>
              </div>
              {#if nodePerformanceMetrics.length > 0}
                <Badge variant="outline" class="uppercase tracking-wide text-[0.65rem]">
                  Showing {Math.min(nodePerformanceMetrics.length, 10)} of {nodePerformanceMetrics.length}
                </Badge>
              {/if}
            </div>
            <p class="text-sm text-muted-foreground mt-1">
              Detailed resource usage and status per node.
            </p>
          </div>
          <div class="slab-body--padded">
            <NodePerformanceTable {nodePerformanceMetrics} {systemHealth} />
          </div>
        </div>

        <!-- Network I/O Metrics -->
        {#if networkIOMetrics.length > 0 && networkIOMetrics.some((m) => m.totalBandwidthIn > 0 || m.totalBandwidthOut > 0)}
          <div class="slab col-span-full">
            <div class="slab-header">
              <div class="flex items-center gap-2">
                <NetworkIcon class="w-4 h-4 text-info" />
                <h3>Network I/O ({currentRange.label})</h3>
              </div>
              <p class="text-sm text-muted-foreground mt-1">
                Total bandwidth usage per node - ingest and egress traffic
              </p>
            </div>
            <div class="slab-body--padded">
              <div class="space-y-3">
                {#each networkIOMetrics.filter((m) => m.totalBandwidthIn > 0 || m.totalBandwidthOut > 0) as metric (metric.nodeId)}
                  {@const node = nodes?.find(
                    (n) => n.id === metric.nodeId || n.nodeName === metric.nodeId
                  )}
                  {@const inGB = (metric.totalBandwidthIn / (1024 * 1024 * 1024)).toFixed(2)}
                  {@const outGB = (metric.totalBandwidthOut / (1024 * 1024 * 1024)).toFixed(2)}
                  {@const totalGB = (
                    (metric.totalBandwidthIn + metric.totalBandwidthOut) /
                    (1024 * 1024 * 1024)
                  ).toFixed(2)}
                  <div class="p-4 border border-border/50">
                    <div class="flex items-center justify-between mb-3">
                      <div>
                        <h4 class="font-medium text-foreground">
                          {node?.nodeName || metric.nodeId}
                        </h4>
                        <p class="text-xs text-muted-foreground">
                          {node?.region || "Unknown region"}
                        </p>
                      </div>
                      <span class="text-sm font-mono text-primary">{totalGB} GB total</span>
                    </div>
                    <div class="grid grid-cols-2 gap-4">
                      <div class="flex items-center gap-3">
                        <div class="w-2 h-8 bg-info"></div>
                        <div>
                          <p class="text-xs text-muted-foreground">Ingest (RX)</p>
                          <p class="font-mono text-info">{inGB} GB</p>
                        </div>
                      </div>
                      <div class="flex items-center gap-3">
                        <div class="w-2 h-8 bg-warning"></div>
                        <div>
                          <p class="text-xs text-muted-foreground">Egress (TX)</p>
                          <p class="font-mono text-warning">{outGB} GB</p>
                        </div>
                      </div>
                    </div>
                  </div>
                {/each}
              </div>
            </div>
          </div>
        {/if}

        <!-- Live System Health Events -->
        {#if recentHealthEvents.length > 0}
          <div class="slab col-span-full">
            <div class="slab-header">
              <div class="flex items-center justify-between w-full">
                <div class="flex items-center gap-2">
                  <ActivityIcon class="w-4 h-4 text-info" />
                  <h3 class="flex items-center gap-2">
                    Live System Health
                    <span class="w-2 h-2 bg-success animate-pulse"></span>
                  </h3>
                </div>
                <Badge variant="outline" class="text-muted-foreground">
                  {recentHealthEvents.length} recent events
                </Badge>
              </div>
              <p class="text-sm text-muted-foreground mt-1">
                Real-time health events from your infrastructure nodes
              </p>
            </div>
            <div class="slab-body--padded">
              <div class="space-y-2 max-h-64 overflow-y-auto">
                {#each recentHealthEvents as { event, ts }, index (`${event.node}-${ts.getTime()}-${index}`)}
                  {@const isHealthy = event.isHealthy}
                  {@const cpuPercent = (event.cpuTenths / 10).toFixed(1)}
                  {@const memPercent = event.ramMax
                    ? (((event.ramCurrent || 0) / event.ramMax) * 100).toFixed(0)
                    : "0"}
                  <div class="flex items-center justify-between p-3 border border-border/50">
                    <div class="flex items-center gap-3">
                      <div class={`w-2 h-2 ${isHealthy ? "bg-success" : "bg-destructive"}`}></div>
                      <div>
                        <p class="text-sm font-medium text-foreground">{event.node}</p>
                        <p class="text-xs text-muted-foreground">{event.location}</p>
                      </div>
                    </div>
                    <div class="flex items-center gap-4 text-xs">
                      <div class="text-right">
                        <span class="text-muted-foreground">CPU</span>
                        <span
                          class={`ml-1 font-mono ${Number(cpuPercent) > 80 ? "text-warning" : "text-success"}`}
                          >{cpuPercent}%</span
                        >
                      </div>
                      <div class="text-right">
                        <span class="text-muted-foreground">RAM</span>
                        <span
                          class={`ml-1 font-mono ${Number(memPercent) > 80 ? "text-warning" : "text-success"}`}
                          >{memPercent}%</span
                        >
                      </div>
                      <Badge variant="outline" class={getStatusBadgeClass(event.status)}>
                        {event.status}
                      </Badge>
                      <span class="text-muted-foreground font-mono min-w-[70px] text-right">
                        {ts.toLocaleTimeString()}
                      </span>
                    </div>
                  </div>
                {/each}
              </div>
            </div>
          </div>
        {/if}

        <!-- Clusters Overview -->
        <div class="slab col-span-full">
          <div class="slab-header">
            <div class="flex items-center gap-2">
              <ServerIcon class="w-4 h-4 text-info" />
              <h3>Clusters</h3>
            </div>
            <p class="text-sm text-muted-foreground mt-1">
              Track cluster health, capacity, and deployment regions.
            </p>
          </div>
          <div class="slab-body--padded">
            {#if !clusters || clusters.length === 0}
              <EmptyState
                title="No clusters found"
                description="Infrastructure clusters will appear here when configured"
                size="sm"
                showAction={false}
              >
                <ServerIcon class="w-6 h-6 text-muted-foreground mx-auto mb-2" />
              </EmptyState>
            {:else}
              <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
                {#each clusters as cluster, index (`${cluster.id}-${index}`)}
                  <ClusterCard {cluster} {getStatusBadgeClass} />
                {/each}
              </div>
            {/if}
          </div>
        </div>

        <!-- Service Instances -->
        <div class="slab col-span-full">
          <div class="slab-header">
            <div class="flex items-center justify-between w-full">
              <div class="flex items-center gap-2">
                <PackageIcon class="w-4 h-4 text-info" />
                <h3>Service Instances</h3>
              </div>
              {#if serviceInstances.length > 0}
                <Badge variant="outline" class="text-muted-foreground">
                  {serviceInstances.length}{#if hasMoreInstances}+{/if} instances
                </Badge>
              {/if}
            </div>
            <p class="text-sm text-muted-foreground mt-1">
              FrameWorks services and their health status from periodic health checks.
            </p>
          </div>
          <div class="slab-body--padded">
            {#if !serviceInstances || serviceInstances.length === 0}
              <EmptyState
                title="No service instances found"
                description="Service instances will appear here when FrameWorks is running (correctly)"
                size="sm"
                showAction={false}
              >
                <PackageIcon class="w-6 h-6 text-muted-foreground mx-auto mb-2" />
              </EmptyState>
            {:else}
              <div class="space-y-2">
                {#each serviceInstances as instance, index (`${instance.id}-${index}`)}
                  {@const node = nodes?.find((n) => n.id === instance.nodeId)}
                  <div class="flex items-center justify-between p-3 border border-border/50">
                    <div class="flex items-center gap-3">
                      <div
                        class={`w-2 h-2 ${instance.healthStatus?.toLowerCase() === "healthy" ? "bg-success" : instance.healthStatus?.toLowerCase() === "unhealthy" ? "bg-destructive" : "bg-muted-foreground"}`}
                      ></div>
                      <div>
                        <p class="text-sm font-medium text-foreground">
                          {formatServiceName(instance.serviceId)}
                        </p>
                        <p class="text-xs text-muted-foreground">
                          {instance.instanceId}
                          {#if instance.version}
                            <span class="text-muted-foreground/60"> • v{instance.version}</span>
                          {/if}
                          {#if node}
                            <span class="text-muted-foreground/60">
                              • {node.nodeName || node.id}</span
                            >
                          {/if}
                        </p>
                      </div>
                    </div>
                    <div class="flex items-center gap-4 text-xs">
                      {#if instance.port}
                        <div class="text-right">
                          <span class="text-muted-foreground">Port</span>
                          <span class="ml-1 font-mono text-foreground">{instance.port}</span>
                        </div>
                      {/if}
                      <div class="text-right min-w-[60px]">
                        <span class="text-muted-foreground">Checked</span>
                        <span class="ml-1 font-mono text-foreground"
                          >{formatTimeAgo(instance.lastHealthCheck)}</span
                        >
                      </div>
                      <Badge variant="outline" class={getHealthBadgeClass(instance.healthStatus)}>
                        {instance.healthStatus || "Unknown"}
                      </Badge>
                    </div>
                  </div>
                {/each}
              </div>

              <!-- Load More Service Instances -->
              {#if hasMoreInstances}
                <div class="flex justify-center pt-4">
                  <button
                    onclick={loadMoreInstances}
                    disabled={loadingMoreInstances}
                    class="px-4 py-2 text-sm text-muted-foreground hover:text-foreground border border-border/50 hover:border-border transition-colors disabled:opacity-50"
                  >
                    {#if loadingMoreInstances}
                      Loading...
                    {:else}
                      Load More Instances
                    {/if}
                  </button>
                </div>
              {/if}
            {/if}
          </div>
        </div>
      </div>
    {/if}
  </div>
</div>

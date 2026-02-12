<script lang="ts">
  import { onMount } from "svelte";
  import { auth } from "$lib/stores/auth";
  import {
    GetInfrastructureOverviewStore,
    GetInfrastructureMetricsStore,
    GetServiceInstancesHealthStore,
  } from "$houdini";
  import { toast } from "$lib/stores/toast.js";
  import LoadingCard from "$lib/components/LoadingCard.svelte";
  import SkeletonLoader from "$lib/components/SkeletonLoader.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import InfrastructureMetricCard from "$lib/components/shared/InfrastructureMetricCard.svelte";
  import { Badge } from "$lib/components/ui/badge";
  import { Card, CardContent } from "$lib/components/ui/card";
  import { getIconComponent } from "$lib/iconUtils";
  import { resolveTimeRange, TIME_RANGE_OPTIONS } from "$lib/utils/time-range";
  import { Select, SelectContent, SelectItem, SelectTrigger } from "$lib/components/ui/select";
  import { Tooltip, TooltipContent, TooltipTrigger } from "$lib/components/ui/tooltip";

  const ServerIcon = getIconComponent("Server");
  const ActivityIcon = getIconComponent("Activity");
  const BuildingIcon = getIconComponent("Building2");
  const PackageIcon = getIconComponent("Package");
  const CalendarIcon = getIconComponent("Calendar");

  const infrastructureStore = new GetInfrastructureOverviewStore();
  const metricsStore = new GetInfrastructureMetricsStore();
  const serviceInstancesHealthStore = new GetServiceInstancesHealthStore();

  let isAuthenticated = false;

  let hasInfrastructureData = $derived(!!$infrastructureStore.data);
  let loading = $derived($infrastructureStore.fetching && !hasInfrastructureData);
  let tenant = $derived($infrastructureStore.data?.tenant ?? null);
  let clusters = $derived(
    $infrastructureStore.data?.clustersConnection?.edges?.map((e) => e.node) ?? []
  );

  let serviceHealth = $derived(
    $serviceInstancesHealthStore.data?.analytics?.infra?.serviceInstancesHealth ?? []
  );

  interface NodePerformanceMetric {
    nodeId: string;
    avgCpuUsage: number;
    avgMemoryUsage: number;
  }

  let nodePerformanceMetrics = $derived.by(() => {
    const aggregated = $metricsStore.data?.analytics?.infra?.nodeMetricsAggregated ?? [];
    if (aggregated.length === 0) return [] as NodePerformanceMetric[];
    return aggregated.map((metric) => ({
      nodeId: metric.nodeId,
      avgCpuUsage: metric.avgCpu ?? 0,
      avgMemoryUsage: metric.avgMemory ?? 0,
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
    const summary = { healthy: 0, degraded: 0, unhealthy: 0, unknown: 0 };
    for (const item of serviceHealth) {
      const status = item?.status?.toLowerCase() ?? "unknown";
      if (status === "healthy") summary.healthy += 1;
      else if (status === "degraded") summary.degraded += 1;
      else if (status === "unhealthy") summary.unhealthy += 1;
      else summary.unknown += 1;
    }
    return summary;
  });

  let timeRange = $state("24h");
  let currentRange = $derived(resolveTimeRange(timeRange));
  const timeRangeOptions = TIME_RANGE_OPTIONS.filter((option) =>
    ["24h", "7d", "30d"].includes(option.value)
  );

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

  auth.subscribe((authState) => {
    isAuthenticated = authState.isAuthenticated;
  });

  onMount(async () => {
    if (!isAuthenticated) {
      await auth.checkAuth();
    }
    await loadInfrastructureData();
  });

  async function loadInfrastructureData() {
    try {
      const range = resolveTimeRange(timeRange);
      const metricsFirst = Math.min(range.days * 24, 150);
      const timeRangeInput = { start: range.start, end: range.end };
      await Promise.all([
        infrastructureStore.fetch(),
        metricsStore.fetch({
          variables: { timeRange: timeRangeInput, first: metricsFirst, noCache: false },
        }),
        serviceInstancesHealthStore.fetch().catch(() => null),
      ]);

      if ($infrastructureStore.errors?.length) {
        console.error("Failed to load infrastructure data:", $infrastructureStore.errors);
        toast.error("Failed to load infrastructure data. Please refresh the page.");
      }
    } catch (error) {
      console.error("Failed to load infrastructure data:", error);
      toast.error("Failed to load infrastructure data. Please refresh the page.");
    }
  }

  function handleTimeRangeChange(value: string) {
    timeRange = value;
    loadInfrastructureData();
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

        <!-- Service Health Summary -->
        <div class="slab col-span-full">
          <div class="slab-header flex items-start justify-between gap-4">
            <div>
              <div class="flex items-center gap-2">
                <PackageIcon class="w-4 h-4 text-info" />
                <h3>Service Health</h3>
              </div>
              <p class="text-sm text-muted-foreground mt-1">
                Aggregate health status across all service instances.
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
            <div class="grid grid-cols-2 sm:grid-cols-4 gap-3">
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
          </div>
        </div>

        <!-- Clusters Grid -->
        <div class="slab col-span-full">
          <div class="slab-header">
            <div class="flex items-center gap-2">
              <ServerIcon class="w-4 h-4 text-info" />
              <h3>Clusters</h3>
            </div>
            <p class="text-sm text-muted-foreground mt-1">
              Click a cluster to view nodes, services, and performance details.
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
                  <a
                    href="/infrastructure/{cluster.clusterId}"
                    class="block no-underline hover:ring-2 hover:ring-primary/50 rounded-lg transition-shadow"
                  >
                    <Card>
                      <CardContent class="space-y-4">
                        <div class="flex items-start justify-between">
                          <div>
                            <h3 class="text-lg font-semibold">{cluster.clusterName}</h3>
                            <p class="text-sm text-muted-foreground">{cluster.clusterId}</p>
                          </div>
                          <Badge
                            variant="outline"
                            class="text-xs uppercase {getStatusBadgeClass(cluster.healthStatus)}"
                          >
                            {cluster.healthStatus}
                          </Badge>
                        </div>
                        <div class="space-y-1 text-sm text-muted-foreground">
                          <p>
                            Created: {cluster.createdAt
                              ? new Date(cluster.createdAt).toLocaleDateString()
                              : "N/A"}
                          </p>
                        </div>
                      </CardContent>
                    </Card>
                  </a>
                {/each}
              </div>
            {/if}
          </div>
        </div>
      </div>
    {/if}
  </div>
</div>

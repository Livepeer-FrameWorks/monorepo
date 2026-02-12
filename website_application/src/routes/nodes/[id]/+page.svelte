<script lang="ts">
  import { onMount, onDestroy, untrack } from "svelte";
  import { resolve } from "$app/paths";
  import { page } from "$app/state";
  import { get } from "svelte/store";
  import {
    fragment,
    GetNodesConnectionStore,
    GetNodePerformance5mStore,
    GetServiceInstancesConnectionStore,
    SystemHealthStore,
    NodeListFieldsStore,
  } from "$houdini";
  import type { SystemHealth$result } from "$houdini";
  import { toast } from "$lib/stores/toast.js";
  import { auth } from "$lib/stores/auth";
  import SkeletonLoader from "$lib/components/SkeletonLoader.svelte";
  import LoadingCard from "$lib/components/LoadingCard.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import InfrastructureMetricCard from "$lib/components/shared/InfrastructureMetricCard.svelte";
  import { Badge } from "$lib/components/ui/badge";
  import { getIconComponent } from "$lib/iconUtils";
  import { resolveTimeRange, TIME_RANGE_OPTIONS } from "$lib/utils/time-range";
  import { Select, SelectContent, SelectItem, SelectTrigger } from "$lib/components/ui/select";
  import { formatBytes } from "$lib/utils/formatters.js";

  const ArrowLeftIcon = getIconComponent("ArrowLeft");
  const HardDriveIcon = getIconComponent("HardDrive");
  const CpuIcon = getIconComponent("Cpu");
  const ActivityIcon = getIconComponent("Activity");
  const PackageIcon = getIconComponent("Package");
  const CalendarIcon = getIconComponent("Calendar");

  let nodeRelayId = $derived(page.params.id as string);

  const nodesStore = new GetNodesConnectionStore();
  const perfStore = new GetNodePerformance5mStore();
  const serviceInstancesStore = new GetServiceInstancesConnectionStore();
  const systemHealthSub = new SystemHealthStore();
  const nodeCoreStore = new NodeListFieldsStore();

  let isAuthenticated = false;

  let hasData = $derived(!!$nodesStore.data);
  let loading = $derived($nodesStore.fetching && !hasData);

  let maskedNodes = $derived($nodesStore.data?.nodesConnection?.edges?.map((e) => e.node) ?? []);
  let allNodes = $derived(maskedNodes.map((n) => get(fragment(n, nodeCoreStore))));
  let node = $derived(allNodes.find((n) => n.id === nodeRelayId) ?? null);
  let nodeId = $derived(node?.nodeId ?? "");

  let serviceInstances = $derived(
    (
      $serviceInstancesStore.data?.analytics?.infra?.serviceInstancesConnection?.edges?.map(
        (e) => e.node
      ) ?? []
    ).filter((s) => s.nodeId === nodeId)
  );

  // 5-minute performance data
  let perfData = $derived(
    $perfStore.data?.analytics?.infra?.nodePerformance5mConnection?.edges?.map((e) => e.node) ?? []
  );

  // Real-time health
  type SystemHealthEvent = NonNullable<SystemHealth$result["liveSystemHealth"]>;
  let currentHealth = $state<{ event: SystemHealthEvent; ts: Date } | null>(null);

  // Live state from node data
  let liveState = $derived(node?.liveState ?? null);

  let cpuPercent = $derived.by(() => {
    if (currentHealth) return (currentHealth.event.cpuTenths / 10).toFixed(1);
    if (liveState) return liveState.cpuPercent.toFixed(1);
    return "—";
  });

  let memPercent = $derived.by(() => {
    if (currentHealth?.event.ramMax) {
      return (((currentHealth.event.ramCurrent || 0) / currentHealth.event.ramMax) * 100).toFixed(
        1
      );
    }
    if (liveState?.ramTotalBytes) {
      return ((liveState.ramUsedBytes / liveState.ramTotalBytes) * 100).toFixed(1);
    }
    return "—";
  });

  let diskPercent = $derived.by(() => {
    if (currentHealth?.event.diskTotalBytes) {
      return (
        ((currentHealth.event.diskUsedBytes || 0) / currentHealth.event.diskTotalBytes) *
        100
      ).toFixed(1);
    }
    if (liveState?.diskTotalBytes) {
      return ((liveState.diskUsedBytes / liveState.diskTotalBytes) * 100).toFixed(1);
    }
    return "—";
  });

  let isHealthy = $derived(currentHealth?.event.isHealthy ?? liveState?.isHealthy ?? null);

  let timeRange = $state("24h");
  let currentRange = $derived(resolveTimeRange(timeRange));
  const timeRangeOptions = TIME_RANGE_OPTIONS.filter((option) =>
    ["24h", "7d", "30d"].includes(option.value)
  );

  let metricCards = $derived([
    {
      key: "cpu",
      label: "CPU",
      value: cpuPercent === "—" ? "—" : `${cpuPercent}%`,
      tone:
        cpuPercent === "—"
          ? "text-muted-foreground"
          : Number(cpuPercent) < 70
            ? "text-success"
            : Number(cpuPercent) < 90
              ? "text-warning"
              : "text-destructive",
    },
    {
      key: "memory",
      label: "Memory",
      value: memPercent === "—" ? "—" : `${memPercent}%`,
      tone:
        memPercent === "—"
          ? "text-muted-foreground"
          : Number(memPercent) < 70
            ? "text-success"
            : Number(memPercent) < 90
              ? "text-warning"
              : "text-destructive",
    },
    {
      key: "disk",
      label: "Disk",
      value: diskPercent === "—" ? "—" : `${diskPercent}%`,
      tone:
        diskPercent === "—"
          ? "text-muted-foreground"
          : Number(diskPercent) < 80
            ? "text-success"
            : Number(diskPercent) < 95
              ? "text-warning"
              : "text-destructive",
    },
    {
      key: "streams",
      label: "Active Streams",
      value: liveState?.activeStreams ?? 0,
      tone: "text-primary",
    },
    {
      key: "services",
      label: "Services",
      value: serviceInstances.length,
      tone: "text-info",
    },
    {
      key: "health",
      label: "Status",
      value: isHealthy === null ? "Unknown" : isHealthy ? "Healthy" : "Unhealthy",
      tone:
        isHealthy === null
          ? "text-muted-foreground"
          : isHealthy
            ? "text-success"
            : "text-destructive",
    },
  ]);

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

      // Once we have the node, fetch performance + services using its nodeId
      const foundNode = allNodes.find((n) => n.id === nodeRelayId);
      if (foundNode) {
        const range = resolveTimeRange(timeRange);
        const timeRangeInput = { start: range.start, end: range.end };
        await Promise.all([
          perfStore.fetch({
            variables: {
              nodeId: foundNode.nodeId,
              timeRange: timeRangeInput,
              first: 100,
            },
          }),
          serviceInstancesStore.fetch({
            variables: { nodeId: foundNode.nodeId },
          }),
        ]);
      }
    } catch (error) {
      console.error("Failed to load node data:", error);
      toast.error("Failed to load node data.");
    }
  }

  // Real-time health subscription
  $effect(() => {
    const healthData = $systemHealthSub.data?.liveSystemHealth;
    if (healthData && nodeId) {
      untrack(() => {
        if (healthData.node === nodeId) {
          currentHealth = {
            event: healthData,
            ts: new Date(healthData.timestamp),
          };
        }
      });
    }
  });

  function handleTimeRangeChange(value: string) {
    timeRange = value;
    if (nodeId) {
      const range = resolveTimeRange(value);
      const timeRangeInput = { start: range.start, end: range.end };
      perfStore.fetch({
        variables: { nodeId, timeRange: timeRangeInput, first: 100 },
      });
    }
  }

  function formatServiceName(serviceId: string) {
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
  <title>{node?.nodeName ?? "Node"} - FrameWorks</title>
</svelte:head>

<div class="h-full flex flex-col">
  <!-- Fixed Page Header -->
  <div class="px-4 sm:px-6 lg:px-8 py-4 border-b border-[hsl(var(--tn-fg-gutter)/0.3)] shrink-0">
    <div class="flex items-center justify-between gap-4">
      <div class="flex items-center gap-3">
        {#if node}
          <a
            href={resolve(`/infrastructure/${node.clusterId}`)}
            class="text-muted-foreground hover:text-foreground transition-colors"
          >
            <ArrowLeftIcon class="w-5 h-5" />
          </a>
        {:else}
          <a
            href={resolve("/nodes")}
            class="text-muted-foreground hover:text-foreground transition-colors"
          >
            <ArrowLeftIcon class="w-5 h-5" />
          </a>
        {/if}
        <HardDriveIcon class="w-5 h-5 text-primary" />
        <div>
          <div class="flex items-center gap-2">
            <h1 class="text-xl font-bold text-foreground">
              {node?.nodeName ?? nodeRelayId}
            </h1>
            {#if isHealthy !== null}
              <Badge
                variant="outline"
                class="text-xs uppercase {getStatusBadgeClass(isHealthy ? 'healthy' : 'unhealthy')}"
              >
                {isHealthy ? "Healthy" : "Unhealthy"}
              </Badge>
            {/if}
          </div>
          {#if node}
            <p class="text-sm text-muted-foreground">
              <span class="font-mono">{node.nodeId}</span>
              <span class="mx-1 text-border">|</span>
              <span class="capitalize">{node.nodeType}</span>
              {#if node.region}
                <span class="mx-1 text-border">|</span>
                {node.region}
              {/if}
            </p>
          {/if}
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
            <div class="grid grid-cols-2 md:grid-cols-3 lg:grid-cols-6 gap-4">
              {#each Array(6) as _, i (i)}
                <LoadingCard variant="infrastructure" />
              {/each}
            </div>
          </div>
        </div>
        <div class="slab col-span-full">
          <div class="slab-header">
            <SkeletonLoader type="text-lg" class="w-32" />
          </div>
          <div class="slab-body--padded">
            {#each Array(4) as _, i (i)}
              <SkeletonLoader type="text" class="w-full mb-2" />
            {/each}
          </div>
        </div>
      </div>
    {:else if !node}
      <div class="p-8">
        <EmptyState
          iconName="HardDrive"
          title="Node not found"
          description="The node could not be found or you don't have access."
        />
      </div>
    {:else}
      <div class="dashboard-grid">
        <!-- Metric Cards -->
        <div class="slab col-span-full">
          <div class="slab-header">
            <div class="flex items-center gap-2">
              <ActivityIcon class="w-4 h-4 text-info" />
              <h3>Node Status</h3>
            </div>
            {#if currentHealth}
              <p class="text-xs text-muted-foreground mt-1">
                Live data from {currentHealth.ts.toLocaleTimeString()}
              </p>
            {:else if liveState}
              <p class="text-xs text-muted-foreground mt-1">
                Last reported {formatTimeAgo(liveState.updatedAt)}
              </p>
            {/if}
          </div>
          <div class="slab-body--padded">
            <div class="grid grid-cols-2 md:grid-cols-3 lg:grid-cols-6 gap-4">
              {#each metricCards as stat (stat.key)}
                <InfrastructureMetricCard label={stat.label} value={stat.value} tone={stat.tone} />
              {/each}
            </div>
          </div>
        </div>

        <!-- Node Info -->
        <div class="slab col-span-full">
          <div class="slab-header">
            <div class="flex items-center gap-2">
              <HardDriveIcon class="w-4 h-4 text-info" />
              <h3>Node Information</h3>
            </div>
          </div>
          <div class="slab-body--padded">
            <div class="grid grid-cols-2 md:grid-cols-4 gap-4 text-sm">
              <div class="space-y-1">
                <p class="text-muted-foreground">Node Type</p>
                <p class="font-medium capitalize">{node.nodeType}</p>
              </div>
              <div class="space-y-1">
                <p class="text-muted-foreground">Region</p>
                <p class="font-medium">{node.region ?? "—"}</p>
              </div>
              <div class="space-y-1">
                <p class="text-muted-foreground">CPU Cores</p>
                <p class="font-medium">{node.cpuCores ?? "—"}</p>
              </div>
              <div class="space-y-1">
                <p class="text-muted-foreground">Memory</p>
                <p class="font-medium">{node.memoryGb ? `${node.memoryGb} GB` : "—"}</p>
              </div>
              <div class="space-y-1">
                <p class="text-muted-foreground">Disk</p>
                <p class="font-medium">{node.diskGb ? `${node.diskGb} GB` : "—"}</p>
              </div>
              <div class="space-y-1">
                <p class="text-muted-foreground">External IP</p>
                <p class="font-medium font-mono text-xs">{node.externalIp ?? "—"}</p>
              </div>
              <div class="space-y-1">
                <p class="text-muted-foreground">Last Heartbeat</p>
                <p class="font-medium">{formatTimeAgo(node.lastHeartbeat)}</p>
              </div>
              <div class="space-y-1">
                <p class="text-muted-foreground">Location</p>
                <p class="font-medium">{liveState?.location ?? "—"}</p>
              </div>
            </div>
          </div>
        </div>

        <!-- 5-Minute Performance History -->
        {#if perfData.length > 0}
          <div class="slab col-span-full">
            <div class="slab-header">
              <div class="flex items-center justify-between w-full">
                <div class="flex items-center gap-2">
                  <CpuIcon class="w-4 h-4 text-info" />
                  <h3>Performance History ({currentRange.label})</h3>
                </div>
                <Badge variant="outline" class="text-muted-foreground">
                  {perfData.length} samples
                </Badge>
              </div>
              <p class="text-sm text-muted-foreground mt-1">
                5-minute aggregated CPU, memory, bandwidth, and stream count.
              </p>
            </div>
            <div class="slab-body">
              <div class="overflow-x-auto max-h-[400px] overflow-y-auto">
                <table class="w-full text-sm">
                  <thead class="sticky top-0 bg-background">
                    <tr class="border-b border-border/50 text-left">
                      <th class="px-4 py-2 font-medium text-muted-foreground">Time</th>
                      <th class="px-4 py-2 font-medium text-muted-foreground text-right">Avg CPU</th
                      >
                      <th class="px-4 py-2 font-medium text-muted-foreground text-right">Max CPU</th
                      >
                      <th class="px-4 py-2 font-medium text-muted-foreground text-right"
                        >Avg Memory</th
                      >
                      <th class="px-4 py-2 font-medium text-muted-foreground text-right"
                        >Max Memory</th
                      >
                      <th class="px-4 py-2 font-medium text-muted-foreground text-right"
                        >Bandwidth</th
                      >
                      <th class="px-4 py-2 font-medium text-muted-foreground text-right">Streams</th
                      >
                    </tr>
                  </thead>
                  <tbody>
                    {#each perfData as row (row.id)}
                      <tr class="border-b border-border/30 hover:bg-muted/30">
                        <td class="px-4 py-2 text-xs text-muted-foreground whitespace-nowrap">
                          {new Date(row.timestamp).toLocaleTimeString()}
                        </td>
                        <td class="px-4 py-2 text-right font-mono">
                          <span class={(row.avgCpu ?? 0) > 80 ? "text-warning" : "text-foreground"}>
                            {(row.avgCpu ?? 0).toFixed(1)}%
                          </span>
                        </td>
                        <td class="px-4 py-2 text-right font-mono text-muted-foreground">
                          {(row.maxCpu ?? 0).toFixed(1)}%
                        </td>
                        <td class="px-4 py-2 text-right font-mono">
                          <span
                            class={(row.avgMemory ?? 0) > 80 ? "text-warning" : "text-foreground"}
                          >
                            {(row.avgMemory ?? 0).toFixed(1)}%
                          </span>
                        </td>
                        <td class="px-4 py-2 text-right font-mono text-muted-foreground">
                          {(row.maxMemory ?? 0).toFixed(1)}%
                        </td>
                        <td class="px-4 py-2 text-right font-mono">
                          {formatBytes(row.totalBandwidth ?? 0)}
                        </td>
                        <td class="px-4 py-2 text-right font-mono">
                          {row.avgStreams?.toFixed(0) ?? 0}
                        </td>
                      </tr>
                    {/each}
                  </tbody>
                </table>
              </div>
            </div>
          </div>
        {/if}

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
                  {serviceInstances.length} instance{serviceInstances.length !== 1 ? "s" : ""}
                </Badge>
              {/if}
            </div>
          </div>
          <div class="slab-body--padded">
            {#if serviceInstances.length === 0}
              <EmptyState
                iconName="Package"
                title="No service instances"
                description="No services are running on this node."
                size="sm"
                showAction={false}
              />
            {:else}
              <div class="space-y-2">
                {#each serviceInstances as instance, index (`${instance.id}-${index}`)}
                  <div class="flex items-center justify-between p-3 border border-border/50">
                    <div class="flex items-center gap-3">
                      <div
                        class="w-2 h-2 {instance.healthStatus?.toLowerCase() === 'healthy'
                          ? 'bg-success'
                          : instance.healthStatus?.toLowerCase() === 'unhealthy'
                            ? 'bg-destructive'
                            : 'bg-muted-foreground'}"
                      ></div>
                      <div>
                        <p class="text-sm font-medium text-foreground">
                          {formatServiceName(instance.serviceId)}
                        </p>
                        <p class="text-xs text-muted-foreground">
                          {instance.instanceId}
                          {#if instance.version}
                            <span class="text-muted-foreground/60"> v{instance.version}</span>
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
                        <span class="ml-1 font-mono text-foreground">
                          {formatTimeAgo(instance.lastHealthCheck)}
                        </span>
                      </div>
                      <Badge
                        variant="outline"
                        class="uppercase text-[0.6rem] {getStatusBadgeClass(instance.healthStatus)}"
                      >
                        {instance.healthStatus || "Unknown"}
                      </Badge>
                    </div>
                  </div>
                {/each}
              </div>
            {/if}
          </div>
        </div>
      </div>
    {/if}
  </div>
</div>

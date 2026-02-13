<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { SvelteMap } from "svelte/reactivity";
  import { auth } from "$lib/stores/auth";
  import { GetNetworkOverviewStore, GetFederationEventsStore } from "$houdini";
  import { toast } from "$lib/stores/toast.js";
  import SkeletonLoader from "$lib/components/SkeletonLoader.svelte";
  import LoadingCard from "$lib/components/LoadingCard.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import InfrastructureMetricCard from "$lib/components/shared/InfrastructureMetricCard.svelte";
  import RoutingMap from "$lib/components/charts/RoutingMap.svelte";
  import { Badge } from "$lib/components/ui/badge";
  import { getIconComponent } from "$lib/iconUtils";
  import { resolveTimeRange, TIME_RANGE_OPTIONS } from "$lib/utils/time-range";
  import { Select, SelectContent, SelectItem, SelectTrigger } from "$lib/components/ui/select";
  import { Tooltip, TooltipContent, TooltipTrigger } from "$lib/components/ui/tooltip";

  const GlobeIcon = getIconComponent("Globe");
  const ActivityIcon = getIconComponent("Activity");
  const NetworkIcon = getIconComponent("Network");
  const ZapIcon = getIconComponent("Zap");
  const CalendarIcon = getIconComponent("Calendar");
  const RefreshCwIcon = getIconComponent("RefreshCw");

  const networkStore = new GetNetworkOverviewStore();
  const eventsStore = new GetFederationEventsStore();

  let isAuthenticated = false;
  let pollTimer: ReturnType<typeof setInterval> | undefined;

  const POLL_INTERVAL_MS = 30_000;

  let hasData = $derived(!!$networkStore.data);
  let loading = $derived($networkStore.fetching && !hasData);

  // Network topology
  let clusters = $derived($networkStore.data?.networkStatus?.clusters ?? []);
  let peerConnections = $derived($networkStore.data?.networkStatus?.peerConnections ?? []);
  let totalNodes = $derived($networkStore.data?.networkStatus?.totalNodes ?? 0);
  let healthyNodes = $derived($networkStore.data?.networkStatus?.healthyNodes ?? 0);
  let updatedAt = $derived($networkStore.data?.networkStatus?.updatedAt ?? null);

  // Traffic matrix
  let trafficMatrix = $derived($networkStore.data?.analytics?.infra?.clusterTrafficMatrix ?? []);

  // Federation summary
  let fedSummary = $derived($networkStore.data?.analytics?.infra?.federationSummary);
  let totalEvents = $derived(fedSummary?.totalEvents ?? 0);
  let overallAvgLatencyMs = $derived(fedSummary?.overallAvgLatencyMs ?? 0);
  let overallFailureRate = $derived(fedSummary?.overallFailureRate ?? 0);
  let eventCounts = $derived(fedSummary?.eventCounts ?? []);

  // Federation events log
  let federationEvents = $derived(
    $eventsStore.data?.analytics?.infra?.federationEventsConnection?.edges?.map((e) => e.node) ?? []
  );

  // Transform clusters to RoutingMap ClusterMarker format
  let mapClusters = $derived(
    clusters.map((c) => ({
      id: c.clusterId,
      name: c.name,
      region: c.region,
      lat: c.latitude,
      lng: c.longitude,
      nodeCount: c.nodeCount,
      healthyNodeCount: c.healthyNodeCount,
      status:
        c.status === "operational" ? "operational" : c.status === "degraded" ? "degraded" : "down",
      activeStreams: 0,
      activeViewers: 0,
    }))
  );

  // Build cluster geo lookup for relationship lines
  let clusterGeoMap = $derived.by(() => {
    const m = new SvelteMap<string, { lat: number; lng: number }>();
    for (const c of clusters) {
      const hasGeo =
        Number.isFinite(c.latitude) &&
        Number.isFinite(c.longitude) &&
        !(c.latitude === 0 && c.longitude === 0);
      if (hasGeo) {
        m.set(c.clusterId, { lat: c.latitude, lng: c.longitude });
      }
    }
    return m;
  });

  // Transform peer connections + traffic into RelationshipLine format
  let mapRelationships = $derived.by(() => {
    const lines: Array<{
      from: [number, number];
      to: [number, number];
      type: "peering" | "traffic" | "replication";
      active: boolean;
      weight?: number;
      metrics?: { eventCount?: number; avgLatencyMs?: number; successRate?: number };
    }> = [];

    // Peer connections as peering lines
    for (const pc of peerConnections) {
      const src = clusterGeoMap.get(pc.sourceCluster);
      const tgt = clusterGeoMap.get(pc.targetCluster);
      if (src && tgt) {
        lines.push({
          from: [src.lat, src.lng],
          to: [tgt.lat, tgt.lng],
          type: "peering",
          active: pc.connected,
        });
      }
    }

    // Traffic matrix as traffic lines
    for (const t of trafficMatrix) {
      const src = clusterGeoMap.get(t.clusterId);
      const tgt = clusterGeoMap.get(t.remoteClusterId);
      if (src && tgt && t.eventCount > 0) {
        // Skip if already have a peering line between same pair
        const hasPeering = lines.some(
          (l) =>
            l.type === "peering" &&
            ((l.from[0] === src.lat &&
              l.from[1] === src.lng &&
              l.to[0] === tgt.lat &&
              l.to[1] === tgt.lng) ||
              (l.from[0] === tgt.lat &&
                l.from[1] === tgt.lng &&
                l.to[0] === src.lat &&
                l.to[1] === src.lng))
        );
        if (!hasPeering) {
          lines.push({
            from: [src.lat, src.lng],
            to: [tgt.lat, tgt.lng],
            type: "traffic",
            active: true,
            weight: Math.min(Math.max(t.eventCount / 100, 1), 8),
            metrics: {
              eventCount: t.eventCount,
              avgLatencyMs: t.avgLatencyMs,
              successRate: t.successRate,
            },
          });
        }
      }
    }

    return lines;
  });

  // Time range
  let timeRange = $state("24h");
  let currentRange = $derived(resolveTimeRange(timeRange));
  const timeRangeOptions = TIME_RANGE_OPTIONS.filter((option) =>
    ["24h", "7d", "30d"].includes(option.value)
  );

  auth.subscribe((authState) => {
    isAuthenticated = authState.isAuthenticated;
  });

  onMount(async () => {
    if (!isAuthenticated) {
      await auth.checkAuth();
    }
    await loadData();
    pollTimer = setInterval(() => loadData(), POLL_INTERVAL_MS);
  });

  onDestroy(() => {
    if (pollTimer) clearInterval(pollTimer);
  });

  async function loadData() {
    try {
      const range = resolveTimeRange(timeRange);
      const timeRangeInput = { start: range.start, end: range.end };
      await Promise.all([
        networkStore.fetch({ variables: { timeRange: timeRangeInput } }),
        eventsStore.fetch({ variables: { timeRange: timeRangeInput, first: 50 } }),
      ]);
      if ($networkStore.errors?.length) {
        console.error("Failed to load federation data:", $networkStore.errors);
        toast.error("Failed to load federation data.");
      }
    } catch (error) {
      console.error("Failed to load federation data:", error);
      toast.error("Failed to load federation data.");
    }
  }

  async function handleTimeRangeChange(value: string | undefined) {
    if (!value) return;
    timeRange = value;
    await loadData();
  }

  // Metric cards
  let metricCards = $derived([
    {
      key: "clusters",
      label: "Clusters",
      value: clusters.length,
      tone: "text-primary",
    },
    {
      key: "nodes",
      label: "Total Nodes",
      value: totalNodes,
      tone: "text-info",
    },
    {
      key: "healthy",
      label: "Healthy Nodes",
      value: healthyNodes,
      tone: totalNodes > 0 && healthyNodes === totalNodes ? "text-success" : "text-warning",
    },
    {
      key: "events",
      label: "Federation Events",
      value: totalEvents.toLocaleString(),
      tone: "text-accent-purple",
    },
    {
      key: "latency",
      label: "Avg Latency",
      value: `${overallAvgLatencyMs.toFixed(1)}ms`,
      tone:
        overallAvgLatencyMs < 100
          ? "text-success"
          : overallAvgLatencyMs < 500
            ? "text-warning"
            : "text-destructive",
    },
    {
      key: "failures",
      label: "Failure Rate",
      value: `${(overallFailureRate * 100).toFixed(2)}%`,
      tone:
        overallFailureRate < 0.01
          ? "text-success"
          : overallFailureRate < 0.05
            ? "text-warning"
            : "text-destructive",
    },
  ]);

  function formatEventType(type: string): string {
    return type.replace(/_/g, " ").replace(/\b\w/g, (c) => c.toUpperCase());
  }

  function formatTimeAgo(dateStr: string | null | undefined): string {
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

  function eventTypeBadgeVariant(
    type: string
  ): "default" | "secondary" | "destructive" | "outline" {
    if (type.includes("failure") || type.includes("error")) return "destructive";
    if (type.includes("election") || type.includes("leader")) return "secondary";
    return "outline";
  }
</script>

<svelte:head>
  <title>Federation Overview - FrameWorks</title>
</svelte:head>

<div class="h-full flex flex-col">
  <!-- Fixed Page Header -->
  <div class="px-4 sm:px-6 lg:px-8 py-4 border-b border-[hsl(var(--tn-fg-gutter)/0.3)] shrink-0">
    <div class="flex items-center justify-between gap-4">
      <div class="flex items-center gap-3">
        <GlobeIcon class="w-5 h-5 text-primary" />
        <div>
          <h1 class="text-xl font-bold text-foreground">Federation Overview</h1>
          <p class="text-sm text-muted-foreground">
            Cross-cluster topology, peering status, and federation traffic
          </p>
        </div>
      </div>
      <div class="flex items-center gap-2">
        {#if updatedAt}
          <Tooltip>
            <TooltipTrigger>
              <span class="text-xs text-muted-foreground">{formatTimeAgo(updatedAt)}</span>
            </TooltipTrigger>
            <TooltipContent>Last updated: {new Date(updatedAt).toLocaleString()}</TooltipContent>
          </Tooltip>
        {/if}
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
            <SkeletonLoader type="text" class="w-full h-[400px]" />
          </div>
        </div>
        <div class="slab col-span-full">
          <div class="slab-header">
            <SkeletonLoader type="text-lg" class="w-36" />
          </div>
          <div class="slab-body--padded">
            {#each Array(4) as _, i (i)}
              <SkeletonLoader type="text" class="w-full mb-2" />
            {/each}
          </div>
        </div>
      </div>
    {:else if clusters.length === 0}
      <div class="p-8">
        <EmptyState
          iconName="Globe"
          title="No federation data"
          description="No clusters are reporting federation status. Clusters will appear here once they establish peer connections."
        />
      </div>
    {:else}
      <div class="dashboard-grid">
        <!-- Metric Cards -->
        <div class="slab col-span-full">
          <div class="slab-header">
            <div class="flex items-center gap-2">
              <ActivityIcon class="w-4 h-4 text-info" />
              <h3>Federation Health ({currentRange.label})</h3>
            </div>
          </div>
          <div class="slab-body--padded">
            <div class="grid grid-cols-2 md:grid-cols-3 lg:grid-cols-6 gap-4">
              {#each metricCards as stat (stat.key)}
                <InfrastructureMetricCard label={stat.label} value={stat.value} tone={stat.tone} />
              {/each}
            </div>
          </div>
        </div>

        <!-- Topology Map -->
        <div class="slab col-span-full">
          <div class="slab-header">
            <div class="flex items-center gap-2">
              <GlobeIcon class="w-4 h-4 text-info" />
              <h3>Cluster Topology</h3>
            </div>
            <p class="text-sm text-muted-foreground mt-1">
              Geographic distribution of clusters and peer connections.
            </p>
          </div>
          <div class="slab-body">
            <RoutingMap
              routes={[]}
              nodes={[]}
              clusters={mapClusters}
              relationships={mapRelationships}
              height={450}
              zoom={2}
            />
          </div>
        </div>

        <!-- Traffic Matrix -->
        {#if trafficMatrix.length > 0}
          <div class="slab col-span-full">
            <div class="slab-header">
              <div class="flex items-center gap-2">
                <NetworkIcon class="w-4 h-4 text-info" />
                <h3>Cross-Cluster Traffic</h3>
              </div>
              <p class="text-sm text-muted-foreground mt-1">
                Federation event volume and latency between cluster pairs.
              </p>
            </div>
            <div class="slab-body">
              <div class="overflow-x-auto">
                <table class="w-full text-sm">
                  <thead>
                    <tr class="border-b border-border/50 text-left">
                      <th class="px-4 py-2 font-medium text-muted-foreground">Source</th>
                      <th class="px-4 py-2 font-medium text-muted-foreground">Destination</th>
                      <th class="px-4 py-2 font-medium text-muted-foreground text-right">Events</th>
                      <th class="px-4 py-2 font-medium text-muted-foreground text-right">Success</th
                      >
                      <th class="px-4 py-2 font-medium text-muted-foreground text-right"
                        >Avg Latency</th
                      >
                      <th class="px-4 py-2 font-medium text-muted-foreground text-right"
                        >Distance</th
                      >
                      <th class="px-4 py-2 font-medium text-muted-foreground text-right"
                        >Success Rate</th
                      >
                    </tr>
                  </thead>
                  <tbody>
                    {#each trafficMatrix as row (row.clusterId + "-" + row.remoteClusterId)}
                      {@const clusterName =
                        clusters.find((c) => c.clusterId === row.clusterId)?.name ?? row.clusterId}
                      {@const remoteName =
                        clusters.find((c) => c.clusterId === row.remoteClusterId)?.name ??
                        row.remoteClusterId}
                      <tr class="border-b border-border/30 hover:bg-muted/30">
                        <td class="px-4 py-2 font-mono text-xs">{clusterName}</td>
                        <td class="px-4 py-2 font-mono text-xs">{remoteName}</td>
                        <td class="px-4 py-2 text-right">{row.eventCount.toLocaleString()}</td>
                        <td class="px-4 py-2 text-right">{row.successCount.toLocaleString()}</td>
                        <td class="px-4 py-2 text-right">{row.avgLatencyMs.toFixed(1)}ms</td>
                        <td class="px-4 py-2 text-right">{row.avgDistanceKm.toFixed(0)}km</td>
                        <td class="px-4 py-2 text-right">
                          <span
                            class={row.successRate >= 0.99
                              ? "text-success"
                              : row.successRate >= 0.95
                                ? "text-warning"
                                : "text-destructive"}
                          >
                            {(row.successRate * 100).toFixed(1)}%
                          </span>
                        </td>
                      </tr>
                    {/each}
                  </tbody>
                </table>
              </div>
            </div>
          </div>
        {/if}

        <!-- Event Type Breakdown -->
        {#if eventCounts.length > 0}
          <div class="slab col-span-full lg:col-span-6">
            <div class="slab-header">
              <div class="flex items-center gap-2">
                <ZapIcon class="w-4 h-4 text-info" />
                <h3>Event Types</h3>
              </div>
              <p class="text-sm text-muted-foreground mt-1">
                Federation events by type with failure breakdown.
              </p>
            </div>
            <div class="slab-body">
              <div class="overflow-x-auto">
                <table class="w-full text-sm">
                  <thead>
                    <tr class="border-b border-border/50 text-left">
                      <th class="px-4 py-2 font-medium text-muted-foreground">Event Type</th>
                      <th class="px-4 py-2 font-medium text-muted-foreground text-right">Count</th>
                      <th class="px-4 py-2 font-medium text-muted-foreground text-right"
                        >Failures</th
                      >
                      <th class="px-4 py-2 font-medium text-muted-foreground text-right"
                        >Avg Latency</th
                      >
                    </tr>
                  </thead>
                  <tbody>
                    {#each eventCounts as ec (ec.eventType)}
                      <tr class="border-b border-border/30 hover:bg-muted/30">
                        <td class="px-4 py-2">
                          <Badge variant={eventTypeBadgeVariant(ec.eventType)}>
                            {formatEventType(ec.eventType)}
                          </Badge>
                        </td>
                        <td class="px-4 py-2 text-right">{ec.count.toLocaleString()}</td>
                        <td class="px-4 py-2 text-right">
                          {#if ec.failureCount > 0}
                            <span class="text-destructive">{ec.failureCount.toLocaleString()}</span>
                          {:else}
                            <span class="text-muted-foreground">0</span>
                          {/if}
                        </td>
                        <td class="px-4 py-2 text-right">{ec.avgLatencyMs.toFixed(1)}ms</td>
                      </tr>
                    {/each}
                  </tbody>
                </table>
              </div>
            </div>
          </div>
        {/if}

        <!-- Recent Federation Events -->
        <div class="slab col-span-full">
          <div class="slab-header flex items-start justify-between gap-4">
            <div>
              <div class="flex items-center gap-2">
                <RefreshCwIcon class="w-4 h-4 text-info" />
                <h3>Recent Events</h3>
              </div>
              <p class="text-sm text-muted-foreground mt-1">
                Latest federation events across all clusters.
              </p>
            </div>
            <Tooltip>
              <TooltipTrigger
                class="text-[10px] uppercase tracking-wide text-muted-foreground border border-border/50 px-2 py-1"
              >
                Auto-refresh 30s
              </TooltipTrigger>
              <TooltipContent>Data refreshes every 30 seconds</TooltipContent>
            </Tooltip>
          </div>
          <div class="slab-body">
            {#if federationEvents.length === 0}
              <div class="p-6 text-center text-muted-foreground text-sm">
                No federation events in the selected time range.
              </div>
            {:else}
              <div class="overflow-x-auto max-h-[400px] overflow-y-auto">
                <table class="w-full text-sm">
                  <thead class="sticky top-0 bg-background">
                    <tr class="border-b border-border/50 text-left">
                      <th class="px-4 py-2 font-medium text-muted-foreground">Time</th>
                      <th class="px-4 py-2 font-medium text-muted-foreground">Type</th>
                      <th class="px-4 py-2 font-medium text-muted-foreground">Local Cluster</th>
                      <th class="px-4 py-2 font-medium text-muted-foreground">Remote Cluster</th>
                      <th class="px-4 py-2 font-medium text-muted-foreground text-right">Latency</th
                      >
                      <th class="px-4 py-2 font-medium text-muted-foreground">Details</th>
                    </tr>
                  </thead>
                  <tbody>
                    {#each federationEvents as evt, i (evt.timestamp + "-" + i)}
                      {@const localName =
                        clusters.find((c) => c.clusterId === evt.localCluster)?.name ??
                        evt.localCluster}
                      {@const remoteName =
                        clusters.find((c) => c.clusterId === evt.remoteCluster)?.name ??
                        evt.remoteCluster}
                      <tr class="border-b border-border/30 hover:bg-muted/30">
                        <td class="px-4 py-2 text-xs text-muted-foreground whitespace-nowrap">
                          {formatTimeAgo(evt.timestamp)}
                        </td>
                        <td class="px-4 py-2">
                          <Badge
                            variant={eventTypeBadgeVariant(evt.eventType)}
                            class="text-[0.65rem]"
                          >
                            {formatEventType(evt.eventType)}
                          </Badge>
                        </td>
                        <td class="px-4 py-2 font-mono text-xs">{localName}</td>
                        <td class="px-4 py-2 font-mono text-xs">{remoteName}</td>
                        <td class="px-4 py-2 text-right text-xs">
                          {evt.latencyMs != null ? `${evt.latencyMs.toFixed(0)}ms` : "—"}
                        </td>
                        <td class="px-4 py-2 text-xs text-muted-foreground max-w-[200px] truncate">
                          {#if evt.failureReason}
                            <span class="text-destructive">{evt.failureReason}</span>
                          {:else if evt.streamName}
                            {evt.streamName}
                          {:else if evt.role}
                            {evt.role}
                          {:else}
                            —
                          {/if}
                        </td>
                      </tr>
                    {/each}
                  </tbody>
                </table>
              </div>
            {/if}
          </div>
        </div>
      </div>
    {/if}
  </div>
</div>

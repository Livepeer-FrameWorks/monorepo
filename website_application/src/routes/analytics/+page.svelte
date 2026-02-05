<script lang="ts">
  import { onMount } from "svelte";
  import { get } from "svelte/store";
  import { goto } from "$app/navigation";
  import { resolve } from "$app/paths";
  import { auth } from "$lib/stores/auth";
  import {
    fragment,
    GetStreamsConnectionStore,
    GetPlatformOverviewStore,
    GetStreamAnalyticsSummariesConnectionStore,
    StreamStatus,
    StreamCoreFieldsStore,
    StreamMetricsListFieldsStore,
  } from "$houdini";
  import { toast } from "$lib/stores/toast.js";
  import { getIconComponent } from "$lib/iconUtils";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import { Button } from "$lib/components/ui/button";
  import { Badge } from "$lib/components/ui/badge";
  import { Select, SelectContent, SelectItem, SelectTrigger } from "$lib/components/ui/select";
  import { GridSeam } from "$lib/components/layout";
  import DashboardMetricCard from "$lib/components/shared/DashboardMetricCard.svelte";
  import { resolveTimeRange, TIME_RANGE_OPTIONS } from "$lib/utils/time-range";

  // Houdini stores
  const streamsStore = new GetStreamsConnectionStore();
  const platformOverviewStore = new GetPlatformOverviewStore();
  const summariesStore = new GetStreamAnalyticsSummariesConnectionStore();

  // Fragment stores for unmasking nested data
  const streamCoreStore = new StreamCoreFieldsStore();
  const streamMetricsStore = new StreamMetricsListFieldsStore();

  let isAuthenticated = false;
  let timeRange = $state("7d");
  let currentRange = $derived(resolveTimeRange(timeRange));
  const timeRangeOptions = TIME_RANGE_OPTIONS.filter((option) =>
    ["24h", "7d", "30d"].includes(option.value)
  );

  // Derived state from Houdini stores
  let loading = $derived(
    $streamsStore.fetching || $platformOverviewStore.fetching || $summariesStore.fetching
  );

  // Get masked data
  let maskedNodes = $derived(
    $streamsStore.data?.streamsConnection?.edges?.map((e) => e.node) ?? []
  );

  // Unmask streams with fragment() and get() pattern
  let streams = $derived(
    maskedNodes.map((node) => {
      const core = get(fragment(node, streamCoreStore));
      const metrics = node.metrics ? get(fragment(node.metrics, streamMetricsStore)) : null;
      return { ...core, metrics };
    })
  );

  let platformOverview = $derived($platformOverviewStore.data?.analytics?.overview ?? null);

  // Build a map of stream IDs to stream data for lookups
  let streamLookup = $derived(new Map(streams.map((s) => [s.id, s])));

  // Top streams from pre-aggregated summaries (server-side aggregation)
  let topStreamsByUsage = $derived.by(() => {
    const edges =
      $summariesStore.data?.analytics?.usage?.streaming?.streamAnalyticsSummariesConnection
        ?.edges ?? [];
    if (edges.length === 0) return [];

    return edges.map((edge) => {
      const node = edge.node;
      const streamData = node.stream ?? streamLookup.get(node.streamId);
      return {
        streamId: node.streamId,
        displayName: streamData?.name ?? node.streamId,
        status: streamData?.metrics?.status ?? null,
        totalViews: node.rangeTotalViews ?? 0,
        uniqueViewers: node.rangeUniqueViewers ?? 0,
        egressGb: node.rangeEgressGb ?? 0,
        percentage: node.rangeEgressSharePercent ?? 0,
      };
    });
  });

  // Subscribe to auth store
  auth.subscribe((authState) => {
    isAuthenticated = authState.isAuthenticated;
  });

  onMount(async () => {
    if (!isAuthenticated) {
      await auth.checkAuth();
    }
    await loadData();
  });

  async function loadData() {
    try {
      const range = resolveTimeRange(timeRange);

      // Load streams, platform overview, and stream summaries in parallel
      await Promise.all([
        streamsStore.fetch(),
        platformOverviewStore
          .fetch({
            variables: { timeRange: { start: range.start, end: range.end }, days: range.days },
          })
          .catch(() => null),
        summariesStore
          .fetch({
            variables: {
              timeRange: { start: range.start, end: range.end },
              sortBy: "EGRESS_GB",
              sortOrder: "DESC",
              first: 10,
            },
          })
          .catch(() => null),
      ]);
    } catch (error) {
      console.error("Failed to load data:", error);
      toast.error("Failed to load analytics data. Please refresh the page.");
    }
  }

  function formatNumber(num: number | undefined | null) {
    if (num === undefined || num === null) return "0";
    if (num >= 1000000) {
      return (num / 1000000).toFixed(1) + "M";
    } else if (num >= 1000) {
      return (num / 1000).toFixed(1) + "K";
    }
    return num.toString();
  }

  // Icons
  const ChartLineIcon = getIconComponent("ChartLine");
  const UsersIcon = getIconComponent("Users");
  const TrendingUpIcon = getIconComponent("TrendingUp");
  const VideoIcon = getIconComponent("Video");
  const ClockIcon = getIconComponent("Clock");
  const CalendarIcon = getIconComponent("Calendar");
  const BarChart2Icon = getIconComponent("BarChart2");
</script>

<svelte:head>
  <title>Analytics - FrameWorks</title>
</svelte:head>

<div class="h-full flex flex-col">
  <!-- Fixed Page Header -->
  <div
    class="px-4 sm:px-6 lg:px-8 py-4 border-b border-[hsl(var(--tn-fg-gutter)/0.3)] shrink-0 flex justify-between items-center"
  >
    <div class="flex items-center gap-3">
      <ChartLineIcon class="w-5 h-5 text-primary" />
      <div>
        <h1 class="text-xl font-bold text-foreground">Analytics</h1>
        <p class="text-sm text-muted-foreground">
          Monitor your streaming performance and viewer engagement
        </p>
      </div>
    </div>
    <div class="flex items-center gap-2">
      <Select
        value={timeRange}
        onValueChange={(value) => {
          timeRange = value;
          loadData();
        }}
        type="single"
      >
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
      <Button href={resolve("/analytics/usage")} variant="outline" size="sm" class="gap-2">
        <TrendingUpIcon class="w-4 h-4" />
        Usage & Costs
      </Button>
    </div>
  </div>

  <!-- Scrollable Content -->
  <div class="flex-1 overflow-y-auto">
    {#if loading}
      <div class="px-4 sm:px-6 lg:px-8 py-6">
        <div class="flex items-center justify-center min-h-64">
          <div class="loading-spinner w-8 h-8"></div>
        </div>
      </div>
    {:else}
      <div class="page-transition">
        <!-- Platform Overview Stats -->
        {#if platformOverview}
          <div
            class="px-4 sm:px-6 lg:px-8 py-2 bg-muted/30 border-b border-[hsl(var(--tn-fg-gutter)/0.2)] flex items-center justify-between"
          >
            <span class="text-xs text-muted-foreground uppercase tracking-wide"
              >Platform Overview</span
            >
            <Badge
              variant="outline"
              class="text-[10px] px-1.5 py-0 text-muted-foreground border-muted-foreground/30"
            >
              {currentRange.label}
            </Badge>
          </div>
          <GridSeam cols={4} stack="2x2" surface="panel" flush={true} class="mb-0">
            <div>
              <DashboardMetricCard
                icon={VideoIcon}
                iconColor="text-primary"
                value={formatNumber(platformOverview.totalStreams)}
                valueColor="text-primary"
                label="Total Streams"
              />
            </div>
            <div>
              <DashboardMetricCard
                icon={UsersIcon}
                iconColor="text-success"
                value={formatNumber(platformOverview.totalViewers)}
                valueColor="text-success"
                label="Total Viewers"
              />
            </div>
            <div>
              <DashboardMetricCard
                icon={ClockIcon}
                iconColor="text-warning"
                value={platformOverview.viewerHours != null
                  ? `${platformOverview.viewerHours.toFixed(1)}h`
                  : "0h"}
                valueColor="text-warning"
                label="Viewer Hours"
              />
            </div>
            <div>
              <DashboardMetricCard
                icon={TrendingUpIcon}
                iconColor="text-purple-500"
                value={formatNumber(platformOverview.peakConcurrentViewers)}
                valueColor="text-purple-500"
                label="Peak Concurrent"
              />
            </div>
          </GridSeam>
        {/if}

        <!-- Main Content -->
        <div class="px-4 sm:px-6 lg:px-8 py-6 space-y-6">
          <!-- Top Streams by Usage -->
          {#if topStreamsByUsage.length > 0}
            <div class="slab">
              <div class="slab-header flex justify-between items-center">
                <div class="flex items-center gap-2">
                  <TrendingUpIcon class="w-4 h-4 text-warning" />
                  <h3>Top Streams</h3>
                </div>
                <span class="text-xs text-muted-foreground">
                  {topStreamsByUsage.length} streams by bandwidth
                </span>
              </div>
              <div class="slab-body--flush overflow-x-auto">
                <table class="w-full text-sm">
                  <thead>
                    <tr
                      class="border-b border-border/50 text-muted-foreground text-xs uppercase tracking-wide"
                    >
                      <th class="text-left py-3 px-4">Stream</th>
                      <th class="text-center py-3 px-4">Status</th>
                      <th class="text-right py-3 px-4">Views</th>
                      <th class="text-right py-3 px-4">Unique Viewers</th>
                      <th class="text-right py-3 px-4">Bandwidth</th>
                      <th class="text-right py-3 px-4">Share</th>
                      <th class="text-right py-3 px-4"></th>
                    </tr>
                  </thead>
                  <tbody>
                    {#each topStreamsByUsage as stream (stream.streamId)}
                      <tr
                        class="border-b border-border/30 hover:bg-muted/10 cursor-pointer"
                        onclick={() => goto(resolve(`/streams/${stream.streamId}/analytics`))}
                      >
                        <td class="py-3 px-4">
                          <span class="font-medium text-foreground">
                            {stream.displayName}
                          </span>
                        </td>
                        <td class="py-3 px-4 text-center">
                          <Badge
                            variant="outline"
                            class={stream.status === StreamStatus.LIVE
                              ? "bg-success/10 text-success border-success/20"
                              : "bg-muted text-muted-foreground"}
                          >
                            {stream.status?.toLowerCase() || "offline"}
                          </Badge>
                        </td>
                        <td class="py-3 px-4 text-right font-mono">
                          {stream.totalViews.toLocaleString()}
                        </td>
                        <td class="py-3 px-4 text-right font-mono">
                          {stream.uniqueViewers.toLocaleString()}
                        </td>
                        <td class="py-3 px-4 text-right font-mono text-info">
                          {stream.egressGb.toFixed(2)} GB
                        </td>
                        <td class="py-3 px-4 text-right">
                          <div class="flex items-center justify-end gap-2">
                            <div class="w-16 h-1.5 bg-muted rounded-full overflow-hidden">
                              <div
                                class="h-full bg-warning"
                                style="width: {Math.min(stream.percentage, 100)}%"
                              ></div>
                            </div>
                            <span class="font-mono text-xs w-12 text-right"
                              >{stream.percentage.toFixed(1)}%</span
                            >
                          </div>
                        </td>
                        <td class="py-3 px-4 text-right">
                          <Button
                            variant="ghost"
                            size="sm"
                            class="gap-1 text-muted-foreground hover:text-foreground"
                            onclick={(e) => {
                              e.stopPropagation();
                              goto(resolve(`/streams/${stream.streamId}/analytics`));
                            }}
                          >
                            <BarChart2Icon class="w-3 h-3" />
                            Analytics
                          </Button>
                        </td>
                      </tr>
                    {/each}
                  </tbody>
                </table>
              </div>
              <div class="slab-actions">
                <Button href={resolve("/streams")} variant="ghost" class="gap-2">
                  <VideoIcon class="w-4 h-4" />
                  Manage All Streams
                </Button>
              </div>
            </div>
          {:else if streams.length > 0}
            <!-- No analytics data but streams exist -->
            <div class="slab">
              <div class="slab-header">
                <h3>Your Streams</h3>
              </div>
              <div class="slab-body--flush">
                <table class="w-full text-sm">
                  <thead>
                    <tr
                      class="border-b border-border/50 text-muted-foreground text-xs uppercase tracking-wide"
                    >
                      <th class="text-left py-3 px-4">Stream</th>
                      <th class="text-center py-3 px-4">Status</th>
                      <th class="text-right py-3 px-4"></th>
                    </tr>
                  </thead>
                  <tbody>
                    {#each streams.slice(0, 10) as stream (stream.id)}
                      <tr
                        class="border-b border-border/30 hover:bg-muted/10 cursor-pointer"
                        onclick={() => goto(resolve(`/streams/${stream.id}/analytics`))}
                      >
                        <td class="py-3 px-4">
                          <span class="font-medium text-foreground">
                            {stream.name}
                          </span>
                        </td>
                        <td class="py-3 px-4 text-center">
                          <Badge
                            variant="outline"
                            class={stream.metrics?.status === StreamStatus.LIVE
                              ? "bg-success/10 text-success border-success/20"
                              : "bg-muted text-muted-foreground"}
                          >
                            {stream.metrics?.status?.toLowerCase() || "offline"}
                          </Badge>
                        </td>
                        <td class="py-3 px-4 text-right">
                          <Button
                            variant="ghost"
                            size="sm"
                            class="gap-1 text-muted-foreground hover:text-foreground"
                            onclick={(e) => {
                              e.stopPropagation();
                              goto(resolve(`/streams/${stream.id}/analytics`));
                            }}
                          >
                            <BarChart2Icon class="w-3 h-3" />
                            Analytics
                          </Button>
                        </td>
                      </tr>
                    {/each}
                  </tbody>
                </table>
              </div>
              <div class="slab-actions">
                <Button href={resolve("/streams")} variant="ghost" class="gap-2">
                  <VideoIcon class="w-4 h-4" />
                  Manage All Streams
                </Button>
              </div>
            </div>
          {:else}
            <!-- No streams at all -->
            <div class="slab">
              <div class="slab-body--padded">
                <EmptyState
                  iconName="ChartLine"
                  title="No streams found"
                  description="Create a stream to start seeing analytics data"
                  actionText="Go to Streams"
                  onAction={() => goto(resolve("/streams"))}
                />
              </div>
            </div>
          {/if}
        </div>
      </div>
    {/if}
  </div>
</div>

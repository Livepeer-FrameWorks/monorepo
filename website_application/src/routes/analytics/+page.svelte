<script lang="ts">
  import { onMount, onDestroy, untrack } from "svelte";
  import { get } from "svelte/store";
  import { goto } from "$app/navigation";
  import { resolve } from "$app/paths";
  import { auth } from "$lib/stores/auth";
  import {
    fragment,
    GetStreamsConnectionStore,
    GetPlatformOverviewStore,
    GetStreamWithAnalyticsStore,
    ViewerMetricsStreamStore,
    StreamStatus,
    StreamCoreFieldsStore,
    StreamMetricsFieldsStore,
  } from "$houdini";
  import type { ViewerMetricsStream$result } from "$houdini";
  import { toast } from "$lib/stores/toast.js";
  import { getIconComponent } from "$lib/iconUtils";
  import LoadingCard from "$lib/components/LoadingCard.svelte";
  import SkeletonLoader from "$lib/components/SkeletonLoader.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import { Button } from "$lib/components/ui/button";
  import { Badge } from "$lib/components/ui/badge";
  import {
    Table,
    TableBody,
    TableCell,
    TableHead,
    TableHeader,
    TableRow,
  } from "$lib/components/ui/table";
  import { GridSeam } from "$lib/components/layout";
  import DashboardMetricCard from "$lib/components/shared/DashboardMetricCard.svelte";
  import ViewerTrendChart from "$lib/components/charts/ViewerTrendChart.svelte";
  import QualityTierChart from "$lib/components/charts/QualityTierChart.svelte";
  import CodecDistributionChart from "$lib/components/charts/CodecDistributionChart.svelte";

  // Houdini stores
  const streamsStore = new GetStreamsConnectionStore();
  const platformOverviewStore = new GetPlatformOverviewStore();
  const streamAnalyticsStore = new GetStreamWithAnalyticsStore();
  const viewerMetricsSub = new ViewerMetricsStreamStore();

  // Fragment stores for unmasking nested data
  const streamCoreStore = new StreamCoreFieldsStore();
  const streamMetricsStore = new StreamMetricsFieldsStore();

  let isAuthenticated = false;
  let user: any = null;

  // Derived state from Houdini stores
  let loading = $derived($streamsStore.fetching || $platformOverviewStore.fetching);

  // Get masked data
  let maskedNodes = $derived($streamsStore.data?.streamsConnection?.edges?.map(e => e.node) ?? []);

  // Unmask streams with fragment() and get() pattern
  let streams = $derived(
    maskedNodes.map(node => {
      const core = get(fragment(node, streamCoreStore));
      const metrics = node.metrics ? get(fragment(node.metrics, streamMetricsStore)) : null;
      return { ...core, metrics };
    })
  );

  let platformOverview = $derived($platformOverviewStore.data?.platformOverview ?? null);
  let analyticsData = $derived($streamAnalyticsStore.data?.stream?.analytics ?? null);
  let viewerMetrics = $derived($streamAnalyticsStore.data?.stream?.viewerTimeSeriesConnection?.edges?.map(e => e.node) ?? []);
  let qualityTierData = $derived.by(() => {
    const edges = $streamAnalyticsStore.data?.stream?.qualityDailyConnection?.edges ?? [];
    if (edges.length === 0) return null;
    // Aggregate all daily records into a single summary
    type QualityEdge = NonNullable<typeof edges>[0];
    return edges.reduce(
      (acc: { tier1080pMinutes: number; tier720pMinutes: number; tier480pMinutes: number; tierSdMinutes: number }, edge: QualityEdge) => ({
        tier1080pMinutes: acc.tier1080pMinutes + (edge.node.tier1080pMinutes || 0),
        tier720pMinutes: acc.tier720pMinutes + (edge.node.tier720pMinutes || 0),
        tier480pMinutes: acc.tier480pMinutes + (edge.node.tier480pMinutes || 0),
        tierSdMinutes: acc.tierSdMinutes + (edge.node.tierSdMinutes || 0),
      }),
      { tier1080pMinutes: 0, tier720pMinutes: 0, tier480pMinutes: 0, tierSdMinutes: 0 }
    );
  });

  // Aggregate codec data for chart
  type CodecData = { codec: string; minutes: number };

  let codecData = $derived.by((): CodecData[] => {
    const edges = $streamAnalyticsStore.data?.stream?.qualityDailyConnection?.edges ?? [];
    if (edges.length === 0) return [];

    // Sum minutes by codec
    let h264 = 0;
    let h265 = 0;
    // Note: If you add more codecs to the backend query, add them here
    type QualityEdge = NonNullable<typeof edges>[0];

    edges.forEach((edge: QualityEdge) => {
      h264 += edge.node.codecH264Minutes || 0;
      h265 += edge.node.codecH265Minutes || 0;
    });

    return [
      { codec: 'H.264', minutes: h264 },
      { codec: 'H.265', minutes: h265 },
    ].filter(d => d.minutes > 0);
  });

  // Mutable selected stream state
  type StreamData = NonNullable<typeof streams>[0];
  let selectedStream = $state<StreamData | null>(null);

  // Live viewer activity (recent connect/disconnect events)
  interface LiveViewerEvent {
    action: string;
    clientCity?: string | null;
    clientCountry?: string | null;
    protocol: string;
    timestamp: number;
    nodeId: string;
  }
  let liveViewerActivity = $state<LiveViewerEvent[]>([]);
  let liveActivityPulse = $state(false);

  // Subscribe to auth store
  auth.subscribe((authState) => {
    isAuthenticated = authState.isAuthenticated;
    user = authState.user || null;
  });

  onMount(async () => {
    if (!isAuthenticated) {
      await auth.checkAuth();
    }
    await loadData();
  });

  onDestroy(() => {
    viewerMetricsSub.unlisten();
  });

  async function loadData() {
    try {
      const timeRange = {
        start: new Date(Date.now() - 7 * 24 * 60 * 60 * 1000).toISOString(),
        end: new Date().toISOString()
      };

      // Load streams and platform overview in parallel
      await Promise.all([
        streamsStore.fetch(),
        platformOverviewStore.fetch({ variables: { timeRange } }).catch(() => null),
      ]);

      // Select first stream if available (use unmasked streams)
      if (streams.length > 0) {
        selectedStream = streams[0];
        await loadAnalyticsForStream(selectedStream.id);
        startRealTimeSubscriptions();
      }
    } catch (error) {
      console.error("Failed to load data:", error);
      toast.error("Failed to load analytics data. Please refresh the page.");
    }
  }

  async function loadAnalyticsForStream(streamId: string) {
    if (!streamId) return;

    const timeRange = {
      start: new Date(Date.now() - 24 * 60 * 60 * 1000).toISOString(),
      end: new Date().toISOString()
    };
    // Longer range for quality tier daily aggregates (7 days)
    const qualityTimeRange = {
      start: new Date(Date.now() - 7 * 24 * 60 * 60 * 1000).toISOString(),
      end: new Date().toISOString()
    };

    try {
      await streamAnalyticsStore.fetch({
        variables: {
          id: streamId,
          timeRange,
          qualityTimeRange,
          interval: "5m"
        }
      }).catch(() => null);
    } catch (error) {
      console.error("Failed to load analytics for stream:", error);
      toast.warning(
        "Failed to load analytics for selected stream. Some data may be unavailable.",
      );
    }
  }

  function startRealTimeSubscriptions() {
    if (!selectedStream || !user) return;

    viewerMetricsSub.unlisten();
    viewerMetricsSub.listen({ stream: selectedStream.id });
  }

  // Effect to handle viewer metrics subscription updates
  $effect(() => {
    const event = $viewerMetricsSub.data?.viewerMetrics;
    if (event) {
      // Wrap mutations in untrack to prevent reading liveViewerActivity from creating a dependency
      untrack(() => {
        const newEvent: LiveViewerEvent = {
          action: event.action,
          clientCity: event.clientCity,
          clientCountry: event.clientCountry,
          protocol: event.protocol,
          timestamp: event.timestamp,
          nodeId: event.nodeId,
        };
        liveViewerActivity = [newEvent, ...liveViewerActivity.slice(0, 9)];

        liveActivityPulse = true;
        setTimeout(() => { liveActivityPulse = false; }, 500);
      });
    }
  });

  async function selectStream(stream: StreamData) {
    selectedStream = stream;
    liveViewerActivity = [];
    await loadAnalyticsForStream(stream.id);
    startRealTimeSubscriptions();
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

  function hasValue(value: any): boolean {
    return value !== null && value !== undefined;
  }

  function healthScoreClass(score: number) {
    if (!hasValue(score)) return "";
    if (score >= 0.9) return "text-success";
    if (score >= 0.7) return "text-warning";
    return "text-destructive";
  }

  function bufferStateClass(state: string | null | undefined) {
    switch (state) {
      case "FULL":
        return "text-success";
      case "DRY":
        return "text-destructive";
      default:
        return "text-warning";
    }
  }

  // Icons
  const ChartLineIcon = getIconComponent("ChartLine");
  const UsersIcon = getIconComponent("Users");
  const ZapIcon = getIconComponent("Zap");
  const TrendingUpIcon = getIconComponent("TrendingUp");
  const GaugeIcon = getIconComponent("Gauge");
  const VideoIcon = getIconComponent("Video");
  const WifiIcon = getIconComponent("Wifi");
  const DatabaseIcon = getIconComponent("Database");
  const ClockIcon = getIconComponent("Clock");

  // Derived stats
  function formatDuration(seconds: number | undefined | null) {
    if (!seconds) return "0s";
    if (seconds < 60) return `${Math.round(seconds)}s`;
    if (seconds < 3600) return `${Math.floor(seconds / 60)}m ${Math.round(seconds % 60)}s`;
    const hours = Math.floor(seconds / 3600);
    const mins = Math.floor((seconds % 3600) / 60);
    return `${hours}h ${mins}m`;
  }

  const streamSummaryCards = $derived(
    analyticsData
      ? [
          { key: "totalViews", label: "Total Views", value: formatNumber(analyticsData.totalViews), tone: "text-primary" },
          { key: "peakViewers", label: "Peak Viewers", value: formatNumber(analyticsData.peakViewers), tone: "text-success" },
          { key: "avgViewers", label: "Avg Viewers", value: Math.round(analyticsData.avgViewers ?? 0), tone: "text-accent-purple" },
          { key: "uniqueViewers", label: "Unique Viewers", value: formatNumber(analyticsData.uniqueViewers), tone: "text-warning" },
          { key: "sessionDuration", label: "Session Duration", value: formatDuration(analyticsData.totalSessionDuration), tone: "text-info" },
        ]
      : [],
  );

  const qualityMetricsCards = $derived(
    analyticsData
      ? [
          analyticsData.uniqueCountries && { key: "countries", label: "Countries", value: analyticsData.uniqueCountries, tone: "text-info" },
          analyticsData.uniqueCities && { key: "cities", label: "Cities", value: analyticsData.uniqueCities, tone: "text-accent-purple" },
          hasValue(analyticsData.avgBufferHealth) && {
            key: "bufferHealth", label: "Buffer Health",
            value: `${Math.round((analyticsData.avgBufferHealth ?? 0) * 100)}%`,
            tone: analyticsData.avgBufferHealth && analyticsData.avgBufferHealth >= 0.8 ? "text-success" : analyticsData.avgBufferHealth && analyticsData.avgBufferHealth >= 0.5 ? "text-warning" : "text-destructive",
          },
          hasValue(analyticsData.avgBitrate) && {
            key: "avgBitrate", label: "Avg Bitrate",
            // avgBitrate is in kbps, divide by 1,000 for Mbps
            value: `${((analyticsData.avgBitrate ?? 0) / 1_000).toFixed(1)} Mbps`,
            tone: "text-primary",
          },
        ].filter(Boolean) as { key: string; label: string; value: string | number; tone: string }[]
      : [],
  );

  const hasVideoQuality = $derived(
    !!(analyticsData?.currentResolution || analyticsData?.currentCodec || analyticsData?.bitrateKbps || analyticsData?.currentFps),
  );
  const hasPerformanceMetrics = $derived(Boolean(analyticsData?.qualityTier));
  const hasBufferInsights = $derived(Boolean(analyticsData?.currentBufferState) || Boolean(analyticsData?.currentIssues));
  const hasHealthDetails = $derived(hasVideoQuality || hasPerformanceMetrics || hasBufferInsights);
</script>

<svelte:head>
  <title>Analytics - FrameWorks</title>
</svelte:head>

<div class="h-full flex flex-col">
  <!-- Fixed Page Header -->
  <div class="px-4 sm:px-6 lg:px-8 py-4 border-b border-[hsl(var(--tn-fg-gutter)/0.3)] shrink-0 flex justify-between items-center">
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
      <Button href={resolve("/analytics/usage")} variant="outline" size="sm" class="gap-2">
        <TrendingUpIcon class="w-4 h-4" />
        Usage
      </Button>
      <Button href={resolve("/analytics/storage")} variant="outline" size="sm" class="gap-2">
        <DatabaseIcon class="w-4 h-4" />
        Storage
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
              value={platformOverview.viewerHours != null ? `${platformOverview.viewerHours.toFixed(1)}h` : "0h"}
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

      <!-- Separator -->
      <div class="section-divider py-8">
        <div class="section-divider__bar"></div>
      </div>

      <!-- Stream Selection Table -->
      {#if streams.length > 0}
        <div class="slab border-t border-b border-[hsl(var(--tn-fg-gutter)/0.3)]">
          <div class="slab-header flex justify-between items-center py-4">
            <div class="flex flex-col gap-1">
              <h3>Select Stream for Analysis</h3>
              <p class="text-xs text-muted-foreground font-normal normal-case tracking-normal">
                Click a row to update the detailed analytics view below
              </p>
            </div>
            <Button href={resolve("/streams")} variant="ghost" size="sm" class="gap-2 text-muted-foreground hover:text-foreground">
              <VideoIcon class="w-4 h-4" />
              Manage Streams
            </Button>
          </div>
          <div class="slab-body--flush">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Name</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead class="text-right">Actions</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {#each streams as stream (stream.id ?? stream.name)}
                  <TableRow
                    class="cursor-pointer transition-colors hover:bg-muted/50 {selectedStream?.id === stream.id ? 'bg-primary/20 border-l-2 border-l-primary font-semibold' : ''}"
                    onclick={() => selectStream(stream)}
                  >
                    <TableCell class="font-medium">
                      {stream.name}
                    </TableCell>
                    <TableCell>
                      <Badge variant="outline" class="{stream.metrics?.status === StreamStatus.LIVE ? 'bg-success/10 text-success border-success/20' : 'bg-muted text-muted-foreground'}">
                        {stream.metrics?.status || "offline"}
                      </Badge>
                    </TableCell>
                    <TableCell class="text-right">
                       <Button variant="ghost" size="icon" class="h-8 w-8 text-muted-foreground" onclick={(e) => { e.stopPropagation(); goto(resolve(`/streams/${stream.id}`)); }}>
                          <ChartLineIcon class="h-4 w-4" />
                       </Button>
                    </TableCell>
                  </TableRow>
                {/each}
              </TableBody>
            </Table>
          </div>
        </div>
      {/if}

      <!-- Separator for Stream Context -->
      {#if selectedStream}
        <div class="flex items-center gap-4 py-8 px-4 sm:px-0 max-w-5xl mx-auto w-full">
           <div class="h-px flex-1 bg-[hsl(var(--tn-fg-gutter)/0.3)]"></div>
           <span class="text-xs font-medium text-muted-foreground uppercase tracking-widest whitespace-nowrap">
             Analytics for: <span class="text-primary">{selectedStream.name}</span>
           </span>
           <div class="h-px flex-1 bg-[hsl(var(--tn-fg-gutter)/0.3)]"></div>
        </div>
      {/if}

      <!-- Main Content Grid -->
      <div class="dashboard-grid">
        <!-- Stream Analytics Slab -->
        {#if selectedStream}
          <div class="slab">
            <div class="slab-header">
              <h3>Overview</h3>
            </div>
            <div class="slab-body--padded space-y-4">
              {#if analyticsData}
                <!-- Primary Stats -->
                {#if streamSummaryCards.length > 0}
                  <div class="grid grid-cols-2 gap-3">
                    {#each streamSummaryCards as stat (stat.key)}
                      <div class="p-3 text-center border border-border bg-muted/30">
                        <p class="text-xs text-muted-foreground uppercase tracking-wide mb-1">{stat.label}</p>
                        <p class="text-xl font-bold {stat.tone}">{stat.value}</p>
                      </div>
                    {/each}
                  </div>
                {/if}

                <!-- Quality & Health -->
                {#if qualityMetricsCards.length > 0}
                  <div class="pt-3 border-t border-border/30">
                    <p class="text-xs text-muted-foreground uppercase tracking-wide mb-3">Quality & Reach</p>
                    <div class="grid grid-cols-2 sm:grid-cols-3 gap-2">
                      {#each qualityMetricsCards as stat (stat.key)}
                        <div class="p-2 text-center border border-border/50 bg-background/50">
                          <p class="text-[10px] text-muted-foreground uppercase">{stat.label}</p>
                          <p class="text-sm font-semibold {stat.tone}">{stat.value}</p>
                        </div>
                      {/each}
                    </div>
                  </div>
                {/if}

                <!-- Health Details -->
                {#if hasHealthDetails}
                  <div class="pt-3 border-t border-border/30">
                    <p class="text-xs text-muted-foreground uppercase tracking-wide mb-3">Stream Health</p>
                    <div class="grid grid-cols-1 sm:grid-cols-3 gap-3">
                      {#if hasVideoQuality}
                        <div class="p-3 border border-border/50 bg-background/50">
                          <p class="text-[10px] text-muted-foreground uppercase mb-2">Video Quality</p>
                          <div class="space-y-1 text-sm font-mono">
                            {#if analyticsData.currentResolution}<p class="text-primary">{analyticsData.currentResolution}</p>{/if}
                            {#if analyticsData.currentCodec}<p class="text-accent-purple">{analyticsData.currentCodec}</p>{/if}
                            {#if analyticsData.bitrateKbps}<p class="text-success">{Math.round(analyticsData.bitrateKbps)}k</p>{/if}
                            {#if analyticsData.currentFps}<p class="text-warning">{analyticsData.currentFps.toFixed(1)} fps</p>{/if}
                          </div>
                        </div>
                      {/if}
                      {#if hasPerformanceMetrics}
                        <div class="p-3 border border-border/50 bg-background/50">
                          <p class="text-[10px] text-muted-foreground uppercase mb-2">Performance</p>
                          {#if analyticsData.qualityTier}
                            <p class="text-sm"><span class="text-muted-foreground">Tier:</span> <span class="font-mono text-accent-purple">{analyticsData.qualityTier}</span></p>
                          {/if}
                        </div>
                      {/if}
                      {#if hasBufferInsights}
                        <div class="p-3 border border-border/50 bg-background/50">
                          <p class="text-[10px] text-muted-foreground uppercase mb-2">Buffer</p>
                          {#if analyticsData.currentBufferState}
                            <p class="text-sm"><span class="text-muted-foreground">State:</span> <span class="font-mono {bufferStateClass(analyticsData.currentBufferState)}">{analyticsData.currentBufferState}</span></p>
                          {/if}
                          {#if analyticsData.currentIssues}
                            <p class="text-xs text-destructive mt-1">{analyticsData.currentIssues}</p>
                          {/if}
                        </div>
                      {/if}
                    </div>
                  </div>
                {/if}
              {:else}
                <EmptyState
                  iconName="Gauge"
                  title="No analytics data available for this stream"
                  description="Start streaming to generate analytics data. Ensure your stream is active and configured correctly."
                  actionText="Go to Streams"
                  onAction={() => goto(resolve("/streams"))}
                />
              {/if}
            </div>

          </div>
        {/if}

        <!-- Viewer Trend Slab -->
        {#if selectedStream}
          <div class="slab">
            <div class="slab-header">
              <h3>Viewer Trend</h3>
            </div>
            <div class="slab-body--padded">
              {#if viewerMetrics.length > 0}
                <ViewerTrendChart
                  data={viewerMetrics.map(m => ({ timestamp: m.timestamp, viewers: m.viewerCount }))}
                  height={240}
                  title=""
                />
              {:else}
                <EmptyState
                  iconName="Users"
                  title="No viewer data"
                  description="No viewer data was recorded for the selected stream in the last 24 hours. Ensure the stream is live and has active viewers."
                  actionText="View Stream"
                  onAction={() => goto(resolve(`/streams/${selectedStream!.id}`))}
                />
              {/if}
            </div>
            <div class="slab-actions">
              <Button href={resolve("/analytics/usage")} variant="ghost" class="gap-2">
                <TrendingUpIcon class="w-4 h-4" />
                Usage Analytics
              </Button>
            </div>
          </div>
        {/if}

        <!-- Quality Tier Distribution Slab -->
        {#if selectedStream}
          <div class="slab">
            <div class="slab-header">
              <h3>Quality Distribution (7 Days)</h3>
            </div>
            <div class="slab-body--padded">
              {#if qualityTierData && (qualityTierData.tier1080pMinutes > 0 || qualityTierData.tier720pMinutes > 0 || qualityTierData.tier480pMinutes > 0 || qualityTierData.tierSdMinutes > 0)}
                <QualityTierChart data={qualityTierData} height={200} />
              {:else}
                <EmptyState
                  iconName="Gauge"
                  title="No quality distribution data"
                  description="No quality tier data was recorded for the selected stream in the last 7 days. Ensure the stream is live and has active viewers."
                  actionText="View Stream"
                  onAction={() => goto(resolve(`/streams/${selectedStream!.id}`))}
                />
              {/if}
            </div>
          </div>
        {/if}

        <!-- Codec Distribution Slab -->
        {#if selectedStream}
          <div class="slab">
            <div class="slab-header">
              <h3>Codec Usage (7 Days)</h3>
            </div>
            <div class="slab-body--padded">
              {#if codecData.length > 0}
                <CodecDistributionChart data={codecData} height={200} />
              {:else}
                <EmptyState
                  iconName="Video"
                  title="No codec usage data"
                  description="No codec usage data was recorded for the selected stream in the last 7 days. Ensure the stream is live and has active viewers."
                  actionText="View Stream"
                  onAction={() => goto(resolve(`/streams/${selectedStream!.id}`))}
                />
              {/if}
            </div>
          </div>
        {/if}

        <!-- Live Activity Slab -->
        {#if selectedStream}
          <div class="slab">
            <div class="slab-header">
              <div class="flex items-center gap-2">
                <h3>Live Activity</h3>
                <div class="w-2 h-2 rounded-full {liveActivityPulse ? 'bg-success animate-ping' : 'bg-muted-foreground/50'}"></div>
              </div>
            </div>
            <div class="slab-body--padded">
              {#if liveViewerActivity.length > 0}
                <div class="space-y-2 max-h-[280px] overflow-y-auto">
                  {#each liveViewerActivity as event, i (event.timestamp + '-' + i)}
                    <div class="flex items-center justify-between p-2 border border-border/30 bg-muted/20 {i === 0 && liveActivityPulse ? 'ring-1 ring-success/50' : ''}">
                      <div class="flex items-center gap-2">
                        <div class="w-1.5 h-1.5 rounded-full {event.action === 'connect' ? 'bg-success' : 'bg-destructive'}"></div>
                        <div>
                          <p class="text-xs font-medium text-foreground">
                            {event.action === 'connect' ? 'Connected' : 'Disconnected'}
                          </p>
                          <p class="text-[10px] text-muted-foreground">
                            {event.clientCity || 'Unknown'}{event.clientCountry ? `, ${event.clientCountry}` : ''} • {event.protocol.toUpperCase()}
                          </p>
                        </div>
                      </div>
                      <span class="text-[10px] text-muted-foreground font-mono">
                        {new Date(event.timestamp).toLocaleTimeString()}
                      </span>
                    </div>
                  {/each}
                </div>
              {:else}
                <div class="flex items-center justify-center h-[120px] border border-border/30 bg-muted/20">
                  <p class="text-muted-foreground text-sm">Waiting for viewer activity...</p>
                </div>
              {/if}
            </div>
          </div>
        {/if}

        <!-- Empty State -->
        {#if streams.length === 0}
          <div class="slab col-span-full">
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

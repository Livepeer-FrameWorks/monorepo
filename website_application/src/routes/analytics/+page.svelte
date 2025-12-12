<script lang="ts">
  import { onMount, onDestroy, untrack } from "svelte";
  import { goto } from "$app/navigation";
  import { resolve } from "$app/paths";
  import { auth } from "$lib/stores/auth";
  import {
    GetStreamsStore,
    GetPlatformOverviewStore,
    GetStreamAnalyticsSummaryStore,
    GetViewerCountTimeSeriesStore,
    GetQualityTierDailyStore,
    ViewerMetricsStreamStore,
    StreamStatus
  } from "$houdini";
  import type { ViewerMetricsStream$result } from "$houdini";
  import { toast } from "$lib/stores/toast.js";
  import { getIconComponent } from "$lib/iconUtils";
  import LoadingCard from "$lib/components/LoadingCard.svelte";
  import SkeletonLoader from "$lib/components/SkeletonLoader.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import { Button } from "$lib/components/ui/button";
  import { Badge } from "$lib/components/ui/badge";
  import { GridSeam } from "$lib/components/layout";
  import DashboardMetricCard from "$lib/components/shared/DashboardMetricCard.svelte";
  import ViewerTrendChart from "$lib/components/charts/ViewerTrendChart.svelte";
  import QualityTierChart from "$lib/components/charts/QualityTierChart.svelte";
  import CodecDistributionChart from "$lib/components/charts/CodecDistributionChart.svelte";

  // Houdini stores
  const streamsStore = new GetStreamsStore();
  const platformOverviewStore = new GetPlatformOverviewStore();
  const streamAnalyticsStore = new GetStreamAnalyticsSummaryStore();
  const viewerCountStore = new GetViewerCountTimeSeriesStore();
  const qualityTierStore = new GetQualityTierDailyStore();
  const viewerMetricsSub = new ViewerMetricsStreamStore();

  let isAuthenticated = false;
  let user: any = null;

  // Derived state from Houdini stores
  let loading = $derived($streamsStore.fetching || $platformOverviewStore.fetching);
  let streams = $derived($streamsStore.data?.streams ?? []);
  let platformOverview = $derived($platformOverviewStore.data?.platformOverview ?? null);
  let analyticsData = $derived($streamAnalyticsStore.data?.streamAnalytics ?? null);
  let viewerMetrics = $derived($viewerCountStore.data?.viewerCountTimeSeries ?? []);
  let qualityTierData = $derived.by(() => {
    const edges = $qualityTierStore.data?.qualityTierDailyConnection?.edges ?? [];
    if (edges.length === 0) return null;
    // Aggregate all daily records into a single summary
    return edges.reduce(
      (acc, edge) => ({
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
    const edges = $qualityTierStore.data?.qualityTierDailyConnection?.edges ?? [];
    if (edges.length === 0) return [];

    // Sum minutes by codec
    let h264 = 0;
    let h265 = 0;
    // Note: If you add more codecs to the backend query, add them here

    edges.forEach(edge => {
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

      // Select first stream if available
      const streamsData = $streamsStore.data?.streams ?? [];
      if (streamsData.length > 0) {
        selectedStream = streamsData[0];
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
      await Promise.all([
        streamAnalyticsStore.fetch({ variables: { stream: streamId, timeRange } }).catch(() => null),
        viewerCountStore.fetch({ variables: { stream: streamId, timeRange, interval: "5m" } }).catch(() => null),
        qualityTierStore.fetch({ variables: { stream: streamId, timeRange: qualityTimeRange, first: 7 } }).catch(() => null),
      ]);
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

  function packetLossClass(loss: number) {
    if (!hasValue(loss)) return "";
    if (loss > 2) return "text-destructive";
    if (loss > 1) return "text-warning";
    return "text-success";
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
  const GlobeIcon = getIconComponent("Globe2");
  const ZapIcon = getIconComponent("Zap");
  const TrendingUpIcon = getIconComponent("TrendingUp");
  const GaugeIcon = getIconComponent("Gauge");
  const VideoIcon = getIconComponent("Video");
  const WifiIcon = getIconComponent("Wifi");

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
            // avgBitrate is in bps, divide by 1,000,000 for Mbps
            value: `${((analyticsData.avgBitrate ?? 0) / 1_000_000).toFixed(1)} Mbps`,
            tone: "text-primary",
          },
          hasValue(analyticsData.packetLossRate) && {
            key: "packetLoss", label: "Packet Loss",
            value: `${((analyticsData.packetLossRate ?? 0) * 100).toFixed(2)}%`,
            tone: packetLossClass((analyticsData.packetLossRate ?? 0) * 100),
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
  <div class="px-4 sm:px-6 lg:px-8 py-4 border-b border-[hsl(var(--tn-fg-gutter)/0.3)] shrink-0">
    <div class="flex items-center gap-3">
      <ChartLineIcon class="w-5 h-5 text-primary" />
      <div>
        <h1 class="text-xl font-bold text-foreground">Analytics</h1>
        <p class="text-sm text-muted-foreground">
          Monitor your streaming performance and viewer engagement
        </p>
      </div>
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
              icon={GlobeIcon}
              iconColor="text-accent-purple"
              value={formatNumber(platformOverview.totalUsers)}
              valueColor="text-accent-purple"
              label="Total Users"
            />
          </div>
          <div>
            <DashboardMetricCard
              icon={WifiIcon}
              iconColor="text-warning"
              value={`${(platformOverview.totalBandwidth / 1000000).toFixed(1)}MB`}
              valueColor="text-warning"
              label="Total Bandwidth"
            />
          </div>
        </GridSeam>
      {/if}

      <!-- Main Content Grid -->
      <div class="dashboard-grid">
        <!-- Stream Selector Slab -->
        {#if streams.length > 0}
          <div class="slab">
            <div class="slab-header">
              <h3>Select Stream</h3>
            </div>
            <div class="slab-body--padded">
              <div class="grid grid-cols-1 gap-2">
                {#each streams as stream (stream.id ?? stream.name)}
                  <button
                    class="w-full text-left p-3 border transition-all {selectedStream?.id === stream.id
                      ? 'border-primary bg-primary/10 ring-1 ring-primary/30'
                      : 'border-border hover:border-info bg-muted/30 hover:bg-muted/50'}"
                    onclick={() => selectStream(stream)}
                  >
                    <div class="flex items-center justify-between">
                      <span class="font-medium text-foreground">{stream.name}</span>
                      <span class="text-xs px-2 py-0.5 rounded {stream.metrics?.status === StreamStatus.LIVE ? 'bg-success/20 text-success' : 'bg-muted text-muted-foreground'}">
                        {stream.metrics?.status || "offline"}
                      </span>
                    </div>
                  </button>
                {/each}
              </div>
            </div>
            <div class="slab-actions">
              <Button href={resolve("/streams")} variant="ghost" class="gap-2">
                <VideoIcon class="w-4 h-4" />
                Manage Streams
              </Button>
            </div>
          </div>
        {/if}

        <!-- Stream Analytics Slab -->
        {#if selectedStream}
          <div class="slab">
            <div class="slab-header">
              <h3>Stream Analytics: {selectedStream.name}</h3>
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
                <p class="text-muted-foreground text-sm">No analytics data available</p>
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
                <div class="flex items-center justify-center h-[200px] border border-border/30 bg-muted/20">
                  <p class="text-muted-foreground text-sm">No viewer data for this time range</p>
                </div>
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
              <QualityTierChart data={qualityTierData} height={200} />
            </div>
          </div>
        {/if}

        <!-- Codec Distribution Slab -->
        {#if selectedStream && codecData.length > 0}
          <div class="slab">
            <div class="slab-header">
              <h3>Codec Usage (7 Days)</h3>
            </div>
            <div class="slab-body--padded">
              <CodecDistributionChart data={codecData} height={200} />
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

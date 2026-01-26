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
  import {
    Select,
    SelectContent,
    SelectItem,
    SelectTrigger,
  } from "$lib/components/ui/select";
  import { GridSeam } from "$lib/components/layout";
  import DashboardMetricCard from "$lib/components/shared/DashboardMetricCard.svelte";
  import ViewerTrendChart from "$lib/components/charts/ViewerTrendChart.svelte";
  import QualityTierChart from "$lib/components/charts/QualityTierChart.svelte";
  import CodecDistributionChart from "$lib/components/charts/CodecDistributionChart.svelte";
  import { resolveTimeRange, TIME_RANGE_OPTIONS } from "$lib/utils/time-range";

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
  let timeRange = $state("7d");
  let currentRange = $derived(resolveTimeRange(timeRange));
  const timeRangeOptions = TIME_RANGE_OPTIONS.filter((option) => ["24h", "7d", "30d"].includes(option.value));

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

  let platformOverview = $derived($platformOverviewStore.data?.analytics?.overview ?? null);

  let streamDailyEdges = $derived(
    $streamAnalyticsStore.data?.analytics?.usage?.streaming?.streamAnalyticsDailyConnection?.edges ?? []
  );
  let latestStreamDaily = $derived.by(() => (streamDailyEdges.length ? streamDailyEdges[0].node : null));

  let viewerHourEdges = $derived(
    $streamAnalyticsStore.data?.analytics?.usage?.streaming?.viewerHoursHourlyConnection?.edges ?? []
  );
  let health5mEdges = $derived(
    $streamAnalyticsStore.data?.analytics?.health?.streamHealth5mConnection?.edges ?? []
  );

  // Stream analytics summary (MV-backed range aggregates)
  let streamAnalyticsSummary = $derived(
    $streamAnalyticsStore.data?.analytics?.usage?.streaming?.streamAnalyticsSummary ?? null
  );

  const useDailyTrend = $derived(currentRange.days > 7);

  let viewerMetrics = $derived.by(() => {
    if (useDailyTrend) {
      return streamDailyEdges
        .map((e) => ({ timestamp: e.node.day, viewers: e.node.uniqueViewers ?? 0 }))
        .sort((a, b) => new Date(a.timestamp).getTime() - new Date(b.timestamp).getTime());
    }
    return viewerHourEdges
      .map((e) => ({ timestamp: e.node.hour, viewers: e.node.uniqueViewers ?? 0 }))
      .sort((a, b) => new Date(a.timestamp).getTime() - new Date(b.timestamp).getTime());
  });

  let healthRangeLabel = $derived(currentRange.days > 7 ? "Last 24 Hours" : currentRange.label);
  let qualityRangeLabel = $derived(currentRange.label);

  let qualityTierData = $derived.by(() => {
    const rangeQuality = streamAnalyticsSummary?.rangeQuality;
    if (!rangeQuality) return null;
    return {
      tier2160pMinutes: rangeQuality.tier2160pMinutes ?? 0,
      tier1440pMinutes: rangeQuality.tier1440pMinutes ?? 0,
      tier1080pMinutes: rangeQuality.tier1080pMinutes ?? 0,
      tier720pMinutes: rangeQuality.tier720pMinutes ?? 0,
      tier480pMinutes: rangeQuality.tier480pMinutes ?? 0,
      tierSdMinutes: rangeQuality.tierSdMinutes ?? 0,
    };
  });

  // Aggregate codec data for chart
  type CodecData = { codec: string; minutes: number };

  let codecData = $derived.by((): CodecData[] => {
    const rangeQuality = streamAnalyticsSummary?.rangeQuality;
    if (!rangeQuality) return [];
    return [
      { codec: "H.264", minutes: rangeQuality.codecH264Minutes ?? 0 },
      { codec: "H.265", minutes: rangeQuality.codecH265Minutes ?? 0 },
    ].filter((d) => d.minutes > 0);
  });

  let analyticsData = $derived.by(() => {
    const metrics = selectedStream?.metrics;
    const summary = streamAnalyticsSummary;
    const fallbackAvgBitrate =
      health5mEdges.length > 0
        ? Math.round(
            health5mEdges.reduce((sum, edge) => sum + (edge.node.avgBitrate ?? 0), 0) /
              health5mEdges.length
          )
        : null;
    const resolution =
      metrics?.primaryWidth && metrics?.primaryHeight
        ? `${metrics.primaryWidth}x${metrics.primaryHeight}`
        : null;
    return {
      // Range aggregates (summary)
      totalViews: summary?.rangeTotalViews ?? latestStreamDaily?.totalViews ?? null,
      peakViewers: summary?.rangePeakConcurrentViewers ?? null,
      avgViewers: summary?.rangeAvgViewers ?? null,
      uniqueViewers: summary?.rangeUniqueViewers ?? latestStreamDaily?.uniqueViewers ?? null,
      totalSessionDuration: summary?.rangeViewerHours != null ? summary.rangeViewerHours * 3600 : null,
      uniqueCountries: summary?.rangeUniqueCountries ?? latestStreamDaily?.uniqueCountries ?? null,
      uniqueCities: latestStreamDaily?.uniqueCities ?? null,
      avgBufferHealth: summary?.rangeAvgBufferHealth ?? null,
      avgBitrate: summary?.rangeAvgBitrate ?? fallbackAvgBitrate,

      // Current snapshot (stream metrics)
      currentResolution: resolution,
      currentCodec: metrics?.primaryCodec ?? null,
      bitrateKbps: metrics?.primaryBitrate ? metrics.primaryBitrate / 1000 : null,
      currentFps: metrics?.primaryFps ?? null,
      qualityTier: metrics?.qualityTier ?? null,
      currentBufferState: metrics?.bufferState ?? null,
      currentIssues: metrics?.issuesDescription ?? null,
    };
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
      const range = resolveTimeRange(timeRange);
      currentRange = range;

      // Load streams and platform overview in parallel
      await Promise.all([
        streamsStore.fetch(),
        platformOverviewStore
          .fetch({ variables: { timeRange: { start: range.start, end: range.end }, days: range.days } })
          .catch(() => null),
      ]);

      // Select first stream if available (use unmasked streams)
      if (streams.length > 0) {
        if (!selectedStream) {
          selectedStream = streams[0];
        }
        if (selectedStream) {
          await loadAnalyticsForStream(selectedStream, range);
          startRealTimeSubscriptions();
        }
      }
    } catch (error) {
      console.error("Failed to load data:", error);
      toast.error("Failed to load analytics data. Please refresh the page.");
    }
  }

  async function loadAnalyticsForStream(stream: StreamData, rangeOverride?: ReturnType<typeof resolveTimeRange>) {
    if (!stream?.id) return;

    const range = rangeOverride ?? resolveTimeRange(timeRange);
    currentRange = range;
    const hourlyFirst = range.days <= 7 ? range.days * 24 : 24 * 7;
    const healthFirst = range.days <= 7 ? Math.min(range.days * 24 * 12, 1000) : 288;
    try {
      await streamAnalyticsStore.fetch({
        variables: {
          id: stream.id,
          streamId: stream.id,
          timeRange: { start: range.start, end: range.end },
          hourlyFirst,
          healthFirst,
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
    viewerMetricsSub.listen({ streamId: selectedStream.id });
  }

  // Effect to handle viewer metrics subscription updates
  $effect(() => {
    const event = $viewerMetricsSub.data?.liveViewerMetrics;
    if (event) {
      // Wrap mutations in untrack to prevent reading liveViewerActivity from creating a dependency
      untrack(() => {
        const newEvent: LiveViewerEvent = {
          action: event.action,
          clientCity: event.clientCity ?? null,
          clientCountry: event.clientCountry ?? null,
          protocol: event.protocol,
          timestamp: Date.now(),
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
    await loadAnalyticsForStream(stream, resolveTimeRange(timeRange));
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
  const CalendarIcon = getIconComponent("Calendar");

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
          hasValue(analyticsData.totalViews) && { key: "totalViews", label: "Total Views", value: formatNumber(analyticsData.totalViews), tone: "text-primary" },
          hasValue(analyticsData.peakViewers) && { key: "peakViewers", label: "Peak Viewers", value: formatNumber(analyticsData.peakViewers), tone: "text-success" },
          hasValue(analyticsData.avgViewers) && { key: "avgViewers", label: "Avg Viewers", value: Math.round(analyticsData.avgViewers ?? 0), tone: "text-accent-purple" },
          hasValue(analyticsData.uniqueViewers) && { key: "uniqueViewers", label: "Unique Viewers", value: formatNumber(analyticsData.uniqueViewers), tone: "text-warning" },
          hasValue(analyticsData.totalSessionDuration) && { key: "sessionDuration", label: "Viewer Hours", value: formatDuration(analyticsData.totalSessionDuration), tone: "text-info" },
        ].filter(Boolean) as { key: string; label: string; value: string | number; tone: string }[]
      : [],
  );

  const qualityMetricsCards = $derived(
    analyticsData
      ? [
          analyticsData.uniqueCountries && { key: "countries", label: "Countries", value: analyticsData.uniqueCountries, tone: "text-info" },
          analyticsData.uniqueCities && { key: "cities", label: "Daily Cities", value: analyticsData.uniqueCities, tone: "text-accent-purple" },
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

  // Operational metrics from streamAnalyticsSummary
  const operationalMetrics = $derived(
    streamAnalyticsSummary
      ? [
          hasValue(streamAnalyticsSummary.rangeTotalSessions) && {
            key: "sessions", label: "Total Sessions",
            value: formatNumber(streamAnalyticsSummary.rangeTotalSessions),
            tone: "text-info",
          },
          hasValue(streamAnalyticsSummary.rangeAvgConnectionTime) && {
            key: "avgConnTime", label: "Avg Connection",
            value: formatDuration(streamAnalyticsSummary.rangeAvgConnectionTime),
            tone: "text-success",
          },
          hasValue(streamAnalyticsSummary.rangePacketLossRate) && {
            key: "packetLoss", label: "Packet Loss",
            value: `${((streamAnalyticsSummary.rangePacketLossRate ?? 0) * 100).toFixed(2)}%`,
            tone: streamAnalyticsSummary.rangePacketLossRate && streamAnalyticsSummary.rangePacketLossRate < 0.01 ? "text-success" : streamAnalyticsSummary.rangePacketLossRate && streamAnalyticsSummary.rangePacketLossRate < 0.05 ? "text-warning" : "text-destructive",
          },
          hasValue(streamAnalyticsSummary.rangeAvgSessionSeconds) && {
            key: "avgSession", label: "Avg Session",
            value: formatDuration(streamAnalyticsSummary.rangeAvgSessionSeconds),
            tone: "text-accent-purple",
          },
        ].filter(Boolean) as { key: string; label: string; value: string | number; tone: string }[]
      : [],
  );

  // Quality tier daily trend data
  const qualityTierDailyEdges = $derived(
    $streamAnalyticsStore.data?.analytics?.usage?.streaming?.qualityTierDailyConnection?.edges ?? []
  );

  // Transform quality tier daily data for trend visualization
  const qualityTierTrendData = $derived.by(() => {
    if (!qualityTierDailyEdges.length) return [];
    return qualityTierDailyEdges
      .map(e => ({
        date: e.node.day,
        tier2160p: e.node.tier2160pMinutes ?? 0,
        tier1440p: e.node.tier1440pMinutes ?? 0,
        tier1080p: e.node.tier1080pMinutes ?? 0,
        tier720p: e.node.tier720pMinutes ?? 0,
        tier480p: e.node.tier480pMinutes ?? 0,
        tierSd: e.node.tierSdMinutes ?? 0,
      }))
      .sort((a, b) => new Date(a.date).getTime() - new Date(b.date).getTime());
  });

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
      <Select value={timeRange} onValueChange={(value) => { timeRange = value; loadData(); }} type="single">
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
        <div class="px-4 sm:px-6 lg:px-8 py-2 bg-muted/30 border-b border-[hsl(var(--tn-fg-gutter)/0.2)] flex items-center justify-between">
          <span class="text-xs text-muted-foreground uppercase tracking-wide">Platform Overview</span>
          <Badge variant="outline" class="text-[10px] px-1.5 py-0 text-muted-foreground border-muted-foreground/30">
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
                      <Badge variant="outline" class={stream.metrics?.status === StreamStatus.LIVE ? 'bg-success/10 text-success border-success/20' : 'bg-muted text-muted-foreground'}>
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

                <!-- Operational Metrics -->
                {#if operationalMetrics.length > 0}
                  <div class="pt-3 border-t border-border/30">
                    <p class="text-xs text-muted-foreground uppercase tracking-wide mb-3">Operational</p>
                    <div class="grid grid-cols-2 sm:grid-cols-4 gap-2">
                      {#each operationalMetrics as stat (stat.key)}
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
              <h3>{useDailyTrend ? "Viewer Trend (Daily)" : "Viewer Trend (Hourly)"}</h3>
            </div>
            <div class="slab-body--padded">
              {#if viewerMetrics.length > 0}
                <ViewerTrendChart
                  data={viewerMetrics}
                  height={240}
                  title=""
                />
              {:else}
                <EmptyState
                  iconName="Users"
                  title="No viewer data"
                  description={`No viewer data was recorded for the selected stream in ${currentRange.label.toLowerCase()}. Ensure the stream is live and has active viewers.`}
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
              <h3>Quality Distribution ({qualityRangeLabel})</h3>
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

        <!-- Quality Tier Daily Trend Slab -->
        {#if selectedStream && qualityTierTrendData.length > 1}
          <div class="slab">
            <div class="slab-header">
              <h3>Quality Trend ({qualityRangeLabel})</h3>
            </div>
            <div class="slab-body--padded">
              <div class="space-y-3">
                {#each ['tier1080p', 'tier720p', 'tier480p'] as tier}
                  {@const tierLabel = tier === 'tier1080p' ? '1080p' : tier === 'tier720p' ? '720p' : '480p'}
                  {@const tierColor = tier === 'tier1080p' ? 'text-success' : tier === 'tier720p' ? 'text-info' : 'text-warning'}
                  {@const totalMinutes = qualityTierTrendData.reduce((sum, d) => sum + (d[tier as keyof typeof d] as number || 0), 0)}
                  {#if totalMinutes > 0}
                    <div class="flex items-center gap-3">
                      <span class="text-xs font-medium {tierColor} w-12">{tierLabel}</span>
                      <div class="flex-1 h-6 flex items-end gap-px">
                        {#each qualityTierTrendData as day}
                          {@const value = day[tier as keyof typeof day] as number || 0}
                          {@const maxValue = Math.max(...qualityTierTrendData.map(d => d[tier as keyof typeof d] as number || 0))}
                          {@const heightPercent = maxValue > 0 ? (value / maxValue) * 100 : 0}
                          <div
                            class="flex-1 bg-current opacity-60 hover:opacity-100 transition-opacity"
                            style="height: {Math.max(heightPercent, 4)}%"
                            title="{new Date(day.date).toLocaleDateString()}: {value} min"
                          ></div>
                        {/each}
                      </div>
                      <span class="text-xs text-muted-foreground w-16 text-right">{formatNumber(totalMinutes)}m</span>
                    </div>
                  {/if}
                {/each}
              </div>
              <div class="flex justify-between text-[10px] text-muted-foreground mt-2 pt-2 border-t border-border/30">
                <span>{new Date(qualityTierTrendData[0]?.date).toLocaleDateString()}</span>
                <span>{new Date(qualityTierTrendData[qualityTierTrendData.length - 1]?.date).toLocaleDateString()}</span>
              </div>
            </div>
          </div>
        {/if}

        <!-- Codec Distribution Slab -->
        {#if selectedStream}
          <div class="slab">
            <div class="slab-header">
              <h3>Codec Usage ({qualityRangeLabel})</h3>
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
                <Badge variant="outline" class="bg-success/10 text-success border-success/30 text-[10px] px-1.5 py-0">
                  LIVE
                </Badge>
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

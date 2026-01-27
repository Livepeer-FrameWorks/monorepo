<script lang="ts">
  import { onMount } from "svelte";
  import { page } from "$app/state";
  import { goto } from "$app/navigation";
  import { resolve } from "$app/paths";
  import {
    fragment,
    GetStreamStore,
    GetStreamOverviewStore,
    GetStreamAnalyticsDailyConnectionStore,
    GetStreamEventsStore,
    StreamCoreFieldsStore,
    StreamMetricsFieldsStore,
  } from "$houdini";
  import { formatNumber, formatDuration } from "$lib/utils/formatters.js";
  import LoadingCard from "$lib/components/LoadingCard.svelte";
  import { getIconComponent } from "$lib/iconUtils";
  import { Button } from "$lib/components/ui/button";
  import { GridSeam } from "$lib/components/layout";
  import ViewerTrendChart from "$lib/components/charts/ViewerTrendChart.svelte";
  import QualityTierChart from "$lib/components/charts/QualityTierChart.svelte";
  import CodecDistributionChart from "$lib/components/charts/CodecDistributionChart.svelte";
  import { EventLog } from "$lib/components/stream-details";
  import type { StreamEvent } from "$lib/components/stream-details/EventLog.svelte";
  import { resolveTimeRange, TIME_RANGE_OPTIONS } from "$lib/utils/time-range";
  import { Select, SelectContent, SelectItem, SelectTrigger } from "$lib/components/ui/select";

  // Houdini stores
  const streamStore = new GetStreamStore();
  const streamOverviewStore = new GetStreamOverviewStore();
  const streamDailyStore = new GetStreamAnalyticsDailyConnectionStore();
  const streamEventsStore = new GetStreamEventsStore();

  // Fragment stores
  const streamCoreStore = new StreamCoreFieldsStore();
  const streamMetricsStore = new StreamMetricsFieldsStore();

  // Icons
  const ArrowLeftIcon = getIconComponent("ArrowLeft");
  const BarChart2Icon = getIconComponent("BarChart2");
  const UsersIcon = getIconComponent("Users");
  const ClockIcon = getIconComponent("Clock");
  const ActivityIcon = getIconComponent("Activity");
  const TrendingUpIcon = getIconComponent("TrendingUp");
  const HeartIcon = getIconComponent("Heart");
  const ZapIcon = getIconComponent("Zap");

  // Derive stream ID from route params
  let streamId = $derived(page.params.id as string);

  // Time range selection
  let timeRange = $state("7d");
  const timeRangeOptions = TIME_RANGE_OPTIONS.filter((o) =>
    ["24h", "7d", "30d", "90d"].includes(o.value)
  );

  // Loading state
  let loading = $state(true);

  // Stream data
  let maskedStream = $derived($streamStore.data?.stream ?? null);
  let streamCoreStoreResult = $derived(
    maskedStream ? fragment(maskedStream, streamCoreStore) : null
  );
  let streamCore = $derived(streamCoreStoreResult ? $streamCoreStoreResult : null);
  let streamMetricsStoreResult = $derived(
    maskedStream?.metrics ? fragment(maskedStream.metrics, streamMetricsStore) : null
  );
  let streamMetrics = $derived(streamMetricsStoreResult ? $streamMetricsStoreResult : null);
  let stream = $derived(streamCore ? { ...streamCore, metrics: streamMetrics } : null);

  // Analytics summary
  let streamAnalyticsSummary = $derived(
    $streamOverviewStore.data?.analytics?.usage?.streaming?.streamAnalyticsSummary ?? null
  );

  // Daily analytics for trend charts
  let streamDailyAnalytics = $derived.by(() => {
    const edges =
      $streamDailyStore.data?.analytics?.usage?.streaming?.streamAnalyticsDailyConnection?.edges ??
      [];
    return edges
      .map((e) => e.node)
      .sort((a, b) => new Date(a.day).getTime() - new Date(b.day).getTime());
  });

  // Quality tier data
  let qualityData = $derived.by(() => {
    const quality = streamAnalyticsSummary?.rangeQuality;
    if (!quality) return null;
    return {
      tier2160pMinutes: quality.tier2160pMinutes ?? 0,
      tier1440pMinutes: quality.tier1440pMinutes ?? 0,
      tier1080pMinutes: quality.tier1080pMinutes ?? 0,
      tier720pMinutes: quality.tier720pMinutes ?? 0,
      tier480pMinutes: quality.tier480pMinutes ?? 0,
      tierSdMinutes: quality.tierSdMinutes ?? 0,
    };
  });

  // Codec distribution
  let codecDistribution = $derived.by(() => {
    const quality = streamAnalyticsSummary?.rangeQuality;
    if (!quality) return [];
    return [
      { codec: "H.264", minutes: quality.codecH264Minutes ?? 0, color: "bg-blue-500" },
      { codec: "HEVC", minutes: quality.codecH265Minutes ?? 0, color: "bg-orange-500" },
      { codec: "VP9", minutes: quality.codecVp9Minutes ?? 0, color: "bg-purple-500" },
      { codec: "AV1", minutes: quality.codecAv1Minutes ?? 0, color: "bg-green-500" },
    ].filter((c) => c.minutes > 0);
  });

  // Map GraphQL event types to EventLog component types
  function mapEventType(type: string): StreamEvent["type"] {
    const typeMap: Record<string, StreamEvent["type"]> = {
      STREAM_START: "stream_start",
      STREAM_END: "stream_end",
      STREAM_LIFECYCLE_UPDATE: "info",
      BUFFER_UPDATE: "quality_change",
      TRACK_LIST_UPDATE: "track_change",
      DVR_START: "dvr_start",
      DVR_STOP: "dvr_stop",
      ERROR: "error",
      WARNING: "warning",
    };
    return typeMap[type] || "info";
  }

  // Stream events for event log
  let streamEvents = $derived.by(() => {
    const edges =
      $streamEventsStore.data?.analytics?.lifecycle?.streamEventsConnection?.edges ?? [];
    return edges.map((e) => ({
      id: e.node.eventId,
      timestamp: e.node.timestamp,
      type: mapEventType(e.node.type),
      message: e.node.status ?? e.node.details ?? "Event",
      nodeName: e.node.nodeId ?? undefined,
      details: e.node.payload ? JSON.stringify(e.node.payload) : undefined,
    })) as StreamEvent[];
  });

  // Viewer trend data for chart
  let viewerTrendData = $derived.by(() => {
    if (!streamDailyAnalytics.length) return [];
    return streamDailyAnalytics.map((d) => ({
      timestamp: d.day,
      viewers: d.uniqueViewers ?? 0,
    }));
  });

  // Metric cards
  let metricCards = $derived.by(() => {
    const summary = streamAnalyticsSummary;
    const metrics = stream?.metrics;
    if (!summary && !metrics) return [];

    return [
      {
        key: "viewers",
        label: "Current Viewers",
        value: metrics?.currentViewers ?? 0,
        icon: UsersIcon,
        tone: "text-info",
        live: true,
      },
      {
        key: "peakViewers",
        label: "Peak Viewers",
        value: summary?.rangePeakConcurrentViewers ?? 0,
        icon: TrendingUpIcon,
        tone: "text-success",
      },
      {
        key: "viewerHours",
        label: "Viewer Hours",
        value: formatNumber(summary?.rangeViewerHours ?? 0) + "h",
        icon: ClockIcon,
        tone: "text-primary",
      },
      {
        key: "sessions",
        label: "Total Sessions",
        value: formatNumber(summary?.rangeTotalSessions ?? 0),
        icon: ActivityIcon,
        tone: "text-accent-purple",
      },
      {
        key: "avgSession",
        label: "Avg Session",
        value: formatDuration(summary?.rangeAvgSessionSeconds ?? 0),
        icon: HeartIcon,
        tone: "text-warning",
      },
      {
        key: "packetLoss",
        label: "Packet Loss",
        value: `${((summary?.rangePacketLossRate ?? 0) * 100).toFixed(2)}%`,
        icon: ZapIcon,
        tone:
          summary?.rangePacketLossRate && summary.rangePacketLossRate < 0.01
            ? "text-success"
            : "text-warning",
      },
    ].filter((c) => c.value !== null && c.value !== undefined);
  });

  async function loadData() {
    loading = true;
    try {
      const range = resolveTimeRange(timeRange);

      await Promise.all([
        streamStore.fetch({ variables: { id: streamId, streamId: streamId } }),
        streamOverviewStore.fetch({
          variables: {
            id: streamId,
            streamId: streamId,
            timeRange: { start: range.start, end: range.end },
          },
        }),
        streamDailyStore.fetch({
          variables: {
            streamId: streamId,
            timeRange: { start: range.start, end: range.end },
            first: Math.min(range.days, 120),
          },
        }),
        streamEventsStore.fetch({
          variables: {
            streamId: streamId,
            timeRange: { start: range.start, end: range.end },
            first: 50,
          },
        }),
      ]);
    } catch (err) {
      console.error("Failed to load stream analytics:", err);
    } finally {
      loading = false;
    }
  }

  // Reload when time range changes
  $effect(() => {
    if (streamId && timeRange) {
      loadData();
    }
  });

  onMount(() => {
    loadData();
  });
</script>

<div class="min-h-screen bg-background">
  <!-- Header -->
  <div class="border-b border-border/50 bg-muted/20">
    <div class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-4">
      <div class="flex items-center justify-between">
        <div class="flex items-center gap-4">
          <Button variant="ghost" size="sm" onclick={() => goto(resolve(`/streams/${streamId}`))}>
            <ArrowLeftIcon class="w-4 h-4 mr-2" />
            Back to Stream
          </Button>
          <div class="border-l border-border/50 pl-4">
            <div class="flex items-center gap-2">
              <BarChart2Icon class="w-5 h-5 text-info" />
              <h1 class="text-lg font-semibold">Stream Analytics</h1>
            </div>
            {#if stream}
              <p class="text-sm text-muted-foreground">{stream.name}</p>
            {/if}
          </div>
        </div>
        <div class="flex items-center gap-3">
          <Select value={timeRange} onValueChange={(v) => (timeRange = v)} type="single">
            <SelectTrigger class="min-w-[120px]">
              {timeRangeOptions.find((o) => o.value === timeRange)?.label ?? "7 Days"}
            </SelectTrigger>
            <SelectContent>
              {#each timeRangeOptions as option (option.value)}
                <SelectItem value={option.value}>{option.label}</SelectItem>
              {/each}
            </SelectContent>
          </Select>
          <Button
            variant="outline"
            size="sm"
            onclick={() => goto(resolve(`/streams/${streamId}/health`))}
          >
            <HeartIcon class="w-4 h-4 mr-2" />
            Health
          </Button>
        </div>
      </div>
    </div>
  </div>

  {#if loading && !stream}
    <div class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8">
      <LoadingCard />
    </div>
  {:else if !stream}
    <div class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8">
      <div class="text-center py-12">
        <p class="text-muted-foreground">Stream not found</p>
        <Button class="mt-4" onclick={() => goto(resolve("/streams"))}>Back to Streams</Button>
      </div>
    </div>
  {:else}
    <div class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-6 space-y-6">
      <!-- Metric Cards -->
      <GridSeam cols={3} stack="2x2" surface="panel" flush={true}>
        {#each metricCards as card (card.key)}
          <div class="p-4 border-r border-b border-border/30 last:border-r-0">
            <div class="flex items-center gap-2 mb-2">
              <svelte:component this={card.icon} class="w-4 h-4 text-muted-foreground" />
              <span class="text-xs text-muted-foreground uppercase tracking-wide">{card.label}</span
              >
              {#if card.live}
                <span class="text-[10px] px-1 py-0.5 rounded bg-success/20 text-success">LIVE</span>
              {/if}
            </div>
            <p class="text-2xl font-bold {card.tone}">{card.value}</p>
          </div>
        {/each}
      </GridSeam>

      <!-- Viewer Trend Chart -->
      {#if viewerTrendData.length > 1}
        <div class="slab">
          <div class="slab-header">
            <h3>Viewer Trend</h3>
          </div>
          <div class="slab-body--padded">
            <ViewerTrendChart data={viewerTrendData} height={250} seriesLabel="Unique Viewers" />
          </div>
        </div>
      {/if}

      <!-- Quality & Codec Row -->
      <div class="grid grid-cols-1 lg:grid-cols-2 gap-6">
        <!-- Quality Tier Distribution -->
        {#if qualityData}
          <div class="slab">
            <div class="slab-header">
              <h3>Quality Distribution</h3>
            </div>
            <div class="slab-body--padded">
              <QualityTierChart data={qualityData} />
            </div>
          </div>
        {/if}

        <!-- Codec Distribution -->
        {#if codecDistribution.length > 0}
          <div class="slab">
            <div class="slab-header">
              <h3>Codec Usage</h3>
            </div>
            <div class="slab-body--padded">
              <CodecDistributionChart data={codecDistribution} />
            </div>
          </div>
        {/if}
      </div>

      <!-- Daily Stats Table -->
      {#if streamDailyAnalytics.length > 0}
        <div class="slab">
          <div class="slab-header">
            <h3>Daily Breakdown</h3>
          </div>
          <div class="slab-body--flush overflow-x-auto max-h-80">
            <table class="w-full text-sm">
              <thead class="sticky top-0 bg-background">
                <tr
                  class="border-b border-border/50 text-muted-foreground text-xs uppercase tracking-wide"
                >
                  <th class="text-left py-3 px-4">Date</th>
                  <th class="text-right py-3 px-4">Viewers</th>
                  <th class="text-right py-3 px-4">Views</th>
                  <th class="text-right py-3 px-4">Egress</th>
                  <th class="text-right py-3 px-4">Countries</th>
                </tr>
              </thead>
              <tbody>
                {#each streamDailyAnalytics.slice().reverse() as day (day.day)}
                  <tr class="border-b border-border/30 hover:bg-muted/10">
                    <td class="py-3 px-4 font-mono text-xs"
                      >{new Date(day.day).toLocaleDateString()}</td
                    >
                    <td class="py-3 px-4 text-right font-mono"
                      >{formatNumber(day.uniqueViewers ?? 0)}</td
                    >
                    <td class="py-3 px-4 text-right font-mono"
                      >{formatNumber(day.totalViews ?? 0)}</td
                    >
                    <td class="py-3 px-4 text-right font-mono text-info"
                      >{((day.egressBytes ?? 0) / 1e9).toFixed(2)} GB</td
                    >
                    <td class="py-3 px-4 text-right font-mono text-primary"
                      >{day.uniqueCountries ?? 0}</td
                    >
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        </div>
      {/if}

      <!-- Event Log -->
      {#if streamEvents.length > 0}
        <div class="slab">
          <div class="slab-header">
            <h3>Recent Events</h3>
          </div>
          <div class="slab-body--flush">
            <EventLog events={streamEvents} maxItems={20} />
          </div>
        </div>
      {/if}
    </div>
  {/if}
</div>

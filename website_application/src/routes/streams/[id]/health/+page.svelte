<script lang="ts">
  import { onMount, onDestroy, untrack } from "svelte";
  import { page } from "$app/state";
  import { goto } from "$app/navigation";
  import { resolve } from "$app/paths";
  import { SvelteMap } from "svelte/reactivity";
  import {
    fragment,
    GetStreamStore,
    GetStreamHealthStore,
    GetViewerSessionsConnectionStore,
    GetRoutingEventsStore,
    TrackListUpdatesStore,
    StreamCoreFieldsStore,
    StreamMetricsFieldsStore,
  } from "$houdini";
  import type { TrackListUpdates$result } from "$houdini";
  import { toast } from "$lib/stores/toast.js";
  import { streamMetrics as realtimeStreamMetrics } from "$lib/stores/realtime";
  import BufferStateIndicator from "$lib/components/health/BufferStateIndicator.svelte";
  import HealthTrendChart from "$lib/components/charts/HealthTrendChart.svelte";
  import BufferHealthHistogram from "$lib/components/charts/BufferHealthHistogram.svelte";
  import LoadingCard from "$lib/components/LoadingCard.svelte";
  import { getIconComponent } from "$lib/iconUtils";
  import { GridSeam } from "$lib/components/layout";
  import { Button } from "$lib/components/ui/button";
  import {
    Table,
    TableBody,
    TableCell,
    TableHead,
    TableHeader,
    TableRow,
  } from "$lib/components/ui/table";
  import { resolveTimeRange, TIME_RANGE_OPTIONS } from "$lib/utils/time-range";
  import {
    Select,
    SelectContent,
    SelectItem,
    SelectTrigger,
  } from "$lib/components/ui/select";

  // Houdini stores
  const streamStore = new GetStreamStore();
  const healthStore = new GetStreamHealthStore();
  const viewerSessionsStore = new GetViewerSessionsConnectionStore();
  const routingEventsStore = new GetRoutingEventsStore();
  const trackListSub = new TrackListUpdatesStore();

  // Fragment stores for unmasking nested data
  const streamCoreStore = new StreamCoreFieldsStore();
  const streamMetricsStore = new StreamMetricsFieldsStore();

  // Types from Houdini - derive from the new healthStore
  type HealthAnalytics = NonNullable<
    NonNullable<NonNullable<typeof $healthStore.data>["analytics"]>["health"]
  >;
  type LifecycleAnalytics = NonNullable<
    NonNullable<NonNullable<typeof $healthStore.data>["analytics"]>["lifecycle"]
  >;
  type HealthMetricType = NonNullable<HealthAnalytics["streamHealthConnection"]["edges"]>[0]["node"];
  type TrackListEventType = NonNullable<LifecycleAnalytics["trackListConnection"]["edges"]>[0]["node"];
  type BufferEventType = NonNullable<LifecycleAnalytics["bufferEventsConnection"]["edges"]>[0]["node"];
  type ClientMetric5mType = NonNullable<HealthAnalytics["clientQoeConnection"]["edges"]>[0]["node"];

  // page is a store; derive the current param value so it updates on navigation
  let streamId = $derived(page?.params?.id as string ?? "");

  // Get masked stream data from store
  let maskedStream = $derived($streamStore.data?.stream ?? null);

  // Unmask StreamCoreFields
  let streamCoreStoreResult = $derived(
    maskedStream ? fragment(maskedStream, streamCoreStore) : null
  );
  let streamCore = $derived(streamCoreStoreResult ? $streamCoreStoreResult : null);

  // Unmask StreamMetricsFields
  let streamMetricsStoreResult = $derived(
    maskedStream?.metrics ? fragment(maskedStream.metrics, streamMetricsStore) : null
  );
  let streamMetrics = $derived(streamMetricsStoreResult ? $streamMetricsStoreResult : null);

  // Combine unmasked data into stream object
  let stream = $derived(
    streamCore
      ? {
          ...streamCore,
          metrics: streamMetrics,
        }
      : null
  );
  // Real-time metrics from STREAM_BUFFER subscription
  let realtimeMetrics = $derived(stream?.id ? $realtimeStreamMetrics[stream.id] : null);
  let healthMetrics = $derived(
    ($healthStore.data?.analytics?.health?.streamHealthConnection?.edges ?? [])
      .map((e: { node: HealthMetricType }) => e?.node)
      .filter((n: HealthMetricType | null | undefined): n is HealthMetricType => n !== null && n !== undefined)
  );
  let currentHealth = $derived(healthMetrics.length > 0 ? healthMetrics[0] : null);

  // Extract buffer health values for histogram (convert 0-1 ratio to 0-100 percentage)
  let bufferHealthValues = $derived(
    healthMetrics
      .map((m: HealthMetricType) => m.bufferHealth)
      .filter((val: number | null | undefined) => val !== null && val !== undefined)
      .map((val: number | null | undefined) => (val as number) * 100)
  );

  // Client metrics (viewer/connection quality)
  let clientMetrics = $derived(
    ($healthStore.data?.analytics?.health?.clientQoeConnection?.edges ?? [])
      .map((e: { node: ClientMetric5mType }) => e?.node)
      .filter((n: ClientMetric5mType | null | undefined): n is ClientMetric5mType => n !== null && n !== undefined)
  );

  // Computed client quality stats
  let clientQualityStats = $derived(() => {
    if (clientMetrics.length === 0) return null;

    const validPacketLoss = clientMetrics.filter((m: ClientMetric5mType) => m.packetLossRate !== null && m.packetLossRate !== undefined);
    const validSessions = clientMetrics.filter((m: ClientMetric5mType) => m.activeSessions !== null && m.activeSessions !== undefined);

    return {
      avgPacketLoss: validPacketLoss.length > 0
        ? validPacketLoss.reduce((sum, m) => sum + (m.packetLossRate ?? 0), 0) / validPacketLoss.length
        : null,
      peakPacketLoss: validPacketLoss.length > 0
        ? Math.max(...validPacketLoss.map(m => m.packetLossRate ?? 0))
        : null,
      totalSessions: validSessions.length > 0
        ? validSessions.reduce((sum, m) => sum + (m.activeSessions ?? 0), 0)
        : 0,
      currentSessions: validSessions.length > 0 ? (validSessions[0]?.activeSessions ?? 0) : 0,
    };
  });

  // Per-node breakdown from client metrics
  let nodeBreakdown = $derived(() => {
    const nodeMap = new SvelteMap<string, { sessions: number; packetLoss: number[]; quality: number[] }>();

    for (const metric of clientMetrics) {
      const nodeId = metric.nodeId ?? 'unknown';
      if (!nodeMap.has(nodeId)) {
        nodeMap.set(nodeId, { sessions: 0, packetLoss: [], quality: [] });
      }
      const node = nodeMap.get(nodeId)!;
      node.sessions += metric.activeSessions ?? 0;
      if (metric.packetLossRate !== null && metric.packetLossRate !== undefined) {
        node.packetLoss.push(metric.packetLossRate);
        node.quality.push(1 - metric.packetLossRate);
      }
    }

    return Array.from(nodeMap.entries()).map(([nodeId, data]) => ({
      nodeId,
      totalSessions: data.sessions,
      avgPacketLoss: data.packetLoss.length > 0
        ? data.packetLoss.reduce((a, b) => a + b, 0) / data.packetLoss.length
        : null,
      avgQuality: data.quality.length > 0
        ? data.quality.reduce((a, b) => a + b, 0) / data.quality.length
        : null,
    }));
  });

  let trackListEvents = $state<TrackListEventType[]>([]);
  let bufferEvents = $derived.by(() => {
    const edges = $healthStore.data?.analytics?.lifecycle?.bufferEventsConnection?.edges ?? [];
    return edges
      .map((e: { node: BufferEventType }) => e?.node)
      .filter((n: BufferEventType | null | undefined): n is BufferEventType => n !== null && n !== undefined)
      .sort((a, b) => new Date(b.timestamp).getTime() - new Date(a.timestamp).getTime());
  });
  let rebufferingEvents = $derived($healthStore.data?.analytics?.health?.rebufferingEventsConnection?.edges?.map(e => e.node) ?? []);

  // 5-minute health aggregates
  let health5mData = $derived(
    ($healthStore.data?.analytics?.health?.streamHealth5mConnection?.edges ?? [])
      .map(e => e.node)
      .sort((a, b) => new Date(a.timestamp).getTime() - new Date(b.timestamp).getTime())
  );

  // Aggregate stats from 5m data
  let health5mSummary = $derived.by(() => {
    if (!health5mData.length) return null;
    const totalRebuffers = health5mData.reduce((sum, d) => sum + (d.rebufferCount ?? 0), 0);
    const totalIssues = health5mData.reduce((sum, d) => sum + (d.issueCount ?? 0), 0);
    const totalBufferDry = health5mData.reduce((sum, d) => sum + (d.bufferDryCount ?? 0), 0);
    const avgBitrate = health5mData.reduce((sum, d) => sum + (d.avgBitrate ?? 0), 0) / health5mData.length;
    const avgFps = health5mData.reduce((sum, d) => sum + (d.avgFps ?? 0), 0) / health5mData.length;
    return { totalRebuffers, totalIssues, totalBufferDry, avgBitrate, avgFps };
  });

  let viewerSessions = $derived(
    $viewerSessionsStore.data?.analytics?.lifecycle?.viewerSessionsConnection?.edges?.map(e => e.node) ?? []
  );
  let routingEvents = $derived(
    ($routingEventsStore.data?.analytics?.infra?.routingEventsConnection?.edges ?? []).map(e => e.node)
  );
  let loading = $derived($streamStore.fetching || $healthStore.fetching || $viewerSessionsStore.fetching || $routingEventsStore.fetching);

  // Aggregate viewer geography from viewer sessions
  let viewerGeography = $derived.by(() => {
    const countryMap = new SvelteMap<string, { count: number; cities: SvelteMap<string, number> }>();

    for (const session of viewerSessions) {
      const country = session.countryCode || 'Unknown';
      const city = session.city || 'Unknown';

      if (!countryMap.has(country)) {
        countryMap.set(country, { count: 0, cities: new SvelteMap() });
      }
      const countryData = countryMap.get(country)!;
      countryData.count++;
      countryData.cities.set(city, (countryData.cities.get(city) || 0) + 1);
    }

    const countries = Array.from(countryMap.entries())
      .map(([code, data]) => ({
        countryCode: code,
        viewerCount: data.count,
        topCities: Array.from(data.cities.entries())
          .map(([city, count]) => ({ city, count }))
          .sort((a, b) => b.count - a.count)
          .slice(0, 3)
      }))
      .sort((a, b) => b.viewerCount - a.viewerCount);

    return {
      totalCountries: countries.length,
      countries: countries.slice(0, 5),
    };
  });

  // Routing efficiency for this stream
  let streamRoutingEfficiency = $derived.by(() => {
    if (routingEvents.length === 0) return null;

    let successCount = 0;
    let totalDistance = 0;
    let distanceCount = 0;

    for (const event of routingEvents) {
      if (event.selectedNode) successCount++;
      if (event.routingDistance) {
        totalDistance += event.routingDistance;
        distanceCount++;
      }
    }

    return {
      totalDecisions: routingEvents.length,
      successRate: (successCount / routingEvents.length) * 100,
      avgDistance: distanceCount > 0 ? totalDistance / distanceCount : 0,
    };
  });
  let error = $state<string | null>(null);
  let timeRange = $state("24h");
  let currentRange = $derived(resolveTimeRange(timeRange));
  const timeRangeOptions = TIME_RANGE_OPTIONS.filter((option) => ["24h", "7d", "30d"].includes(option.value));

  // Pagination state
  let healthMetricsDisplayCount = $state(10);
  let trackListDisplayCount = $state(10);
  let viewerSessionsDisplayCount = $state(10);
  let hasMoreHealthMetrics = $derived(healthMetrics.length > healthMetricsDisplayCount);
  let hasMoreTrackListEvents = $derived(trackListEvents.length > trackListDisplayCount);
  let hasMoreViewerSessions = $derived(viewerSessions.length > viewerSessionsDisplayCount);
  let loadingMoreHealthMetrics = $state(false);
  let loadingMoreTrackListEvents = $state(false);
  let loadingMoreViewerSessions = $state(false);

  // Check if there are more pages to load from server
  let healthMetricsHasNextPage = $derived(
    $healthStore.data?.analytics?.health?.streamHealthConnection?.pageInfo?.hasNextPage ?? false
  );
  let trackListEventsHasNextPage = $derived(
    $healthStore.data?.analytics?.lifecycle?.trackListConnection?.pageInfo?.hasNextPage ?? false
  );
  let viewerSessionsHasNextPage = $derived(
    $viewerSessionsStore.data?.analytics?.lifecycle?.viewerSessionsConnection?.pageInfo?.hasNextPage ?? false
  );

  const getTimeRange = () => {
    const range = resolveTimeRange(timeRange);
    currentRange = range;
    return { start: range.start, end: range.end };
  };

  const getMetricsFirst = () => {
    const range = resolveTimeRange(timeRange);
    currentRange = range;
    if (range.days <= 7) {
      return Math.min(range.days * 24 * 12, 1000);
    }
    return 288;
  };

  // Auto-refresh interval
  let refreshInterval: ReturnType<typeof setInterval> | null = null;

  // Effect to handle track list subscription errors
  $effect(() => {
    const errors = $trackListSub.errors;
    if (errors?.length) {
      console.warn("Track list subscription error:", errors);
      // Non-fatal: page still works, just without real-time updates
    }
  });

  // Effect to handle track list subscription updates
  $effect(() => {
    const update = $trackListSub.data?.liveTrackListUpdates;
    if (update) {
      untrack(() => handleTrackListUpdate(update));
    }
  });

  // Effect to sync track list events from store
  // IMPORTANT: The length check must be INSIDE untrack() to avoid creating a reactive dependency
  // on trackListEvents, which would cause an effect loop
  $effect(() => {
    const edges = $healthStore.data?.analytics?.lifecycle?.trackListConnection?.edges;
    if (edges) {
      untrack(() => {
        // Only sync if we haven't populated yet (check inside untrack to avoid dependency)
        if (trackListEvents.length === 0) {
          trackListEvents = edges
            .map((e: { node: TrackListEventType }) => e?.node)
            .filter((n: TrackListEventType | null | undefined): n is TrackListEventType => n !== null && n !== undefined);
        }
      });
    }
  });

  onMount(async () => {
    if (!streamId) {
      error = "Invalid stream ID";
      return;
    }
    await loadStreamData();
    await loadHealthData();
    startTrackListSubscription();
    // Set up auto-refresh every 30 seconds for current health
    refreshInterval = setInterval(async () => {
      try {
        if (streamId) {
          const analyticsStreamId = stream?.id ?? streamId;
          await healthStore.fetch({
            variables: { id: streamId, streamId: analyticsStreamId, timeRange: getTimeRange(), metricsFirst: getMetricsFirst() }
          });
        }
      } catch (err) {
        console.error("Failed to refresh health data:", err);
      }
    }, 30000);
  });

  onDestroy(() => {
    if (refreshInterval) {
      clearInterval(refreshInterval);
    }
    trackListSub.unlisten();
  });

  function startTrackListSubscription() {
    if (!stream) return;
    trackListSub.listen({ streamId: stream.id });
  }


  // Utility functions for color formatting
  function getBufferStateColor(state: string | null | undefined): string {
    if (!state) return "text-muted-foreground";
    switch (state.toLowerCase()) {
      case "good": return "text-success";
      case "warning": return "text-warning";
      case "critical": return "text-destructive";
      default: return "text-muted-foreground";
    }
  }


  async function loadStreamData() {
    if (!streamId) return;
    try {
      const analyticsStreamId = stream?.id ?? streamId;
      const result = await streamStore.fetch({ variables: { id: streamId, streamId: analyticsStreamId } });

      if (!result.data?.stream) {
        error = "Stream not found";
        return;
      }
    } catch (err: unknown) {
      // Ignore AbortErrors which happen on navigation/cancellation
      const errObj = err as { name?: string; message?: string };
      if (errObj.name === 'AbortError' || errObj.message === 'aborted' || errObj.message === 'Aborted') {
        return;
      }
      console.error("Failed to load stream:", err);
      error = "Failed to load stream data";
    }
  }

  async function loadHealthData() {
    if (!streamId) return;
    try {
      // Load all health data in a single query via Stream edges
      const analyticsStreamId = stream?.id ?? streamId;
      await healthStore.fetch({
        variables: {
          id: streamId,
          streamId: analyticsStreamId,
          timeRange: getTimeRange(),
          metricsFirst: getMetricsFirst(), // Combined for both health and client metrics
        },
      });
      // Fetch viewer sessions and routing events in parallel
      await Promise.all([
        viewerSessionsStore.fetch({
          variables: {
            streamId: analyticsStreamId,
            timeRange: getTimeRange(),
            first: 200,
          },
        }).catch(() => null),
        routingEventsStore.fetch({
          variables: {
            streamId: analyticsStreamId,
            timeRange: getTimeRange(),
          },
        }).catch(() => null),
      ]);
    } catch (err: unknown) {
      // Ignore AbortErrors which happen on navigation/cancellation
      const errObj = err as { name?: string; message?: string };
      if (errObj.name === 'AbortError' || errObj.message === 'aborted' || errObj.message === 'Aborted') {
        return;
      }
      console.error("Failed to load health data:", err);
      error = "Failed to load health monitoring data";
    }
  }

  function handleTrackListUpdate(event: NonNullable<TrackListUpdates$result["liveTrackListUpdates"]>) {
    // Add the new track list event to the list
    const newEvent: TrackListEventType = {
      timestamp: new Date().toISOString(),
      streamId: event.streamId ?? "",
      trackList: (event.tracks ?? []).map(t => t?.trackName).filter(Boolean).join(", "),
      trackCount: event.totalTracks || 0,
      tracks: event.tracks ?? [],
      nodeId: null,
    };

    trackListEvents = [newEvent, ...trackListEvents].slice(0, 100);

    // Show toast for significant track changes
    if (trackListEvents.length > 1 && event.totalTracks !== trackListEvents[1]?.trackCount) {
      toast.success(`Track list updated: ${event.totalTracks} track(s) active`);
    }
  }

  function formatTimestamp(timestamp: string) {
    return new Date(timestamp).toLocaleString();
  }

  function parseBufferPayload(payload: unknown): Record<string, unknown> | null {
    if (!payload) return null;
    if (typeof payload === "string") {
      try {
        return JSON.parse(payload) as Record<string, unknown>;
      } catch {
        return null;
      }
    }
    if (typeof payload === "object") {
      return payload as Record<string, unknown>;
    }
    return null;
  }

  // Parse tracks from trackList JSON string if tracks array is empty/malformed
  function getTracksForEvent(event: TrackListEventType) {
    // If tracks array has valid data, use it
    if (event.tracks && event.tracks.length > 0 && event.tracks[0]?.trackName) {
      return event.tracks;
    }
    // Otherwise try to parse from trackList JSON string
    if (event.trackList) {
      try {
        const parsed = JSON.parse(event.trackList);
        if (Array.isArray(parsed)) {
          return parsed;
        }
      } catch {
        // Not valid JSON, ignore
      }
    }
    return [];
  }

  function navigateBack() {
    goto(resolve(`/streams/${streamId}`));
  }

  async function loadMoreHealthMetrics() {
    // Show more from already loaded data
    if (healthMetrics.length > healthMetricsDisplayCount) {
      healthMetricsDisplayCount = Math.min(healthMetricsDisplayCount + 10, healthMetrics.length);
    }
  }

  async function loadMoreTrackListEvents() {
    // Show more from already loaded data
    if (trackListEvents.length > trackListDisplayCount) {
      trackListDisplayCount = Math.min(trackListDisplayCount + 10, trackListEvents.length);
    }
  }

  async function loadMoreViewerSessions() {
    if (viewerSessions.length > viewerSessionsDisplayCount) {
      viewerSessionsDisplayCount = Math.min(viewerSessionsDisplayCount + 10, viewerSessions.length);
      return;
    }
    if (!viewerSessionsHasNextPage || loadingMoreViewerSessions) return;
    try {
      loadingMoreViewerSessions = true;
      await viewerSessionsStore.loadNextPage();
    } catch (err) {
      console.error("Failed to load more viewer sessions:", err);
    } finally {
      loadingMoreViewerSessions = false;
    }
  }

  function handleTimeRangeChange(value: string) {
    timeRange = value;
    trackListEvents = [];
    healthMetricsDisplayCount = 10;
    trackListDisplayCount = 10;
    viewerSessionsDisplayCount = 10;
    loadHealthData();
  }

  const ArrowLeftIcon = getIconComponent("ArrowLeft");
  const AlertTriangleIcon = getIconComponent("AlertTriangle");
  const PauseIcon = getIconComponent("Pause");
  const InfoIcon = getIconComponent("Info");
  const CalendarIcon = getIconComponent("Calendar");
  const GlobeIcon = getIconComponent("Globe2");
  const ActivityIcon = getIconComponent("Activity");
  const MapPinIcon = getIconComponent("MapPin");

  // Protocol hint tooltip text for packet loss N/A values
  const packetLossHint = "Packet statistics are available for UDP-based protocols (SRT, WebRTC) which prioritize low latency. HTTP-based protocols (HLS, DASH) use TCP which guarantees delivery but adds latency through retransmission.";
</script>

<svelte:head>
  <title>Stream Health - {stream?.name || 'Loading...'} - FrameWorks</title>
</svelte:head>

<div class="h-full flex flex-col">
  <!-- Fixed Page Header -->
  <div class="px-4 sm:px-6 lg:px-8 py-4 border-b border-[hsl(var(--tn-fg-gutter)/0.3)] shrink-0">
    <div class="flex items-center justify-between gap-4">
      <div class="flex items-center gap-4">
        <Button
          variant="ghost"
          size="icon"
          class="rounded-full"
          onclick={navigateBack}
        >
          <ArrowLeftIcon class="w-5 h-5" />
        </Button>
        <div>
          <h1 class="text-xl font-bold text-foreground">Stream Health</h1>
          <p class="text-sm text-muted-foreground mt-0.5">
            {#if stream}{stream.name} • {/if}{currentRange.label}
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

  <!-- Content -->
  <div class="flex-1 overflow-y-auto">
    {#if loading}
      <div class="p-4 space-y-4">
        <LoadingCard variant="analytics" />
        <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
          <LoadingCard variant="analytics" />
          <LoadingCard variant="analytics" />
        </div>
      </div>
    {:else if error}
      <div class="slab">
        <div class="slab-body--padded text-center">
          <AlertTriangleIcon class="w-8 h-8 text-error mx-auto mb-4" />
          <h3 class="text-lg font-semibold text-error mb-2">Error Loading Health Data</h3>
          <p class="text-muted-foreground mb-4">{error}</p>
          <Button variant="destructive" onclick={loadHealthData}>Retry</Button>
        </div>
      </div>
    {:else}
      <!-- Current Health Status -->
      {#if currentHealth}
        <div class="slab">
          <div class="slab-header">
            <h2>Current Health Status</h2>
          </div>
          <div class="slab-body--padded">
            <div class="grid grid-cols-1 md:grid-cols-2 gap-6">
              <!-- Buffer State -->
              <div class="flex items-center justify-center py-4">
                <BufferStateIndicator
                  bufferState={currentHealth.bufferState}
                  bufferHealth={currentHealth.bufferHealth}
                  size="lg"
                />
              </div>

              <!-- Key Metrics -->
              <div class="space-y-3">
                <div class="flex justify-between border-b border-border/30 pb-2">
                  <span class="text-muted-foreground">Quality Tier</span>
                  <span class="font-mono text-info">{currentHealth.qualityTier || 'Unknown'}</span>
                </div>
                <div class="flex justify-between border-b border-border/30 pb-2">
                  <span class="text-muted-foreground">Bitrate</span>
                  <span class="font-mono text-success">
                    {currentHealth.bitrate ? `${(currentHealth.bitrate / 1000).toFixed(2)} Mbps` : 'N/A'}
                  </span>
                </div>
                <div class="flex justify-between">
                  <span class="text-muted-foreground">Buffer Health</span>
                  <span class="font-mono {(currentHealth.bufferHealth ?? 0) < 0.5 ? 'text-warning' : 'text-success'}">
                    {currentHealth.bufferHealth ? `${(currentHealth.bufferHealth * 100).toFixed(0)}%` : 'N/A'}
                  </span>
                </div>
                {#if currentHealth.issuesDescription}
                  <div class="mt-4 p-3 bg-warning/10 border border-warning/30">
                    <span class="text-sm text-warning">{currentHealth.issuesDescription}</span>
                  </div>
                {/if}
              </div>
            </div>
          </div>
        </div>

        <!-- Encoding Details -->
        {#if currentHealth.qualityTier || currentHealth.gopSize || currentHealth.codec || currentHealth.fps}
          <div class="slab border-t-0">
            <div class="slab-header">
              <h3>Encoding Details</h3>
            </div>
            <GridSeam cols={4} stack="2x2" surface="panel" flush={true}>
              {#if currentHealth.qualityTier}
                <div class="p-4">
                  <p class="text-xs text-muted-foreground uppercase tracking-wide mb-1">Quality</p>
                  <p class="font-mono text-lg text-info">{currentHealth.qualityTier}</p>
                </div>
              {/if}
              {#if currentHealth.codec}
                <div class="p-4">
                  <p class="text-xs text-muted-foreground uppercase tracking-wide mb-1">Codec</p>
                  <p class="font-mono text-lg text-accent-purple">{currentHealth.codec}</p>
                </div>
              {/if}
              {#if currentHealth.gopSize}
                <div class="p-4">
                  <p class="text-xs text-muted-foreground uppercase tracking-wide mb-1">GOP Size</p>
                  <p class="font-mono text-lg text-primary">{currentHealth.gopSize} frames</p>
                </div>
              {/if}
              {#if currentHealth.fps}
                <div class="p-4">
                  <p class="text-xs text-muted-foreground uppercase tracking-wide mb-1">Frame Rate</p>
                  <p class="font-mono text-lg text-warning-alt">{currentHealth.fps.toFixed(1)} fps</p>
                </div>
              {/if}
              {#if currentHealth.width && currentHealth.height}
                <div class="p-4">
                  <p class="text-xs text-muted-foreground uppercase tracking-wide mb-1">Resolution</p>
                  <p class="font-mono text-lg text-success">{currentHealth.width}x{currentHealth.height}</p>
                </div>
              {/if}
              {#if currentHealth.bitrate}
                <div class="p-4">
                  <p class="text-xs text-muted-foreground uppercase tracking-wide mb-1">Bitrate</p>
                  <p class="font-mono text-lg text-primary">{(currentHealth.bitrate / 1000).toFixed(2)} Mbps</p>
                </div>
              {/if}
            </GridSeam>
          </div>
        {/if}

        <!-- Real-time Metrics (from STREAM_BUFFER subscription) -->
        {#if realtimeMetrics && (realtimeMetrics.streamJitterMs !== undefined || realtimeMetrics.streamBufferMs !== undefined || realtimeMetrics.maxKeepawaMs !== undefined)}
          <div class="slab border-t-0">
            <div class="slab-header flex items-center gap-2">
              <h3>Real-time Metrics</h3>
              <span class="px-2 py-0.5 bg-success/20 text-success text-xs rounded-full">LIVE</span>
            </div>
            <GridSeam cols={4} stack="2x2" surface="panel" flush={true}>
              {#if realtimeMetrics.streamBufferMs !== undefined && realtimeMetrics.streamBufferMs !== null}
                <div class="p-4">
                  <p class="text-xs text-muted-foreground uppercase tracking-wide mb-1">Buffer Depth</p>
                  <p class="font-mono text-lg text-info">{realtimeMetrics.streamBufferMs}ms</p>
                </div>
              {/if}
              {#if realtimeMetrics.streamJitterMs !== undefined && realtimeMetrics.streamJitterMs !== null}
                <div class="p-4">
                  <p class="text-xs text-muted-foreground uppercase tracking-wide mb-1">Jitter</p>
                  <p class="font-mono text-lg {realtimeMetrics.streamJitterMs > 100 ? 'text-destructive' : realtimeMetrics.streamJitterMs > 50 ? 'text-warning' : 'text-success'}">
                    {realtimeMetrics.streamJitterMs}ms
                  </p>
                </div>
              {/if}
              {#if realtimeMetrics.maxKeepawaMs !== undefined && realtimeMetrics.maxKeepawaMs !== null}
                <div class="p-4">
                  <p class="text-xs text-muted-foreground uppercase tracking-wide mb-1">Viewer Lag</p>
                  <p class="font-mono text-lg text-muted-foreground">{realtimeMetrics.maxKeepawaMs}ms</p>
                </div>
              {/if}
              {#if realtimeMetrics.trackCount !== undefined && realtimeMetrics.trackCount !== null}
                <div class="p-4">
                  <p class="text-xs text-muted-foreground uppercase tracking-wide mb-1">Active Tracks</p>
                  <p class="font-mono text-lg text-primary">{realtimeMetrics.trackCount}</p>
                </div>
              {/if}
            </GridSeam>
            {#if realtimeMetrics.hasIssues && realtimeMetrics.mistIssues}
              <div class="p-4 bg-warning/10 border-t border-warning/30">
                <p class="text-sm text-warning">
                  <span class="font-medium">Issues detected:</span> {realtimeMetrics.mistIssues}
                </p>
              </div>
            {/if}
          </div>
        {/if}

        <!-- Buffer Details -->
        {#if currentHealth.bufferSize || currentHealth.bufferHealth}
          <div class="slab border-t-0">
            <div class="slab-header">
              <h3>Buffer Details</h3>
            </div>
            <GridSeam cols={2} stack="2x2" surface="panel" flush={true}>
              {#if currentHealth.bufferSize}
                <div class="p-4">
                  <p class="text-xs text-muted-foreground uppercase tracking-wide mb-1">Buffer Duration</p>
                  <p class="font-mono text-lg text-primary">{(currentHealth.bufferSize / 1000).toFixed(1)}s</p>
                </div>
              {/if}
              {#if currentHealth.bufferHealth != null}
                {@const healthPercent = currentHealth.bufferHealth * 100}
                <div class="p-4">
                  <p class="text-xs text-muted-foreground uppercase tracking-wide mb-1">Buffer Health</p>
                  <div class="flex items-center gap-2">
                    <div class="flex-1 bg-muted h-2">
                      <div
                        class="h-2 {healthPercent < 30 ? 'bg-destructive' : healthPercent < 60 ? 'bg-warning' : 'bg-success'}"
                        style="width: {Math.min(healthPercent, 100)}%"
                      ></div>
                    </div>
                    <span class="font-mono text-sm {healthPercent < 30 ? 'text-destructive' : healthPercent < 60 ? 'text-warning' : 'text-success'}">
                      {healthPercent.toFixed(0)}%
                    </span>
                  </div>
                </div>
              {/if}
            </GridSeam>
          </div>
        {/if}

      {/if}

      <!-- Client Quality Section -->
      {#if clientMetrics.length > 0}
        {@const stats = clientQualityStats()}
        {@const nodes = nodeBreakdown()}
        <div class="slab">
          <div class="slab-header">
            <h2>Client Quality</h2>
            <span class="text-xs text-muted-foreground font-normal normal-case ml-2">
              Viewer connection metrics (5-min aggregates)
            </span>
          </div>

          <!-- Stats Grid -->
          <GridSeam cols={4} stack="2x2" surface="panel" flush={true}>
            <div class="p-4">
              <p class="text-xs text-muted-foreground uppercase tracking-wide mb-1">Avg Packet Loss</p>
              <p class="font-mono text-lg {(stats?.avgPacketLoss ?? 0) > 0.01 ? 'text-warning' : 'text-success'} flex items-center gap-1">
                {stats?.avgPacketLoss != null ? `${(stats.avgPacketLoss * 100).toFixed(3)}%` : 'N/A'}
                {#if stats?.avgPacketLoss == null}
                  <span title={packetLossHint}>
                    <InfoIcon class="w-3.5 h-3.5 text-muted-foreground cursor-help" />
                  </span>
                {/if}
              </p>
            </div>
            <div class="p-4">
              <p class="text-xs text-muted-foreground uppercase tracking-wide mb-1">Peak Packet Loss</p>
              <p class="font-mono text-lg {(stats?.peakPacketLoss ?? 0) > 0.05 ? 'text-destructive' : (stats?.peakPacketLoss ?? 0) > 0.01 ? 'text-warning' : 'text-success'} flex items-center gap-1">
                {stats?.peakPacketLoss != null ? `${(stats.peakPacketLoss * 100).toFixed(3)}%` : 'N/A'}
                {#if stats?.peakPacketLoss == null}
                  <span title={packetLossHint}>
                    <InfoIcon class="w-3.5 h-3.5 text-muted-foreground cursor-help" />
                  </span>
                {/if}
              </p>
            </div>
            <div class="p-4">
              <p class="text-xs text-muted-foreground uppercase tracking-wide mb-1">Current Sessions</p>
              <p class="font-mono text-lg text-info">{stats?.currentSessions ?? 0}</p>
            </div>
          </GridSeam>

          <!-- Client Packet Loss Trend Chart -->
          {#if clientMetrics.length > 1}
            <div class="slab-body--padded border-t border-border/30">
              <h4 class="text-sm font-medium text-muted-foreground mb-3">Packet Loss Trend</h4>
              <HealthTrendChart
                data={clientMetrics.map(m => ({
                  timestamp: m.timestamp,
                  packetLoss: m.packetLossRate,
                }))}
                height={200}
                showBufferHealth={false}
                showBitrate={false}
                showPacketLoss={true}
              />
              <p class="text-xs text-muted-foreground mt-2">
                Packet Loss (red, lower=better)
              </p>
            </div>
          {/if}

          <!-- Per-Node Breakdown -->
          {#if nodes.length > 0}
            <div class="border-t border-border/30">
              <div class="p-3 bg-muted/20">
                <h4 class="text-sm font-medium text-muted-foreground">Per-Node Breakdown</h4>
              </div>
              <div class="divide-y divide-border/30">
                {#each nodes as node (node.nodeId)}
                  <div class="p-3 flex items-center justify-between">
                    <div>
                      <p class="font-mono text-sm text-foreground">{node.nodeId}</p>
                      <p class="text-xs text-muted-foreground">{node.totalSessions} total sessions</p>
                    </div>
                    <div class="flex gap-4 text-right">
                      <div>
                        <p class="text-xs text-muted-foreground">Packet Loss</p>
                        <p class="font-mono text-sm {(node.avgPacketLoss ?? 0) > 0.01 ? 'text-warning' : 'text-success'} flex items-center justify-end gap-1">
                          {node.avgPacketLoss !== null ? `${(node.avgPacketLoss * 100).toFixed(3)}%` : 'N/A'}
                          {#if node.avgPacketLoss === null}
                            <span title={packetLossHint}>
                              <InfoIcon class="w-3 h-3 text-muted-foreground cursor-help" />
                            </span>
                          {/if}
                        </p>
                      </div>
                      <div>
                        <p class="text-xs text-muted-foreground">Quality</p>
                        <p class="font-mono text-sm {(node.avgQuality ?? 1) < 0.95 ? 'text-warning' : 'text-success'}">
                          {node.avgQuality !== null ? `${(node.avgQuality * 100).toFixed(1)}%` : 'N/A'}
                        </p>
                      </div>
                    </div>
                  </div>
                {/each}
              </div>
            </div>
          {/if}
        </div>
      {/if}

      <!-- Viewer Geography & Routing Section -->
      {#if viewerGeography.countries.length > 0 || routingEvents.length > 0}
        <div class="dashboard-grid">
          <!-- Viewer Geography -->
          {#if viewerGeography.countries.length > 0}
            <div class="slab">
              <div class="slab-header">
                <div class="flex items-center gap-2">
                  <GlobeIcon class="w-4 h-4 text-primary" />
                  <h3>Viewer Geography</h3>
                </div>
                <span class="text-xs text-muted-foreground font-normal normal-case ml-2">
                  {viewerGeography.totalCountries} countries
                </span>
              </div>
              <div class="slab-body--padded">
                <div class="space-y-3">
                  {#each viewerGeography.countries as country (country.countryCode)}
                    <div class="p-3 border border-border/30 bg-muted/10">
                      <div class="flex items-center justify-between mb-2">
                        <span class="font-medium text-foreground">{country.countryCode}</span>
                        <span class="text-sm font-mono text-primary">{country.viewerCount} sessions</span>
                      </div>
                      {#if country.topCities.length > 0}
                        <div class="flex flex-wrap gap-2">
                          {#each country.topCities as city (city.city)}
                            <span class="px-2 py-0.5 bg-muted/50 text-xs text-muted-foreground rounded">
                              <MapPinIcon class="w-3 h-3 inline mr-1" />{city.city} ({city.count})
                            </span>
                          {/each}
                        </div>
                      {/if}
                    </div>
                  {/each}
                </div>
              </div>
            </div>
          {/if}

          <!-- Routing Efficiency -->
          {#if streamRoutingEfficiency}
            <div class="slab">
              <div class="slab-header">
                <div class="flex items-center gap-2">
                  <ActivityIcon class="w-4 h-4 text-success" />
                  <h3>Routing Efficiency</h3>
                </div>
              </div>
              <div class="slab-body--padded">
                <div class="grid grid-cols-3 gap-4 mb-4">
                  <div class="text-center p-3 border border-border/30 bg-muted/10">
                    <p class="text-xs text-muted-foreground uppercase mb-1">Decisions</p>
                    <p class="text-xl font-bold text-primary">{streamRoutingEfficiency.totalDecisions}</p>
                  </div>
                  <div class="text-center p-3 border border-border/30 bg-muted/10">
                    <p class="text-xs text-muted-foreground uppercase mb-1">Success Rate</p>
                    <p class="text-xl font-bold text-success">{streamRoutingEfficiency.successRate.toFixed(1)}%</p>
                  </div>
                  <div class="text-center p-3 border border-border/30 bg-muted/10">
                    <p class="text-xs text-muted-foreground uppercase mb-1">Avg Distance</p>
                    <p class="text-xl font-bold text-warning">{streamRoutingEfficiency.avgDistance.toFixed(0)}km</p>
                  </div>
                </div>

                <!-- Recent Routing Decisions -->
                {#if routingEvents.length > 0}
                  <div class="border-t border-border/30 pt-3">
                    <p class="text-xs text-muted-foreground uppercase tracking-wide mb-2">Recent Decisions</p>
                    <div class="space-y-2 max-h-48 overflow-y-auto">
                      {#each routingEvents.slice(0, 5) as evt, i (i)}
                        <div class="flex items-center justify-between p-2 bg-muted/20 text-xs">
                          <div class="flex items-center gap-2">
                            <span class="px-1.5 py-0.5 rounded {evt.status === 'success' || evt.status === 'SUCCESS' ? 'bg-success/20 text-success' : 'bg-destructive/20 text-destructive'} font-mono">
                              {evt.status}
                            </span>
                            <span class="text-muted-foreground">→</span>
                            <span class="font-mono text-foreground">{evt.selectedNode || 'N/A'}</span>
                          </div>
                          <div class="text-right text-muted-foreground">
                            {#if evt.routingDistance}
                              <span class="font-mono">{evt.routingDistance.toFixed(0)}km</span>
                            {/if}
                          </div>
                        </div>
                      {/each}
                    </div>
                  </div>
                {/if}
              </div>
            </div>
          {/if}
        </div>
      {/if}

      <!-- Two-column grid: Health Metrics + Track List -->
      <div class="dashboard-grid">
        <!-- Recent Health Metrics -->
        <div class="slab">
          <div class="slab-header flex items-center justify-between">
            <h3>Recent Health Metrics</h3>
            {#if healthMetrics.length > 0}
              <span class="text-xs text-muted-foreground font-normal normal-case">
                {Math.min(healthMetricsDisplayCount, healthMetrics.length)} of {healthMetrics.length}{#if healthMetricsHasNextPage}+{/if}
              </span>
            {/if}
          </div>
          {#if healthMetrics.length > 0}
            <div class="slab-body--flush max-h-96 overflow-y-auto">
              {#each healthMetrics.slice(0, healthMetricsDisplayCount) as metric (metric.timestamp + metric.nodeId)}
                <div class="p-3 border-b border-border/30 last:border-b-0">
                  <div class="flex justify-between items-start mb-2">
                    <span class="text-xs text-muted-foreground">{formatTimestamp(metric.timestamp)}</span>
                    <span class="text-xs {getBufferStateColor(metric.bufferState)}">
                      {metric.bufferState || 'Unknown'}
                    </span>
                  </div>
                  <div class="grid grid-cols-2 gap-2 text-sm">
                    <div>
                      <span class="text-muted-foreground text-xs">Bitrate</span>
                      <p class="font-mono text-info">
                        {metric.bitrate ? `${(metric.bitrate / 1000).toFixed(2)} Mbps` : 'N/A'}
                      </p>
                    </div>
                    <div>
                      <span class="text-muted-foreground text-xs">Quality</span>
                      <p class="font-mono text-info">{metric.qualityTier || 'N/A'}</p>
                    </div>
                  </div>
                  {#if metric.issuesDescription}
                    <div class="mt-2 p-2 bg-warning/10 border border-warning/30 flex items-start gap-2">
                      <AlertTriangleIcon class="w-4 h-4 text-warning mt-0.5 shrink-0" />
                      <p class="text-sm text-warning">{metric.issuesDescription}</p>
                    </div>
                  {/if}
                </div>
              {/each}
            </div>
            {#if hasMoreHealthMetrics || healthMetricsHasNextPage}
              <div class="slab-actions">
                <Button
                  variant="ghost"
                  class="w-full"
                  onclick={loadMoreHealthMetrics}
                  disabled={loadingMoreHealthMetrics}
                >
                  {loadingMoreHealthMetrics ? 'Loading...' : 'Load More Metrics'}
                </Button>
              </div>
            {/if}
          {:else}
            <div class="slab-body--padded text-center">
              <p class="text-muted-foreground py-8">No health data in {currentRange.label.toLowerCase()}</p>
            </div>
          {/if}
        </div>

        <!-- Track List Updates -->
        <div class="slab">
          <div class="slab-header flex items-center justify-between">
            <h3>Track List Updates</h3>
            {#if trackListEvents.length > 0}
              <span class="text-xs text-muted-foreground font-normal normal-case">
                {Math.min(trackListDisplayCount, trackListEvents.length)} of {trackListEvents.length}{#if trackListEventsHasNextPage}+{/if}
              </span>
            {/if}
          </div>
          {#if trackListEvents.length > 0}
            <div class="slab-body--flush max-h-96 overflow-y-auto">
              {#each trackListEvents.slice(0, trackListDisplayCount) as event, i (i)}
                {@const tracks = getTracksForEvent(event)}
                <div class="p-3 border-b border-border/30 last:border-b-0">
                  <div class="flex justify-between items-start mb-2">
                    <div>
                      <span class="font-medium text-foreground">{event.trackCount} tracks active</span>
                      {#if event.nodeId}
                        <p class="text-xs text-muted-foreground">Node: {event.nodeId}</p>
                      {/if}
                    </div>
                    <span class="text-xs text-muted-foreground">{formatTimestamp(event.timestamp || "")}</span>
                  </div>

                  {#if tracks.length > 0}
                    <div class="space-y-2">
                      {#each tracks as track (track?.trackName ?? Math.random())}
                        <div class="p-2 bg-muted/30 border border-border/30">
                          <div class="flex items-center justify-between mb-2">
                            <div class="flex items-center gap-2">
                              <span class="text-foreground font-medium text-sm">{track?.trackName || 'Unknown'}</span>
                              <span class="text-xs px-1.5 py-0.5 bg-accent-purple/20 text-accent-purple">
                                {track?.trackType || 'N/A'}
                              </span>
                              {#if track?.codec}
                                <span class="text-xs px-1.5 py-0.5 bg-info/20 text-info">
                                  {track.codec}
                                </span>
                              {/if}
                            </div>
                            {#if track?.bitrateKbps}
                              <span class="text-xs font-mono text-success">{track.bitrateKbps} kbps</span>
                            {/if}
                          </div>
                          <div class="grid grid-cols-4 gap-2 text-xs">
                            {#if track?.width && track?.height}
                              <div>
                                <span class="text-muted-foreground">Resolution</span>
                                <p class="font-mono text-foreground">{track.width}x{track.height}</p>
                              </div>
                            {/if}
                            {#if track?.fps}
                              <div>
                                <span class="text-muted-foreground">FPS</span>
                                <p class="font-mono text-foreground">{track.fps.toFixed(1)}</p>
                              </div>
                            {/if}
                            {#if track?.buffer !== undefined && track?.buffer !== null}
                              <div>
                                <span class="text-muted-foreground">Buffer</span>
                                <p class="font-mono {track.buffer < 100 ? 'text-warning' : 'text-success'}">{track.buffer}ms</p>
                              </div>
                            {/if}
                            {#if track?.jitter !== undefined && track?.jitter !== null}
                              <div>
                                <span class="text-muted-foreground">Jitter</span>
                                <p class="font-mono {(track.jitter || 0) > 50 ? 'text-warning' : 'text-success'}">{track.jitter}ms</p>
                              </div>
                            {/if}
                            {#if track?.channels}
                              <div>
                                <span class="text-muted-foreground">Channels</span>
                                <p class="font-mono text-foreground">{track.channels}</p>
                              </div>
                            {/if}
                            {#if track?.sampleRate}
                              <div>
                                <span class="text-muted-foreground">Sample Rate</span>
                                <p class="font-mono text-foreground">{(track.sampleRate / 1000).toFixed(1)} kHz</p>
                              </div>
                            {/if}
                            {#if track?.hasBFrames !== undefined && track?.hasBFrames !== null}
                              <div>
                                <span class="text-muted-foreground">B-Frames</span>
                                <p class="font-mono {track.hasBFrames ? 'text-success' : 'text-muted-foreground'}">{track.hasBFrames ? 'Yes' : 'No'}</p>
                              </div>
                            {/if}
                          </div>
                        </div>
                      {/each}
                    </div>
                  {/if}
                </div>
              {/each}
            </div>
            {#if hasMoreTrackListEvents || trackListEventsHasNextPage}
              <div class="slab-actions">
                <Button
                  variant="ghost"
                  class="w-full"
                  onclick={loadMoreTrackListEvents}
                  disabled={loadingMoreTrackListEvents}
                >
                  {loadingMoreTrackListEvents ? 'Loading...' : 'Load More Events'}
                </Button>
              </div>
            {/if}
          {:else}
            <div class="slab-body--padded text-center">
              <p class="text-muted-foreground py-8">No track list updates recorded</p>
            </div>
          {/if}
        </div>
      </div>

      <!-- Buffer Events -->
      {#if bufferEvents.length > 0}
        <div class="slab">
          <div class="slab-header flex items-center justify-between">
            <h3>Buffer Events</h3>
            <span class="text-xs text-muted-foreground font-normal normal-case">
              {Math.min(10, bufferEvents.length)} of {bufferEvents.length}
            </span>
          </div>
          <div class="slab-body--flush max-h-72 overflow-y-auto">
            {#each bufferEvents.slice(0, 10) as event (event.eventId)}
              {@const payload = parseBufferPayload(event.payload)}
              {@const health = payload?.health as Record<string, unknown> | undefined}
              {@const trackCount = Array.isArray(health?.tracks) ? health?.tracks?.length : null}
              <div class="p-3 border-b border-border/30 last:border-b-0">
                <div class="flex items-start justify-between gap-4">
                  <div class="flex items-center gap-2">
                    <BufferStateIndicator bufferState={event.bufferState} size="sm" compact />
                    <div>
                      <p class="font-medium text-foreground">Buffer {event.bufferState}</p>
                      {#if event.nodeId}
                        <p class="text-xs text-muted-foreground">Node: {event.nodeId}</p>
                      {/if}
                    </div>
                  </div>
                  <span class="text-xs text-muted-foreground">{formatTimestamp(event.timestamp)}</span>
                </div>

                <div class="grid grid-cols-2 md:grid-cols-4 gap-3 mt-3 text-xs">
                  <div>
                    <span class="text-muted-foreground">Buffer</span>
                    <p class="font-mono text-foreground">
                      {typeof health?.buffer === "number" ? `${health?.buffer}ms` : "—"}
                    </p>
                  </div>
                  <div>
                    <span class="text-muted-foreground">Jitter</span>
                    <p class="font-mono text-foreground">
                      {typeof health?.jitter === "number" ? `${health?.jitter}ms` : "—"}
                    </p>
                  </div>
                  <div>
                    <span class="text-muted-foreground">Max Keepaway</span>
                    <p class="font-mono text-foreground">
                      {typeof health?.maxkeepaway === "number" ? `${health?.maxkeepaway}ms` : "—"}
                    </p>
                  </div>
                  <div>
                    <span class="text-muted-foreground">Tracks</span>
                    <p class="font-mono text-foreground">{trackCount ?? "—"}</p>
                  </div>
                </div>

                {#if typeof health?.issues === "string" && health?.issues.length > 0}
                  <div class="mt-3 p-2 bg-warning/10 border border-warning/30 flex items-start gap-2">
                    <AlertTriangleIcon class="w-4 h-4 text-warning mt-0.5 shrink-0" />
                    <p class="text-sm text-warning">{health?.issues}</p>
                  </div>
                {/if}

                {#if !health && event.eventData}
                  <div class="mt-3 p-2 bg-muted/30 border border-border/30">
                    <p class="text-xs text-muted-foreground">Event Data</p>
                    <p class="text-xs font-mono text-foreground break-all">{event.eventData}</p>
                  </div>
                {/if}
              </div>
            {/each}
          </div>
        </div>
      {/if}

      <!-- Rebuffering Events -->
      {#if rebufferingEvents.length > 0}
        <div class="slab">
          <div class="slab-header">
            <h3>Rebuffering Events</h3>
          </div>
          <div class="slab-body--flush max-h-64 overflow-y-auto">
            {#each rebufferingEvents.slice(0, 10) as event, i (`${event.timestamp}-${event.nodeId}-${event.rebufferStart}-${i}`)}
              <div class="p-3 border-b border-border/30 last:border-b-0">
                <div class="flex justify-between items-start">
                  <div class="flex items-center gap-2">
                    <PauseIcon class="w-4 h-4 text-warning-alt" />
                    <span class="font-medium text-foreground">
                      {event.rebufferStart ? 'Rebuffer Started' : 'Rebuffer Ended'}
                    </span>
                  </div>
                  <span class="text-xs text-muted-foreground">{formatTimestamp(event.timestamp)}</span>
                </div>
                <div class="mt-2 text-sm">
                  <span class="text-muted-foreground text-xs">Buffer State</span>
                  <p class={getBufferStateColor(event.bufferState)}>{event.bufferState}</p>
                </div>
              </div>
            {/each}
          </div>
        </div>
      {/if}

      <!-- Viewer Sessions -->
      <div class="slab">
        <div class="slab-header flex items-center justify-between">
          <h3>Viewer Sessions</h3>
          {#if viewerSessions.length > 0}
            <span class="text-xs text-muted-foreground font-normal normal-case">
              {Math.min(viewerSessionsDisplayCount, viewerSessions.length)} of {viewerSessions.length}{#if viewerSessionsHasNextPage}+{/if}
            </span>
          {/if}
        </div>
        {#if viewerSessions.length > 0}
          <div class="slab-body--flush">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Time</TableHead>
                  <TableHead>Session</TableHead>
                  <TableHead>Protocol</TableHead>
                  <TableHead>Location</TableHead>
                  <TableHead class="text-right">Duration</TableHead>
                  <TableHead class="text-right">Quality</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {#each viewerSessions.slice(0, viewerSessionsDisplayCount) as session (session.id)}
                  <TableRow>
                    <TableCell class="text-xs text-muted-foreground font-mono">
                      {formatTimestamp(session.timestamp)}
                    </TableCell>
                    <TableCell class="font-mono text-xs">
                      {session.sessionId?.slice(0, 8) ?? "—"}
                    </TableCell>
                    <TableCell class="text-xs">
                      {session.connector ?? "—"}
                    </TableCell>
                    <TableCell class="text-xs">
                      {session.city || "Unknown"}{session.countryCode ? `, ${session.countryCode}` : ""}
                    </TableCell>
                    <TableCell class="text-xs text-right">
                      {session.durationSeconds ? `${Math.round(session.durationSeconds)}s` : "—"}
                    </TableCell>
                    <TableCell class="text-xs text-right">
                      {session.connectionQuality != null ? `${(session.connectionQuality * 100).toFixed(1)}%` : "—"}
                    </TableCell>
                  </TableRow>
                {/each}
              </TableBody>
            </Table>
          </div>
          {#if hasMoreViewerSessions || viewerSessionsHasNextPage}
            <div class="slab-actions">
              <Button
                variant="ghost"
                class="w-full"
                onclick={loadMoreViewerSessions}
                disabled={loadingMoreViewerSessions}
              >
                {loadingMoreViewerSessions ? "Loading..." : "Load More Sessions"}
              </Button>
            </div>
          {/if}
        {:else}
          <div class="slab-body--padded text-center">
            <p class="text-muted-foreground py-6">No viewer sessions in {currentRange.label.toLowerCase()}</p>
          </div>
        {/if}
      </div>

      <!-- Buffer Health Histogram -->
      {#if bufferHealthValues.length > 0}
        <div class="slab">
          <div class="slab-header">
            <h3>Buffer Health Distribution</h3>
          </div>
          <div class="slab-body--padded">
            <BufferHealthHistogram 
              data={bufferHealthValues} 
              height={250} 
            />
          </div>
        </div>
      {/if}

      <!-- Historical Health Trends Chart -->
      <div class="slab">
        <div class="slab-header flex items-center justify-between">
          <h3>Historical Health Trends</h3>
          {#if healthMetrics.length > 0}
            <span class="text-xs text-muted-foreground font-normal normal-case">
              {healthMetrics.length} data points
            </span>
          {/if}
        </div>
        {#if healthMetrics.length > 0}
          <div class="slab-body--padded">
            <HealthTrendChart
              data={healthMetrics.map(m => ({
                timestamp: m.timestamp,
                bufferHealth: m.bufferHealth,
                bitrate: m.bitrate,
              }))}
              height={350}
              showBufferHealth={true}
              showBitrate={true}
            />
          </div>
        {:else}
          <div class="slab-body--padded text-center">
            <p class="text-muted-foreground py-8">No historical health data available</p>
          </div>
        {/if}
      </div>

      <!-- 5-Minute Health Aggregates -->
      {#if health5mData.length > 0}
      <div class="slab">
        <div class="slab-header flex items-center justify-between">
          <h3>5-Minute Aggregates</h3>
          {#if health5mSummary}
            <div class="flex items-center gap-4 text-xs">
              <span class="text-muted-foreground">
                Rebuffers: <span class="font-semibold {health5mSummary.totalRebuffers === 0 ? 'text-success' : 'text-warning'}">{health5mSummary.totalRebuffers}</span>
              </span>
              <span class="text-muted-foreground">
                Issues: <span class="font-semibold {health5mSummary.totalIssues === 0 ? 'text-success' : 'text-destructive'}">{health5mSummary.totalIssues}</span>
              </span>
              <span class="text-muted-foreground">
                Buffer Dry: <span class="font-semibold {health5mSummary.totalBufferDry === 0 ? 'text-success' : 'text-warning'}">{health5mSummary.totalBufferDry}</span>
              </span>
            </div>
          {/if}
        </div>
        <div class="slab-body--padded">
          <!-- 5m Trend Bars -->
          <div class="space-y-4">
            <!-- Rebuffer Count Trend -->
            <div>
              <div class="flex items-center justify-between mb-2">
                <span class="text-xs text-muted-foreground uppercase tracking-wide">Rebuffers Over Time</span>
                <span class="text-xs text-muted-foreground">{health5mData.length} intervals</span>
              </div>
              <div class="flex items-end gap-px h-12">
                {#each health5mData as point (point.timestamp)}
                  {@const maxRebuffers = Math.max(...health5mData.map(d => d.rebufferCount ?? 0), 1)}
                  {@const heightPct = (point.rebufferCount ?? 0) / maxRebuffers * 100}
                  <div
                    class="flex-1 transition-all {point.rebufferCount ? 'bg-warning' : 'bg-success/30'}"
                    style="height: {Math.max(heightPct, 4)}%"
                    title="{new Date(point.timestamp).toLocaleTimeString()}: {point.rebufferCount ?? 0} rebuffers"
                  ></div>
                {/each}
              </div>
            </div>

            <!-- Issue Count Trend -->
            <div>
              <div class="flex items-center justify-between mb-2">
                <span class="text-xs text-muted-foreground uppercase tracking-wide">Issues Over Time</span>
              </div>
              <div class="flex items-end gap-px h-12">
                {#each health5mData as point (point.timestamp)}
                  {@const maxIssues = Math.max(...health5mData.map(d => d.issueCount ?? 0), 1)}
                  {@const heightPct = (point.issueCount ?? 0) / maxIssues * 100}
                  <div
                    class="flex-1 transition-all {point.issueCount ? 'bg-destructive' : 'bg-success/30'}"
                    style="height: {Math.max(heightPct, 4)}%"
                    title="{new Date(point.timestamp).toLocaleTimeString()}: {point.issueCount ?? 0} issues{point.sampleIssues ? ` (${point.sampleIssues})` : ''}"
                  ></div>
                {/each}
              </div>
            </div>

            <!-- Avg Bitrate Trend -->
            <div>
              <div class="flex items-center justify-between mb-2">
                <span class="text-xs text-muted-foreground uppercase tracking-wide">Avg Bitrate</span>
                <span class="text-xs text-info font-mono">{health5mSummary ? (health5mSummary.avgBitrate / 1000).toFixed(1) : 0} kbps avg</span>
              </div>
              <div class="flex items-end gap-px h-12">
                {#each health5mData as point (point.timestamp)}
                  {@const maxBitrate = Math.max(...health5mData.map(d => d.avgBitrate ?? 0), 1)}
                  {@const heightPct = (point.avgBitrate ?? 0) / maxBitrate * 100}
                  <div
                    class="flex-1 bg-info/60 transition-all"
                    style="height: {Math.max(heightPct, 4)}%"
                    title="{new Date(point.timestamp).toLocaleTimeString()}: {((point.avgBitrate ?? 0) / 1000).toFixed(1)} kbps"
                  ></div>
                {/each}
              </div>
            </div>
          </div>

          <!-- Time Range Labels -->
          <div class="flex justify-between text-[10px] text-muted-foreground mt-2 pt-2 border-t border-border/30">
            <span>{health5mData[0] ? new Date(health5mData[0].timestamp).toLocaleTimeString() : ''}</span>
            <span>{health5mData.length > 0 ? new Date(health5mData[health5mData.length - 1].timestamp).toLocaleTimeString() : ''}</span>
          </div>
        </div>
      </div>
      {/if}
    {/if}
  </div>
</div>

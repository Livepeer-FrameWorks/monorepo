<script lang="ts">
  import { onMount, onDestroy, untrack } from "svelte";
  import { page } from "$app/state";
  import { goto } from "$app/navigation";
  import { resolve } from "$app/paths";
  import {
    GetStreamStore,
    GetCurrentStreamHealthStore,
    GetStreamHealthMetricsStore,
    GetTrackListEventsStore,
    GetRebufferingEventsStore,
    TrackListUpdatesStore,
    GetClientMetrics5mStore,
    GetStreamAnalyticsStore,
  } from "$houdini";
  import type { TrackListUpdates$result } from "$houdini";
  import { toast } from "$lib/stores/toast.js";
  import BufferStateIndicator from "$lib/components/health/BufferStateIndicator.svelte";
  import HealthTrendChart from "$lib/components/charts/HealthTrendChart.svelte";
  import BufferHealthHistogram from "$lib/components/charts/BufferHealthHistogram.svelte";
  import LoadingCard from "$lib/components/LoadingCard.svelte";
  import { getIconComponent } from "$lib/iconUtils";
  import { GridSeam } from "$lib/components/layout";
  import { Button } from "$lib/components/ui/button";

  // Houdini stores
  const streamStore = new GetStreamStore();
  const currentHealthStore = new GetCurrentStreamHealthStore();
  const healthMetricsStore = new GetStreamHealthMetricsStore();
  const trackListEventsStore = new GetTrackListEventsStore();
  const rebufferingEventsStore = new GetRebufferingEventsStore();
  const trackListSub = new TrackListUpdatesStore();
  const clientMetricsStore = new GetClientMetrics5mStore();
  const analyticsStore = new GetStreamAnalyticsStore();

  // Types from Houdini
  type StreamType = NonNullable<NonNullable<typeof $streamStore.data>["stream"]>;
  type HealthMetricType = NonNullable<NonNullable<typeof $currentHealthStore.data>["currentStreamHealth"]>;
  type TrackListEventType = NonNullable<NonNullable<NonNullable<typeof $trackListEventsStore.data>["trackListEventsConnection"]>["edges"]>[0]["node"];
  type RebufferingEventType = NonNullable<NonNullable<NonNullable<typeof $rebufferingEventsStore.data>["rebufferingEvents"]>[0]>;
  type ClientMetric5mType = NonNullable<NonNullable<NonNullable<typeof $clientMetricsStore.data>["clientMetrics5mConnection"]>["edges"]>[0]["node"];

  // page is a store; derive the current param value so it updates on navigation
  let streamId = $derived(page?.params?.id as string ?? "");

  // Derived state from Houdini stores
  let stream = $derived($streamStore.data?.stream ?? null);
  let currentHealth = $derived($currentHealthStore.data?.currentStreamHealth ?? null);
  let streamAnalytics = $derived($analyticsStore.data?.streamAnalytics ?? null);
  let healthMetrics = $derived(
    $healthMetricsStore.data?.streamHealthMetricsConnection?.edges
      ?.map(e => e?.node)
      .filter((n): n is HealthMetricType => n !== null && n !== undefined) ?? []
  );
  
  // Extract buffer health values for histogram (convert 0-1 ratio to 0-100 percentage)
  let bufferHealthValues = $derived(
    healthMetrics
      .map(m => m.bufferHealth)
      .filter(val => val !== null && val !== undefined)
      .map(val => (val as number) * 100)
  );

  // Client metrics (viewer/connection quality)
  let clientMetrics = $derived(
    $clientMetricsStore.data?.clientMetrics5mConnection?.edges
      ?.map(e => e?.node)
      .filter((n): n is ClientMetric5mType => n !== null && n !== undefined) ?? []
  );

  // Computed client quality stats
  let clientQualityStats = $derived(() => {
    if (clientMetrics.length === 0) return null;

    const validPacketLoss = clientMetrics.filter(m => m.packetLossRate !== null && m.packetLossRate !== undefined);
    const validQuality = clientMetrics.filter(m => m.avgConnectionQuality !== null && m.avgConnectionQuality !== undefined);
    const validSessions = clientMetrics.filter(m => m.activeSessions !== null && m.activeSessions !== undefined);

    return {
      avgPacketLoss: validPacketLoss.length > 0
        ? validPacketLoss.reduce((sum, m) => sum + (m.packetLossRate ?? 0), 0) / validPacketLoss.length
        : null,
      peakPacketLoss: validPacketLoss.length > 0
        ? Math.max(...validPacketLoss.map(m => m.packetLossRate ?? 0))
        : null,
      avgConnectionQuality: validQuality.length > 0
        ? validQuality.reduce((sum, m) => sum + (m.avgConnectionQuality ?? 0), 0) / validQuality.length
        : null,
      totalSessions: validSessions.length > 0
        ? validSessions.reduce((sum, m) => sum + (m.activeSessions ?? 0), 0)
        : 0,
      currentSessions: validSessions.length > 0 ? (validSessions[0]?.activeSessions ?? 0) : 0,
    };
  });

  // Per-node breakdown from client metrics
  let nodeBreakdown = $derived(() => {
    const nodeMap = new Map<string, { sessions: number; packetLoss: number[]; quality: number[] }>();

    for (const metric of clientMetrics) {
      const nodeId = metric.nodeId ?? 'unknown';
      if (!nodeMap.has(nodeId)) {
        nodeMap.set(nodeId, { sessions: 0, packetLoss: [], quality: [] });
      }
      const node = nodeMap.get(nodeId)!;
      node.sessions += metric.activeSessions ?? 0;
      if (metric.packetLossRate !== null && metric.packetLossRate !== undefined) {
        node.packetLoss.push(metric.packetLossRate);
      }
      if (metric.avgConnectionQuality !== null && metric.avgConnectionQuality !== undefined) {
        node.quality.push(metric.avgConnectionQuality);
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
  let rebufferingEvents = $derived($rebufferingEventsStore.data?.rebufferingEvents ?? []);
  let loading = $derived($streamStore.fetching || $currentHealthStore.fetching);
  let error = $state<string | null>(null);

  // Pagination state
  let healthMetricsDisplayCount = $state(10);
  let trackListDisplayCount = $state(10);
  let hasMoreHealthMetrics = $derived(healthMetrics.length > healthMetricsDisplayCount);
  let hasMoreTrackListEvents = $derived(trackListEvents.length > trackListDisplayCount);
  let loadingMoreHealthMetrics = $state(false);
  let loadingMoreTrackListEvents = $state(false);

  // Check if there are more pages to load from server
  let healthMetricsHasNextPage = $derived(
    $healthMetricsStore.data?.streamHealthMetricsConnection?.pageInfo?.hasNextPage ?? false
  );
  let trackListEventsHasNextPage = $derived(
    $trackListEventsStore.data?.trackListEventsConnection?.pageInfo?.hasNextPage ?? false
  );

  // Time range for historical data (last 24 hours)
  const getTimeRange = () => ({
    start: new Date(Date.now() - 24 * 60 * 60 * 1000).toISOString(),
    end: new Date().toISOString(),
  });

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
    const update = $trackListSub.data?.trackListUpdates;
    if (update) {
      untrack(() => handleTrackListUpdate(update));
    }
  });

  // Effect to sync track list events from store
  // IMPORTANT: The length check must be INSIDE untrack() to avoid creating a reactive dependency
  // on trackListEvents, which would cause an effect loop
  $effect(() => {
    const edges = $trackListEventsStore.data?.trackListEventsConnection?.edges;
    if (edges) {
      untrack(() => {
        // Only sync if we haven't populated yet (check inside untrack to avoid dependency)
        if (trackListEvents.length === 0) {
          trackListEvents = edges
            .map(e => e?.node)
            .filter((n): n is TrackListEventType => n !== null && n !== undefined);
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
        // Use stream.id (internal UUID) for analytics queries - this is the canonical identifier
        const streamUUID = $streamStore.data?.stream?.id;
        if (streamUUID) {
          await currentHealthStore.fetch({ variables: { stream: streamUUID } });
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
    const streamData = $streamStore.data?.stream;
    if (!streamData) return;
    // Use stream.id (internal UUID) for subscriptions - this is the canonical identifier
    trackListSub.listen({ stream: streamData.id });
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
      const result = await streamStore.fetch({ variables: { id: streamId } });

      if (!result.data?.stream) {
        error = "Stream not found";
        return;
      }
    } catch (err: any) {
      // Ignore AbortErrors which happen on navigation/cancellation
      if (err.name === 'AbortError' || err.message === 'aborted' || err.message === 'Aborted') {
        return;
      }
      console.error("Failed to load stream:", err);
      error = "Failed to load stream data";
    }
  }

  async function loadHealthData() {
    if (!streamId) return;
    try {
      const streamIdParam = $streamStore.data?.stream?.id;
      if (!streamIdParam) return;

      // Load all health data in parallel
      await Promise.all([
        currentHealthStore.fetch({ variables: { stream: streamIdParam } }),
        healthMetricsStore.fetch({
          variables: { stream: streamIdParam, first: 100, timeRange: getTimeRange(), noCache: true },
        }),
        trackListEventsStore.fetch({
          variables: { stream: streamIdParam, first: 100, timeRange: getTimeRange(), noCache: true },
        }),
        rebufferingEventsStore.fetch({ variables: { stream: streamIdParam, timeRange: getTimeRange() } }),
        clientMetricsStore.fetch({
          variables: { stream: streamIdParam, first: 288, timeRange: getTimeRange() }, // 288 = 24h at 5min intervals
        }),
        analyticsStore.fetch({
          variables: { stream: streamIdParam, timeRange: getTimeRange() },
        }),
      ]);
    } catch (err: any) {
      // Ignore AbortErrors which happen on navigation/cancellation
      if (err.name === 'AbortError' || err.message === 'aborted' || err.message === 'Aborted') {
        return;
      }
      console.error("Failed to load health data:", err);
      error = "Failed to load health monitoring data";
    }
  }

  function handleTrackListUpdate(event: NonNullable<TrackListUpdates$result["trackListUpdates"]>) {
    // Add the new track list event to the list
    const newEvent: TrackListEventType = {
      timestamp: new Date().toISOString(),
      stream: event.streamName ?? "",
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
    // First, show more from already loaded data
    if (healthMetrics.length > healthMetricsDisplayCount) {
      healthMetricsDisplayCount = Math.min(healthMetricsDisplayCount + 10, healthMetrics.length);
      return;
    }
    // If we've shown all loaded data and there's more on server, fetch next page
    if (healthMetricsHasNextPage) {
      loadingMoreHealthMetrics = true;
      try {
        await healthMetricsStore.loadNextPage();
        healthMetricsDisplayCount += 10;
      } catch (err) {
        console.error("Failed to load more health metrics:", err);
      } finally {
        loadingMoreHealthMetrics = false;
      }
    }
  }

  async function loadMoreTrackListEvents() {
    // First, show more from already loaded data
    if (trackListEvents.length > trackListDisplayCount) {
      trackListDisplayCount = Math.min(trackListDisplayCount + 10, trackListEvents.length);
      return;
    }
    // If we've shown all loaded data and there's more on server, fetch next page
    if (trackListEventsHasNextPage) {
      loadingMoreTrackListEvents = true;
      try {
        await trackListEventsStore.loadNextPage();
        // After loading more, sync from store
        const edges = $trackListEventsStore.data?.trackListEventsConnection?.edges;
        if (edges) {
          trackListEvents = edges
            .map(e => e?.node)
            .filter((n): n is TrackListEventType => n !== null && n !== undefined);
        }
        trackListDisplayCount += 10;
      } catch (err) {
        console.error("Failed to load more track list events:", err);
      } finally {
        loadingMoreTrackListEvents = false;
      }
    }
  }

  const ArrowLeftIcon = getIconComponent("ArrowLeft");
  const AlertTriangleIcon = getIconComponent("AlertTriangle");
  const PauseIcon = getIconComponent("Pause");
</script>

<svelte:head>
  <title>Stream Health - {stream?.name || 'Loading...'} - FrameWorks</title>
</svelte:head>

<div class="h-full flex flex-col">
  <!-- Fixed Page Header -->
  <div class="px-4 sm:px-6 lg:px-8 py-4 border-b border-[hsl(var(--tn-fg-gutter)/0.3)] shrink-0">
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
          {#if stream}{stream.name} • {/if}Last 24 hours
        </p>
      </div>
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
                    {currentHealth.bitrate ? `${(currentHealth.bitrate / 1000000).toFixed(2)} Mbps` : 'N/A'}
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
                  <p class="font-mono text-lg text-primary">{(currentHealth.bitrate / 1000000).toFixed(2)} Mbps</p>
                </div>
              {/if}
            </GridSeam>
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

        <!-- Audio Details -->
        {#if currentHealth.audioCodec || currentHealth.audioChannels || currentHealth.audioSampleRate}
          <div class="slab border-t-0">
            <div class="slab-header">
              <h3>Audio Details</h3>
            </div>
            <GridSeam cols={4} stack="2x2" surface="panel" flush={true}>
              {#if currentHealth.audioCodec}
                <div class="p-4">
                  <p class="text-xs text-muted-foreground uppercase tracking-wide mb-1">Audio Codec</p>
                  <p class="font-mono text-lg text-accent-purple">{currentHealth.audioCodec}</p>
                </div>
              {/if}
              {#if currentHealth.audioChannels}
                <div class="p-4">
                  <p class="text-xs text-muted-foreground uppercase tracking-wide mb-1">Channels</p>
                  <p class="font-mono text-lg text-info">{currentHealth.audioChannels}</p>
                </div>
              {/if}
              {#if currentHealth.audioSampleRate}
                <div class="p-4">
                  <p class="text-xs text-muted-foreground uppercase tracking-wide mb-1">Sample Rate</p>
                  <p class="font-mono text-lg text-primary">{(currentHealth.audioSampleRate / 1000).toFixed(1)} kHz</p>
                </div>
              {/if}
              {#if currentHealth.audioBitrate}
                <div class="p-4">
                  <p class="text-xs text-muted-foreground uppercase tracking-wide mb-1">Audio Bitrate</p>
                  <p class="font-mono text-lg text-warning-alt">{currentHealth.audioBitrate} kbps</p>
                </div>
              {/if}
            </GridSeam>
          </div>
        {/if}
      {/if}

      <!-- Ingest Network Stats (from live_streams) -->
      {#if streamAnalytics && (streamAnalytics.packetsSent || streamAnalytics.packetsLost)}
        <div class="slab">
          <div class="slab-header">
            <h2>Ingest Network Stats</h2>
            <span class="text-xs text-muted-foreground font-normal normal-case ml-2">
              Stream-level packet statistics
            </span>
          </div>
          <GridSeam cols={4} stack="2x2" surface="panel" flush={true}>
            <div class="p-4">
              <p class="text-xs text-muted-foreground uppercase tracking-wide mb-1">Packets Sent</p>
              <p class="font-mono text-lg text-info">
                {streamAnalytics.packetsSent?.toLocaleString() ?? 'N/A'}
              </p>
            </div>
            <div class="p-4">
              <p class="text-xs text-muted-foreground uppercase tracking-wide mb-1">Packets Lost</p>
              <p class="font-mono text-lg {(streamAnalytics.packetsLost ?? 0) > 0 ? 'text-warning' : 'text-success'}">
                {streamAnalytics.packetsLost?.toLocaleString() ?? '0'}
              </p>
            </div>
            <div class="p-4">
              <p class="text-xs text-muted-foreground uppercase tracking-wide mb-1">Packets Retransmitted</p>
              <p class="font-mono text-lg text-muted-foreground">
                {streamAnalytics.packetsRetrans?.toLocaleString() ?? '0'}
              </p>
            </div>
            <div class="p-4">
              <p class="text-xs text-muted-foreground uppercase tracking-wide mb-1">Packet Loss Rate</p>
              <p class="font-mono text-lg {(streamAnalytics.packetLossRate ?? 0) > 0.01 ? 'text-warning' : 'text-success'}">
                {streamAnalytics.packetLossRate !== null && streamAnalytics.packetLossRate !== undefined
                  ? `${(streamAnalytics.packetLossRate * 100).toFixed(3)}%`
                  : 'N/A'}
              </p>
            </div>
          </GridSeam>
        </div>
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
              <p class="font-mono text-lg {(stats?.avgPacketLoss ?? 0) > 0.01 ? 'text-warning' : 'text-success'}">
                {stats?.avgPacketLoss != null ? `${(stats.avgPacketLoss * 100).toFixed(3)}%` : 'N/A'}
              </p>
            </div>
            <div class="p-4">
              <p class="text-xs text-muted-foreground uppercase tracking-wide mb-1">Peak Packet Loss</p>
              <p class="font-mono text-lg {(stats?.peakPacketLoss ?? 0) > 0.05 ? 'text-destructive' : (stats?.peakPacketLoss ?? 0) > 0.01 ? 'text-warning' : 'text-success'}">
                {stats?.peakPacketLoss != null ? `${(stats.peakPacketLoss * 100).toFixed(3)}%` : 'N/A'}
              </p>
            </div>
            <div class="p-4">
              <p class="text-xs text-muted-foreground uppercase tracking-wide mb-1">Avg Connection Quality</p>
              <p class="font-mono text-lg {(stats?.avgConnectionQuality ?? 1) < 0.95 ? 'text-warning' : 'text-success'}">
                {stats?.avgConnectionQuality != null ? `${(stats.avgConnectionQuality * 100).toFixed(1)}%` : 'N/A'}
              </p>
            </div>
            <div class="p-4">
              <p class="text-xs text-muted-foreground uppercase tracking-wide mb-1">Current Sessions</p>
              <p class="font-mono text-lg text-info">{stats?.currentSessions ?? 0}</p>
            </div>
          </GridSeam>

          <!-- Packet Loss Trend Chart -->
          {#if clientMetrics.length > 1}
            <div class="slab-body--padded border-t border-border/30">
              <h4 class="text-sm font-medium text-muted-foreground mb-3">Packet Loss Over Time</h4>
              <HealthTrendChart
                data={clientMetrics.map(m => ({
                  timestamp: m.timestamp,
                  bufferHealth: m.avgConnectionQuality,
                  bitrate: m.packetLossRate !== null ? m.packetLossRate * 100000000 : null,
                }))}
                height={200}
                showBufferHealth={true}
                showBitrate={true}
              />
              <p class="text-xs text-muted-foreground mt-2">
                Blue: Connection Quality (%) • Orange: Packet Loss (scaled)
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
                        <p class="font-mono text-sm {(node.avgPacketLoss ?? 0) > 0.01 ? 'text-warning' : 'text-success'}">
                          {node.avgPacketLoss !== null ? `${(node.avgPacketLoss * 100).toFixed(3)}%` : 'N/A'}
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
              {#each healthMetrics.slice(0, healthMetricsDisplayCount) as metric (metric.timestamp)}
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
                        {metric.bitrate ? `${(metric.bitrate / 1000000).toFixed(2)} Mbps` : 'N/A'}
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
              <p class="text-muted-foreground py-8">No health data in the last 24 hours</p>
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
                      {#each tracks as track}
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

      <!-- Rebuffering Events -->
      {#if rebufferingEvents.length > 0}
        <div class="slab">
          <div class="slab-header">
            <h3>Rebuffering Events</h3>
          </div>
          <div class="slab-body--flush max-h-64 overflow-y-auto">
            {#each rebufferingEvents.slice(0, 10) as event (event.timestamp)}
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
    {/if}
  </div>
</div>

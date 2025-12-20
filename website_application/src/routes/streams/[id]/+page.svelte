<script lang="ts">
  import { onMount, onDestroy, untrack } from "svelte";
  import { page } from "$app/state";
  import { goto } from "$app/navigation";
  import { resolve } from "$app/paths";
  import {
    fragment,
    GetStreamStore,
    GetStreamKeysStore,
    GetDVRRequestsStore,
    GetClipsConnectionStore,
    GetStreamOverviewStore,
    GetStreamAnalyticsDailyConnectionStore,
    UpdateStreamStore,
    DeleteStreamStore,
    RefreshStreamKeyStore,
    CreateStreamKeyStore,
    DeleteStreamKeyStore,
    StreamEventsStore,
    ViewerMetricsStreamStore,
    TrackListUpdatesStore,
    ClipLifecycleStore,
    DvrLifecycleStore,
    StreamCoreFieldsStore,
    StreamMetricsFieldsStore,
    StreamAnalyticsFieldsStore,
    StreamHealthFieldsStore,
  } from "$houdini";
  import type {
    StreamEvents$result,
    ViewerMetricsStream$result,
    TrackListUpdates$result,
    ClipLifecycle$result,
    DvrLifecycle$result,
  } from "$houdini";
  import { toast } from "$lib/stores/toast.js";
  import { streamMetrics as realtimeStreamMetrics } from "$lib/stores/realtime";
  import LoadingCard from "$lib/components/LoadingCard.svelte";
  import { getIconComponent } from "$lib/iconUtils";
  import { Button } from "$lib/components/ui/button";
  import {
    Tabs,
    TabsContent,
    TabsList,
    TabsTrigger,
  } from "$lib/components/ui/tabs";
  import {
    StreamEditModal,
    StreamDeleteModal,
    StreamCreateKeyModal,
    StreamStatusCard,
    StreamKeyCard,
    StreamPlaybackCard,
    OverviewTabPanel,
    RecordingsTabPanel,
    PlaybackTabPanel,
    HealthSidebar,
    EventLog,
    StreamSetupPanel,
  } from "$lib/components/stream-details";
  import { SectionDivider } from "$lib/components/layout";
  import type { StreamEvent, EventType } from "$lib/components/stream-details/EventLog.svelte";

  // Houdini stores
  const streamStore = new GetStreamStore();
  const streamKeysStore = new GetStreamKeysStore();
  const dvrRequestsStore = new GetDVRRequestsStore();
  const clipsStore = new GetClipsConnectionStore();
  const streamOverviewStore = new GetStreamOverviewStore();
  const updateStreamMutation = new UpdateStreamStore();
  const deleteStreamMutation = new DeleteStreamStore();
  const refreshStreamKeyMutation = new RefreshStreamKeyStore();
  const createStreamKeyMutation = new CreateStreamKeyStore();
  const deleteStreamKeyMutation = new DeleteStreamKeyStore();
  const streamEventsSub = new StreamEventsStore();
  const viewerMetricsSub = new ViewerMetricsStreamStore();
  const trackListSub = new TrackListUpdatesStore();
  const clipLifecycleSub = new ClipLifecycleStore();
  const dvrLifecycleSub = new DvrLifecycleStore();
  const streamDailyStore = new GetStreamAnalyticsDailyConnectionStore();

  // Fragment stores for unmasking nested data
  const streamCoreStore = new StreamCoreFieldsStore();
  const streamMetricsStore = new StreamMetricsFieldsStore();
  const streamAnalyticsStore = new StreamAnalyticsFieldsStore();
  const streamHealthStore = new StreamHealthFieldsStore();

  // Types from Houdini
  type StreamType = NonNullable<NonNullable<typeof $streamStore.data>["stream"]>;
  type StreamKeyType = NonNullable<NonNullable<NonNullable<NonNullable<typeof $streamKeysStore.data>["streamKeysConnection"]>["edges"]>[0]>["node"];
  type RecordingType = NonNullable<NonNullable<NonNullable<typeof $dvrRequestsStore.data>["dvrRecordingsConnection"]>["edges"]>[0]["node"];
  type TrackInfo = NonNullable<TrackListUpdates$result["trackListUpdates"]>;
  type HealthData = NonNullable<NonNullable<NonNullable<typeof $streamOverviewStore.data>["stream"]>["currentHealth"]>;

  // page is a store; derive the param so it stays in sync with navigation
  let streamId = $derived(page.params.id as string);

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

  // Unmask StreamAnalyticsFields from GetStream query
  let streamAnalyticsStoreResult = $derived(
    maskedStream?.analytics ? fragment(maskedStream.analytics, streamAnalyticsStore) : null
  );
  let streamAnalytics = $derived(streamAnalyticsStoreResult ? $streamAnalyticsStoreResult : null);

  // Unmask StreamHealthFields from GetStream query
  let streamHealthStoreResult = $derived(
    maskedStream?.currentHealth ? fragment(maskedStream.currentHealth, streamHealthStore) : null
  );
  let streamHealth = $derived(streamHealthStoreResult ? $streamHealthStoreResult : null);

  // Combine unmasked data into stream object
  let stream = $derived(
    streamCore
      ? {
          ...streamCore,
          metrics: streamMetrics,
          analytics: streamAnalytics,
          currentHealth: streamHealth,
        }
      : null
  );

  // Derived state from Houdini stores
  // Map to create mutable objects (Houdini returns readonly types)
  let streamKeys = $derived(
    ($streamKeysStore.data?.streamKeysConnection?.edges?.map(e => ({
      id: e.node.id,
      streamId: e.node.streamId,
      keyValue: e.node.keyValue,
      keyName: e.node.keyName ?? '',
      isActive: e.node.isActive,
      createdAt: e.node.createdAt,
      lastUsedAt: e.node.lastUsedAt ?? undefined
    })) ?? [])
  );
  let recordings = $derived($dvrRequestsStore.data?.dvrRecordingsConnection?.edges?.map(e => e.node) ?? []);
  let clips = $derived($clipsStore.data?.clipsConnection?.edges?.map(e => e.node) ?? []);

  // Analytics and health from GetStreamDetail query (also needs unmasking)
  let maskedDetailStream = $derived($streamOverviewStore.data?.stream ?? null);
  let detailAnalyticsStoreResult = $derived(
    maskedDetailStream?.analytics ? fragment(maskedDetailStream.analytics, streamAnalyticsStore) : null
  );
  let detailHealthStoreResult = $derived(
    maskedDetailStream?.currentHealth ? fragment(maskedDetailStream.currentHealth, streamHealthStore) : null
  );
  let analytics = $derived(detailAnalyticsStoreResult ? $detailAnalyticsStoreResult : streamAnalytics);
  let baseHealth = $derived(detailHealthStoreResult ? $detailHealthStoreResult : streamHealth);
  // Merge base health (from GraphQL query) with real-time metrics (from subscription)
  let health = $derived.by(() => {
    const realtime = stream?.id ? $realtimeStreamMetrics[stream.id] : null;
    if (!baseHealth && !realtime) return null;
    return {
      ...baseHealth,
      // Real-time buffer/jitter data from STREAM_BUFFER subscription (overrides if present)
      bufferState: realtime?.bufferState ?? baseHealth?.bufferState,
      streamBufferMs: realtime?.streamBufferMs,
      streamJitterMs: realtime?.streamJitterMs,
      maxKeepawaMs: realtime?.maxKeepawaMs,
      hasIssues: realtime?.hasIssues,
      issuesDescription: realtime?.issuesDescription ?? baseHealth?.issuesDescription,
      mistIssues: realtime?.mistIssues,
      trackCount: realtime?.trackCount,
      qualityTier: realtime?.qualityTier ?? baseHealth?.qualityTier,
    };
  });
  let viewerMetrics = $derived($streamOverviewStore.data?.stream?.viewerTimeSeriesConnection?.edges?.map(e => e.node) ?? []);

  // Stream daily analytics history
  let streamDailyAnalytics = $derived.by(() => {
    const edges = $streamDailyStore.data?.streamAnalyticsDailyConnection?.edges ?? [];
    if (edges.length === 0) return [];
    return edges.map(edge => ({
      day: edge.node.day,
      internalName: edge.node.internalName,
      totalViews: edge.node.totalViews,
      uniqueViewers: edge.node.uniqueViewers,
      uniqueCountries: edge.node.uniqueCountries,
      uniqueCities: edge.node.uniqueCities,
      egressBytes: edge.node.egressBytes,
      egressGb: edge.node.egressBytes / (1024 * 1024 * 1024),
    })).sort((a, b) => new Date(a.day).getTime() - new Date(b.day).getTime());
  });

  let loading = $derived($streamStore.fetching || $streamKeysStore.fetching);
  let error = $state<string | null>(null);
  let showEditModal = $state(false);
  let showDeleteModal = $state(false);
  let showCreateKeyModal = $state(false);
  let actionLoading = $state({
    refreshKey: false,
    deleteStream: false,
    editStream: false,
    createKey: false,
    deleteKey: null as string | null,
  });

  // Health sidebar state
  let healthSidebarCollapsed = $state(false);

  // Event log state
  let eventLogCollapsed = $state(true);
  let streamEvents = $state<StreamEvent[]>([]);

  // Auto-refresh interval for live data (fallback)
  let refreshInterval: ReturnType<typeof setInterval> | null = null;
  let healthRefreshInterval: ReturnType<typeof setInterval> | null = null;

  // Real-time metrics from subscription
  let realtimeViewers = $state(0);

  // Current track info from subscription
  let currentTracks = $state<TrackInfo | null>(null);

  // Derived: is stream live?
  let isLive = $derived(stream?.metrics?.isLive ?? false);

  // Fallback track info from StreamMetricsFields when subscription hasn't fired yet
  // This uses the primary track data that's already fetched with the stream query
  // Type assertion needed because we're creating a partial match for display purposes
  let fallbackTracks = $derived.by((): TrackInfo | null => {
    if (!streamMetrics?.isLive || !streamMetrics?.primaryCodec) return null;
    // Create a fallback that satisfies the TrackInfo type from the subscription
    // OverviewTabPanel only uses a subset of these fields for display
    return {
      streamName: stream?.name ?? '',
      totalTracks: 1,
      videoTrackCount: 1,
      audioTrackCount: 0,
      qualityTier: streamMetrics.qualityTier ?? null,
      primaryWidth: streamMetrics.primaryWidth ?? null,
      primaryHeight: streamMetrics.primaryHeight ?? null,
      primaryFps: streamMetrics.primaryFps ?? null,
      primaryVideoBitrate: streamMetrics.primaryBitrate ?? null,
      primaryVideoCodec: streamMetrics.primaryCodec ?? null,
      primaryAudioBitrate: null,
      primaryAudioCodec: null,
      primaryAudioChannels: null,
      primaryAudioSampleRate: null,
      tracks: [{
        trackName: 'video0',
        trackType: 'video',
        codec: streamMetrics.primaryCodec ?? null,
        width: streamMetrics.primaryWidth ?? null,
        height: streamMetrics.primaryHeight ?? null,
        fps: streamMetrics.primaryFps ?? null,
        bitrateKbps: streamMetrics.primaryBitrate ? Math.round(streamMetrics.primaryBitrate / 1000) : null,
        bitrateBps: streamMetrics.primaryBitrate ?? null,
        buffer: null,
        jitter: null,
        resolution: streamMetrics.primaryWidth && streamMetrics.primaryHeight
          ? `${streamMetrics.primaryWidth}x${streamMetrics.primaryHeight}`
          : null,
        hasBFrames: null,
        channels: null,
        sampleRate: null,
      }]
    };
  });

  // Effect to handle subscription errors
  $effect(() => {
    if ($streamEventsSub.errors?.length) {
      console.warn("Stream events subscription error:", $streamEventsSub.errors);
    }
    if ($viewerMetricsSub.errors?.length) {
      console.warn("Viewer metrics subscription error:", $viewerMetricsSub.errors);
    }
    if ($trackListSub.errors?.length) {
      console.warn("Track list subscription error:", $trackListSub.errors);
    }
    if ($clipLifecycleSub.errors?.length) {
      console.warn("Clip lifecycle subscription error:", $clipLifecycleSub.errors);
    }
    if ($dvrLifecycleSub.errors?.length) {
      console.warn("DVR lifecycle subscription error:", $dvrLifecycleSub.errors);
    }
  });

  // Effect to handle stream events subscription
  // Use untrack to prevent effect loops when mutating state
  $effect(() => {
    const event = $streamEventsSub.data?.streamEvents;
    if (event) {
      untrack(() => handleStreamEvent(event));
    }
  });

  // Effect to handle viewer metrics subscription
  $effect(() => {
    const metrics = $viewerMetricsSub.data?.viewerMetrics;
    if (metrics) {
      untrack(() => handleViewerMetrics(metrics));
    }
  });

  // Effect to handle track list subscription
  $effect(() => {
    const tracks = $trackListSub.data?.trackListUpdates;
    if (tracks) {
      untrack(() => {
        currentTracks = tracks;
        addEvent("track_change", `Track list updated: ${tracks.totalTracks} track(s)`);
      });
    }
  });

  $effect(() => {
    const event = $clipLifecycleSub.data?.clipLifecycle;
    if (event) {
      untrack(() => handleClipLifecycleEvent(event));
    }
  });

  $effect(() => {
    const event = $dvrLifecycleSub.data?.dvrLifecycle;
    if (event) {
      untrack(() => handleDvrLifecycleEvent(event));
    }
  });

  // Auto-expand health sidebar when stream goes live
  $effect(() => {
    if (isLive) {
      untrack(() => {
        // Check healthSidebarCollapsed INSIDE untrack to avoid reactive dependency
        if (healthSidebarCollapsed) {
          healthSidebarCollapsed = false;
        }
      });
    }
  });

  onMount(async () => {
    await loadStreamData();

    // Set up auto-refresh every 60 seconds as fallback
    refreshInterval = setInterval(loadLiveData, 60000);

    // Refresh health every 30 seconds when live (via streamOverviewStore)
    healthRefreshInterval = setInterval(async () => {
      if (isLive && stream?.id) {
        const timeRange = {
          start: new Date(Date.now() - 24 * 60 * 60 * 1000).toISOString(),
          end: new Date().toISOString()
        };
        await streamOverviewStore.fetch({ variables: { id: stream.id, timeRange, interval: "5m" } });
      }
    }, 30000);
  });

  onDestroy(() => {
    if (refreshInterval) clearInterval(refreshInterval);
    if (healthRefreshInterval) clearInterval(healthRefreshInterval);
    streamEventsSub.unlisten();
    viewerMetricsSub.unlisten();
    trackListSub.unlisten();
    clipLifecycleSub.unlisten();
    dvrLifecycleSub.unlisten();
  });

  function startSubscriptions() {
    if (!stream) return;

    // Use stream.id (internal UUID) for subscriptions - this is the canonical identifier
    streamEventsSub.listen({ stream: stream.id });
    viewerMetricsSub.listen({ stream: stream.id });
    trackListSub.listen({ stream: stream.id });
    clipLifecycleSub.listen({ stream: stream.id });
    dvrLifecycleSub.listen({ stream: stream.id });
  }

  function addEvent(type: EventType, message: string, details?: string) {
    // Use untrack to prevent reading streamEvents from creating a reactive dependency
    // when this function is called from within $effect blocks
    untrack(() => {
      const event: StreamEvent = {
        id: `${Date.now()}-${Math.random().toString(36).slice(2)}`,
        timestamp: new Date().toISOString(),
        type,
        message,
        details,
        streamName: stream?.name,
      };
      streamEvents = [event, ...streamEvents].slice(0, 100);
    });
  }

  // Clip Lifecycle Stages (mapped from proto/ipc.proto)
  const ClipLifecycleStage = {
    REQUESTED: 1,
    QUEUED: 2,
    PROGRESS: 3,
    DONE: 4,
    FAILED: 5,
    DELETED: 6,
  };

  function handleStreamEvent(event: NonNullable<StreamEvents$result["streamEvents"]>) {
    // Handle lifecycle update (stream going live/offline with rich data)
    if (event.lifecycleUpdate) {
      const isLive = event.lifecycleUpdate.status === "live";
      if (isLive) {
        toast.success("Stream is now live!");
        addEvent("stream_start", "Stream started", `Viewers: ${event.lifecycleUpdate.totalViewers ?? 0}`);
        // Expand event log on stream start
        eventLogCollapsed = false;
      } else {
        toast.info("Stream went offline");
        addEvent("stream_end", "Stream ended");
      }

      // Log buffer state if concerning
      if (event.lifecycleUpdate.bufferState === "DRY" || event.lifecycleUpdate.bufferState === "EMPTY") {
        addEvent("warning", "Buffer issue", `Buffer state: ${event.lifecycleUpdate.bufferState}`);
      }
    }

    // Handle stream end event (final stats)
    if (event.endEvent) {
      toast.info("Stream ended");
      const stats = [
        event.endEvent.totalViewers ? `Viewers: ${event.endEvent.totalViewers}` : null,
        event.endEvent.viewerSeconds ? `Watch time: ${Math.round(event.endEvent.viewerSeconds / 60)}min` : null,
      ].filter(Boolean).join(", ");
      addEvent("stream_end", "Stream ended", stats || undefined);
    }
  }

  function handleViewerMetrics(metrics: NonNullable<ViewerMetricsStream$result["viewerMetrics"]>) {
    // Note: realtimeViewers mutations are safe here because this function is called
    // from within untrack() in the $effect, and addEvent also uses untrack() internally
    if (metrics.action === "connect") {
      realtimeViewers++;
      const location = [metrics.clientCity, metrics.clientCountry].filter(Boolean).join(", ") || "Unknown";
      addEvent("viewer_connect", "Viewer connected", `Location: ${location}`);
    } else if (metrics.action === "disconnect") {
      realtimeViewers = Math.max(0, realtimeViewers - 1);
      addEvent("viewer_disconnect", "Viewer disconnected");
    }
  }

  function handleClipLifecycleEvent(event: NonNullable<ClipLifecycle$result["clipLifecycle"]>) {
    if (event.stage === ClipLifecycleStage.DONE) {
      if (event.s3Url) {
        addEvent("info", `Clip '${event.clipHash}' uploaded`, `URL: ${event.s3Url}`);
      } else {
        addEvent("info", `Clip '${event.clipHash}' created`, `Path: ${event.filePath}`);
      }
    } else if (event.stage === ClipLifecycleStage.FAILED) {
      addEvent("error", `Clip '${event.clipHash}' failed`, `Error: ${event.error}`);
    } else if (event.stage === ClipLifecycleStage.DELETED) {
      addEvent("info", `Clip '${event.clipHash}' deleted`);
    } else if (event.stage === ClipLifecycleStage.REQUESTED) {
      addEvent("info", `Clip '${event.clipHash}' requested`);
    }
  }

  function handleDvrLifecycleEvent(event: NonNullable<DvrLifecycle$result["dvrLifecycle"]>) {
    if (event.status === "RECORDING") {
      addEvent("dvr_start", `DVR recording started for '${event.internalName}'`);
    } else if (event.status === "COMPLETED") {
      addEvent("dvr_stop", `DVR recording completed for '${event.internalName}'`, `Segments: ${event.segmentCount}`);
    } else if (event.status === "FAILED") {
      addEvent("error", `DVR recording failed for '${event.internalName}'`, `Error: ${event.error}`);
    } else if (event.status === "DELETED") {
      addEvent("info", `DVR recording deleted for '${event.internalName}'`);
    }
  }

  async function loadStreamData() {
    try {
      error = null;

      const result = await streamStore.fetch({ variables: { id: streamId } });

      if (!result.data?.stream) {
        error = "Stream not found";
        return;
      }

      const timeRange = {
        start: new Date(Date.now() - 24 * 60 * 60 * 1000).toISOString(),
        end: new Date().toISOString(),
      };
      // Longer time range for daily analytics (30 days)
      const dailyTimeRange = {
        start: new Date(Date.now() - 30 * 24 * 60 * 60 * 1000).toISOString(),
        end: new Date().toISOString(),
      };
      // Use streamId (route param) which is the same as the internal UUID
      const streamUUID = streamId;

      await Promise.all([
        streamKeysStore.fetch({ variables: { streamId } }),
        dvrRequestsStore.fetch({ variables: { internalName: streamId } }),
        clipsStore.fetch({ variables: { streamId: streamUUID, first: 100 } }),
        streamOverviewStore.fetch({ variables: { id: streamUUID, timeRange, interval: "5m" } }).catch(() => null),
        streamDailyStore.fetch({ variables: { internalName: streamUUID, timeRange: dailyTimeRange, first: 30 } }).catch(() => null),
      ]);

      startSubscriptions();

      // Add initial event
      addEvent("info", "Stream data loaded");
    } catch (err) {
      console.error("Failed to load stream data:", err);
      error = "Failed to load stream data";
    }
  }

  async function loadLiveData() {
    try {
      const timeRange = {
        start: new Date(Date.now() - 24 * 60 * 60 * 1000).toISOString(),
        end: new Date().toISOString(),
      };
      // Use streamId (route param) which is the same as the internal UUID
      const streamUUID = streamId;

      await Promise.all([
        streamStore.fetch({ variables: { id: streamId } }),
        streamUUID ? streamOverviewStore.fetch({ variables: { id: streamUUID, timeRange, interval: "5m" } }).catch(() => null) : Promise.resolve(),
      ]);
    } catch (err) {
      console.error("Failed to refresh live data:", err);
    }
  }

  async function handleRefreshStreamKey() {
    if (!stream) return;

    try {
      actionLoading.refreshKey = true;
      const result = await refreshStreamKeyMutation.mutate({ id: streamId });
      if (result.data?.refreshStreamKey?.__typename === "Stream") {
        toast.success("Stream key refreshed successfully!");
        addEvent("info", "Stream key refreshed");
        await streamStore.fetch({ variables: { id: streamId } });
      } else {
        const errorResult = result.data?.refreshStreamKey as unknown as { message?: string };
        toast.error(errorResult?.message || "Failed to refresh stream key");
      }
    } catch (err) {
      console.error("Failed to refresh stream key:", err);
      toast.error("Failed to refresh stream key");
    } finally {
      actionLoading.refreshKey = false;
    }
  }

  async function handleEditStream(formData: { name?: string; description?: string; record?: boolean }) {
    if (!stream) return;

    try {
      actionLoading.editStream = true;
      const result = await updateStreamMutation.mutate({
        id: streamId,
        input: formData,
      });
      if (result.data?.updateStream?.__typename === "Stream") {
        showEditModal = false;
        toast.success("Stream updated successfully!");
        addEvent("info", "Stream settings updated");
        await streamStore.fetch({ variables: { id: streamId } });
      } else {
        const errorResult = result.data?.updateStream as unknown as { message?: string };
        toast.error(errorResult?.message || "Failed to update stream");
      }
    } catch (err) {
      console.error("Failed to update stream:", err);
      toast.error("Failed to update stream");
    } finally {
      actionLoading.editStream = false;
    }
  }

  async function handleDeleteStream() {
    if (!stream) return;

    try {
      actionLoading.deleteStream = true;
      const result = await deleteStreamMutation.mutate({ id: streamId });
      if (result.data?.deleteStream?.__typename === "DeleteSuccess") {
        goto(resolve("/streams"));
      } else {
        const errorResult = result.data?.deleteStream as unknown as { message?: string };
        toast.error(errorResult?.message || "Failed to delete stream");
        actionLoading.deleteStream = false;
      }
    } catch (err) {
      console.error("Failed to delete stream:", err);
      toast.error("Failed to delete stream");
      actionLoading.deleteStream = false;
    }
  }

  async function handleCreateStreamKey(formData: { keyName: string; isActive: boolean }) {
    try {
      actionLoading.createKey = true;
      const result = await createStreamKeyMutation.mutate({
        streamId,
        input: { name: formData.keyName },
      });
      if (result.data?.createStreamKey?.__typename === "StreamKey") {
        showCreateKeyModal = false;
        toast.success("Stream key created successfully!");
        addEvent("info", `Stream key "${formData.keyName}" created`);
        await streamKeysStore.fetch({ variables: { streamId } });
      } else {
        const errorResult = result.data?.createStreamKey as unknown as { message?: string };
        toast.error(errorResult?.message || "Failed to create stream key");
      }
    } catch (err) {
      console.error("Failed to create stream key:", err);
      toast.error("Failed to create stream key");
    } finally {
      actionLoading.createKey = false;
    }
  }

  async function handleDeleteStreamKey(keyId: string) {
    try {
      actionLoading.deleteKey = keyId;
      const result = await deleteStreamKeyMutation.mutate({ streamId, keyId });
      if (result.data?.deleteStreamKey?.__typename === "DeleteSuccess") {
        toast.success("Stream key deleted successfully!");
        addEvent("info", "Stream key deleted");
        await streamKeysStore.fetch({ variables: { streamId } });
      } else {
        const errorResult = result.data?.deleteStreamKey as unknown as { message?: string };
        toast.error(errorResult?.message || "Failed to delete stream key");
      }
    } catch (err) {
      console.error("Failed to delete stream key:", err);
      toast.error("Failed to delete stream key");
    } finally {
      actionLoading.deleteKey = null;
    }
  }

  function copyToClipboard(text: string) {
    navigator.clipboard.writeText(text).then(() => {
      toast.success("Copied to clipboard");
    });
  }

  function navigateBack() {
    goto(resolve("/streams"));
  }

  function toggleHealthSidebar() {
    healthSidebarCollapsed = !healthSidebarCollapsed;
  }

  function toggleEventLog() {
    eventLogCollapsed = !eventLogCollapsed;
  }

  const ArrowLeftIcon = getIconComponent("ArrowLeft");
  const EditIcon = getIconComponent("Edit");
  const Trash2Icon = getIconComponent("Trash2");
  const ActivityIcon = getIconComponent("Activity");
  const CircleIcon = getIconComponent("Circle");
  const InfoIcon = getIconComponent("Info");
  const SettingsIcon = getIconComponent("Settings");
  const KeyIcon = getIconComponent("Key");
  const VideoIcon = getIconComponent("Video");
  const PlayIcon = getIconComponent("Play");
</script>

<svelte:head>
  <title>Stream Details - {stream?.name || "Loading..."} - FrameWorks</title>
</svelte:head>

<div class="h-full flex flex-col">
  <!-- Fixed Page Header -->
  <div class="px-4 sm:px-6 lg:px-8 py-4 border-b border-[hsl(var(--tn-fg-gutter)/0.3)] shrink-0">
    <div class="flex items-center justify-between">
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
          <h1 class="text-xl font-bold text-foreground">
            Stream Details
          </h1>
          <div class="flex items-center gap-2 mt-0.5">
            <span class="text-sm font-medium text-foreground">
              {stream?.name || "Loading..."}
            </span>
            <span class="text-xs text-muted-foreground">•</span>
            <span class="text-xs text-muted-foreground font-mono">
              {stream?.id?.slice(0, 8) || ""}...
            </span>
            {#if stream}
              <!-- Status Badge -->
              <span class="flex items-center gap-1.5 px-2 py-0.5 rounded text-[10px] font-medium {isLive ? 'bg-success/20 text-success' : 'bg-muted text-muted-foreground'}">
                <CircleIcon class="w-1.5 h-1.5 {isLive ? 'fill-current animate-pulse' : ''}" />
                {isLive ? "LIVE" : "OFFLINE"}
              </span>

              {#if stream.record}
                <span class="flex items-center gap-1.5 px-2 py-0.5 rounded text-[10px] font-medium bg-error/20 text-error">
                  <CircleIcon class="w-1.5 h-1.5 fill-current" />
                  REC
                </span>
              {/if}
            {/if}
          </div>
        </div>
      </div>

      {#if stream && !loading}
        <div class="flex items-center space-x-2">
          <!-- Health Toggle (desktop) -->
          <Button
            variant="ghost"
            size="sm"
            class="gap-2 flex {healthSidebarCollapsed ? '' : 'bg-[hsl(var(--tn-bg-visual))] text-primary'}"
            onclick={toggleHealthSidebar}
          >
            <ActivityIcon class="w-4 h-4" />
            Health
          </Button>

          <Button
            variant="ghost"
            size="sm"
            class="gap-2"
            onclick={() => (showEditModal = true)}
          >
            <EditIcon class="w-4 h-4" />
            <span class="hidden sm:inline">Edit</span>
          </Button>

          <Button
            variant="ghost"
            size="sm"
            class="gap-2 text-destructive hover:text-destructive hover:bg-destructive/10"
            onclick={() => (showDeleteModal = true)}
          >
            <Trash2Icon class="w-4 h-4" />
            <span class="hidden sm:inline">Delete</span>
          </Button>
        </div>
      {/if}
    </div>
  </div>

  <!-- Main Content Area -->
  <div class="flex-1 flex overflow-hidden">
    {#if loading}
      <div class="flex-1 p-6">
        <LoadingCard variant="analytics" />
      </div>
    {:else if error}
      {@const AlertTriangleIcon = getIconComponent("AlertTriangle")}
      <div class="flex-1 p-6">
        <div class="border border-error/30 bg-error/10 p-8 text-center">
          <AlertTriangleIcon class="w-8 h-8 text-error mx-auto mb-4" />
          <h3 class="text-lg font-semibold text-error mb-2">Error Loading Stream</h3>
          <p class="text-error mb-4">{error}</p>
          <Button variant="outline" onclick={loadStreamData}>Retry</Button>
        </div>
      </div>
    {:else if stream}
      <!-- Main Content (scrollable) -->
      <div class="flex-1 overflow-y-auto">
        <div class="flex flex-col">
          <!-- Stream Overview Cards -->
          <div class="grid grid-cols-1 md:grid-cols-3 divide-y md:divide-y-0 md:divide-x divide-[hsl(var(--tn-fg-gutter)/0.3)] bg-background">
            <StreamStatusCard {stream} {analytics} />
            <StreamKeyCard
              {stream}
              loading={actionLoading.refreshKey}
              onRefresh={handleRefreshStreamKey}
              onCopy={copyToClipboard}
            />
            <StreamPlaybackCard {stream} onCopy={copyToClipboard} />
          </div>

          <SectionDivider showBar={false} class="p-0" />

          <!-- Tabbed Content -->
          <div class="slab border-b border-[hsl(var(--tn-fg-gutter)/0.3)]">
            <Tabs value="overview" class="w-full">
              <TabsList class="flex w-full rounded-none p-0 h-auto bg-[hsl(var(--tn-bg-dark)/0.5)] border-b border-[hsl(var(--tn-fg-gutter)/0.3)] justify-start overflow-x-auto items-center">
                  <TabsTrigger
                    value="overview"
                    class="gap-2 px-4 py-3 text-sm font-medium text-muted-foreground border-b-2 border-transparent rounded-none data-[state=active]:text-info data-[state=active]:border-info cursor-pointer hover:bg-muted/20 transition-colors"
                  >
                    <InfoIcon class="w-4 h-4" />
                    Overview
                  </TabsTrigger>
                  <TabsTrigger
                    value="ingest"
                    class="gap-2 px-4 py-3 text-sm font-medium text-muted-foreground border-b-2 border-transparent rounded-none data-[state=active]:text-info data-[state=active]:border-info cursor-pointer hover:bg-muted/20 transition-colors"
                  >
                    <SettingsIcon class="w-4 h-4" />
                    Ingest
                  </TabsTrigger>
                  <TabsTrigger
                    value="recordings"
                    class="gap-2 px-4 py-3 text-sm font-medium text-muted-foreground border-b-2 border-transparent rounded-none data-[state=active]:text-info data-[state=active]:border-info cursor-pointer hover:bg-muted/20 transition-colors"
                  >
                    <VideoIcon class="w-4 h-4" />
                    Recordings ({recordings.length})
                  </TabsTrigger>
                  <TabsTrigger
                    value="playback"
                    class="gap-2 px-4 py-3 text-sm font-medium text-muted-foreground border-b-2 border-transparent rounded-none data-[state=active]:text-info data-[state=active]:border-info cursor-pointer hover:bg-muted/20 transition-colors"
                  >
                    <PlayIcon class="w-4 h-4" />
                    Playback
                  </TabsTrigger>
                </TabsList>

              <TabsContent value="overview" class="p-0 min-h-[20rem]">
                <OverviewTabPanel
                  {stream}
                  {streamKeys}
                  {recordings}
                  {clips}
                  {analytics}
                  tracks={currentTracks ?? fallbackTracks}
                  {viewerMetrics}
                  dailyAnalytics={streamDailyAnalytics}
                />
              </TabsContent>

              <TabsContent value="ingest" class="p-0 min-h-[20rem]">
                <StreamSetupPanel
                  {stream}
                  {streamKeys}
                  onRefreshKey={handleRefreshStreamKey}
                  refreshingKey={actionLoading.refreshKey}
                  onCreateKey={() => (showCreateKeyModal = true)}
                  onCopyKey={copyToClipboard}
                  onDeleteKey={handleDeleteStreamKey}
                  deleteLoading={actionLoading.deleteKey}
                />
              </TabsContent>

              <TabsContent value="recordings" class="p-0 min-h-[20rem]">
                <RecordingsTabPanel
                  {recordings}
                  onEnableRecording={() => (showEditModal = true)}
                  onCopyLink={copyToClipboard}
                />
              </TabsContent>

              <TabsContent value="playback" class="p-0 min-h-[20rem]">
                <PlaybackTabPanel
                  playbackId={stream?.playbackId}
                />
              </TabsContent>
            </Tabs>
          </div>

          <!-- Event Log (collapsible) -->
          <EventLog
            events={streamEvents}
            title="Event Log"
            collapsed={eventLogCollapsed}
            onToggle={toggleEventLog}
          />
        </div>
      </div>

      <!-- Health Sidebar (right side, collapsible) -->
      <div class="block shrink-0 {healthSidebarCollapsed ? 'w-10' : 'w-72'}">
        <HealthSidebar
          {streamId}
          streamName={stream.name}
          {isLive}
          {health}
          {analytics}
          collapsed={healthSidebarCollapsed}
          onToggle={toggleHealthSidebar}
        />
      </div>
    {/if}
  </div>

  <!-- Modals -->
  <StreamEditModal
    bind:open={showEditModal}
    {stream}
    loading={actionLoading.editStream}
    onSave={handleEditStream}
  />
  <StreamDeleteModal
    bind:open={showDeleteModal}
    streamName={stream?.name || ""}
    loading={actionLoading.deleteStream}
    onConfirm={handleDeleteStream}
  />
  <StreamCreateKeyModal
    bind:open={showCreateKeyModal}
    loading={actionLoading.createKey}
    onCreate={handleCreateStreamKey}
  />
</div>

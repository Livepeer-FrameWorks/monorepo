<script lang="ts">
  import { onMount, onDestroy, untrack } from "svelte";
  import { page } from "$app/state";
  import { goto } from "$app/navigation";
  import { resolve } from "$app/paths";
  import {
    GetStreamStore,
    GetStreamKeysStore,
    GetStreamRecordingsStore,
    GetStreamAnalyticsStore,
    GetCurrentStreamHealthStore,
    GetViewerCountTimeSeriesStore,
    UpdateStreamStore,
    DeleteStreamStore,
    RefreshStreamKeyStore,
    CreateStreamKeyStore,
    DeleteStreamKeyStore,
    StreamEventsStore,
    ViewerMetricsStreamStore,
    TrackListUpdatesStore,
  } from "$houdini";
  import type {
    StreamEvents$result,
    ViewerMetricsStream$result,
    TrackListUpdates$result,
  } from "$houdini";
  import { toast } from "$lib/stores/toast.js";
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
    StreamKeysTabPanel,
    RecordingsTabPanel,
    HealthSidebar,
    EventLog,
    StreamSetupPanel,
  } from "$lib/components/stream-details";
  import type { StreamEvent, EventType } from "$lib/components/stream-details/EventLog.svelte";

  // Houdini stores
  const streamStore = new GetStreamStore();
  const streamKeysStore = new GetStreamKeysStore();
  const recordingsStore = new GetStreamRecordingsStore();
  const analyticsStore = new GetStreamAnalyticsStore();
  const healthStore = new GetCurrentStreamHealthStore();
  const viewerCountStore = new GetViewerCountTimeSeriesStore();
  const updateStreamMutation = new UpdateStreamStore();
  const deleteStreamMutation = new DeleteStreamStore();
  const refreshStreamKeyMutation = new RefreshStreamKeyStore();
  const createStreamKeyMutation = new CreateStreamKeyStore();
  const deleteStreamKeyMutation = new DeleteStreamKeyStore();
  const streamEventsSub = new StreamEventsStore();
  const viewerMetricsSub = new ViewerMetricsStreamStore();
  const trackListSub = new TrackListUpdatesStore();

  // Types from Houdini
  type StreamType = NonNullable<NonNullable<typeof $streamStore.data>["stream"]>;
  type StreamKeyType = NonNullable<NonNullable<typeof $streamKeysStore.data>["streamKeys"]>[0];
  type RecordingType = NonNullable<NonNullable<typeof $recordingsStore.data>["recordings"]>[0];
  type TrackInfo = NonNullable<TrackListUpdates$result["trackListUpdates"]>;
  type HealthData = NonNullable<NonNullable<typeof $healthStore.data>["currentStreamHealth"]>;

  // page is a store; derive the param so it stays in sync with navigation
  let streamId = $derived(page.params.id as string);

  // Derived state from Houdini stores
  let stream = $derived($streamStore.data?.stream ?? null);
  let streamKeys = $derived($streamKeysStore.data?.streamKeys ?? []);
  let recordings = $derived($recordingsStore.data?.recordings ?? []);
  let analytics = $derived($analyticsStore.data?.streamAnalytics ?? null);
  let health = $derived($healthStore.data?.currentStreamHealth ?? null);
  let viewerMetrics = $derived($viewerCountStore.data?.viewerCountTimeSeries ?? []);
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

    // Refresh health every 30 seconds when live
    healthRefreshInterval = setInterval(async () => {
      if (isLive && stream?.id) {
        await healthStore.fetch({ variables: { stream: stream.id } });
      }
    }, 30000);
  });

  onDestroy(() => {
    if (refreshInterval) clearInterval(refreshInterval);
    if (healthRefreshInterval) clearInterval(healthRefreshInterval);
    streamEventsSub.unlisten();
    viewerMetricsSub.unlisten();
    trackListSub.unlisten();
  });

  function startSubscriptions() {
    const streamData = $streamStore.data?.stream;
    if (!streamData) return;

    // Use stream.id (internal UUID) for subscriptions - this is the canonical identifier
    streamEventsSub.listen({ stream: streamData.id });
    viewerMetricsSub.listen({ stream: streamData.id });
    trackListSub.listen({ stream: streamData.id });
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

  function handleStreamEvent(event: NonNullable<StreamEvents$result["streamEvents"]>) {
    if (event.type === "STREAM_START" || event.status === "LIVE") {
      toast.success("Stream is now live!");
      addEvent("stream_start", "Stream started", `Status: ${event.status}`);
      // Expand event log on stream start
      eventLogCollapsed = false;
    } else if (event.type === "STREAM_END" || event.status === "OFFLINE") {
      toast.info("Stream went offline");
      addEvent("stream_end", "Stream ended");
    } else if (event.type === "STREAM_ERROR") {
      addEvent("error", "Stream error", event.details ? JSON.stringify(event.details) : undefined);
    } else if (event.type === "BUFFER_UPDATE") {
      // Buffer updates are handled by health subscription, log only critical ones
      const details = event.details as Record<string, unknown> | null;
      if (details?.bufferState === "DRY") {
        addEvent("warning", "Buffer dry", "Stream may be experiencing issues");
      }
    } else if (event.type === "TRACK_LIST_UPDATE") {
      addEvent("track_change", "Track list updated");
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
      // Use stream.id (internal UUID) for analytics queries - this is the canonical identifier
      const streamUUID = result.data.stream.id;

      await Promise.all([
        streamKeysStore.fetch({ variables: { streamId } }),
        recordingsStore.fetch({ variables: { streamId } }),
        analyticsStore.fetch({ variables: { stream: streamUUID, timeRange } }).catch(() => null),
        healthStore.fetch({ variables: { stream: streamUUID } }).catch(() => null),
        viewerCountStore.fetch({ variables: { stream: streamUUID, timeRange, interval: "5m" } }).catch(() => null),
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
      // Use stream.id (internal UUID) for analytics queries - this is the canonical identifier
      const streamUUID = $streamStore.data?.stream?.id;

      await Promise.all([
        streamStore.fetch({ variables: { id: streamId } }),
        streamUUID ? analyticsStore.fetch({ variables: { stream: streamUUID, timeRange } }).catch(() => null) : Promise.resolve(),
        streamUUID ? viewerCountStore.fetch({ variables: { stream: streamUUID, timeRange, interval: "5m" } }).catch(() => null) : Promise.resolve(),
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
</script>

<svelte:head>
  <title>Stream Details - {stream?.name || "Loading..."} - FrameWorks</title>
</svelte:head>

<div class="h-full flex flex-col">
  <!-- Fixed Page Header -->
  <div class="px-4 sm:px-6 lg:px-8 py-4 border-b border-border shrink-0">
    <div class="flex items-center justify-between">
      <div class="flex items-center space-x-4">
        <button
          onclick={navigateBack}
          class="p-2 border border-border/50 hover:bg-muted/50 transition-colors"
        >
          <ArrowLeftIcon class="w-5 h-5" />
        </button>

        <div class="flex items-center gap-3">
          <h1 class="text-xl font-bold text-foreground">
            {stream?.name || "Stream Details"}
          </h1>

          {#if stream}
            <!-- Status Badge -->
            <span class="flex items-center gap-1.5 px-2 py-1 text-xs font-medium {isLive ? 'bg-success/20 text-success' : 'bg-muted text-muted-foreground'}">
              <CircleIcon class="w-2 h-2 {isLive ? 'fill-current animate-pulse' : ''}" />
              {isLive ? "LIVE" : "OFFLINE"}
            </span>

            {#if stream.record}
              <span class="flex items-center gap-1.5 px-2 py-1 text-xs font-medium bg-error/20 text-error">
                <CircleIcon class="w-2 h-2 fill-current" />
                REC
              </span>
            {/if}
          {/if}
        </div>
      </div>

      {#if stream && !loading}
        <div class="flex items-center space-x-2">
          <!-- Health Toggle (desktop) -->
          <Button
            variant={healthSidebarCollapsed ? "outline" : "default"}
            size="sm"
            class="gap-2 hidden md:flex"
            onclick={toggleHealthSidebar}
          >
            <ActivityIcon class="w-4 h-4" />
            Health
          </Button>

          <Button
            variant="outline"
            size="sm"
            class="gap-2"
            onclick={() => (showEditModal = true)}
          >
            <EditIcon class="w-4 h-4" />
            <span class="hidden sm:inline">Edit</span>
          </Button>

          <Button
            variant="destructive"
            size="sm"
            class="gap-2"
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
        <div class="p-4 sm:p-6 lg:p-8 space-y-6">
          <!-- Stream Overview Cards -->
          <div class="grid grid-cols-1 md:grid-cols-3 border border-border/30">
            <div class="border-b md:border-b-0 md:border-r border-border/30">
              <StreamStatusCard {stream} {analytics} />
            </div>
            <div class="border-b md:border-b-0 md:border-r border-border/30">
              <StreamKeyCard
                {stream}
                loading={actionLoading.refreshKey}
                onRefresh={handleRefreshStreamKey}
                onCopy={copyToClipboard}
              />
            </div>
            <div>
              <StreamPlaybackCard {stream} onCopy={copyToClipboard} />
            </div>
          </div>

          <!-- Tabbed Content -->
          <div class="border border-border">
            <Tabs value="overview" class="w-full">
              <div class="border-b border-border bg-brand-surface-muted">
                <TabsList class="flex px-4 bg-transparent w-full justify-start overflow-x-auto">
                  <TabsTrigger
                    value="overview"
                    class="gap-2 px-4 py-3 text-sm font-medium text-muted-foreground border-b-2 border-transparent rounded-none data-[state=active]:text-info data-[state=active]:border-info"
                  >
                    <InfoIcon class="w-4 h-4" />
                    Overview
                  </TabsTrigger>
                  <TabsTrigger
                    value="setup"
                    class="gap-2 px-4 py-3 text-sm font-medium text-muted-foreground border-b-2 border-transparent rounded-none data-[state=active]:text-info data-[state=active]:border-info"
                  >
                    <SettingsIcon class="w-4 h-4" />
                    Setup
                  </TabsTrigger>
                  <TabsTrigger
                    value="keys"
                    class="gap-2 px-4 py-3 text-sm font-medium text-muted-foreground border-b-2 border-transparent rounded-none data-[state=active]:text-info data-[state=active]:border-info"
                  >
                    <KeyIcon class="w-4 h-4" />
                    Keys ({streamKeys.length})
                  </TabsTrigger>
                  <TabsTrigger
                    value="recordings"
                    class="gap-2 px-4 py-3 text-sm font-medium text-muted-foreground border-b-2 border-transparent rounded-none data-[state=active]:text-info data-[state=active]:border-info"
                  >
                    <VideoIcon class="w-4 h-4" />
                    Recordings ({recordings.length})
                  </TabsTrigger>
                </TabsList>
              </div>

              <TabsContent value="overview" class="p-4 sm:p-6 min-h-[20rem]">
                <OverviewTabPanel
                  {stream}
                  {streamKeys}
                  {recordings}
                  {analytics}
                  tracks={currentTracks}
                  {viewerMetrics}
                />
              </TabsContent>

              <TabsContent value="setup" class="p-4 sm:p-6 min-h-[20rem]">
                <StreamSetupPanel
                  {stream}
                  onRefreshKey={handleRefreshStreamKey}
                  refreshingKey={actionLoading.refreshKey}
                />
              </TabsContent>

              <TabsContent value="keys" class="p-4 sm:p-6 min-h-[20rem]">
                <StreamKeysTabPanel
                  {streamKeys}
                  onCreateKey={() => (showCreateKeyModal = true)}
                  onCopyKey={copyToClipboard}
                  onDeleteKey={handleDeleteStreamKey}
                  deleteLoading={actionLoading.deleteKey}
                />
              </TabsContent>

              <TabsContent value="recordings" class="p-4 sm:p-6 min-h-[20rem]">
                <RecordingsTabPanel
                  {recordings}
                  onEnableRecording={() => (showEditModal = true)}
                  onCopyLink={copyToClipboard}
                  resolveUrl={resolve}
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
      <div class="hidden md:block shrink-0 {healthSidebarCollapsed ? 'w-10' : 'w-72'}">
        <HealthSidebar
          {streamId}
          streamName={stream.name}
          {isLive}
          {health}
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

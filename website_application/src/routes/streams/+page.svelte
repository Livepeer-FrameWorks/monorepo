<script lang="ts">
  import { preventDefault } from "svelte/legacy";
  import { onMount, onDestroy } from "svelte";
  import { resolve } from "$app/paths";
  import { auth } from "$lib/stores/auth";
  import { streamsService } from "$lib/graphql/services/streams.js";
  import { dvrService } from "$lib/graphql/services/dvr.js";
  import { healthService } from "$lib/graphql/services/health.js";
import {
  subscribeToStreamMetrics,
  streamMetrics,
} from "$lib/stores/realtime.js";
  import { getIngestUrls, getDeliveryUrls } from "$lib/config";
  import { toast } from "$lib/stores/toast.js";
  import type { Stream, StreamKey } from "$lib/graphql/generated/types";
  import LoadingCard from "$lib/components/LoadingCard.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import HealthScoreIndicator from "$lib/components/health/HealthScoreIndicator.svelte";
  import { FlowLayout } from "$lib/components/layout";
  import StreamSetupGuide from "$lib/components/stream-details/StreamSetupGuide.svelte";
  import DeleteStreamModal from "$lib/components/stream-details/DeleteStreamModal.svelte";
  import CreateStreamKeyModal from "$lib/components/stream-details/CreateStreamKeyModal.svelte";
  import CreateStreamModal from "$lib/components/stream-details/CreateStreamModal.svelte";
  import StreamMetricsGrid from "$lib/components/stream-details/StreamMetricsGrid.svelte";
  import StreamDeliveryPanel from "$lib/components/stream-details/StreamDeliveryPanel.svelte";
  import StreamIngestPanel from "$lib/components/stream-details/StreamIngestPanel.svelte";
  import StreamCard from "$lib/components/stream-details/StreamCard.svelte";
  import StreamKeysManager from "$lib/components/stream-details/StreamKeysManager.svelte";
  import DVRRecordingsManager from "$lib/components/stream-details/DVRRecordingsManager.svelte";
  import { getIconComponent } from "$lib/iconUtils";
  import { Button } from "$lib/components/ui/button";
  import {
    Dialog,
    DialogContent,
    DialogDescription,
    DialogFooter,
    DialogHeader,
    DialogTitle,
  } from "$lib/components/ui/dialog";
  import { Input } from "$lib/components/ui/input";
  import { Textarea } from "$lib/components/ui/textarea";
  import { Checkbox } from "$lib/components/ui/checkbox";
  import { Label } from "$lib/components/ui/label";
  import {
    Tabs,
    TabsContent,
    TabsList,
    TabsTrigger,
  } from "$lib/components/ui/tabs";

  let isAuthenticated = false;
  let streams = $state<Stream[]>([]);
  let loading = $state(true);
  let refreshingKey = $state(false);
let copiedUrl = $state("");

// Real-time subscription management
let unsubscribeStreamMetrics = null;
let unsubscribeMetricsStore = null;
let latestMetricsByStream = {};

  // Stream creation
  let creatingStream = $state(false);
  let showCreateModal = $state(false);
  let newStreamTitle = $state("");
  let newStreamDescription = $state("");
  let newStreamRecord = $state(false);

  // Stream deletion
  let deletingStreamId = $state("");
  let showDeleteModal = $state(false);
  let streamToDelete = $state<Stream | null>(null);

  // Selected stream for detailed view
  let selectedStream = $state<Stream | null>(null);

  // Tab management
  let activeTab = $state("overview"); // overview, keys, recordings

  // Stream keys management
  let streamKeys = $state<StreamKey[]>([]);
  let loadingStreamKeys = $state(false);
  let creatingStreamKey = $state(false);
  let showCreateKeyModal = $state(false);
  let newKeyName = $state("");
  let deletingKeyId = $state("");

  // Stream health data for all streams
  let streamHealthData = $state(new Map());

  // DVR management
  let streamRecordings = $state([]);
  let loadingRecordings = $state(false);
  let startingDVR = $state(false);
  let stoppingDVR = $state(false);

  // Real-time metrics for selected stream (from GraphQL subscriptions)
  let realTimeMetrics = $state({
    currentViewers: 0,
    peakViewers: 0,
    bandwidth: 0,
    connectionQuality: null,
    bufferHealth: null,
    timestamp: null,
  });

  // Subscribe to auth store
  auth.subscribe((authState) => {
    isAuthenticated = authState.isAuthenticated;
  });

  onMount(async () => {
    unsubscribeMetricsStore = streamMetrics.subscribe((metrics) => {
      latestMetricsByStream = metrics || {};
      if (selectedStream) {
        applyRealTimeMetrics(selectedStream.id);
      }
    });

    if (!isAuthenticated) {
      await auth.checkAuth();
    }
    await loadStreams();

    // Select first stream by default if available
    if (streams.length > 0) {
      selectedStream = streams[0];
      startRealTimeSubscriptions();
    }
  });

  // Cleanup on unmount
  onDestroy(() => {
    if (unsubscribeStreamMetrics) {
      unsubscribeStreamMetrics();
    }
    if (unsubscribeMetricsStore) {
      unsubscribeMetricsStore();
    }
  });

  async function loadStreams() {
    try {
      loading = true;
      streams = await streamsService.getStreams();

      // Update auth store with latest streams
      auth.updateStreams(streams);

      // Load health data for all streams
      await loadStreamsHealthData();

      // Auto-select first stream if available
      if (streams.length > 0 && !selectedStream) {
        selectedStream = streams[0];
        startRealTimeSubscriptions();
      }
    } catch (error) {
      console.error("Failed to load streams:", error);
    } finally {
      loading = false;
    }
  }

  // Load health data for all streams
  async function loadStreamsHealthData() {
    const healthPromises = streams.map(async (stream) => {
      try {
        const health = await healthService.getCurrentStreamHealth(stream.id);
        if (health) {
          streamHealthData.set(stream.id, health);
        }
      } catch (error) {
        console.warn(
          `Failed to load health data for stream ${stream.id}:`,
          error,
        );
      }
    });

    await Promise.allSettled(healthPromises);
    // Trigger reactive update
    streamHealthData = streamHealthData;
  }

  // Start real-time GraphQL subscriptions
  function startRealTimeSubscriptions() {
    if (!selectedStream) return;

    // Clean up existing subscription
    if (unsubscribeStreamMetrics) {
      unsubscribeStreamMetrics();
    }

    // Subscribe to viewer metrics for the selected stream
    unsubscribeStreamMetrics = subscribeToStreamMetrics(selectedStream.id);
    applyRealTimeMetrics(selectedStream.id);
  }

  // Create new stream
  async function createStream() {
    if (!newStreamTitle.trim()) {
      toast.warning("Please enter a stream title");
      return;
    }

    try {
      creatingStream = true;
      const newStream = await streamsService.createStream({
        name: newStreamTitle.trim(),
        description: newStreamDescription.trim() || undefined,
        record: newStreamRecord,
      });

      // Add new stream to list
      streams = [...streams, newStream];

      // Select the new stream
      selectedStream = newStream;
      startRealTimeSubscriptions();

      // Update auth store
      auth.updateStreams(streams);

      // Close modal and reset form
      showCreateModal = false;
      newStreamTitle = "";
      newStreamDescription = "";
      newStreamRecord = false;

      toast.success("Stream created successfully!");
    } catch (error) {
      console.error("Failed to create stream:", error);
      toast.error("Failed to create stream. Please try again.");
    } finally {
      creatingStream = false;
    }
  }

  // Delete stream
  async function deleteStream() {
    if (!streamToDelete) return;

    try {
      deletingStreamId = streamToDelete.id;
      await streamsService.deleteStream(streamToDelete.id);

      // Remove stream from list
      streams = streams.filter((s) => s.id !== streamToDelete.id);

      // Update selected stream if deleted
      if (selectedStream?.id === streamToDelete.id) {
        selectedStream = streams.length > 0 ? streams[0] : null;
        if (selectedStream) {
          startRealTimeSubscriptions();
        }
      }

      // Update auth store
      auth.updateStreams(streams);

      // Close modal
      showDeleteModal = false;
      streamToDelete = null;

      toast.success("Stream deleted successfully!");
    } catch (error) {
      console.error("Failed to delete stream:", error);
      toast.error("Failed to delete stream. Please try again.");
    } finally {
      deletingStreamId = "";
    }
  }

  // Select stream
  function selectStream(stream) {
    selectedStream = stream;
    startRealTimeSubscriptions();

    // Load stream keys when stream is selected
    if (activeTab === "keys") {
      loadStreamKeys();
    }
  }

  // Load stream keys for selected stream
  async function loadStreamKeys() {
    if (!selectedStream) return;

    try {
      loadingStreamKeys = true;
      streamKeys = await streamsService.getStreamKeys(selectedStream.id);
    } catch (error) {
      console.error("Failed to load stream keys:", error);
      toast.error("Failed to load stream keys");
    } finally {
      loadingStreamKeys = false;
    }
  }

  // Create new stream key
  async function createStreamKey() {
    if (!selectedStream || !newKeyName.trim()) return;

    try {
      creatingStreamKey = true;
      const newKey = await streamsService.createStreamKey(selectedStream.id, {
        name: newKeyName.trim(),
      });

      streamKeys = [...streamKeys, newKey];
      showCreateKeyModal = false;
      newKeyName = "";

      toast.success("Stream key created successfully!");
    } catch (error) {
      console.error("Failed to create stream key:", error);
      toast.error("Failed to create stream key");
    } finally {
      creatingStreamKey = false;
    }
  }

  // Delete stream key
  async function deleteStreamKey(keyId) {
    if (!selectedStream) return;

    try {
      deletingKeyId = keyId;
      await streamsService.deleteStreamKey(selectedStream.id, keyId);

      streamKeys = streamKeys.filter((key) => key.id !== keyId);
      toast.success("Stream key deleted successfully!");
    } catch (error) {
      console.error("Failed to delete stream key:", error);
      toast.error("Failed to delete stream key");
    } finally {
      deletingKeyId = "";
    }
  }

  // Load stream recordings
  async function loadStreamRecordings() {
    if (!selectedStream) return;

    try {
      loadingRecordings = true;
      const result = await dvrService.getStreamRecordings(selectedStream.name);
      streamRecordings = result.recordings || [];

      await dvrService
        .getRecordingConfig(selectedStream.name)
        .catch(() => null);
    } catch (error) {
      console.error("Failed to load recordings:", error);
      toast.error("Failed to load recordings");
    } finally {
      loadingRecordings = false;
    }
  }

  // Start DVR recording
  async function startDVRRecording() {
    if (!selectedStream) return;

    try {
      startingDVR = true;
      const result = await dvrService.startDVR(
        selectedStream.name,
        selectedStream.id,
      );

      if (result.success) {
        toast.success("DVR recording started successfully!");
        loadStreamRecordings(); // Refresh recordings list
      } else {
        toast.error(result.error || "Failed to start DVR recording");
      }
    } catch (error) {
      console.error("Failed to start DVR:", error);
      toast.error("Failed to start DVR recording");
    } finally {
      startingDVR = false;
    }
  }

  // Stop DVR recording
  async function stopDVRRecording(dvrHash) {
    try {
      stoppingDVR = true;
      const result = await dvrService.stopDVR(dvrHash);

      if (result.success) {
        toast.success("DVR recording stopped successfully!");
        loadStreamRecordings(); // Refresh recordings list
      } else {
        toast.error(result.error || "Failed to stop DVR recording");
      }
    } catch (error) {
      console.error("Failed to stop DVR:", error);
      toast.error("Failed to stop DVR recording");
    } finally {
      stoppingDVR = false;
    }
  }

  function handleTabChange(tab) {
    activeTab = tab;
    if (tab === "keys" && selectedStream) {
      loadStreamKeys();
    } else if (tab === "recordings" && selectedStream) {
      loadStreamRecordings();
    }
  }

  // Show delete confirmation
  function confirmDeleteStream(stream) {
    streamToDelete = stream;
    showDeleteModal = true;
  }

  /**
   * Format bandwidth for display
   * @param {number} bytes
   */
  function formatBandwidth(bytes) {
    if (!bytes) return "0 Kbps";
    const kbps = Math.round((bytes / 1024) * 8);
    if (kbps > 1000) {
      return `${(kbps / 1000).toFixed(1)} Mbps`;
    }
    return `${kbps} Kbps`;
  }

  /**
   * Format resolution
   * @param {number} width
   * @param {number} height
   */
  /**
   * @param {string} streamId
   */
  async function refreshStreamKey(streamId) {
    if (!streamId) return;

    try {
      refreshingKey = true;
      const updatedStream = await streamsService.refreshStreamKey(streamId);

      if (updatedStream) {
        streams = streams.map((stream) =>
          stream.id === streamId ? updatedStream : stream,
        );

        if (selectedStream?.id === streamId) {
          selectedStream = updatedStream;
        }

        toast.success(
          "Stream key refreshed successfully! Please update your streaming software with the new key.",
        );
      }
    } catch (error) {
      console.error("Failed to refresh stream key:", error);
      toast.error("Failed to refresh stream key. Please try again.");
    } finally {
      refreshingKey = false;
    }
  }

  /**
   * @param {string} text
   */
  async function copyToClipboard(text) {
    try {
      await navigator.clipboard.writeText(text);
      copiedUrl = text;
      setTimeout(() => {
        copiedUrl = "";
      }, 2000);
    } catch (error) {
      console.error("Failed to copy to clipboard:", error);
    }
  }

  // Get the first stream (primary stream)

  // Reactive URLs based on selected stream
  let ingestUrls = $derived(
    selectedStream ? getIngestUrls(selectedStream.streamKey) : {},
  );
  let deliveryUrls = $derived(
    selectedStream ? getDeliveryUrls(selectedStream.playbackId) : {},
  );

  // Stream status for selected stream
  let streamStatus = $derived(selectedStream?.status || "offline");
  let isLive = $derived(streamStatus === "live");
  let viewers = $derived(
    selectedStream?.viewers || realTimeMetrics.viewers || 0,
  );

  // Ingest protocols configuration
  const ingestProtocols = [
    {
      name: "RTMP",
      description:
        "Standard streaming protocol for OBS, XSplit, and most streaming software",
      key: "rtmp",
      icon: "Wifi",
      recommended: true,
      setup:
        "Use this URL as your RTMP server in streaming software like OBS Studio",
    },
    {
      name: "WebRTC (WHIP)",
      description: "Ultra-low latency browser-based streaming",
      key: "whip",
      icon: "Globe",
      recommended: false,
      setup:
        "Modern browsers and WebRTC-compatible software can stream directly to this endpoint",
    },
  ];

  // Delivery protocols configuration
  const deliveryProtocols = [
    {
      name: "HLS",
      description:
        "HTTP Live Streaming - works in all browsers and mobile apps",
      key: "hls",
      icon: "FileText",
      recommended: true,
      fileExtension: ".m3u8",
    },
    {
      name: "WebRTC (WHEP)",
      description: "Ultra-low latency playback in modern browsers",
      key: "webrtc",
      icon: "Link",
      recommended: false,
      fileExtension: "",
    },
    {
      name: "WebM",
      description: "Direct WebM video stream",
      key: "webm",
      icon: "Play",
      recommended: false,
      fileExtension: ".webm",
    },
    {
      name: "MKV",
      description: "Matroska video container",
      key: "mkv",
      icon: "Video",
      recommended: false,
      fileExtension: ".mkv",
    },
    {
      name: "MP4",
      description: "Direct MP4 video stream",
      key: "mp4",
      icon: "Video",
      recommended: false,
      fileExtension: ".mp4",
    },
    // ,
    // {
    //   name: 'Embed Player',
    //   description: 'Ready-to-use iframe embed code',
    //   key: 'embed',
    //   icon: '🔗',
    //   recommended: false,
    //   fileExtension: ''
    // }
  ];

  function applyRealTimeMetrics(streamId) {
    if (!streamId) return;

    const metrics = latestMetricsByStream?.[streamId];
    if (!metrics) return;

    realTimeMetrics = {
      currentViewers: metrics.currentViewers ?? 0,
      peakViewers: metrics.peakViewers ?? 0,
      bandwidth: metrics.bandwidth ?? 0,
      connectionQuality: metrics.connectionQuality ?? null,
      bufferHealth: metrics.bufferHealth ?? null,
      timestamp: metrics.timestamp ?? null,
    };

    if (selectedStream?.id === streamId) {
      const currentViewers = metrics.currentViewers ?? 0;
      selectedStream = {
        ...selectedStream,
        viewers: currentViewers,
      };
    }

    if (metrics.currentViewers != null) {
      const currentViewers = metrics.currentViewers;
      streams = streams.map((stream) =>
        stream.id === streamId && stream.viewers !== currentViewers
          ? { ...stream, viewers: currentViewers }
          : stream,
      );
    }
  }

  const SvelteComponent = $derived(getIconComponent("Plus"));
  const SvelteComponent_1 = $derived(getIconComponent("BarChart3"));
  const SvelteComponent_2 = $derived(getIconComponent("Play"));
</script>

<svelte:head>
  <title>Streams - FrameWorks</title>
</svelte:head>

<div class="space-y-8 page-transition">
  <!-- Page Header -->
  <div class="flex justify-between items-start">
    <div>
      <h1 class="text-3xl font-bold text-tokyo-night-fg mb-2">
        Stream Management
      </h1>
      <p class="text-tokyo-night-fg-dark">
        Manage your live streams, view analytics, and configure ingest/delivery
        settings
      </p>
    </div>

    <div class="flex space-x-3">
      <Button
        variant="cta"
        class="gap-2"
        onclick={() => (showCreateModal = true)}
      >
        <SvelteComponent class="w-4 h-4" />
        Create Stream
      </Button>
      <Button href={resolve("/analytics")} variant="outline" class="gap-2">
        <SvelteComponent_1 class="w-4 h-4" />
        View Analytics
      </Button>
      <Button class="gap-2 cursor-not-allowed" disabled>
        <SvelteComponent_2 class="w-4 h-4 text-tokyo-night-red" />
        Go Live
      </Button>
    </div>
  </div>

  {#if loading}
    <!-- Loading Skeleton for Streams -->
    <div class="card mb-8">
      <div class="card-header">
        <div class="skeleton-text-lg w-48 mb-2"></div>
        <div class="skeleton-text w-96"></div>
      </div>
      <div class="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-4">
        {#each Array.from({ length: 6 }) as _, i (i)}
          <LoadingCard variant="stream" />
        {/each}
      </div>
    </div>

    <!-- Loading Skeleton for Stream Details -->
    <div class="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-6 gap-6 mb-8">
      {#each Array.from({ length: 6 }) as _, index (index)}
        <LoadingCard variant="metric" />
      {/each}
    </div>
  {:else if streams.length === 0}
    <!-- No Streams State -->
    <div class="card">
      <EmptyState
        iconName="Video"
        title="No Streams Found"
        description="Create your first stream to get started with broadcasting"
        actionText="Create Stream"
        onAction={() => (showCreateModal = true)}
      />
    </div>
  {:else}
    <!-- Streams List -->
    <div class="card mb-8">
      <div class="card-header">
        <h2 class="text-xl font-semibold text-tokyo-night-fg mb-2">
          Your Streams ({streams.length})
        </h2>
        <p class="text-tokyo-night-fg-dark">
          Select a stream to view detailed configuration and metrics
        </p>
      </div>

      <FlowLayout minWidth="md" gap="default">
        {#each streams as stream (stream.id ?? stream.name ?? stream.playbackId)}
          <StreamCard
            {stream}
            selected={selectedStream?.id === stream.id}
            deleting={deletingStreamId === stream.id}
            healthData={streamHealthData.get(stream.id) || null}
            onSelect={() => selectStream(stream)}
            onDelete={() => confirmDeleteStream(stream)}
          />
        {/each}
      </FlowLayout>
    </div>

    {#if selectedStream}
      <!-- Stream Status Cards for Selected Stream -->
      <StreamMetricsGrid
        {selectedStream}
        {isLive}
        {viewers}
        {realTimeMetrics}
        {formatBandwidth}
      />

      {#if realTimeMetrics.timestamp && isLive}
        <div class="text-center">
          <p class="text-xs text-tokyo-night-comment">
            Last updated: {new Date(
              realTimeMetrics.timestamp,
            ).toLocaleTimeString()}
          </p>
        </div>
      {/if}

      <!-- Stream Configuration Tabs -->
      {@const SvelteComponent_10 = getIconComponent("Settings")}
      {@const SvelteComponent_11 = getIconComponent("Key")}
      {@const SvelteComponent_12 = getIconComponent("Video")}
      <div class="card">
        <div class="card-header">
          <div class="flex items-center justify-between mb-4">
            <h2 class="text-xl font-semibold text-tokyo-night-fg">
              Stream Configuration
            </h2>
            <p class="text-tokyo-night-fg-dark text-sm">
              Manage ingest, keys, and delivery settings
            </p>
          </div>
        </div>

        <Tabs value={activeTab} onValueChange={handleTabChange} class="w-full">
          <TabsList class="grid grid-cols-3 gap-2 px-4">
            <TabsTrigger value="overview" class="gap-2">
              <SvelteComponent_10 class="w-4 h-4" />
              Overview
            </TabsTrigger>
            <TabsTrigger value="keys" class="gap-2">
              <SvelteComponent_11 class="w-4 h-4" />
              Stream Keys
            </TabsTrigger>
            <TabsTrigger value="recordings" class="gap-2">
              <SvelteComponent_12 class="w-4 h-4" />
              Recordings
            </TabsTrigger>
          </TabsList>

          <TabsContent value="overview" class="mt-6 space-y-6">
            <div class="grid grid-cols-1 xl:grid-cols-2 gap-8">
              <!-- Ingest Configuration -->
              <StreamIngestPanel
                {selectedStream}
                {ingestUrls}
                {ingestProtocols}
                {copiedUrl}
                {refreshingKey}
                onCopy={copyToClipboard}
                onRefreshKey={refreshStreamKey}
              />

              <!-- Delivery Configuration -->
              <StreamDeliveryPanel
                {deliveryUrls}
                {deliveryProtocols}
                {copiedUrl}
                onCopy={copyToClipboard}
              />
            </div>

            <!-- Quick Setup Guide -->
            <StreamSetupGuide />
          </TabsContent>

          <TabsContent value="keys" class="mt-6 space-y-6">
            <!-- Stream Keys Tab Content -->
            <StreamKeysManager
              {streamKeys}
              {loadingStreamKeys}
              {deletingKeyId}
              {copiedUrl}
              onCreateKey={() => (showCreateKeyModal = true)}
              onDeleteKey={deleteStreamKey}
              onCopy={copyToClipboard}
            />
          </TabsContent>

          <TabsContent value="recordings" class="mt-6 space-y-6">
            <!-- DVR Recordings Tab Content -->
            <DVRRecordingsManager
              {isLive}
              {streamRecordings}
              {loadingRecordings}
              {startingDVR}
              {stoppingDVR}
              {copiedUrl}
              onStartRecording={startDVRRecording}
              onStopRecording={stopDVRRecording}
              onCopy={copyToClipboard}
            />
          </TabsContent>
        </Tabs>
      </div>
    {/if}
  {/if}
</div>

<CreateStreamKeyModal
  open={showCreateKeyModal}
  bind:keyName={newKeyName}
  creating={creatingStreamKey}
  onSubmit={createStreamKey}
  onCancel={() => {
    showCreateKeyModal = false;
    newKeyName = "";
  }}
/>

<CreateStreamModal
  open={showCreateModal}
  bind:title={newStreamTitle}
  bind:description={newStreamDescription}
  bind:record={newStreamRecord}
  creating={creatingStream}
  onSubmit={createStream}
  onCancel={() => {
    showCreateModal = false;
    newStreamTitle = "";
    newStreamDescription = "";
    newStreamRecord = false;
  }}
/>

<!-- Delete Stream Modal -->
<DeleteStreamModal
  open={showDeleteModal && !!streamToDelete}
  stream={streamToDelete}
  deleting={!!deletingStreamId}
  onConfirm={deleteStream}
  onCancel={() => {
    showDeleteModal = false;
    streamToDelete = null;
  }}
/>

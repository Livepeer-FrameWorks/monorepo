<script>
  import { onMount, onDestroy } from "svelte";
  import { base } from "$app/paths";
  import { auth } from "$lib/stores/auth";
  import { streamsService } from "$lib/graphql/services/streams.js";
  import { dvrService } from "$lib/graphql/services/dvr.js";
  import { healthService } from "$lib/graphql/services/health.js";
  import { subscribeToStreamMetrics } from "$lib/stores/realtime.js";
  import { getIngestUrls, getDeliveryUrls } from "$lib/config";
  import { toast } from "$lib/stores/toast.js";
  import LoadingCard from "$lib/components/LoadingCard.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import HealthScoreIndicator from "$lib/components/health/HealthScoreIndicator.svelte";
  import { getIconComponent } from "$lib/iconUtils.js";

  let isAuthenticated = false;
  /** @type {any} */
  let user = null;
  /** @type {any[]} */
  let streams = [];
  let loading = true;
  let refreshingKey = false;
  let copiedUrl = "";
  
  // Real-time subscription management
  let unsubscribeStreamMetrics = null;

  // Stream creation
  let creatingStream = false;
  let showCreateModal = false;
  let newStreamTitle = "";
  let newStreamDescription = "";
  let newStreamRecord = false;

  // Stream deletion
  let deletingStreamId = "";
  let showDeleteModal = false;
  /** @type {any} */
  let streamToDelete = null;

  // Selected stream for detailed view
  /** @type {any} */
  let selectedStream = null;

  // Tab management
  let activeTab = "overview"; // overview, keys, recordings

  // Stream keys management
  /** @type {any[]} */
  let streamKeys = [];
  let loadingStreamKeys = false;
  let creatingStreamKey = false;
  let showCreateKeyModal = false;
  let newKeyName = "";
  let deletingKeyId = "";

  // Stream health data for all streams
  let streamHealthData = new Map();

  // DVR management
  let streamRecordings = [];
  let recordingConfig = null;
  let loadingRecordings = false;
  let startingDVR = false;
  let stoppingDVR = false;

  // Real-time metrics for selected stream (from GraphQL subscriptions)
  let realTimeMetrics = {
    currentViewers: 0,
    peakViewers: 0,
    bandwidth: 0,
    connectionQuality: null,
    bufferHealth: null,
    timestamp: null
  };

  // Subscribe to auth store
  auth.subscribe((authState) => {
    isAuthenticated = authState.isAuthenticated;
    user = authState.user;
  });

  onMount(async () => {
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
        console.warn(`Failed to load health data for stream ${stream.id}:`, error);
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
        record: newStreamRecord
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
      streams = streams.filter(s => s.id !== streamToDelete.id);
      
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
        name: newKeyName.trim()
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
      
      streamKeys = streamKeys.filter(key => key.id !== keyId);
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
      
      // Also load recording configuration
      const configResult = await dvrService.getRecordingConfig(selectedStream.name);
      recordingConfig = configResult.config;
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
      const result = await dvrService.startDVR(selectedStream.name, selectedStream.id);
      
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

  // Switch tabs
  function switchTab(tab) {
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
  function formatResolution(width, height) {
    if (!width || !height) return "Unknown";
    return `${width}x${height}`;
  }

  /**
   * @param {string} streamId
   */
  async function refreshStreamKey(streamId) {
    try {
      refreshingKey = true;
      const response = await streamAPI.refreshStreamKey(streamId);

      if (response.data) {
        streams = streams.map((stream) =>
          stream.id === streamId
            ? { ...stream, streamKey: response.data.streamKey }
            : stream
        );
        toast.success("Stream key refreshed successfully! Please update your streaming software with the new key.");
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
  $: primaryStream = streams.length > 0 ? streams[0] : null;

  // Reactive URLs based on selected stream
  $: ingestUrls = selectedStream ? getIngestUrls(selectedStream.streamKey) : {};
  $: deliveryUrls = selectedStream
    ? getDeliveryUrls(selectedStream.playbackId)
    : {};

  // Stream status for selected stream
  $: streamStatus = selectedStream?.status || "offline";
  $: isLive = streamStatus === "live";
  $: viewers = selectedStream?.viewers || realTimeMetrics.viewers || 0;

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
</script>

<svelte:head>
  <title>Streams - FrameWorks</title>
</svelte:head>

<style>
  .tab-button {
    display: flex;
    align-items: center;
    padding: 0.5rem 1rem;
    font-size: 0.875rem;
    font-weight: 500;
    color: var(--tokyo-night-fg-dark);
    border-bottom: 2px solid transparent;
    transition: color 0.2s, border-color 0.2s;
  }
  
  .tab-button:hover {
    color: var(--tokyo-night-fg);
    border-bottom-color: var(--tokyo-night-cyan);
  }
  
  .tab-active {
    color: var(--tokyo-night-cyan);
    border-bottom-color: var(--tokyo-night-cyan);
  }
</style>

<div class="space-y-8 page-transition">
  <!-- Page Header -->
  <div class="flex justify-between items-start">
    <div>
      <h1 class="text-3xl font-bold text-tokyo-night-fg mb-2">
        Stream Management
      </h1>
      <p class="text-tokyo-night-fg-dark">
        Manage your live streams, view analytics, and configure ingest/delivery settings
      </p>
    </div>

    <div class="flex space-x-3">
      <button 
        class="btn-secondary"
        on:click={() => showCreateModal = true}
      >
        <svelte:component this={getIconComponent('Plus')} class="w-4 h-4 mr-2" />
        Create Stream
      </button>
      <a href="{base}/analytics" class="btn-secondary">
        <svelte:component this={getIconComponent('BarChart3')} class="w-4 h-4 mr-2" />
        View Analytics
      </a>
      <button class="btn-primary cursor-not-allowed" disabled>
        <svelte:component this={getIconComponent('Play')} class="w-4 h-4 mr-2 text-tokyo-night-red" />
        Go Live
      </button>
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
        {#each Array(6) as _, i}
          <LoadingCard variant="stream" />
        {/each}
      </div>
    </div>

    <!-- Loading Skeleton for Stream Details -->
    <div class="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-6 gap-6 mb-8">
      {#each Array(6) as _}
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
        onAction={() => showCreateModal = true}
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

      <div class="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-4">
        {#each streams as stream}
          <div 
            class="bg-tokyo-night-bg-highlight p-4 rounded-lg border border-tokyo-night-fg-gutter cursor-pointer transition-all hover:border-tokyo-night-cyan {selectedStream?.id === stream.id ? 'border-tokyo-night-cyan bg-tokyo-night-bg-highlight' : ''}"
            role="button"
            tabindex="0"
            on:click={() => selectStream(stream)}
            on:keydown={(e) => e.key === 'Enter' && selectStream(stream)}
          >
            <div class="flex items-center justify-between mb-3">
              <h3 class="font-semibold text-tokyo-night-fg truncate">
                {stream.name || `Stream ${stream.id.slice(0, 8)}`}
              </h3>
              <div class="flex items-center space-x-2">
                <div class="w-2 h-2 rounded-full {stream.status === 'live' ? 'bg-tokyo-night-green animate-pulse' : 'bg-tokyo-night-red'}"></div>
                {#if stream.status === 'live'}
                  <a
                    href="{base}/view?type=live&id={stream.playbackId || stream.id}"
                    class="text-tokyo-night-cyan hover:text-tokyo-night-blue text-sm p-1"
                    on:click|stopPropagation
                    title="Watch live stream"
                  >
                    <svelte:component this={getIconComponent('Play')} class="w-4 h-4" />
                  </a>
                {/if}
                <button
                  class="text-tokyo-night-red hover:text-red-400 text-sm"
                  on:click|stopPropagation={() => confirmDeleteStream(stream)}
                  disabled={deletingStreamId === stream.id}
                >
                  {#if deletingStreamId === stream.id}
                    ...
                  {:else}
                    <svelte:component this={getIconComponent('X')} class="w-4 h-4" />
                  {/if}
                </button>
              </div>
            </div>
            
            <div class="grid grid-cols-2 gap-4 text-sm mb-3">
              <div>
                <p class="text-tokyo-night-comment">Status</p>
                <p class="font-semibold text-tokyo-night-fg capitalize">{stream.status || 'offline'}</p>
              </div>
              <div>
                <p class="text-tokyo-night-comment">Viewers</p>
                <p class="font-semibold text-tokyo-night-fg">{stream.viewers || 0}</p>
              </div>
            </div>

            <!-- Health Indicator -->
            {#if streamHealthData.has(stream.id)}
              {@const health = streamHealthData.get(stream.id)}
              <div class="mb-3">
                <div class="flex items-center justify-between">
                  <div class="flex items-center space-x-2">
                    <HealthScoreIndicator 
                      healthScore={health.healthScore} 
                      size="sm" 
                      showLabel={false}
                    />
                    <span class="text-xs text-tokyo-night-comment">Health</span>
                  </div>
                  <a 
                    href="{base}/streams/{stream.id}/health"
                    class="text-xs text-tokyo-night-cyan hover:text-tokyo-night-blue transition-colors"
                    on:click|stopPropagation
                  >
                    View Details
                  </a>
                </div>
                {#if health.issuesDescription}
                  <p class="text-xs text-red-400 mt-1 truncate">{health.issuesDescription}</p>
                {/if}
              </div>
            {:else if stream.status === 'live'}
              <div class="mb-3">
                <div class="flex items-center justify-between">
                  <span class="text-xs text-tokyo-night-comment">Loading health...</span>
                  <a 
                    href="{base}/streams/{stream.id}/health"
                    class="text-xs text-tokyo-night-cyan hover:text-tokyo-night-blue transition-colors"
                    on:click|stopPropagation
                  >
                    View Details
                  </a>
                </div>
              </div>
            {/if}

            <div class="pt-3 border-t border-tokyo-night-fg-gutter">
              <p class="text-xs text-tokyo-night-comment truncate">
                ID: {stream.playbackId || stream.id.slice(0, 16)}
              </p>
            </div>
          </div>
        {/each}
      </div>
    </div>

    {#if selectedStream}
      <!-- Stream Status Cards for Selected Stream -->
      <div class="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-6 gap-6 mb-8">
        <div class="glow-card p-6">
          <div class="flex items-center justify-between">
            <div>
              <p class="text-sm text-tokyo-night-comment">Stream Status</p>
              <p
                class="text-2xl font-bold {isLive
                  ? 'text-tokyo-night-green'
                  : 'text-tokyo-night-red'}"
              >
                {isLive ? "Live" : "Offline"}
              </p>
            </div>
            <div
              class="w-3 h-3 {isLive
                ? 'bg-tokyo-night-green animate-pulse'
                : 'bg-tokyo-night-red'} rounded-full"
            />
          </div>
        </div>

        <div class="glow-card p-6">
          <div class="flex items-center justify-between">
            <div>
              <p class="text-sm text-tokyo-night-comment">Current Viewers</p>
              <p class="text-2xl font-bold text-tokyo-night-fg">{viewers}</p>
            </div>
            <svelte:component this={getIconComponent('Users')} class="w-6 h-6" />
          </div>
        </div>

        <div class="glow-card p-6">
          <div class="flex items-center justify-between">
            <div>
              <p class="text-sm text-tokyo-night-comment">Bandwidth</p>
              <p class="text-sm font-bold text-tokyo-night-fg">
                {formatBandwidth(realTimeMetrics.bandwidth)}
              </p>
            </div>
            <svelte:component this={getIconComponent('BarChart3')} class="w-8 h-8 text-tokyo-night-blue" />
          </div>
        </div>

        <div class="glow-card p-6">
          <div class="flex items-center justify-between">
            <div>
              <p class="text-sm text-tokyo-night-comment">Resolution</p>
              <p class="text-lg font-bold text-tokyo-night-fg">
                {selectedStream?.resolution || "N/A"}
              </p>
            </div>
            <svelte:component this={getIconComponent('Monitor')} class="w-8 h-8 text-tokyo-night-purple" />
          </div>
        </div>

        <div class="glow-card p-6">
          <div class="flex items-center justify-between">
            <div>
              <p class="text-sm text-tokyo-night-comment">Stream Key</p>
              <p class="text-sm font-mono text-tokyo-night-fg">
                {selectedStream?.streamKey
                  ? `${selectedStream.streamKey.slice(0, 8)}...`
                  : "No stream"}
              </p>
            </div>
            <svelte:component this={getIconComponent('Key')} class="w-8 h-8 text-tokyo-night-yellow" />
          </div>
        </div>

        <div class="glow-card p-6">
          <div class="flex items-center justify-between">
            <div>
              <p class="text-sm text-tokyo-night-comment">Playback ID</p>
              <p class="text-sm font-mono text-tokyo-night-fg">
                {selectedStream?.playbackId
                  ? `${selectedStream.playbackId.slice(0, 8)}...`
                  : "No stream"}
              </p>
            </div>
            <svelte:component this={getIconComponent('Play')} class="w-8 h-8 text-tokyo-night-green" />
          </div>
        </div>
      </div>

      {#if realTimeMetrics.timestamp && isLive}
        <div class="text-center">
          <p class="text-xs text-tokyo-night-comment">
            Last updated: {new Date(realTimeMetrics.timestamp).toLocaleTimeString()}
          </p>
        </div>
      {/if}

      <!-- Stream Configuration Tabs -->
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
          
          <!-- Tab Navigation -->
          <div class="flex border-b border-tokyo-night-fg-gutter">
            <button
              class="tab-button {activeTab === 'overview' ? 'tab-active' : ''}"
              on:click={() => switchTab('overview')}
            >
              <svelte:component this={getIconComponent('Settings')} class="w-4 h-4 mr-2" />
              Overview
            </button>
            <button
              class="tab-button {activeTab === 'keys' ? 'tab-active' : ''}"
              on:click={() => switchTab('keys')}
            >
              <svelte:component this={getIconComponent('Key')} class="w-4 h-4 mr-2" />
              Stream Keys
            </button>
            <button
              class="tab-button {activeTab === 'recordings' ? 'tab-active' : ''}"
              on:click={() => switchTab('recordings')}
            >
              <svelte:component this={getIconComponent('Video')} class="w-4 h-4 mr-2" />
              Recordings
            </button>
          </div>
        </div>

        <!-- Tab Content -->
        {#if activeTab === 'overview'}
          <!-- Overview Tab Content -->
          <div class="grid grid-cols-1 xl:grid-cols-2 gap-8">
        <!-- Ingest Configuration -->
        <div class="card">
          <div class="card-header">
            <h2 class="text-xl font-semibold text-tokyo-night-fg mb-2">
Stream Ingest
            </h2>
            <p class="text-tokyo-night-fg-dark">
              Configure your streaming software to broadcast to these endpoints
            </p>
          </div>

          <div class="space-y-6">
            <!-- Stream Key Section -->
            <div
              class="bg-tokyo-night-bg-highlight p-4 rounded-lg border border-tokyo-night-fg-gutter"
            >
              <div class="flex items-center justify-between mb-3">
                <label for="stream-key-input" class="block text-sm font-medium text-tokyo-night-fg"
                  >Stream Key</label
                >
                <button
                  on:click={() => refreshStreamKey(selectedStream?.id)}
                  class="btn-secondary text-xs px-3 py-1"
                  disabled={refreshingKey}
                >
                  {#if refreshingKey}
          Refreshing...
        {:else}
          <svelte:component this={getIconComponent('RefreshCw')} class="w-4 h-4 mr-1" />
          Regenerate
        {/if}
                </button>
              </div>
              <div class="flex items-center space-x-3">
                <input
                  id="stream-key-input"
                  type="text"
                  value={selectedStream?.streamKey || "Loading..."}
                  readonly
                  class="input flex-1 font-mono text-sm"
                />
                <button
                  on:click={() => copyToClipboard(selectedStream?.streamKey)}
                  class="btn-secondary"
                  disabled={!selectedStream?.streamKey}
                >
                  {#if copiedUrl === selectedStream?.streamKey}
                    <svelte:component this={getIconComponent('CheckCircle')} class="w-4 h-4" />
                  {:else}
                    <svelte:component this={getIconComponent('Copy')} class="w-4 h-4" />
                  {/if}
                </button>
              </div>
              <p class="text-xs text-tokyo-night-comment mt-2">
                Keep your stream key private. Anyone with this key can broadcast
                to your channel.
              </p>
            </div>

            <!-- Ingest Protocols -->
            {#each ingestProtocols as protocol}
              <div class="border border-tokyo-night-fg-gutter rounded-lg p-4">
                <div class="flex items-center justify-between mb-3">
                  <div class="flex items-center space-x-3">
                    <svelte:component this={getIconComponent(protocol.icon)} class="w-5 h-5 text-tokyo-night-cyan" />
                    <div>
                      <h3
                        class="font-semibold text-tokyo-night-fg flex items-center space-x-2"
                      >
                        <span>{protocol.name}</span>
                        {#if protocol.recommended}
                          <span class="badge-success text-xs">Recommended</span>
                        {/if}
                      </h3>
                      <p class="text-xs text-tokyo-night-comment">
                        {protocol.description}
                      </p>
                    </div>
                  </div>
                </div>

                <div class="flex items-center space-x-3 mb-2">
                  <input
                    type="text"
                    value={ingestUrls[protocol.key] || "Stream key required"}
                    readonly
                    class="input flex-1 font-mono text-sm"
                  />
                  <button
                    on:click={() =>
                      copyToClipboard(ingestUrls[protocol.key] || "")}
                    class="btn-secondary"
                    disabled={!ingestUrls[protocol.key]}
                  >
                    {#if copiedUrl === ingestUrls[protocol.key]}
                      <svelte:component this={getIconComponent('CheckCircle')} class="w-4 h-4" />
                    {:else}
                      <svelte:component this={getIconComponent('Copy')} class="w-4 h-4" />
                    {/if}
                  </button>
                </div>

                <p class="text-xs text-tokyo-night-comment">
                  {protocol.setup}
                </p>
              </div>
            {/each}
          </div>
        </div>

        <!-- Delivery Configuration -->
        <div class="card">
          <div class="card-header">
            <h2 class="text-xl font-semibold text-tokyo-night-fg mb-2">
Stream Delivery
            </h2>
            <p class="text-tokyo-night-fg-dark">
              Multiple playback options for viewers and applications
            </p>
          </div>

          <div class="space-y-4">
            {#each deliveryProtocols as protocol}
              <div class="border border-tokyo-night-fg-gutter rounded-lg p-4">
                <div class="flex items-center justify-between mb-3">
                  <div class="flex items-center space-x-3">
                    <svelte:component this={getIconComponent(protocol.icon)} class="w-5 h-5 text-tokyo-night-cyan" />
                    <div>
                      <h3
                        class="font-semibold text-tokyo-night-fg flex items-center space-x-2"
                      >
                        <span>{protocol.name}</span>
                        {#if protocol.recommended}
                          <span class="badge-success text-xs">Recommended</span>
                        {/if}
                      </h3>
                      <p class="text-xs text-tokyo-night-comment">
                        {protocol.description}
                      </p>
                    </div>
                  </div>
                </div>

                {#if protocol.key === "embed"}
                  <!-- Special handling for embed code -->
                  <div class="space-y-2">
                    <textarea
                      readonly
                      value={`<iframe src="${
                        deliveryUrls[protocol.key] || ""
                      }" frameborder="0" allowfullscreen></iframe>`}
                      class="input w-full font-mono text-sm h-20 resize-none"
                    />
                    <button
                      on:click={() =>
                        copyToClipboard(
                          `<iframe src="${
                            deliveryUrls[protocol.key] || ""
                          }" frameborder="0" allowfullscreen></iframe>`
                        )}
                      class="btn-secondary w-full"
                      disabled={!deliveryUrls[protocol.key]}
                    >
                      {copiedUrl.includes(deliveryUrls[protocol.key])
                        ? "✓ Copied"
                        : "Copy Embed Code"}
                    </button>
                  </div>
                {:else}
                  <!-- Regular URL display -->
                  <div class="flex items-center space-x-3">
                    <input
                      type="text"
                      value={deliveryUrls[protocol.key] || "Playback ID required"}
                      readonly
                      class="input flex-1 font-mono text-sm"
                    />
                    <button
                      on:click={() =>
                        copyToClipboard(deliveryUrls[protocol.key] || "")}
                      class="btn-secondary"
                      disabled={!deliveryUrls[protocol.key]}
                    >
                      {#if copiedUrl === deliveryUrls[protocol.key]}
                        <svelte:component this={getIconComponent('CheckCircle')} class="w-4 h-4" />
                      {:else}
                        <svelte:component this={getIconComponent('Copy')} class="w-4 h-4" />
                      {/if}
                    </button>
                  </div>
                {/if}
              </div>
            {/each}
          </div>
        </div>
      </div>

      <!-- Quick Setup Guide -->
      <div class="card">
        <div class="card-header">
          <h2 class="text-xl font-semibold text-tokyo-night-fg mb-2">
Quick Setup Guide
          </h2>
          <p class="text-tokyo-night-fg-dark">Get started streaming in minutes</p>
        </div>

        <div class="grid grid-cols-1 md:grid-cols-3 gap-6">
          <div class="text-center">
            <div class="text-3xl mb-3">
              <svelte:component this={getIconComponent('Monitor')} class="w-8 h-8 text-tokyo-night-purple mx-auto" />
            </div>
            <h3 class="font-semibold text-tokyo-night-fg mb-2">
              1. Configure Software
            </h3>
            <p class="text-sm text-tokyo-night-comment">
              Copy the RTMP URL into OBS Studio, XSplit, or your preferred
              streaming software
            </p>
          </div>

          <div class="text-center">
            <div class="text-3xl mb-3">
              <svelte:component this={getIconComponent('Key')} class="w-8 h-8 text-tokyo-night-yellow mx-auto" />
            </div>
            <h3 class="font-semibold text-tokyo-night-fg mb-2">
              2. Add Stream Key
            </h3>
            <p class="text-sm text-tokyo-night-comment">
              Paste your unique stream key to authenticate your broadcast
            </p>
          </div>

          <div class="text-center">
            <div class="text-3xl mb-3">
              <svelte:component this={getIconComponent('Video')} class="w-8 h-8 text-tokyo-night-cyan mx-auto" />
            </div>
            <h3 class="font-semibold text-tokyo-night-fg mb-2">
              3. Start Streaming
            </h3>
            <p class="text-sm text-tokyo-night-comment">
              Hit "Start Streaming" and share your HLS playback URL with viewers
            </p>
          </div>
        </div>
      </div>

        {:else if activeTab === 'keys'}
          <!-- Stream Keys Tab Content -->
          <div class="space-y-6">
            <div class="flex items-center justify-between">
              <div>
                <h3 class="text-lg font-semibold text-tokyo-night-fg">Stream Keys Management</h3>
                <p class="text-tokyo-night-fg-dark text-sm">Create and manage multiple stream keys for different streaming setups</p>
              </div>
              <button
                class="btn-primary"
                on:click={() => showCreateKeyModal = true}
              >
                <svelte:component this={getIconComponent('Plus')} class="w-4 h-4 mr-2" />
                Create Key
              </button>
            </div>

            {#if loadingStreamKeys}
              <div class="space-y-4">
                {#each Array(3) as _}
                  <LoadingCard variant="stream" />
                {/each}
              </div>
            {:else if streamKeys.length === 0}
              <EmptyState
                iconName="Key"
                title="No Stream Keys"
                description="Create your first stream key to start broadcasting"
                actionText="Create Stream Key"
                onAction={() => showCreateKeyModal = true}
              />
            {:else}
              <div class="space-y-4">
                {#each streamKeys as key}
                  <div class="bg-tokyo-night-bg-highlight p-4 rounded-lg border border-tokyo-night-fg-gutter">
                    <div class="flex items-center justify-between mb-3">
                      <div class="flex items-center space-x-3">
                        <svelte:component this={getIconComponent('Key')} class="w-5 h-5 text-tokyo-night-yellow" />
                        <div>
                          <h4 class="font-semibold text-tokyo-night-fg">
                            {key.keyName || 'Unnamed Key'}
                          </h4>
                          <p class="text-xs text-tokyo-night-comment">
                            Created {new Date(key.createdAt).toLocaleDateString()}
                          </p>
                        </div>
                      </div>
                      
                      <div class="flex items-center space-x-2">
                        <span class="px-2 py-1 text-xs rounded {key.isActive ? 'bg-tokyo-night-green bg-opacity-20 text-tokyo-night-green' : 'bg-tokyo-night-red bg-opacity-20 text-tokyo-night-red'}">
                          {key.isActive ? 'Active' : 'Inactive'}
                        </span>
                        <button
                          class="text-tokyo-night-red hover:text-red-400 p-1"
                          on:click={() => deleteStreamKey(key.id)}
                          disabled={deletingKeyId === key.id}
                        >
                          {#if deletingKeyId === key.id}
                            <svelte:component this={getIconComponent('Loader2')} class="w-4 h-4 animate-spin" />
                          {:else}
                            <svelte:component this={getIconComponent('Trash2')} class="w-4 h-4" />
                          {/if}
                        </button>
                      </div>
                    </div>

                    <div class="flex items-center space-x-3">
                      <input
                        type="text"
                        value={key.keyValue}
                        readonly
                        class="input flex-1 font-mono text-sm"
                      />
                      <button
                        on:click={() => copyToClipboard(key.keyValue)}
                        class="btn-secondary"
                      >
                        {#if copiedUrl === key.keyValue}
                          <svelte:component this={getIconComponent('CheckCircle')} class="w-4 h-4" />
                        {:else}
                          <svelte:component this={getIconComponent('Copy')} class="w-4 h-4" />
                        {/if}
                      </button>
                    </div>

                    {#if key.lastUsedAt}
                      <p class="text-xs text-tokyo-night-comment mt-2">
                        Last used: {new Date(key.lastUsedAt).toLocaleString()}
                      </p>
                    {/if}
                  </div>
                {/each}
              </div>
            {/if}
          </div>

        {:else if activeTab === 'recordings'}
          <!-- DVR Recordings Tab Content -->
          <div class="space-y-6">
            <div class="flex items-center justify-between">
              <div>
                <h3 class="text-lg font-semibold text-tokyo-night-fg">DVR Recordings</h3>
                <p class="text-tokyo-night-fg-dark text-sm">Start recording and manage DVR sessions for this stream</p>
              </div>
              
              <div class="flex space-x-3">
                {#if isLive}
                  <button
                    class="btn-primary"
                    on:click={startDVRRecording}
                    disabled={startingDVR}
                  >
                    {#if startingDVR}
                      <svelte:component this={getIconComponent('Loader2')} class="w-4 h-4 mr-2 animate-spin" />
                      Starting...
                    {:else}
                      <svelte:component this={getIconComponent('Video')} class="w-4 h-4 mr-2" />
                      Start Recording
                    {/if}
                  </button>
                {:else}
                  <div class="text-sm text-tokyo-night-comment px-4 py-2 bg-tokyo-night-bg-dark rounded-lg">
                    Stream must be live to start recording
                  </div>
                {/if}
              </div>
            </div>

            {#if loadingRecordings}
              <div class="space-y-4">
                {#each Array(3) as _}
                  <LoadingCard variant="stream" />
                {/each}
              </div>
            {:else if streamRecordings.length === 0}
              <EmptyState
                iconName="Video"
                title="No Recordings Yet"
                description={isLive ? "Start your first DVR recording above" : "Start streaming, then create DVR recordings"}
                actionText={isLive ? "Start Recording" : ""}
                onAction={isLive ? startDVRRecording : undefined}
              />
            {:else}
              <div class="space-y-4">
                {#each streamRecordings as recording}
                  <div class="bg-tokyo-night-bg-highlight p-4 rounded-lg border border-tokyo-night-fg-gutter">
                    <div class="flex items-center justify-between mb-3">
                      <div class="flex items-center space-x-3">
                        <svelte:component this={getIconComponent('Video')} class="w-5 h-5 text-tokyo-night-cyan" />
                        <div>
                          <h4 class="font-semibold text-tokyo-night-fg">
                            DVR Recording
                          </h4>
                          <p class="text-xs text-tokyo-night-comment">
                            {recording.createdAt ? new Date(recording.createdAt).toLocaleString() : 'N/A'}
                          </p>
                        </div>
                      </div>
                      
                      <div class="flex items-center space-x-2">
                        <span class="px-2 py-1 text-xs rounded bg-tokyo-night-bg-dark {recording.status === 'completed' ? 'text-tokyo-night-green' : recording.status === 'recording' ? 'text-tokyo-night-yellow' : recording.status === 'failed' ? 'text-tokyo-night-red' : 'text-tokyo-night-fg-dark'}">
                          {recording.status === 'recording' ? '● Recording' : recording.status}
                        </span>
                        
                        {#if recording.status === 'recording'}
                          <button
                            class="text-tokyo-night-red hover:text-red-400 p-1"
                            on:click={() => stopDVRRecording(recording.dvrHash)}
                            disabled={stoppingDVR}
                          >
                            {#if stoppingDVR}
                              <svelte:component this={getIconComponent('Loader2')} class="w-4 h-4 animate-spin" />
                            {:else}
                              <svelte:component this={getIconComponent('Square')} class="w-4 h-4" />
                            {/if}
                          </button>
                        {/if}
                      </div>
                    </div>

                    <div class="grid grid-cols-2 md:grid-cols-4 gap-4 text-sm mb-3">
                      <div>
                        <p class="text-tokyo-night-comment">Duration</p>
                        <p class="font-medium text-tokyo-night-fg">
                          {recording.durationSeconds ? Math.floor(recording.durationSeconds / 60) + 'm' : 'N/A'}
                        </p>
                      </div>
                      <div>
                        <p class="text-tokyo-night-comment">Size</p>
                        <p class="font-medium text-tokyo-night-fg">
                          {recording.sizeBytes ? (recording.sizeBytes / (1024 * 1024)).toFixed(1) + ' MB' : 'N/A'}
                        </p>
                      </div>
                      <div>
                        <p class="text-tokyo-night-comment">Storage Node</p>
                        <p class="font-medium text-tokyo-night-fg truncate">
                          {recording.storageNodeId || 'N/A'}
                        </p>
                      </div>
                      <div>
                        <p class="text-tokyo-night-comment">Format</p>
                        <p class="font-medium text-tokyo-night-fg">
                          HLS
                        </p>
                      </div>
                    </div>

                    {#if recording.manifestPath}
                      <div class="flex items-center space-x-3">
                        <input
                          type="text"
                          value={recording.manifestPath}
                          readonly
                          class="input flex-1 font-mono text-sm"
                        />
                        <button
                          on:click={() => copyToClipboard(recording.manifestPath)}
                          class="btn-secondary"
                        >
                          {#if copiedUrl === recording.manifestPath}
                            <svelte:component this={getIconComponent('CheckCircle')} class="w-4 h-4" />
                          {:else}
                            <svelte:component this={getIconComponent('Copy')} class="w-4 h-4" />
                          {/if}
                        </button>
                        {#if recording.status === 'completed'}
                          <a
                            href="{base}/view?type=dvr&id={recording.dvrHash || recording.id}"
                            class="btn-primary"
                            title="Watch DVR recording"
                          >
                            <svelte:component this={getIconComponent('Play')} class="w-4 h-4" />
                          </a>
                        {/if}
                      </div>
                    {:else}
                      <div class="flex items-center space-x-3">
                        <input
                          type="text"
                          value={recording.dvrHash}
                          readonly
                          class="input flex-1 font-mono text-sm"
                        />
                        <button
                          on:click={() => copyToClipboard(recording.dvrHash)}
                          class="btn-secondary"
                        >
                          {#if copiedUrl === recording.dvrHash}
                            <svelte:component this={getIconComponent('CheckCircle')} class="w-4 h-4" />
                          {:else}
                            <svelte:component this={getIconComponent('Copy')} class="w-4 h-4" />
                          {/if}
                        </button>
                      </div>
                    {/if}

                    {#if recording.errorMessage}
                      <p class="text-xs text-tokyo-night-red mt-2">
                        Error: {recording.errorMessage}
                      </p>
                    {/if}
                  </div>
                {/each}
              </div>
            {/if}
          </div>
        {/if}
      </div>

      <!-- Quick Setup Guide -->
      <div class="card">
        <div class="card-header">
          <h2 class="text-xl font-semibold text-tokyo-night-fg mb-2">
            Quick Setup Guide
          </h2>
          <p class="text-tokyo-night-fg-dark">Get started streaming in minutes</p>
        </div>

        <div class="grid grid-cols-1 md:grid-cols-3 gap-6">
          <div class="text-center">
            <div class="text-3xl mb-3">
              <svelte:component this={getIconComponent('Monitor')} class="w-8 h-8 text-tokyo-night-purple mx-auto" />
            </div>
            <h3 class="font-semibold text-tokyo-night-fg mb-2">
              1. Configure Software
            </h3>
            <p class="text-sm text-tokyo-night-comment">
              Copy the RTMP URL into OBS Studio, XSplit, or your preferred
              streaming software
            </p>
          </div>

          <div class="text-center">
            <div class="text-3xl mb-3">
              <svelte:component this={getIconComponent('Key')} class="w-8 h-8 text-tokyo-night-yellow mx-auto" />
            </div>
            <h3 class="font-semibold text-tokyo-night-fg mb-2">
              2. Add Stream Key
            </h3>
            <p class="text-sm text-tokyo-night-comment">
              Paste your unique stream key to authenticate your broadcast
            </p>
          </div>

          <div class="text-center">
            <div class="text-3xl mb-3">
              <svelte:component this={getIconComponent('Video')} class="w-8 h-8 text-tokyo-night-cyan mx-auto" />
            </div>
            <h3 class="font-semibold text-tokyo-night-fg mb-2">
              3. Start Streaming
            </h3>
            <p class="text-sm text-tokyo-night-comment">
              Hit "Start Streaming" and share your HLS playback URL with viewers
            </p>
          </div>
        </div>
      </div>
    {/if}
  {/if}
</div>

<!-- Create Stream Key Modal -->
{#if showCreateKeyModal}
  <div class="fixed inset-0 bg-black bg-opacity-50 flex items-center justify-center z-50">
    <div class="bg-tokyo-night-bg-light p-6 rounded-lg border border-tokyo-night-fg-gutter max-w-md w-full mx-4">
      <h3 class="text-xl font-semibold text-tokyo-night-fg mb-4">Create Stream Key</h3>
      
      <div class="space-y-4">
        <div>
          <label for="key-name" class="block text-sm font-medium text-tokyo-night-fg-dark mb-2">
            Key Name *
          </label>
          <input
            id="key-name"
            type="text"
            bind:value={newKeyName}
            placeholder="Production Key"
            class="input w-full"
            disabled={creatingStreamKey}
          />
          <p class="text-xs text-tokyo-night-comment mt-1">
            Give your stream key a descriptive name to identify its purpose
          </p>
        </div>
      </div>
      
      <div class="flex justify-end space-x-3 mt-6">
        <button
          class="btn-secondary"
          on:click={() => {
            showCreateKeyModal = false;
            newKeyName = '';
          }}
          disabled={creatingStreamKey}
        >
          Cancel
        </button>
        <button
          class="btn-primary"
          on:click={createStreamKey}
          disabled={creatingStreamKey || !newKeyName.trim()}
        >
          {creatingStreamKey ? 'Creating...' : 'Create Key'}
        </button>
      </div>
    </div>
  </div>
{/if}

<!-- Create Stream Modal -->
{#if showCreateModal}
  <div class="fixed inset-0 bg-black bg-opacity-50 flex items-center justify-center z-50">
    <div class="bg-tokyo-night-bg-light p-6 rounded-lg border border-tokyo-night-fg-gutter max-w-md w-full mx-4">
      <h3 class="text-xl font-semibold text-tokyo-night-fg mb-4">Create New Stream</h3>
      
      <div class="space-y-4">
        <div>
          <label for="stream-title" class="block text-sm font-medium text-tokyo-night-fg-dark mb-2">
            Stream Title *
          </label>
          <input
            id="stream-title"
            type="text"
            bind:value={newStreamTitle}
            placeholder="My Awesome Stream"
            class="input w-full"
            disabled={creatingStream}
          />
        </div>
        
        <div>
          <label for="stream-description" class="block text-sm font-medium text-tokyo-night-fg-dark mb-2">
            Description (Optional)
          </label>
          <textarea
            id="stream-description"
            bind:value={newStreamDescription}
            placeholder="Description of your stream..."
            class="input w-full h-20 resize-none"
            disabled={creatingStream}
          />
        </div>
        
        <div>
          <label class="flex items-center space-x-3 cursor-pointer">
            <input
              type="checkbox"
              bind:checked={newStreamRecord}
              disabled={creatingStream}
              class="w-4 h-4 text-tokyo-night-blue bg-tokyo-night-bg border border-tokyo-night-fg-gutter rounded focus:ring-tokyo-night-blue focus:ring-2"
            />
            <div>
              <span class="text-sm font-medium text-tokyo-night-fg">Enable Recording</span>
              <p class="text-xs text-tokyo-night-comment">
                Automatically record your stream to create VOD content
              </p>
            </div>
          </label>
        </div>
      </div>
      
      <div class="flex justify-end space-x-3 mt-6">
        <button
          class="btn-secondary"
          on:click={() => {
            showCreateModal = false;
            newStreamTitle = '';
            newStreamDescription = '';
            newStreamRecord = false;
          }}
          disabled={creatingStream}
        >
          Cancel
        </button>
        <button
          class="btn-primary"
          on:click={createStream}
          disabled={creatingStream || !newStreamTitle.trim()}
        >
          {creatingStream ? 'Creating...' : 'Create Stream'}
        </button>
      </div>
    </div>
  </div>
{/if}

<!-- Delete Stream Modal -->
{#if showDeleteModal && streamToDelete}
  <div class="fixed inset-0 bg-black bg-opacity-50 flex items-center justify-center z-50">
    <div class="bg-tokyo-night-bg-light p-6 rounded-lg border border-tokyo-night-fg-gutter max-w-md w-full mx-4">
      <h3 class="text-xl font-semibold text-tokyo-night-red mb-4">Delete Stream</h3>
      
      <p class="text-tokyo-night-fg-dark mb-6">
        Are you sure you want to delete the stream 
        <span class="font-semibold text-tokyo-night-fg">"{streamToDelete.name || `Stream ${streamToDelete.id.slice(0, 8)}`}"</span>?
        This action cannot be undone.
      </p>
      
      <div class="flex justify-end space-x-3">
        <button
          class="btn-secondary"
          on:click={() => {
            showDeleteModal = false;
            streamToDelete = null;
          }}
          disabled={!!deletingStreamId}
        >
          Cancel
        </button>
        <button
          class="btn-danger"
          on:click={deleteStream}
          disabled={!!deletingStreamId}
        >
          {deletingStreamId ? 'Deleting...' : 'Delete Stream'}
        </button>
      </div>
    </div>
  </div>
{/if}

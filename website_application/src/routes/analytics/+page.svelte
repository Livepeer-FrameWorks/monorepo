<script>
  import { onMount, onDestroy } from "svelte";
  import { auth } from "$lib/stores/auth";
  import { streamAPI, analyticsAPIFunctions } from "$lib/api";
  import { realtimeStreams, streamMetrics as rtStreamMetrics, connectionStatus } from "$lib/stores/realtime";

  let isAuthenticated = false;
  /** @type {any} */
  let user = null;
  /** @type {any[]} */
  let streams = [];
  /** @type {any[]} */
  let streamMetrics = [];
  let loading = true;
  /** @type {any} */
  let selectedStream = null;
  /** @type {number | null} */
  let refreshInterval = null;
  
  // Real-time data subscriptions
  let realtimeData = [];
  let realtimeMetrics = {};
  let connectionState = { status: 'disconnected', message: 'Disconnected' };

  // Real analytics data (not hardcoded)
  /** @type {any} */
  let analyticsData = {
    viewers: 0,
    maxViewers: 0,
    status: "offline",
    bandwidth_in: 0,
    bandwidth_out: 0,
    resolution: null,
    bitrate: null,
    tracks: {},
    uptime: 0,
    lastUpdated: null,
  };

  // Subscribe to auth store
  auth.subscribe((/** @type {any} */ authState) => {
    isAuthenticated = authState.isAuthenticated;
    user = authState.user?.user || null;
    streams = authState.user?.streams || [];
  });

  // Subscribe to real-time data
  realtimeStreams.subscribe(data => {
    realtimeData = data;
    // Merge real-time data with current analytics if available
    if (selectedStream && realtimeData.length > 0) {
      const rtStream = realtimeData.find(s => s.stream_id === selectedStream.id);
      if (rtStream) {
        analyticsData = {
          ...analyticsData,
          viewers: rtStream.viewers || analyticsData.viewers,
          status: rtStream.status || analyticsData.status,
          lastUpdated: new Date()
        };
      }
    }
  });

  rtStreamMetrics.subscribe(data => {
    realtimeMetrics = data;
    // Update analytics with real-time metrics
    if (selectedStream && realtimeMetrics[selectedStream.id]) {
      const metrics = realtimeMetrics[selectedStream.id];
      analyticsData = {
        ...analyticsData,
        bandwidth_in: metrics.bandwidth_in || analyticsData.bandwidth_in,
        bandwidth_out: metrics.bandwidth_out || analyticsData.bandwidth_out,
        bitrate: metrics.bitrate_kbps ? `${metrics.bitrate_kbps} kbps` : analyticsData.bitrate,
        lastUpdated: new Date()
      };
    }
  });

  connectionStatus.subscribe(status => {
    connectionState = status;
  });

  onMount(async () => {
    if (!isAuthenticated) {
      await auth.checkAuth();
    }

    // Load streams first if not already loaded from auth store
    if (streams.length === 0) {
      await loadStreams();
    }

    if (streams.length > 0) {
      selectedStream = streams[0];
      await loadRealAnalytics();
      startAutoRefresh();
    }
    loading = false;
  });

  onDestroy(() => {
    if (refreshInterval) {
      clearInterval(refreshInterval);
    }
  });

  async function loadStreams() {
    try {
      const response = await streamAPI.getStreams();
      streams = response.data || [];
    } catch (error) {
      console.error("Failed to load streams:", error);
    }
  }

  async function loadRealAnalytics() {
    if (!selectedStream) {
      // If no specific stream is selected, aggregate data from all streams
      await loadStreamMetrics();
      if (streamMetrics.length > 0) {
        const aggregatedMetrics = streamMetrics.reduce(
          (acc, curr) => ({
            viewers: (acc.viewers || 0) + (curr.viewers || 0),
            maxViewers: Math.max(acc.maxViewers || 0, curr.max_viewers || 0),
            status: acc.status || curr.status,
            bandwidth_in: (acc.bandwidth_in || 0) + (curr.bandwidth_in || 0),
            bandwidth_out: (acc.bandwidth_out || 0) + (curr.bandwidth_out || 0),
            resolution: acc.resolution || curr.resolution,
            bitrate: acc.bitrate || curr.bitrate,
            tracks: { ...acc.tracks, ...curr.tracks },
            uptime: acc.uptime || 0,
            lastUpdated: acc.lastUpdated || curr.lastUpdated,
            source: acc.source || curr.source,
            title: acc.title || curr.title,
            internal_name: acc.internal_name || curr.internal_name,
            created_at: acc.created_at || curr.created_at,
          }),
          {}
        );

        analyticsData = {
          ...aggregatedMetrics,
          uptime: calculateUptime(aggregatedMetrics.created_at),
          lastUpdated: new Date(),
          source: "aggregated_data",
          title: "All Streams Overview",
          internal_name: "All Streams",
          created_at: aggregatedMetrics.created_at,
        };
      } else {
        analyticsData = {
          viewers: 0,
          maxViewers: 0,
          status: "offline",
          bandwidth_in: 0,
          bandwidth_out: 0,
          resolution: null,
          bitrate: null,
          tracks: {},
          uptime: 0,
          lastUpdated: null,
          source: "no_data",
          title: "No Streams Found",
          internal_name: "No Streams",
          created_at: null,
        };
      }
    } else {
      // If a specific stream is selected, load its analytics
      try {
        const [detailsResponse, eventsResponse, healthResponse] = await Promise.all([
          analyticsAPIFunctions.getStreamDetails(selectedStream.internal_name),
          analyticsAPIFunctions.getStreamEvents(selectedStream.internal_name),
          analyticsAPIFunctions.getStreamHealthMetrics({
            start_time: new Date(Date.now() - 24*60*60*1000).toISOString(), // Last 24h
            end_time: new Date().toISOString()
          })
        ]);

        const metrics = detailsResponse.data || {};
        const events = eventsResponse.data || [];
        const healthMetrics = healthResponse.data || [];

        console.log("Stream details from Periscope:", metrics);
        console.log("Stream events from Periscope:", events);
        console.log("Stream health metrics from ClickHouse:", healthMetrics);

        // Get the latest health metrics for this stream
        const streamHealthMetrics = healthMetrics.filter(m => 
          m.internal_name === (metrics.internal_name || selectedStream.internal_name)
        );
        const latestHealth = streamHealthMetrics.length > 0 ? streamHealthMetrics[0] : {};

        analyticsData = {
          viewers: metrics.current_viewers || 0,
          maxViewers: metrics.peak_viewers || 0,
          status: metrics.status || "offline",
          bandwidth_in: metrics.bandwidth_in || 0,
          bandwidth_out: metrics.bandwidth_out || 0,
          resolution: latestHealth.width && latestHealth.height ? `${latestHealth.width}x${latestHealth.height}` : metrics.resolution || null,
          bitrate: latestHealth.bitrate ? `${latestHealth.bitrate} kbps` : metrics.bitrate_kbps ? `${metrics.bitrate_kbps} kbps` : null,
          tracks: { 
            events, 
            healthMetrics: streamHealthMetrics,
            codec: latestHealth.codec,
            profile: latestHealth.profile,
            fps: latestHealth.fps,
            width: latestHealth.width,
            height: latestHealth.height,
            track_metadata: latestHealth.track_metadata
          },
          uptime: calculateUptime(
            metrics.session_start_time || selectedStream.created_at
          ),
          lastUpdated: new Date(),
          source: "periscope_query_with_health",
          title: selectedStream.title,
          internal_name: metrics.internal_name || selectedStream.internal_name,
          created_at: metrics.session_start_time || selectedStream.created_at,
        };
      } catch (error) {
        console.error("Failed to load analytics:", error);
      }
    }
  }

  function startAutoRefresh() {
    refreshInterval = setInterval(async () => {
      await loadRealAnalytics();
    }, 10000); // Refresh every 10 seconds
  }

  /**
   * @param {string} createdAt
   */
  function calculateUptime(createdAt) {
    if (!createdAt) return 0;
    const created = new Date(createdAt);
    const now = new Date();
    return Math.floor((now.getTime() - created.getTime()) / (1000 * 60)); // minutes
  }

  /**
   * @param {number} minutes
   */
  function formatUptime(minutes) {
    if (minutes < 60) return `${minutes}m`;
    const hours = Math.floor(minutes / 60);
    const mins = minutes % 60;
    return `${hours}h ${mins}m`;
  }

  /**
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

  async function loadStreamMetrics() {
    try {
      const response = await analyticsAPIFunctions.getStreamAnalytics();
      streamMetrics = response.data || [];
      console.log('Stream analytics from Periscope:', streamMetrics);
    } catch (error) {
      console.error('Failed to load stream analytics:', error);
      streamMetrics = [];
    }
  }
</script>

<svelte:head>
  <title>Analytics - FrameWorks</title>
</svelte:head>

<div class="space-y-8 page-transition">
  <!-- Page Header -->
  <div class="flex justify-between items-start">
    <div>
      <h1 class="text-3xl font-bold text-tokyo-night-fg mb-2">
        📊 Stream Analytics
      </h1>
      <p class="text-tokyo-night-fg-dark">
        Real-time metrics and performance data from your streams ({streams.length}
        total)
      </p>
    </div>

    <div class="flex items-center space-x-3">
      <!-- Real-time connection indicator -->
      <div class="flex items-center space-x-2 text-sm">
        <div class="flex items-center space-x-1">
          <div class="w-2 h-2 rounded-full {connectionState.status === 'connected' ? 'bg-tokyo-night-green animate-pulse' : connectionState.status === 'reconnecting' ? 'bg-tokyo-night-yellow animate-pulse' : 'bg-tokyo-night-red'}"></div>
          <span class="text-tokyo-night-comment">{connectionState.message}</span>
        </div>
      </div>
      
      {#if streams.length > 1}
        <select
          bind:value={selectedStream}
          on:change={loadRealAnalytics}
          class="input"
        >
          <option value={null}>All Streams Overview</option>
          {#each streams as stream}
            <option value={stream}
              >{stream.title || `Stream ${stream.id.slice(0, 8)}`}</option
            >
          {/each}
        </select>
      {/if}
      <button class="btn-secondary" on:click={loadRealAnalytics}>
        <span class="mr-2">🔄</span>
        Refresh
      </button>
      <a href="/streams" class="btn-primary">
        <span class="mr-2">🎥</span>
        Manage Streams
      </a>
    </div>
  </div>

  {#if loading}
    <div class="flex items-center justify-center min-h-64">
      <div class="loading-spinner w-8 h-8" />
    </div>
  {:else if streams.length === 0}
    <div class="card text-center py-12">
      <div class="text-6xl mb-4">📊</div>
      <h3 class="text-xl font-semibold text-tokyo-night-fg mb-2">
        No Streams Found
      </h3>
      <p class="text-tokyo-night-fg-dark mb-6">
        Create your first stream to start tracking analytics
      </p>
      <a href="/streams" class="btn-primary">
        <span class="mr-2">🎥</span>
        Create Stream
      </a>
    </div>
  {:else}
    <!-- Real Analytics Overview -->
    <div class="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-4 gap-6">
      <div class="glow-card p-6">
        <div class="flex items-center justify-between">
          <div>
            <p class="text-sm text-tokyo-night-comment">Current Viewers</p>
            <p class="text-2xl font-bold text-tokyo-night-blue">
              {analyticsData.viewers}
            </p>
          </div>
          <span class="text-2xl">👥</span>
        </div>
      </div>

      <div class="glow-card p-6">
        <div class="flex items-center justify-between">
          <div>
            <p class="text-sm text-tokyo-night-comment">Peak Viewers</p>
            <p class="text-2xl font-bold text-tokyo-night-green">
              {analyticsData.maxViewers}
            </p>
          </div>
          <span class="text-2xl">📈</span>
        </div>
      </div>

      <div class="glow-card p-6">
        <div class="flex items-center justify-between">
          <div>
            <p class="text-sm text-tokyo-night-comment">Stream Status</p>
            <p
              class="text-2xl font-bold {analyticsData.status === 'live'
                ? 'text-tokyo-night-green'
                : 'text-tokyo-night-red'}"
            >
              {analyticsData.status === "live" ? "Live" : "Offline"}
            </p>
          </div>
          <span class="text-2xl"
            >{analyticsData.status === "live" ? "🔴" : "⚫"}</span
          >
        </div>
      </div>

      <div class="glow-card p-6">
        <div class="flex items-center justify-between">
          <div>
            <p class="text-sm text-tokyo-night-comment">Uptime</p>
            <p class="text-2xl font-bold text-tokyo-night-purple">
              {formatUptime(analyticsData.uptime)}
            </p>
          </div>
          <span class="text-2xl">⏱️</span>
        </div>
      </div>
    </div>

    <!-- Stream Details -->
    <div class="grid grid-cols-1 xl:grid-cols-2 gap-8">
      <!-- Technical Metrics -->
      <div class="card">
        <div class="card-header">
          <h2 class="text-xl font-semibold text-tokyo-night-fg mb-2">
            🔧 Technical Metrics
          </h2>
          <p class="text-tokyo-night-fg-dark">
            Real-time technical data from MistServer
          </p>
        </div>

        <div class="space-y-4">
          <div class="bg-tokyo-night-bg-highlight p-4 rounded-lg">
            <div class="grid grid-cols-2 gap-4">
              <div>
                <p class="text-sm text-tokyo-night-comment">Resolution</p>
                <p class="text-lg font-semibold text-tokyo-night-fg">
                  {analyticsData.resolution || "Unknown"}
                </p>
              </div>
              <div>
                <p class="text-sm text-tokyo-night-comment">Bitrate</p>
                <p class="text-lg font-semibold text-tokyo-night-fg">
                  {analyticsData.bitrate || "Unknown"}
                </p>
              </div>
            </div>
          </div>

          <div class="bg-tokyo-night-bg-highlight p-4 rounded-lg">
            <div class="grid grid-cols-2 gap-4">
              <div>
                <p class="text-sm text-tokyo-night-comment">Bandwidth In</p>
                <p class="text-lg font-semibold text-tokyo-night-fg">
                  {formatBandwidth(analyticsData.bandwidth_in)}
                </p>
              </div>
              <div>
                <p class="text-sm text-tokyo-night-comment">Bandwidth Out</p>
                <p class="text-lg font-semibold text-tokyo-night-fg">
                  {formatBandwidth(analyticsData.bandwidth_out)}
                </p>
              </div>
            </div>
          </div>

          <div class="bg-tokyo-night-bg-highlight p-4 rounded-lg">
            <div class="grid grid-cols-2 gap-4">
              <div>
                <p class="text-sm text-tokyo-night-comment">Stream Key</p>
                <p class="text-sm font-mono text-tokyo-night-fg">
                  {selectedStream?.stream_key
                    ? `${selectedStream.stream_key.slice(0, 12)}...`
                    : "N/A"}
                </p>
              </div>
              <div>
                <p class="text-sm text-tokyo-night-comment">Playback ID</p>
                <p class="text-sm font-mono text-tokyo-night-fg">
                  {selectedStream?.playback_id
                    ? `${selectedStream.playback_id.slice(0, 12)}...`
                    : "N/A"}
                </p>
              </div>
            </div>
          </div>
        </div>
      </div>

      <!-- Stream Information -->
      <div class="card">
        <div class="card-header">
          <h2 class="text-xl font-semibold text-tokyo-night-fg mb-2">
            ℹ️ Stream Information
          </h2>
          <p class="text-tokyo-night-fg-dark">
            Basic stream details and configuration
          </p>
        </div>

        <div class="space-y-4">
          <div class="bg-tokyo-night-bg-highlight p-4 rounded-lg">
            <h3 class="font-semibold text-tokyo-night-fg mb-2">
              {analyticsData.title || "Untitled Stream"}
            </h3>
            {#if analyticsData.description}
              <p class="text-sm text-tokyo-night-comment mb-3">
                {analyticsData.description}
              </p>
            {/if}
            <div class="grid grid-cols-2 gap-4 text-sm">
              <div>
                <p class="text-tokyo-night-comment">Created</p>
                <p class="text-tokyo-night-fg">
                  {analyticsData.created_at
                    ? new Date(analyticsData.created_at).toLocaleDateString()
                    : "Unknown"}
                </p>
              </div>
              <div>
                <p class="text-tokyo-night-comment">Last Updated</p>
                <p class="text-tokyo-night-fg">
                  {analyticsData.lastUpdated
                    ? analyticsData.lastUpdated.toLocaleString()
                    : "Never"}
                </p>
              </div>
            </div>
          </div>

          <div class="bg-tokyo-night-bg-highlight p-4 rounded-lg">
            <h3 class="font-semibold text-tokyo-night-fg mb-3">Settings</h3>
            <div class="space-y-2 text-sm">
              <div class="flex justify-between">
                <span class="text-tokyo-night-comment">Recording</span>
                <span class="text-tokyo-night-fg">
                  {analyticsData.is_recording_enabled ? "Enabled" : "Disabled"}
                </span>
              </div>
              <div class="flex justify-between">
                <span class="text-tokyo-night-comment">Public</span>
                <span class="text-tokyo-night-fg">
                  {analyticsData.is_public ? "Yes" : "No"}
                </span>
              </div>
              <div class="flex justify-between">
                <span class="text-tokyo-night-comment">Internal Name</span>
                <span class="text-xs font-mono text-tokyo-night-fg">
                  {analyticsData.internal_name || "N/A"}
                </span>
              </div>
            </div>
          </div>

          {#if analyticsData.tracks && Object.keys(analyticsData.tracks).length > 0}
            <div class="bg-tokyo-night-bg-highlight p-4 rounded-lg">
              <h3 class="font-semibold text-tokyo-night-fg mb-3">
                Technical Stream Information
              </h3>

              {#if analyticsData.tracks.codec || analyticsData.tracks.width || analyticsData.tracks.fps}
                <!-- Stream health data from ClickHouse -->
                <div class="space-y-4">
                  <div class="border border-tokyo-night-selection rounded-lg p-3">
                    <div class="flex justify-between items-center mb-2">
                      <h4 class="font-medium text-tokyo-night-fg">Video Stream</h4>
                      <span class="text-xs bg-tokyo-night-green/20 text-tokyo-night-green px-2 py-1 rounded">
                        {analyticsData.tracks.codec || "Unknown Codec"}
                      </span>
                    </div>

                    <div class="grid grid-cols-2 gap-4 text-sm">
                      {#if analyticsData.tracks.width && analyticsData.tracks.height}
                        <div class="flex justify-between">
                          <span class="text-tokyo-night-comment">Resolution</span>
                          <span class="text-tokyo-night-fg">{analyticsData.tracks.width}x{analyticsData.tracks.height}</span>
                        </div>
                      {/if}
                      {#if analyticsData.tracks.fps}
                        <div class="flex justify-between">
                          <span class="text-tokyo-night-comment">Frame Rate</span>
                          <span class="text-tokyo-night-fg">{analyticsData.tracks.fps} fps</span>
                        </div>
                      {/if}
                      {#if analyticsData.tracks.profile}
                        <div class="flex justify-between">
                          <span class="text-tokyo-night-comment">Profile</span>
                          <span class="text-tokyo-night-fg">{analyticsData.tracks.profile}</span>
                        </div>
                      {/if}
                      {#if analyticsData.bitrate}
                        <div class="flex justify-between">
                          <span class="text-tokyo-night-comment">Bitrate</span>
                          <span class="text-tokyo-night-fg font-semibold text-tokyo-night-green">{analyticsData.bitrate}</span>
                        </div>
                      {/if}
                    </div>
                  </div>

                  <div class="text-xs text-tokyo-night-comment mt-2">
                    <span class="inline-flex items-center space-x-1">
                      <span class="w-2 h-2 bg-tokyo-night-green rounded-full"></span>
                      <span>Real-time codec data from MistServer via ClickHouse</span>
                    </span>
                  </div>
                </div>
              {:else if analyticsData.tracks.tracks && analyticsData.tracks.tracks.length > 0}
                <!-- Analytics API track data (from health data) -->
                <div class="space-y-4">
                  {#each analyticsData.tracks.tracks as track}
                    <div
                      class="border border-tokyo-night-selection rounded-lg p-3"
                    >
                      <div class="flex justify-between items-center mb-2">
                        <h4 class="font-medium text-tokyo-night-fg capitalize">
                          {track.track_name || `${track.type} Track`}
                        </h4>
                        <span
                          class="text-xs bg-tokyo-night-selection text-tokyo-night-fg px-2 py-1 rounded"
                        >
                          {track.codec || "Unknown"}
                        </span>
                      </div>

                      <div class="grid grid-cols-2 gap-2 text-sm">
                        {#if track.type === "video"}
                          <div class="flex justify-between">
                            <span class="text-tokyo-night-comment"
                              >Resolution</span
                            >
                            <span class="text-tokyo-night-fg"
                              >{track.resolution || "Unknown"}</span
                            >
                          </div>
                          <div class="flex justify-between">
                            <span class="text-tokyo-night-comment"
                              >Frame Rate</span
                            >
                            <span class="text-tokyo-night-fg"
                              >{track.fps
                                ? `${track.fps} fps`
                                : "Unknown"}</span
                            >
                          </div>
                        {:else if track.type === "audio"}
                          <div class="flex justify-between">
                            <span class="text-tokyo-night-comment"
                              >Channels</span
                            >
                            <span class="text-tokyo-night-fg"
                              >{track.channels || "Unknown"}</span
                            >
                          </div>
                          <div class="flex justify-between">
                            <span class="text-tokyo-night-comment"
                              >Sample Rate</span
                            >
                            <span class="text-tokyo-night-fg"
                              >{track.sample_rate
                                ? `${track.sample_rate} Hz`
                                : "Unknown"}</span
                            >
                          </div>
                        {/if}
                        <div class="flex justify-between">
                          <span class="text-tokyo-night-comment">Bitrate</span>
                          <span
                            class="text-tokyo-night-fg font-semibold text-tokyo-night-green"
                          >
                            {track.bitrate_kbps
                              ? `${track.bitrate_kbps} kbps`
                              : "Unknown"}
                          </span>
                        </div>
                        {#if track.buffer !== undefined}
                          <div class="flex justify-between">
                            <span class="text-tokyo-night-comment">Buffer</span>
                            <span class="text-tokyo-night-fg"
                              >{track.buffer} ms</span
                            >
                          </div>
                        {/if}
                      </div>
                    </div>
                  {/each}

                  <div class="text-xs text-tokyo-night-comment mt-2">
                    <span class="inline-flex items-center space-x-1">
                      <span class="w-2 h-2 bg-tokyo-night-green rounded-full" />
                      <span
                        >Real-time data from MistServer API ({analyticsData.source})</span
                      >
                    </span>
                  </div>
                </div>
              {:else if analyticsData.tracks.tracks && analyticsData.tracks.tracks.length > 0}
                <div class="space-y-4">
                  {#each analyticsData.tracks.tracks as track}
                    <div
                      class="border border-tokyo-night-selection rounded-lg p-3"
                    >
                      <div class="flex justify-between items-center mb-2">
                        <h4 class="font-medium text-tokyo-night-fg capitalize">
                          {track.type} Track {track.track_id}
                        </h4>
                        <span
                          class="text-xs bg-tokyo-night-selection text-tokyo-night-fg px-2 py-1 rounded"
                        >
                          {track.codec || "Unknown"}
                        </span>
                      </div>

                      <div class="grid grid-cols-2 gap-2 text-sm">
                        {#if track.type === "video"}
                          <div class="flex justify-between">
                            <span class="text-tokyo-night-comment"
                              >Resolution</span
                            >
                            <span class="text-tokyo-night-fg"
                              >{track.resolution || "Unknown"}</span
                            >
                          </div>
                          <div class="flex justify-between">
                            <span class="text-tokyo-night-comment"
                              >Frame Rate</span
                            >
                            <span class="text-tokyo-night-fg"
                              >{track.fps
                                ? `${track.fps} fps`
                                : "Unknown"}</span
                            >
                          </div>
                        {:else if track.type === "audio"}
                          <div class="flex justify-between">
                            <span class="text-tokyo-night-comment"
                              >Channels</span
                            >
                            <span class="text-tokyo-night-fg"
                              >{track.channels || "Unknown"}</span
                            >
                          </div>
                          <div class="flex justify-between">
                            <span class="text-tokyo-night-comment"
                              >Sample Rate</span
                            >
                            <span class="text-tokyo-night-fg"
                              >{track.sample_rate
                                ? `${track.sample_rate} Hz`
                                : "Unknown"}</span
                            >
                          </div>
                        {/if}
                        <div class="flex justify-between">
                          <span class="text-tokyo-night-comment">Bitrate</span>
                          <span class="text-tokyo-night-fg"
                            >{track.bitrate_kbps
                              ? `${track.bitrate_kbps} kbps`
                              : "Unknown"}</span
                          >
                        </div>
                      </div>
                    </div>
                  {/each}

                  <div class="text-xs text-tokyo-night-comment mt-2">
                    <span class="inline-flex items-center space-x-1">
                      <span
                        class="w-2 h-2 bg-tokyo-night-yellow rounded-full"
                      />
                      <span
                        >Fallback data from Data API ({analyticsData.tracks
                          .source})</span
                      >
                    </span>
                  </div>
                </div>
              {:else if analyticsData.tracks.resolution}
                <div class="grid grid-cols-2 gap-4 text-sm">
                  <div class="flex justify-between">
                    <span class="text-tokyo-night-comment">Codec</span>
                    <span class="text-tokyo-night-fg"
                      >{analyticsData.tracks.codec || "Unknown"}</span
                    >
                  </div>
                  <div class="flex justify-between">
                    <span class="text-tokyo-night-comment">Resolution</span>
                    <span class="text-tokyo-night-fg"
                      >{analyticsData.tracks.resolution || "Unknown"}</span
                    >
                  </div>
                  <div class="flex justify-between">
                    <span class="text-tokyo-night-comment">Frame Rate</span>
                    <span class="text-tokyo-night-fg"
                      >{analyticsData.tracks.fps
                        ? `${analyticsData.tracks.fps} fps`
                        : "Unknown"}</span
                    >
                  </div>
                  <div class="flex justify-between">
                    <span class="text-tokyo-night-comment">Bitrate</span>
                    <span class="text-tokyo-night-fg"
                      >{analyticsData.tracks.bitrate
                        ? `${analyticsData.tracks.bitrate}`
                        : "Unknown"}</span
                    >
                  </div>
                </div>

                <div class="text-xs text-tokyo-night-comment mt-2">
                  <span class="inline-flex items-center space-x-1">
                    <span class="w-2 h-2 bg-tokyo-night-red rounded-full" />
                    <span>Track data ({analyticsData.tracks.source})</span>
                  </span>
                </div>
              {:else}
                <p class="text-tokyo-night-comment text-sm">
                  No track information available
                </p>
              {/if}
            </div>
          {/if}
        </div>
      </div>
    </div>

    {#if analyticsData.lastUpdated}
      <div class="text-center">
        <p class="text-xs text-tokyo-night-comment">
          Last updated: {analyticsData.lastUpdated.toLocaleString()}
        </p>
      </div>
    {/if}
  {/if}
</div>

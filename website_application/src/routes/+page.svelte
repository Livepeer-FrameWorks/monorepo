<script>
  import { onMount, onDestroy } from "svelte";
  import { goto } from "$app/navigation";
  import { base } from "$app/paths";
  import { auth } from "$lib/stores/auth";
  import { analyticsService } from "$lib/graphql/services/analytics.js";
  import { billingService } from "$lib/graphql/services/billing.js";
  import { toast } from "$lib/stores/toast.js";
  import {
    realtimeStreams,
    streamMetrics,
    realtimeViewers,
    connectionStatus,
  } from "$lib/stores/realtime";

  let isAuthenticated = false;
  /** @type {any} */
  let user = null;
  /** @type {any[]} */
  let streams = [];
  let loading = true;

  // Real-time dashboard data
  /** @type {any[]} */
  let realtimeData = [];
  /** @type {any} */
  let liveMetrics = {};
  let totalRealtimeViewers = 0;
  /** @type {{ status: string, message: string }} */
  let wsConnectionStatus = { status: "disconnected", message: "Disconnected" };

  // Usage and billing data
  /** @type {any} */
  let usageData = null;
  /** @type {any} */
  let billingStatus = null;

  // Subscribe to auth store
  auth.subscribe((/** @type {any} */ authState) => {
    isAuthenticated = authState.isAuthenticated;
    // authState.user contains the full API response: { user: {...}, streams: [...] }
    user = authState.user?.user || null;
    streams = authState.user?.streams || [];
    loading = authState.loading;
  });

  // Subscribe to real-time data
  realtimeStreams.subscribe((data) => {
    realtimeData = data;
  });

  streamMetrics.subscribe((data) => {
    liveMetrics = data;
  });

  realtimeViewers.subscribe((data) => {
    totalRealtimeViewers = data;
  });

  connectionStatus.subscribe((status) => {
    wsConnectionStatus = status;
  });

  onMount(async () => {
    await auth.checkAuth();

    // Load dashboard data if authenticated (layout handles redirects)
    if (isAuthenticated) {
      await loadDashboardData();
    }
  });

  async function loadDashboardData() {
    try {
      // Load platform overview and billing status
      const [platformOverview, billingStatusData] = await Promise.all([
        analyticsService.getPlatformOverview(),
        billingService.getBillingStatus(),
      ]);

      usageData = {
        totalStreams: platformOverview.totalStreams || 0,
        totalViewers: platformOverview.totalViewers || 0,
        totalBandwidth: platformOverview.totalBandwidth || 0
      };
      billingStatus = billingStatusData || {};
    } catch (err) {
      console.error("Failed to load dashboard data:", err);
    }
  }

  // Note: Authentication redirects are handled by +layout.svelte

  // Get primary stream (most recently active or first stream)
  $: primaryStream =
    streams && streams.length > 0
      ? streams.find((s) => s.status === "live") || streams[0]
      : null;

  // Enhanced stream stats with real-time data
  $: enhancedStreamStats = {
    total: streams?.length || 0,
    live:
      streams?.filter((s) => s.status === "live").length ||
      realtimeData?.filter((s) => s.status === "live").length ||
      0,
    totalViewers:
      totalRealtimeViewers > 0
        ? totalRealtimeViewers
        : streams?.reduce((sum, s) => sum + (s.viewers || 0), 0) || 0,
    realtimeStreams: realtimeData?.length || 0,
  };

  // Calculate total bandwidth from live metrics
  $: totalBandwidth = Object.values(liveMetrics).reduce((total, stream) => {
    return total + (stream.bandwidth_in || 0) + (stream.bandwidth_out || 0);
  }, 0);

  /**
   * @param {string} text
   */
  function copyToClipboard(text) {
    navigator.clipboard.writeText(text);
    toast.success("Stream key copied to clipboard!");
  }

  // Format bytes to human readable
  /**
   * @param {number} bytes
   */
  function formatBytes(bytes) {
    if (bytes === 0) return "0 B";
    const k = 1024;
    const sizes = ["B", "KB", "MB", "GB", "TB"];
    const i = Math.floor(Math.log(bytes) / Math.log(k));
    return parseFloat((bytes / Math.pow(k, i)).toFixed(1)) + " " + sizes[i];
  }

  /**
   * @param {number} num
   */
  function formatNumber(num) {
    return new Intl.NumberFormat().format(Math.round(num));
  }
</script>

<svelte:head>
  <title>Dashboard - FrameWorks</title>
</svelte:head>

{#if loading}
  <div class="flex items-center justify-center min-h-64">
    <div class="loading-spinner w-8 h-8" />
  </div>
{:else if isAuthenticated}
  <div class="page-transition">
    <!-- Welcome Header -->
    <div class="mb-8">
      <h1 class="text-3xl font-bold gradient-text mb-2">
        Welcome back, {user?.email?.split("@")[0] || "Streamer"}! 👋
      </h1>
      <p class="text-tokyo-night-fg-dark">
        Your streams are looking great today. Here's what's happening across
        your platform.
      </p>
    </div>

    <!-- Real-time Dashboard Stats -->
    <div class="grid grid-cols-1 md:grid-cols-4 gap-6 mb-8">
      <!-- Live Streams -->
      <div class="glow-card p-6 text-center relative">
        <div class="absolute top-3 right-3">
          <div class="flex items-center space-x-1 text-xs">
            <div
              class="w-2 h-2 rounded-full {wsConnectionStatus.status ===
              'connected'
                ? 'bg-tokyo-night-green animate-pulse'
                : 'bg-tokyo-night-red'}"
            />
            <span class="text-tokyo-night-comment">Live</span>
          </div>
        </div>
        <div class="text-3xl mb-2">🔴</div>
        <div class="text-2xl font-bold text-tokyo-night-green mb-1">
          {enhancedStreamStats.live}
        </div>
        <div class="text-sm text-tokyo-night-comment">Streams Live</div>
      </div>

      <!-- Total Viewers -->
      <div class="glow-card p-6 text-center">
        <div class="text-3xl mb-2">👥</div>
        <div class="text-2xl font-bold text-tokyo-night-cyan mb-1">
          {formatNumber(enhancedStreamStats.totalViewers)}
        </div>
        <div class="text-sm text-tokyo-night-comment">Total Viewers</div>
      </div>

      <!-- Bandwidth Usage -->
      <div class="glow-card p-6 text-center">
        <div class="text-3xl mb-2">📡</div>
        <div class="text-2xl font-bold text-tokyo-night-purple mb-1">
          {formatBytes(totalBandwidth)}/s
        </div>
        <div class="text-sm text-tokyo-night-comment">Bandwidth</div>
      </div>

      <!-- Usage This Week -->
      <div class="glow-card p-6 text-center">
        <div class="text-3xl mb-2">💰</div>
        <div class="text-2xl font-bold text-tokyo-night-yellow mb-1">
          {usageData?.stream_hours
            ? formatNumber(usageData.stream_hours)
            : "0"}h
        </div>
        <div class="text-sm text-tokyo-night-comment">Stream Hours (7d)</div>
      </div>
    </div>

    <!-- Main Content Grid -->
    <div class="grid grid-cols-1 lg:grid-cols-3 gap-6">
      <!-- Streams Overview Card -->
      <div class="card">
        <div class="card-header">
          <h2 class="text-xl font-semibold text-tokyo-night-fg mb-2">
            Stream Central
          </h2>
          <p class="text-tokyo-night-fg-dark">
            Everything you need to manage your streams in one place
          </p>
        </div>

        <div class="space-y-6">
          <!-- Stream Stats Grid with Real-time Data -->
          <div class="grid grid-cols-3 gap-4">
            <div class="text-center">
              <div class="text-2xl font-bold text-tokyo-night-fg">
                {enhancedStreamStats.total}
              </div>
              <div class="text-xs text-tokyo-night-comment">Total Streams</div>
            </div>
            <div class="text-center">
              <div class="text-2xl font-bold text-tokyo-night-green">
                {enhancedStreamStats.live}
              </div>
              <div class="text-xs text-tokyo-night-comment">Live Now</div>
            </div>
            <div class="text-center">
              <div class="text-2xl font-bold text-tokyo-night-cyan">
                {formatNumber(enhancedStreamStats.totalViewers)}
              </div>
              <div class="text-xs text-tokyo-night-comment">
                Current Viewers
              </div>
            </div>
          </div>

          <!-- Real-time Connection Status -->
          {#if wsConnectionStatus.status !== "connected"}
            <div
              class="bg-tokyo-night-yellow/10 border border-tokyo-night-yellow/30 rounded-lg p-3"
            >
              <div class="flex items-center space-x-2 text-sm">
                <div
                  class="w-2 h-2 rounded-full bg-tokyo-night-yellow animate-pulse"
                />
                <span class="text-tokyo-night-yellow"
                  >{wsConnectionStatus.message}</span
                >
              </div>
            </div>
          {/if}

          <!-- Primary Stream Details -->
          {#if primaryStream}
            <div
              class="bg-tokyo-night-bg-highlight p-4 rounded-lg border border-tokyo-night-fg-gutter"
            >
              <div class="flex items-center justify-between mb-3">
                <h3 class="font-semibold text-tokyo-night-fg">
                  {primaryStream.title ||
                    `Stream ${primaryStream.id.slice(0, 8)}`}
                </h3>
                <div class="flex items-center space-x-2">
                  <div
                    class="w-2 h-2 rounded-full {primaryStream.status === 'live'
                      ? 'bg-tokyo-night-green animate-pulse'
                      : 'bg-tokyo-night-red'}"
                  />
                  <span class="text-xs text-tokyo-night-comment capitalize">
                    {primaryStream.status || "offline"}
                  </span>
                </div>
              </div>

              <div class="grid grid-cols-2 gap-4 text-sm mb-4">
                <div>
                  <p class="text-tokyo-night-comment">Viewers</p>
                  <p class="font-semibold text-tokyo-night-fg">
                    {primaryStream.viewers || 0}
                  </p>
                </div>
                <div>
                  <p class="text-tokyo-night-comment">Resolution</p>
                  <p class="font-semibold text-tokyo-night-fg">
                    {primaryStream.resolution || "Unknown"}
                  </p>
                </div>
              </div>

              <!-- Stream Key (Primary Stream) -->
              <div>
                <label
                  for="primary-stream-key"
                  class="block text-sm font-medium text-tokyo-night-fg-dark mb-2"
                  >Stream Key</label
                >
                <div class="flex items-center space-x-3">
                  <input
                    id="primary-stream-key"
                    type="text"
                    value={primaryStream.stream_key || "Loading..."}
                    readonly
                    class="input flex-1 font-mono text-sm"
                  />
                  <button
                    on:click={() => copyToClipboard(primaryStream.stream_key)}
                    class="btn-secondary"
                    disabled={!primaryStream.stream_key}
                  >
                    Copy
                  </button>
                </div>
                <p class="text-xs text-tokyo-night-comment mt-2">
                  Keep your stream key private. Anyone with this key can
                  broadcast to your channel.
                </p>
              </div>
            </div>
          {:else}
            <div class="text-center py-6">
              <div class="text-4xl mb-2">🎥</div>
              <p class="text-tokyo-night-fg-dark mb-4">No streams found</p>
              <a href="{base}/streams" class="btn-primary">
                <span class="mr-2">➕</span>
                Create Your First Stream
              </a>
            </div>
          {/if}

          <!-- Quick Actions -->
          <div class="flex space-x-3">
            <a href="{base}/streams" class="btn-primary flex-1 text-center">
              <span class="mr-2">🎥</span>
              Manage Streams
            </a>
            <a href="{base}/analytics" class="btn-secondary flex-1 text-center">
              <span class="mr-2">📊</span>
              View Analytics
            </a>
          </div>
        </div>
      </div>

      <!-- Usage & Billing Overview Card -->
      <div class="card">
        <div class="card-header">
          <h2 class="text-xl font-semibold text-tokyo-night-fg mb-2">
            Your Account
          </h2>
          <p class="text-tokyo-night-fg-dark">
            Current plan details and usage for this billing period
          </p>
        </div>

        <div class="space-y-6">
          <!-- Current Plan -->
          {#if billingStatus}
            <div
              class="bg-tokyo-night-bg-highlight p-4 rounded-lg border border-tokyo-night-fg-gutter"
            >
              <div class="flex items-center justify-between mb-3">
                <h3 class="font-semibold text-tokyo-night-fg">
                  Current Plan: {billingStatus.tier?.display_name || "Free"}
                </h3>
                <span
                  class="bg-tokyo-night-green/20 text-tokyo-night-green px-2 py-1 rounded text-xs capitalize"
                >
                  {billingStatus.subscription?.status || "active"}
                </span>
              </div>

              <div class="grid grid-cols-2 gap-4 text-sm">
                <div>
                  <p class="text-tokyo-night-comment">Monthly Cost</p>
                  <p class="font-semibold text-tokyo-night-fg">
                    {billingStatus.tier?.base_price
                      ? `$${billingStatus.tier.base_price}`
                      : "Free"}
                  </p>
                </div>
                <div>
                  <p class="text-tokyo-night-comment">Usage (7d)</p>
                  <p class="font-semibold text-tokyo-night-fg">
                    {usageData?.stream_hours
                      ? `${formatNumber(usageData.stream_hours)}h`
                      : "0h"}
                  </p>
                </div>
              </div>
            </div>
          {/if}

          <!-- Weekly Usage Summary -->
          {#if usageData}
            <div class="grid grid-cols-2 gap-4">
              <div
                class="text-center p-3 bg-tokyo-night-bg-highlight rounded-lg"
              >
                <div class="text-lg font-bold text-tokyo-night-blue">
                  {usageData.egress_gb
                    ? formatNumber(usageData.egress_gb)
                    : "0"} GB
                </div>
                <div class="text-xs text-tokyo-night-comment">
                  Bandwidth Used
                </div>
              </div>
              <div
                class="text-center p-3 bg-tokyo-night-bg-highlight rounded-lg"
              >
                <div class="text-lg font-bold text-tokyo-night-purple">
                  {usageData.peak_viewers || 0}
                </div>
                <div class="text-xs text-tokyo-night-comment">Peak Viewers</div>
              </div>
            </div>
          {/if}

          <!-- Quick Actions -->
          <div class="space-y-3">
            <a
              href="{base}/analytics/usage"
              class="btn-primary w-full text-center block"
            >
              <span class="mr-2">💰</span>
              View Detailed Usage & Costs
            </a>
            <a
              href="{base}/account/billing"
              class="btn-secondary w-full text-center block"
            >
              <span class="mr-2">💳</span>
              Manage Billing
            </a>
          </div>
        </div>
      </div>

      <!-- System Health & Real-time Status Card -->
      <div class="card">
        <div class="card-header">
          <h2 class="text-xl font-semibold text-tokyo-night-fg mb-2">
            Platform Status
          </h2>
          <p class="text-tokyo-night-fg-dark">
            Everything's running smoothly - here's what's powering your streams
          </p>
        </div>

        <div class="space-y-6">
          <!-- Connection Status -->
          <div class="space-y-3">
            <h3 class="font-semibold text-tokyo-night-fg text-sm">
              Platform Status
            </h3>

            <div class="space-y-2">
              <!-- WebSocket Connection -->
              <div
                class="flex items-center justify-between p-3 bg-tokyo-night-bg-highlight rounded-lg"
              >
                <div class="flex items-center space-x-3">
                  <div
                    class="w-3 h-3 rounded-full {wsConnectionStatus.status ===
                    'connected'
                      ? 'bg-tokyo-night-green'
                      : wsConnectionStatus.status === 'reconnecting'
                      ? 'bg-tokyo-night-yellow animate-pulse'
                      : 'bg-tokyo-night-red'}"
                  />
                  <span class="text-sm text-tokyo-night-fg"
                    >Real-time Updates</span
                  >
                </div>
                <span class="text-xs text-tokyo-night-comment capitalize"
                  >{wsConnectionStatus.message}</span
                >
              </div>

              <!-- Streaming Service -->
              <div
                class="flex items-center justify-between p-3 bg-tokyo-night-bg-highlight rounded-lg"
              >
                <div class="flex items-center space-x-3">
                  <div class="w-3 h-3 rounded-full bg-tokyo-night-green" />
                  <span class="text-sm text-tokyo-night-fg"
                    >Streaming Service</span
                  >
                </div>
                <span class="text-xs text-tokyo-night-comment">Operational</span
                >
              </div>

              <!-- Analytics Service -->
              <div
                class="flex items-center justify-between p-3 bg-tokyo-night-bg-highlight rounded-lg"
              >
                <div class="flex items-center space-x-3">
                  <div
                    class="w-3 h-3 rounded-full {usageData
                      ? 'bg-tokyo-night-green'
                      : 'bg-tokyo-night-yellow'}"
                  />
                  <span class="text-sm text-tokyo-night-fg"
                    >Analytics Service</span
                  >
                </div>
                <span class="text-xs text-tokyo-night-comment"
                  >{usageData ? "Operational" : "Loading"}</span
                >
              </div>
            </div>
          </div>

          <!-- Real-time Stream Health -->
          {#if Object.keys(liveMetrics).length > 0}
            <div class="space-y-3">
              <h3 class="font-semibold text-tokyo-night-fg text-sm">
                Live Stream Health
              </h3>

              {#each Object.entries(liveMetrics) as [streamId, metrics]}
                <div class="p-3 bg-tokyo-night-bg-highlight rounded-lg">
                  <div class="flex items-center justify-between mb-2">
                    <span class="text-sm font-medium text-tokyo-night-fg"
                      >Stream {streamId.slice(0, 8)}</span
                    >
                    <div class="flex items-center space-x-1">
                      <div
                        class="w-2 h-2 bg-tokyo-night-green rounded-full animate-pulse"
                      />
                      <span class="text-xs text-tokyo-night-comment">Live</span>
                    </div>
                  </div>

                  <div class="grid grid-cols-2 gap-2 text-xs">
                    <div>
                      <span class="text-tokyo-night-comment">Bandwidth:</span>
                      <span class="text-tokyo-night-fg ml-1"
                        >{formatBytes(
                          (metrics.bandwidth_in || 0) +
                            (metrics.bandwidth_out || 0)
                        )}/s</span
                      >
                    </div>
                    <div>
                      <span class="text-tokyo-night-comment">Bitrate:</span>
                      <span class="text-tokyo-night-fg ml-1"
                        >{metrics.bitrate_kbps || "Unknown"} kbps</span
                      >
                    </div>
                    {#if metrics.video_codec || metrics.audio_codec}
                      <div class="col-span-2">
                        <span class="text-tokyo-night-comment">Codecs:</span>
                        <span class="text-tokyo-night-fg ml-1">
                          {#if metrics.video_codec}
                            Video: {metrics.video_codec}{#if metrics.audio_codec},
                            {/if}
                          {/if}
                          {#if metrics.audio_codec}
                            Audio: {metrics.audio_codec}
                          {/if}
                        </span>
                      </div>
                    {/if}
                  </div>
                </div>
              {/each}
            </div>
          {/if}

          <!-- Quick Actions for System -->
          <div class="space-y-2">
            <a
              href="{base}/analytics/realtime"
              class="btn-primary w-full text-center block"
            >
              <span class="mr-2">⚡</span>
              Real-time Analytics
            </a>
            <a href="{base}/analytics" class="btn-secondary w-full text-center block">
              <span class="mr-2">📊</span>
              Full Analytics
            </a>
          </div>
        </div>
      </div>
    </div>

    <!-- Quick Setup Guide - Full Width Section -->
    <div class="mt-8">
      <div class="card">
        <div class="card-header">
          <h2 class="text-xl font-semibold text-tokyo-night-fg mb-2">
            New to Streaming?
          </h2>
          <p class="text-tokyo-night-fg-dark">
            No worries! Get your first stream up and running in just 3 steps
          </p>
        </div>

        <div class="space-y-4">
          <!-- Step 1 -->
          <div class="flex items-start space-x-4">
            <div
              class="w-8 h-8 bg-tokyo-night-cyan text-tokyo-night-bg font-bold rounded-full flex items-center justify-center text-sm"
            >
              1
            </div>
            <div>
              <h3 class="font-semibold text-tokyo-night-fg">
                Get OBS Studio (It's Free!)
              </h3>
              <p class="text-sm text-tokyo-night-fg-dark">
                The most popular streaming software - works on any computer
              </p>
              <a
                href="https://obsproject.com/"
                target="_blank"
                class="text-tokyo-night-cyan hover:underline text-sm"
              >
                Download OBS Studio →
              </a>
            </div>
          </div>

          <!-- Step 2 -->
          <div class="flex items-start space-x-4">
            <div
              class="w-8 h-8 bg-tokyo-night-cyan text-tokyo-night-bg font-bold rounded-full flex items-center justify-center text-sm"
            >
              2
            </div>
            <div>
              <h3 class="font-semibold text-tokyo-night-fg">
                Connect OBS to FrameWorks
              </h3>
              <p class="text-sm text-tokyo-night-fg-dark mb-2">
                Just copy these settings into OBS (Settings → Stream):
              </p>
              <div
                class="bg-tokyo-night-bg-highlight p-3 rounded border border-tokyo-night-fg-gutter"
              >
                <p class="text-xs text-tokyo-night-comment">Server URL:</p>
                <p class="font-mono text-sm text-tokyo-night-fg">
                  rtmp://localhost:1935/live
                </p>
                <p class="text-xs text-tokyo-night-comment mt-2">Stream Key:</p>
                <p class="font-mono text-sm text-tokyo-night-fg">
                  {primaryStream?.stream_key
                    ? `${primaryStream.stream_key.slice(0, 12)}...`
                    : "Create a stream first"}
                </p>
              </div>
            </div>
          </div>

          <!-- Step 3 -->
          <div class="flex items-start space-x-4">
            <div
              class="w-8 h-8 bg-tokyo-night-cyan text-tokyo-night-bg font-bold rounded-full flex items-center justify-center text-sm"
            >
              3
            </div>
            <div>
              <h3 class="font-semibold text-tokyo-night-fg">Hit "Go Live"!</h3>
              <p class="text-sm text-tokyo-night-fg-dark">
                That's it! Click "Start Streaming" in OBS and you'll be
                broadcasting to the world
              </p>
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
{/if}

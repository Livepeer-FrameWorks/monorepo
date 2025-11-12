<script>
  import { onMount } from "svelte";
  import { resolve } from "$app/paths";
  import { auth } from "$lib/stores/auth";
  import { analyticsService } from "$lib/graphql/services/analytics.js";
  import { billingService } from "$lib/graphql/services/billing.js";
  import { toast } from "$lib/stores/toast.js";
  import { getIconComponent } from "$lib/iconUtils";
  import { Button } from "$lib/components/ui/button";
  import { GridSeam } from "$lib/components/layout";
  import DashboardMetricCard from "$lib/components/shared/DashboardMetricCard.svelte";
  import StreamStatsGrid from "$lib/components/dashboard/StreamStatsGrid.svelte";
  import ConnectionStatusBanner from "$lib/components/dashboard/ConnectionStatusBanner.svelte";
  import PrimaryStreamCard from "$lib/components/dashboard/PrimaryStreamCard.svelte";
  import ServiceStatusList from "$lib/components/dashboard/ServiceStatusList.svelte";
  import LiveStreamHealthCards from "$lib/components/dashboard/LiveStreamHealthCards.svelte";
  import ObsSetupGuide from "$lib/components/dashboard/ObsSetupGuide.svelte";
  import {
    realtimeStreams,
    streamMetrics,
    realtimeViewers,
    connectionStatus,
  } from "$lib/stores/realtime";

  let isAuthenticated = $state(false);
  /** @type {any} */
  let user = $state(null);
  /** @type {any[]} */
  let streams = $state([]);
  let loading = $state(true);

  // Real-time dashboard data
  /** @type {any[]} */
  let realtimeData = $state([]);
  /** @type {any} */
  let liveMetrics = $state({});
  let totalRealtimeViewers = $state(0);
  /** @type {{ status: string, message: string }} */
  let wsConnectionStatus = $state({ status: "disconnected", message: "Disconnected" });

  // Usage and billing data
  /** @type {any} */
  let usageData = $state(null);
  /** @type {any} */
  let billingStatus = $state(null);

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
  let primaryStream =
    $derived(streams && streams.length > 0
      ? streams.find((s) => s.status === "live") || streams[0]
      : null);

  // Enhanced stream stats with real-time data
  let enhancedStreamStats = $derived({
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
  });

  // Calculate total bandwidth from live metrics
  let totalBandwidth = $derived(Object.values(liveMetrics).reduce((total, stream) => {
    return total + (stream.bandwidth_in || 0) + (stream.bandwidth_out || 0);
  }, 0));

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
    <div class="loading-spinner w-8 h-8"></div>
  </div>
{:else if isAuthenticated}
  {@const SvelteComponent = getIconComponent('Play')}
  {@const SvelteComponent_1 = getIconComponent('Users')}
  {@const SvelteComponent_2 = getIconComponent('Wifi')}
  {@const SvelteComponent_3 = getIconComponent('CreditCard')}
  {@const SvelteComponent_6 = getIconComponent('Video')}
  {@const SvelteComponent_7 = getIconComponent('BarChart3')}
  {@const SvelteComponent_8 = getIconComponent('CreditCard')}
  {@const SvelteComponent_9 = getIconComponent('CreditCard')}
  {@const SvelteComponent_10 = getIconComponent('Zap')}
  {@const SvelteComponent_11 = getIconComponent('BarChart3')}
  <div class="page-transition">
    <!-- Welcome Header -->
    <div class="mb-8">
      <h1 class="text-3xl font-bold gradient-text mb-2">
        Welcome back, {user?.email?.split("@")[0] || "Streamer"}!
      </h1>
      <p class="text-tokyo-night-fg-dark">
        Stream dashboard and control panel
      </p>
    </div>

    <!-- Real-time Dashboard Stats -->
    <GridSeam cols={4} stack="md" surface="panel" class="mb-8">
      <div>
        <DashboardMetricCard
          icon={SvelteComponent}
          iconColor="text-tokyo-night-red"
          value={enhancedStreamStats.live}
          valueColor="text-tokyo-night-green"
          label="Streams Live"
          statusIndicator={{
            connected: wsConnectionStatus.status === "connected",
            label: "Live",
          }}
        />
      </div>

      <div>
        <DashboardMetricCard
          icon={SvelteComponent_1}
          iconColor="text-tokyo-night-cyan"
          value={formatNumber(enhancedStreamStats.totalViewers)}
          valueColor="text-tokyo-night-cyan"
          label="Total Viewers"
        />
      </div>

      <div>
        <DashboardMetricCard
          icon={SvelteComponent_2}
          iconColor="text-tokyo-night-purple"
          value={`${formatBytes(totalBandwidth)}/s`}
          valueColor="text-tokyo-night-purple"
          label="Bandwidth"
        />
      </div>

      <div>
        <DashboardMetricCard
          icon={SvelteComponent_3}
          iconColor="text-tokyo-night-yellow"
          value={`${usageData?.stream_hours ? formatNumber(usageData.stream_hours) : "0"}h`}
          valueColor="text-tokyo-night-yellow"
          label="Stream Hours (7d)"
        />
      </div>
    </GridSeam>

    <!-- Main Content Grid -->
    <div class="grid grid-cols-1 lg:grid-cols-3 gap-6">
      <!-- Streams Overview Card -->
      <div class="panel">
        <div class="mb-6 pb-4 border-b border-tokyo-night-fg-gutter/30">
          <h2 class="text-xl font-semibold text-tokyo-night-fg mb-2">
            Stream Central
          </h2>
          <p class="text-tokyo-night-fg-dark">
            Everything you need to manage your streams in one place
          </p>
        </div>

        <div class="space-y-6">
          <!-- Stream Stats Grid with Real-time Data -->
          <StreamStatsGrid
            totalStreams={enhancedStreamStats.total}
            liveStreams={enhancedStreamStats.live}
            totalViewers={formatNumber(enhancedStreamStats.totalViewers)}
          />

          <!-- Real-time Connection Status -->
          <ConnectionStatusBanner
            visible={wsConnectionStatus.status !== "connected"}
            message={wsConnectionStatus.message}
          />

          <!-- Primary Stream Details -->
          <PrimaryStreamCard
            stream={primaryStream}
            onCopyStreamKey={copyToClipboard}
            createStreamUrl={resolve("/streams")}
          />

          <!-- Quick Actions -->
          <div class="flex space-x-3">
            <Button href={resolve("/streams")} class="flex-1 justify-center gap-2">
              <SvelteComponent_6 class="w-4 h-4" />
              Manage Streams
            </Button>
            <Button
              href={resolve("/analytics")}
              variant="outline"
              class="flex-1 justify-center gap-2"
            >
              <SvelteComponent_7 class="w-4 h-4" />
              View Analytics
            </Button>
          </div>
        </div>
      </div>

      <!-- Usage & Billing Overview Card -->
      <div class="panel">
        <div class="mb-6 pb-4 border-b border-tokyo-night-fg-gutter/30">
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
            <Button
              href={resolve("/analytics/usage")}
              class="w-full justify-center gap-2"
            >
              <SvelteComponent_8 class="w-4 h-4" />
              View Detailed Usage & Costs
            </Button>
            <Button
              href={resolve("/account/billing")}
              variant="outline"
              class="w-full justify-center gap-2"
            >
              <SvelteComponent_9 class="w-4 h-4" />
              Manage Billing
            </Button>
          </div>
        </div>
      </div>

      <!-- System Health & Real-time Status Card -->
      <div class="panel">
        <div class="mb-6 pb-4 border-b border-tokyo-night-fg-gutter/30">
          <h2 class="text-xl font-semibold text-tokyo-night-fg mb-2">
            Platform Status
          </h2>
          <p class="text-tokyo-night-fg-dark">
            Service health and real-time metrics
          </p>
        </div>

        <div class="space-y-6">
          <!-- Connection Status -->
          <ServiceStatusList
            wsStatus={wsConnectionStatus}
            analyticsLoaded={!!usageData}
          />

          <!-- Real-time Stream Health -->
          <LiveStreamHealthCards
            {liveMetrics}
            {formatBytes}
          />

          <!-- Quick Actions for System -->
          <div class="space-y-2">
            <Button
              href={resolve("/analytics/realtime")}
              class="w-full justify-center gap-2"
            >
              <SvelteComponent_10 class="w-4 h-4" />
              Real-time Analytics
            </Button>
            <Button
              href={resolve("/analytics")}
              variant="outline"
              class="w-full justify-center gap-2"
            >
              <SvelteComponent_11 class="w-4 h-4" />
              Full Analytics
            </Button>
          </div>
        </div>
      </div>
    </div>

    <!-- Quick Setup Guide - Full Width Section -->
    <ObsSetupGuide streamKey={primaryStream?.streamKey || null} />
  </div>
{/if}

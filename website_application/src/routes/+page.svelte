<script lang="ts">
  import { onMount } from "svelte";
  import { resolve } from "$app/paths";
  import { auth } from "$lib/stores/auth";
  import {
    GetPlatformOverviewStore,
    GetBillingStatusStore,
    GetStreamsConnectionStore,
  } from "$houdini";
  import { toast } from "$lib/stores/toast.js";
  import { getIconComponent } from "$lib/iconUtils";
  import { Button } from "$lib/components/ui/button";
  import { GridSeam, SectionDivider } from "$lib/components/layout";
  import DashboardMetricCard from "$lib/components/shared/DashboardMetricCard.svelte";
  import StreamStatsGrid from "$lib/components/dashboard/StreamStatsGrid.svelte";
  import ConnectionStatusBanner from "$lib/components/dashboard/ConnectionStatusBanner.svelte";
  import PrimaryStreamCard from "$lib/components/dashboard/PrimaryStreamCard.svelte";
  import ServiceStatusList from "$lib/components/dashboard/ServiceStatusList.svelte";
  import LiveStreamHealthCards from "$lib/components/dashboard/LiveStreamHealthCards.svelte";
  import ObsSetupGuide from "$lib/components/dashboard/ObsSetupGuide.svelte";
  import { EventLog, type StreamEvent } from "$lib/components/stream-details";
  import {
    realtimeStreams,
    streamMetrics,
    realtimeViewers,
    connectionStatus,
  } from "$lib/stores/realtime";

  // Houdini stores
  const platformOverviewStore = new GetPlatformOverviewStore();
  const billingStatusStore = new GetBillingStatusStore();
  const streamsConnectionStore = new GetStreamsConnectionStore();

  // User type from auth store
  interface UserData {
    email?: string;
    first_name?: string;
    last_name?: string;
    [key: string]: unknown;
  }

  let isAuthenticated = $state(false);
  let user = $state<UserData | null>(null);
  let loading = $state(true);

  // Stream metrics type for real-time data (matches realtime.ts StreamMetric)
  interface StreamMetrics {
    // Bandwidth in bits per second (from ViewerMetrics subscription)
    bandwidthInBps?: number;
    bandwidthOutBps?: number;
    // Legacy fields (not currently populated)
    bitrate_kbps?: number;
    video_codec?: string;
    audio_codec?: string;
  }

  // Real-time dashboard data
  let realtimeData = $state<Array<{ status?: string }>>([]);
  let liveMetrics = $state<Record<string, StreamMetrics>>({});
  let totalRealtimeViewers = $state(0);
  let wsConnectionStatus = $state<{ status: string; message: string }>({ status: "disconnected", message: "Disconnected" });

  // Service status tracking
  let controlPlaneStatus = $state<"connected" | "loading" | "error">("loading");
  let dataPlaneStatus = $state<"connected" | "error">("error");

  // Platform events for event log
  let platformEvents = $state<StreamEvent[]>([]);
  let eventLogCollapsed = $state(true);

  // Helper to add platform events
  function addPlatformEvent(
    type: StreamEvent["type"],
    message: string,
    details?: string,
    streamName?: string
  ) {
    const event: StreamEvent = {
      id: `event-${Date.now()}-${Math.random().toString(36).substr(2, 9)}`,
      timestamp: new Date().toISOString(),
      type,
      message,
      details,
      streamName,
    };
    platformEvents = [event, ...platformEvents].slice(0, 50); // Keep last 50 events
  }

  // Derived data from Houdini stores
  let streams = $derived($streamsConnectionStore.data?.streamsConnection?.edges?.map(e => e.node) ?? []);
  let usageData = $derived($platformOverviewStore.data?.platformOverview ? {
    totalStreams: $platformOverviewStore.data.platformOverview.totalStreams || 0,
    totalViewers: $platformOverviewStore.data.platformOverview.totalViewers || 0,
    totalBandwidth: $platformOverviewStore.data.platformOverview.totalBandwidth || 0,
    streamHours: $platformOverviewStore.data.platformOverview.streamHours || 0,
    egressGb: $platformOverviewStore.data.platformOverview.egressGb || 0,
    peakViewers: $platformOverviewStore.data.platformOverview.peakViewers || 0,
    // New viewer consumption metrics
    viewerHours: $platformOverviewStore.data.platformOverview.viewerHours || 0,
    deliveredMinutes: $platformOverviewStore.data.platformOverview.deliveredMinutes || 0,
    uniqueViewers: $platformOverviewStore.data.platformOverview.uniqueViewers || 0,
    ingestHours: $platformOverviewStore.data.platformOverview.ingestHours || 0,
    peakConcurrentViewers: $platformOverviewStore.data.platformOverview.peakConcurrentViewers || 0,
  } : null);
  let billingStatus = $derived($billingStatusStore.data?.billingStatus ?? null);

  // Subscribe to auth store (user info only, streams fetched separately)
  auth.subscribe((authState) => {
    isAuthenticated = authState.isAuthenticated;
    user = (authState.user as unknown as UserData) || null;
    loading = authState.loading;
    // Update control plane status based on auth state
    if (authState.loading) {
      controlPlaneStatus = "loading";
    } else if (authState.isAuthenticated) {
      controlPlaneStatus = "connected";
    } else {
      controlPlaneStatus = "error";
    }
  });

  // Track previous stream statuses to detect changes
  let prevStreamStatuses = $state<Record<string, string>>({});

  // Subscribe to real-time data
  realtimeStreams.subscribe((data) => {
    const newData = data as Array<{ status?: string; name?: string }>;
    // Check for status changes
    newData.forEach((stream) => {
      const streamName = stream.name || "Unknown";
      const prevStatus = prevStreamStatuses[streamName];
      if (stream.status && prevStatus !== stream.status) {
        if (stream.status === "live") {
          addPlatformEvent("stream_start", `Stream started`, undefined, streamName);
        } else if (stream.status === "offline" && prevStatus === "live") {
          addPlatformEvent("stream_end", `Stream ended`, undefined, streamName);
        }
        prevStreamStatuses[streamName] = stream.status;
      }
    });
    realtimeData = newData;
  });

  streamMetrics.subscribe((data) => {
    liveMetrics = data as Record<string, StreamMetrics>;
  });

  realtimeViewers.subscribe((data) => {
    totalRealtimeViewers = data;
  });

  connectionStatus.subscribe((status) => {
    // Log connection status changes to platform events
    if (wsConnectionStatus.status !== status.status) {
      if (status.status === "connected") {
        addPlatformEvent("info", "Real-time connection established");
      } else if (status.status === "disconnected") {
        addPlatformEvent("warning", "Real-time connection lost", status.message);
      } else if (status.status === "error") {
        addPlatformEvent("error", "Connection error", status.message);
      }
    }
    wsConnectionStatus = status;
  });

  onMount(async () => {
    await auth.checkAuth();

    // Load dashboard data if authenticated (layout handles redirects)
    if (isAuthenticated) {
      addPlatformEvent("info", "Dashboard loaded", `Welcome back!`);
      await loadDashboardData();
    }
  });

  async function loadDashboardData() {
    try {
      // Load platform overview, billing status, and streams in parallel
      await Promise.all([
        platformOverviewStore.fetch({
          variables: {
            timeRange: {
              start: new Date(Date.now() - 24 * 60 * 60 * 1000).toISOString(),
              end: new Date().toISOString(),
            },
          },
        }),
        billingStatusStore.fetch(),
        streamsConnectionStore.fetch({ variables: { first: 50 } }),
      ]);
      dataPlaneStatus = "connected";
    } catch (err) {
      console.error("Failed to load dashboard data:", err);
      dataPlaneStatus = "error";
    }
  }

  // Note: Authentication redirects are handled by +layout.svelte

  // Get primary stream (most recently active or first stream)
  let primaryStream = $derived(
    streams && streams.length > 0
      ? streams.find((s) => s.metrics?.status === "LIVE") || streams[0]
      : null
  );

  // Enhanced stream stats with real-time data
  let enhancedStreamStats = $derived({
    total: streams?.length || 0,
    live:
      streams?.filter((s) => s.metrics?.status === "LIVE").length ||
      realtimeData?.filter((s) => s.status === "live").length ||
      0,
    totalViewers:
      totalRealtimeViewers > 0
        ? totalRealtimeViewers
        : streams?.reduce((sum, s) => sum + (s.metrics?.currentViewers || 0), 0) || 0,
    realtimeStreams: realtimeData?.length || 0,
  });

  // Calculate total bandwidth from live metrics (bandwidthInBps/OutBps are in bits/sec)
  let totalBandwidth = $derived(Object.values(liveMetrics).reduce((total: number, stream) => {
    return total + (stream.bandwidthInBps || 0) + (stream.bandwidthOutBps || 0);
  }, 0));

  function copyToClipboard(text: string) {
    navigator.clipboard.writeText(text);
    toast.success("Stream key copied to clipboard!");
  }

  function formatBytes(bytes: number): string {
    if (bytes === 0) return "0 B";
    const k = 1024;
    const sizes = ["B", "KB", "MB", "GB", "TB"];
    const i = Math.floor(Math.log(bytes) / Math.log(k));
    return parseFloat((bytes / Math.pow(k, i)).toFixed(1)) + " " + sizes[i];
  }

  function formatNumber(num: number): string {
    return new Intl.NumberFormat().format(Math.round(num));
  }
</script>

<svelte:head>
  <title>Dashboard - FrameWorks</title>
</svelte:head>

<div class="h-full flex flex-col">
  {#if loading}
    <div class="px-4 sm:px-6 lg:px-8 py-6">
      <div class="flex items-center justify-center min-h-64">
        <div class="loading-spinner w-8 h-8"></div>
      </div>
    </div>
  {:else if isAuthenticated}
    {@const PlayIcon = getIconComponent('Play')}
    {@const UsersIcon = getIconComponent('Users')}
    {@const WifiIcon = getIconComponent('Wifi')}
    {@const ClockIcon = getIconComponent('Clock')}
    {@const VideoIcon = getIconComponent('Video')}
    {@const ChartLineIcon = getIconComponent('ChartLine')}
    {@const CreditCardIcon = getIconComponent('CreditCard')}
    {@const ZapIcon = getIconComponent('Zap')}
    {@const GlobeIcon = getIconComponent('Globe')}
    {@const GaugeIcon = getIconComponent('Gauge')}
    {@const ServerIcon = getIconComponent('Server')}
    {@const LayoutDashboardIcon = getIconComponent('LayoutDashboard')}
    <!-- Fixed Page Header -->
    <div class="px-4 sm:px-6 lg:px-8 py-4 border-b border-[hsl(var(--tn-fg-gutter)/0.3)] shrink-0">
      <div class="flex items-center gap-3">
        <LayoutDashboardIcon class="w-5 h-5 text-primary" />
        <div>
          <h1 class="text-xl font-bold text-foreground">Dashboard</h1>
          <p class="text-sm text-muted-foreground">
            Welcome back, {user?.email?.split("@")[0] || "Streamer"}
          </p>
        </div>
      </div>
    </div>

    <!-- Scrollable Content -->
    <div class="flex-1 overflow-y-auto">
    <div class="page-transition">

      <!-- Real-time Dashboard Stats (4→2×2→1 responsive) -->
      <GridSeam cols={4} stack="2x2" surface="panel" flush={true} class="mb-0">
        <div>
          <DashboardMetricCard
            icon={PlayIcon}
            iconColor="text-destructive"
            value={enhancedStreamStats.live}
            valueColor="text-success"
            label="Streams Live"
            statusIndicator={{
              connected: wsConnectionStatus.status === "connected",
              label: "Live",
            }}
          />
        </div>

        <div>
          <DashboardMetricCard
            icon={UsersIcon}
            iconColor="text-info"
            value={formatNumber(enhancedStreamStats.totalViewers)}
            valueColor="text-info"
            label="Total Viewers"
          />
        </div>

        <div>
          <DashboardMetricCard
            icon={WifiIcon}
            iconColor="text-accent-purple"
            value={`${formatBytes(totalBandwidth)}/s`}
            valueColor="text-accent-purple"
            label="Bandwidth"
          />
        </div>

        <div>
          <DashboardMetricCard
            icon={ClockIcon}
            iconColor="text-warning"
            value={`${usageData?.streamHours ? formatNumber(usageData.streamHours) : "0"}h`}
            valueColor="text-warning"
            label="Stream Hours (24h)"
          />
        </div>
      </GridSeam>

      <SectionDivider class="my-8" />

      <!-- Main Content Grid (seamed layout, no outer padding) -->
      <div class="dashboard-grid">
        <!-- Streams Overview Slab -->
        <div class="slab">
          <div class="slab-header">
            <h3>Streams Overview</h3>
          </div>
          <div class="slab-body--padded space-y-4">
            <StreamStatsGrid
              totalStreams={enhancedStreamStats.total}
              liveStreams={enhancedStreamStats.live}
              totalViewers={formatNumber(enhancedStreamStats.totalViewers)}
            />

            <ConnectionStatusBanner
              visible={wsConnectionStatus.status !== "connected"}
              message={wsConnectionStatus.message}
            />

            <PrimaryStreamCard
              stream={primaryStream}
              onCopyStreamKey={copyToClipboard}
              createStreamUrl={resolve("/streams")}
            />
          </div>
          <div class="slab-actions slab-actions--row">
            <Button href={resolve("/streams")} variant="ghost" class="gap-2">
              <VideoIcon class="w-4 h-4" />
              Manage Streams
            </Button>
            <Button href={resolve("/analytics")} variant="ghost" class="gap-2">
              <ChartLineIcon class="w-4 h-4" />
              Stream Analytics
            </Button>
          </div>
        </div>

        <!-- Usage & Billing Slab -->
        <div class="slab">
          <div class="slab-header">
            <h3>Usage & Billing</h3>
          </div>
          <div class="slab-body--padded">
            {#if billingStatus}
              <div class="space-y-4">
                <div class="flex items-center justify-between">
                  <span class="text-foreground font-medium">
                    {billingStatus.currentTier?.name || "Free"} Plan
                  </span>
                  <span class="bg-success/20 text-success px-2 py-1 text-xs capitalize">
                    {billingStatus.billingStatus || "active"}
                  </span>
                </div>

                <div class="grid grid-cols-2 gap-4 text-sm">
                  <div>
                    <p class="text-muted-foreground">Monthly Cost</p>
                    <p class="font-semibold text-foreground text-lg">
                      {billingStatus.currentTier?.basePrice
                        ? `$${billingStatus.currentTier.basePrice}`
                        : "Free"}
                    </p>
                  </div>
                  <div>
                    <p class="text-muted-foreground">Usage (24h)</p>
                    <p class="font-semibold text-foreground text-lg">
                      {usageData?.streamHours
                        ? `${formatNumber(usageData.streamHours)}h`
                        : "0h"}
                    </p>
                  </div>
                </div>

                {#if usageData && (usageData.egressGb > 0 || usageData.peakViewers > 0 || usageData.viewerHours > 0)}
                  <div class="grid grid-cols-2 gap-4 text-sm pt-4 border-t border-border/30">
                    <div>
                      <p class="text-muted-foreground">Egress (24h)</p>
                      <p class="font-semibold text-primary">
                        {usageData.egressGb ? formatNumber(usageData.egressGb) : "0"} GB
                      </p>
                    </div>
                    <div>
                      <p class="text-muted-foreground">Viewer Hours</p>
                      <p class="font-semibold text-info">
                        {usageData.viewerHours ? formatNumber(usageData.viewerHours) : "0"}h
                      </p>
                    </div>
                    <div>
                      <p class="text-muted-foreground">Unique Viewers</p>
                      <p class="font-semibold text-accent-purple">
                        {usageData.uniqueViewers || 0}
                      </p>
                    </div>
                    <div>
                      <p class="text-muted-foreground">Peak Concurrent</p>
                      <p class="font-semibold text-success">
                        {usageData.peakConcurrentViewers || 0}
                      </p>
                    </div>
                    <div class="col-span-2">
                      <p class="text-muted-foreground">Delivered Minutes</p>
                      <p class="font-semibold text-warning">
                        {usageData.deliveredMinutes ? formatNumber(usageData.deliveredMinutes) : "0"}
                      </p>
                    </div>
                  </div>
                {/if}
              </div>
            {/if}
          </div>
          <div class="slab-actions slab-actions--row">
            <Button href={resolve("/account/billing")} variant="ghost" class="gap-2">
              <CreditCardIcon class="w-4 h-4" />
              Billing
            </Button>
            <Button href={resolve("/analytics/usage")} variant="ghost" class="gap-2">
              <GaugeIcon class="w-4 h-4" />
              Usage Analytics
            </Button>
          </div>
        </div>

        <!-- System Health Slab -->
        <div class="slab">
          <div class="slab-header">
            <h3>System Health</h3>
          </div>
          <div class="slab-body--padded space-y-4">
            <ServiceStatusList
              wsStatus={wsConnectionStatus}
              controlPlane={controlPlaneStatus}
              dataPlane={dataPlaneStatus}
            />

            <LiveStreamHealthCards
              {liveMetrics}
              {formatBytes}
            />
          </div>
          <div class="slab-actions slab-actions--row">
            <Button href={resolve("/analytics")} variant="ghost" class="gap-2">
              <ChartLineIcon class="w-4 h-4" />
              Analytics
            </Button>
            <Button href={resolve("/analytics/geographic")} variant="ghost" class="gap-2">
              <GlobeIcon class="w-4 h-4" />
              Geographic
            </Button>
            <Button href={resolve("/infrastructure")} variant="ghost" class="gap-2">
              <ServerIcon class="w-4 h-4" />
              Infrastructure
            </Button>
          </div>
        </div>

        <!-- Setup Guide Slab -->
        <div class="slab">
          <div class="slab-header">
            <h3>Quick Setup</h3>
          </div>
          <div class="slab-body--padded">
            <ObsSetupGuide streamKey={primaryStream?.streamKey || null} />
          </div>
        </div>

        <!-- Recent Activity Event Log -->
        <EventLog
          class="col-span-full"
          events={platformEvents}
          title="Recent Activity"
          maxVisible={5}
          collapsed={eventLogCollapsed}
          onToggle={() => (eventLogCollapsed = !eventLogCollapsed)}
          showStreamName={true}
          emptyMessage="No recent activity. Events will appear here as streams go live and viewers connect."
        />
      </div>
    </div>
    </div>
  {/if}
</div>

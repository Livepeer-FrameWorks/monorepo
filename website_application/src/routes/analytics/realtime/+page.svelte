<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { resolve } from "$app/paths";
  import { auth } from "$lib/stores/auth";
  import {
    GetStreamsStore,
    GetPlatformOverviewStore,
    GetNodesStore,
    SystemHealthStore
  } from "$houdini";
  import { toast } from "$lib/stores/toast.js";
  import { getIconComponent } from "$lib/iconUtils";
  import { Button } from "$lib/components/ui/button";
  import { GridSeam } from "$lib/components/layout";
  import DashboardMetricCard from "$lib/components/shared/DashboardMetricCard.svelte";
  import { StreamCard } from "$lib/components/cards";

  // Houdini stores
  const streamsStore = new GetStreamsStore();
  const platformOverviewStore = new GetPlatformOverviewStore();
  const nodesStore = new GetNodesStore();
  const systemHealthSub = new SystemHealthStore();

  // Types from Houdini
  type StreamData = NonNullable<NonNullable<typeof $streamsStore.data>["streams"]>[0];
  type NodeData = NonNullable<NonNullable<typeof $nodesStore.data>["nodes"]>[0];

  let isAuthenticated = false;
  let loading = $derived($streamsStore.fetching || $platformOverviewStore.fetching || $nodesStore.fetching);
  let refreshInterval = $state<ReturnType<typeof setInterval> | null>(null);

  // Derived data from stores
  let streams = $derived($streamsStore.data?.streams ?? []);
  let nodeData = $derived($nodesStore.data?.nodes ?? []);

  // Real-time metrics aggregated from actual streams
  let liveMetrics = $state({
    totalViewers: 0,
    activeStreams: 0,
    totalBandwidthIn: 0,
    totalBandwidthOut: 0,
    lastUpdated: new Date()
  });

  // Real viewer activity from actual stream metrics (last 20 data points)
  let viewerActivity = $state<{time: Date, viewers: number}[]>([]);

  // Subscribe to auth store
  auth.subscribe((authState) => {
    isAuthenticated = authState.isAuthenticated;
  });

  onMount(async () => {
    if (!isAuthenticated) {
      await auth.checkAuth();
    }
    await loadAllData();
    startRealTimeUpdates();
    systemHealthSub.listen();
  });

  onDestroy(() => {
    if (refreshInterval) {
      clearInterval(refreshInterval);
    }
    systemHealthSub.unlisten();
  });

  async function loadAllData() {
    try {
      await Promise.all([
        streamsStore.fetch(),
        nodesStore.fetch(),
        updateRealTimeMetrics(),
      ]);

      if ($streamsStore.errors?.length) {
        toast.error("Failed to load streams data. Some metrics may be unavailable.");
      }
      if ($nodesStore.errors?.length) {
        toast.warning("Failed to load infrastructure data. Node information may be unavailable.");
      }
    } catch (error) {
      console.error("Failed to load data:", error);
      toast.error("Failed to load data. Please try again.");
    }
  }

  async function updateRealTimeMetrics() {
    try {
      const now = new Date();
      const start = new Date(now.getTime() - 24 * 60 * 60 * 1000);

      await platformOverviewStore.fetch({
        variables: {
          timeRange: {
            start: start.toISOString(),
            end: now.toISOString(),
          }
        }
      });

      const platformData = $platformOverviewStore.data?.platformOverview;
      if (platformData) {
        liveMetrics = {
          totalViewers: platformData.totalViewers || 0,
          activeStreams: platformData.totalStreams || 0,
          totalBandwidthIn: platformData.totalUploadBytes || 0,
          totalBandwidthOut: platformData.totalDownloadBytes || 0,
          lastUpdated: new Date()
        };
      }

      viewerActivity = [
        ...viewerActivity.slice(-19),
        { time: new Date(), viewers: liveMetrics.totalViewers }
      ];

    } catch (error) {
      console.error("Failed to update realtime metrics:", error);
      toast.warning("Failed to update real-time metrics. Data may be outdated.");
    }
  }

  function startRealTimeUpdates() {
    refreshInterval = setInterval(async () => {
      await updateRealTimeMetrics();
    }, 5000);
  }

  function formatBandwidth(bytes: number) {
    if (!bytes) return "0 Kbps";
    const kbps = Math.round((bytes / 1024) * 8);
    if (kbps >= 1000000) {
      return `${(kbps / 1000000).toFixed(1)} Gbps`;
    } else if (kbps >= 1000) {
      return `${(kbps / 1000).toFixed(1)} Mbps`;
    }
    return `${kbps} Kbps`;
  }

  // Calculate max viewer count for chart scaling
  let maxViewers = $derived(viewerActivity.length > 0 ? Math.max(...viewerActivity.map(point => point.viewers)) : 1);

  // Icons
  const ZapIcon = getIconComponent('Zap');
  const RefreshIcon = getIconComponent('RefreshCw');
  const UsersIcon = getIconComponent('Users');
  const VideoIcon = getIconComponent('Video');
  const DownloadIcon = getIconComponent('Download');
  const UploadIcon = getIconComponent('Upload');
  const TrendingUpIcon = getIconComponent('TrendingUp');
  const ServerIcon = getIconComponent('Server');
  const PlusIcon = getIconComponent('Plus');
  const ChartLineIcon = getIconComponent('ChartLine');
  const GlobeIcon = getIconComponent('Globe2');
</script>

<svelte:head>
  <title>Real-time Analytics - FrameWorks</title>
</svelte:head>

<div class="h-full flex flex-col">
  <!-- Fixed Page Header -->
  <div class="px-4 sm:px-6 lg:px-8 py-4 border-b border-border shrink-0">
    <div class="flex justify-between items-center">
      <div class="flex items-center gap-3">
        <ZapIcon class="w-5 h-5 text-primary" />
        <div>
          <h1 class="text-xl font-bold text-foreground">Real-time</h1>
          <p class="text-sm text-muted-foreground">
            Live streaming metrics from your MistServer infrastructure
          </p>
        </div>
      </div>
      <div class="flex items-center gap-3">
        <div class="flex items-center gap-2">
          <div class="w-2 h-2 bg-success rounded-full animate-pulse"></div>
          <span class="text-sm text-muted-foreground">
            Updated {liveMetrics.lastUpdated.toLocaleTimeString()}
          </span>
        </div>
        <Button variant="outline" size="sm" onclick={updateRealTimeMetrics}>
          <RefreshIcon class="w-4 h-4 mr-2" />
          Refresh
        </Button>
      </div>
    </div>
  </div>

  <!-- Scrollable Content -->
  <div class="flex-1 overflow-y-auto">
  {#if loading}
    <div class="px-4 sm:px-6 lg:px-8 py-6">
      <div class="flex items-center justify-center min-h-64">
        <div class="loading-spinner w-8 h-8"></div>
      </div>
    </div>
  {:else}
    <div class="page-transition">

      <!-- Live Metrics Stats -->
      <GridSeam cols={4} stack="2x2" surface="panel" flush={true} class="mb-0">
        <div>
          <DashboardMetricCard
            icon={UsersIcon}
            iconColor="text-primary"
            value={liveMetrics.totalViewers}
            valueColor="text-primary"
            label="Total Viewers"
            statusIndicator={{ connected: true, label: "Live" }}
          />
        </div>
        <div>
          <DashboardMetricCard
            icon={VideoIcon}
            iconColor="text-success"
            value={liveMetrics.activeStreams}
            valueColor="text-success"
            label="Active Streams"
            subtitle={`of ${streams.length} total`}
          />
        </div>
        <div>
          <DashboardMetricCard
            icon={DownloadIcon}
            iconColor="text-info"
            value={formatBandwidth(liveMetrics.totalBandwidthIn)}
            valueColor="text-info"
            label="Bandwidth In"
          />
        </div>
        <div>
          <DashboardMetricCard
            icon={UploadIcon}
            iconColor="text-warning"
            value={formatBandwidth(liveMetrics.totalBandwidthOut)}
            valueColor="text-warning"
            label="Bandwidth Out"
          />
        </div>
      </GridSeam>

      <!-- Main Content Grid -->
      <div class="dashboard-grid">
        <!-- Live Viewer Activity Slab -->
        <div class="slab xl:col-span-2">
          <div class="slab-header">
            <div class="flex items-center gap-2">
              <TrendingUpIcon class="w-4 h-4 text-info" />
              <h3>Live Viewer Activity</h3>
            </div>
          </div>
          <div class="slab-body--padded">
            {#if viewerActivity.length > 0}
              <div class="bg-muted/30 p-4 border border-border/30">
                <div class="flex items-end gap-1 h-40">
                  {#each viewerActivity as point, i (point.time.getTime ? point.time.getTime() : i)}
                    <div
                      class="bg-primary flex-1 rounded-t transition-all duration-500 relative group min-w-[8px]"
                      style="height: {maxViewers > 0 ? Math.max((point.viewers / maxViewers) * 100, 2) : 2}%"
                      title="{point.time.toLocaleTimeString()}: {point.viewers} viewers"
                    >
                      {#if i === viewerActivity.length - 1}
                        <div class="absolute -top-7 left-1/2 transform -translate-x-1/2 bg-card border border-border px-2 py-0.5 rounded text-xs whitespace-nowrap animate-pulse">
                          {point.viewers}
                        </div>
                      {/if}
                    </div>
                  {/each}
                </div>
                <div class="flex justify-between text-xs text-muted-foreground mt-3 pt-2 border-t border-border/30">
                  <span>{viewerActivity.length > 0 ? `${Math.floor((viewerActivity.length - 1) * 5 / 60)}m ago` : ''}</span>
                  <span>Now</span>
                </div>
              </div>
              <div class="flex items-center justify-between text-sm mt-4">
                <div class="flex items-center gap-2">
                  <div class="w-3 h-3 bg-primary rounded"></div>
                  <span class="text-muted-foreground">Total Concurrent Viewers</span>
                </div>
                <div class="text-muted-foreground">
                  Peak: <span class="font-semibold text-foreground">{maxViewers}</span>
                </div>
              </div>
            {:else}
              <div class="flex items-center justify-center h-48 border border-border/30 bg-muted/20">
                <p class="text-muted-foreground">Collecting real-time data...</p>
              </div>
            {/if}
          </div>
          <div class="slab-actions slab-actions--row">
            <Button href={resolve("/analytics")} variant="ghost" class="gap-2">
              <ChartLineIcon class="w-4 h-4" />
              Overview
            </Button>
            <Button href={resolve("/analytics/geographic")} variant="ghost" class="gap-2">
              <GlobeIcon class="w-4 h-4" />
              Geographic
            </Button>
          </div>
        </div>

        <!-- Infrastructure Nodes Slab -->
        <div class="slab">
          <div class="slab-header">
            <div class="flex items-center gap-2">
              <ServerIcon class="w-4 h-4 text-info" />
              <h3>Infrastructure Nodes</h3>
            </div>
          </div>
          <div class="slab-body--padded">
            {#if nodeData && nodeData.length > 0}
              <div class="space-y-3">
                {#each nodeData as node (node.id)}
                  <div class="flex items-center justify-between p-3 border border-border/30 bg-muted/20">
                    <div>
                      <h4 class="font-medium text-foreground">{node.nodeName}</h4>
                      <p class="text-xs text-muted-foreground">{node.region || 'Unknown region'}</p>
                      {#if node.externalIp}
                        <p class="text-[10px] text-muted-foreground font-mono">{node.externalIp}</p>
                      {/if}
                    </div>
                    <div class="text-right">
                      <span class="text-xs px-2 py-0.5 rounded-full {node.status === 'HEALTHY' ? 'bg-success/20 text-success' : 'bg-destructive/20 text-destructive'}">
                        {node.status}
                      </span>
                      <p class="text-[10px] text-muted-foreground mt-1">
                        {node.lastHeartbeat ? new Date(node.lastHeartbeat).toLocaleTimeString() : 'N/A'}
                      </p>
                    </div>
                  </div>
                {/each}
              </div>
            {:else}
              <div class="flex items-center justify-center h-32 border border-border/30 bg-muted/20">
                <p class="text-muted-foreground text-sm">No nodes configured</p>
              </div>
            {/if}
          </div>
          <div class="slab-actions">
            <Button href={resolve("/infrastructure")} variant="ghost" class="gap-2">
              <ServerIcon class="w-4 h-4" />
              View Infrastructure
            </Button>
          </div>
        </div>

        <!-- Active Streams Slab -->
        <div class="slab col-span-full">
          <div class="slab-header">
            <div class="flex items-center gap-2">
              <VideoIcon class="w-4 h-4 text-success" />
              <h3>Active Streams</h3>
            </div>
          </div>
          <div class="slab-body--padded">
            {#if streams.length > 0}
              <div class="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-4">
                {#each streams as stream (stream.id ?? stream.name)}
                  <StreamCard
                    {stream}
                    nodeName={nodeData && nodeData.length > 0 ? nodeData[0].nodeName : undefined}
                  />
                {/each}
              </div>
            {:else}
              <div class="text-center py-12">
                <VideoIcon class="w-6 h-6 text-muted-foreground mx-auto mb-4" />
                <h3 class="text-lg font-semibold text-foreground mb-2">No Streams Found</h3>
                <p class="text-muted-foreground mb-4">Create your first stream to see real-time analytics</p>
                <Button href={resolve("/streams")} class="gap-2">
                  <PlusIcon class="w-4 h-4" />
                  Create Stream
                </Button>
              </div>
            {/if}
          </div>
        </div>
      </div>
    </div>
  {/if}
  </div>
</div>

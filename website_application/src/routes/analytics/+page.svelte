<script>
  import { onMount, onDestroy } from "svelte";
  import { goto } from "$app/navigation";
  import { base } from "$app/paths";
  import { auth } from "$lib/stores/auth";
  import { streamsService } from "$lib/graphql/services/streams.js";
  import { analyticsService } from "$lib/graphql/services/analytics.js";
  import { toast } from "$lib/stores/toast.js";
  import LoadingCard from "$lib/components/LoadingCard.svelte";
  import SkeletonLoader from "$lib/components/SkeletonLoader.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import { getIconComponent } from "$lib/iconUtils.js";

  let isAuthenticated = false;
  let user = null;
  let loading = true;
  
  // Data
  let streams = [];
  let selectedStream = null;
  let analyticsData = null;
  let viewerMetrics = [];
  let platformOverview = null;
  
  // Real-time subscriptions
  let viewerMetricsSubscription = null;

  // Subscribe to auth store
  auth.subscribe((authState) => {
    isAuthenticated = authState.isAuthenticated;
    user = authState.user;
    streams = authState.streams || [];
  });

  onMount(async () => {
    if (!isAuthenticated) {
      await auth.checkAuth();
    }
    await loadData();
  });

  onDestroy(() => {
    if (viewerMetricsSubscription) {
      viewerMetricsSubscription.unsubscribe();
    }
  });

  async function loadData() {
    try {
      loading = true;
      
      // Load streams and platform overview
      const [streamsData, platformData] = await Promise.all([
        streamsService.getStreams().catch(() => []),
        analyticsService.getPlatformOverview().catch(() => null)
      ]);
      
      streams = streamsData || [];
      platformOverview = platformData;
      
      if (streams.length > 0) {
        selectedStream = streams[0];
        await loadAnalyticsForStream(selectedStream.id);
        startRealTimeSubscriptions();
      }
      
    } catch (error) {
      console.error('Failed to load data:', error);
      toast.error('Failed to load analytics data. Please refresh the page.');
    } finally {
      loading = false;
    }
  }

  async function loadAnalyticsForStream(streamId) {
    if (!streamId) return;
    
    try {
      // Load stream analytics and viewer metrics
      const [streamAnalytics, metrics] = await Promise.all([
        analyticsService.getStreamAnalytics(streamId).catch(() => null),
        analyticsService.getViewerMetrics(streamId).catch(() => [])
      ]);
      
      analyticsData = streamAnalytics;
      viewerMetrics = metrics || [];
      
    } catch (error) {
      console.error('Failed to load analytics for stream:', error);
      toast.warning('Failed to load analytics for selected stream. Some data may be unavailable.');
    }
  }

  function startRealTimeSubscriptions() {
    if (!selectedStream || !user) return;
    
    // Clean up existing subscriptions
    if (viewerMetricsSubscription) {
      viewerMetricsSubscription.unsubscribe();
    }
    
    // Subscribe to real-time viewer metrics
    viewerMetricsSubscription = streamsService.subscribeToViewerMetrics(
      selectedStream.id,
      {
        onViewerMetrics: (metrics) => {
          // Add real-time metrics to the array
          viewerMetrics = [...viewerMetrics.slice(-99), {
            timestamp: metrics.timestamp,
            viewerCount: metrics.currentViewers
          }];
        },
        onError: (error) => {
          console.error('Viewer metrics subscription failed:', error);
        }
      }
    );
  }

  async function selectStream(stream) {
    selectedStream = stream;
    await loadAnalyticsForStream(stream.id);
    startRealTimeSubscriptions();
  }

  function formatDate(dateString) {
    return new Date(dateString).toLocaleDateString();
  }

  function formatNumber(num) {
    if (num >= 1000000) {
      return (num / 1000000).toFixed(1) + 'M';
    } else if (num >= 1000) {
      return (num / 1000).toFixed(1) + 'K';
    }
    return num?.toString() || '0';
  }
</script>

<svelte:head>
  <title>Analytics - FrameWorks</title>
</svelte:head>

<div class="min-h-screen bg-tokyo-night-bg text-tokyo-night-fg">
  <div class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8">
    <div class="mb-8">
      <h1 class="text-3xl font-bold text-tokyo-night-blue mb-2">
        Analytics Dashboard
      </h1>
      <p class="text-tokyo-night-comment">
        Monitor your streaming performance and viewer engagement
      </p>
    </div>

    {#if loading}
      <!-- Platform Overview Skeleton -->
      <LoadingCard variant="analytics" className="mb-8" />

      <!-- Stream Selector Skeleton -->
      <div class="bg-tokyo-night-surface rounded-lg p-6 mb-8">
        <SkeletonLoader type="text-lg" className="w-32 mb-4" />
        <div class="grid grid-cols-1 md:grid-cols-3 gap-3">
          {#each Array(3) as _}
            <div class="p-3 border border-tokyo-night-selection rounded-lg">
              <SkeletonLoader type="text" className="w-24 mb-1" />
              <SkeletonLoader type="text-sm" className="w-16" />
            </div>
          {/each}
        </div>
      </div>

      <!-- Stream Analytics Skeleton -->
      <LoadingCard variant="analytics" />
    {:else}
      <!-- Platform Overview -->
      {#if platformOverview}
        <div class="bg-tokyo-night-surface rounded-lg p-6 mb-8">
          <h2 class="text-xl font-semibold mb-4 text-tokyo-night-cyan">Platform Overview</h2>
          <div class="grid grid-cols-2 md:grid-cols-4 gap-4">
            <div class="text-center">
              <div class="text-2xl font-bold text-tokyo-night-blue">{formatNumber(platformOverview.totalStreams)}</div>
              <div class="text-sm text-tokyo-night-comment">Total Streams</div>
            </div>
            <div class="text-center">
              <div class="text-2xl font-bold text-tokyo-night-green">{formatNumber(platformOverview.totalViewers)}</div>
              <div class="text-sm text-tokyo-night-comment">Total Viewers</div>
            </div>
            <div class="text-center">
              <div class="text-2xl font-bold text-tokyo-night-purple">{formatNumber(platformOverview.totalUsers)}</div>
              <div class="text-sm text-tokyo-night-comment">Total Users</div>
            </div>
            <div class="text-center">
              <div class="text-2xl font-bold text-tokyo-night-orange">{(platformOverview.totalBandwidth / 1000000).toFixed(1)}MB</div>
              <div class="text-sm text-tokyo-night-comment">Total Bandwidth</div>
            </div>
          </div>
        </div>
      {/if}

      <!-- Stream Selector -->
      {#if streams.length > 1}
        <div class="bg-tokyo-night-surface rounded-lg p-6 mb-8">
          <h2 class="text-xl font-semibold mb-4 text-tokyo-night-cyan">Select Stream</h2>
          <div class="grid grid-cols-1 md:grid-cols-3 gap-3">
            {#each streams as stream}
              <button
                on:click={() => selectStream(stream)}
                class="p-3 border border-tokyo-night-selection rounded-lg text-left hover:bg-tokyo-night-selection transition-colors {selectedStream?.id === stream.id ? 'border-tokyo-night-blue bg-tokyo-night-selection' : ''}"
              >
                <div class="font-medium">{stream.name}</div>
                <div class="text-sm text-tokyo-night-comment">Status: {stream.status}</div>
              </button>
            {/each}
          </div>
        </div>
      {/if}

      <!-- Stream Analytics -->
      {#if selectedStream}
        <div class="bg-tokyo-night-surface rounded-lg p-6 mb-8">
          <h2 class="text-xl font-semibold mb-4 text-tokyo-night-cyan">Stream Analytics: {selectedStream.name}</h2>
          
          {#if analyticsData}
            <div class="grid grid-cols-2 md:grid-cols-4 gap-4 mb-6">
              <div class="text-center">
                <div class="text-2xl font-bold text-tokyo-night-blue">{formatNumber(analyticsData.totalViews)}</div>
                <div class="text-sm text-tokyo-night-comment">Total Views</div>
              </div>
              <div class="text-center">
                <div class="text-2xl font-bold text-tokyo-night-green">{formatNumber(analyticsData.peakViewers)}</div>
                <div class="text-sm text-tokyo-night-comment">Peak Viewers</div>
              </div>
              <div class="text-center">
                <div class="text-2xl font-bold text-tokyo-night-purple">{Math.round(analyticsData.averageViewers)}</div>
                <div class="text-sm text-tokyo-night-comment">Avg Viewers</div>
              </div>
              <div class="text-center">
                <div class="text-2xl font-bold text-tokyo-night-orange">{formatNumber(analyticsData.uniqueViewers)}</div>
                <div class="text-sm text-tokyo-night-comment">Unique Viewers</div>
              </div>
            </div>
            
            <div class="grid grid-cols-1 md:grid-cols-2 gap-4 mb-6">
              <div>
                <div class="text-sm text-tokyo-night-comment">Total View Time</div>
                <div class="text-lg font-semibold">{Math.round(analyticsData.totalViewTime / 3600)} hours</div>
              </div>
              <div>
                <div class="text-sm text-tokyo-night-comment">Time Range</div>
                <div class="text-lg font-semibold">
                  {#if analyticsData.timeRange}
                    {formatDate(analyticsData.timeRange.start)} - {formatDate(analyticsData.timeRange.end)}
                  {:else}
                    N/A
                  {/if}
                </div>
              </div>
            </div>

            <!-- Stream Health Metrics -->
            {#if analyticsData.currentHealthScore !== null || analyticsData.currentCodec || analyticsData.rebufferCount !== null}
              <div class="border-t border-tokyo-night-selection pt-6">
                <h3 class="text-lg font-semibold text-tokyo-night-purple mb-4">Stream Health & Quality</h3>
                
                <div class="grid grid-cols-2 md:grid-cols-4 gap-4 mb-4">
                  {#if analyticsData.currentHealthScore !== null}
                    <div class="text-center">
                      <div class="text-2xl font-bold {analyticsData.currentHealthScore >= 0.9 ? 'text-green-400' : analyticsData.currentHealthScore >= 0.7 ? 'text-yellow-400' : 'text-red-400'}">
                        {Math.round(analyticsData.currentHealthScore * 100)}%
                      </div>
                      <div class="text-sm text-tokyo-night-comment">Health Score</div>
                    </div>
                  {/if}
                  
                  {#if analyticsData.rebufferCount !== null}
                    <div class="text-center">
                      <div class="text-2xl font-bold {analyticsData.rebufferCount > 10 ? 'text-red-400' : analyticsData.rebufferCount > 5 ? 'text-yellow-400' : 'text-green-400'}">
                        {analyticsData.rebufferCount}
                      </div>
                      <div class="text-sm text-tokyo-night-comment">Rebuffers</div>
                    </div>
                  {/if}
                  
                  {#if analyticsData.alertCount !== null}
                    <div class="text-center">
                      <div class="text-2xl font-bold {analyticsData.alertCount > 5 ? 'text-red-400' : analyticsData.alertCount > 2 ? 'text-yellow-400' : 'text-green-400'}">
                        {analyticsData.alertCount}
                      </div>
                      <div class="text-sm text-tokyo-night-comment">Health Alerts</div>
                    </div>
                  {/if}
                  
                  {#if analyticsData.packetLossPercentage !== null}
                    <div class="text-center">
                      <div class="text-2xl font-bold {analyticsData.packetLossPercentage > 2 ? 'text-red-400' : analyticsData.packetLossPercentage > 1 ? 'text-yellow-400' : 'text-green-400'}">
                        {analyticsData.packetLossPercentage.toFixed(1)}%
                      </div>
                      <div class="text-sm text-tokyo-night-comment">Packet Loss</div>
                    </div>
                  {/if}
                </div>

                <div class="grid grid-cols-1 md:grid-cols-3 gap-4">
                  {#if analyticsData.currentCodec || analyticsData.currentResolution}
                    <div>
                      <div class="text-sm text-tokyo-night-comment">Video Quality</div>
                      <div class="space-y-1">
                        {#if analyticsData.currentResolution}
                          <div class="text-sm font-mono text-tokyo-night-blue">{analyticsData.currentResolution}</div>
                        {/if}
                        {#if analyticsData.currentCodec}
                          <div class="text-sm font-mono text-tokyo-night-purple">{analyticsData.currentCodec}</div>
                        {/if}
                        {#if analyticsData.currentBitrate}
                          <div class="text-sm font-mono text-tokyo-night-green">{Math.round(analyticsData.currentBitrate / 1000)}k</div>
                        {/if}
                        {#if analyticsData.currentFps}
                          <div class="text-sm font-mono text-tokyo-night-orange">{analyticsData.currentFps.toFixed(1)} fps</div>
                        {/if}
                      </div>
                    </div>
                  {/if}

                  {#if analyticsData.frameJitterMs !== null || analyticsData.keyframeStabilityMs !== null}
                    <div>
                      <div class="text-sm text-tokyo-night-comment">Performance</div>
                      <div class="space-y-1">
                        {#if analyticsData.frameJitterMs !== null}
                          <div class="text-sm">
                            <span class="text-tokyo-night-fg">Jitter:</span>
                            <span class="font-mono {analyticsData.frameJitterMs > 30 ? 'text-red-400' : 'text-green-400'}">
                              {analyticsData.frameJitterMs.toFixed(1)}ms
                            </span>
                          </div>
                        {/if}
                        {#if analyticsData.keyframeStabilityMs !== null}
                          <div class="text-sm">
                            <span class="text-tokyo-night-fg">Keyframe:</span>
                            <span class="font-mono text-tokyo-night-cyan">{analyticsData.keyframeStabilityMs.toFixed(1)}ms</span>
                          </div>
                        {/if}
                        {#if analyticsData.qualityTier}
                          <div class="text-sm">
                            <span class="text-tokyo-night-fg">Tier:</span>
                            <span class="font-mono text-tokyo-night-purple">{analyticsData.qualityTier}</span>
                          </div>
                        {/if}
                      </div>
                    </div>
                  {/if}

                  {#if analyticsData.bufferState || analyticsData.currentIssues}
                    <div>
                      <div class="text-sm text-tokyo-night-comment">Buffer & Issues</div>
                      <div class="space-y-1">
                        {#if analyticsData.bufferState}
                          <div class="text-sm">
                            <span class="text-tokyo-night-fg">Buffer:</span>
                            <span class="font-mono {analyticsData.bufferState === 'FULL' ? 'text-green-400' : analyticsData.bufferState === 'DRY' ? 'text-red-400' : 'text-yellow-400'}">
                              {analyticsData.bufferState}
                            </span>
                          </div>
                        {/if}
                        {#if analyticsData.currentIssues}
                          <div class="text-sm text-red-400">{analyticsData.currentIssues}</div>
                        {/if}
                      </div>
                    </div>
                  {/if}
                </div>
              </div>
            {/if}
          {:else}
            <p class="text-tokyo-night-comment">No analytics data available for this stream</p>
          {/if}
        </div>

        <!-- Real-time Viewer Metrics -->
        <div class="bg-tokyo-night-surface rounded-lg p-6">
          <h2 class="text-xl font-semibold mb-4 text-tokyo-night-cyan">Real-time Viewer Metrics</h2>
          
          {#if viewerMetrics.length > 0}
            <div class="space-y-2">
              <div class="text-sm text-tokyo-night-comment">Recent viewer counts:</div>
              <div class="flex flex-wrap gap-2">
                {#each viewerMetrics.slice(-10) as metric}
                  <div class="bg-tokyo-night-bg px-3 py-2 rounded text-sm">
                    <div class="font-medium">{metric.viewerCount} viewers</div>
                    <div class="text-xs text-tokyo-night-comment">
                      {new Date(metric.timestamp).toLocaleTimeString()}
                    </div>
                  </div>
                {/each}
              </div>
            </div>
          {:else}
            <p class="text-tokyo-night-comment">No real-time metrics available</p>
          {/if}
        </div>
      {:else if streams.length === 0}
        <EmptyState 
          icon="BarChart"
          title="No streams found"
          description="Create a stream to start seeing analytics data"
          actionText="Go to Streams"
          onAction={() => goto(`${base}/streams`)}
        />
      {/if}
    {/if}
  </div>
</div>
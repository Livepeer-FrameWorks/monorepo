<script>
  import { onMount, onDestroy } from "svelte";
  import { goto } from "$app/navigation";
  import { resolve } from "$app/paths";
  import { auth } from "$lib/stores/auth";
  import { streamsService } from "$lib/graphql/services/streams.js";
  import { analyticsService } from "$lib/graphql/services/analytics.js";
  import { toast } from "$lib/stores/toast.js";
  import LoadingCard from "$lib/components/LoadingCard.svelte";
  import SkeletonLoader from "$lib/components/SkeletonLoader.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import {
    Card,
    CardHeader,
    CardTitle,
    CardDescription,
    CardContent,
  } from "$lib/components/ui/card";
  import { Button } from "$lib/components/ui/button";
  import { Badge } from "$lib/components/ui/badge";
  import { Panel } from "$lib/components/layout";

  let isAuthenticated = false;
  let user = null;
  let loading = $state(true);

  // Data
  let streams = $state([]);
  let selectedStream = $state(null);
  let analyticsData = $state(null);
  let viewerMetrics = $state([]);
  let platformOverview = $state(null);

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
        analyticsService.getPlatformOverview().catch(() => null),
      ]);

      streams = streamsData || [];
      platformOverview = platformData;

      if (streams.length > 0) {
        selectedStream = streams[0];
        await loadAnalyticsForStream(selectedStream.id);
        startRealTimeSubscriptions();
      }
    } catch (error) {
      console.error("Failed to load data:", error);
      toast.error("Failed to load analytics data. Please refresh the page.");
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
        analyticsService.getViewerMetrics(streamId).catch(() => []),
      ]);

      analyticsData = streamAnalytics;
      viewerMetrics = metrics || [];
    } catch (error) {
      console.error("Failed to load analytics for stream:", error);
      toast.warning(
        "Failed to load analytics for selected stream. Some data may be unavailable.",
      );
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
          viewerMetrics = [
            ...viewerMetrics.slice(-99),
            {
              timestamp: metrics.timestamp,
              viewerCount: metrics.currentViewers,
            },
          ];
        },
        onError: (error) => {
          console.error("Viewer metrics subscription failed:", error);
        },
      },
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
      return (num / 1000000).toFixed(1) + "M";
    } else if (num >= 1000) {
      return (num / 1000).toFixed(1) + "K";
    }
    return num?.toString() || "0";
  }

  function formatTimeRange(range) {
    if (!range?.start || !range?.end) {
      return "N/A";
    }
    return `${formatDate(range.start)} - ${formatDate(range.end)}`;
  }

  function hasValue(value) {
    return value !== null && value !== undefined;
  }

  function healthScoreClass(score) {
    if (!hasValue(score)) return "";
    if (score >= 0.9) return "text-green-400";
    if (score >= 0.7) return "text-yellow-400";
    return "text-red-400";
  }

  function rebufferClass(count) {
    if (!hasValue(count)) return "";
    if (count > 10) return "text-red-400";
    if (count > 5) return "text-yellow-400";
    return "text-green-400";
  }

  function alertClass(count) {
    if (!hasValue(count)) return "";
    if (count > 5) return "text-red-400";
    if (count > 2) return "text-yellow-400";
    return "text-green-400";
  }

  function packetLossClass(loss) {
    if (!hasValue(loss)) return "";
    if (loss > 2) return "text-red-400";
    if (loss > 1) return "text-yellow-400";
    return "text-green-400";
  }

  function bufferStateClass(state) {
    switch (state) {
      case "FULL":
        return "text-green-400";
      case "DRY":
        return "text-red-400";
      default:
        return "text-yellow-400";
    }
  }

  function jitterClass(ms) {
    if (!hasValue(ms)) return "";
    return ms > 30 ? "text-red-400" : "text-green-400";
  }

  const streamSummaryCards = $derived(
    analyticsData
      ? [
          {
            key: "totalViews",
            label: "Total Views",
            value: formatNumber(analyticsData.totalViews),
            tone: "text-tokyo-night-blue",
          },
          {
            key: "peakViewers",
            label: "Peak Viewers",
            value: formatNumber(analyticsData.peakViewers),
            tone: "text-tokyo-night-green",
          },
          {
            key: "avgViewers",
            label: "Avg Viewers",
            value: Math.round(analyticsData.averageViewers ?? 0),
            tone: "text-tokyo-night-purple",
          },
          {
            key: "uniqueViewers",
            label: "Unique Viewers",
            value: formatNumber(analyticsData.uniqueViewers),
            tone: "text-tokyo-night-orange",
          },
        ]
      : [],
  );

  const streamSecondaryCards = $derived(
    analyticsData
      ? [
          {
            key: "viewTime",
            label: "Total View Time",
            value: `${Math.round((analyticsData.totalViewTime ?? 0) / 3600)} hours`,
          },
          {
            key: "timeRange",
            label: "Time Range",
            value: formatTimeRange(analyticsData.timeRange),
          },
        ]
      : [],
  );

  const healthSummaryCards = $derived(
    analyticsData
      ? [
          hasValue(analyticsData.currentHealthScore) && {
            key: "healthScore",
            label: "Health Score",
            value: `${Math.round((analyticsData.currentHealthScore ?? 0) * 100)}%`,
            tone: healthScoreClass(analyticsData.currentHealthScore ?? 0),
          },
          hasValue(analyticsData.rebufferCount) && {
            key: "rebuffer",
            label: "Rebuffers",
            value: `${analyticsData.rebufferCount ?? 0}`,
            tone: rebufferClass(analyticsData.rebufferCount ?? 0),
          },
          hasValue(analyticsData.alertCount) && {
            key: "alerts",
            label: "Health Alerts",
            value: `${analyticsData.alertCount ?? 0}`,
            tone: alertClass(analyticsData.alertCount ?? 0),
          },
          hasValue(analyticsData.packetLossPercentage) && {
            key: "packetLoss",
            label: "Packet Loss",
            value: `${(analyticsData.packetLossPercentage ?? 0).toFixed(1)}%`,
            tone: packetLossClass(analyticsData.packetLossPercentage ?? 0),
          },
        ].filter(Boolean)
      : [],
  );

  const hasVideoQuality = $derived(
    !!(
      analyticsData?.currentResolution ||
      analyticsData?.currentCodec ||
      analyticsData?.currentBitrate ||
      analyticsData?.currentFps
    ),
  );
  const hasPerformanceMetrics = $derived(
    hasValue(analyticsData?.frameJitterMs) ||
      hasValue(analyticsData?.keyframeStabilityMs) ||
      Boolean(analyticsData?.qualityTier),
  );
  const hasBufferInsights = $derived(
    Boolean(analyticsData?.bufferState) ||
      Boolean(analyticsData?.currentIssues),
  );
  const hasHealthDetails = $derived(
    hasVideoQuality || hasPerformanceMetrics || hasBufferInsights,
  );
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
          {#each Array(3) as _, index (index)}
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
        <Card class="mb-8">
          <CardHeader>
            <CardTitle class="text-tokyo-night-cyan"
              >Platform Overview</CardTitle
            >
            <CardDescription
              >Streaming activity across your organization</CardDescription
            >
          </CardHeader>
          <CardContent>
            <div class="grid grid-cols-2 md:grid-cols-4 gap-4">
              <Card class="text-center border border-tokyo-night-selection">
                <CardContent class="py-4">
                  <div class="text-2xl font-bold text-tokyo-night-blue">
                    {formatNumber(platformOverview.totalStreams)}
                  </div>
                  <CardDescription>Total Streams</CardDescription>
                </CardContent>
              </Card>
              <Card class="text-center border border-tokyo-night-selection">
                <CardContent class="py-4">
                  <div class="text-2xl font-bold text-tokyo-night-green">
                    {formatNumber(platformOverview.totalViewers)}
                  </div>
                  <CardDescription>Total Viewers</CardDescription>
                </CardContent>
              </Card>
              <Card class="text-center border border-tokyo-night-selection">
                <CardContent class="py-4">
                  <div class="text-2xl font-bold text-tokyo-night-purple">
                    {formatNumber(platformOverview.totalUsers)}
                  </div>
                  <CardDescription>Total Users</CardDescription>
                </CardContent>
              </Card>
              <Card class="text-center border border-tokyo-night-selection">
                <CardContent class="py-4">
                  <div class="text-2xl font-bold text-tokyo-night-orange">
                    {(platformOverview.totalBandwidth / 1000000).toFixed(1)}MB
                  </div>
                  <CardDescription>Total Bandwidth</CardDescription>
                </CardContent>
              </Card>
            </div>
          </CardContent>
        </Card>
      {/if}

      <!-- Stream Selector -->
      {#if streams.length > 1}
        <Card class="mb-8">
          <CardHeader>
            <CardTitle class="text-tokyo-night-cyan">Select Stream</CardTitle>
            <CardDescription
              >Choose which stream’s analytics to view</CardDescription
            >
          </CardHeader>
          <CardContent>
            <div class="grid grid-cols-1 md:grid-cols-3 gap-3">
              {#each streams as stream (stream.id ?? stream.name)}
                <Button
                  variant={selectedStream?.id === stream.id
                    ? "default"
                    : "outline"}
                  class="justify-start text-left h-auto py-3 px-4 border border-tokyo-night-selection"
                  onclick={() => selectStream(stream)}
                >
                  <div class="flex flex-col items-start">
                    <span class="font-medium">{stream.name}</span>
                    <span class="text-xs text-tokyo-night-comment"
                      >Status: {stream.status}</span
                    >
                  </div>
                </Button>
              {/each}
            </div>
          </CardContent>
        </Card>
      {/if}

      <!-- Stream Analytics -->
      {#if selectedStream}
        <Card class="mb-8">
          <CardHeader>
            <CardTitle class="text-tokyo-night-cyan">
              Stream Analytics: {selectedStream.name}
            </CardTitle>
            <CardDescription>
              Detailed engagement metrics for this stream.
            </CardDescription>
          </CardHeader>
          <CardContent class="space-y-6">
            {#if analyticsData}
              {#if streamSummaryCards.length > 0}
                <Panel spacing="default">
                  <div
                    class="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-4 gap-4"
                  >
                    {#each streamSummaryCards as stat (stat.key)}
                      <Card>
                        <CardContent class="py-4 text-center space-y-2">
                          <Badge
                            variant="outline"
                            class="mx-auto w-fit uppercase tracking-wide text-[0.65rem]"
                          >
                            {stat.label}
                          </Badge>
                          <span
                            class={`text-2xl font-semibold ${stat.tone ?? ""}`}
                          >
                            {stat.value}
                          </span>
                        </CardContent>
                      </Card>
                    {/each}
                  </div>
                </Panel>
              {/if}

              {#if streamSecondaryCards.length > 0}
                <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
                  {#each streamSecondaryCards as stat (stat.key)}
                    <Card>
                      <CardContent class="py-4 space-y-2">
                        <Badge
                          variant="outline"
                          class="w-fit uppercase tracking-wide text-[0.65rem]"
                        >
                          {stat.label}
                        </Badge>
                        <span class="text-lg font-semibold">{stat.value}</span>
                      </CardContent>
                    </Card>
                  {/each}
                </div>
              {/if}

              {#if healthSummaryCards.length > 0 || hasHealthDetails}
                <Panel spacing="default">
                  <div class="space-y-4">
                    <div class="flex items-center gap-2">
                      <Badge
                        variant="secondary"
                        class="uppercase tracking-wide text-[0.65rem]"
                      >
                        Stream Health
                      </Badge>
                      <span class="text-xs text-muted-foreground">
                        Quality indicators updated in real-time
                      </span>
                    </div>

                    {#if healthSummaryCards.length > 0}
                      <div
                        class="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-4 gap-4"
                      >
                        {#each healthSummaryCards as stat (stat.key)}
                          <Card>
                            <CardContent class="py-4 text-center space-y-2">
                              <Badge
                                variant="outline"
                                class="mx-auto w-fit uppercase tracking-wide text-[0.65rem]"
                              >
                                {stat.label}
                              </Badge>
                              <span
                                class={`text-2xl font-semibold ${stat.tone ?? ""}`}
                              >
                                {stat.value}
                              </span>
                            </CardContent>
                          </Card>
                        {/each}
                      </div>
                    {/if}

                  <div class="grid grid-cols-1 md:grid-cols-3 gap-4">
                    {#if hasVideoQuality}
                      <Card>
                        <CardContent class="space-y-2">
                          <Badge
                            variant="outline"
                            class="w-fit uppercase tracking-wide text-[0.65rem]"
                          >
                            Video Quality
                          </Badge>
                          <div class="space-y-1 text-sm">
                            {#if analyticsData.currentResolution}
                              <p class="font-mono text-primary">
                                {analyticsData.currentResolution}
                              </p>
                            {/if}
                            {#if analyticsData.currentCodec}
                              <p class="font-mono text-purple-300">
                                {analyticsData.currentCodec}
                              </p>
                            {/if}
                            {#if analyticsData.currentBitrate}
                              <p class="font-mono text-green-300">
                                {Math.round(
                                  (analyticsData.currentBitrate ?? 0) / 1000,
                                )}k
                              </p>
                            {/if}
                            {#if analyticsData.currentFps}
                              <p class="font-mono text-orange-300">
                                {analyticsData.currentFps.toFixed(1)} fps
                              </p>
                            {/if}
                          </div>
                        </CardContent>
                      </Card>
                    {/if}

                    {#if hasPerformanceMetrics}
                      <Card>
                        <CardContent class="space-y-2">
                          <Badge
                            variant="outline"
                            class="w-fit uppercase tracking-wide text-[0.65rem]"
                          >
                            Performance
                          </Badge>
                          <div class="space-y-1 text-sm">
                            {#if hasValue(analyticsData.frameJitterMs)}
                              {@const jitter = analyticsData.frameJitterMs ?? 0}
                              <p>
                                <span class="text-muted-foreground"
                                  >Jitter:</span
                                >
                                <span
                                  class={`ml-1 font-mono ${jitterClass(jitter)}`}
                                >
                                  {jitter.toFixed(1)}ms
                                </span>
                              </p>
                            {/if}
                            {#if hasValue(analyticsData.keyframeStabilityMs)}
                              {@const keyframe =
                                analyticsData.keyframeStabilityMs ?? 0}
                              <p>
                                <span class="text-muted-foreground"
                                  >Keyframe:</span
                                >
                                <span class="ml-1 font-mono text-primary">
                                  {keyframe.toFixed(1)}ms
                                </span>
                              </p>
                            {/if}
                            {#if analyticsData.qualityTier}
                              <p>
                                <span class="text-muted-foreground">Tier:</span>
                                <span class="ml-1 font-mono text-purple-300">
                                  {analyticsData.qualityTier}
                                </span>
                              </p>
                            {/if}
                          </div>
                        </CardContent>
                      </Card>
                    {/if}

                    {#if hasBufferInsights}
                      <Card>
                        <CardContent class="space-y-2">
                          <Badge
                            variant="outline"
                            class="w-fit uppercase tracking-wide text-[0.65rem]"
                          >
                            Buffer & Issues
                          </Badge>
                          <div class="space-y-1 text-sm">
                            {#if analyticsData.bufferState}
                              <p>
                                <span class="text-muted-foreground"
                                  >Buffer:</span
                                >
                                <span
                                  class={`ml-1 font-mono ${bufferStateClass(analyticsData.bufferState)}`}
                                >
                                  {analyticsData.bufferState}
                                </span>
                              </p>
                            {/if}
                            {#if analyticsData.currentIssues}
                              <p class="font-medium text-rose-400">
                                {analyticsData.currentIssues}
                              </p>
                            {/if}
                          </div>
                        </CardContent>
                      </Card>
                    {/if}
                  </div>
                  </div>
                </Panel>
              {/if}
            {:else}
              <p class="text-muted-foreground">
                No analytics data available for this stream
              </p>
            {/if}
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle class="text-tokyo-night-cyan">
              Real-time Viewer Metrics
            </CardTitle>
            <CardDescription>
              Recent viewer counts captured from live updates
            </CardDescription>
          </CardHeader>
          <CardContent>
            {#if viewerMetrics.length > 0}
              <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-5 gap-3">
                {#each viewerMetrics.slice(-10) as metric (metric.timestamp)}
                  <div
                    class="rounded-lg border border-border bg-card/40 p-3 text-center"
                  >
                    <p class="text-sm font-medium">
                      {metric.viewerCount} viewers
                    </p>
                    <Badge
                      variant="outline"
                      class="mt-2 w-fit mx-auto text-[0.65rem]"
                    >
                      {new Date(metric.timestamp).toLocaleTimeString()}
                    </Badge>
                  </div>
                {/each}
              </div>
            {:else}
              <p class="text-muted-foreground">
                No real-time metrics available
              </p>
            {/if}
          </CardContent>
        </Card>
      {:else if streams.length === 0}
        <EmptyState
          icon="BarChart"
          title="No streams found"
          description="Create a stream to start seeing analytics data"
          actionText="Go to Streams"
          onAction={() => goto(resolve("/streams"))}
        />
      {/if}
    {/if}
  </div>
</div>

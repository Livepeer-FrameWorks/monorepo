<script lang="ts">
  import { onMount } from "svelte";
  import {
    GetProcessingUsageStore,
  } from "$houdini";
  import { toast } from "$lib/stores/toast.js";
  import { GridSeam } from "$lib/components/layout";
  import DashboardMetricCard from "$lib/components/shared/DashboardMetricCard.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import { Button } from "$lib/components/ui/button";
  import { Badge } from "$lib/components/ui/badge";
  import { getIconComponent } from "$lib/iconUtils";
  import { formatDuration } from "$lib/utils/formatters.js";
  import {
    Select,
    SelectContent,
    SelectItem,
    SelectTrigger,
  } from "$lib/components/ui/select";
  import {
    Table,
    TableHeader,
    TableHead,
    TableRow,
    TableBody,
    TableCell,
  } from "$lib/components/ui/table";

  // Houdini stores
  const processingUsageStore = new GetProcessingUsageStore();

  // Type for processing usage edges
  type ProcessingUsageEdge = NonNullable<NonNullable<NonNullable<typeof $processingUsageStore.data>["processingUsageConnection"]>["edges"]>[0];
  type ProcessingUsageSummary = NonNullable<NonNullable<NonNullable<typeof $processingUsageStore.data>["processingUsageConnection"]>["summaries"]>[0];

  // Derived state
  let loading = $derived($processingUsageStore.fetching);
  let usageRecords = $derived($processingUsageStore.data?.processingUsageConnection?.edges?.map((e: ProcessingUsageEdge) => e.node) ?? []);
  let summaries = $derived($processingUsageStore.data?.processingUsageConnection?.summaries ?? []);
  let totalCount = $derived($processingUsageStore.data?.processingUsageConnection?.totalCount ?? 0);

  // Time range selection
  let timeRange = $state("7d"); // 24h, 7d, 30d
  let processTypeFilter = $state("all"); // all, Livepeer, AV

  // Type for aggregated stats
  type AggregateStats = {
    livepeerSeconds: number;
    livepeerSegments: number;
    nativeAvSeconds: number;
    nativeAvSegments: number;
    audioSeconds: number;
    videoSeconds: number;
    h264Seconds: number;
    vp9Seconds: number;
    av1Seconds: number;
    hevcSeconds: number;
    aacSeconds: number;
    opusSeconds: number;
  };

  // Aggregate stats from summaries
  let aggregateStats = $derived.by((): AggregateStats | null => {
    if (summaries.length === 0) return null;

    return summaries.reduce((acc: AggregateStats, s: ProcessingUsageSummary) => ({
      livepeerSeconds: acc.livepeerSeconds + (s.livepeerSeconds || 0),
      livepeerSegments: acc.livepeerSegments + (s.livepeerSegmentCount || 0),
      nativeAvSeconds: acc.nativeAvSeconds + (s.nativeAvSeconds || 0),
      nativeAvSegments: acc.nativeAvSegments + (s.nativeAvSegmentCount || 0),
      audioSeconds: acc.audioSeconds + (s.audioSeconds || 0),
      videoSeconds: acc.videoSeconds + (s.videoSeconds || 0),
      // Per-codec breakdown
      h264Seconds: acc.h264Seconds + (s.livepeerH264Seconds || 0) + (s.nativeAvH264Seconds || 0),
      vp9Seconds: acc.vp9Seconds + (s.livepeerVp9Seconds || 0) + (s.nativeAvVp9Seconds || 0),
      av1Seconds: acc.av1Seconds + (s.livepeerAv1Seconds || 0) + (s.nativeAvAv1Seconds || 0),
      hevcSeconds: acc.hevcSeconds + (s.livepeerHevcSeconds || 0) + (s.nativeAvHevcSeconds || 0),
      aacSeconds: acc.aacSeconds + (s.nativeAvAacSeconds || 0),
      opusSeconds: acc.opusSeconds + (s.nativeAvOpusSeconds || 0),
    }), {
      livepeerSeconds: 0,
      livepeerSegments: 0,
      nativeAvSeconds: 0,
      nativeAvSegments: 0,
      audioSeconds: 0,
      videoSeconds: 0,
      h264Seconds: 0,
      vp9Seconds: 0,
      av1Seconds: 0,
      hevcSeconds: 0,
      aacSeconds: 0,
      opusSeconds: 0,
    });
  });

  // Total transcoding time
  let totalTranscodeSeconds = $derived(
    (aggregateStats?.livepeerSeconds ?? 0) + (aggregateStats?.nativeAvSeconds ?? 0)
  );

  // Codec distribution for chart
  let codecDistribution = $derived.by(() => {
    if (!aggregateStats) return [];
    const codecs = [
      { name: "H.264", seconds: aggregateStats.h264Seconds, color: "bg-blue-500" },
      { name: "VP9", seconds: aggregateStats.vp9Seconds, color: "bg-purple-500" },
      { name: "AV1", seconds: aggregateStats.av1Seconds, color: "bg-green-500" },
      { name: "HEVC", seconds: aggregateStats.hevcSeconds, color: "bg-orange-500" },
      { name: "AAC", seconds: aggregateStats.aacSeconds, color: "bg-pink-500" },
      { name: "Opus", seconds: aggregateStats.opusSeconds, color: "bg-cyan-500" },
    ].filter(c => c.seconds > 0);

    const total = codecs.reduce((sum, c) => sum + c.seconds, 0);
    return codecs.map(c => ({
      ...c,
      percentage: total > 0 ? (c.seconds / total) * 100 : 0,
    }));
  });

  // Icons
  const CpuIcon = getIconComponent("Cpu");
  const ClockIcon = getIconComponent("Clock");
  const LayersIcon = getIconComponent("Layers");
  const VideoIcon = getIconComponent("Video");
  const MusicIcon = getIconComponent("Music");
  const CalendarIcon = getIconComponent("Calendar");
  const ZapIcon = getIconComponent("Zap");
  const ServerIcon = getIconComponent("Server");

  function formatSeconds(seconds: number): string {
    if (seconds < 60) return `${seconds.toFixed(1)}s`;
    if (seconds < 3600) return `${(seconds / 60).toFixed(1)}m`;
    return `${(seconds / 3600).toFixed(1)}h`;
  }

  function getProcessTypeBadge(type: string) {
    switch (type) {
      case "Livepeer":
        return "bg-purple-500/20 text-purple-400 border-purple-500/30";
      case "AV":
        return "bg-blue-500/20 text-blue-400 border-blue-500/30";
      default:
        return "bg-muted text-muted-foreground";
    }
  }

  function getTrackTypeBadge(type: string | null | undefined) {
    if (type === "video") return "bg-green-500/20 text-green-400 border-green-500/30";
    if (type === "audio") return "bg-pink-500/20 text-pink-400 border-pink-500/30";
    return "bg-muted text-muted-foreground";
  }

  async function loadData() {
    try {
      const now = new Date();
      let start = new Date();

      switch (timeRange) {
        case "24h":
          start.setDate(now.getDate() - 1);
          break;
        case "7d":
          start.setDate(now.getDate() - 7);
          break;
        case "30d":
          start.setDate(now.getDate() - 30);
          break;
      }

      const range = {
        start: start.toISOString(),
        end: now.toISOString(),
      };

      await processingUsageStore.fetch({
        variables: {
          timeRange: range,
          processType: processTypeFilter === "all" ? null : processTypeFilter,
        },
      });
    } catch (error) {
      console.error("Failed to load transcoding data:", error);
      toast.error("Failed to load transcoding analytics.");
    }
  }

  onMount(() => {
    loadData();
  });

  function handleTimeRangeChange(value: string) {
    timeRange = value;
    loadData();
  }

  function handleProcessTypeChange(value: string) {
    processTypeFilter = value;
    loadData();
  }
</script>

<svelte:head>
  <title>Transcoding Analytics - FrameWorks</title>
</svelte:head>

<div class="h-full flex flex-col">
  <!-- Fixed Page Header -->
  <div class="px-4 sm:px-6 lg:px-8 py-4 border-b border-[hsl(var(--tn-fg-gutter)/0.3)] shrink-0 bg-background">
    <div class="flex items-center justify-between">
      <div class="flex items-center gap-3">
        <CpuIcon class="w-5 h-5 text-primary" />
        <div>
          <h1 class="text-xl font-bold text-foreground">Transcoding Analytics</h1>
          <p class="text-sm text-muted-foreground">
            Monitor transcoding usage, codec distribution, and processing efficiency
          </p>
        </div>
      </div>

      <div class="flex items-center gap-2">
        <Select bind:value={processTypeFilter} onValueChange={handleProcessTypeChange} type="single">
          <SelectTrigger class="w-auto min-w-[140px]">
            <ServerIcon class="w-4 h-4 mr-2 text-muted-foreground" />
            {processTypeFilter === 'all' ? 'All Engines' : processTypeFilter === 'Livepeer' ? 'Livepeer Gateway' : 'Native AV'}
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">All Engines</SelectItem>
            <SelectItem value="Livepeer">Livepeer Gateway</SelectItem>
            <SelectItem value="AV">Native AV</SelectItem>
          </SelectContent>
        </Select>

        <Select bind:value={timeRange} onValueChange={handleTimeRangeChange} type="single">
          <SelectTrigger class="w-auto min-w-[140px]">
            <CalendarIcon class="w-4 h-4 mr-2 text-muted-foreground" />
            {timeRange === '24h' ? 'Last 24 Hours' : timeRange === '7d' ? 'Last 7 Days' : 'Last 30 Days'}
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="24h">Last 24 Hours</SelectItem>
            <SelectItem value="7d">Last 7 Days</SelectItem>
            <SelectItem value="30d">Last 30 Days</SelectItem>
          </SelectContent>
        </Select>
      </div>
    </div>
  </div>

  <!-- Scrollable Content -->
  <div class="flex-1 overflow-y-auto bg-background/50">
    {#if loading && !aggregateStats}
      <!-- Simple loading state -->
      <div class="p-8 flex justify-center">
        <div class="w-8 h-8 border-4 border-primary border-t-transparent rounded-full animate-spin"></div>
      </div>
    {:else}
      <div class="page-transition">
        <!-- Stats Bar -->
        <GridSeam cols={4} stack="2x2" surface="panel" flush={true} class="mb-0">
          <div>
            <DashboardMetricCard
              icon={ClockIcon}
              iconColor="text-primary"
              value={formatSeconds(totalTranscodeSeconds)}
              valueColor="text-primary"
              label="Total Transcode Time"
            />
          </div>
          <div>
            <DashboardMetricCard
              icon={ZapIcon}
              iconColor="text-purple-400"
              value={formatSeconds(aggregateStats?.livepeerSeconds ?? 0)}
              valueColor="text-purple-400"
              label="Livepeer Gateway"
            />
          </div>
          <div>
            <DashboardMetricCard
              icon={CpuIcon}
              iconColor="text-blue-400"
              value={formatSeconds(aggregateStats?.nativeAvSeconds ?? 0)}
              valueColor="text-blue-400"
              label="Native AV Processing"
            />
          </div>
          <div>
            <DashboardMetricCard
              icon={LayersIcon}
              iconColor="text-accent-purple"
              value={((aggregateStats?.livepeerSegments ?? 0) + (aggregateStats?.nativeAvSegments ?? 0)).toLocaleString()}
              valueColor="text-accent-purple"
              label="Total Segments"
            />
          </div>
        </GridSeam>

        <div class="dashboard-grid">
          <!-- Codec Distribution -->
          <div class="slab col-span-full lg:col-span-4">
            <div class="slab-header">
              <h3>Codec Distribution</h3>
            </div>
            <div class="slab-body--padded">
              {#if codecDistribution.length > 0}
                <div class="space-y-3">
                  {#each codecDistribution as codec}
                    <div class="space-y-1">
                      <div class="flex justify-between text-sm">
                        <span class="text-foreground">{codec.name}</span>
                        <span class="text-muted-foreground font-mono">{formatSeconds(codec.seconds)} ({codec.percentage.toFixed(1)}%)</span>
                      </div>
                      <div class="h-2 bg-muted rounded-full overflow-hidden">
                        <div
                          class={`h-full ${codec.color} transition-all duration-500`}
                          style="width: {codec.percentage}%"
                        ></div>
                      </div>
                    </div>
                  {/each}
                </div>

                <!-- Video vs Audio breakdown -->
                <div class="mt-6 pt-4 border-t border-border">
                  <div class="grid grid-cols-2 gap-4 text-sm">
                    <div class="flex items-center gap-2">
                      <VideoIcon class="w-4 h-4 text-green-400" />
                      <span class="text-muted-foreground">Video:</span>
                      <span class="font-mono ml-auto">{formatSeconds(aggregateStats?.videoSeconds ?? 0)}</span>
                    </div>
                    <div class="flex items-center gap-2">
                      <MusicIcon class="w-4 h-4 text-pink-400" />
                      <span class="text-muted-foreground">Audio:</span>
                      <span class="font-mono ml-auto">{formatSeconds(aggregateStats?.audioSeconds ?? 0)}</span>
                    </div>
                  </div>
                </div>
              {:else}
                <EmptyState
                  iconName="PieChart"
                  title="No codec data"
                  description="Codec distribution will appear when you have transcoding activity."
                />
              {/if}
            </div>
          </div>

          <!-- Daily Summaries -->
          <div class="slab col-span-full lg:col-span-8">
            <div class="slab-header">
              <h3>Daily Summary</h3>
            </div>
            <div class="slab-body--padded">
              {#if summaries.length > 0}
                <div class="overflow-x-auto">
                  <Table>
                    <TableHeader>
                      <TableRow>
                        <TableHead class="w-[120px]">Date</TableHead>
                        <TableHead class="text-right">Livepeer</TableHead>
                        <TableHead class="text-right">Native AV</TableHead>
                        <TableHead class="text-right">Video</TableHead>
                        <TableHead class="text-right">Audio</TableHead>
                        <TableHead class="text-right">Segments</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {#each summaries.slice().reverse() as summary}
                        <TableRow>
                          <TableCell class="font-mono text-xs">
                            {new Date(summary.date).toLocaleDateString()}
                          </TableCell>
                          <TableCell class="text-right font-mono text-purple-400">
                            {formatSeconds(summary.livepeerSeconds || 0)}
                          </TableCell>
                          <TableCell class="text-right font-mono text-blue-400">
                            {formatSeconds(summary.nativeAvSeconds || 0)}
                          </TableCell>
                          <TableCell class="text-right font-mono text-green-400">
                            {formatSeconds(summary.videoSeconds || 0)}
                          </TableCell>
                          <TableCell class="text-right font-mono text-pink-400">
                            {formatSeconds(summary.audioSeconds || 0)}
                          </TableCell>
                          <TableCell class="text-right font-mono text-muted-foreground">
                            {((summary.livepeerSegmentCount || 0) + (summary.nativeAvSegmentCount || 0)).toLocaleString()}
                          </TableCell>
                        </TableRow>
                      {/each}
                    </TableBody>
                  </Table>
                </div>
              {:else}
                <EmptyState
                  iconName="Calendar"
                  title="No daily data"
                  description="Daily summaries will appear when you have transcoding activity."
                />
              {/if}
            </div>
          </div>

          <!-- Recent Transcoding Events -->
          <div class="slab col-span-full">
            <div class="slab-header">
              <div class="flex items-center justify-between w-full">
                <h3>Recent Transcoding Events</h3>
                <span class="text-xs text-muted-foreground">{totalCount} total events</span>
              </div>
            </div>
            <div class="slab-body">
              {#if usageRecords.length > 0}
                <div class="overflow-x-auto">
                  <Table>
                    <TableHeader>
                      <TableRow>
                        <TableHead class="w-[160px]">Time</TableHead>
                        <TableHead>Stream</TableHead>
                        <TableHead class="w-[100px]">Engine</TableHead>
                        <TableHead class="w-[80px]">Track</TableHead>
                        <TableHead>Codec</TableHead>
                        <TableHead class="text-right">Duration</TableHead>
                        <TableHead class="text-right">Resolution</TableHead>
                        <TableHead class="text-right">RTF</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {#each usageRecords.slice(0, 50) as record}
                        <TableRow>
                          <TableCell class="font-mono text-xs text-muted-foreground">
                            {new Date(record.timestamp).toLocaleString()}
                          </TableCell>
                          <TableCell class="font-mono text-xs">
                            {record.streamName}
                          </TableCell>
                          <TableCell>
                            <Badge variant="outline" class={getProcessTypeBadge(record.processType)}>
                              {record.processType}
                            </Badge>
                          </TableCell>
                          <TableCell>
                            {#if record.trackType}
                              <Badge variant="outline" class={getTrackTypeBadge(record.trackType)}>
                                {record.trackType}
                              </Badge>
                            {:else}
                              <span class="text-muted-foreground">-</span>
                            {/if}
                          </TableCell>
                          <TableCell class="font-mono text-xs">
                            {#if record.inputCodec && record.outputCodec && record.inputCodec !== record.outputCodec}
                              {record.inputCodec} <span class="text-muted-foreground">→</span> {record.outputCodec}
                            {:else}
                              {record.outputCodec || record.inputCodec || '-'}
                            {/if}
                          </TableCell>
                          <TableCell class="text-right font-mono">
                            {formatDuration(record.durationMs)}
                          </TableCell>
                          <TableCell class="text-right font-mono text-xs">
                            {#if record.outputWidth && record.outputHeight}
                              {record.outputWidth}x{record.outputHeight}
                            {:else if record.width && record.height}
                              {record.width}x{record.height}
                            {:else}
                              <span class="text-muted-foreground">-</span>
                            {/if}
                          </TableCell>
                          <TableCell class="text-right font-mono text-xs">
                            {#if record.rtfOut}
                              <span class={record.rtfOut >= 1 ? 'text-success' : 'text-warning'}>
                                {record.rtfOut.toFixed(2)}x
                              </span>
                            {:else}
                              <span class="text-muted-foreground">-</span>
                            {/if}
                          </TableCell>
                        </TableRow>
                      {/each}
                    </TableBody>
                  </Table>
                </div>
              {:else}
                <div class="p-8">
                  <EmptyState
                    iconName="Cpu"
                    title="No transcoding events"
                    description="Transcoding events will appear here when streams are actively being processed."
                  />
                </div>
              {/if}
            </div>
          </div>
        </div>
      </div>
    {/if}
  </div>
</div>

<script lang="ts">
  import { formatDate, formatDuration } from "$lib/utils/stream-helpers";
  import { getIconComponent } from "$lib/iconUtils";
  import ViewerTrendChart from "$lib/components/charts/ViewerTrendChart.svelte";
  import QualityTierChart from "$lib/components/charts/QualityTierChart.svelte";
  import CodecDistributionChart from "$lib/components/charts/CodecDistributionChart.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";

  // Local interface matching Houdini TrackListUpdates subscription
  interface StreamTrack {
    trackName: string;
    trackType: string;
    codec?: string | null;
    resolution?: string | null;
    width?: number | null;
    height?: number | null;
    fps?: number | null;
    bitrateKbps?: number | null;
    channels?: number | null;
    sampleRate?: number | null;
  }

  interface TrackInfo {
    streamId: string;
    totalTracks: number | null;
    tracks?: StreamTrack[] | null;
  }

  interface ViewerMetric {
    timestamp: string;
    viewerCount: number;
    stream?: string | null;
  }

  interface DailyAnalytics {
    day: string;
    streamId: string;
    totalViews: number;
    uniqueViewers: number;
    uniqueCountries: number;
    uniqueCities: number;
    egressBytes: number;
    egressGb: number;
  }

  interface QualityTierSummary {
    tier2160pMinutes: number;
    tier1440pMinutes: number;
    tier1080pMinutes: number;
    tier720pMinutes: number;
    tier480pMinutes: number;
    tierSdMinutes: number;
    codecH264Minutes?: number;
    codecH265Minutes?: number;
    totalMinutes?: number;
    avgBitrate?: number | null;
    avgFps?: number | null;
  }

  interface CodecData {
    codec: string;
    minutes: number;
  }

  interface StreamData {
    name: string;
    description?: string | null;
    createdAt?: string | null;
    updatedAt?: string | null;
    metrics?: {
      isLive?: boolean;
    } | null;
  }

  interface StreamKeyData {
    id?: string;
    keyValue?: string;
  }

  interface RecordingData {
    sizeBytes?: number | null;
    isFrozen?: boolean;
  }

  interface ClipData {
    sizeBytes?: number | null;
    isFrozen?: boolean;
  }

  interface AnalyticsData {
    peakViewers?: number | null;
    totalSessionDuration?: number | null;
    packetsSent?: number | null;
    packetsLost?: number | null;
    packetsRetrans?: number | null;
    packetLossRate?: number | null;
  }

  let {
    stream,
    streamKeys,
    recordings,
    clips = [],
    analytics,
    tracks = null,
    viewerMetrics = [],
    dailyAnalytics = [],
    qualityTierSummary = null,
    codecDistribution = [],
  }: {
    stream: StreamData;
    streamKeys: StreamKeyData[];
    recordings: RecordingData[];
    clips?: ClipData[];
    analytics: AnalyticsData | null;
    tracks?: TrackInfo | null;
    viewerMetrics?: ViewerMetric[];
    dailyAnalytics?: DailyAnalytics[];
    qualityTierSummary?: QualityTierSummary | null;
    codecDistribution?: CodecData[];
  } = $props();

  // Separate video and audio tracks
  const videoTracks = $derived(
    tracks?.tracks?.filter((t: StreamTrack) => t.trackType === "video") || []
  );
  const audioTracks = $derived(
    tracks?.tracks?.filter((t: StreamTrack) => t.trackType === "audio") || []
  );

  // Storage calculations
  const storageStats = $derived.by(() => {
    const loadedRecordings = recordings || [];
    const loadedClips = clips || [];

    const recordingBytes = loadedRecordings.reduce((sum, r) => sum + (r.sizeBytes || 0), 0);
    const clipBytes = loadedClips.reduce((sum, c) => sum + (c.sizeBytes || 0), 0);
    const frozenRecordings = loadedRecordings.filter((r) => r.isFrozen).length;
    const frozenClips = loadedClips.filter((c) => c.isFrozen).length;

    return {
      totalBytes: recordingBytes + clipBytes,
      recordingBytes,
      clipBytes,
      frozenAssets: frozenRecordings + frozenClips,
      totalAssets: loadedRecordings.length + loadedClips.length,
    };
  });

  function formatBytes(bytes: number): string {
    if (bytes === 0) return "0 B";
    const k = 1024;
    const sizes = ["B", "KB", "MB", "GB", "TB"];
    const i = Math.floor(Math.log(bytes) / Math.log(k));
    return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + " " + sizes[i];
  }

  // Map viewer metrics for the chart
  const chartData = $derived(
    viewerMetrics.map((m) => ({ timestamp: m.timestamp, viewers: m.viewerCount }))
  );

  const VideoIcon = $derived(getIconComponent("Video"));
  const MicIcon = $derived(getIconComponent("Mic"));
  const ActivityIcon = $derived(getIconComponent("Activity"));
  const TrendingUpIcon = $derived(getIconComponent("TrendingUp"));
  const NetworkIcon = $derived(getIconComponent("Wifi"));
  const HardDriveIcon = $derived(getIconComponent("HardDrive"));
  const SnowflakeIcon = $derived(getIconComponent("Snowflake"));
  const ScissorsIcon = $derived(getIconComponent("Scissors"));
  const FilmIcon = $derived(getIconComponent("Film"));
  const InfoIcon = $derived(getIconComponent("Info"));
  const CalendarIcon = $derived(getIconComponent("Calendar"));
  const GlobeIcon = $derived(getIconComponent("Globe"));

  // Format large numbers with commas
  function formatNumber(n: number | null | undefined): string {
    if (n === null || n === undefined) return "0";
    return n.toLocaleString();
  }

  function formatMinutes(minutes: number | null | undefined): string {
    if (minutes === null || minutes === undefined) return "0m";
    if (minutes >= 60) return `${(minutes / 60).toFixed(1)}h`;
    return `${Math.round(minutes)}m`;
  }

  // Packet loss status color
  function getPacketLossColor(rate: number | null | undefined): string {
    if (rate === null || rate === undefined) return "text-muted-foreground";
    if (rate > 0.05) return "text-error";
    if (rate > 0.01) return "text-warning";
    return "text-success";
  }
</script>

<div class="dashboard-grid border-t border-[hsl(var(--tn-fg-gutter)/0.3)]">
  <!-- Live Track Info (when stream is active) -->
  {#if tracks && tracks.tracks && tracks.tracks.length > 0}
    <div class="slab col-span-full">
      <div class="slab-header flex items-center gap-2">
        <ActivityIcon class="w-5 h-5 text-success animate-pulse" />
        <h3 class="font-semibold text-xs uppercase tracking-wide text-muted-foreground">
          Live Encoding
        </h3>
        <span class="text-xs text-muted-foreground ml-auto">
          {tracks.totalTracks ?? 0} track{(tracks.totalTracks ?? 0) !== 1 ? "s" : ""} active
        </span>
      </div>

      <div class="slab-body--padded grid grid-cols-1 md:grid-cols-2 gap-4">
        <!-- Video Tracks -->
        {#each videoTracks as track (track.trackName)}
          <div class="p-4 bg-muted/20">
            <div class="flex items-center gap-2 mb-3">
              <VideoIcon class="w-4 h-4 text-accent-purple" />
              <span class="font-medium text-foreground">{track.trackName}</span>
              {#if track.codec}
                <span
                  class="px-2 py-0.5 text-xs font-mono bg-accent-purple/10 text-accent-purple rounded"
                >
                  {track.codec}
                </span>
              {/if}
            </div>
            <div class="grid grid-cols-2 gap-2 text-sm">
              {#if track.resolution || (track.width && track.height)}
                <div>
                  <span class="text-muted-foreground">Resolution</span>
                  <p class="font-mono text-success">
                    {track.resolution || `${track.width}x${track.height}`}
                  </p>
                </div>
              {/if}
              {#if track.fps}
                <div>
                  <span class="text-muted-foreground">Frame Rate</span>
                  <p class="font-mono text-warning-alt">{track.fps.toFixed(1)} fps</p>
                </div>
              {/if}
              {#if track.bitrateKbps}
                <div>
                  <span class="text-muted-foreground">Bitrate</span>
                  <p class="font-mono text-primary">
                    {track.bitrateKbps >= 1000
                      ? `${(track.bitrateKbps / 1000).toFixed(1)} Mbps`
                      : `${track.bitrateKbps} kbps`}
                  </p>
                </div>
              {/if}
            </div>
          </div>
        {/each}

        <!-- Audio Tracks -->
        {#each audioTracks as track (track.trackName)}
          <div class="p-4 bg-muted/20">
            <div class="flex items-center gap-2 mb-3">
              <MicIcon class="w-4 h-4 text-info" />
              <span class="font-medium text-foreground">{track.trackName}</span>
              {#if track.codec}
                <span class="px-2 py-0.5 text-xs font-mono bg-info/10 text-info rounded">
                  {track.codec}
                </span>
              {/if}
            </div>
            <div class="grid grid-cols-2 gap-2 text-sm">
              {#if track.channels}
                <div>
                  <span class="text-muted-foreground">Channels</span>
                  <p class="font-mono text-foreground">
                    {track.channels === 1
                      ? "Mono"
                      : track.channels === 2
                        ? "Stereo"
                        : `${track.channels}ch`}
                  </p>
                </div>
              {/if}
              {#if track.sampleRate}
                <div>
                  <span class="text-muted-foreground">Sample Rate</span>
                  <p class="font-mono text-foreground">
                    {(track.sampleRate / 1000).toFixed(1)} kHz
                  </p>
                </div>
              {/if}
              {#if track.bitrateKbps}
                <div>
                  <span class="text-muted-foreground">Bitrate</span>
                  <p class="font-mono text-primary">{track.bitrateKbps} kbps</p>
                </div>
              {/if}
            </div>
          </div>
        {/each}
      </div>
    </div>
  {:else if stream.metrics?.isLive}
    <!-- Stream is live but no track info yet -->
    <div class="slab col-span-full">
      <div class="slab-body--padded flex flex-col items-center justify-center py-8 text-center">
        <ActivityIcon class="w-8 h-8 text-warning mb-2" />
        <h4 class="text-warning font-medium">Waiting for track information...</h4>
        <p class="text-sm text-muted-foreground mt-1">
          Track details will appear once the encoder starts sending data.
        </p>
      </div>
    </div>
  {/if}

  <!-- Stream Information -->
  <div class="slab">
    <div class="slab-header">
      <h3 class="font-semibold text-xs uppercase tracking-wide text-muted-foreground">
        Stream Information
      </h3>
    </div>
    <div class="slab-body--padded space-y-3">
      <div>
        <span class="text-sm text-muted-foreground">Name</span>
        <p class="text-foreground font-medium">
          {stream.name}
        </p>
      </div>
      {#if stream.description}
        <div>
          <span class="text-sm text-muted-foreground">Description</span>
          <p class="text-foreground">{stream.description}</p>
        </div>
      {/if}
      <div>
        <span class="text-sm text-muted-foreground">Created</span>
        <p class="text-foreground">
          {formatDate(stream.createdAt)}
        </p>
      </div>
      <div>
        <span class="text-sm text-muted-foreground">Last Updated</span>
        <p class="text-foreground">
          {formatDate(stream.updatedAt)}
        </p>
      </div>
    </div>
  </div>

  <!-- Quick Stats -->
  <div class="slab">
    <div class="slab-header">
      <h3 class="font-semibold text-xs uppercase tracking-wide text-muted-foreground">
        Quick Stats
      </h3>
    </div>
    <div class="slab-body--padded space-y-3">
      <div class="flex justify-between items-center">
        <span class="text-muted-foreground">Total Stream Keys:</span>
        <span class="font-mono text-info font-medium">{streamKeys.length}</span>
      </div>
      <div class="flex justify-between items-center">
        <span class="text-muted-foreground">Total Recordings:</span>
        <span class="font-mono text-info font-medium">{recordings.length}</span>
      </div>
      {#if analytics}
        <div class="flex justify-between items-center">
          <span class="text-muted-foreground">24h Peak Viewers:</span>
          <span class="font-mono text-info font-medium">{analytics.peakViewers || 0}</span>
        </div>
        <div class="flex justify-between items-center">
          <span class="text-muted-foreground">Total Watch Time:</span>
          <span class="font-mono text-info font-medium"
            >{formatDuration(analytics.totalSessionDuration || 0)}</span
          >
        </div>
      {/if}
    </div>
  </div>

  <!-- Storage Summary -->
  <div class="slab">
    <div class="slab-header flex items-center gap-2">
      <HardDriveIcon class="w-5 h-5 text-accent-purple" />
      <h3 class="font-semibold text-xs uppercase tracking-wide text-muted-foreground">
        Storage Summary
      </h3>
    </div>
    <div class="slab-body--padded space-y-4">
      <div class="flex justify-between items-center">
        <span class="text-muted-foreground">Total Usage (Visible):</span>
        <span class="font-mono text-accent-purple font-medium text-lg">
          {formatBytes(storageStats.totalBytes)}
        </span>
      </div>

      <div class="grid grid-cols-2 gap-4 pt-2 border-t border-border/30">
        <div>
          <div class="flex items-center gap-1.5 mb-1">
            <FilmIcon class="w-3.5 h-3.5 text-blue-500" />
            <span class="text-xs text-muted-foreground">DVR Usage</span>
          </div>
          <p class="font-mono text-sm">{formatBytes(storageStats.recordingBytes)}</p>
        </div>
        <div>
          <div class="flex items-center gap-1.5 mb-1">
            <ScissorsIcon class="w-3.5 h-3.5 text-purple-500" />
            <span class="text-xs text-muted-foreground">Clips Usage</span>
          </div>
          <p class="font-mono text-sm">{formatBytes(storageStats.clipBytes)}</p>
        </div>
      </div>

      {#if storageStats.frozenAssets > 0}
        <div
          class="flex items-center justify-between pt-2 border-t border-border/30 bg-blue-500/5 -mx-4 px-4 py-2 mt-2"
        >
          <div class="flex items-center gap-2 text-blue-400">
            <SnowflakeIcon class="w-4 h-4" />
            <span class="text-sm font-medium">Archived to Cold Storage</span>
          </div>
          <span class="font-mono text-blue-400 font-medium">{storageStats.frozenAssets} items</span>
        </div>
      {/if}
    </div>
  </div>

  <!-- Network Stats (from stream analytics) -->
  {#if analytics && (analytics.packetsSent || analytics.packetsLost || analytics.packetLossRate !== undefined)}
    <div class="slab col-span-full">
      <div class="slab-header flex items-center gap-2">
        <NetworkIcon class="w-5 h-5 text-info" />
        <h3 class="font-semibold text-xs uppercase tracking-wide text-muted-foreground">
          Network Stats
        </h3>
      </div>
      <div class="slab-body--padded">
        <div class="grid grid-cols-2 md:grid-cols-4 gap-4">
          <div>
            <span class="text-sm text-muted-foreground">Packets Sent</span>
            <p class="font-mono text-lg text-foreground">{formatNumber(analytics.packetsSent)}</p>
          </div>
          <div>
            <span class="text-sm text-muted-foreground">Packets Lost</span>
            <p
              class="font-mono text-lg {analytics.packetsLost > 0
                ? 'text-warning'
                : 'text-foreground'}"
            >
              {formatNumber(analytics.packetsLost)}
            </p>
          </div>
          <div>
            <span class="text-sm text-muted-foreground">Retransmitted</span>
            <p class="font-mono text-lg text-foreground">
              {formatNumber(analytics.packetsRetrans)}
            </p>
          </div>
          <div>
            <span class="text-sm text-muted-foreground">Packet Loss Rate</span>
            <p
              class="font-mono text-lg {getPacketLossColor(
                analytics.packetLossRate
              )} flex items-center gap-1"
            >
              {analytics.packetLossRate !== null && analytics.packetLossRate !== undefined
                ? `${(analytics.packetLossRate * 100).toFixed(3)}%`
                : "N/A"}
              {#if analytics.packetLossRate === null || analytics.packetLossRate === undefined}
                <span
                  title="Packet statistics are available for UDP-based protocols (SRT, WebRTC) which prioritize low latency. HTTP-based protocols (HLS, DASH) use TCP which guarantees delivery but adds latency through retransmission."
                >
                  <InfoIcon class="w-3.5 h-3.5 text-muted-foreground cursor-help" />
                </span>
              {/if}
            </p>
          </div>
        </div>
      </div>
    </div>
  {/if}

  <!-- Quality + Codec Distribution -->
  {#if qualityTierSummary}
    <div class="slab col-span-full">
      <div class="slab-header flex items-center gap-2">
        <VideoIcon class="w-5 h-5 text-success" />
        <h3 class="font-semibold text-xs uppercase tracking-wide text-muted-foreground">
          Quality Mix
        </h3>
        {#if qualityTierSummary.totalMinutes}
          <span class="text-xs text-muted-foreground ml-auto">
            {formatMinutes(qualityTierSummary.totalMinutes)} analyzed
          </span>
        {/if}
      </div>
      <div class="slab-body--padded">
        <div class="grid grid-cols-1 md:grid-cols-2 border border-border/30">
          <div class="p-4 border-b border-border/30 md:border-b-0 md:border-r border-border/30">
            <QualityTierChart data={qualityTierSummary} height={200} />
          </div>
          <div class="p-4">
            <CodecDistributionChart data={codecDistribution} height={200} title="" />
          </div>
        </div>

        <div class="grid grid-cols-2 md:grid-cols-4 gap-4 mt-4">
          <div>
            <span class="text-sm text-muted-foreground">Avg Bitrate</span>
            <p class="font-mono text-lg text-foreground">
              {qualityTierSummary.avgBitrate
                ? `${Math.round(qualityTierSummary.avgBitrate / 1000)} kbps`
                : "N/A"}
            </p>
          </div>
          <div>
            <span class="text-sm text-muted-foreground">Avg FPS</span>
            <p class="font-mono text-lg text-foreground">
              {qualityTierSummary.avgFps ? qualityTierSummary.avgFps.toFixed(1) : "N/A"}
            </p>
          </div>
          <div>
            <span class="text-sm text-muted-foreground">H.264 Minutes</span>
            <p class="font-mono text-lg text-foreground">
              {formatMinutes(qualityTierSummary.codecH264Minutes ?? 0)}
            </p>
          </div>
          <div>
            <span class="text-sm text-muted-foreground">H.265 Minutes</span>
            <p class="font-mono text-lg text-foreground">
              {formatMinutes(qualityTierSummary.codecH265Minutes ?? 0)}
            </p>
          </div>
        </div>
      </div>
    </div>
  {/if}

  <!-- Viewer Trend Chart -->
  <div class="slab col-span-full">
    <div class="slab-header flex items-center gap-2">
      <TrendingUpIcon class="w-5 h-5 text-primary" />
      <h3 class="font-semibold text-xs uppercase tracking-wide text-muted-foreground">
        Viewer Trend (24h)
      </h3>
    </div>
    <div class="slab-body--padded">
      {#if chartData.length > 0}
        <ViewerTrendChart data={chartData} height={200} title="" />
      {:else}
        <div class="h-[200px] flex items-center justify-center">
          <EmptyState
            iconName="Users"
            title="No viewer data"
            description="Viewer activity for the last 24 hours will appear here."
          />
        </div>
      {/if}
    </div>
  </div>

  <!-- Daily Analytics History -->
  {#if dailyAnalytics.length > 0}
    <div class="slab col-span-full">
      <div class="slab-header flex items-center gap-2">
        <CalendarIcon class="w-5 h-5 text-info" />
        <h3 class="font-semibold text-xs uppercase tracking-wide text-muted-foreground">
          Daily Analytics (Last 30 Days)
        </h3>
        <span class="text-xs text-muted-foreground ml-auto">{dailyAnalytics.length} days</span>
      </div>

      <!-- Summary Cards -->
      <div
        class="slab-body--padded grid grid-cols-2 md:grid-cols-4 gap-4 pb-4 border-b border-border/30"
      >
        <div>
          <span class="text-sm text-muted-foreground">Total Views</span>
          <p class="font-mono text-lg text-info font-medium">
            {formatNumber(dailyAnalytics.reduce((sum, d) => sum + d.totalViews, 0))}
          </p>
        </div>
        <div>
          <span class="text-sm text-muted-foreground">Unique Viewers</span>
          <p class="font-mono text-lg text-success font-medium">
            {formatNumber(Math.max(...dailyAnalytics.map((d) => d.uniqueViewers)))}
            <span class="text-xs text-muted-foreground">peak</span>
          </p>
        </div>
        <div>
          <span class="text-sm text-muted-foreground">Countries Reached</span>
          <p class="font-mono text-lg text-accent-purple font-medium flex items-center gap-1">
            <GlobeIcon class="w-4 h-4" />
            {Math.max(...dailyAnalytics.map((d) => d.uniqueCountries))}
          </p>
        </div>
        <div>
          <span class="text-sm text-muted-foreground">Total Bandwidth</span>
          <p class="font-mono text-lg text-warning font-medium">
            {dailyAnalytics.reduce((sum, d) => sum + d.egressGb, 0).toFixed(2)} GB
          </p>
        </div>
      </div>

      <!-- Daily Breakdown Table -->
      <div class="slab-body--padded overflow-x-auto">
        <table class="w-full text-sm">
          <thead>
            <tr
              class="text-muted-foreground text-xs uppercase tracking-wide border-b border-border/30"
            >
              <th class="text-left py-2 px-2">Date</th>
              <th class="text-right py-2 px-2">Views</th>
              <th class="text-right py-2 px-2">Unique Viewers</th>
              <th class="text-right py-2 px-2">Countries</th>
              <th class="text-right py-2 px-2">Cities</th>
              <th class="text-right py-2 px-2">Bandwidth</th>
            </tr>
          </thead>
          <tbody>
            {#each dailyAnalytics.slice().reverse() as day, i (`${day.day}-${i}`)}
              <tr class="border-b border-border/20 hover:bg-muted/20">
                <td class="py-2 px-2 font-mono text-foreground">
                  {new Date(day.day).toLocaleDateString(undefined, {
                    month: "short",
                    day: "numeric",
                    year: "numeric",
                  })}
                </td>
                <td class="py-2 px-2 text-right font-mono text-info">
                  {formatNumber(day.totalViews)}
                </td>
                <td class="py-2 px-2 text-right font-mono text-success">
                  {formatNumber(day.uniqueViewers)}
                </td>
                <td class="py-2 px-2 text-right font-mono text-accent-purple">
                  {day.uniqueCountries}
                </td>
                <td class="py-2 px-2 text-right font-mono text-muted-foreground">
                  {day.uniqueCities}
                </td>
                <td class="py-2 px-2 text-right font-mono text-warning">
                  {day.egressGb.toFixed(2)} GB
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    </div>
  {/if}
</div>

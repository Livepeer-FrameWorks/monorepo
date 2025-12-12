<script lang="ts">
  import { formatDate, formatDuration } from "$lib/utils/stream-helpers";
  import { getIconComponent } from "$lib/iconUtils";
  import ViewerTrendChart from "$lib/components/charts/ViewerTrendChart.svelte";

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
    streamName: string;
    totalTracks: number | null;
    tracks?: StreamTrack[] | null;
  }

  interface ViewerMetric {
    timestamp: string;
    viewerCount: number;
    stream?: string | null;
  }

  let { stream, streamKeys, recordings, analytics, tracks = null, viewerMetrics = [] }: {
    stream: any;
    streamKeys: any[];
    recordings: any[];
    analytics: any;
    tracks?: TrackInfo | null;
    viewerMetrics?: ViewerMetric[];
  } = $props();

  // Separate video and audio tracks
  const videoTracks = $derived(
    tracks?.tracks?.filter((t: StreamTrack) => t.trackType === "video") || []
  );
  const audioTracks = $derived(
    tracks?.tracks?.filter((t: StreamTrack) => t.trackType === "audio") || []
  );

  // Map viewer metrics for the chart
  const chartData = $derived(
    viewerMetrics.map(m => ({ timestamp: m.timestamp, viewers: m.viewerCount }))
  );

  const VideoIcon = $derived(getIconComponent("Video"));
  const MicIcon = $derived(getIconComponent("Mic"));
  const ActivityIcon = $derived(getIconComponent("Activity"));
  const TrendingUpIcon = $derived(getIconComponent("TrendingUp"));
  const NetworkIcon = $derived(getIconComponent("Wifi"));

  // Format large numbers with commas
  function formatNumber(n: number | null | undefined): string {
    if (n === null || n === undefined) return "0";
    return n.toLocaleString();
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
        <h3 class="font-semibold text-xs uppercase tracking-wide text-muted-foreground">Live Encoding</h3>
        <span class="text-xs text-muted-foreground ml-auto">
          {tracks.totalTracks ?? 0} track{(tracks.totalTracks ?? 0) !== 1 ? 's' : ''} active
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
                <span class="px-2 py-0.5 text-xs font-mono bg-accent-purple/10 text-accent-purple rounded">
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
                    {track.channels === 1 ? 'Mono' : track.channels === 2 ? 'Stereo' : `${track.channels}ch`}
                  </p>
                </div>
              {/if}
              {#if track.sampleRate}
                <div>
                  <span class="text-muted-foreground">Sample Rate</span>
                  <p class="font-mono text-foreground">{(track.sampleRate / 1000).toFixed(1)} kHz</p>
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
      <h3 class="font-semibold text-xs uppercase tracking-wide text-muted-foreground">Stream Information</h3>
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
      <h3 class="font-semibold text-xs uppercase tracking-wide text-muted-foreground">Quick Stats</h3>
    </div>
    <div class="slab-body--padded space-y-3">
      <div class="flex justify-between items-center">
        <span class="text-muted-foreground">Total Stream Keys:</span>
        <span class="font-mono text-info font-medium"
          >{streamKeys.length}</span
        >
      </div>
      <div class="flex justify-between items-center">
        <span class="text-muted-foreground">Total Recordings:</span>
        <span class="font-mono text-info font-medium"
          >{recordings.length}</span
        >
      </div>
      {#if analytics}
        <div class="flex justify-between items-center">
          <span class="text-muted-foreground">24h Peak Viewers:</span>
          <span class="font-mono text-info font-medium"
            >{analytics.peakViewers || 0}</span
          >
        </div>
        <div class="flex justify-between items-center">
          <span class="text-muted-foreground">Total Watch Time:</span>
          <span class="font-mono text-info font-medium"
            >{formatDuration(analytics.totalSessionDuration || 0)}</span
          >
        </div>
        {#if analytics.avgConnectionQuality}
          <div class="flex justify-between items-center">
            <span class="text-muted-foreground">Connection Quality:</span>
            <span
              class="font-mono font-medium {analytics.avgConnectionQuality >
              0.9
                ? 'text-success'
                : analytics.avgConnectionQuality > 0.7
                  ? 'text-warning'
                  : 'text-error'}"
            >
              {(analytics.avgConnectionQuality * 100).toFixed(1)}%
            </span>
          </div>
        {/if}
      {/if}
    </div>
  </div>

  <!-- Network Stats (from stream analytics) -->
  {#if analytics && (analytics.packetsSent || analytics.packetsLost || analytics.packetLossRate !== undefined)}
    <div class="slab col-span-full">
      <div class="slab-header flex items-center gap-2">
        <NetworkIcon class="w-5 h-5 text-info" />
        <h3 class="font-semibold text-xs uppercase tracking-wide text-muted-foreground">Network Stats</h3>
      </div>
      <div class="slab-body--padded">
        <div class="grid grid-cols-2 md:grid-cols-4 gap-4">
          <div>
            <span class="text-sm text-muted-foreground">Packets Sent</span>
            <p class="font-mono text-lg text-foreground">{formatNumber(analytics.packetsSent)}</p>
          </div>
          <div>
            <span class="text-sm text-muted-foreground">Packets Lost</span>
            <p class="font-mono text-lg {analytics.packetsLost > 0 ? 'text-warning' : 'text-foreground'}">
              {formatNumber(analytics.packetsLost)}
            </p>
          </div>
          <div>
            <span class="text-sm text-muted-foreground">Retransmitted</span>
            <p class="font-mono text-lg text-foreground">{formatNumber(analytics.packetsRetrans)}</p>
          </div>
          <div>
            <span class="text-sm text-muted-foreground">Packet Loss Rate</span>
            <p class="font-mono text-lg {getPacketLossColor(analytics.packetLossRate)}">
              {analytics.packetLossRate !== null && analytics.packetLossRate !== undefined
                ? `${(analytics.packetLossRate * 100).toFixed(3)}%`
                : "N/A"}
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
      <h3 class="font-semibold text-xs uppercase tracking-wide text-muted-foreground">Viewer Trend (24h)</h3>
    </div>
    <div class="slab-body--padded">
      {#if chartData.length > 0}
        <ViewerTrendChart
          data={chartData}
          height={200}
          title=""
        />
      {:else}
        <div class="flex items-center justify-center h-[200px] border border-border/30 bg-muted/20">
          <p class="text-muted-foreground text-sm">No viewer data for this time range</p>
        </div>
      {/if}
    </div>
  </div>
</div>

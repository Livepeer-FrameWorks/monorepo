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
</script>

<div class="space-y-4">
  <!-- Live Track Info (when stream is active) -->
  {#if tracks && tracks.tracks && tracks.tracks.length > 0}
    <div class="p-6 border border-success/30">
      <div class="flex items-center gap-2 mb-4">
        <ActivityIcon class="w-5 h-5 text-success animate-pulse" />
        <h4 class="text-lg font-semibold text-success">Live Encoding</h4>
        <span class="text-xs text-muted-foreground ml-auto">
          {tracks.totalTracks ?? 0} track{(tracks.totalTracks ?? 0) !== 1 ? 's' : ''} active
        </span>
      </div>

      <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
        <!-- Video Tracks -->
        {#each videoTracks as track (track.trackName)}
          <div class="p-4 border border-border/50">
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
          <div class="p-4 border border-border/50">
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
    <div class="p-6 border border-warning/30">
      <div class="flex items-center gap-2">
        <ActivityIcon class="w-5 h-5 text-warning" />
        <span class="text-warning font-medium">Waiting for track information...</span>
      </div>
      <p class="text-sm text-muted-foreground mt-2">
        Track details will appear once the encoder starts sending data.
      </p>
    </div>
  {/if}

  <div class="grid grid-cols-1 lg:grid-cols-2 gap-4">
    <!-- Stream Information -->
    <div class="p-6 border border-border/50">
      <h4 class="text-lg font-semibold gradient-text mb-4">
        Stream Information
      </h4>
      <div class="space-y-3">
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
    <div class="p-6 border border-border/50">
      <h4 class="text-lg font-semibold gradient-text mb-4">Quick Stats</h4>
      <div class="space-y-3">
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
  </div>

  <!-- Viewer Trend Chart -->
  <div class="p-6 border border-border/50">
    <div class="flex items-center gap-2 mb-4">
      <TrendingUpIcon class="w-5 h-5 text-primary" />
      <h4 class="text-lg font-semibold gradient-text">Viewer Trend (24h)</h4>
    </div>
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

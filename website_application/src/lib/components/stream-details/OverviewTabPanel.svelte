<script lang="ts">
  import type { TrackListUpdates$result } from "$houdini";
  import { formatDate } from "$lib/utils/stream-helpers";
  import { getIconComponent } from "$lib/iconUtils";
  import {
    classifyTrack,
    formatTrackBitrate,
    trackCodec,
    trackDisplayName,
    trackFps,
    trackResolution,
    type TrackLike,
  } from "$lib/utils/track-display";

  type TrackInfo = NonNullable<TrackListUpdates$result["liveTrackListUpdates"]>;
  type StreamTrack = NonNullable<TrackInfo["tracks"]>[number];

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

  // Overview is a config/status panel: live tracks, stream metadata, config counts.
  // Historical analytics (viewer trend, quality/codec mix, daily breakdown) live on
  // the dedicated /streams/[id]/analytics route, not here.
  let {
    stream,
    streamKeys,
    recordings,
    tracks = null,
  }: {
    stream: StreamData;
    streamKeys: StreamKeyData[];
    recordings: unknown[];
    tracks?: TrackInfo | null;
  } = $props();

  // Separate source media tracks from generated thumbnail/sprite outputs.
  const videoTracks = $derived(
    tracks?.tracks?.filter((t: StreamTrack) => classifyTrack(t) === "video") || []
  );
  const audioTracks = $derived(
    tracks?.tracks?.filter((t: StreamTrack) => classifyTrack(t) === "audio") || []
  );
  const generatedTracks = $derived(
    tracks?.tracks?.filter((t: StreamTrack) => classifyTrack(t) === "generated") || []
  );
  const displayedTrackCount = $derived(
    videoTracks.length + audioTracks.length + generatedTracks.length
  );

  const VideoIcon = $derived(getIconComponent("Video"));
  const MicIcon = $derived(getIconComponent("Mic"));
  const ActivityIcon = $derived(getIconComponent("Activity"));

  function trackKey(track: TrackLike, index: number): string {
    return `${trackDisplayName(track, index)}:${trackCodec(track)}:${index}`;
  }
</script>

<div class="dashboard-grid border-t border-[hsl(var(--tn-fg-gutter)/0.3)]">
  <!-- Live Track Info (when stream is active) -->
  {#if tracks && tracks.tracks && tracks.tracks.length > 0}
    <div class="slab col-span-full">
      <div class="slab-header flex items-center gap-2">
        <ActivityIcon class="w-5 h-5 text-success animate-pulse" />
        <h3 class="font-semibold text-xs uppercase tracking-wide text-muted-foreground">
          Live Tracks
        </h3>
        <span class="text-xs text-muted-foreground ml-auto">
          {displayedTrackCount} track{displayedTrackCount !== 1 ? "s" : ""} active
        </span>
      </div>

      <div class="slab-body--padded grid grid-cols-1 md:grid-cols-2 gap-4">
        <!-- Video Tracks -->
        {#each videoTracks as track, i (trackKey(track, i))}
          <div class="p-4 bg-muted/20">
            <div class="flex items-center gap-2 mb-3">
              <VideoIcon class="w-4 h-4 text-accent-purple" />
              <span class="font-medium text-foreground">{trackDisplayName(track, i)}</span>
              {#if trackCodec(track)}
                <span
                  class="px-2 py-0.5 text-xs font-mono bg-accent-purple/10 text-accent-purple rounded"
                >
                  {trackCodec(track)}
                </span>
              {/if}
            </div>
            <div class="grid grid-cols-2 gap-2 text-sm">
              {#if trackResolution(track)}
                <div>
                  <span class="text-muted-foreground">Resolution</span>
                  <p class="font-mono text-success">
                    {trackResolution(track)}
                  </p>
                </div>
              {/if}
              {#if trackFps(track)}
                <div>
                  <span class="text-muted-foreground">Frame Rate</span>
                  <p class="font-mono text-warning-alt">{trackFps(track)?.toFixed(1)} fps</p>
                </div>
              {/if}
              {#if formatTrackBitrate(track)}
                <div>
                  <span class="text-muted-foreground">Bitrate</span>
                  <p class="font-mono text-primary">
                    {formatTrackBitrate(track)}
                  </p>
                </div>
              {/if}
            </div>
          </div>
        {/each}

        <!-- Audio Tracks -->
        {#each audioTracks as track, i (trackKey(track, i))}
          <div class="p-4 bg-muted/20">
            <div class="flex items-center gap-2 mb-3">
              <MicIcon class="w-4 h-4 text-info" />
              <span class="font-medium text-foreground">{trackDisplayName(track, i)}</span>
              {#if trackCodec(track)}
                <span class="px-2 py-0.5 text-xs font-mono bg-info/10 text-info rounded">
                  {trackCodec(track)}
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
              {#if formatTrackBitrate(track)}
                <div>
                  <span class="text-muted-foreground">Bitrate</span>
                  <p class="font-mono text-primary">{formatTrackBitrate(track)}</p>
                </div>
              {/if}
            </div>
          </div>
        {/each}

        {#if generatedTracks.length > 0}
          <div class="p-4 bg-muted/20 md:col-span-2">
            <div class="flex items-center gap-2 mb-3">
              <VideoIcon class="w-4 h-4 text-muted-foreground" />
              <span class="font-medium text-foreground">Generated Outputs</span>
              <span class="text-xs text-muted-foreground">
                {generatedTracks.length} track{generatedTracks.length !== 1 ? "s" : ""}
              </span>
            </div>
            <div class="flex flex-wrap gap-2">
              {#each generatedTracks as track, i (trackKey(track, i))}
                <span
                  class="px-2 py-1 text-xs bg-muted/40 text-muted-foreground border border-border/30"
                >
                  {trackDisplayName(track, i)}
                  {#if trackCodec(track)}
                    <span class="font-mono text-foreground ml-1">{trackCodec(track)}</span>
                  {/if}
                  {#if trackResolution(track)}
                    <span class="font-mono ml-1">{trackResolution(track)}</span>
                  {/if}
                </span>
              {/each}
            </div>
          </div>
        {/if}
      </div>
    </div>
  {:else if stream.metrics?.isLive}
    <!-- Stream is live but no track info yet -->
    <div class="slab col-span-full">
      <div class="slab-body--padded flex flex-col items-center justify-center py-8 text-center">
        <ActivityIcon class="w-8 h-8 text-warning mb-2" />
        <h4 class="text-warning font-medium">Waiting for track information...</h4>
        <p class="text-sm text-muted-foreground mt-1">
          Track details will appear once live inventory arrives.
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

  <!-- Quick Stats (config counts; historical metrics live on the analytics route) -->
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
    </div>
  </div>
</div>

<script lang="ts">
  interface StreamMetrics {
    bandwidth_in?: number;
    bandwidth_out?: number;
    bitrate_kbps?: number;
    video_codec?: string;
    audio_codec?: string;
  }

  interface Props {
    liveMetrics: Record<string, StreamMetrics>;
    formatBytes: (bytes: number) => string;
  }

  let { liveMetrics, formatBytes }: Props = $props();

  let hasLiveStreams = $derived(Object.keys(liveMetrics).length > 0);
</script>

{#if hasLiveStreams}
  <div class="space-y-3">
    <h3 class="font-semibold text-foreground text-sm">
      Live Stream Health
    </h3>

    {#each Object.entries(liveMetrics) as [streamId, metrics] (streamId)}
      <div class="p-3 bg-muted">
        <div class="flex items-center justify-between mb-2">
          <span class="text-sm font-medium text-foreground"
            >Stream {streamId.slice(0, 8)}</span
          >
          <div class="flex items-center space-x-1">
            <div
              class="w-2 h-2 bg-success rounded-full animate-pulse"
            ></div>
            <span class="text-xs text-muted-foreground">Live</span>
          </div>
        </div>

        <div class="grid grid-cols-2 gap-2 text-xs">
          <div>
            <span class="text-muted-foreground">Bandwidth:</span>
            <span class="text-foreground ml-1"
              >{formatBytes(
                (metrics.bandwidth_in || 0) + (metrics.bandwidth_out || 0)
              )}/s</span
            >
          </div>
          <div>
            <span class="text-muted-foreground">Bitrate:</span>
            <span class="text-foreground ml-1"
              >{metrics.bitrate_kbps || "Unknown"} kbps</span
            >
          </div>
          {#if metrics.video_codec || metrics.audio_codec}
            <div class="col-span-2">
              <span class="text-muted-foreground">Codecs:</span>
              <span class="text-foreground ml-1">
                {#if metrics.video_codec}
                  Video: {metrics.video_codec}{#if metrics.audio_codec},{/if}
                {/if}
                {#if metrics.audio_codec}
                  Audio: {metrics.audio_codec}
                {/if}
              </span>
            </div>
          {/if}
        </div>
      </div>
    {/each}
  </div>
{/if}

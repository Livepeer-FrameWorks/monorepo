<script lang="ts">
  // StreamMetrics matches the StreamMetric interface from realtime.ts
  interface StreamMetrics {
    // Bandwidth in bits per second (from ViewerMetrics subscription)
    bandwidthInBps?: number;
    bandwidthOutBps?: number;
    // Legacy fields
    bitrate_kbps?: number;
    video_codec?: string;
    audio_codec?: string;
  }

  interface Props {
    liveMetrics: Record<string, StreamMetrics>;
  }

  let { liveMetrics }: Props = $props();

  let hasLiveStreams = $derived(Object.keys(liveMetrics).length > 0);

  // Convert bits per second to a human-readable format
  function formatBitsPerSec(bps: number): string {
    if (bps >= 1_000_000_000) {
      return `${(bps / 1_000_000_000).toFixed(1)} Gbps`;
    } else if (bps >= 1_000_000) {
      return `${(bps / 1_000_000).toFixed(1)} Mbps`;
    } else if (bps >= 1_000) {
      return `${(bps / 1_000).toFixed(1)} Kbps`;
    }
    return `${bps} bps`;
  }
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
              >{formatBitsPerSec(
                (metrics.bandwidthInBps || 0) + (metrics.bandwidthOutBps || 0)
              )}</span
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

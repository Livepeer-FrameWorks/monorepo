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
    <h3 class="font-semibold text-tokyo-night-fg text-sm">
      Live Stream Health
    </h3>

    {#each Object.entries(liveMetrics) as [streamId, metrics] (streamId)}
      <div class="p-3 bg-tokyo-night-bg-highlight rounded-lg">
        <div class="flex items-center justify-between mb-2">
          <span class="text-sm font-medium text-tokyo-night-fg"
            >Stream {streamId.slice(0, 8)}</span
          >
          <div class="flex items-center space-x-1">
            <div
              class="w-2 h-2 bg-tokyo-night-green rounded-full animate-pulse"
            ></div>
            <span class="text-xs text-tokyo-night-comment">Live</span>
          </div>
        </div>

        <div class="grid grid-cols-2 gap-2 text-xs">
          <div>
            <span class="text-tokyo-night-comment">Bandwidth:</span>
            <span class="text-tokyo-night-fg ml-1"
              >{formatBytes(
                (metrics.bandwidth_in || 0) + (metrics.bandwidth_out || 0)
              )}/s</span
            >
          </div>
          <div>
            <span class="text-tokyo-night-comment">Bitrate:</span>
            <span class="text-tokyo-night-fg ml-1"
              >{metrics.bitrate_kbps || "Unknown"} kbps</span
            >
          </div>
          {#if metrics.video_codec || metrics.audio_codec}
            <div class="col-span-2">
              <span class="text-tokyo-night-comment">Codecs:</span>
              <span class="text-tokyo-night-fg ml-1">
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

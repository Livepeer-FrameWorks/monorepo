<script lang="ts">
  interface StreamQualityMetrics {
    width?: number;
    height?: number;
    codec?: string;
    bitrate?: number;
    fps?: number;
    qualityTier?: string;
    frameJitterMs?: number;
    packetLossPercentage?: number;
    audioCodec?: string;
    audioSampleRate?: number;
    audioBitrate?: number;
    audioChannels?: number;
  }

  interface Props {
    metrics?: StreamQualityMetrics | null;
    compact?: boolean;
  }

  let { metrics = null, compact = false }: Props = $props();

  let formattedMetrics = $derived(
    metrics
      ? {
          resolution:
            metrics.width && metrics.height
              ? `${metrics.width}x${metrics.height}`
              : "Unknown",
          codec: metrics.codec || "Unknown",
          bitrate: metrics.bitrate
            ? `${(metrics.bitrate / 1000).toFixed(0)}k`
            : "Unknown",
          fps: metrics.fps ? `${metrics.fps.toFixed(1)} fps` : "Unknown",
          qualityTier: metrics.qualityTier || "Unknown",
          frameJitter: typeof metrics.frameJitterMs === "number"
            ? `${metrics.frameJitterMs.toFixed(1)}ms`
            : "N/A",
          packetLoss: typeof metrics.packetLossPercentage === "number"
            ? `${metrics.packetLossPercentage.toFixed(2)}%`
            : "N/A",
        }
      : null,
  );
</script>

{#if formattedMetrics && metrics}
  <div class="bg-tokyo-night-surface rounded-lg p-4">
    <h3 class="text-lg font-semibold text-tokyo-night-cyan mb-4">Quality Metrics</h3>
    
    <div class={compact ? "grid grid-cols-2 gap-3" : "grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4"}>
      <!-- Video Quality -->
      <div class="space-y-2">
        <h4 class="text-sm font-medium text-tokyo-night-comment">Video Quality</h4>
        <div class="space-y-1">
          <div class="flex justify-between">
            <span class="text-sm text-tokyo-night-fg">Resolution:</span>
            <span class="text-sm font-mono text-tokyo-night-blue">{formattedMetrics.resolution}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-sm text-tokyo-night-fg">Codec:</span>
            <span class="text-sm font-mono text-tokyo-night-purple">{formattedMetrics.codec}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-sm text-tokyo-night-fg">Bitrate:</span>
            <span class="text-sm font-mono text-tokyo-night-green">{formattedMetrics.bitrate}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-sm text-tokyo-night-fg">Frame Rate:</span>
            <span class="text-sm font-mono text-tokyo-night-orange">{formattedMetrics.fps}</span>
          </div>
        </div>
      </div>

      <!-- Performance Metrics -->
      <div class="space-y-2">
        <h4 class="text-sm font-medium text-tokyo-night-comment">Performance</h4>
        <div class="space-y-1">
          <div class="flex justify-between">
            <span class="text-sm text-tokyo-night-fg">Quality Tier:</span>
            <span class="text-sm font-mono text-tokyo-night-cyan">{formattedMetrics.qualityTier}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-sm text-tokyo-night-fg">Frame Jitter:</span>
            <span class="text-sm font-mono {metrics.frameJitterMs > 30 ? 'text-red-400' : 'text-green-400'}">
              {formattedMetrics.frameJitter}
            </span>
          </div>
          <div class="flex justify-between">
            <span class="text-sm text-tokyo-night-fg">Packet Loss:</span>
            <span class="text-sm font-mono {metrics.packetLossPercentage > 2 ? 'text-red-400' : 'text-green-400'}">
              {formattedMetrics.packetLoss}
            </span>
          </div>
        </div>
      </div>

      {#if !compact && metrics.audioCodec}
        <!-- Audio Quality -->
        <div class="space-y-2">
          <h4 class="text-sm font-medium text-tokyo-night-comment">Audio Quality</h4>
          <div class="space-y-1">
            <div class="flex justify-between">
              <span class="text-sm text-tokyo-night-fg">Codec:</span>
              <span class="text-sm font-mono text-tokyo-night-purple">{metrics.audioCodec}</span>
            </div>
            {#if metrics.audioSampleRate}
              <div class="flex justify-between">
                <span class="text-sm text-tokyo-night-fg">Sample Rate:</span>
                <span class="text-sm font-mono text-tokyo-night-blue">{metrics.audioSampleRate}Hz</span>
              </div>
            {/if}
            {#if metrics.audioBitrate}
              <div class="flex justify-between">
                <span class="text-sm text-tokyo-night-fg">Bitrate:</span>
                <span class="text-sm font-mono text-tokyo-night-green">{metrics.audioBitrate}k</span>
              </div>
            {/if}
            {#if metrics.audioChannels}
              <div class="flex justify-between">
                <span class="text-sm text-tokyo-night-fg">Channels:</span>
                <span class="text-sm font-mono text-tokyo-night-orange">{metrics.audioChannels}</span>
              </div>
            {/if}
          </div>
        </div>
      {/if}
    </div>
  </div>
{:else}
  <div class="bg-tokyo-night-surface rounded-lg p-4">
    <h3 class="text-lg font-semibold text-tokyo-night-cyan mb-4">Quality Metrics</h3>
    <p class="text-tokyo-night-comment">No quality metrics available</p>
  </div>
{/if}

<!--
  StatsPanel.svelte - "Stats for nerds" debug panel
  Port of src/components/StatsPanel.tsx
-->
<script lang="ts">
  import { cn, type ContentMetadata, type PlaybackQuality } from '@livepeer-frameworks/player-core';
  import Button from './ui/Button.svelte';

  interface StreamStateInfo {
    status?: string;
    viewers?: number;
    tracks?: Array<{
      type: string;
      codec: string;
      width?: number;
      height?: number;
      bps?: number;
      channels?: number;
    }>;
  }

  interface Props {
    isOpen: boolean;
    onClose: () => void;
    metadata?: ContentMetadata | null;
    streamState?: StreamStateInfo | null;
    quality?: PlaybackQuality | null;
    videoElement?: HTMLVideoElement | null;
    protocol?: string;
    nodeId?: string;
    geoDistance?: number;
  }

  let {
    isOpen,
    onClose,
    metadata = null,
    streamState = null,
    quality = null,
    videoElement = null,
    protocol = undefined,
    nodeId = undefined,
    geoDistance = undefined,
  }: Props = $props();

  // Video element stats (reactive)
  let currentRes = $derived(videoElement ? `${videoElement.videoWidth}x${videoElement.videoHeight}` : '—');
  let buffered = $derived.by(() => {
    if (!videoElement || videoElement.buffered.length === 0) return '—';
    return (videoElement.buffered.end(videoElement.buffered.length - 1) - videoElement.currentTime).toFixed(1);
  });
  let playbackRate = $derived(videoElement?.playbackRate?.toFixed(2) ?? '1.00');

  // Quality monitor stats
  let qualityScore = $derived(quality?.score?.toFixed(0) ?? '—');
  let bitrateKbps = $derived(quality?.bitrate ? `${(quality.bitrate / 1000).toFixed(0)} kbps` : '—');
  let frameDropRate = $derived(quality?.frameDropRate?.toFixed(1) ?? '—');
  let stallCount = $derived(quality?.stallCount ?? 0);
  let latency = $derived(quality?.latency ? `${Math.round(quality.latency)} ms` : '—');

  // Stream state stats
  let viewers = $derived(streamState?.viewers ?? metadata?.viewers ?? '—');
  let streamStatus = $derived(streamState?.status ?? metadata?.status ?? '—');

  // Format track info
  function formatTracks(): string {
    if (!streamState?.tracks?.length) return '—';
    return streamState.tracks.map(t => {
      if (t.type === 'video') {
        return `${t.codec} ${t.width}x${t.height}@${t.bps ? Math.round(t.bps / 1000) + 'kbps' : '?'}`;
      }
      return `${t.codec} ${t.channels}ch`;
    }).join(', ');
  }

  // Build stats array
  let stats = $derived.by(() => {
    const result: Array<{ label: string; value: string }> = [];

    if (metadata?.title) {
      result.push({ label: 'Title', value: metadata.title });
    }

    result.push(
      { label: 'Resolution', value: currentRes },
      { label: 'Buffer', value: `${buffered}s` },
      { label: 'Latency', value: latency },
      { label: 'Bitrate', value: bitrateKbps },
      { label: 'Quality Score', value: `${qualityScore}/100` },
      { label: 'Frame Drop Rate', value: `${frameDropRate}%` },
      { label: 'Stalls', value: String(stallCount) },
      { label: 'Playback Rate', value: `${playbackRate}x` },
      { label: 'Protocol', value: protocol ?? '—' },
      { label: 'Node', value: nodeId ?? '—' },
      { label: 'Geo Distance', value: geoDistance ? `${geoDistance.toFixed(0)} km` : '—' },
      { label: 'Viewers', value: String(viewers) },
      { label: 'Status', value: streamStatus },
      { label: 'Tracks', value: formatTracks() },
    );

    if (metadata?.durationSeconds) {
      const mins = Math.floor(metadata.durationSeconds / 60);
      const secs = metadata.durationSeconds % 60;
      result.push({ label: 'Duration', value: `${mins}:${String(secs).padStart(2, '0')}` });
    }

    if (metadata?.recordingSizeBytes) {
      const mb = (metadata.recordingSizeBytes / (1024 * 1024)).toFixed(1);
      result.push({ label: 'Size', value: `${mb} MB` });
    }

    return result;
  });
</script>

{#if isOpen}
  <div
    class={cn(
      'fw-stats-panel absolute top-2 right-2 z-30',
      'bg-black border border-white/10 rounded',
      'text-white text-xs font-mono',
      'max-w-[320px] max-h-[80%] overflow-auto',
      'shadow-lg'
    )}
    style="background-color: #000000;"
  >
    <!-- Header -->
    <div class="flex items-center justify-between px-3 py-2 border-b border-white/10">
      <span class="text-white/70 text-[10px] uppercase tracking-wider">
        Stats Overlay
      </span>
      <Button
        type="button"
        variant="ghost"
        onclick={onClose}
        class="text-white/50 hover:text-white transition-colors p-1 -mr-1 h-auto w-auto min-w-0"
        aria-label="Close stats panel"
      >
        <svg width="12" height="12" viewBox="0 0 12 12" fill="none" stroke="currentColor" stroke-width="1.5">
          <path d="M2 2l8 8M10 2l-8 8" />
        </svg>
      </Button>
    </div>

    <!-- Stats grid -->
    <div class="px-3 py-2 space-y-1">
      {#each stats as { label, value }}
        <div class="flex justify-between gap-4">
          <span class="text-white/50 shrink-0">{label}</span>
          <span class="text-white/90 truncate text-right">{value}</span>
        </div>
      {/each}
    </div>
  </div>
{/if}

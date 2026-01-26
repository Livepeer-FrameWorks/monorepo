<!--
  StatsPanel.svelte - "Stats for nerds" debug panel
  Port of src/components/StatsPanel.tsx
-->
<script lang="ts">
  import { cn, type ContentMetadata, type PlaybackQuality, type StreamState } from '@livepeer-frameworks/player-core';
  import Button from './ui/Button.svelte';

  interface Props {
    isOpen: boolean;
    onClose: () => void;
    metadata?: ContentMetadata | null;
    streamState?: StreamState | null;
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
  let viewers = $derived(metadata?.viewers ?? '—');
  let streamStatus = $derived(streamState?.status ?? metadata?.status ?? '—');

  const mistInfo = $derived(metadata?.mist ?? streamState?.streamInfo);

  function deriveTracksFromMist() {
    const mistTracks = mistInfo?.meta?.tracks;
    if (!mistTracks) return undefined;
    return Object.values(mistTracks).map((t: any) => ({
      type: t.type,
      codec: t.codec,
      width: t.width,
      height: t.height,
      bitrate: typeof t.bps === 'number' ? Math.round(t.bps) : undefined,
      fps: typeof t.fpks === 'number' ? t.fpks / 1000 : undefined,
      channels: t.channels,
      sampleRate: t.rate,
    }));
  }

  // Format track info
  function formatTracks(): string {
    const tracks = metadata?.tracks ?? deriveTracksFromMist();
    if (!tracks?.length) return '—';
    return tracks.map(t => {
      if (t.type === 'video') {
        const resolution = t.width && t.height ? `${t.width}x${t.height}` : '?';
        const bitrate = t.bitrate ? `${Math.round(t.bitrate / 1000)}kbps` : '?';
        return `${t.codec ?? '?'} ${resolution}@${bitrate}`;
      }
      const channels = t.channels ? `${t.channels}ch` : '?';
      return `${t.codec ?? '?'} ${channels}`;
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
      { label: 'Mist Type', value: mistInfo?.type ?? '—' },
      {
        label: 'Mist Buffer Window',
        value: mistInfo?.meta?.buffer_window != null
          ? String(mistInfo.meta.buffer_window)
          : '—',
      },
      { label: 'Mist Lastms', value: mistInfo?.lastms != null ? String(mistInfo.lastms) : '—' },
      { label: 'Mist Unixoffset', value: mistInfo?.unixoffset != null ? String(mistInfo.unixoffset) : '—' },
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

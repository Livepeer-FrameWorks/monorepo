<script lang="ts">
  import { resolve } from "$app/paths";
  import { getIconComponent } from "$lib/iconUtils";
  import { getShareUrl } from "$lib/config";
  import BufferStateIndicator from "$lib/components/health/BufferStateIndicator.svelte";
  import { StreamStatus } from "$houdini";

  // Stream data interface matching Houdini's generated types
  interface StreamCardData {
    id: string;
    streamId?: string;
    name: string;
    playbackId?: string | null;
    metrics?: {
      status?: string | null;
      isLive?: boolean | null;
      currentViewers?: number | null;
      // Quality metrics (from live_streams table)
      bufferState?: string | null;
      qualityTier?: string | null;
      primaryWidth?: number | null;
      primaryHeight?: number | null;
      primaryFps?: number | null;
      primaryCodec?: string | null;
      primaryBitrate?: number | null;
      hasIssues?: boolean | null;
      issuesDescription?: string | null;
    } | null;
    viewers?: number;
  }

  // Health data from StreamHealthMetric (optional, for detailed health metrics)
  interface HealthData {
    bufferState?: string | null;
    issuesDescription?: string | null;
    bufferHealth?: number | null;
    qualityTier?: string | null;
    hasIssues?: boolean | null;
  }

  interface Props {
    stream: StreamCardData;
    selected: boolean;
    deleting: boolean;
    healthData: HealthData | null;
    onSelect: () => void;
    onDelete: () => void;
  }

  let { stream, selected, deleting, healthData, onSelect, onDelete }: Props = $props();

  // Derive status from metrics edge
  const status = $derived(stream.metrics?.status);
  const isLive = $derived(status === StreamStatus.LIVE);
  const displayStreamId = $derived(stream.streamId || stream.id);

  // Merge health data: prefer explicit healthData, fall back to stream.metrics
  const effectiveHealthData = $derived.by(() => {
    if (healthData) return healthData;
    if (stream.metrics) {
      return {
        bufferState: stream.metrics.bufferState,
        qualityTier: stream.metrics.qualityTier,
        hasIssues: stream.metrics.hasIssues,
        issuesDescription: stream.metrics.issuesDescription,
        bufferHealth: null, // Only available from detailed health query
      };
    }
    return null;
  });

  const PlayIcon = getIconComponent("Play");
  const Trash2Icon = getIconComponent("Trash2");
  const Loader2Icon = getIconComponent("Loader2");
</script>

<div
  class="slab h-full transition-all duration-200 group !p-0 cursor-pointer {selected
    ? 'ring-1 ring-inset ring-primary'
    : 'hover:bg-muted/30 hover:shadow-lg'}"
  role="button"
  tabindex="0"
  onclick={onSelect}
  onkeydown={(e) => e.key === "Enter" && onSelect()}
>
  <div class="slab-header flex items-center justify-between">
    <div class="flex items-center gap-2 min-w-0">
      <h3 class="truncate text-foreground" title={stream.name}>
        {stream.name || `Stream ${displayStreamId.slice(0, 8)}`}
      </h3>
      {#if selected}
        <span
          class="text-[10px] bg-primary/20 text-primary px-1.5 py-0.5 rounded-sm font-medium shrink-0 uppercase tracking-wider"
        >
          Selected
        </span>
      {/if}
    </div>

    <div class="flex items-center gap-3">
      <div
        class="w-2 h-2 rounded-full shrink-0 {isLive
          ? 'bg-success animate-pulse'
          : 'bg-muted-foreground/30'}"
        title={isLive ? "Live" : "Offline"}
      ></div>

      <button
        class="text-muted-foreground hover:text-destructive transition-colors opacity-0 group-hover:opacity-100 focus:opacity-100 p-1 -mr-2"
        onclick={(event) => {
          event.stopPropagation();
          onDelete();
        }}
        disabled={deleting}
        title="Delete stream"
      >
        {#if deleting}
          <Loader2Icon class="w-3.5 h-3.5 animate-spin" />
        {:else}
          <Trash2Icon class="w-3.5 h-3.5" />
        {/if}
      </button>
    </div>
  </div>

  <div class="slab-body--padded flex-1 flex flex-col gap-3">
    <div class="grid grid-cols-2 gap-4 text-sm">
      <div>
        <p class="text-xs text-muted-foreground uppercase tracking-wider mb-1">Status</p>
        <p class="font-medium text-foreground capitalize">
          {status?.toLowerCase() || "offline"}
        </p>
      </div>
      <div>
        <p class="text-xs text-muted-foreground uppercase tracking-wider mb-1">Viewers</p>
        <p class="font-medium text-foreground">
          {stream.viewers || 0}
        </p>
      </div>
    </div>

    <!-- Health Indicator -->
    {#if effectiveHealthData}
      <div class="pt-3 border-t border-border/30">
        <div class="flex items-center gap-2 mb-1">
          <BufferStateIndicator
            bufferState={effectiveHealthData.bufferState ?? undefined}
            compact
          />
          <span class="text-xs font-medium capitalize">
            {(effectiveHealthData.bufferState ?? "unknown").toLowerCase()}
          </span>
          {#if effectiveHealthData.qualityTier}
            <span
              class="ml-auto text-[10px] px-1.5 py-0.5 bg-accent/10 text-accent border border-accent/20 rounded-sm"
            >
              {effectiveHealthData.qualityTier}
            </span>
          {/if}
        </div>
        {#if effectiveHealthData?.hasIssues && effectiveHealthData?.issuesDescription}
          <p
            class="text-xs text-destructive mt-1.5 truncate"
            title={effectiveHealthData.issuesDescription}
          >
            ⚠ {effectiveHealthData.issuesDescription}
          </p>
        {/if}
      </div>
    {/if}
  </div>

  {#if isLive}
    <div class="slab-actions">
      <a
        href={resolve(getShareUrl(stream.playbackId || displayStreamId))}
        class="flex items-center justify-center py-3 text-sm font-medium text-primary hover:bg-primary/5 transition-colors"
        onclick={(event) => event.stopPropagation()}
        title="Watch live stream"
      >
        <PlayIcon class="w-4 h-4 mr-2" />
        Watch Live
      </a>
    </div>
  {/if}
</div>

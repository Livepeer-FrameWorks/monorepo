<script lang="ts">
  import { resolve } from "$app/paths";
  import { getIconComponent } from "$lib/iconUtils";
  import BufferStateIndicator from "$lib/components/health/BufferStateIndicator.svelte";
  import { StreamStatus } from "$houdini";

  // Stream data interface matching Houdini's generated types
  interface StreamCardData {
    id: string;
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

  let { stream, selected, deleting, healthData, onSelect, onDelete }: Props =
    $props();

  // Derive status from metrics edge
  const status = $derived(stream.metrics?.status);
  const isLive = $derived(status === StreamStatus.LIVE);

  // Merge health data: prefer explicit healthData, fall back to stream.metrics
  const effectiveHealthData = $derived.by(() => {
    if (healthData) return healthData;
    if (stream.metrics) {
      return {
        bufferState: stream.metrics.bufferState,
        qualityTier: stream.metrics.qualityTier,
        hasIssues: stream.metrics.hasIssues,
        issuesDescription: stream.metrics.issuesDescription,
        bufferHealth: null // Only available from detailed health query
      };
    }
    return null;
  });

  const PlayIcon = getIconComponent("Play");
  const Trash2Icon = getIconComponent("Trash2");
  const Loader2Icon = getIconComponent("Loader2");
</script>

<div
  class="bg-muted p-4 border cursor-pointer transition-all group {selected
    ? 'border-primary ring-2 ring-primary/20 bg-primary/5'
    : 'border-border hover:border-info hover:bg-muted/80'}"
  role="button"
  tabindex="0"
  onclick={onSelect}
  onkeydown={(e) => e.key === "Enter" && onSelect()}
>
  <div class="flex items-center justify-between mb-3">
    <div class="flex items-center gap-2 min-w-0">
      <h3 class="font-semibold text-foreground truncate">
        {stream.name || `Stream ${stream.id.slice(0, 8)}`}
      </h3>
      {#if selected}
        <span class="text-xs bg-primary/20 text-primary px-1.5 py-0.5 rounded font-medium shrink-0">
          Selected
        </span>
      {/if}
    </div>
    <div class="flex items-center space-x-2">
      <div
        class="w-2 h-2 rounded-full {isLive
          ? 'bg-success animate-pulse'
          : 'bg-destructive'}"
      ></div>
      {#if isLive}
        <a
          href={resolve(`/view?type=live&id=${stream.playbackId || stream.id}` as any)}
          class="text-info hover:text-primary text-sm p-1"
          onclick={(event) => event.stopPropagation()}
          title="Watch live stream"
        >
          <PlayIcon class="w-4 h-4" />
        </a>
      {/if}
      <button
        class="p-1.5 text-muted-foreground hover:text-destructive hover:bg-destructive/10 transition-colors opacity-0 group-hover:opacity-100 focus:opacity-100"
        onclick={(event) => {
          event.stopPropagation();
          onDelete();
        }}
        disabled={deleting}
        title="Delete stream"
      >
        {#if deleting}
          <Loader2Icon class="w-4 h-4 animate-spin" />
        {:else}
          <Trash2Icon class="w-4 h-4" />
        {/if}
      </button>
    </div>
  </div>

  <div class="grid grid-cols-2 gap-4 text-sm mb-3">
    <div>
      <p class="text-muted-foreground">Status</p>
      <p class="font-semibold text-foreground capitalize">
        {status?.toLowerCase() || "offline"}
      </p>
    </div>
    <div>
      <p class="text-muted-foreground">Viewers</p>
      <p class="font-semibold text-foreground">
        {stream.viewers || 0}
      </p>
    </div>
  </div>

  <!-- Health Indicator -->
  <div class="mb-3">
    <div class="flex items-center justify-between">
      {#if effectiveHealthData}
        <div class="flex items-center gap-3">
          <div class="flex items-center gap-1.5">
            <BufferStateIndicator
              bufferState={effectiveHealthData.bufferState ?? undefined}
              compact
            />
            <span class="text-xs text-muted-foreground capitalize">
              {(effectiveHealthData.bufferState ?? 'unknown').toLowerCase()}
            </span>
          </div>
          {#if effectiveHealthData.qualityTier}
            <span class="text-xs px-1.5 py-0.5 bg-accent/10 text-accent border border-accent/20">
              {effectiveHealthData.qualityTier}
            </span>
          {/if}
        </div>
      {:else}
        <span class="text-xs text-muted-foreground">—</span>
      {/if}
      <a
        href={resolve(`/streams/${stream.id}`)}
        class="text-xs text-info hover:text-primary hover:underline transition-colors"
        onclick={(event) => event.stopPropagation()}
      >
        Details →
      </a>
    </div>
    {#if effectiveHealthData?.hasIssues && effectiveHealthData?.issuesDescription}
      <p class="text-xs text-destructive mt-1.5 truncate">
        ⚠ {effectiveHealthData.issuesDescription}
      </p>
    {/if}
  </div>

  <div class="pt-3 border-t border-border">
    <p class="text-xs text-muted-foreground truncate">
      ID: {stream.playbackId || stream.id.slice(0, 16)}
    </p>
  </div>
</div>

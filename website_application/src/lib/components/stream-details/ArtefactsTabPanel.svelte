<script lang="ts">
  import { goto } from "$app/navigation";
  import { getIconComponent } from "$lib/iconUtils";
  import { formatDate, formatDuration } from "$lib/utils/stream-helpers";
  import { isExpired } from "$lib/utils/formatters.js";
  import { Button } from "$lib/components/ui/button";
  import { getContentDeliveryUrls } from "$lib/config";
  import PlaybackProtocols from "$lib/components/PlaybackProtocols.svelte";

  interface Recording {
    dvrHash: string;
    playbackId?: string | null;
    streamId?: string | null;
    stream?: { streamId: string } | null;
    manifestPath?: string | null;
    status?: string | null;
    createdAt?: string | null;
    expiresAt?: string | null;
    durationSeconds?: number | null;
    sizeBytes?: number | null;
    isFrozen?: boolean;
  }

  interface Clip {
    id: string;
    clipHash?: string | null;
    playbackId?: string | null;
    streamId?: string | null;
    stream?: { streamId: string } | null;
    title?: string | null;
    status?: string | null;
    createdAt?: string | null;
    expiresAt?: string | null;
    duration?: number | null;
    sizeBytes?: number | null;
    isFrozen?: boolean;
  }

  interface Props {
    recordings: Recording[];
    clips: Clip[];
    onEnableRecording?: () => void;
  }

  let { recordings, clips, onEnableRecording }: Props = $props();

  let expandedItem = $state<string | null>(null);
  let activeTab = $state<"all" | "dvr" | "clips">("all");

  // Storage calculations
  const storageStats = $derived.by(() => {
    const dvrBytes = recordings.reduce((sum, r) => sum + (r.sizeBytes || 0), 0);
    const clipBytes = clips.reduce((sum, c) => sum + (c.sizeBytes || 0), 0);
    const frozenDvr = recordings.filter((r) => r.isFrozen).length;
    const frozenClips = clips.filter((c) => c.isFrozen).length;

    return {
      totalBytes: dvrBytes + clipBytes,
      dvrBytes,
      clipBytes,
      frozenCount: frozenDvr + frozenClips,
      dvrCount: recordings.length,
      clipCount: clips.length,
    };
  });

  // Filter items based on active tab
  const visibleRecordings = $derived(activeTab === "clips" ? [] : recordings);
  const visibleClips = $derived(activeTab === "dvr" ? [] : clips);
  const hasItems = $derived(recordings.length > 0 || clips.length > 0);

  function formatBytes(bytes: number): string {
    if (bytes === 0) return "0 B";
    const k = 1024;
    const sizes = ["B", "KB", "MB", "GB", "TB"];
    const i = Math.floor(Math.log(bytes) / Math.log(k));
    return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + " " + sizes[i];
  }

  function getRecordingUrls(playbackId: string | undefined | null) {
    if (!playbackId) return null;
    return getContentDeliveryUrls(playbackId, "dvr");
  }

  function getClipUrls(playbackId: string | undefined | null) {
    if (!playbackId) return null;
    return getContentDeliveryUrls(playbackId, "clip");
  }

  function playContent(playbackId: string) {
    // eslint-disable-next-line svelte/no-navigation-without-resolve
    goto(`/view?id=${playbackId}`);
  }

  function getStatusClass(status: string | null | undefined): string {
    const s = status?.toLowerCase();
    if (s === "deleted" || s === "failed") return "bg-error/20 text-error";
    if (s === "processing" || s === "queued" || s === "requested")
      return "bg-warning/20 text-warning";
    if (s === "completed" || s === "ready") return "bg-success/20 text-success";
    return "bg-info/20 text-info";
  }

  function isPlayable(
    status: string | null | undefined,
    playbackId: string | null | undefined,
    expired: boolean
  ): boolean {
    if (!playbackId || expired) return false;
    const s = status?.toLowerCase();
    return !["deleted", "failed", "processing", "requested", "queued"].includes(s || "");
  }

  const DownloadIcon = getIconComponent("Download");
  const PlayIcon = getIconComponent("Play");
  const ChevronDownIcon = getIconComponent("ChevronDown");
  const ChevronUpIcon = getIconComponent("ChevronUp");
  const FilmIcon = getIconComponent("Film");
  const ScissorsIcon = getIconComponent("Scissors");
  const HardDriveIcon = getIconComponent("HardDrive");
  const SnowflakeIcon = getIconComponent("Snowflake");
</script>

<div class="border-t border-[hsl(var(--tn-fg-gutter)/0.3)]">
  <!-- Storage Summary -->
  <div class="p-4 bg-muted/10 border-b border-[hsl(var(--tn-fg-gutter)/0.3)]">
    <div class="flex flex-wrap items-center gap-6">
      <div class="flex items-center gap-2">
        <HardDriveIcon class="w-4 h-4 text-accent-purple" />
        <span class="text-sm text-muted-foreground">Total:</span>
        <span class="font-mono text-sm text-foreground font-medium">
          {formatBytes(storageStats.totalBytes)}
        </span>
      </div>
      <div class="flex items-center gap-2">
        <FilmIcon class="w-4 h-4 text-blue-500" />
        <span class="text-sm text-muted-foreground">DVR:</span>
        <span class="font-mono text-sm text-foreground">
          {formatBytes(storageStats.dvrBytes)}
        </span>
        <span class="text-xs text-muted-foreground">({storageStats.dvrCount})</span>
      </div>
      <div class="flex items-center gap-2">
        <ScissorsIcon class="w-4 h-4 text-purple-500" />
        <span class="text-sm text-muted-foreground">Clips:</span>
        <span class="font-mono text-sm text-foreground">
          {formatBytes(storageStats.clipBytes)}
        </span>
        <span class="text-xs text-muted-foreground">({storageStats.clipCount})</span>
      </div>
      {#if storageStats.frozenCount > 0}
        <div class="flex items-center gap-2 text-blue-400">
          <SnowflakeIcon class="w-4 h-4" />
          <span class="text-sm">{storageStats.frozenCount} archived</span>
        </div>
      {/if}
    </div>
  </div>

  <!-- Filter Tabs -->
  {#if hasItems}
    <div class="flex border-b border-[hsl(var(--tn-fg-gutter)/0.3)]">
      <button
        type="button"
        class="px-4 py-2 text-sm font-medium transition-colors {activeTab === 'all'
          ? 'text-primary border-b-2 border-primary'
          : 'text-muted-foreground hover:text-foreground'}"
        onclick={() => (activeTab = "all")}
      >
        All ({recordings.length + clips.length})
      </button>
      <button
        type="button"
        class="px-4 py-2 text-sm font-medium transition-colors {activeTab === 'dvr'
          ? 'text-primary border-b-2 border-primary'
          : 'text-muted-foreground hover:text-foreground'}"
        onclick={() => (activeTab = "dvr")}
      >
        DVR ({recordings.length})
      </button>
      <button
        type="button"
        class="px-4 py-2 text-sm font-medium transition-colors {activeTab === 'clips'
          ? 'text-primary border-b-2 border-primary'
          : 'text-muted-foreground hover:text-foreground'}"
        onclick={() => (activeTab = "clips")}
      >
        Clips ({clips.length})
      </button>
    </div>
  {/if}

  {#if !hasItems}
    <div class="p-6">
      <div
        class="bg-[hsl(var(--tn-bg-dark)/0.3)] border border-[hsl(var(--tn-fg-gutter)/0.3)] p-6 text-center space-y-4"
      >
        <p class="text-foreground font-medium">No artefacts available</p>
        <p class="text-sm text-muted-foreground">
          DVR recordings and clips appear here when created from this stream.
        </p>
        <Button onclick={onEnableRecording} variant="ghost" class="gap-2 w-full sm:w-auto mx-auto">
          Enable Recording
        </Button>
      </div>
    </div>
  {:else}
    <div class="divide-y divide-[hsl(var(--tn-fg-gutter)/0.3)]">
      <!-- DVR Recordings -->
      {#each visibleRecordings as recording (recording.dvrHash)}
        {@const urls = getRecordingUrls(recording.playbackId)}
        {@const isExpanded = expandedItem === `dvr-${recording.dvrHash}`}
        {@const expired = isExpired(recording.expiresAt)}
        {@const canPlay = isPlayable(recording.status, recording.playbackId, expired)}
        {@const displayId = recording.stream?.streamId ?? recording.streamId ?? recording.dvrHash}
        <div>
          <div class="p-4">
            <div class="flex flex-col md:flex-row md:items-center md:justify-between gap-4">
              <div class="space-y-1">
                <div class="flex items-center gap-2">
                  <FilmIcon class="w-4 h-4 text-blue-500 shrink-0" />
                  <h5
                    class="text-sm font-medium text-foreground truncate max-w-md"
                    title={recording.dvrHash}
                  >
                    {displayId}
                  </h5>
                  <span
                    class="text-xs px-2 py-0.5 rounded-full font-medium {getStatusClass(
                      recording.status
                    )}"
                  >
                    {recording.status || "Ready"}
                  </span>
                  {#if recording.isFrozen}
                    <SnowflakeIcon class="w-3.5 h-3.5 text-blue-400" title="Archived" />
                  {/if}
                </div>
                <p class="text-xs text-muted-foreground">
                  {recording.createdAt ? formatDate(recording.createdAt) : "N/A"} •
                  {formatDuration(recording.durationSeconds || 0)}
                  {#if recording.sizeBytes}
                    • {formatBytes(recording.sizeBytes)}
                  {/if}
                </p>
              </div>

              <div class="flex items-center gap-2">
                {#if canPlay && urls?.primary.hls}
                  <Button
                    variant="ghost"
                    size="sm"
                    onclick={() => recording.playbackId && playContent(recording.playbackId)}
                    class="gap-2 border border-border/30"
                  >
                    <PlayIcon class="w-4 h-4" />
                    Play
                  </Button>
                  <Button
                    href={urls.primary.mp4}
                    target="_blank"
                    rel="noopener noreferrer"
                    variant="ghost"
                    size="sm"
                    class="gap-2 border border-border/30"
                  >
                    <DownloadIcon class="w-4 h-4" />
                  </Button>
                {:else if expired}
                  <span class="text-xs text-muted-foreground italic">Expired</span>
                {:else if !canPlay}
                  <span class="text-xs text-muted-foreground italic"
                    >{recording.status || "Processing"}</span
                  >
                {/if}
                {#if canPlay}
                  <Button
                    variant="ghost"
                    size="sm"
                    onclick={() => (expandedItem = isExpanded ? null : `dvr-${recording.dvrHash}`)}
                    class="border border-border/30"
                  >
                    {#if isExpanded}
                      <ChevronUpIcon class="w-4 h-4" />
                    {:else}
                      <ChevronDownIcon class="w-4 h-4" />
                    {/if}
                  </Button>
                {/if}
              </div>
            </div>
          </div>

          {#if isExpanded && canPlay}
            <div class="px-4 pb-4 border-t border-[hsl(var(--tn-fg-gutter)/0.2)] bg-muted/5">
              <PlaybackProtocols
                contentId={recording.playbackId ?? ""}
                contentType="dvr"
                showPrimary={true}
                showAdditional={true}
              />
            </div>
          {/if}
        </div>
      {/each}

      <!-- Clips -->
      {#each visibleClips as clip (clip.id)}
        {@const urls = getClipUrls(clip.playbackId)}
        {@const isExpanded = expandedItem === `clip-${clip.id}`}
        {@const expired = isExpired(clip.expiresAt)}
        {@const canPlay = isPlayable(clip.status, clip.playbackId, expired)}
        {@const displayTitle = clip.title || clip.clipHash || clip.id}
        <div>
          <div class="p-4">
            <div class="flex flex-col md:flex-row md:items-center md:justify-between gap-4">
              <div class="space-y-1">
                <div class="flex items-center gap-2">
                  <ScissorsIcon class="w-4 h-4 text-purple-500 shrink-0" />
                  <h5
                    class="text-sm font-medium text-foreground truncate max-w-md"
                    title={clip.clipHash || clip.id}
                  >
                    {displayTitle}
                  </h5>
                  <span
                    class="text-xs px-2 py-0.5 rounded-full font-medium {getStatusClass(
                      clip.status
                    )}"
                  >
                    {clip.status || "Ready"}
                  </span>
                  {#if clip.isFrozen}
                    <SnowflakeIcon class="w-3.5 h-3.5 text-blue-400" title="Archived" />
                  {/if}
                </div>
                <p class="text-xs text-muted-foreground">
                  {clip.createdAt ? formatDate(clip.createdAt) : "N/A"} •
                  {formatDuration(clip.duration || 0)}
                  {#if clip.sizeBytes}
                    • {formatBytes(clip.sizeBytes)}
                  {/if}
                </p>
              </div>

              <div class="flex items-center gap-2">
                {#if canPlay && urls?.primary.hls}
                  <Button
                    variant="ghost"
                    size="sm"
                    onclick={() => clip.playbackId && playContent(clip.playbackId)}
                    class="gap-2 border border-border/30"
                  >
                    <PlayIcon class="w-4 h-4" />
                    Play
                  </Button>
                  <Button
                    href={urls.primary.mp4}
                    target="_blank"
                    rel="noopener noreferrer"
                    variant="ghost"
                    size="sm"
                    class="gap-2 border border-border/30"
                  >
                    <DownloadIcon class="w-4 h-4" />
                  </Button>
                {:else if expired}
                  <span class="text-xs text-muted-foreground italic">Expired</span>
                {:else if !canPlay}
                  <span class="text-xs text-muted-foreground italic"
                    >{clip.status || "Processing"}</span
                  >
                {/if}
                {#if canPlay}
                  <Button
                    variant="ghost"
                    size="sm"
                    onclick={() => (expandedItem = isExpanded ? null : `clip-${clip.id}`)}
                    class="border border-border/30"
                  >
                    {#if isExpanded}
                      <ChevronUpIcon class="w-4 h-4" />
                    {:else}
                      <ChevronDownIcon class="w-4 h-4" />
                    {/if}
                  </Button>
                {/if}
              </div>
            </div>
          </div>

          {#if isExpanded && canPlay}
            <div class="px-4 pb-4 border-t border-[hsl(var(--tn-fg-gutter)/0.2)] bg-muted/5">
              <PlaybackProtocols
                contentId={clip.playbackId ?? ""}
                contentType="clip"
                showPrimary={true}
                showAdditional={true}
              />
            </div>
          {/if}
        </div>
      {/each}
    </div>
  {/if}
</div>

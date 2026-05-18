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
    sourceStreamId?: string | null;
    stream?: { streamId: string } | null;
    manifestPath?: string | null;
    status?: string | null;
    createdAt?: string | null;
    expiresAt?: string | null;
    durationSeconds?: number | null;
    sizeBytes?: number | null;
    isFrozen?: boolean | null;
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
    isFrozen?: boolean | null;
  }

  interface VodArtifact {
    id: string;
    artifactHash: string;
    playbackId?: string | null;
    streamId?: string | null;
    originType?: string | null;
    originId?: string | null;
    title?: string | null;
    filename?: string | null;
    status?: string | null;
    createdAt?: string | null;
    expiresAt?: string | null;
    durationMs?: number | null;
    sizeBytes?: number | null;
  }

  interface Props {
    recordings: Recording[];
    clips: Clip[];
    vodArtifacts?: VodArtifact[];
    onEnableRecording?: () => void;
  }

  let { recordings, clips, vodArtifacts = [], onEnableRecording }: Props = $props();

  let expandedItem = $state<string | null>(null);
  let activeTab = $state<"all" | "dvr" | "clips" | "vod">("all");

  // Storage calculations
  const storageStats = $derived.by(() => {
    const dvrBytes = recordings.reduce((sum, r) => sum + (r.sizeBytes || 0), 0);
    const clipBytes = clips.reduce((sum, c) => sum + (c.sizeBytes || 0), 0);
    const vodBytes = vodArtifacts.reduce((sum, v) => sum + (v.sizeBytes || 0), 0);
    const frozenDvr = recordings.filter((r) => r.isFrozen).length;
    const frozenClips = clips.filter((c) => c.isFrozen).length;

    return {
      totalBytes: dvrBytes + clipBytes + vodBytes,
      dvrBytes,
      clipBytes,
      vodBytes,
      frozenCount: frozenDvr + frozenClips,
      dvrCount: recordings.length,
      clipCount: clips.length,
      vodCount: vodArtifacts.length,
    };
  });

  // Filter items based on active tab
  const visibleRecordings = $derived(
    activeTab === "clips" || activeTab === "vod" ? [] : recordings
  );
  const visibleClips = $derived(activeTab === "dvr" || activeTab === "vod" ? [] : clips);
  const visibleVodArtifacts = $derived(
    activeTab === "dvr" || activeTab === "clips" ? [] : vodArtifacts
  );
  const hasItems = $derived(recordings.length > 0 || clips.length > 0 || vodArtifacts.length > 0);

  function formatBytes(bytes: number): string {
    if (bytes === 0) return "0 B";
    const k = 1024;
    const sizes = ["B", "KB", "MB", "GB", "TB"];
    const i = Math.floor(Math.log(bytes) / Math.log(k));
    return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + " " + sizes[i];
  }

  function getClipUrls(playbackId: string | undefined | null) {
    if (!playbackId) return null;
    return getContentDeliveryUrls(playbackId, "clip");
  }

  function playContent(playbackId: string) {
    // eslint-disable-next-line svelte/no-navigation-without-resolve
    goto(`/view?id=${playbackId}`);
  }

  function dvrViewUrl(recording: Recording) {
    // /view resolves through resolveViewerEndpoint, which expects the
    // public playback_id. The internal dvr_hash is not a playback
    // identifier — callers without playbackId are not playable
    // (recording in pre-flight, failed, or deleted state).
    if (!recording.playbackId) return null;
    const params = new URLSearchParams({ type: "dvr", id: recording.playbackId });
    return `/view?${params.toString()}`;
  }

  function playDvrRecording(recording: Recording) {
    const url = dvrViewUrl(recording);
    if (!url) return;
    // eslint-disable-next-line svelte/no-navigation-without-resolve
    goto(url);
  }

  function toggleDvrDetails(recording: Recording) {
    const key = `dvr-${recording.dvrHash}`;
    expandedItem = expandedItem === key ? null : key;
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
  const VideoIcon = getIconComponent("Video");
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
      <div class="flex items-center gap-2">
        <VideoIcon class="w-4 h-4 text-emerald-500" />
        <span class="text-sm text-muted-foreground">VOD:</span>
        <span class="font-mono text-sm text-foreground">
          {formatBytes(storageStats.vodBytes)}
        </span>
        <span class="text-xs text-muted-foreground">({storageStats.vodCount})</span>
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
        All ({recordings.length + clips.length + vodArtifacts.length})
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
      <button
        type="button"
        class="px-4 py-2 text-sm font-medium transition-colors {activeTab === 'vod'
          ? 'text-primary border-b-2 border-primary'
          : 'text-muted-foreground hover:text-foreground'}"
        onclick={() => (activeTab = "vod")}
      >
        VOD ({vodArtifacts.length})
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
        {@const isExpanded = expandedItem === `dvr-${recording.dvrHash}`}
        {@const expired = isExpired(recording.expiresAt)}
        {@const canPlay = isPlayable(recording.status, recording.playbackId, expired)}
        {@const displayId =
          recording.sourceStreamId ??
          recording.stream?.streamId ??
          recording.streamId ??
          recording.dvrHash}
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
                {#if canPlay}
                  <Button
                    variant="ghost"
                    size="sm"
                    onclick={() => playDvrRecording(recording)}
                    class="gap-2 border border-border/30"
                  >
                    <PlayIcon class="w-4 h-4" />
                    Play
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
                    onclick={() => toggleDvrDetails(recording)}
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
              <p class="pt-4 text-sm text-muted-foreground">
                Chapter rotation is configured on the source stream and applies to the next
                recording. Edit the stream to change <em>Historical chapters</em> or
                <em>Chapter interval</em>.
              </p>
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

      <!-- VOD-shaped stream artifacts -->
      {#each visibleVodArtifacts as vod (vod.id)}
        {@const isExpanded = expandedItem === `vod-${vod.id}`}
        {@const expired = isExpired(vod.expiresAt)}
        {@const canPlay = isPlayable(vod.status, vod.playbackId, expired)}
        {@const displayTitle =
          vod.title ||
          (vod.originType === "dvr_chapter" ? "DVR chapter" : vod.filename) ||
          vod.artifactHash}
        <div>
          <div class="p-4">
            <div class="flex flex-col md:flex-row md:items-center md:justify-between gap-4">
              <div class="space-y-1">
                <div class="flex items-center gap-2">
                  <VideoIcon class="w-4 h-4 text-emerald-500 shrink-0" />
                  <h5
                    class="text-sm font-medium text-foreground truncate max-w-md"
                    title={vod.artifactHash}
                  >
                    {displayTitle}
                  </h5>
                  {#if vod.originType === "dvr_chapter"}
                    <span class="text-xs px-2 py-0.5 rounded-full font-medium bg-info/20 text-info">
                      Chapter
                    </span>
                  {/if}
                  <span
                    class="text-xs px-2 py-0.5 rounded-full font-medium {getStatusClass(
                      vod.status
                    )}"
                  >
                    {vod.status || "Ready"}
                  </span>
                </div>
                <p class="text-xs text-muted-foreground">
                  {vod.createdAt ? formatDate(vod.createdAt) : "N/A"} •
                  {formatDuration(vod.durationMs ? Math.floor(vod.durationMs / 1000) : 0)}
                  {#if vod.sizeBytes}
                    • {formatBytes(vod.sizeBytes)}
                  {/if}
                </p>
              </div>

              <div class="flex items-center gap-2">
                {#if canPlay}
                  <Button
                    variant="ghost"
                    size="sm"
                    onclick={() => vod.playbackId && playContent(vod.playbackId)}
                    class="gap-2 border border-border/30"
                  >
                    <PlayIcon class="w-4 h-4" />
                    Play
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    onclick={() => (expandedItem = isExpanded ? null : `vod-${vod.id}`)}
                    class="border border-border/30"
                  >
                    {#if isExpanded}
                      <ChevronUpIcon class="w-4 h-4" />
                    {:else}
                      <ChevronDownIcon class="w-4 h-4" />
                    {/if}
                  </Button>
                {:else if expired}
                  <span class="text-xs text-muted-foreground italic">Expired</span>
                {:else}
                  <span class="text-xs text-muted-foreground italic"
                    >{vod.status || "Processing"}</span
                  >
                {/if}
              </div>
            </div>
          </div>

          {#if isExpanded && canPlay}
            <div class="px-4 pb-4 border-t border-[hsl(var(--tn-fg-gutter)/0.2)] bg-muted/5">
              <PlaybackProtocols
                contentId={vod.playbackId ?? ""}
                contentType="vod"
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

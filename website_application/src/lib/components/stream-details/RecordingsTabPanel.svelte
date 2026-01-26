<script lang="ts">
  import { goto } from "$app/navigation";
  import { resolve } from "$app/paths";
  import { getIconComponent } from "$lib/iconUtils";
  import { formatDate, formatDuration } from "$lib/utils/stream-helpers";
  import { isExpired } from "$lib/utils/formatters.js";
  import { Button } from "$lib/components/ui/button";
  import { getContentDeliveryUrls, getShareUrl } from "$lib/config";
  import PlaybackProtocols from "$lib/components/PlaybackProtocols.svelte";

  interface Recording {
    dvrHash: string;
    playbackId?: string | null;
    streamId?: string | null;
    stream?: {
      streamId: string;
    } | null;
    manifestPath?: string | null;
    status?: string | null;
    createdAt?: string | null;
    expiresAt?: string | null;
    durationSeconds?: number | null;
  }

  interface Props {
    recordings: Recording[];
    onEnableRecording?: () => void;
  }

  let { recordings, onEnableRecording }: Props = $props();

  let expandedRecording = $state<string | null>(null);

  // Get delivery URLs for a recording using playbackId
  function getRecordingUrls(playbackId: string | undefined | null) {
    if (!playbackId) return null;
    return getContentDeliveryUrls(playbackId, "dvr");
  }

  function playRecording(playbackId: string) {
    const url = getShareUrl(playbackId);
    if (url) {
      goto(resolve(url));
    }
  }

  const DownloadIcon = getIconComponent("Download");
  const PlayIcon = getIconComponent("Play");
  const ChevronDownIcon = getIconComponent("ChevronDown");
  const ChevronUpIcon = getIconComponent("ChevronUp");
</script>

<div class="slab border-t border-[hsl(var(--tn-fg-gutter)/0.3)]">
  <div class="slab-header">
    <h3 class="font-semibold text-xs uppercase tracking-wide text-muted-foreground">Recordings</h3>
    <p class="text-xs text-muted-foreground/70 mt-1">
      View and manage DVR recordings for this stream.
    </p>
  </div>

  {#if recordings.length === 0}
    <div class="slab-body--padded">
      <div class="bg-[hsl(var(--tn-bg-dark)/0.3)] border border-[hsl(var(--tn-fg-gutter)/0.3)] p-6 text-center space-y-4">
        <p class="text-foreground font-medium">No recordings available</p>
        <p class="text-sm text-muted-foreground">
          Recordings appear here when DVR is enabled and streams have been archived.
        </p>
        <Button
          onclick={onEnableRecording}
          variant="ghost"
          class="gap-2 w-full sm:w-auto mx-auto"
        >
          Enable Recording
        </Button>
      </div>
    </div>
  {:else}
    <div class="slab-body--flush">
      {#each recordings as recording (recording.dvrHash)}
        {@const urls = getRecordingUrls(recording.playbackId)}
        {@const isExpanded = expandedRecording === recording.dvrHash}
        {@const isExpiredRecording = isExpired(recording.expiresAt)}
        {@const isPlayable = !!recording.playbackId && !isExpiredRecording && recording.status && !['deleted', 'failed', 'processing', 'requested', 'queued'].includes(recording.status.toLowerCase())}
        {@const displayStreamId = recording.stream?.streamId ?? recording.streamId}
        <div class="border-b border-[hsl(var(--tn-fg-gutter)/0.3)] last:border-0">
          <!-- Recording header -->
          <div class="p-4">
            <div class="flex flex-col md:flex-row md:items-center md:justify-between gap-4">
              <div class="space-y-1">
                <div class="flex items-center space-x-2">
                  <h5 class="text-base font-semibold text-foreground truncate max-w-md" title={recording.dvrHash}>
                    {displayStreamId || recording.dvrHash}
                  </h5>
                  <span
                    class="text-xs px-2 py-1 rounded-full font-medium {
                      recording.status?.toLowerCase() === 'deleted' ? 'bg-error/20 text-error' :
                      recording.status?.toLowerCase() === 'failed' ? 'bg-error/20 text-error' :
                      recording.status?.toLowerCase() === 'processing' ? 'bg-warning/20 text-warning' :
                      recording.status?.toLowerCase() === 'completed' ? 'bg-success/20 text-success' :
                      'bg-info/20 text-info'
                    }"
                  >
                    {recording.status || "Ready"}
                  </span>
                </div>
                <p class="text-xs text-muted-foreground">
                  Created: {recording.createdAt ? formatDate(recording.createdAt) : "N/A"} • Duration: {formatDuration(recording.durationSeconds || 0)}
                </p>
              </div>

              <div class="flex items-center space-x-2">
                {#if isPlayable && urls?.primary.hls}
                  <Button
                    variant="ghost"
                    size="sm"
                    onclick={() => recording.playbackId && playRecording(recording.playbackId)}
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
                    Download
                  </Button>
                {:else if isExpiredRecording}
                  <span class="text-xs text-muted-foreground italic">Expired</span>
                {:else if !isPlayable}
                  <span class="text-xs text-muted-foreground italic">Not available</span>
                {/if}
                {#if isPlayable}
                  <Button
                    variant="ghost"
                    size="sm"
                    onclick={() => expandedRecording = isExpanded ? null : recording.dvrHash}
                    class="border border-border/30"
                    title={isExpanded ? "Collapse protocols" : "Show all protocols"}
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

          <!-- Expanded protocol URLs -->
          {#if isExpanded && isPlayable}
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
    </div>
  {/if}
</div>

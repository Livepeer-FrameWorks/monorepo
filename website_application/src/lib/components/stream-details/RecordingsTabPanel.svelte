<script lang="ts">
  import { goto } from "$app/navigation";
  import { getIconComponent } from "$lib/iconUtils";
  import { formatDate, formatDuration } from "$lib/utils/stream-helpers";
  import { Button } from "$lib/components/ui/button";
  import { getContentDeliveryUrls } from "$lib/config";
  import PlaybackProtocols from "$lib/components/PlaybackProtocols.svelte";

  interface Recording {
    dvrHash: string;
    internalName?: string | null;
    manifestPath?: string | null;
    status?: string | null;
    createdAt?: string | null;
    durationSeconds?: number | null;
  }

  interface Props {
    recordings: Recording[];
    onEnableRecording?: () => void;
    onCopyLink?: (url: string) => void;
  }

  let { recordings, onEnableRecording, onCopyLink }: Props = $props();

  let expandedRecording = $state<string | null>(null);

  // Get delivery URLs for a recording using dvrHash
  function getRecordingUrls(dvrHash: string | undefined | null) {
    if (!dvrHash) return null;
    return getContentDeliveryUrls(dvrHash, "dvr");
  }

  function playRecording(dvrHash: string) {
    goto(`/view?type=dvr&id=${dvrHash}`);
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
        {@const urls = getRecordingUrls(recording.dvrHash)}
        {@const isExpanded = expandedRecording === recording.dvrHash}
        {@const isPlayable = recording.status && !['deleted', 'failed', 'processing', 'requested', 'queued'].includes(recording.status.toLowerCase())}
        <div class="border-b border-[hsl(var(--tn-fg-gutter)/0.3)] last:border-0">
          <!-- Recording header -->
          <div class="p-4">
            <div class="flex flex-col md:flex-row md:items-center md:justify-between gap-4">
              <div class="space-y-1">
                <div class="flex items-center space-x-2">
                  <h5 class="text-base font-semibold text-foreground truncate max-w-md" title={recording.dvrHash}>
                    {recording.internalName || recording.dvrHash}
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
                    onclick={() => playRecording(recording.dvrHash)}
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
                contentId={recording.dvrHash}
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
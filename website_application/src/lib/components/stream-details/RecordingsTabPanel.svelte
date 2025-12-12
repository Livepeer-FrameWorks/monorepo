<script lang="ts">
  import { getIconComponent } from "$lib/iconUtils";
  import { formatDate, formatDuration } from "$lib/utils/stream-helpers";
  import { Button } from "$lib/components/ui/button";

  let { recordings, onEnableRecording, onCopyLink, resolveUrl } = $props();

  const CopyIcon = getIconComponent("Copy");
  const DownloadIcon = getIconComponent("Download");
  const PlayIcon = getIconComponent("Play");
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
      {#each recordings as recording (recording.id ?? recording.asset?.id ?? recording.playbackId)}
        <div class="p-6 border-b border-[hsl(var(--tn-fg-gutter)/0.3)] last:border-0">
          <div
            class="flex flex-col md:flex-row md:items-center md:justify-between gap-4"
          >
            <div class="space-y-2">
              <div class="flex items-center space-x-2">
                <h5 class="text-lg font-semibold text-foreground">
                  {recording.asset?.name || "Recording"}
                </h5>
                <span
                  class="text-xs bg-info/20 text-info px-2 py-1 rounded-full font-medium"
                >
                  {recording.status || "Ready"}
                </span>
              </div>
              <p class="text-sm text-muted-foreground">
                Created: {formatDate(recording.createdAt)}
              </p>
              <p class="text-sm text-muted-foreground">
                Duration: {formatDuration(recording.asset?.duration || 0)}
              </p>
              <div class="flex items-center space-x-2">
                <code
                  class="px-3 py-1 bg-muted/20 text-xs font-mono text-foreground overflow-auto"
                >
                  {recording.asset?.downloadUrl || "No download available yet"}
                </code>
                <Button
                  variant="ghost"
                  size="sm"
                  onclick={() =>
                    onCopyLink(
                      recording.asset?.playbackId ||
                        recording.asset?.downloadUrl,
                    )}
                  disabled={!recording.asset?.playbackId &&
                    !recording.asset?.downloadUrl}
                  class="gap-2 border border-border/30"
                >
                  <CopyIcon class="w-4 h-4" />
                  Copy Link
                </Button>
              </div>
            </div>

            <div class="flex items-center space-x-2">
              {#if recording.asset?.downloadUrl}
                <Button
                  href={recording.asset.downloadUrl}
                  target="_blank"
                  rel="noopener noreferrer"
                  variant="ghost"
                  size="sm"
                  class="gap-2 border border-border/30"
                >
                  <DownloadIcon class="w-4 h-4" />
                  Download
                </Button>
              {/if}
              {#if recording.asset?.playbackId}
                <Button
                  href={resolveUrl(
                    `/view?type=recording&id=${recording.asset.playbackId}`,
                  )}
                  variant="ghost"
                  size="sm"
                  class="gap-2 border border-border/30"
                >
                  <PlayIcon class="w-4 h-4" />
                  Watch
                </Button>
              {/if}
            </div>
          </div>
        </div>
      {/each}
    </div>
  {/if}
</div>

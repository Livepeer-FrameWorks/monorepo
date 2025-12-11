<script lang="ts">
  import { getIconComponent } from "$lib/iconUtils";
  import { formatDate, formatDuration } from "$lib/utils/stream-helpers";
  import { Button } from "$lib/components/ui/button";

  let { recordings, onEnableRecording, onCopyLink, resolveUrl } = $props();

  const CopyIcon = getIconComponent("Copy");
  const DownloadIcon = getIconComponent("Download");
  const PlayIcon = getIconComponent("Play");
</script>

<div class="flex justify-between items-center mb-6">
  <div>
    <h4 class="text-lg font-semibold gradient-text">Recordings</h4>
    <p class="text-sm text-muted-foreground">
      View and manage DVR recordings for this stream.
    </p>
  </div>
</div>

{#if recordings.length === 0}
  <div
    class="border border-dashed border-border/50 p-8 text-center space-y-3"
  >
    <p class="text-foreground font-medium">No recordings available</p>
    <p class="text-sm text-muted-foreground">
      Recordings appear here when DVR is enabled and streams have been archived.
    </p>
    <Button
      onclick={onEnableRecording}
      class="gap-2 w-fit mx-auto"
    >
      Enable Recording
    </Button>
  </div>
{:else}
  <div class="space-y-4">
    {#each recordings as recording (recording.id ?? recording.asset?.id ?? recording.playbackId)}
      <div class="border border-border/50 p-6">
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
                class="px-3 py-1 border border-border/50 text-xs font-mono text-foreground overflow-auto"
              >
                {recording.asset?.downloadUrl || "No download available yet"}
              </code>
              <Button
                variant="outline"
                size="sm"
                onclick={() =>
                  onCopyLink(
                    recording.asset?.playbackId ||
                      recording.asset?.downloadUrl,
                  )}
                disabled={!recording.asset?.playbackId &&
                  !recording.asset?.downloadUrl}
                class="gap-2"
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
                variant="outline"
                size="sm"
                class="gap-2"
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
                variant="outline"
                size="sm"
                class="gap-2"
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

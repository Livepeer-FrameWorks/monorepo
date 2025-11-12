<script>
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
    <p class="text-sm text-tokyo-night-comment">
      View and manage DVR recordings for this stream.
    </p>
  </div>
</div>

{#if recordings.length === 0}
  <div
    class="bg-tokyo-night-bg-highlight border border-dashed border-tokyo-night-fg-gutter rounded-lg p-8 text-center space-y-3"
  >
    <p class="text-tokyo-night-fg font-medium">No recordings available</p>
    <p class="text-sm text-tokyo-night-comment">
      Recordings appear here when DVR is enabled and streams have been archived.
    </p>
    <Button
      onclick={onEnableRecording}
      class="gap-2 w-fit mx-auto hover:shadow-brand-soft"
    >
      Enable Recording
    </Button>
  </div>
{:else}
  <div class="space-y-4">
    {#each recordings as recording (recording.id ?? recording.asset?.id ?? recording.playbackId)}
      <div
        class="bg-tokyo-night-bg-highlight border border-tokyo-night-fg-gutter rounded-lg p-6 transition-all hover:border-tokyo-night-cyan/30 hover:shadow-brand-subtle"
      >
        <div
          class="flex flex-col md:flex-row md:items-center md:justify-between gap-4"
        >
          <div class="space-y-2">
            <div class="flex items-center space-x-2">
              <h5 class="text-lg font-semibold text-tokyo-night-fg">
                {recording.asset?.name || "Recording"}
              </h5>
              <span
                class="text-xs bg-tokyo-night-cyan/20 text-tokyo-night-cyan px-2 py-1 rounded-full font-medium"
              >
                {recording.status || "Ready"}
              </span>
            </div>
            <p class="text-sm text-tokyo-night-comment">
              Created: {formatDate(recording.createdAt)}
            </p>
            <p class="text-sm text-tokyo-night-comment">
              Duration: {formatDuration(recording.asset?.duration || 0)}
            </p>
            <div class="flex items-center space-x-2">
              <code
                class="px-3 py-1 bg-tokyo-night-bg-dark rounded text-xs font-mono text-tokyo-night-fg overflow-auto"
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
                class="gap-2 hover:shadow-brand-subtle"
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
                class="gap-2 hover:shadow-brand-subtle"
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
                class="gap-2 hover:shadow-brand-subtle"
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

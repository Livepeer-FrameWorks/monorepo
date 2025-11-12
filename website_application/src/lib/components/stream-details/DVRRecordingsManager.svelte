<script lang="ts">
  import { resolve } from "$app/paths";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { getIconComponent } from "$lib/iconUtils";
  import LoadingCard from "$lib/components/LoadingCard.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";

  interface Recording {
    id: string;
    dvrHash: string;
    createdAt?: string;
    status: string;
    durationSeconds?: number;
    sizeBytes?: number;
    storageNodeId?: string;
    manifestPath?: string;
    errorMessage?: string;
  }

  interface Props {
    isLive: boolean;
    streamRecordings: Recording[];
    loadingRecordings: boolean;
    startingDVR: boolean;
    stoppingDVR: boolean;
    copiedUrl: string;
    onStartRecording: () => void;
    onStopRecording: (dvrHash: string) => void;
    onCopy: (value: string) => void;
  }

  let {
    isLive,
    streamRecordings,
    loadingRecordings,
    startingDVR,
    stoppingDVR,
    copiedUrl,
    onStartRecording,
    onStopRecording,
    onCopy,
  }: Props = $props();

  const Loader2Icon = getIconComponent("Loader2");
  const VideoIcon = getIconComponent("Video");
  const SquareIcon = getIconComponent("Square");
  const CheckCircleIcon = getIconComponent("CheckCircle");
  const CopyIcon = getIconComponent("Copy");
  const PlayIcon = getIconComponent("Play");
</script>

<div class="space-y-6">
  <div class="flex items-center justify-between">
    <div>
      <h3 class="text-lg font-semibold text-tokyo-night-fg">DVR Recordings</h3>
      <p class="text-tokyo-night-fg-dark text-sm">
        Start recording and manage DVR sessions for this stream
      </p>
    </div>

    <div class="flex space-x-3">
      {#if isLive}
        <Button
          class="gap-2"
          onclick={onStartRecording}
          disabled={startingDVR}
        >
          {#if startingDVR}
            <Loader2Icon class="w-4 h-4 animate-spin" />
            Starting...
          {:else}
            <VideoIcon class="w-4 h-4" />
            Start Recording
          {/if}
        </Button>
      {:else}
        <div
          class="text-sm text-tokyo-night-comment px-4 py-2 bg-tokyo-night-bg-dark rounded-lg"
        >
          Stream must be live to start recording
        </div>
      {/if}
    </div>
  </div>

  {#if loadingRecordings}
    <div class="space-y-4">
      {#each Array.from({ length: 3 }) as _, index (index)}
        <LoadingCard variant="stream" />
      {/each}
    </div>
  {:else if streamRecordings.length === 0}
    <EmptyState
      iconName="Video"
      title="No Recordings Yet"
      description={isLive
        ? "Start your first DVR recording above"
        : "Start streaming, then create DVR recordings"}
      actionText={isLive ? "Start Recording" : ""}
      onAction={isLive ? onStartRecording : undefined}
    />
  {:else}
    <div class="space-y-4">
      {#each streamRecordings as recording (recording.id ?? recording.dvrHash)}
        <div
          class="bg-tokyo-night-bg-highlight p-4 rounded-lg border border-tokyo-night-fg-gutter"
        >
          <div class="flex items-center justify-between mb-3">
            <div class="flex items-center space-x-3">
              <VideoIcon class="w-5 h-5 text-tokyo-night-cyan" />
              <div>
                <h4 class="font-semibold text-tokyo-night-fg">
                  DVR Recording
                </h4>
                <p class="text-xs text-tokyo-night-comment">
                  {recording.createdAt
                    ? new Date(recording.createdAt).toLocaleString()
                    : "N/A"}
                </p>
              </div>
            </div>

            <div class="flex items-center space-x-2">
              <span
                class="px-2 py-1 text-xs rounded bg-tokyo-night-bg-dark {recording.status ===
                'completed'
                  ? 'text-tokyo-night-green'
                  : recording.status === 'recording'
                    ? 'text-tokyo-night-yellow'
                    : recording.status === 'failed'
                      ? 'text-tokyo-night-red'
                      : 'text-tokyo-night-fg-dark'}"
              >
                {recording.status === "recording"
                  ? "● Recording"
                  : recording.status}
              </span>

              {#if recording.status === "recording"}
                <Button
                  variant="destructive"
                  size="icon-sm"
                  onclick={() => onStopRecording(recording.dvrHash)}
                  disabled={stoppingDVR}
                >
                  {#if stoppingDVR}
                    <Loader2Icon class="w-4 h-4 animate-spin" />
                  {:else}
                    <SquareIcon class="w-4 h-4" />
                  {/if}
                </Button>
              {/if}
            </div>
          </div>

          <div class="grid grid-cols-2 md:grid-cols-4 gap-4 text-sm mb-3">
            <div>
              <p class="text-tokyo-night-comment">Duration</p>
              <p class="font-medium text-tokyo-night-fg">
                {recording.durationSeconds
                  ? Math.floor(recording.durationSeconds / 60) + "m"
                  : "N/A"}
              </p>
            </div>
            <div>
              <p class="text-tokyo-night-comment">Size</p>
              <p class="font-medium text-tokyo-night-fg">
                {recording.sizeBytes
                  ? (recording.sizeBytes / (1024 * 1024)).toFixed(1) + " MB"
                  : "N/A"}
              </p>
            </div>
            <div>
              <p class="text-tokyo-night-comment">Storage Node</p>
              <p class="font-medium text-tokyo-night-fg truncate">
                {recording.storageNodeId || "N/A"}
              </p>
            </div>
            <div>
              <p class="text-tokyo-night-comment">Format</p>
              <p class="font-medium text-tokyo-night-fg">HLS</p>
            </div>
          </div>

          {#if recording.manifestPath}
            <div class="flex items-center space-x-3">
              <Input
                type="text"
                value={recording.manifestPath}
                readonly
                class="flex-1 font-mono text-sm"
              />
              <Button
                variant="outline"
                size="sm"
                onclick={() => onCopy(recording.manifestPath || "")}
              >
                {#if copiedUrl === recording.manifestPath}
                  <CheckCircleIcon class="w-4 h-4" />
                {:else}
                  <CopyIcon class="w-4 h-4" />
                {/if}
              </Button>
              {#if recording.status === "completed"}
                <Button
                  href={resolve(
                    `/view?type=dvr&id=${recording.dvrHash || recording.id}`,
                  )}
                  class="gap-2"
                  title="Watch DVR recording"
                >
                  <PlayIcon class="w-4 h-4" />
                  Watch Recording
                </Button>
              {/if}
            </div>
          {:else}
            <div class="flex items-center space-x-3">
              <Input
                type="text"
                value={recording.dvrHash}
                readonly
                class="flex-1 font-mono text-sm"
              />
              <Button
                variant="outline"
                size="sm"
                onclick={() => onCopy(recording.dvrHash)}
              >
                {#if copiedUrl === recording.dvrHash}
                  <CheckCircleIcon class="w-4 h-4" />
                {:else}
                  <CopyIcon class="w-4 h-4" />
                {/if}
              </Button>
            </div>
          {/if}

          {#if recording.errorMessage}
            <p class="text-xs text-tokyo-night-red mt-2">
              Error: {recording.errorMessage}
            </p>
          {/if}
        </div>
      {/each}
    </div>
  {/if}
</div>

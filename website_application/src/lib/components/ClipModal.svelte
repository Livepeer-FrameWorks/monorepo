<script lang="ts">
  import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter } from "$lib/components/ui/dialog";
  import { Button } from "$lib/components/ui/button";
  import Player from "./Player.svelte";
  import { getIconComponent } from "$lib/iconUtils";

  interface Clip {
    id?: string;
    title?: string;
    description?: string;
    playbackId?: string;
    thumbnailUrl?: string;
    duration?: number;
    startTime?: number;
    endTime?: number;
    status?: string;
    createdAt?: string;
    streamName?: string;
  }

  interface Props {
    clip?: Clip | null;
    onClose?: () => void;
  }

  let { clip = null, onClose = () => {} }: Props = $props();

  const CloseIcon = getIconComponent("X");

  function formatDuration(totalSeconds: number | undefined) {
    if (!totalSeconds || totalSeconds < 0) return "0:00";
    const minutes = Math.floor(totalSeconds / 60);
    const seconds = Math.floor(totalSeconds % 60);
    return `${minutes}:${seconds.toString().padStart(2, "0")}`;
  }

  function close() {
    onClose?.();
  }

  const streamStatusColor = {
    Available: "bg-tokyo-night-green/20 text-tokyo-night-green",
    Processing: "bg-tokyo-night-yellow/20 text-tokyo-night-yellow",
    Failed: "bg-tokyo-night-red/20 text-tokyo-night-red",
  } as const;
</script>

{#if clip}
  <Dialog
    open={Boolean(clip)}
    onOpenChange={(value) => {
      if (!value) close();
    }}
  >
    <DialogContent class="max-w-5xl overflow-hidden p-0">
      <DialogHeader class="flex flex-row items-start justify-between gap-4 border-b border-border p-6">
        <div class="min-w-0 space-y-2 text-left">
          <DialogTitle class="truncate text-xl font-semibold text-foreground">
            {clip.title || "Clip Preview"}
          </DialogTitle>
          {#if clip.description}
            <DialogDescription class="line-clamp-2 text-sm text-foreground/70">
              {clip.description}
            </DialogDescription>
          {/if}
        </div>
        <Button variant="ghost" size="icon" onclick={close} aria-label="Close clip modal">
          <CloseIcon class="h-5 w-5" />
        </Button>
      </DialogHeader>

      <section class="bg-black">
        <Player
          contentId={clip.playbackId || clip.id}
          contentType="clip"
          thumbnailUrl={clip.thumbnailUrl}
          options={{
            autoplay: true,
            muted: false,
            controls: true,
            preferredProtocol: "auto",
            analytics: {
              enabled: true,
              sessionTracking: false,
            },
          }}
        />
      </section>

      <section class="grid gap-4 border-t border-border p-6 text-sm text-foreground">
        <div class="grid grid-cols-2 gap-4 md:grid-cols-4">
          <div>
            <p class="text-muted-foreground">Duration</p>
            <p class="font-medium">{formatDuration(clip.duration ?? (clip.endTime && clip.startTime ? clip.endTime - clip.startTime : undefined))}</p>
          </div>
          <div>
            <p class="text-muted-foreground">Start Time</p>
            <p class="font-medium">{formatDuration(clip.startTime)}</p>
          </div>
          <div>
            <p class="text-muted-foreground">Status</p>
            <p class={`font-medium inline-flex items-center rounded-full px-2 py-1 text-xs ${streamStatusColor[clip.status || "Available"] || "bg-primary/10 text-primary"}`}>
              {clip.status || "Available"}
            </p>
          </div>
          <div>
            <p class="text-muted-foreground">Created</p>
            <p class="font-medium">{clip.createdAt ? new Date(clip.createdAt).toLocaleString() : "N/A"}</p>
          </div>
        </div>

        {#if clip.streamName}
          <div class="rounded-lg border border-border p-4">
            <p class="text-sm text-foreground/80">
              From stream: <span class="font-medium text-foreground">{clip.streamName}</span>
            </p>
          </div>
        {/if}
      </section>

      <DialogFooter class="border-t border-border bg-muted/10 px-6 py-4">
        <Button variant="secondary" onclick={close}>
          Close
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
{/if}

<style>
  .line-clamp-2 {
    display: -webkit-box;
    -webkit-line-clamp: 2;
    -webkit-box-orient: vertical;
    overflow: hidden;
  }
</style>

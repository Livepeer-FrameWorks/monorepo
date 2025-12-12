<script lang="ts">
  import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter } from "$lib/components/ui/dialog";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import Player from "./Player.svelte";
  import { getIconComponent } from "$lib/iconUtils";
  import { getContentDeliveryUrls, type PrimaryProtocolUrls } from "$lib/config";
  import { toast } from "$lib/stores/toast";

  // Clip type matching Houdini schema
  interface ClipData {
    id: string;
    clipHash?: string | null;
    title?: string | null;
    description?: string | null;
    startTime?: number | null;
    endTime?: number | null;
    duration?: number | null;
    status?: string | null;
    playbackId?: string | null;
    manifestPath?: string | null;
    createdAt?: string | null;
    streamName?: string;
  }

  interface Props {
    clip?: ClipData | null;
    onClose?: () => void;
  }

  let { clip = null, onClose = () => {} }: Props = $props();

  let copiedField = $state<string | null>(null);
  let showUrls = $state(false);

  // Get delivery URLs for clip using clipHash
  let clipUrls = $derived(clip?.clipHash ? getContentDeliveryUrls(clip.clipHash, "clip") : null);

  // Primary protocols to show for clips
  const clipProtocols: Array<{
    key: keyof PrimaryProtocolUrls;
    name: string;
    icon: string;
  }> = [
    { key: "hls", name: "HLS", icon: "Play" },
    { key: "dash", name: "DASH", icon: "Film" },
    { key: "mp4", name: "MP4", icon: "FileVideo" },
    { key: "webm", name: "WebM", icon: "FileVideo" },
  ];

  async function copyToClipboard(text: string, field: string) {
    try {
      await navigator.clipboard.writeText(text);
      copiedField = field;
      toast.success("Copied to clipboard");
      setTimeout(() => {
        if (copiedField === field) copiedField = null;
      }, 2000);
    } catch {
      toast.error("Failed to copy");
    }
  }

  const CloseIcon = getIconComponent("X");
  const CopyIcon = getIconComponent("Copy");
  const CheckCircleIcon = getIconComponent("CheckCircle");
  const ChevronDownIcon = getIconComponent("ChevronDown");
  const ChevronUpIcon = getIconComponent("ChevronUp");
  const DownloadIcon = getIconComponent("Download");
  const LinkIcon = getIconComponent("Link");

  function formatDuration(totalSeconds: number | null | undefined) {
    if (totalSeconds == null || totalSeconds < 0) return "0:00";
    const minutes = Math.floor(totalSeconds / 60);
    const seconds = Math.floor(totalSeconds % 60);
    return `${minutes}:${seconds.toString().padStart(2, "0")}`;
  }

  function close() {
    onClose?.();
  }

  const streamStatusColor: Record<string, string> = {
    Available: "bg-success/20 text-success",
    Processing: "bg-warning/20 text-warning",
    Failed: "bg-destructive/20 text-destructive",
  };
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
          contentId={clip.id}
          contentType="clip"
          thumbnailUrl={undefined}
          options={{
            autoplay: true,
            muted: false,
            controls: true,
          }}
        />
      </section>

      <section class="grid gap-4 border-t border-border p-6 text-sm text-foreground">
        <div class="grid grid-cols-2 gap-4 md:grid-cols-4">
          <div>
            <p class="text-muted-foreground">Duration</p>
            <p class="font-medium">{formatDuration(clip.duration)}</p>
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
          <div class="border border-border p-4">
            <p class="text-sm text-foreground/80">
              From stream: <span class="font-medium text-foreground">{clip.streamName}</span>
            </p>
          </div>
        {/if}

        <!-- Playback URLs Section -->
        {#if clipUrls && clip.clipHash}
          <div class="border border-border">
            <button
              class="w-full p-3 flex items-center justify-between text-sm hover:bg-muted/10 transition-colors"
              onclick={() => showUrls = !showUrls}
            >
              <div class="flex items-center gap-2">
                <LinkIcon class="w-4 h-4 text-info" />
                <span class="font-medium">Direct Playback URLs</span>
              </div>
              {#if showUrls}
                <ChevronUpIcon class="w-4 h-4" />
              {:else}
                <ChevronDownIcon class="w-4 h-4" />
              {/if}
            </button>

            {#if showUrls}
              <div class="border-t border-border p-4 space-y-3 bg-muted/5">
                {#each clipProtocols as protocol}
                  {@const ProtocolIcon = getIconComponent(protocol.icon)}
                  {@const url = clipUrls.primary[protocol.key]}
                  <div class="flex items-center gap-2">
                    <div class="flex items-center gap-1.5 w-16 shrink-0">
                      <ProtocolIcon class="w-3 h-3 text-muted-foreground" />
                      <span class="text-xs font-medium text-muted-foreground">{protocol.name}</span>
                    </div>
                    <Input
                      type="text"
                      value={url || "N/A"}
                      readonly
                      class="flex-1 font-mono text-xs h-8"
                    />
                    <Button
                      variant="ghost"
                      size="sm"
                      onclick={() => copyToClipboard(url || "", `clip-${protocol.key}`)}
                      disabled={!url}
                      class="border border-border/30 h-8 w-8 p-0"
                    >
                      {#if copiedField === `clip-${protocol.key}`}
                        <CheckCircleIcon class="w-3 h-3" />
                      {:else}
                        <CopyIcon class="w-3 h-3" />
                      {/if}
                    </Button>
                  </div>
                {/each}
              </div>
            {/if}
          </div>
        {/if}
      </section>

      <DialogFooter class="border-t border-border bg-muted/10 px-6 py-4">
        <div class="flex items-center gap-2 w-full justify-between">
          {#if clipUrls?.primary.mp4}
            <Button
              href={clipUrls.primary.mp4}
              target="_blank"
              rel="noopener noreferrer"
              variant="outline"
              class="gap-2"
            >
              <DownloadIcon class="w-4 h-4" />
              Download MP4
            </Button>
          {:else}
            <div></div>
          {/if}
          <Button variant="secondary" onclick={close}>
            Close
          </Button>
        </div>
      </DialogFooter>
    </DialogContent>
  </Dialog>
{/if}

<style>
  .line-clamp-2 {
    display: -webkit-box;
    -webkit-line-clamp: 2;
    -webkit-box-orient: vertical;
    line-clamp: 2;
    overflow: hidden;
  }
</style>

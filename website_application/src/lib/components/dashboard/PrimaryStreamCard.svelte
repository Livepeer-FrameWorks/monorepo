<script lang="ts">
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { getIconComponent } from "$lib/iconUtils";
  import { resolve } from "$app/paths";
  import { StreamStatus } from "$houdini";

  // Define stream data interface matching Houdini types
  export interface PrimaryStreamData {
    id: string;
    streamId?: string;
    name: string;
    streamKey: string;
    metrics?: {
      status?: string | null;
      isLive?: boolean | null;
      currentViewers?: number | null;
    } | null;
    viewers?: number;
    resolution?: string;
  }

  interface Props {
    stream: PrimaryStreamData | null;
    onCopyStreamKey: (key: string) => void;
    createStreamUrl: string;
  }

  let { stream, onCopyStreamKey, createStreamUrl }: Props = $props();

  // Derive status from metrics edge
  const status = $derived(stream?.metrics?.status);
  const isLive = $derived(status === StreamStatus.LIVE);
  const displayStreamId = $derived(stream?.streamId || stream?.id || "");
</script>

{#if stream}
  {@const SettingsIcon = getIconComponent("Settings")}
  <div class="space-y-4">
    <div class="flex items-center justify-between mb-3">
      <div class="flex items-center gap-2">
        <h3 class="font-semibold text-foreground">
          {stream.name || `Stream ${displayStreamId.slice(0, 8)}`}
        </h3>
        <Button
          href={resolve(`/streams/${stream.id}`)}
          variant="ghost"
          size="icon"
          class="h-6 w-6 text-muted-foreground hover:text-foreground"
          title="Stream Settings"
        >
          <SettingsIcon class="w-3.5 h-3.5" />
        </Button>
      </div>
      <div class="flex items-center space-x-2">
        <div
          class="w-2 h-2 rounded-full {isLive
            ? 'bg-success animate-pulse'
            : 'bg-destructive'}"
        ></div>
        <span class="text-xs text-muted-foreground capitalize">
          {status?.toLowerCase() || "offline"}
        </span>
      </div>
    </div>

    <div class="grid grid-cols-2 gap-4 text-sm mb-4">
      <div>
        <p class="text-muted-foreground">Viewers</p>
        <p class="font-semibold text-foreground">
          {stream.viewers || 0}
        </p>
      </div>
      <div>
        <p class="text-muted-foreground">Resolution</p>
        <p class="font-semibold text-foreground">
          {stream.resolution || "Unknown"}
        </p>
      </div>
    </div>

    <!-- Stream Key -->
    <div>
      <label
        for="primary-stream-key"
        class="block text-sm font-medium text-muted-foreground mb-2"
        >Stream Key</label
      >
      <div class="flex items-center space-x-3">
        <Input
          id="primary-stream-key"
          type="text"
          value={stream.streamKey || "Loading..."}
          readonly
          class="flex-1 font-mono text-sm"
        />
        <Button
          variant="outline"
          onclick={() => onCopyStreamKey(stream.streamKey || "")}
          disabled={!stream.streamKey}
        >
          Copy
        </Button>
      </div>
      <p class="text-xs text-muted-foreground mt-2">
        Keep your stream key private. Anyone with this key can broadcast to your
        channel.
      </p>
    </div>
  </div>
{:else}
  {@const VideoIcon = getIconComponent("Video")}
  {@const PlusIcon = getIconComponent("Plus")}
  <div class="bg-[hsl(var(--tn-bg-dark)/0.3)] border border-[hsl(var(--tn-fg-gutter)/0.3)] p-6 space-y-4">
    <div class="text-center space-y-2">
      <div class="text-4xl">
        <VideoIcon class="w-10 h-10 text-muted-foreground mx-auto" />
      </div>
      <p class="text-muted-foreground">No streams found</p>
    </div>
    <Button href={createStreamUrl} variant="ghost" class="w-full">
      <PlusIcon class="w-4 h-4 mr-2" />
      Create Your First Stream
    </Button>
  </div>
{/if}

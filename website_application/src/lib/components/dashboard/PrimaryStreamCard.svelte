<script lang="ts">
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { getIconComponent } from "$lib/iconUtils";

  interface Stream {
    id: string;
    name?: string;
    status?: string;
    viewers?: number;
    resolution?: string;
    streamKey?: string;
  }

  interface Props {
    stream: Stream | null;
    onCopyStreamKey: (key: string) => void;
    createStreamUrl: string;
  }

  let { stream, onCopyStreamKey, createStreamUrl }: Props = $props();
</script>

{#if stream}
  <div
    class="bg-tokyo-night-bg-highlight p-4 rounded-lg border border-tokyo-night-fg-gutter"
  >
    <div class="flex items-center justify-between mb-3">
      <h3 class="font-semibold text-tokyo-night-fg">
        {stream.name || `Stream ${stream.id.slice(0, 8)}`}
      </h3>
      <div class="flex items-center space-x-2">
        <div
          class="w-2 h-2 rounded-full {stream.status === 'live'
            ? 'bg-tokyo-night-green animate-pulse'
            : 'bg-tokyo-night-red'}"
        ></div>
        <span class="text-xs text-tokyo-night-comment capitalize">
          {stream.status || "offline"}
        </span>
      </div>
    </div>

    <div class="grid grid-cols-2 gap-4 text-sm mb-4">
      <div>
        <p class="text-tokyo-night-comment">Viewers</p>
        <p class="font-semibold text-tokyo-night-fg">
          {stream.viewers || 0}
        </p>
      </div>
      <div>
        <p class="text-tokyo-night-comment">Resolution</p>
        <p class="font-semibold text-tokyo-night-fg">
          {stream.resolution || "Unknown"}
        </p>
      </div>
    </div>

    <!-- Stream Key -->
    <div>
      <label
        for="primary-stream-key"
        class="block text-sm font-medium text-tokyo-night-fg-dark mb-2"
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
      <p class="text-xs text-tokyo-night-comment mt-2">
        Keep your stream key private. Anyone with this key can broadcast to your
        channel.
      </p>
    </div>
  </div>
{:else}
  {@const VideoIcon = getIconComponent("Video")}
  {@const PlusIcon = getIconComponent("Plus")}
  <div class="text-center py-6">
    <div class="text-4xl mb-2">
      <VideoIcon class="w-10 h-10 text-tokyo-night-fg-dark mx-auto" />
    </div>
    <p class="text-tokyo-night-fg-dark mb-4">No streams found</p>
    <Button href={createStreamUrl}>
      <PlusIcon class="w-4 h-4 mr-2" />
      Create Your First Stream
    </Button>
  </div>
{/if}

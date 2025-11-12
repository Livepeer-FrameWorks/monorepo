<script lang="ts">
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { getIconComponent } from "$lib/iconUtils";

  interface Protocol {
    key: string;
    name: string;
    icon: string;
    description: string;
    recommended?: boolean;
    setup?: string;
  }

  interface Stream {
    id: string;
    streamKey?: string;
  }

  interface Props {
    selectedStream: Stream | null;
    ingestUrls: Record<string, string>;
    ingestProtocols: Protocol[];
    copiedUrl: string;
    refreshingKey: boolean;
    onCopy: (url: string) => void;
    onRefreshKey: (streamId: string) => void;
  }

  let {
    selectedStream,
    ingestUrls,
    ingestProtocols,
    copiedUrl,
    refreshingKey,
    onCopy,
    onRefreshKey,
  }: Props = $props();

  const RefreshCwIcon = getIconComponent("RefreshCw");
  const CheckCircleIcon = getIconComponent("CheckCircle");
  const CopyIcon = getIconComponent("Copy");
</script>

<div class="card">
  <div class="card-header">
    <h2 class="text-xl font-semibold text-tokyo-night-fg mb-2">
      Stream Ingest
    </h2>
    <p class="text-tokyo-night-fg-dark">
      Configure your streaming software to broadcast to these endpoints
    </p>
  </div>

  <div class="space-y-6">
    <!-- Stream Key Section -->
    <div
      class="bg-tokyo-night-bg-highlight p-4 rounded-lg border border-tokyo-night-fg-gutter"
    >
      <div class="flex items-center justify-between mb-3">
        <label
          for="stream-key-input"
          class="block text-sm font-medium text-tokyo-night-fg"
          >Stream Key</label
        >
        <Button
          variant="outline"
          size="sm"
          class="gap-2 text-xs"
          onclick={() => onRefreshKey(selectedStream?.id || "")}
          disabled={refreshingKey}
        >
          {#if refreshingKey}
            Refreshing...
          {:else}
            <RefreshCwIcon class="w-4 h-4" />
            Regenerate
          {/if}
        </Button>
      </div>
      <div class="flex items-center space-x-3">
        <Input
          id="stream-key-input"
          type="text"
          value={selectedStream?.streamKey || "Loading..."}
          readonly
          class="flex-1 font-mono text-sm"
        />
        <Button
          variant="outline"
          size="sm"
          onclick={() => onCopy(selectedStream?.streamKey || "")}
          disabled={!selectedStream?.streamKey}
        >
          {#if copiedUrl === selectedStream?.streamKey}
            <CheckCircleIcon class="w-4 h-4" />
          {:else}
            <CopyIcon class="w-4 h-4" />
          {/if}
        </Button>
      </div>
      <p class="text-xs text-tokyo-night-comment mt-2">
        Keep your stream key private. Anyone with this key can broadcast to your
        channel.
      </p>
    </div>

    <!-- Ingest Protocols -->
    {#each ingestProtocols as protocol (protocol.key ?? protocol.name)}
      {@const ProtocolIcon = getIconComponent(protocol.icon)}
      <div class="border border-tokyo-night-fg-gutter rounded-lg p-4">
        <div class="flex items-center justify-between mb-3">
          <div class="flex items-center space-x-3">
            <ProtocolIcon class="w-5 h-5 text-tokyo-night-cyan" />
            <div>
              <h3
                class="font-semibold text-tokyo-night-fg flex items-center space-x-2"
              >
                <span>{protocol.name}</span>
                {#if protocol.recommended}
                  <span class="badge-success text-xs">Recommended</span>
                {/if}
              </h3>
              <p class="text-xs text-tokyo-night-comment">
                {protocol.description}
              </p>
            </div>
          </div>
        </div>

        <div class="flex items-center space-x-3 mb-2">
          <Input
            type="text"
            value={ingestUrls[protocol.key] || "Stream key required"}
            readonly
            class="flex-1 font-mono text-sm"
          />
          <Button
            variant="outline"
            size="sm"
            onclick={() => onCopy(ingestUrls[protocol.key] || "")}
            disabled={!ingestUrls[protocol.key]}
          >
            {#if copiedUrl === ingestUrls[protocol.key]}
              <CheckCircleIcon class="w-4 h-4" />
            {:else}
              <CopyIcon class="w-4 h-4" />
            {/if}
          </Button>
        </div>

        <p class="text-xs text-tokyo-night-comment">
          {protocol.setup}
        </p>
      </div>
    {/each}
  </div>
</div>

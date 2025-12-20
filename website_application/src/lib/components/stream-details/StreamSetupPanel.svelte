<script lang="ts">
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Badge } from "$lib/components/ui/badge";
  import { getIconComponent } from "$lib/iconUtils";
  import { getIngestUrls } from "$lib/config";
  import { formatDate } from "$lib/utils/stream-helpers";
  import { toast } from "$lib/stores/toast";
  import EmptyState from "$lib/components/EmptyState.svelte";

  interface Stream {
    id: string;
    name: string;
    streamKey?: string | null;
    playbackId?: string | null;
  }

  interface StreamKey {
    id: string;
    keyName: string;
    keyValue: string;
    isActive: boolean;
    createdAt?: string;
    lastUsedAt?: string;
  }

  interface Props {
    stream: Stream;
    streamKeys?: StreamKey[];
    onRefreshKey?: () => void;
    refreshingKey?: boolean;
    onCreateKey?: () => void;
    onCopyKey?: (key: string) => void;
    onDeleteKey?: (keyId: string) => void;
    deleteLoading?: string | null;
  }

  let {
    stream,
    streamKeys = [],
    onRefreshKey,
    refreshingKey = false,
    onCreateKey,
    onCopyKey,
    onDeleteKey,
    deleteLoading = null,
  }: Props = $props();

  let copiedField = $state<string | null>(null);

  // Derive URLs from stream data using unified helper
  let ingestUrls = $derived(getIngestUrls(stream.streamKey || ""));

  // Protocol definitions for ingest
  const ingestProtocols = [
    {
      key: "rtmp",
      name: "RTMP",
      icon: "Radio",
      description: "Standard RTMP for OBS, Streamlabs, vMix",
      recommended: true,
      setup: "Server URL: Copy this into your streaming software's server field",
    },
    {
      key: "srt",
      name: "SRT",
      icon: "Zap",
      description: "Secure Reliable Transport - low latency",
      setup: "Use as caller mode with 200ms latency",
    },
    {
      key: "whip",
      name: "WHIP",
      icon: "Globe",
      description: "WebRTC HTTP Ingest Protocol - browser-based streaming",
      setup: "Use with StreamCrafter SDK: npm i @livepeer-frameworks/streamcrafter-react",
    },
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

  const RefreshCwIcon = getIconComponent("RefreshCw");
  const CheckCircleIcon = getIconComponent("CheckCircle");
  const CopyIcon = getIconComponent("Copy");
  const KeyIcon = getIconComponent("Key");
  const PlusIcon = getIconComponent("Plus");
  const LoaderIcon = getIconComponent("Loader");
  const TrashIcon = getIconComponent("Trash2");
</script>

<div class="dashboard-grid border-t border-[hsl(var(--tn-fg-gutter)/0.3)]">
  <!-- Ingest Section -->
  <div class="slab">
    <div class="slab-header">
      <h3 class="font-semibold text-xs uppercase tracking-wide text-muted-foreground">Ingest URLs</h3>
      <p class="text-xs text-muted-foreground/70 mt-1">
        Configure your streaming software to broadcast to these endpoints
      </p>
    </div>
    <div class="slab-body--flush">
      {#each ingestProtocols as protocol}
        {@const ProtocolIcon = getIconComponent(protocol.icon)}
        {@const url = ingestUrls[protocol.key as keyof typeof ingestUrls]}
        <div class="p-6 border-b border-[hsl(var(--tn-fg-gutter)/0.3)] last:border-0">
          <div class="flex items-center justify-between mb-2">
            <div class="flex items-center gap-2">
              <ProtocolIcon class="w-4 h-4 text-info" />
              <span class="font-medium text-foreground">{protocol.name}</span>
              {#if protocol.recommended}
                <span class="text-xs px-1.5 py-0.5 bg-success/20 text-success rounded-none">Recommended</span>
              {/if}
            </div>
          </div>
          <p class="text-xs text-muted-foreground mb-2">{protocol.description}</p>
          <div class="flex items-center gap-2">
            <Input
              type="text"
              value={url || "Stream key required"}
              readonly
              class="flex-1 font-mono text-xs"
            />
            <Button
              variant="ghost"
              size="sm"
              onclick={() => copyToClipboard(url || "", `ingest-${protocol.key}`)}
              disabled={!url}
              class="border border-border/30"
            >
              {#if copiedField === `ingest-${protocol.key}`}
                <CheckCircleIcon class="w-4 h-4" />
              {:else}
                <CopyIcon class="w-4 h-4" />
              {/if}
            </Button>
          </div>
          {#if protocol.setup}
            <p class="text-xs text-muted-foreground/70 mt-1">{protocol.setup}</p>
          {/if}
        </div>
      {/each}
    </div>
  </div>

  <!-- Stream Keys Management Section -->
  <div class="slab">
    <div class="slab-header flex items-center justify-between">
      <div>
        <h3 class="font-semibold text-xs uppercase tracking-wide text-muted-foreground">Stream Keys Management</h3>
        <p class="text-xs text-muted-foreground/70 mt-1">
          Manage multiple stream keys for key rotation and security
        </p>
      </div>
      {#if onCreateKey}
        <Button variant="ghost" class="gap-2 text-primary hover:text-primary/80" onclick={onCreateKey}>
          <PlusIcon class="w-4 h-4" />
          Create Key
        </Button>
      {/if}
    </div>

    {#if streamKeys.length > 0}
      <div class="slab-body--flush">
        {#each streamKeys as key (key.id ?? key.keyValue)}
          <div class="p-6 border-b border-[hsl(var(--tn-fg-gutter)/0.3)] last:border-0">
            <div class="flex items-center justify-between">
              <div class="flex-1">
                <div class="flex items-center space-x-3 mb-2">
                  <h5 class="font-medium text-foreground">
                    {key.keyName}
                  </h5>
                  <Badge
                    variant={key.isActive ? "default" : "secondary"}
                    tone={key.isActive ? "green" : "default"}
                  >
                    {key.isActive ? "Active" : "Inactive"}
                  </Badge>
                </div>

                <div class="flex items-center space-x-2 mb-2">
                  <code
                    class="flex-1 px-3 py-2 text-sm font-mono text-info bg-muted/20"
                  >
                    {key.keyValue}
                  </code>
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    class="hover:bg-muted/50"
                    onclick={() => onCopyKey?.(key.keyValue)}
                  >
                    <CopyIcon class="w-4 h-4" />
                  </Button>
                </div>

                <div class="text-sm text-muted-foreground">
                  Created: {formatDate(key.createdAt)}
                  {#if key.lastUsedAt}
                    • Last used: {formatDate(key.lastUsedAt)}
                  {/if}
                </div>
              </div>

              {#if onDeleteKey}
                <Button
                  variant="destructive"
                  size="icon-sm"
                  class="ml-4"
                  onclick={() => onDeleteKey(key.id)}
                  disabled={deleteLoading === key.id}
                >
                  {#if deleteLoading === key.id}
                    <LoaderIcon class="w-4 h-4 animate-spin" />
                  {:else}
                    <TrashIcon class="w-4 h-4" />
                  {/if}
                </Button>
              {/if}
            </div>
          </div>
        {/each}
      </div>
    {:else}
      <div class="slab-body--padded">
        <EmptyState
          iconName="Key"
          title="No Stream Keys"
          description="Create your first stream key to start broadcasting"
          actionText="Create Stream Key"
          onAction={onCreateKey}
        />
      </div>
    {/if}
  </div>
</div>

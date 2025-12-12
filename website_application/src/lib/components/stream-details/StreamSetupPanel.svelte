<script lang="ts">
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Textarea } from "$lib/components/ui/textarea";
  import { getIconComponent } from "$lib/iconUtils";
  import { getIngestUrls, getDeliveryUrls, getEmbedCode, getDocsSiteUrl } from "$lib/config";
  import { toast } from "$lib/stores/toast";

  interface Stream {
    id: string;
    name: string;
    streamKey?: string | null;
    playbackId?: string | null;
  }

  interface Props {
    stream: Stream;
    onRefreshKey?: () => void;
    refreshingKey?: boolean;
  }

  let { stream, onRefreshKey, refreshingKey = false }: Props = $props();

  let copiedField = $state<string | null>(null);

  // Derive URLs from stream data
  let ingestUrls = $derived(getIngestUrls(stream.streamKey || ""));
  let deliveryUrls = $derived(getDeliveryUrls(stream.playbackId || stream.name || ""));
  let embedCode = $derived(getEmbedCode(stream.playbackId || stream.name || ""));

  // Protocol definitions
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
      description: "WebRTC HTTP Ingest Protocol - browser-based",
      setup: "For browser-based WebRTC publishing",
    },
  ];

  const deliveryProtocols = [
    {
      key: "hls",
      name: "HLS",
      icon: "Play",
      description: "HTTP Live Streaming - widest compatibility",
      recommended: true,
    },
    {
      key: "webrtc",
      name: "WebRTC",
      icon: "Zap",
      description: "Ultra-low latency delivery",
    },
    {
      key: "embed",
      name: "React Player",
      icon: "Code",
      description: "Embed using the NPM package",
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
</script>

<div class="dashboard-grid border-t border-[hsl(var(--tn-fg-gutter)/0.3)]">
  <!-- Stream Key Section -->
  <div class="slab">
    <div class="slab-header">
      <div class="flex items-center gap-2">
        <KeyIcon class="w-4 h-4 text-warning" />
        <h3 class="font-semibold text-xs uppercase tracking-wide text-muted-foreground">Stream Key</h3>
      </div>
      <p class="text-xs text-muted-foreground/70 mt-1">
        Keep your stream key private. Anyone with this key can broadcast to your channel.
      </p>
    </div>
    <div class="slab-body--padded">
      <div class="flex items-center gap-3">
        <Input
          type="password"
          value={stream.streamKey || "No stream key"}
          readonly
          class="flex-1 font-mono text-sm"
        />
        <Button
          variant="ghost"
          size="sm"
          onclick={() => copyToClipboard(stream.streamKey || "", "streamKey")}
          disabled={!stream.streamKey}
          class="border border-border/30"
        >
          {#if copiedField === "streamKey"}
            <CheckCircleIcon class="w-4 h-4" />
          {:else}
            <CopyIcon class="w-4 h-4" />
          {/if}
        </Button>
        {#if onRefreshKey}
          <Button
            variant="ghost"
            size="sm"
            onclick={onRefreshKey}
            disabled={refreshingKey}
            class="border border-border/30"
          >
            <RefreshCwIcon class="w-4 h-4 {refreshingKey ? 'animate-spin' : ''}" />
          </Button>
        {/if}
      </div>
    </div>
  </div>

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

  <!-- Delivery Section -->
  <div class="slab">
    <div class="slab-header">
      <h3 class="font-semibold text-xs uppercase tracking-wide text-muted-foreground">Playback URLs</h3>
      <p class="text-xs text-muted-foreground/70 mt-1">
        Share these URLs with viewers to watch your stream
      </p>
    </div>
    <div class="slab-body--flush">
      {#each deliveryProtocols as protocol}
        {@const ProtocolIcon = getIconComponent(protocol.icon)}
        {@const url = protocol.key === "embed" ? embedCode : deliveryUrls[protocol.key as keyof typeof deliveryUrls]}
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

          {#if protocol.key === "embed"}
            <Textarea
              readonly
              value={url || "Playback ID required"}
              class="font-mono text-xs h-32 resize-none"
            />
            <div class="mt-2 flex gap-2">
              <Button
                variant="ghost"
                size="sm"
                class="flex-1 border border-border/30"
                onclick={() => copyToClipboard(url || "", "embed")}
                disabled={!url}
              >
                {#if copiedField === "embed"}
                  <CheckCircleIcon class="w-4 h-4 mr-2" />
                  Copied!
                {:else}
                  <CopyIcon class="w-4 h-4 mr-2" />
                  Copy Code
                {/if}
              </Button>
              <Button
                variant="ghost"
                size="sm"
                class="border border-border/30"
                onclick={() => window.open(`${getDocsSiteUrl()}/streamers/playback`, '_blank')}
              >
                View Docs
              </Button>
            </div>
          {:else}
            <div class="flex items-center gap-2">
              <Input
                type="text"
                value={url || "Playback ID required"}
                readonly
                class="flex-1 font-mono text-xs"
              />
              <Button
                variant="ghost"
                size="sm"
                onclick={() => copyToClipboard(url || "", `delivery-${protocol.key}`)}
                disabled={!url}
                class="border border-border/30"
              >
                {#if copiedField === `delivery-${protocol.key}`}
                  <CheckCircleIcon class="w-4 h-4" />
                {:else}
                  <CopyIcon class="w-4 h-4" />
                {/if}
              </Button>
            </div>
          {/if}
        </div>
      {/each}
    </div>
  </div>
</div>

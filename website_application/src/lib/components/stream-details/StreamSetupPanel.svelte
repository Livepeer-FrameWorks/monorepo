<script lang="ts">
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Textarea } from "$lib/components/ui/textarea";
  import { getIconComponent } from "$lib/iconUtils";
  import { getIngestUrls, getContentDeliveryUrls, getDocsSiteUrl, type PrimaryProtocolUrls, type AdditionalProtocolUrls } from "$lib/config";
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
  let showAdditionalProtocols = $state(false);

  // Derive URLs from stream data using unified helper
  let ingestUrls = $derived(getIngestUrls(stream.streamKey || ""));
  let contentUrls = $derived(getContentDeliveryUrls(stream.playbackId || stream.name || "", "live"));

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
      description: "WebRTC HTTP Ingest Protocol - browser-based",
      setup: "For browser-based WebRTC publishing",
    },
  ];

  // Primary delivery protocols (shown by default)
  const primaryDeliveryProtocols: Array<{
    key: keyof PrimaryProtocolUrls;
    name: string;
    icon: string;
    description: string;
    recommended?: boolean;
  }> = [
    { key: "hls", name: "HLS", icon: "Play", description: "HTTP Live Streaming - best compatibility", recommended: true },
    { key: "hlsCmaf", name: "HLS (CMAF)", icon: "Play", description: "Lower latency HLS variant" },
    { key: "dash", name: "DASH", icon: "Film", description: "MPEG-DASH adaptive streaming" },
    { key: "webrtc", name: "WebRTC", icon: "Zap", description: "Ultra-low latency (~0.5s)" },
    { key: "mp4", name: "MP4", icon: "FileVideo", description: "Progressive download" },
    { key: "webm", name: "WebM", icon: "FileVideo", description: "Open format (VP8/VP9)" },
    { key: "srt", name: "SRT", icon: "Radio", description: "Secure Reliable Transport" },
  ];

  // Additional delivery protocols (expandable)
  const additionalDeliveryProtocols: Array<{
    key: keyof AdditionalProtocolUrls;
    name: string;
    icon: string;
    description: string;
  }> = [
    { key: "rtsp", name: "RTSP", icon: "Monitor", description: "IP cameras, VLC, ffmpeg" },
    { key: "rtmp", name: "RTMP", icon: "Radio", description: "Legacy Flash/OBS playback" },
    { key: "ts", name: "MPEG-TS", icon: "FileVideo", description: "Transport stream, DVB compatible" },
    { key: "mkv", name: "MKV", icon: "FileVideo", description: "Matroska container" },
    { key: "flv", name: "FLV", icon: "FileVideo", description: "Flash Video (legacy)" },
    { key: "aac", name: "AAC", icon: "Music", description: "Audio-only stream" },
    { key: "smoothStreaming", name: "Smooth Streaming", icon: "Film", description: "Microsoft format" },
    { key: "hds", name: "HDS", icon: "Film", description: "Adobe HTTP Dynamic Streaming" },
    { key: "sdp", name: "SDP", icon: "FileText", description: "Session Description Protocol" },
    { key: "rawH264", name: "Raw H264", icon: "FileVideo", description: "Elementary video stream" },
    { key: "wsmp4", name: "WS/MP4", icon: "FileVideo", description: "MP4 over WebSocket" },
    { key: "wsWebrtc", name: "WS/WebRTC", icon: "Zap", description: "WebRTC over WebSocket" },
    { key: "dtsc", name: "DTSC", icon: "Server", description: "MistServer internal" },
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
  const ChevronDownIcon = getIconComponent("ChevronDown");
  const ChevronUpIcon = getIconComponent("ChevronUp");
  const ExternalLinkIcon = getIconComponent("ExternalLink");
  const CodeIcon = getIconComponent("Code");
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
        Share these URLs with viewers to watch your stream.
        <a
          href={`${getDocsSiteUrl()}/streamers/playback`}
          target="_blank"
          rel="noopener noreferrer"
          class="text-info hover:underline inline-flex items-center gap-1"
        >
          Protocol docs <ExternalLinkIcon class="w-3 h-3" />
        </a>
      </p>
    </div>
    <div class="slab-body--flush">
      <!-- Primary protocols -->
      {#each primaryDeliveryProtocols as protocol}
        {@const ProtocolIcon = getIconComponent(protocol.icon)}
        {@const url = contentUrls.primary[protocol.key]}
        <div class="p-4 border-b border-[hsl(var(--tn-fg-gutter)/0.3)]">
          <div class="flex items-center justify-between mb-2">
            <div class="flex items-center gap-2">
              <ProtocolIcon class="w-4 h-4 text-info" />
              <span class="font-medium text-foreground text-sm">{protocol.name}</span>
              {#if protocol.recommended}
                <span class="text-xs px-1.5 py-0.5 bg-success/20 text-success rounded-none">Recommended</span>
              {/if}
            </div>
          </div>
          <p class="text-xs text-muted-foreground mb-2">{protocol.description}</p>
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
              onclick={() => copyToClipboard(url || "", `primary-${protocol.key}`)}
              disabled={!url}
              class="border border-border/30"
            >
              {#if copiedField === `primary-${protocol.key}`}
                <CheckCircleIcon class="w-4 h-4" />
              {:else}
                <CopyIcon class="w-4 h-4" />
              {/if}
            </Button>
          </div>
        </div>
      {/each}

      <!-- Embed code -->
      <div class="p-4 border-b border-[hsl(var(--tn-fg-gutter)/0.3)]">
        <div class="flex items-center justify-between mb-2">
          <div class="flex items-center gap-2">
            <CodeIcon class="w-4 h-4 text-info" />
            <span class="font-medium text-foreground text-sm">React Player</span>
          </div>
        </div>
        <p class="text-xs text-muted-foreground mb-2">Embed using the NPM package</p>
        <Textarea
          readonly
          value={contentUrls.embed || "Playback ID required"}
          class="font-mono text-xs h-32 resize-none"
        />
        <div class="mt-2 flex gap-2">
          <Button
            variant="ghost"
            size="sm"
            class="flex-1 border border-border/30"
            onclick={() => copyToClipboard(contentUrls.embed || "", "embed")}
            disabled={!contentUrls.embed}
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
      </div>

      <!-- Additional protocols toggle -->
      <button
        class="w-full p-3 flex items-center justify-between text-sm text-muted-foreground hover:bg-muted/10 transition-colors"
        onclick={() => showAdditionalProtocols = !showAdditionalProtocols}
      >
        <span>{showAdditionalProtocols ? "Hide" : "Show"} additional protocols ({additionalDeliveryProtocols.length})</span>
        {#if showAdditionalProtocols}
          <ChevronUpIcon class="w-4 h-4" />
        {:else}
          <ChevronDownIcon class="w-4 h-4" />
        {/if}
      </button>

      <!-- Additional protocols (collapsible) -->
      {#if showAdditionalProtocols}
        <div class="border-t border-[hsl(var(--tn-fg-gutter)/0.3)] bg-muted/5">
          {#each additionalDeliveryProtocols as protocol}
            {@const ProtocolIcon = getIconComponent(protocol.icon)}
            {@const url = contentUrls.additional[protocol.key]}
            <div class="p-4 border-b border-[hsl(var(--tn-fg-gutter)/0.2)] last:border-0">
              <div class="flex items-center justify-between mb-1">
                <div class="flex items-center gap-2">
                  <ProtocolIcon class="w-3 h-3 text-muted-foreground" />
                  <span class="font-medium text-foreground text-sm">{protocol.name}</span>
                </div>
              </div>
              <p class="text-xs text-muted-foreground mb-2">{protocol.description}</p>
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
                  onclick={() => copyToClipboard(url || "", `additional-${protocol.key}`)}
                  disabled={!url}
                  class="border border-border/30"
                >
                  {#if copiedField === `additional-${protocol.key}`}
                    <CheckCircleIcon class="w-4 h-4" />
                  {:else}
                    <CopyIcon class="w-4 h-4" />
                  {/if}
                </Button>
              </div>
            </div>
          {/each}
        </div>
      {/if}
    </div>
  </div>
</div>

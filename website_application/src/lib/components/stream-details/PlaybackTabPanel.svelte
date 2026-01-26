<script lang="ts">
  import { getContentDeliveryUrls, getDocsSiteUrl } from "$lib/config";
  import { Button } from "$lib/components/ui/button";
  import { Textarea } from "$lib/components/ui/textarea";
  import { toast } from "$lib/stores/toast";
  import { getIconComponent } from "$lib/iconUtils";
  import PlaybackProtocols from "$lib/components/PlaybackProtocols.svelte";

  interface Props {
    playbackId: string | null | undefined;
  }

  let { playbackId }: Props = $props();

  // Get all URLs for this stream
  let urls = $derived(playbackId ? getContentDeliveryUrls(playbackId, "live") : null);

  let copiedField = $state<string | null>(null);

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

  const CodeIcon = getIconComponent("Code");
  const CopyIcon = getIconComponent("Copy");
  const CheckCircleIcon = getIconComponent("CheckCircle");
  const ExternalLinkIcon = getIconComponent("ExternalLink");
</script>

<div class="slab border-t border-[hsl(var(--tn-fg-gutter)/0.3)]">
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

  {#if !playbackId}
    <div class="slab-body--padded">
      <div class="bg-[hsl(var(--tn-bg-dark)/0.3)] border border-[hsl(var(--tn-fg-gutter)/0.3)] p-6 text-center">
        <p class="text-muted-foreground">
          Playback ID not available. Stream may still be initializing.
        </p>
      </div>
    </div>
  {:else}
    <div class="slab-body--padded space-y-6">
      <!-- All Protocol URLs -->
      <div>
        <h4 class="text-sm font-medium text-foreground mb-3">Protocol URLs</h4>
        <PlaybackProtocols
          contentId={playbackId}
          contentType="live"
          showPrimary={true}
          showAdditional={true}
          docsUrl={`${getDocsSiteUrl()}/streamers/playback`}
        />
      </div>

      <!-- Embed Code Section -->
      {#if urls?.embed}
        <div class="border-t border-[hsl(var(--tn-fg-gutter)/0.3)] pt-6">
          <div class="flex items-center gap-2 mb-3">
            <CodeIcon class="w-4 h-4 text-info" />
            <h4 class="text-sm font-medium text-foreground">React Player Component</h4>
          </div>
          <p class="text-xs text-muted-foreground mb-3">
            Embed using the <code class="bg-muted px-1 rounded">@livepeer-frameworks/player-react</code> NPM package
          </p>
          <Textarea
            readonly
            value={urls.embed}
            class="font-mono text-xs h-40 resize-none"
          />
          <div class="mt-3 flex gap-2">
            <Button
              variant="ghost"
              size="sm"
              class="flex-1 border border-border/30"
              onclick={() => copyToClipboard(urls?.embed || "", "embed")}
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
              <ExternalLinkIcon class="w-4 h-4 mr-2" />
              Player Docs
            </Button>
          </div>
        </div>
      {/if}
    </div>
  {/if}
</div>

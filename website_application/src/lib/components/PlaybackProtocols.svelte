<script lang="ts">
  import { resolve } from "$app/paths";
  import { getContentDeliveryUrls, PROTOCOL_INFO, type ContentType } from "$lib/config";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { toast } from "$lib/stores/toast";
  import { getIconComponent } from "$lib/iconUtils";

  interface Props {
    contentId: string;
    contentType: ContentType;
    showPrimary?: boolean;
    showAdditional?: boolean;
    docsUrl?: string;
  }

  let {
    contentId,
    contentType,
    showPrimary = true,
    showAdditional = true,
    docsUrl = "/docs/playback"
  }: Props = $props();

  // Get all URLs for this content
  let urls = $derived(contentId ? getContentDeliveryUrls(contentId, contentType) : null);

  let showAdvanced = $state(false);

  // Filter protocols based on props and toggle state
  let primaryProtocols = $derived(
    PROTOCOL_INFO.filter((p) => {
      if (p.category !== "primary") return false;
      if (!showPrimary) return false;
      if (p.key === "play") return false;
      return true;
    })
  );

  let additionalProtocols = $derived(
    PROTOCOL_INFO.filter((p) => {
      if (p.category !== "additional") return false;
      if (!showAdditional) return false;
      if (p.key === "play") return false;
      return true;
    })
  );

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

  function getUrl(key: string): string {
    if (!urls) return "";
    // Check primary first, then additional
    const primaryKey = key as keyof typeof urls.primary;
    const additionalKey = key as keyof typeof urls.additional;
    return urls.primary[primaryKey] || urls.additional[additionalKey] || "";
  }

  const CopyIcon = getIconComponent("Copy");
  const CheckCircleIcon = getIconComponent("CheckCircle");
  const ExternalLinkIcon = getIconComponent("ExternalLink");
  const ChevronDownIcon = getIconComponent("ChevronDown");
  const ChevronUpIcon = getIconComponent("ChevronUp");
</script>

{#if urls}
  <div class="space-y-3">
    <div class="flex items-center justify-between">
      <p class="text-xs text-muted-foreground">
        Playback URLs for this {contentType === "dvr" ? "recording" : contentType}.
      </p>
      <a
        href={resolve(docsUrl)}
        target="_blank"
        rel="noopener noreferrer"
        class="text-xs text-info hover:underline inline-flex items-center gap-1"
      >
        Protocol docs <ExternalLinkIcon class="w-3 h-3" />
      </a>
    </div>

    <!-- Primary Protocols -->
    <div class="space-y-2">
      {#each primaryProtocols as protocol (protocol.key)}
        {@const url = getUrl(protocol.key)}
        {#if url}
          <div class="flex items-center gap-2">
            <div class="flex flex-col w-24 shrink-0">
              <span class="text-xs font-medium text-foreground">{protocol.label}</span>
              <span class="text-[10px] text-muted-foreground/70 truncate" title={protocol.description}>
                {protocol.description}
              </span>
            </div>
            <Input
              type="text"
              value={url}
              readonly
              class="flex-1 font-mono text-xs h-8 bg-muted/30"
            />
            <Button
              variant="ghost"
              size="sm"
              onclick={() => copyToClipboard(url, `${protocol.key}-${contentId}`)}
              class="border border-border/30 h-8 w-8 p-0"
              title="Copy URL"
            >
              {#if copiedField === `${protocol.key}-${contentId}`}
                <CheckCircleIcon class="w-3.5 h-3.5 text-success" />
              {:else}
                <CopyIcon class="w-3.5 h-3.5" />
              {/if}
            </Button>
          </div>
        {/if}
      {/each}
    </div>

    <!-- Advanced Protocols Toggle -->
    {#if additionalProtocols.length > 0 && showAdditional}
      <div class="pt-1">
        {#if showAdvanced}
          <div class="space-y-2 mb-2 pt-2 border-t border-border/40 animate-in slide-in-from-top-2 duration-200">
            {#each additionalProtocols as protocol (protocol.key)}
              {@const url = getUrl(protocol.key)}
              {#if url}
                <div class="flex items-center gap-2">
                  <div class="flex flex-col w-24 shrink-0">
                    <span class="text-xs font-medium text-muted-foreground">{protocol.label}</span>
                  </div>
                  <Input
                    type="text"
                    value={url}
                    readonly
                    class="flex-1 font-mono text-xs h-8 bg-muted/10 text-muted-foreground"
                  />
                  <Button
                    variant="ghost"
                    size="sm"
                    onclick={() => copyToClipboard(url, `${protocol.key}-${contentId}`)}
                    class="border border-border/30 h-8 w-8 p-0 text-muted-foreground"
                    title="Copy URL"
                  >
                    {#if copiedField === `${protocol.key}-${contentId}`}
                      <CheckCircleIcon class="w-3.5 h-3.5 text-success" />
                    {:else}
                      <CopyIcon class="w-3.5 h-3.5" />
                    {/if}
                  </Button>
                </div>
              {/if}
            {/each}
          </div>
        {/if}

        <Button
          variant="ghost"
          size="sm"
          class="w-full h-6 text-xs text-muted-foreground hover:text-foreground gap-1"
          onclick={() => showAdvanced = !showAdvanced}
        >
          {#if showAdvanced}
            Hide Advanced Protocols <ChevronUpIcon class="w-3 h-3" />
          {:else}
            Show Advanced Protocols <ChevronDownIcon class="w-3 h-3" />
          {/if}
        </Button>
      </div>
    {/if}
  </div>
{/if}

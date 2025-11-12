<script lang="ts">
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Textarea } from "$lib/components/ui/textarea";
  import { getIconComponent } from "$lib/iconUtils";

  interface Protocol {
    key: string;
    name: string;
    icon: string;
    description: string;
    recommended?: boolean;
  }

  interface Props {
    deliveryUrls: Record<string, string>;
    deliveryProtocols: Protocol[];
    copiedUrl: string;
    onCopy: (url: string) => void;
  }

  let { deliveryUrls, deliveryProtocols, copiedUrl, onCopy }: Props = $props();

  const CheckCircleIcon = getIconComponent("CheckCircle");
  const CopyIcon = getIconComponent("Copy");
</script>

<div class="card">
  <div class="card-header">
    <h2 class="text-xl font-semibold text-tokyo-night-fg mb-2">
      Stream Delivery
    </h2>
    <p class="text-tokyo-night-fg-dark">
      Multiple playback options for viewers and applications
    </p>
  </div>

  <div class="space-y-4">
    {#each deliveryProtocols as protocol (protocol.key ?? protocol.name)}
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

        {#if protocol.key === "embed"}
          <!-- Special handling for embed code -->
          <div class="space-y-2">
            <Textarea
              readonly
              value={`<iframe src="${
                deliveryUrls[protocol.key] || ""
              }" frameborder="0" allowfullscreen></iframe>`}
              class="font-mono text-sm h-20"
            />
            <Button
              variant="outline"
              class="w-full justify-center"
              onclick={() =>
                onCopy(
                  `<iframe src="${
                    deliveryUrls[protocol.key] || ""
                  }" frameborder="0" allowfullscreen></iframe>`,
                )}
              disabled={!deliveryUrls[protocol.key]}
            >
              {copiedUrl.includes(deliveryUrls[protocol.key])
                ? "✓ Copied"
                : "Copy Embed Code"}
            </Button>
          </div>
        {:else}
          <!-- Regular URL display -->
          <div class="flex items-center space-x-3">
            <Input
              type="text"
              value={deliveryUrls[protocol.key] || "Playback ID required"}
              readonly
              class="flex-1 font-mono text-sm"
            />
            <Button
              variant="outline"
              size="sm"
              onclick={() => onCopy(deliveryUrls[protocol.key] || "")}
              disabled={!deliveryUrls[protocol.key]}
            >
              {#if copiedUrl === deliveryUrls[protocol.key]}
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

<script lang="ts">
  import { createEventDispatcher } from "svelte";
  import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription } from "$lib/components/ui/dialog";
  import { Badge } from "$lib/components/ui/badge";
  import { Button } from "$lib/components/ui/button";
  import { getIconComponent } from "../iconUtils";

  interface Props {
    show?: boolean;
    item?: {
      name: string;
      description?: string;
      icon?: string;
    } | null;
  }

  let { show = $bindable(false), item = null }: Props = $props();

  const dispatch = createEventDispatcher();

  function close() {
    if (show) {
      show = false;
      dispatch("close");
    }
  }

  $effect(() => {
    if (!show) {
      dispatch("close");
    }
  });
</script>

{#if item}
  {@const Icon = getIconComponent(item.icon)}
  <Dialog
    open={show}
    onOpenChange={(value) => {
      if (!value) {
        close();
      } else {
        show = value;
      }
    }}
  >
    <DialogContent class="max-w-xl">
      <DialogHeader class="flex flex-row items-start gap-3 text-left">
        <div class="flex h-12 w-12 items-center justify-center rounded-lg bg-tokyo-night-bg-light">
          <Icon class="h-6 w-6 text-primary" />
        </div>
        <div class="space-y-1">
          <DialogTitle class="text-xl font-semibold text-foreground">
            {item.name}
          </DialogTitle>
          <Badge variant="outline" class="uppercase tracking-wide text-xs">
            Coming Soon
          </Badge>
        </div>
      </DialogHeader>

      <DialogDescription class="text-sm text-foreground/80">
        {item.description || "This feature is planned for a future release."}
      </DialogDescription>

      <section class="mt-4 space-y-4 text-sm text-foreground">
        <div class="rounded-lg border border-warning/40 bg-warning/10 p-4">
          <h3 class="mb-2 flex items-center gap-2 text-sm font-medium text-warning">
            <svg
              class="h-5 w-5"
              fill="none"
              stroke="currentColor"
              viewBox="0 0 24 24"
            >
              <path
                stroke-linecap="round"
                stroke-linejoin="round"
                stroke-width="2"
                d="M12 8v4m0 4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z"
              />
            </svg>
            In Development
          </h3>
          <p>
            This feature is part of our public roadmap. Follow our docs for the
            latest implementation updates and timelines.
          </p>
        </div>

        <div class="space-y-2">
          <h3 class="text-sm font-semibold text-foreground">What to expect:</h3>
          <ul class="space-y-2 text-foreground/80">
            {#if item.name === "Stream Settings"}
              <li>Configure transcoding, recording, and stream options.</li>
              <li>Set stream access controls and metadata.</li>
              <li>Customize stream thumbnails and descriptions.</li>
            {:else if item.name === "Stream Composer"}
              <li>Compose multiple input streams with picture-in-picture layouts.</li>
              <li>Visual editor for stream overlays and scenes.</li>
              <li>Deliver a unified output stream from multiple sources.</li>
            {:else if item.name === "Browser Streaming"}
              <li>WebRTC infrastructure is ready and configured.</li>
              <li>Final browser integration and UI polish underway.</li>
              <li>One-click "Go Live" experience directly in the dashboard.</li>
            {:else if item.name === "Recordings"}
              <li>Automatic live stream recording and archival.</li>
              <li>Integration with storage node routing and metering.</li>
              <li>Leverages the existing MistServer recording pipeline.</li>
            {:else if item.name === "Reports"}
              <li>Generate detailed analytics and billing reports.</li>
              <li>Export usage data for external BI tooling.</li>
              <li>Schedule recurring delivery to your teams.</li>
            {:else}
              <li>Additional details for this feature will be announced soon.</li>
            {/if}
          </ul>
        </div>
      </section>

      <div class="mt-6 flex items-center justify-end gap-2">
        <Button variant="secondary" onclick={close}>
          Close
        </Button>
        <Button
          href="https://docs.frameworks.live"
          target="_blank"
          rel="noreferrer"
        >
          View Roadmap
        </Button>
      </div>
    </DialogContent>
  </Dialog>
{/if}

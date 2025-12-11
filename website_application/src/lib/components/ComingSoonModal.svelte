<script lang="ts">
  import { createEventDispatcher } from "svelte";
  import {
    Dialog,
    DialogContent,
    DialogHeader,
    DialogTitle,
    DialogDescription,
  } from "$lib/components/ui/dialog";
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
  {@const Icon = getIconComponent(item.icon || "HelpCircle")}
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
        <div
          class="flex h-12 w-12 items-center justify-center bg-card"
        >
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
        <div class="border border-warning/40 bg-warning/10 p-4">
          <h3
            class="mb-2 flex items-center gap-2 text-sm font-medium text-warning"
          >
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
            {#if item.name === "Browser Streaming"}
              <li>
                One-click "Go Live" experience, directly in the dashboard.
              </li>
              <li>Compose tracks from any stream or connected device.</li>
            {:else if item.name === "Stream Settings"}
              <li>Configure transcoding, recording, and stream options.</li>
              <li>Set stream access controls and metadata.</li>
              <li>Customize stream thumbnails and descriptions.</li>
            {:else if item.name === "Stream Composer"}
              <li>
                Compose multiple input streams with picture-in-picture layouts.
              </li>
              <li>Visual editor for stream overlays and scenes.</li>
              <li>
                Compose on our processing nodes or on your own media node.
              </li>
            {:else if item.name === "Device Discovery"}
              <li>Manage your AV devices and their settings.</li>
              <li>Select tracks to make available for stream composition.</li>
              <li>View device status and availability.</li>
            {:else if item.name === "Network Status"}
              <li>View publicly exposed cluster metrics.</li>
              <li>Live status of platform and services.</li>
            {:else if item.name === "AI Processing"}
              <li>Configure AI-powered computer vision and analysis.</li>
              <li>View AI processing history and results.</li>
              <li>Export detailed AI processing results and metadata.</li>
            {:else if item.name === "Live Transcription"}
              <li>Configure transcription models and languages.</li>
              <li>View transcription history and results.</li>
              <li>Export detailed transcripts and metadata.</li>
            {:else if item.name === "Team Members"}
              <li>Connect with your team members.</li>
              <li>Invite new team members to your account.</li>
              <li>Remove team members from your account.</li>
            {:else if item.name === "Permissions"}
              <li>Manage permissions for your account.</li>
              <li>Assign roles and permissions to your team members.</li>
            {:else if item.name === "Team Activity"}
              <li>View activity logs for your team.</li>
              <li>Rollback to previous configurations.</li>
            {:else if item.name === "Webhooks"}
              <li>Configure integrations with external services.</li>
              <li>Receive real-time notifications from the platform.</li>
              <li>
                Automate & customize your media pipeline by plugging in your own
                logic.
              </li>
            {:else if item.name === "SDKs & Libraries"}
              <li>Ready-to-use SDKs and libraries for your projects.</li>
              <li>Documentation and examples for each SDK.</li>
              <li>Support for multiple programming languages.</li>
            {:else if item.name === "Support Tickets"}
              <li>Talk directly to our support team.</li>
              <li>Map tickets to streams and users.</li>
            {:else}
              <li>
                Additional details for this feature will be announced soon.
              </li>
            {/if}
          </ul>
        </div>
      </section>

      <div class="mt-6 flex items-center justify-end gap-2">
        <Button variant="secondary" onclick={close}>Close</Button>
        <Button
          href="https://docs.frameworks.network"
          target="_blank"
          rel="noreferrer"
        >
          View Roadmap
        </Button>
      </div>
    </DialogContent>
  </Dialog>
{/if}

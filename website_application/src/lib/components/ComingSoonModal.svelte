<script lang="ts">
  import { createEventDispatcher } from "svelte";
  import {
    Dialog,
    DialogContent,
    DialogTitle,
    DialogDescription,
  } from "$lib/components/ui/dialog";
  import { Badge } from "$lib/components/ui/badge";
  import { Button } from "$lib/components/ui/button";
  import { getIconComponent } from "../iconUtils";
  import { getDocsSiteUrl } from "$lib/config";

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
  const docsSiteUrl = getDocsSiteUrl().replace(/\/$/, "");

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
    <DialogContent class="max-w-xl p-0 gap-0 border-none bg-transparent shadow-none overflow-hidden focus:outline-none">
      <div class="slab w-full border border-border/50 shadow-xl">
        <div class="slab-header">
          <div class="flex items-center gap-3">
            <div class="flex h-8 w-8 items-center justify-center rounded bg-primary/10">
              <Icon class="h-4 w-4 text-primary" />
            </div>
            <DialogTitle class="text-base font-bold text-foreground">
              {item.name}
            </DialogTitle>
            <Badge variant="outline" class="ml-auto uppercase tracking-wide text-[10px] h-5 bg-background/50">
              Coming Soon
            </Badge>
          </div>
        </div>

        <div class="slab-body--padded space-y-5">
          <DialogDescription class="text-sm text-muted-foreground">
            {item.description || "This feature is planned for a future release."}
          </DialogDescription>

          <div class="border-l-2 border-warning bg-warning/5 p-3 sm:p-4">
            <h3 class="mb-1 flex items-center gap-2 text-sm font-semibold text-warning">
              <svg class="h-4 w-4" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 8v4m0 4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z" />
              </svg>
              In Development
            </h3>
            <p class="text-xs text-muted-foreground leading-relaxed">
              This feature is part of our public roadmap. Follow our documentation for the latest updates on implementation and timelines.
            </p>
          </div>

          <div class="space-y-3">
            <h3 class="text-sm font-semibold text-foreground">What to expect</h3>
            <ul class="space-y-2 text-sm text-muted-foreground">
              {#if item.name === "Composer"}
                <li class="flex gap-2"><span class="text-primary">•</span> Compose multiple input streams with picture-in-picture layouts.</li>
                <li class="flex gap-2"><span class="text-primary">•</span> Visual editor for stream overlays and scenes.</li>
                <li class="flex gap-2"><span class="text-primary">•</span> Compose on our processing nodes or on your own media node.</li>
              {:else if item.name === "Devices"}
                <li class="flex gap-2"><span class="text-primary">•</span> Manage your AV devices and their settings.</li>
                <li class="flex gap-2"><span class="text-primary">•</span> Select tracks to make available for stream composition.</li>
                <li class="flex gap-2"><span class="text-primary">•</span> View device status and availability.</li>
              {:else if item.name === "Computer Vision"}
                <li class="flex gap-2"><span class="text-primary">•</span> Configure AI-powered computer vision and analysis.</li>
                <li class="flex gap-2"><span class="text-primary">•</span> View AI processing history and results.</li>
                <li class="flex gap-2"><span class="text-primary">•</span> Export detailed AI processing results and metadata.</li>
              {:else if item.name === "Live Transcription"}
                <li class="flex gap-2"><span class="text-primary">•</span> Configure transcription models and languages.</li>
                <li class="flex gap-2"><span class="text-primary">•</span> View transcription history and results.</li>
                <li class="flex gap-2"><span class="text-primary">•</span> Export detailed transcripts and metadata.</li>
              {:else if item.name === "Daydream"}
                <li class="flex gap-2"><span class="text-primary">•</span> Apply live video models on your content.</li>
                <li class="flex gap-2"><span class="text-primary">•</span> Turn your live streams into art.</li>
              {:else if item.name === "Team Members"}
                <li class="flex gap-2"><span class="text-primary">•</span> Connect with your team members.</li>
                <li class="flex gap-2"><span class="text-primary">•</span> Invite new team members to your account.</li>
                <li class="flex gap-2"><span class="text-primary">•</span> Remove team members from your account.</li>
              {:else if item.name === "Permissions"}
                <li class="flex gap-2"><span class="text-primary">•</span> Manage permissions for your account.</li>
                <li class="flex gap-2"><span class="text-primary">•</span> Assign roles and permissions to your team members.</li>
              {:else if item.name === "Team Activity"}
                <li class="flex gap-2"><span class="text-primary">•</span> View activity logs for your team.</li>
                <li class="flex gap-2"><span class="text-primary">•</span> Rollback to previous configurations.</li>
              {:else if item.name === "Webhooks"}
                <li class="flex gap-2"><span class="text-primary">•</span> Configure integrations with external services.</li>
                <li class="flex gap-2"><span class="text-primary">•</span> Receive real-time notifications from the platform.</li>
                <li class="flex gap-2"><span class="text-primary">•</span> Automate & customize your media pipeline by plugging in your own logic.</li>
              {:else if item.name === "SDKs & Libraries"}
                <li class="flex gap-2"><span class="text-primary">•</span> Ready-to-use SDKs and libraries for your projects.</li>
                <li class="flex gap-2"><span class="text-primary">•</span> Documentation and examples for each SDK.</li>
                <li class="flex gap-2"><span class="text-primary">•</span> Support for multiple programming languages.</li>
              {:else if item.name === "Support Tickets"}
                <li class="flex gap-2"><span class="text-primary">•</span> Talk directly to our support team.</li>
                <li class="flex gap-2"><span class="text-primary">•</span> Map tickets to streams and users.</li>
              {:else}
                <li class="flex gap-2"><span class="text-primary">•</span> Additional details for this feature will be announced soon.</li>
              {/if}
            </ul>
          </div>
        </div>

        <div class="slab-actions slab-actions--row flex gap-0">
          <Button 
            variant="ghost" 
            onclick={close} 
            class="rounded-none h-12 flex-1 border-r border-[hsl(var(--tn-fg-gutter)/0.3)] hover:bg-muted/10 text-muted-foreground hover:text-foreground"
          >
            Close
          </Button>
          <Button
            href={docsSiteUrl}
            target="_blank"
            rel="noreferrer"
            variant="ghost"
            class="rounded-none h-12 flex-1 hover:bg-primary/10 text-primary hover:text-primary flex items-center justify-center gap-2"
          >
            View Roadmap
            <svg class="w-3 h-3 opacity-50" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M10 6H6a2 2 0 00-2 2v10a2 2 0 002 2h10a2 2 0 002-2v-4M14 4h6m0 0v6m0-6L10 14" /></svg>
          </Button>
        </div>
      </div>
    </DialogContent>
  </Dialog>
{/if}

<script lang="ts">
  import { getRtmpServerUrl } from "$lib/config";
  import { toast } from "$lib/stores/toast";
  import { getIconComponent } from "$lib/iconUtils";
  import { Button } from "$lib/components/ui/button";

  interface Props {
    streamKey: string | null;
  }

  let { streamKey }: Props = $props();

  let displayStreamKey = $derived(
    streamKey ? `${streamKey.slice(0, 12)}...` : "Create a stream first"
  );

  // Build RTMP server URL from config
  let rtmpServerUrl = $derived(getRtmpServerUrl());

  function copyToClipboard(text: string, label: string) {
    navigator.clipboard.writeText(text);
    toast.success(`${label} copied to clipboard!`);
  }

  const CopyIcon = getIconComponent("Copy");
</script>

    <div class="space-y-4">
      <!-- Step 1 -->
      <div class="flex items-start space-x-4">
        <div
          class="w-8 h-8 bg-info text-background font-bold rounded-full flex items-center justify-center text-sm"
        >
          1
        </div>
        <div>
          <h3 class="font-semibold text-foreground">
            Get OBS Studio (It's Free!)
          </h3>
          <p class="text-sm text-muted-foreground">
            The most popular streaming software - works on any computer
          </p>
          <a
            href="https://obsproject.com/"
            target="_blank"
            class="text-info hover:underline text-sm"
          >
            Download OBS Studio →
          </a>
        </div>
      </div>

      <!-- Step 2 -->
      <div class="flex items-start space-x-4">
        <div
          class="w-8 h-8 bg-info text-background font-bold rounded-full flex items-center justify-center text-sm"
        >
          2
        </div>
        <div>
          <h3 class="font-semibold text-foreground">
            Connect OBS to FrameWorks
          </h3>
          <p class="text-sm text-muted-foreground mb-2">
            Just copy these settings into OBS (Settings → Stream):
          </p>
          <div
            class="bg-muted/30 p-3 space-y-3"
          >
            <div class="flex items-center justify-between gap-2">
              <div class="min-w-0 flex-1">
                <p class="text-xs text-muted-foreground">Server URL:</p>
                <p class="font-mono text-sm text-foreground truncate">
                  {rtmpServerUrl}
                </p>
              </div>
              <Button
                variant="ghost"
                size="icon"
                class="h-8 w-8 shrink-0"
                onclick={() => copyToClipboard(rtmpServerUrl, "Server URL")}
              >
                <CopyIcon class="w-4 h-4" />
              </Button>
            </div>
            <div class="flex items-center justify-between gap-2">
              <div class="min-w-0 flex-1">
                <p class="text-xs text-muted-foreground">Stream Key:</p>
                <p class="font-mono text-sm text-foreground truncate">
                  {displayStreamKey}
                </p>
              </div>
              {#if streamKey}
                <Button
                  variant="ghost"
                  size="icon"
                  class="h-8 w-8 shrink-0"
                  onclick={() => copyToClipboard(streamKey, "Stream Key")}
                >
                  <CopyIcon class="w-4 h-4" />
                </Button>
              {/if}
            </div>
          </div>
        </div>
      </div>

      <!-- Step 3 -->
      <div class="flex items-start space-x-4">
        <div
          class="w-8 h-8 bg-info text-background font-bold rounded-full flex items-center justify-center text-sm"
        >
          3
        </div>
        <div>
          <h3 class="font-semibold text-foreground">Start Streaming</h3>
          <p class="text-sm text-muted-foreground">
            Click "Start Streaming" in OBS to begin broadcasting
          </p>
        </div>
      </div>
    </div>

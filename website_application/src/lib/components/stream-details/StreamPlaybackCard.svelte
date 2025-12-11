<script lang="ts">
  import { getIconComponent } from "$lib/iconUtils";
  import { formatDate } from "$lib/utils/stream-helpers";

  let { stream, onCopy } = $props();

  const CopyIcon = getIconComponent("Copy");
</script>

<div class="p-6">
  <h3 class="text-lg font-semibold gradient-text mb-4">Playback Info</h3>
  <div class="space-y-3">
    <div>
      <span class="text-sm text-muted-foreground">Playback ID</span>
      <div class="flex items-center space-x-2 mt-1">
        <code
          class="flex-1 px-3 py-2 text-sm font-mono text-info border border-border/50"
        >
          {stream?.playbackId || "Not set"}
        </code>
        <button
          onclick={() => onCopy?.(stream?.playbackId)}
          class="p-2 border border-border/50 hover:bg-muted/50 transition-colors"
        >
          <CopyIcon class="w-4 h-4" />
        </button>
      </div>
    </div>
    <div class="text-sm text-muted-foreground">
      Created: {formatDate(stream?.createdAt)}
    </div>
  </div>
</div>

<script lang="ts">
  import { getIconComponent } from "$lib/iconUtils";
  import { formatDate } from "$lib/utils/stream-helpers";

  let { stream, onCopy } = $props();

  const CopyIcon = getIconComponent("Copy");
</script>

<div class="slab h-full shadow-none border-none">
  <div class="slab-header">
    <div class="flex items-center justify-between h-6">
      <h3 class="font-semibold text-xs uppercase tracking-wide text-muted-foreground">Playback Info</h3>
    </div>
  </div>
  <div class="slab-body--padded space-y-3">
    <div>
      <span class="text-sm text-muted-foreground">Playback ID</span>
      <div class="flex items-center space-x-2 mt-1">
        <code
          class="flex-1 min-w-0 px-3 py-2 text-xs font-mono text-info bg-muted/20 truncate"
        >
          {stream?.playbackId || "Not set"}
        </code>
        <button
          onclick={() => onCopy?.(stream?.playbackId)}
          class="p-2 hover:bg-muted/50 transition-colors text-muted-foreground hover:text-foreground"
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

<script lang="ts">
  import { getIconComponent } from "$lib/iconUtils";

  let { stream, loading = false, onRefresh, onCopy } = $props();

  const RefreshIcon = getIconComponent("RefreshCw");
  const CopyIcon = getIconComponent("Copy");
</script>

<div class="slab h-full shadow-none border-none">
  <div class="slab-header flex items-center justify-between">
    <h3 class="font-semibold text-xs uppercase tracking-wide text-muted-foreground">Stream Key</h3>
    <button
      onclick={onRefresh}
      disabled={loading}
      class="h-6 w-6 flex items-center justify-center p-0 text-muted-foreground hover:text-foreground transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
    >
      <RefreshIcon class="w-4 h-4 {loading ? 'animate-spin' : ''}" />
    </button>
  </div>
  <div class="slab-body--padded space-y-3">
    <div>
      <span class="text-sm text-muted-foreground">Primary Key</span>
      <div class="flex items-center space-x-2 mt-1">
        <code
          class="flex-1 min-w-0 px-3 py-2 text-xs font-mono text-info bg-muted/20 truncate"
        >
          {stream?.streamKey || "Not set"}
        </code>
        <button
          onclick={() => onCopy?.(stream?.streamKey)}
          class="p-2 hover:bg-muted/50 transition-colors text-muted-foreground hover:text-foreground"
        >
          <CopyIcon class="w-4 h-4" />
        </button>
      </div>
    </div>
  </div>
</div>

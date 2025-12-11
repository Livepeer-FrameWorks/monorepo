<script lang="ts">
  import { getIconComponent } from "$lib/iconUtils";

  let { stream, loading = false, onRefresh, onCopy } = $props();

  const RefreshIcon = getIconComponent("RefreshCw");
  const CopyIcon = getIconComponent("Copy");
</script>

<div class="p-6">
  <div class="flex items-center justify-between mb-4">
    <h3 class="text-lg font-semibold gradient-text">Stream Key</h3>
    <button
      onclick={onRefresh}
      disabled={loading}
      class="p-2 border border-border/50 hover:bg-muted/50 transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
    >
      <RefreshIcon class="w-4 h-4 {loading ? 'animate-spin' : ''}" />
    </button>
  </div>
  <div class="space-y-3">
    <div>
      <span class="text-sm text-muted-foreground">Primary Key</span>
      <div class="flex items-center space-x-2 mt-1">
        <code
          class="flex-1 px-3 py-2 text-sm font-mono text-info border border-border/50"
        >
          {stream?.streamKey || "Not set"}
        </code>
        <button
          onclick={() => onCopy?.(stream?.streamKey)}
          class="p-2 border border-border/50 hover:bg-muted/50 transition-colors"
        >
          <CopyIcon class="w-4 h-4" />
        </button>
      </div>
    </div>
  </div>
</div>

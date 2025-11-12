<script>
  import { getIconComponent } from "$lib/iconUtils";

  let { stream, loading = false, onRefresh, onCopy } = $props();

  const RefreshIcon = getIconComponent("RefreshCw");
  const CopyIcon = getIconComponent("Copy");
</script>

<div
  class="glass-card p-6 transition-all hover:shadow-brand-subtle hover:scale-[1.01]"
>
  <div class="flex items-center justify-between mb-4">
    <h3 class="text-lg font-semibold gradient-text">Stream Key</h3>
    <button
      onclick={onRefresh}
      disabled={loading}
      class="p-2 rounded-lg bg-tokyo-night-bg-dark hover:bg-tokyo-night-selection transition-all hover:shadow-brand-subtle disabled:opacity-50 disabled:cursor-not-allowed"
    >
      <RefreshIcon class="w-4 h-4 {loading ? 'animate-spin' : ''}" />
    </button>
  </div>
  <div class="space-y-3">
    <div>
      <span class="text-sm text-tokyo-night-comment">Primary Key</span>
      <div class="flex items-center space-x-2 mt-1">
        <code
          class="flex-1 px-3 py-2 bg-tokyo-night-bg-dark rounded-lg text-sm font-mono text-tokyo-night-cyan border border-tokyo-night-fg-gutter transition-all focus-within:ring-2 focus-within:ring-tokyo-night-blue"
        >
          {stream?.streamKey || "Not set"}
        </code>
        <button
          onclick={() => onCopy?.(stream?.streamKey)}
          class="p-2 rounded-lg bg-tokyo-night-bg-dark hover:bg-tokyo-night-selection transition-all hover:shadow-brand-subtle"
        >
          <CopyIcon class="w-4 h-4" />
        </button>
      </div>
    </div>
  </div>
</div>

<script lang="ts">
  import { Button } from "$lib/components/ui/button";
  import { Switch } from "$lib/components/ui/switch";

  interface Props {
    showQueryEditor: boolean;
    showCodeExamples: boolean;
    hasHistory: boolean;
    demoMode: boolean;
    loading: boolean;
    onOpenOverlay: (type: "schema" | "templates") => void;
    onToggleQuery: () => void;
    onToggleCode: () => void;
    onClearHistory: () => void;
    onExecute: () => void;
    onDemoModeChange: (checked: boolean) => void;
  }

  let {
    showQueryEditor,
    showCodeExamples,
    hasHistory,
    demoMode,
    loading,
    onOpenOverlay,
    onToggleQuery,
    onToggleCode,
    onClearHistory,
    onExecute,
    onDemoModeChange,
  }: Props = $props();
</script>

<!-- Header with controls -->
<div
  class="flex items-center justify-between border-b border-tokyo-night-fg-gutter bg-tokyo-night-bg-light p-4"
>
  <div class="flex space-x-4">
    <!-- Overlay triggers -->
    <button
      class="flex items-center space-x-2 px-3 py-1 rounded text-sm transition-colors text-tokyo-night-fg hover:bg-tokyo-night-bg-highlight"
      onclick={() => onOpenOverlay("templates")}
    >
      <span>Templates</span>
    </button>
    <button
      class="flex items-center space-x-2 px-3 py-1 rounded text-sm transition-colors text-tokyo-night-fg hover:bg-tokyo-night-bg-highlight"
      onclick={() => onOpenOverlay("schema")}
    >
      <span>Schema</span>
    </button>

    <!-- Query/Code toggle -->
    <div class="flex border border-tokyo-night-fg-gutter rounded overflow-hidden">
      <button
        class="px-3 py-1 text-sm transition-colors {showQueryEditor
          ? 'bg-tokyo-night-blue text-white'
          : 'text-tokyo-night-fg hover:bg-tokyo-night-bg-highlight'}"
        onclick={onToggleQuery}
      >
        Query
      </button>
      <button
        class="px-3 py-1 text-sm transition-colors {showCodeExamples
          ? 'bg-tokyo-night-blue text-white'
          : 'text-tokyo-night-fg hover:bg-tokyo-night-bg-highlight'}"
        onclick={onToggleCode}
      >
        Code
      </button>
    </div>

    {#if hasHistory}
      <button
        class="flex items-center space-x-2 px-3 py-1 rounded text-sm text-tokyo-night-fg hover:bg-tokyo-night-bg-highlight transition-colors"
        onclick={onClearHistory}
      >
        <span>Clear History</span>
      </button>
    {/if}
  </div>

  <div class="flex items-center space-x-3">
    <div class="flex items-center space-x-2">
      <Switch
        checked={demoMode}
        onCheckedChange={onDemoModeChange}
        id="demo-mode-toggle"
      />
      <label for="demo-mode-toggle" class="text-xs text-tokyo-night-fg">
        {demoMode ? "Demo Mode" : "Demo"}
      </label>
    </div>

    <Button class="gap-2" onclick={onExecute} disabled={loading}>
      <span>{loading ? "Running..." : "Execute"}</span>
    </Button>
  </div>
</div>

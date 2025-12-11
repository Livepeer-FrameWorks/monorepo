<script lang="ts">
  import { Button } from "$lib/components/ui/button";
  import { Switch } from "$lib/components/ui/switch";
  import { BookOpen } from "lucide-svelte";

  interface Props {
    showQueryEditor: boolean;
    showCodeExamples: boolean;
    hasHistory: boolean;
    demoMode: boolean;
    loading: boolean;
    onOpenLibrary: () => void;
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
    onOpenLibrary,
    onToggleQuery,
    onToggleCode,
    onClearHistory,
    onExecute,
    onDemoModeChange,
  }: Props = $props();
</script>

<!-- Header with controls -->
<div
  class="flex items-center justify-between border-b border-border bg-card p-4"
>
  <div class="flex space-x-4">
    <!-- Query Library trigger -->
    <button
      class="flex items-center gap-2 px-3 py-1 text-sm transition-colors text-foreground hover:bg-muted/50"
      onclick={onOpenLibrary}
    >
      <BookOpen class="w-4 h-4" />
      <span>Library</span>
    </button>

    <!-- Query/Code toggle -->
    <div class="flex border border-border/50 overflow-hidden">
      <button
        class="px-3 py-1 text-sm transition-colors {showQueryEditor
          ? 'bg-primary text-primary-foreground'
          : 'text-foreground hover:bg-muted/50'}"
        onclick={onToggleQuery}
      >
        Query
      </button>
      <button
        class="px-3 py-1 text-sm transition-colors {showCodeExamples
          ? 'bg-primary text-primary-foreground'
          : 'text-foreground hover:bg-muted/50'}"
        onclick={onToggleCode}
      >
        Code
      </button>
    </div>

    {#if hasHistory}
      <button
        class="flex items-center space-x-2 px-3 py-1 text-sm text-foreground hover:bg-muted/50 transition-colors"
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
      <label for="demo-mode-toggle" class="text-xs text-foreground">
        {demoMode ? "Demo Mode" : "Demo"}
      </label>
    </div>

    <Button class="gap-2" onclick={onExecute} disabled={loading}>
      <span>{loading ? "Running..." : "Execute"}</span>
    </Button>
  </div>
</div>

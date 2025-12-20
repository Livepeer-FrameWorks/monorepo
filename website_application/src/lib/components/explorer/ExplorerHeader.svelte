<script lang="ts">
  import { Button } from "$lib/components/ui/button";
  import { Switch } from "$lib/components/ui/switch";
  import * as Popover from "$lib/components/ui/popover";
  import { BookOpen, HelpCircle } from "lucide-svelte";
  import type { VariableDefinition } from "$lib/graphql/services/gqlParser";
  import { getVariableHint } from "$lib/graphql/services/gqlParser";

  interface QueryHelp {
    description?: string;
    variables: VariableDefinition[];
    fragments: string[];
  }

  interface Props {
    showQueryEditor: boolean;
    showCodeExamples: boolean;
    hasHistory: boolean;
    demoMode: boolean;
    loading: boolean;
    queryHelp?: QueryHelp;
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
    queryHelp,
    onOpenLibrary,
    onToggleQuery,
    onToggleCode,
    onClearHistory,
    onExecute,
    onDemoModeChange,
  }: Props = $props();

  let hasHelp = $derived(
    queryHelp && (queryHelp.description || queryHelp.variables.length > 0 || queryHelp.fragments.length > 0)
  );
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

    <!-- Query Help popover -->
    {#if hasHelp}
      <Popover.Root>
        <Popover.Trigger>
          <button
            class="flex items-center gap-2 px-3 py-1 text-sm transition-colors text-muted-foreground hover:text-foreground hover:bg-muted/50"
            title="Query documentation"
          >
            <HelpCircle class="w-4 h-4" />
            <span>Help</span>
          </button>
        </Popover.Trigger>
        <Popover.Content class="w-80 max-h-96 overflow-y-auto" align="start">
          <div class="space-y-3">
            {#if queryHelp?.description}
              <div>
                <h4 class="text-xs font-semibold text-muted-foreground uppercase tracking-wide mb-1">Description</h4>
                <p class="text-sm text-foreground">{queryHelp.description}</p>
              </div>
            {/if}

            {#if queryHelp?.variables && queryHelp.variables.length > 0}
              {#if queryHelp?.description}
                <div class="border-t border-border/50"></div>
              {/if}
              <div>
                <h4 class="text-xs font-semibold text-muted-foreground uppercase tracking-wide mb-2">Variables</h4>
                <ul class="space-y-2">
                  {#each queryHelp.variables as v}
                    {@const hint = getVariableHint(v.name, v.type)}
                    <li class="text-sm">
                      <div class="flex items-baseline gap-2">
                        <code class="text-primary font-mono text-xs">${v.name}</code>
                        <span class="text-muted-foreground text-xs">
                          {v.type}{#if v.defaultValue !== undefined}<span class="text-green-600"> = {JSON.stringify(v.defaultValue)}</span>{/if}
                        </span>
                      </div>
                      {#if hint}
                        <p class="text-xs text-muted-foreground mt-0.5 ml-1">{hint}</p>
                      {/if}
                    </li>
                  {/each}
                </ul>
              </div>
            {/if}

            {#if queryHelp?.fragments && queryHelp.fragments.length > 0}
              <div class="border-t border-border/50"></div>
              <div>
                <h4 class="text-xs font-semibold text-muted-foreground uppercase tracking-wide mb-1">Fragments Used</h4>
                <div class="flex flex-wrap gap-1">
                  {#each queryHelp.fragments as frag}
                    <code class="text-xs bg-muted px-1.5 py-0.5 rounded font-mono">{frag}</code>
                  {/each}
                </div>
              </div>
            {/if}
          </div>
        </Popover.Content>
      </Popover.Root>
    {/if}

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

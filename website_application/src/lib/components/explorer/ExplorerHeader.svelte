<script lang="ts">
  import { Button } from "$lib/components/ui/button";
  import * as Popover from "$lib/components/ui/popover";
  import { BookOpen, Database, HelpCircle } from "lucide-svelte";
  import type { VariableDefinition } from "$lib/graphql/services/gqlParser";
  import { getVariableHint } from "$lib/graphql/services/gqlParser";

  interface QueryHelp {
    description?: string;
    variables: VariableDefinition[];
    fragments: string[];
    tips?: Array<{ title: string; body: string }>;
  }

  type PanelView = 'templates' | 'schema' | null;

  interface Props {
    showQueryEditor: boolean;
    showCodeExamples: boolean;
    hasHistory: boolean;
    demoMode: boolean;
    loading: boolean;
    queryHelp?: QueryHelp;
    activePanel?: PanelView;
    onPanelChange: (panel: PanelView) => void;
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
    activePanel = null,
    onPanelChange,
    onToggleQuery,
    onToggleCode,
    onClearHistory,
    onExecute,
    onDemoModeChange,
  }: Props = $props();

  let hasHelp = $derived(
    queryHelp &&
    (queryHelp.description ||
      queryHelp.variables.length > 0 ||
      queryHelp.fragments.length > 0 ||
      (queryHelp.tips && queryHelp.tips.length > 0))
  );

  function togglePanel(panel: PanelView) {
    onPanelChange(activePanel === panel ? null : panel);
  }
</script>

<!-- Header with controls -->
<div
  class="flex items-center justify-between border-b border-border bg-card p-4"
>
  <div class="flex space-x-4">
    <!-- Templates button -->
    <button
      class="flex items-center gap-2 px-3 py-1 text-sm transition-colors {activePanel === 'templates' ? 'text-primary bg-muted/30' : 'text-foreground hover:bg-muted/50'}"
      onclick={() => togglePanel('templates')}
      title={activePanel === 'templates' ? 'Hide Templates' : 'Show Templates'}
    >
      <BookOpen class="w-4 h-4" />
      <span>Templates</span>
    </button>

    <!-- Schema button -->
    <button
      class="flex items-center gap-2 px-3 py-1 text-sm transition-colors {activePanel === 'schema' ? 'text-primary bg-muted/30' : 'text-foreground hover:bg-muted/50'}"
      onclick={() => togglePanel('schema')}
      title={activePanel === 'schema' ? 'Hide Schema' : 'Show Schema'}
    >
      <Database class="w-4 h-4" />
      <span>Schema</span>
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
        <Popover.Content class="w-80 max-w-[calc(100vw-2rem)] max-h-96 overflow-y-auto" align="start">
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
                  {#each queryHelp.variables as v (v.name)}
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
                  {#each queryHelp.fragments as frag (frag)}
                    <code class="text-xs bg-muted px-1.5 py-0.5 rounded font-mono">{frag}</code>
                  {/each}
                </div>
              </div>
            {/if}

            {#if queryHelp?.tips && queryHelp.tips.length > 0}
              <div class="border-t border-border/50"></div>
              <div>
                <h4 class="text-xs font-semibold text-muted-foreground uppercase tracking-wide mb-2">Tips</h4>
                <div class="space-y-2 text-xs text-muted-foreground">
                  {#each queryHelp.tips as tip (tip.title)}
                    <div>
                      <p class="text-foreground text-xs font-medium">{tip.title}</p>
                      <p class="text-xs text-muted-foreground">{tip.body}</p>
                    </div>
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
        <span class="text-xs text-muted-foreground">Sandbox</span>
        <button
          type="button"
          class="relative h-7 w-20 border rounded-none shadow-inner transition-colors {demoMode ? 'bg-emerald-500/20 border-emerald-500/40' : 'bg-muted/50 border-border'}"
          onclick={() => onDemoModeChange(!demoMode)}
          aria-pressed={demoMode}
        >
          <span class="absolute inset-0 flex items-center justify-between px-2 text-[10px] uppercase tracking-wide">
            <span class="{demoMode ? 'text-foreground' : 'text-muted-foreground'}">On</span>
            <span class="{!demoMode ? 'text-foreground' : 'text-muted-foreground'}">Off</span>
          </span>
          <span
            class="absolute top-0.5 h-6 w-8 bg-foreground/90 border border-border/40 rounded-none transition-all"
            class:left-0.5={!demoMode}
            class:right-0.5={demoMode}
          ></span>
        </button>
      </div>

    <Button class="gap-2" onclick={onExecute} disabled={loading}>
      <span>{loading ? "Running..." : "Execute"}</span>
    </Button>
  </div>
</div>

<script lang="ts">
  import { browser } from '$app/environment';
  import CodeMirrorEditor from './CodeMirrorEditor.svelte';

  // Schema can be a GraphQLSchema, introspection result, or null
  type SchemaInput = Record<string, unknown> | null;

  interface Props {
    query: string;
    variables: string;
    schema?: SchemaInput;
    onKeyPress: (event: KeyboardEvent) => void;
    onCursorInfo?: (info: unknown | null) => void;
  }

  let {
    query = $bindable(),
    variables = $bindable(),
    schema = null,
    onKeyPress,
    onCursorInfo,
  }: Props = $props();

  function handleQueryKeydown(event: KeyboardEvent) {
    // Check for Ctrl+Enter to execute
    if (event.ctrlKey && event.key === 'Enter') {
      event.preventDefault();
      onKeyPress(event);
    }
  }

  function handleVariablesKeydown(event: KeyboardEvent) {
    // Check for Ctrl+Enter to execute
    if (event.ctrlKey && event.key === 'Enter') {
      event.preventDefault();
      onKeyPress(event);
    }
  }
</script>

<!-- Query Editor Section -->
<div class="flex-1 flex flex-col min-h-0">
  <div class="flex items-center justify-between px-4 py-2 border-b border-border/30 bg-muted/20">
    <h3 class="text-sm font-semibold text-foreground">GraphQL Query</h3>
    <div class="flex items-center gap-3 text-xs text-muted-foreground">
      <span><kbd class="px-1 py-0.5 bg-muted border border-border/50 font-mono text-[10px]">Ctrl</kbd>+<kbd class="px-1 py-0.5 bg-muted border border-border/50 font-mono text-[10px]">Space</kbd> autocomplete</span>
      <span><kbd class="px-1 py-0.5 bg-muted border border-border/50 font-mono text-[10px]">Ctrl</kbd>+<kbd class="px-1 py-0.5 bg-muted border border-border/50 font-mono text-[10px]">Enter</kbd> run</span>
    </div>
  </div>
  <div class="flex-1 min-h-[300px] max-h-[500px] overflow-hidden">
    {#if browser}
      <CodeMirrorEditor
        bind:value={query}
        language="graphql"
        {schema}
        placeholder="# Enter your GraphQL query here..."
        minHeight="300px"
        onkeydown={handleQueryKeydown}
        {onCursorInfo}
      />
    {:else}
      <!-- SSR fallback -->
      <textarea
        bind:value={query}
        placeholder="Enter your GraphQL query here..."
        class="w-full h-full text-sm font-mono bg-background p-3 text-foreground placeholder-muted-foreground resize-none border-0 focus:outline-none"
      ></textarea>
    {/if}
  </div>
</div>

<!-- Variables Section -->
<div class="border-t border-border/30">
  <div class="flex items-center justify-between px-4 py-2 bg-muted/20">
    <h3 class="text-sm font-semibold text-foreground">Variables (JSON)</h3>
  </div>
  <div class="h-32 overflow-hidden">
    {#if browser}
      <CodeMirrorEditor
        bind:value={variables}
        language="json"
        placeholder={'{}'}
        minHeight="128px"
        onkeydown={handleVariablesKeydown}
      />
    {:else}
      <!-- SSR fallback -->
      <textarea
        bind:value={variables}
        placeholder={'{}'}
        class="w-full h-full text-sm font-mono bg-background p-3 text-foreground placeholder-muted-foreground resize-none border-0 focus:outline-none"
      ></textarea>
    {/if}
  </div>
</div>

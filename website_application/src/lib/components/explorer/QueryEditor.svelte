<script lang="ts">
  interface Props {
    query: string;
    variables: string;
    onKeyPress: (event: KeyboardEvent) => void;
  }

  let {
    query = $bindable(),
    variables = $bindable(),
    onKeyPress,
  }: Props = $props();

  let queryTextarea = $state<HTMLTextAreaElement | undefined>();
  let variablesTextarea = $state<HTMLTextAreaElement | undefined>();
</script>

<!-- Query Editor Section -->
<div class="p-4 flex-1 flex flex-col">
  <div class="flex items-center justify-between mb-2">
    <h3 class="text-sm font-semibold text-tokyo-night-fg">GraphQL Query</h3>
    <span class="text-xs text-tokyo-night-comment">Ctrl+Enter to execute</span>
  </div>
  <textarea
    bind:this={queryTextarea}
    bind:value={query}
    placeholder="Enter your GraphQL query here..."
    class="w-full flex-1 min-h-[300px] text-sm font-mono bg-tokyo-night-bg border border-tokyo-night-fg-gutter rounded p-3 text-tokyo-night-fg placeholder-tokyo-night-comment resize-none focus:border-tokyo-night-blue focus:ring-1 focus:ring-tokyo-night-blue"
    onkeydown={onKeyPress}
  ></textarea>
</div>

<!-- Variables Section -->
<div class="border-t border-tokyo-night-fg-gutter p-4">
  <h3 class="text-sm font-semibold text-tokyo-night-fg mb-2">
    Variables (JSON)
  </h3>
  <textarea
    bind:this={variablesTextarea}
    bind:value={variables}
    placeholder={"{}"}
    class="w-full h-32 text-sm font-mono bg-tokyo-night-bg border border-tokyo-night-fg-gutter rounded p-3 text-tokyo-night-fg placeholder-tokyo-night-comment resize-none focus:border-tokyo-night-blue focus:ring-1 focus:ring-tokyo-night-blue"
    onkeydown={onKeyPress}
  ></textarea>
</div>

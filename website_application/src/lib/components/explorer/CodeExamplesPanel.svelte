<script lang="ts">
  interface Props {
    codeExamples: Record<string, string>;
    selectedLanguage: string;
    languages: Array<{ key: string; name: string }>;
    onCopy: (code: string) => void;
  }

  let {
    codeExamples,
    selectedLanguage = $bindable(),
    languages,
    onCopy,
  }: Props = $props();
</script>

<!-- Code Examples Section -->
<div class="p-4 flex-1 flex flex-col">
  <div class="flex items-center justify-between mb-3">
    <h3 class="text-sm font-semibold text-foreground">Code Examples</h3>
    <select
      bind:value={selectedLanguage}
      class="text-xs bg-background border border-border/50 px-2 py-1 text-foreground"
    >
      {#each languages as lang (lang.key)}
        <option value={lang.key}>{lang.name}</option>
      {/each}
    </select>
  </div>

  {#if codeExamples[selectedLanguage]}
    <div class="relative flex-1 min-w-0 min-h-0">
      <pre
        class="text-xs bg-background p-3 border border-border/50 overflow-auto h-full text-foreground font-mono whitespace-pre"><code
          >{codeExamples[selectedLanguage]}</code
        ></pre>
      <button
        class="absolute top-2 right-2 text-xs border border-border/50 px-2 py-1 hover:bg-muted/50 transition-colors bg-background/80"
        onclick={() => onCopy(codeExamples[selectedLanguage])}
      >
        Copy
      </button>
    </div>
  {/if}
</div>

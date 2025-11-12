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
    <h3 class="text-sm font-semibold text-tokyo-night-fg">Code Examples</h3>
    <select
      bind:value={selectedLanguage}
      class="text-xs bg-tokyo-night-bg border border-tokyo-night-fg-gutter rounded px-2 py-1 text-tokyo-night-fg"
    >
      {#each languages as lang (lang.key)}
        <option value={lang.key}>{lang.name}</option>
      {/each}
    </select>
  </div>

  {#if codeExamples[selectedLanguage]}
    <div class="relative flex-1">
      <pre
        class="text-xs bg-tokyo-night-bg p-3 rounded border border-tokyo-night-fg-gutter overflow-auto h-full text-tokyo-night-fg font-mono"><code
          >{codeExamples[selectedLanguage]}</code
        ></pre>
      <button
        class="absolute top-2 right-2 text-xs bg-tokyo-night-bg-highlight border border-tokyo-night-fg-gutter rounded px-2 py-1 hover:bg-tokyo-night-bg-light transition-colors"
        onclick={() => onCopy(codeExamples[selectedLanguage])}
      >
        Copy
      </button>
    </div>
  {/if}
</div>

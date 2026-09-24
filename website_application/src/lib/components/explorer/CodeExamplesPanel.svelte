<script lang="ts">
  interface Language {
    key: string;
    name: string;
    group?: string;
  }

  interface Props {
    codeExamples: Record<string, string | undefined>;
    selectedLanguage: string;
    languages: Language[];
    onCopy: (code: string) => void;
  }

  let { codeExamples, selectedLanguage = $bindable(), languages, onCopy }: Props = $props();

  let groups = $derived.by(() => {
    const out: Array<{ label: string | undefined; items: Language[] }> = [];
    for (const lang of languages) {
      const last = out[out.length - 1];
      if (last && last.label === lang.group) last.items.push(lang);
      else out.push({ label: lang.group, items: [lang] });
    }
    return out;
  });

  let code = $derived(codeExamples[selectedLanguage]);
</script>

<!-- Code Examples Section -->
<div class="p-4 flex-1 flex flex-col">
  <div class="flex items-center justify-between mb-3">
    <h3 class="text-sm font-semibold text-foreground">Code Examples</h3>
    <select
      bind:value={selectedLanguage}
      class="text-xs bg-background border border-border/50 px-2 py-1 text-foreground"
    >
      {#each groups as group, i (group.label ?? i)}
        {#if group.label}
          <optgroup label={group.label}>
            {#each group.items as lang (lang.key)}
              <option value={lang.key}>{lang.name}</option>
            {/each}
          </optgroup>
        {:else}
          {#each group.items as lang (lang.key)}
            <option value={lang.key}>{lang.name}</option>
          {/each}
        {/if}
      {/each}
    </select>
  </div>

  {#if code}
    <div class="relative flex-1 min-w-0 min-h-0">
      <pre
        class="text-xs bg-background p-3 border border-border/50 overflow-auto h-full text-foreground font-mono whitespace-pre [tab-size:4]"><code
          >{code}</code
        ></pre>
      <button
        class="absolute top-2 right-2 text-xs border border-border/50 px-2 py-1 hover:bg-muted/50 transition-colors bg-background/80"
        onclick={() => onCopy(code ?? "")}
      >
        Copy
      </button>
    </div>
  {:else if Object.keys(codeExamples).length > 0}
    <p class="text-xs text-muted-foreground">Loading SDK operations…</p>
  {/if}
</div>

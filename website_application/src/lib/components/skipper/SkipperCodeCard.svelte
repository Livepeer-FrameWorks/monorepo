<script lang="ts">
  import { getIconComponent } from "$lib/iconUtils";

  interface FieldSummary {
    name?: string;
    type?: string;
    description?: string;
  }

  interface Props {
    toolName: string;
    payload: Record<string, unknown>;
  }

  let { toolName, payload }: Props = $props();

  const CopyIcon = getIconComponent("Copy");
  const CheckIcon = getIconComponent("Check");
  const FileCodeIcon = getIconComponent("FileCode");
  const DatabaseIcon = getIconComponent("Database");

  const toolLabels: Record<string, string> = {
    introspect_schema: "Schema Introspection",
    generate_query: "Generated Query",
    execute_query: "Query Result",
  };

  let query = $derived((payload.query || payload.Query || "") as string);
  let variables = $derived(
    (payload.variables || payload.Variables || null) as Record<string, unknown> | null
  );
  let hints = $derived((payload.hints || payload.Hints || []) as string[]);
  let hint = $derived((payload.hint || payload.Hint || "") as string);
  let fields = $derived((payload.fields || payload.Fields || []) as FieldSummary[]);
  let source = $derived((payload.source || payload.Source || "") as string);

  let isExecuteQuery = $derived(toolName === "execute_query");
  let isIntrospect = $derived(toolName === "introspect_schema");

  let codeContent = $derived.by(() => {
    if (query) return query;
    if (isExecuteQuery) return JSON.stringify(payload, null, 2);
    return "";
  });
  let allHints = $derived([...(hint ? [hint] : []), ...hints]);
  let hasVars = $derived(variables && Object.keys(variables).length > 0);

  let copied = $state(false);

  function copyCode() {
    navigator.clipboard.writeText(codeContent);
    copied = true;
    setTimeout(() => (copied = false), 2000);
  }
</script>

<div class="rounded-lg border border-border bg-card text-sm">
  <div class="flex items-center gap-2 border-b border-border px-4 py-2.5">
    {#if isExecuteQuery}
      <DatabaseIcon class="h-4 w-4 text-muted-foreground" />
    {:else}
      <FileCodeIcon class="h-4 w-4 text-muted-foreground" />
    {/if}
    <span class="font-semibold text-foreground">
      {toolLabels[toolName] ?? toolName}
    </span>
    {#if source}
      <span
        class="rounded-full border border-border bg-muted/40 px-2 py-0.5 text-[10px] text-muted-foreground"
      >
        {source}
      </span>
    {/if}
  </div>

  {#if isIntrospect && fields.length > 0}
    <div class="border-b border-border px-4 py-2.5">
      <div
        class="mb-1.5 text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground"
      >
        Fields
      </div>
      <div class="space-y-1">
        {#each fields as field (field.name)}
          <div class="flex items-center gap-2 text-xs">
            <span class="font-mono text-foreground">{field.name}</span>
            {#if field.type}
              <span class="text-muted-foreground/60">{field.type}</span>
            {/if}
          </div>
        {/each}
      </div>
    </div>
  {/if}

  {#if codeContent}
    <div class="group/code relative">
      <pre
        class="overflow-x-auto border-b border-border bg-muted/20 p-3 pr-10 text-xs text-foreground"><code
          >{codeContent}</code
        ></pre>
      <button
        type="button"
        class="absolute right-2 top-2 rounded-md border border-border bg-background/80 p-1 text-muted-foreground opacity-0 transition hover:text-foreground group-hover/code:opacity-100"
        onclick={copyCode}
        aria-label="Copy code"
      >
        {#if copied}
          <CheckIcon class="h-3.5 w-3.5 text-emerald-500" />
        {:else}
          <CopyIcon class="h-3.5 w-3.5" />
        {/if}
      </button>
    </div>
  {/if}

  {#if hasVars}
    <div class="border-b border-border px-4 py-2.5">
      <div
        class="mb-1.5 text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground"
      >
        Variables
      </div>
      <pre class="text-xs text-foreground">{JSON.stringify(variables, null, 2)}</pre>
    </div>
  {/if}

  {#if allHints.length > 0}
    <div class="px-4 py-2.5">
      {#each allHints as h (h)}
        <p class="text-xs text-muted-foreground">{h}</p>
      {/each}
    </div>
  {/if}
</div>

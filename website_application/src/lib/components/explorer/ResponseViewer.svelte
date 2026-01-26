<script lang="ts">
  import { formatTypeString } from "$lib/graphql/services/schemaUtils";

  interface FormattedResponse {
    timestamp: string;
    duration: string;
    statusIcon: string;
    data: string | null;
    error: unknown | null;
  }

  interface Props {
    response: FormattedResponse | null;
    loading: boolean;
    onCopy: (text: string) => void;
    fieldDocs?: Array<{
      name: string;
      description?: string;
      args?: Array<{ name: string; description?: string; type?: { name?: string; kind?: string; ofType?: { name?: string } } }>;
      type?: { name?: string; kind?: string; ofType?: { name?: string } };
    }>;
    focusDoc?: {
      kind: "field" | "argument" | "enum" | "type" | "directive" | "variable";
      signature: string;
      description?: string;
      args?: Array<{
        name: string;
        type: string;
        description?: string;
        inputFields?: Array<{ name: string; type: string; description?: string }>;
      }>;
      inputFields?: Array<{ name: string; type: string; description?: string }>;
      enumValues?: Array<{ name: string; description?: string }>;
    } | null;
  }

  let { response, loading, onCopy, fieldDocs = [], focusDoc = null }: Props = $props();

  let showFieldDocs = $state(true);
</script>

<!-- Right Panel - Docs + Response -->
<div class="flex-1 flex flex-col min-w-0 overflow-hidden">
  <!-- Docs Panel (above Response) -->
  {#if focusDoc || fieldDocs.length > 0}
    <div class="p-3 border-b border-border/50 shrink-0 bg-muted/10">
      {#if focusDoc}
        <div class="flex items-center justify-between mb-2">
          <h4 class="text-xs font-semibold text-foreground">Focus Docs</h4>
          <button
            class="text-[10px] text-muted-foreground hover:text-foreground transition-colors"
            onclick={() => (showFieldDocs = !showFieldDocs)}
          >
            {showFieldDocs ? "Hide" : "Show"}
          </button>
        </div>
        {#if showFieldDocs}
          <div class="space-y-2 max-h-40 overflow-auto min-w-0">
            <div class="text-xs">
              <div class="flex items-baseline gap-2">
                <code class="text-primary font-mono break-words">{focusDoc.signature}</code>
              </div>
              {#if focusDoc.description}
                <div class="text-[11px] text-muted-foreground mt-0.5 break-words">
                  {focusDoc.description}
                </div>
              {/if}
            </div>

            {#if focusDoc.args && focusDoc.args.length > 0}
              <div class="text-xs">
                <div class="text-[11px] text-muted-foreground mb-1">Args</div>
                <div class="space-y-1">
                  {#each focusDoc.args as arg (arg.name)}
                    <div class="text-[11px]">
                      <span class="text-foreground font-mono">{arg.name}</span>
                      <span class="text-muted-foreground">: {arg.type}</span>
                      {#if arg.description}
                        <div class="text-[10px] text-muted-foreground ml-2 break-words">
                          {arg.description}
                        </div>
                      {/if}
                      {#if arg.inputFields && arg.inputFields.length > 0}
                        <div class="text-[10px] text-muted-foreground ml-2 mt-1">
                          Input fields:
                          <div class="mt-1 space-y-1">
                            {#each arg.inputFields as inputField (inputField.name)}
                              <div class="ml-2">
                                <span class="text-foreground font-mono">{inputField.name}</span>
                                <span class="text-muted-foreground">: {inputField.type}</span>
                                {#if inputField.description}
                                  <div class="text-[10px] text-muted-foreground ml-2 break-words">
                                    {inputField.description}
                                  </div>
                                {/if}
                              </div>
                            {/each}
                          </div>
                        </div>
                      {/if}
                    </div>
                  {/each}
                </div>
              </div>
            {/if}

            {#if focusDoc.inputFields && focusDoc.inputFields.length > 0}
              <div class="text-xs">
                <div class="text-[11px] text-muted-foreground mb-1">Fields</div>
                <div class="space-y-1">
                  {#each focusDoc.inputFields as inputField (inputField.name)}
                    <div class="text-[11px]">
                      <span class="text-foreground font-mono">{inputField.name}</span>
                      <span class="text-muted-foreground">: {inputField.type}</span>
                      {#if inputField.description}
                        <div class="text-[10px] text-muted-foreground ml-2 break-words">
                          {inputField.description}
                        </div>
                      {/if}
                    </div>
                  {/each}
                </div>
              </div>
            {/if}

            {#if focusDoc.enumValues && focusDoc.enumValues.length > 0}
              <div class="text-xs">
                <div class="text-[11px] text-muted-foreground mb-1">Enum values</div>
                <div class="flex flex-wrap gap-1">
                  {#each focusDoc.enumValues as enumVal (enumVal.name)}
                    <span class="text-[10px] bg-muted px-1.5 py-0.5 border border-border/40 font-mono">
                      {enumVal.name}
                    </span>
                  {/each}
                </div>
              </div>
            {/if}
          </div>
        {/if}
      {:else if fieldDocs.length > 0}
        <div class="flex items-center justify-between mb-2">
          <h4 class="text-xs font-semibold text-foreground">Field Docs</h4>
          <button
            class="text-[10px] text-muted-foreground hover:text-foreground transition-colors"
            onclick={() => (showFieldDocs = !showFieldDocs)}
          >
            {showFieldDocs ? "Hide" : "Show"}
          </button>
        </div>
        {#if showFieldDocs}
          <div class="space-y-2 max-h-40 overflow-auto min-w-0">
            {#each fieldDocs as field (field.name)}
              <div class="text-xs">
                <div class="flex items-baseline gap-2">
                  <code class="text-primary font-mono">{field.name}</code>
                  <span class="text-muted-foreground">{formatTypeString(field.type)}</span>
                </div>
                {#if field.description}
                  <div class="text-[11px] text-muted-foreground mt-0.5 break-words">{field.description}</div>
                {/if}
                {#if field.args && field.args.length > 0}
                  <div class="text-[10px] text-muted-foreground mt-0.5 break-words">
                    Args: {field.args.map((arg) => `${arg.name}: ${formatTypeString(arg.type)}`).join(", ")}
                  </div>
                  {#each field.args.filter(a => a.description) as arg (arg.name)}
                    <div class="text-[10px] text-muted-foreground ml-2">
                      <span class="font-mono">{arg.name}</span>: {arg.description}
                    </div>
                  {/each}
                {/if}
              </div>
            {/each}
          </div>
        {/if}
      {/if}
    </div>
  {/if}

  <!-- Response Panel -->
  <div class="p-4 flex-1 flex flex-col min-h-0 overflow-hidden">
    <div class="flex items-center justify-between mb-2 shrink-0">
      <h3 class="text-sm font-semibold text-foreground">Response</h3>
      {#if response}
        <div class="flex items-center space-x-2 text-xs">
          <span class="text-muted-foreground">{response.timestamp}</span>
          <span class="text-muted-foreground">•</span>
          <span class="text-muted-foreground">{response.duration}</span>
          <span>{response.statusIcon}</span>
        </div>
      {/if}
    </div>

    <div class="flex-1 min-h-0 border border-border/50 overflow-hidden">
      {#if loading}
        <div class="flex items-center justify-center h-full min-h-[300px]">
          <div class="text-center">
            <div class="text-muted-foreground">Executing query...</div>
          </div>
        </div>
      {:else if response}
        <div class="relative h-full min-h-[300px] min-w-0 overflow-auto">
          <pre
            class="text-sm p-4 text-foreground font-mono whitespace-pre-wrap break-words"
          >{#if response.error}{JSON.stringify(
                response.error,
                null,
                2,
              )}{:else}{response.data}{/if}</pre>
          <button
            class="absolute top-2 right-2 text-xs border border-border/50 px-2 py-1 hover:bg-muted/50 transition-colors bg-background/80 backdrop-blur-sm"
            onclick={() =>
              onCopy(
                response.error
                  ? JSON.stringify(response.error, null, 2)
                  : response.data || "",
              )}
          >
            Copy
          </button>
        </div>
      {:else}
        <div class="flex items-center justify-center h-full min-h-[300px]">
          <div class="text-center">
            <div class="text-foreground font-medium mb-2">
              GraphQL Explorer
            </div>
            <div class="text-muted-foreground text-sm">
              Execute a query to see results here
            </div>
          </div>
        </div>
      {/if}
    </div>
  </div>
</div>

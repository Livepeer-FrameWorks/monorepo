<script lang="ts">
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
  }

  let { response, loading, onCopy }: Props = $props();
</script>

<!-- Right Panel - Response -->
<div class="flex-1 flex flex-col min-w-0 overflow-hidden">
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

    <div
      class="flex-1 min-h-0 border border-border/50 overflow-hidden"
    >
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

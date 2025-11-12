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
<div class="flex-1 flex flex-col">
  <div class="p-4 flex-1 flex flex-col">
    <div class="flex items-center justify-between mb-2">
      <h3 class="text-sm font-semibold text-tokyo-night-fg">Response</h3>
      {#if response}
        <div class="flex items-center space-x-2 text-xs">
          <span class="text-tokyo-night-comment">{response.timestamp}</span>
          <span class="text-tokyo-night-comment">•</span>
          <span class="text-tokyo-night-comment">{response.duration}</span>
          <span>{response.statusIcon}</span>
        </div>
      {/if}
    </div>

    <div
      class="flex-1 min-h-0 border border-tokyo-night-fg-gutter rounded bg-tokyo-night-bg"
    >
      {#if loading}
        <div class="flex items-center justify-center h-full min-h-[300px]">
          <div class="text-center">
            <div class="text-tokyo-night-comment">Executing query...</div>
          </div>
        </div>
      {:else if response}
        <div class="relative h-full min-h-[300px]">
          <pre
            class="text-sm p-4 h-full overflow-auto text-tokyo-night-fg font-mono">{#if response.error}{JSON.stringify(
                response.error,
                null,
                2,
              )}{:else}{response.data}{/if}</pre>
          <button
            class="absolute top-2 right-2 text-xs bg-tokyo-night-bg-highlight border border-tokyo-night-fg-gutter rounded px-2 py-1 hover:bg-tokyo-night-bg-light transition-colors"
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
            <div class="text-tokyo-night-fg font-medium mb-2">
              GraphQL Explorer
            </div>
            <div class="text-tokyo-night-comment text-sm">
              Execute a query to see results here
            </div>
          </div>
        </div>
      {/if}
    </div>
  </div>
</div>

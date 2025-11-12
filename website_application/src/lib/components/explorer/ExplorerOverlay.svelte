<script lang="ts">
  interface QueryTemplate {
    name: string;
    description: string;
    query: string;
    variables: Record<string, unknown>;
  }

  interface QueryHistoryItem {
    id: number;
    query: string;
    variables: Record<string, unknown>;
    result: { statusIcon: string; [key: string]: unknown };
    timestamp: string;
  }

  interface SchemaType {
    fields: Array<{ name: string; description?: string }>;
  }

  interface Schema {
    queryType?: SchemaType;
    mutationType?: SchemaType;
    subscriptionType?: SchemaType;
  }

  interface Props {
    open: boolean;
    type: "schema" | "templates" | null;
    schema: Schema | null;
    queryTemplates: Record<string, QueryTemplate[]> | null;
    queryHistory: QueryHistoryItem[];
    loading: boolean;
    selectedTemplate: QueryTemplate | null;
    onClose: () => void;
    onSelectTemplate: (template: QueryTemplate) => void;
    onLoadFromHistory: (item: QueryHistoryItem) => void;
    onLoadSchema: () => void;
  }

  let {
    open,
    type,
    schema,
    queryTemplates,
    queryHistory,
    loading,
    selectedTemplate,
    onClose,
    onSelectTemplate,
    onLoadFromHistory,
    onLoadSchema,
  }: Props = $props();

  function stopEvent(event: Event) {
    event.stopPropagation();
  }
</script>

<!-- Overlay for Templates and Schema -->
{#if open}
  <div
    class="fixed inset-0 bg-black bg-opacity-50 flex z-50"
    role="button"
    tabindex="0"
    aria-label={`Close ${type} panel`}
    onclick={onClose}
    onkeydown={(event) => {
      if (event.key === "Enter" || event.key === " ") {
        event.preventDefault();
        onClose();
      }
    }}
  >
    <div
      class="w-1/3 max-w-md bg-tokyo-night-bg-light border-r border-tokyo-night-fg-gutter h-full overflow-y-auto slide-in-left"
      role="dialog"
      aria-modal="true"
      tabindex="-1"
      onclick={stopEvent}
      onkeydown={stopEvent}
    >
      <div
        class="p-4 border-b border-tokyo-night-fg-gutter flex items-center justify-between"
      >
        <h3 class="text-lg font-semibold text-tokyo-night-fg capitalize">
          {type}
        </h3>
        <button
          class="text-tokyo-night-fg hover:text-tokyo-night-red transition-colors"
          onclick={onClose}
        >
          ✕
        </button>
      </div>

      <div class="p-4 max-h-[calc(100vh-80px)] overflow-y-auto">
        {#if type === "templates" && queryTemplates}
          <h4 class="text-sm font-semibold text-tokyo-night-fg mb-3">
            Query Templates
          </h4>

          {#each ["queries", "mutations", "subscriptions"] as category (category)}
            {#if queryTemplates[category]?.length > 0}
              <div class="mb-4">
                <h4
                  class="text-xs font-medium text-tokyo-night-comment uppercase tracking-wider mb-2"
                >
                  {category}
                </h4>
                <div class="space-y-1">
                  {#each queryTemplates[category] as template (template.name)}
                    <button
                      class="w-full text-left p-3 text-xs rounded transition-all duration-200 hover:bg-tokyo-night-bg-highlight {selectedTemplate ===
                      template
                        ? 'bg-tokyo-night-bg-highlight border-l-2 border-tokyo-night-blue shadow-sm'
                        : 'border-l-2 border-transparent hover:border-tokyo-night-fg-gutter'}"
                      onclick={() => onSelectTemplate(template)}
                    >
                      <div class="font-medium text-tokyo-night-fg mb-1">
                        {template.name}
                      </div>
                      <div class="text-tokyo-night-comment leading-relaxed">
                        {template.description}
                      </div>
                    </button>
                  {/each}
                </div>
              </div>
            {/if}
          {/each}

          {#if queryHistory.length > 0}
            <div class="mt-6 pt-4 border-t border-tokyo-night-fg-gutter">
              <h4
                class="text-xs font-medium text-tokyo-night-comment uppercase tracking-wider mb-2"
              >
                Recent History
              </h4>
              <div class="space-y-1">
                {#each queryHistory as item (item.id)}
                  <button
                    class="w-full text-left p-2 text-xs rounded transition-colors hover:bg-tokyo-night-bg-highlight border border-transparent"
                    onclick={() => onLoadFromHistory(item)}
                  >
                    <div class="font-medium text-tokyo-night-fg truncate">
                      {item.query
                        .split("\n")[0]
                        .replace(/query\s+\w*\s*\{/, "")
                        .trim() || "Query"}
                    </div>
                    <div class="text-tokyo-night-comment mt-1">
                      {new Date(item.timestamp).toLocaleTimeString()} • {item
                        .result.statusIcon}
                    </div>
                  </button>
                {/each}
              </div>
            </div>
          {/if}
        {/if}

        {#if type === "schema"}
          <h4 class="text-sm font-semibold text-tokyo-night-fg mb-3">
            Schema Explorer
          </h4>
          {#if loading}
            <div class="flex items-center justify-center py-8">
              <span class="text-tokyo-night-comment">Loading schema...</span>
            </div>
          {:else if schema}
            <div class="space-y-4">
              {#if schema.queryType}
                <div>
                  <h4
                    class="text-xs font-medium text-tokyo-night-green uppercase tracking-wider mb-2"
                  >
                    Queries
                  </h4>
                  <div class="space-y-1">
                    {#each schema.queryType.fields as field (field.name)}
                      <div class="text-xs p-2 bg-tokyo-night-bg rounded">
                        <div class="font-mono text-tokyo-night-fg">
                          {field.name}
                        </div>
                        {#if field.description}
                          <div class="text-tokyo-night-comment mt-1">
                            {field.description}
                          </div>
                        {/if}
                      </div>
                    {/each}
                  </div>
                </div>
              {/if}

              {#if schema.mutationType}
                <div>
                  <h4
                    class="text-xs font-medium text-tokyo-night-blue uppercase tracking-wider mb-2"
                  >
                    Mutations
                  </h4>
                  <div class="space-y-1">
                    {#each schema.mutationType.fields as field (field.name)}
                      <div class="text-xs p-2 bg-tokyo-night-bg rounded">
                        <div class="font-mono text-tokyo-night-fg">
                          {field.name}
                        </div>
                        {#if field.description}
                          <div class="text-tokyo-night-comment mt-1">
                            {field.description}
                          </div>
                        {/if}
                      </div>
                    {/each}
                  </div>
                </div>
              {/if}

              {#if schema.subscriptionType}
                <div>
                  <h4
                    class="text-xs font-medium text-tokyo-night-purple uppercase tracking-wider mb-2"
                  >
                    Subscriptions
                  </h4>
                  <div class="space-y-1">
                    {#each schema.subscriptionType.fields as field (field.name)}
                      <div class="text-xs p-2 bg-tokyo-night-bg rounded">
                        <div class="font-mono text-tokyo-night-fg">
                          {field.name}
                        </div>
                        {#if field.description}
                          <div class="text-tokyo-night-comment mt-1">
                            {field.description}
                          </div>
                        {/if}
                      </div>
                    {/each}
                  </div>
                </div>
              {/if}
            </div>
          {:else}
            <button
              class="text-sm text-tokyo-night-blue hover:underline"
              onclick={onLoadSchema}
            >
              Load Schema
            </button>
          {/if}
        {/if}
      </div>
    </div>
  </div>
{/if}

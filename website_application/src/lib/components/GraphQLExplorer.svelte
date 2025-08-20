<script>
  import { onMount } from "svelte";
  import { explorerService } from "$lib/graphql/services/explorer.js";
  import { toast } from "$lib/stores/toast.js";

  export let initialQuery = "";
  export let authToken = null;

  // Component state
  let query =
    initialQuery ||
    `query GetStreams {
  streams {
    id
    name
    description
    streamKey
    playbackId
    status
    record
    createdAt
    updatedAt
  }
}`;
  let variables = "{}";
  let response = null;
  let loading = false;
  let schema = null;
  let queryTemplates = null;
  let selectedTemplate = null;
  let showVariables = true; // Always show variables when they exist
  let showSchema = false;
  let showTemplates = false;
  let showCodeExamples = false;
  let showQueryEditor = true;
  let selectedLanguage = "javascript";
  let queryHistory = [];
  let demoMode = false;

  // Overlay state
  let overlayOpen = false;
  let overlayType = null; // 'schema' or 'templates'
  let sidebarCollapsed = false;

  // Editor state
  let queryTextarea;
  let variablesTextarea;

  const languages = [
    { key: "javascript", name: "JavaScript (Apollo)" },
    { key: "fetch", name: "JavaScript (Fetch)" },
    { key: "curl", name: "cURL" },
    { key: "python", name: "Python" },
    { key: "go", name: "Go" },
  ];

  onMount(async () => {
    await loadQueryTemplates();
    loadQueryHistory();
  });

  async function loadQueryTemplates() {
    try {
      queryTemplates = explorerService.getQueryTemplates();
    } catch (error) {
      console.error("Failed to load query templates:", error);
      toast.error("Failed to load query templates");
    }
  }

  async function loadSchema() {
    if (schema) return; // Already loaded

    try {
      loading = true;
      schema = await explorerService.getSchema();
    } catch (error) {
      console.error("Failed to load schema:", error);
      toast.error("Failed to load GraphQL schema");
    } finally {
      loading = false;
    }
  }

  async function executeQuery() {
    if (!query.trim()) {
      toast.warning("Please enter a GraphQL query");
      return;
    }

    // Validate query syntax
    const validation = explorerService.validateQuery(query);
    if (!validation.valid) {
      toast.error(`Query validation error: ${validation.error}`);
      return;
    }

    // Parse variables
    let parsedVariables = {};
    if (variables.trim()) {
      try {
        parsedVariables = JSON.parse(variables);
      } catch (error) {
        toast.error("Invalid JSON in variables");
        return;
      }
    }

    // Determine operation type
    const operationType = query.trim().startsWith("mutation")
      ? "mutation"
      : "query";

    try {
      loading = true;
      const result = await explorerService.executeQuery(
        query,
        parsedVariables,
        operationType,
        demoMode
      );
      response = explorerService.formatResponse(result);

      // Add to history
      addToHistory(query, parsedVariables, response);

      if (result.error) {
        toast.error(
          "Query execution failed - check the response panel for details"
        );
      } else {
        const modeIndicator = demoMode ? " (Demo)" : "";
        toast.success(`Query executed successfully in ${response.duration}${modeIndicator}`);
      }
    } catch (error) {
      console.error("Query execution failed:", error);
      toast.error("Failed to execute query");
    } finally {
      loading = false;
    }
  }

  function selectTemplate(template) {
    selectedTemplate = template;
    query = template.query;
    variables = JSON.stringify(template.variables, null, 2);
    closeOverlay(); // Close overlay after selection
  }

  function addToHistory(queryText, vars, result) {
    const historyItem = {
      id: Date.now(),
      query: queryText,
      variables: vars,
      result: result,
      timestamp: new Date().toISOString(),
    };

    queryHistory = [historyItem, ...queryHistory].slice(0, 10); // Keep last 10
    saveQueryHistory();
  }

  function loadFromHistory(historyItem) {
    query = historyItem.query;
    variables = JSON.stringify(historyItem.variables, null, 2);
    response = historyItem.result;
  }

  function saveQueryHistory() {
    try {
      localStorage.setItem(
        "graphql_explorer_history",
        JSON.stringify(queryHistory)
      );
    } catch (error) {
      console.error("Failed to save query history:", error);
    }
  }

  function loadQueryHistory() {
    try {
      const saved = localStorage.getItem("graphql_explorer_history");
      if (saved) {
        queryHistory = JSON.parse(saved);
      }
    } catch (error) {
      console.error("Failed to load query history:", error);
    }
  }

  function clearHistory() {
    queryHistory = [];
    saveQueryHistory();
    toast.success("Query history cleared");
  }

  function copyToClipboard(text) {
    navigator.clipboard
      .writeText(text)
      .then(() => {
        toast.success("Copied to clipboard");
      })
      .catch(() => {
        toast.error("Failed to copy to clipboard");
      });
  }

  function generateCodeExamples() {
    if (!query.trim()) return {};

    const vars = variables.trim() ? JSON.parse(variables) : {};
    return explorerService.generateCodeExamples(query, vars, authToken);
  }

  function formatJSON(jsonString) {
    try {
      const parsed = JSON.parse(jsonString);
      return JSON.stringify(parsed, null, 2);
    } catch {
      return jsonString;
    }
  }

  function handleKeyPress(event) {
    if (event.ctrlKey && event.key === "Enter") {
      event.preventDefault();
      executeQuery();
    }
  }

  // Reactive code examples
  $: codeExamples = showCodeExamples ? generateCodeExamples() : {};

  // Handle overlay state
  function openOverlay(type) {
    overlayType = type;
    overlayOpen = true;
    if (type === "schema" && !schema) {
      loadSchema();
    }
  }

  function closeOverlay() {
    overlayOpen = false;
    overlayType = null;
  }

  function toggleQueryCodeView() {
    showQueryEditor = !showQueryEditor;
    showCodeExamples = !showCodeExamples;
  }

  function toggleSidebar() {
    sidebarCollapsed = !sidebarCollapsed;
  }
</script>

<div
  class="graphql-explorer bg-tokyo-night-bg rounded-lg border border-tokyo-night-fg-gutter overflow-hidden"
>
  <!-- Header with controls -->
  <div
    class="flex items-center justify-between border-b border-tokyo-night-fg-gutter bg-tokyo-night-bg-light p-4"
  >
    <div class="flex space-x-4">
      <!-- Overlay triggers -->
      <button
        class="flex items-center space-x-2 px-3 py-1 rounded text-sm transition-colors text-tokyo-night-fg hover:bg-tokyo-night-bg-highlight"
        on:click={() => openOverlay("templates")}
      >
        <span>Templates</span>
      </button>
      <button
        class="flex items-center space-x-2 px-3 py-1 rounded text-sm transition-colors text-tokyo-night-fg hover:bg-tokyo-night-bg-highlight"
        on:click={() => openOverlay("schema")}
      >
        <span>Schema</span>
      </button>

      <!-- Query/Code toggle -->
      <div
        class="flex border border-tokyo-night-fg-gutter rounded overflow-hidden"
      >
        <button
          class="px-3 py-1 text-sm transition-colors {showQueryEditor
            ? 'bg-tokyo-night-blue text-white'
            : 'text-tokyo-night-fg hover:bg-tokyo-night-bg-highlight'}"
          on:click={() => {
            showQueryEditor = true;
            showCodeExamples = false;
          }}
        >
          Query
        </button>
        <button
          class="px-3 py-1 text-sm transition-colors {showCodeExamples
            ? 'bg-tokyo-night-blue text-white'
            : 'text-tokyo-night-fg hover:bg-tokyo-night-bg-highlight'}"
          on:click={() => {
            showQueryEditor = false;
            showCodeExamples = true;
          }}
        >
          Code
        </button>
      </div>

      {#if queryHistory.length > 0}
        <button
          class="flex items-center space-x-2 px-3 py-1 rounded text-sm text-tokyo-night-fg hover:bg-tokyo-night-bg-highlight transition-colors"
          on:click={clearHistory}
        >
          <span>Clear History</span>
        </button>
      {/if}
    </div>

    <div class="flex items-center space-x-3">
      <!-- Demo Mode Toggle -->
      <label class="flex items-center space-x-2 cursor-pointer group">
        <div class="relative">
          <input
            type="checkbox"
            bind:checked={demoMode}
            class="sr-only"
          />
          <div
            class="w-10 h-5 rounded-full transition-colors duration-200 {demoMode
              ? 'bg-tokyo-night-green'
              : 'bg-tokyo-night-fg-gutter'}"
          >
            <div
              class="w-4 h-4 bg-white rounded-full shadow-md transform transition-transform duration-200 translate-y-0.5 {demoMode
                ? 'translate-x-5'
                : 'translate-x-0.5'}"
            ></div>
          </div>
        </div>
        <span class="text-xs text-tokyo-night-fg group-hover:text-tokyo-night-green transition-colors">
          {demoMode ? 'Demo Mode' : 'Demo'}
        </span>
      </label>

      <button
        class="btn-primary flex items-center space-x-2"
        on:click={executeQuery}
        disabled={loading}
      >
        <span>{loading ? "Running..." : "Execute"}</span>
      </button>
    </div>
  </div>

  <!-- 2-Column Layout: Left (Query/Code+Variables) | Right (Response) -->
  <div class="flex min-h-[600px] relative">
    <!-- Left Panel - Query Editor or Code Examples -->
    <div
      class="flex-1 flex flex-col border-r border-tokyo-night-fg-gutter max-w-[60%]"
    >
      {#if showQueryEditor}
        <!-- Query Editor Section -->
        <div class="p-4 flex-1 flex flex-col">
          <div class="flex items-center justify-between mb-2">
            <h3 class="text-sm font-semibold text-tokyo-night-fg">
              GraphQL Query
            </h3>
            <span class="text-xs text-tokyo-night-comment"
              >Ctrl+Enter to execute</span
            >
          </div>
          <textarea
            bind:this={queryTextarea}
            bind:value={query}
            placeholder="Enter your GraphQL query here..."
            class="w-full flex-1 min-h-[300px] text-sm font-mono bg-tokyo-night-bg border border-tokyo-night-fg-gutter rounded p-3 text-tokyo-night-fg placeholder-tokyo-night-comment resize-none focus:border-tokyo-night-blue focus:ring-1 focus:ring-tokyo-night-blue"
            on:keydown={handleKeyPress}
          />
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
            on:keydown={handleKeyPress}
          />
        </div>
      {/if}

      {#if showCodeExamples}
        <!-- Code Examples Section -->
        <div class="p-4 flex-1 flex flex-col">
          <div class="flex items-center justify-between mb-3">
            <h3 class="text-sm font-semibold text-tokyo-night-fg">
              Code Examples
            </h3>
            <select
              bind:value={selectedLanguage}
              class="text-xs bg-tokyo-night-bg border border-tokyo-night-fg-gutter rounded px-2 py-1 text-tokyo-night-fg"
            >
              {#each languages as lang}
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
                on:click={() => copyToClipboard(codeExamples[selectedLanguage])}
              >
                Copy
              </button>
            </div>
          {/if}
        </div>
      {/if}
    </div>

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
                    2
                  )}{:else}{response.data}{/if}</pre>
              <button
                class="absolute top-2 right-2 text-xs bg-tokyo-night-bg-highlight border border-tokyo-night-fg-gutter rounded px-2 py-1 hover:bg-tokyo-night-bg-light transition-colors"
                on:click={() =>
                  copyToClipboard(
                    response.error
                      ? JSON.stringify(response.error, null, 2)
                      : response.data
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
  </div>
</div>

<!-- Overlay for Templates and Schema -->
{#if overlayOpen}
  <!-- svelte-ignore a11y-click-events-have-key-events a11y-no-static-element-interactions -->
  <div
    class="fixed inset-0 bg-black bg-opacity-50 flex z-50"
    role="dialog"
    on:click={closeOverlay}
  >
    <!-- svelte-ignore a11y-click-events-have-key-events a11y-no-static-element-interactions -->
    <div
      class="w-1/3 max-w-md bg-tokyo-night-bg-light border-r border-tokyo-night-fg-gutter h-full overflow-y-auto slide-in-left"
      role="dialog"
      on:click|stopPropagation
    >
      <div
        class="p-4 border-b border-tokyo-night-fg-gutter flex items-center justify-between"
      >
        <h3 class="text-lg font-semibold text-tokyo-night-fg capitalize">
          {overlayType}
        </h3>
        <button
          class="text-tokyo-night-fg hover:text-tokyo-night-red transition-colors"
          on:click={closeOverlay}
        >
          ✕
        </button>
      </div>

      <div class="p-4 max-h-[calc(100vh-80px)] overflow-y-auto">
        {#if overlayType === "templates" && queryTemplates}
          <h4 class="text-sm font-semibold text-tokyo-night-fg mb-3">
            Query Templates
          </h4>

          {#each ["queries", "mutations", "subscriptions"] as category}
            {#if queryTemplates[category]?.length > 0}
              <div class="mb-4">
                <h4
                  class="text-xs font-medium text-tokyo-night-comment uppercase tracking-wider mb-2"
                >
                  {category}
                </h4>
                <div class="space-y-1">
                  {#each queryTemplates[category] as template}
                    <button
                      class="w-full text-left p-3 text-xs rounded transition-all duration-200 hover:bg-tokyo-night-bg-highlight {selectedTemplate ===
                      template
                        ? 'bg-tokyo-night-bg-highlight border-l-2 border-tokyo-night-blue shadow-sm'
                        : 'border-l-2 border-transparent hover:border-tokyo-night-fg-gutter'}"
                      on:click={() => selectTemplate(template)}
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
                {#each queryHistory as item}
                  <button
                    class="w-full text-left p-2 text-xs rounded transition-colors hover:bg-tokyo-night-bg-highlight border border-transparent"
                    on:click={() => loadFromHistory(item)}
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

        {#if overlayType === "schema"}
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
                    {#each schema.queryType.fields as field}
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
                    {#each schema.mutationType.fields as field}
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
                    {#each schema.subscriptionType.fields as field}
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
              on:click={loadSchema}
            >
              Load Schema
            </button>
          {/if}
        {/if}
      </div>
    </div>
  </div>
{/if}

<style>
  .graphql-explorer {
    min-height: 600px;
  }

  .slide-in-left {
    animation: slideInLeft 0.3s ease-out;
  }

  @keyframes slideInLeft {
    from {
      transform: translateX(-100%);
    }
    to {
      transform: translateX(0);
    }
  }

  @media (max-width: 1024px) {
    .graphql-explorer {
      min-height: 800px;
    }
  }

  @media (max-width: 768px) {
    .graphql-explorer {
      flex-direction: column;
    }
  }
</style>

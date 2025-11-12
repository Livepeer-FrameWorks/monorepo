<script lang="ts">
  import { onMount } from "svelte";
  import { explorerService } from "$lib/graphql/services/explorer.js";
  import { toast } from "$lib/stores/toast.js";
  import ExplorerHeader from "$lib/components/explorer/ExplorerHeader.svelte";
  import QueryEditor from "$lib/components/explorer/QueryEditor.svelte";
  import CodeExamplesPanel from "$lib/components/explorer/CodeExamplesPanel.svelte";
  import ResponseViewer from "$lib/components/explorer/ResponseViewer.svelte";
  import ExplorerOverlay from "$lib/components/explorer/ExplorerOverlay.svelte";

  interface Props {
    initialQuery?: string;
    authToken?: string | null;
  }

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
    result: unknown;
    timestamp: string;
  }

  let { initialQuery = "", authToken = null }: Props = $props();

  // Component state
  let query =
    $state(initialQuery ||
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
}`);
  let variables = $state("{}");
  let response = $state(null);
  let loading = $state(false);
  let schema: null | {
    queryType?: { fields: Array<{ name: string; description?: string }> };
    mutationType?: { fields: Array<{ name: string; description?: string }> };
    subscriptionType?: { fields: Array<{ name: string; description?: string }> };
  } = $state(null);
  let queryTemplates: Record<string, QueryTemplate[]> | null = $state(null);
  let selectedTemplate: QueryTemplate | null = $state(null);
  let showCodeExamples = $state(false);
  let showQueryEditor = $state(true);
  let selectedLanguage = $state("javascript");
  let queryHistory = $state<QueryHistoryItem[]>([]);
  let demoMode = $state(false);

  // Overlay state
  let overlayOpen = $state(false);
  let overlayType = $state<"schema" | "templates" | null>(null); // 'schema' or 'templates'

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
        console.error("Invalid JSON in variables:", error);
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

  function selectTemplate(template: QueryTemplate) {
    selectedTemplate = template;
    query = template.query;
    variables = JSON.stringify(template.variables, null, 2);
    closeOverlay(); // Close overlay after selection
  }

  function addToHistory(
    queryText: string,
    vars: Record<string, unknown>,
    result: unknown,
  ) {
    const historyItem: QueryHistoryItem = {
      id: Date.now(),
      query: queryText,
      variables: vars,
      result: result,
      timestamp: new Date().toISOString(),
    };

    queryHistory = [historyItem, ...queryHistory].slice(0, 10); // Keep last 10
    saveQueryHistory();
  }

  function loadFromHistory(historyItem: QueryHistoryItem) {
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
        queryHistory = JSON.parse(saved) as QueryHistoryItem[];
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

  function copyToClipboard(text: string) {
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

  function handleKeyPress(event: KeyboardEvent) {
    if (event.ctrlKey && event.key === "Enter") {
      event.preventDefault();
      executeQuery();
    }
  }

  // Reactive code examples
  let codeExamples = $derived(showCodeExamples ? generateCodeExamples() : {});

  // Handle overlay state
  function openOverlay(type: "schema" | "templates") {
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

</script>

<div
  class="graphql-explorer bg-tokyo-night-bg rounded-lg border border-tokyo-night-fg-gutter overflow-hidden"
>
  <ExplorerHeader
    {showQueryEditor}
    {showCodeExamples}
    hasHistory={queryHistory.length > 0}
    {demoMode}
    {loading}
    onOpenOverlay={openOverlay}
    onToggleQuery={() => {
      showQueryEditor = true;
      showCodeExamples = false;
    }}
    onToggleCode={() => {
      showQueryEditor = false;
      showCodeExamples = true;
    }}
    onClearHistory={clearHistory}
    onExecute={executeQuery}
    onDemoModeChange={(checked) => (demoMode = checked)}
  />

  <!-- 2-Column Layout: Left (Query/Code+Variables) | Right (Response) -->
  <div class="flex min-h-[600px] relative">
    <!-- Left Panel - Query Editor or Code Examples -->
    <div
      class="flex-1 flex flex-col border-r border-tokyo-night-fg-gutter max-w-[60%]"
    >
      {#if showQueryEditor}
        <QueryEditor bind:query bind:variables onKeyPress={handleKeyPress} />
      {/if}

      {#if showCodeExamples}
        <CodeExamplesPanel
          {codeExamples}
          bind:selectedLanguage
          {languages}
          onCopy={copyToClipboard}
        />
      {/if}
    </div>

    <ResponseViewer {response} {loading} onCopy={copyToClipboard} />
  </div>
</div>

<ExplorerOverlay
  open={overlayOpen}
  type={overlayType}
  {schema}
  {queryTemplates}
  {queryHistory}
  {loading}
  {selectedTemplate}
  onClose={closeOverlay}
  onSelectTemplate={selectTemplate}
  onLoadFromHistory={loadFromHistory}
  onLoadSchema={loadSchema}
/>

<style>
  .graphql-explorer {
    min-height: 600px;
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

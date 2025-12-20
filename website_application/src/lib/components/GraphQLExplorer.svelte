<script lang="ts">
  import { explorerService, type Template, type TemplateGroups } from "$lib/graphql/services/explorer.js";
  import { extractOperationType, extractDescription, extractVariableDefinitions, extractFragmentSpreads } from "$lib/graphql/services/gqlParser.js";
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

  // Use the Template type from the loader
  type QueryTemplate = Template;

  interface QueryHistoryItem {
    id: number;
    query: string;
    variables: Record<string, unknown>;
    result: unknown;
    timestamp: string;
  }

  interface FormattedResponse {
    status: string;
    statusIcon: string;
    timestamp: string;
    duration: string;
    data: string | null;
    error: {
      message: string;
      graphQLErrors?: unknown;
      networkError?: unknown;
    } | null;
  }

  let { initialQuery = "", authToken = null }: Props = $props();

  // Component state
  let query =
    $state(initialQuery ||
    `query GetStreamsConnection {
  streamsConnection(first: 10) {
    edges {
      node {
        id
        name
        description
        streamKey
        playbackId
        record
        createdAt
        updatedAt
        metrics {
          status
          isLive
          currentViewers
        }
      }
    }
    pageInfo {
      hasNextPage
      endCursor
    }
    totalCount
  }
}`);
  let variables = $state("{}");
  let response = $state<FormattedResponse | null>(null);
  let loading = $state(false);
  let schema: null | {
    queryType?: { fields: Array<{ name: string; description?: string }> };
    mutationType?: { fields: Array<{ name: string; description?: string }> };
    subscriptionType?: { fields: Array<{ name: string; description?: string }> };
  } = $state(null);
  let queryTemplates: TemplateGroups | null = $state(null);
  let selectedTemplate: QueryTemplate | null = $state(null);
  let showCodeExamples = $state(false);
  let showQueryEditor = $state(true);
  let selectedLanguage = $state("javascript");
  let queryHistory = $state<QueryHistoryItem[]>([]);
  let demoMode = $state(false);

  // Library drawer state
  let libraryOpen = $state(false);

  const languages = [
    { key: "javascript", name: "JavaScript (Apollo)" },
    { key: "fetch", name: "JavaScript (Fetch)" },
    { key: "curl", name: "cURL" },
    { key: "python", name: "Python" },
    { key: "go", name: "Go" },
  ];

  // Initialize on mount
  $effect(() => {
    loadQueryTemplates();
    loadQueryHistory();
    // Load schema for CodeMirror autocomplete
    loadSchema();
  });

  async function loadQueryTemplates() {
    try {
      queryTemplates = await explorerService.getTemplates();
    } catch (error) {
      console.error("Failed to load query templates:", error);
      toast.error("Failed to load query templates");
    }
  }

  async function loadSchema() {
    if (schema) return; // Already loaded

    try {
      loading = true;
      schema = (await explorerService.getSchema()) as any;
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

    // Determine operation type (handles comment-prefixed queries)
    const operationType = extractOperationType(query);

    try {
      loading = true;
      const result = await explorerService.executeQuery(
        query,
        parsedVariables,
        operationType,
        demoMode
      );
      response = explorerService.formatResponse(result) as any;

      // Add to history
      addToHistory(query, parsedVariables, response);

      if (result.error) {
        toast.error(
          "Query execution failed - check the response panel for details"
        );
      } else if (response) {
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
    response = historyItem.result as any;
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

  // Query help metadata for the Help popover
  let queryHelp = $derived({
    description: extractDescription(query),
    variables: extractVariableDefinitions(query),
    fragments: extractFragmentSpreads(query),
  });

  // Handle library drawer state
  function openLibrary() {
    libraryOpen = true;
  }

  function closeLibrary() {
    libraryOpen = false;
  }

  // Generate query from schema field selection
  interface SchemaField {
    name: string;
    description?: string;
    args?: Array<{
      name: string;
      description?: string;
      type?: { name?: string; kind?: string; ofType?: { name?: string } };
    }>;
    type?: { name?: string; kind?: string; ofType?: { name?: string } };
  }

  function handleSelectSchemaField(field: SchemaField, operationType: string) {
    const generatedQuery = explorerService.generateQueryFromField(field, operationType);
    query = generatedQuery.query;
    variables = JSON.stringify(generatedQuery.variables, null, 2);
  }

</script>

<div
  class="graphql-explorer h-full flex flex-col border border-border/50 overflow-hidden"
>
  <ExplorerHeader
    {showQueryEditor}
    {showCodeExamples}
    hasHistory={queryHistory.length > 0}
    {demoMode}
    {loading}
    {queryHelp}
    onOpenLibrary={openLibrary}
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
  <div class="flex flex-1 min-h-0 relative">
    <!-- Left Panel - Query Editor or Code Examples -->
    <div
      class="flex-1 flex flex-col border-r border-border max-w-[60%] min-w-0 overflow-hidden"
    >
      {#if showQueryEditor}
        <QueryEditor bind:query bind:variables {schema} onKeyPress={handleKeyPress} />
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
  open={libraryOpen}
  {schema}
  {queryTemplates}
  queryHistory={queryHistory as any}
  {loading}
  onClose={closeLibrary}
  onSelectTemplate={selectTemplate}
  onLoadFromHistory={loadFromHistory}
  onSelectSchemaField={handleSelectSchemaField}
  onLoadSchema={loadSchema}
/>

<style>
  @media (max-width: 768px) {
    .graphql-explorer {
      flex-direction: column;
    }
  }
</style>

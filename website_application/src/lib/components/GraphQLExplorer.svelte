<script lang="ts">
  import {
    explorerService,
    type Template,
    type TemplateGroups,
    type ResolvedExplorerSection,
    type ResolvedExplorerExample,
  } from "$lib/graphql/services/explorer.js";
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

  const DEFAULT_QUERY = `query GetStreamsConnection {
  streamsConnection(page: { first: 10 }) {
    edges {
      node {
        id
        streamId
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
}`;

  // Component state
  let query = $state(DEFAULT_QUERY);

  // Sync from prop when provided
  $effect.pre(() => {
    if (initialQuery) {
      query = initialQuery;
    }
  });
  let variables = $state("{}");
  let response = $state<FormattedResponse | null>(null);
  let loading = $state(false);
  let schemaLoading = $state(false);
  type SchemaField = {
    name: string;
    description?: string;
    args?: Array<{
      name: string;
      description?: string;
      type?: { name?: string; kind?: string; ofType?: { name?: string } };
    }>;
    type?: { name?: string; kind?: string; ofType?: { name?: string } };
    isDeprecated?: boolean;
    deprecationReason?: string;
  };

  let schema: null | {
    queryType?: { fields: SchemaField[] };
    mutationType?: { fields: SchemaField[] };
    subscriptionType?: { fields: SchemaField[] };
    types?: Array<{
      name?: string;
      kind?: string;
      description?: string;
      fields?: SchemaField[];
      inputFields?: SchemaField[];
      enumValues?: Array<{
        name?: string;
        description?: string;
        isDeprecated?: boolean;
        deprecationReason?: string;
      }>;
    }>;
  } = $state(null);
  let queryTemplates: TemplateGroups | null = $state(null);
  let catalogSections = $state<ResolvedExplorerSection[] | null>(null);
  let selectedTemplate: QueryTemplate | null = $state(null);
  let showCodeExamples = $state(false);
  let showQueryEditor = $state(true);
  let selectedLanguage = $state("javascript");
  let queryHistory = $state<QueryHistoryItem[]>([]);
  let demoMode = $state(false);

  type FocusDoc = {
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

  let activeDoc = $state<FocusDoc>(null);

  // Panel state: which panel is active ('templates', 'schema', or null for closed)
  type PanelView = 'templates' | 'schema' | null;
  let activePanel = $state<PanelView>(null);

  // For responsive behavior - default to desktop (sidebar visible)
  let isMobile = $state(false);

  $effect(() => {
    const handleResize = () => {
      isMobile = window.innerWidth < 1024;
    };
    handleResize();
    window.addEventListener('resize', handleResize);
    return () => window.removeEventListener('resize', handleResize);
  });

  function handlePanelChange(panel: PanelView) {
    activePanel = panel;
  }

  const languages = [
    { key: "javascript", name: "JavaScript (Apollo)" },
    { key: "fetch", name: "JavaScript (Fetch)" },
    { key: "curl", name: "cURL" },
    { key: "python", name: "Python" },
    { key: "go", name: "Go" },
  ];

  // Initialize on mount - use a flag to ensure we only run once
  let hasInitialized = false;

  $effect(() => {
    if (hasInitialized) return;
    hasInitialized = true;

    loadQueryTemplates();
    loadQueryHistory();
    // Load schema in background for CodeMirror autocomplete (doesn't block UI)
    loadSchema();
  });

  async function loadQueryTemplates() {
    try {
      queryTemplates = await explorerService.getTemplates();
      catalogSections = await explorerService.getCatalog();
    } catch (error) {
      console.error("Failed to load query templates:", error);
      toast.error("Failed to load query templates");
    }
  }

  async function loadSchema() {
    if (schema || schemaLoading) {
      return;
    }

    try {
      schemaLoading = true;
      // Use cached schema when available to avoid duplicate network requests on navigation
      schema = (await explorerService.getCachedSchema()) as any;
    } catch (error) {
      console.error("Failed to load schema:", error);
      toast.error("Failed to load GraphQL schema");
    } finally {
      schemaLoading = false;
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
    tips: explorerService.getQueryHints(query),
  });

  // Generate query from schema field selection

  function handleSelectSchemaField(field: SchemaField, operationType: string) {
    const generatedQuery = explorerService.generateQueryFromField(field, operationType);
    query = generatedQuery.query;
    variables = JSON.stringify(generatedQuery.variables, null, 2);
  }

  function extractRootFieldNames(queryText: string): string[] {
    const withoutComments = queryText
      .split("\n")
      .filter((line) => !line.trim().startsWith("#"))
      .join("\n");
    const start = withoutComments.indexOf("{");
    if (start === -1) return [];

    const fields = new Set<string>();
    let depth = 0;
    let i = start;
    let inString = false;
    let inBlockString = false;

    while (i < withoutComments.length) {
      const ch = withoutComments[i];
      const next = withoutComments[i + 1];
      const next2 = withoutComments[i + 2];

      if (!inString && ch === "\"" && next === "\"" && next2 === "\"") {
        inBlockString = !inBlockString;
        i += 3;
        continue;
      }
      if (inBlockString) {
        i += 1;
        continue;
      }
      if (ch === "\"" && !inString) {
        inString = true;
        i += 1;
        continue;
      }
      if (ch === "\"" && inString) {
        inString = false;
        i += 1;
        continue;
      }
      if (inString) {
        i += 1;
        continue;
      }

      if (ch === "{") {
        depth += 1;
        i += 1;
        continue;
      }
      if (ch === "}") {
        depth -= 1;
        if (depth <= 0) break;
        i += 1;
        continue;
      }

      if (depth === 1) {
        if (ch === "." && next === "." && next2 === ".") {
          i += 3;
          continue;
        }

        if (/[A-Za-z_]/.test(ch)) {
          let j = i + 1;
          while (j < withoutComments.length && /[A-Za-z0-9_]/.test(withoutComments[j])) {
            j += 1;
          }
          const name = withoutComments.slice(i, j);
          if (name !== "on") {
            fields.add(name);
          }
          i = j;
          continue;
        }
      }

      i += 1;
    }

    return Array.from(fields);
  }

  function getSchemaFieldsForOperation(operationType: string): SchemaField[] {
    if (!schema) return [];
    if (operationType === "mutation") return schema.mutationType?.fields ?? [];
    if (operationType === "subscription") return schema.subscriptionType?.fields ?? [];
    return schema.queryType?.fields ?? [];
  }

  let rootFieldHints = $derived.by(() => {
    if (!schema) return [];
    const operationType = extractOperationType(query);
    const rootFields = extractRootFieldNames(query);
    if (!rootFields.length) return [];

    const schemaFields = getSchemaFieldsForOperation(operationType);
    const hintMap = new Map(schemaFields.map((field) => [field.name, field]));
    return rootFields
      .map((name) => hintMap.get(name))
      .filter((field): field is SchemaField => !!field);
  });

  function handleSelectExample(example: ResolvedExplorerExample) {
    if (example.template) {
      const vars = example.variables ?? example.template.variables;
      selectedTemplate = example.template;
      query = example.template.query;
      variables = JSON.stringify(vars, null, 2);
      return;
    }
    if (example.query) {
      query = example.query;
      variables = JSON.stringify(example.variables ?? {}, null, 2);
    }
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
    {activePanel}
    onPanelChange={handlePanelChange}
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

  <!-- 3-Panel Layout: Sidebar | Query Editor | Response -->
  <div class="flex flex-1 min-h-0 relative">
    <!-- Left Sidebar - Templates or Schema (inline on desktop, modal on mobile) -->
    <ExplorerOverlay
      view={activePanel}
      sidebarMode={!isMobile}
      {schema}
      {queryTemplates}
      {catalogSections}
      queryHistory={queryHistory as any}
      loading={schemaLoading}
      onClose={() => handlePanelChange(null)}
      onSelectTemplate={selectTemplate}
      onSelectExample={handleSelectExample}
      onLoadFromHistory={loadFromHistory}
      onSelectSchemaField={handleSelectSchemaField}
      onLoadSchema={loadSchema}
    />

    <!-- Middle Panel - Query Editor or Code Examples -->
    <div class="flex-1 flex flex-col border-r border-border min-w-0 overflow-y-auto">
      {#if showQueryEditor}
        <QueryEditor
          bind:query
          bind:variables
          {schema}
          onKeyPress={handleKeyPress}
          onCursorInfo={(info) => (activeDoc = info as FocusDoc)}
        />
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

    <!-- Right Panel - Response -->
    <ResponseViewer
      {response}
      {loading}
      fieldDocs={rootFieldHints}
      focusDoc={activeDoc}
      onCopy={copyToClipboard}
    />
  </div>
</div>

<style>
  @media (max-width: 768px) {
    .graphql-explorer {
      flex-direction: column;
    }
  }
</style>

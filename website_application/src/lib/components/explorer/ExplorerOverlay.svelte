<script lang="ts">
  import { SvelteMap, SvelteSet } from "svelte/reactivity";
  import { fade, fly } from "svelte/transition";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import {
    ChevronRight,
    ChevronDown,
    ArrowLeft,
    Play,
    X,
    Clock,
    BookOpen,
    Database,
    Search,
    FileCode,
    AlertTriangle,
  } from "lucide-svelte";
  import type {
    Template,
    TemplateGroups,
    ResolvedExplorerSection,
    ResolvedExplorerExample,
  } from "$lib/graphql/services/explorer";
  import {
    formatTypeString,
    getBaseTypeName,
    isScalarType,
    getObjectTypeFields,
    type IntrospectedSchema,
  } from "$lib/graphql/services/schemaUtils";

  interface TypeRef {
    name?: string;
    kind?: string;
    ofType?: TypeRef;
  }

  interface SchemaField {
    name: string;
    description?: string;
    args?: Array<{
      name: string;
      description?: string;
      type?: TypeRef;
    }>;
    type?: TypeRef;
    isDeprecated?: boolean;
    deprecationReason?: string;
  }

  interface SchemaType {
    fields: SchemaField[];
  }

  type Schema = IntrospectedSchema & {
    queryType?: SchemaType;
    mutationType?: SchemaType;
    subscriptionType?: SchemaType;
  };

  interface QueryHistoryItem {
    id: number;
    query: string;
    variables: Record<string, unknown>;
    result: { statusIcon: string; [key: string]: unknown };
    timestamp: string;
  }

  type PanelView = "templates" | "schema" | null;

  interface Props {
    view: PanelView;
    schema: Schema | null;
    queryTemplates: TemplateGroups | null;
    catalogSections?: ResolvedExplorerSection[] | null;
    queryHistory: QueryHistoryItem[];
    loading: boolean;
    sidebarMode?: boolean; // When true, renders as inline sidebar instead of modal
    onClose: () => void;
    onSelectTemplate: (template: Template) => void;
    onSelectExample: (example: ResolvedExplorerExample) => void;
    onLoadFromHistory: (item: QueryHistoryItem) => void;
    onSelectSchemaField?: (field: SchemaField, operationType: string) => void;
    onLoadSchema: () => void;
  }

  let {
    view,
    schema,
    queryTemplates,
    catalogSections = null,
    queryHistory,
    loading,
    sidebarMode = false,
    onClose,
    onSelectTemplate,
    onSelectExample,
    onLoadFromHistory,
    onSelectSchemaField,
    onLoadSchema,
  }: Props = $props();

  // Navigation state for schema exploration
  interface NavItem {
    kind: "root" | "type" | "field";
    name: string;
    parentType?: string; // For fields, the type they belong to
  }
  let navigationStack = $state<NavItem[]>([{ kind: "root", name: "Schema" }]);
  let currentNav = $derived(navigationStack[navigationStack.length - 1]);

  // Track expanded field for detail view (legacy, will be replaced by navigation)
  let expandedField = $state<string | null>(null);

  // Template search state
  let templateSearch = $state("");

  // Guides filter state
  let showAdvancedGuides = $state(false);

  // Library view (curated vs all)
  let libraryTab = $state<"curated" | "all">("curated");

  // Schema-wide search (searches across all operations and types)
  let schemaSearch = $state("");

  // Schema section collapse state (all expanded by default)
  let collapsedSections: SvelteSet<string> = new SvelteSet();
  function toggleSection(section: string) {
    const newSet = new SvelteSet(collapsedSections);
    if (newSet.has(section)) {
      newSet.delete(section);
    } else {
      newSet.add(section);
    }
    collapsedSections = newSet;
  }

  // Filter templates based on search
  const filteredTemplates = $derived.by(() => {
    if (!queryTemplates) return null;
    if (!templateSearch.trim()) return queryTemplates;

    const search = templateSearch.toLowerCase();
    return {
      queries: queryTemplates.queries.filter(
        (t) => t.name.toLowerCase().includes(search) || t.description.toLowerCase().includes(search)
      ),
      mutations: queryTemplates.mutations.filter(
        (t) => t.name.toLowerCase().includes(search) || t.description.toLowerCase().includes(search)
      ),
      subscriptions: queryTemplates.subscriptions.filter(
        (t) => t.name.toLowerCase().includes(search) || t.description.toLowerCase().includes(search)
      ),
      fragments: queryTemplates.fragments.filter(
        (t) => t.name.toLowerCase().includes(search) || t.description.toLowerCase().includes(search)
      ),
    };
  });

  // Filter catalog sections based on core/advanced tags
  const filteredCatalogSections = $derived.by(() => {
    if (!catalogSections) return null;
    if (showAdvancedGuides) return catalogSections;

    return catalogSections
      .map((section) => ({
        ...section,
        examples: section.examples.filter((example) => example.tags?.includes("core")),
      }))
      .filter((section) => section.examples.length > 0);
  });

  // Helper to check if a field matches the search
  function fieldMatchesSearch(field: SchemaField, search: string): boolean {
    if (field.name?.toLowerCase().includes(search)) return true;
    if (field.description?.toLowerCase().includes(search)) return true;
    if (
      field.args?.some(
        (arg) =>
          arg.name?.toLowerCase().includes(search) ||
          arg.description?.toLowerCase().includes(search)
      )
    )
      return true;
    return false;
  }

  // Filtered queries
  const filteredQueries = $derived.by(() => {
    if (!schema?.queryType?.fields) return [];
    const search = schemaSearch.trim().toLowerCase();
    if (!search) return schema.queryType.fields;
    return schema.queryType.fields.filter((f) => fieldMatchesSearch(f, search));
  });

  // Filtered mutations
  const filteredMutations = $derived.by(() => {
    if (!schema?.mutationType?.fields) return [];
    const search = schemaSearch.trim().toLowerCase();
    if (!search) return schema.mutationType.fields;
    return schema.mutationType.fields.filter((f) => fieldMatchesSearch(f, search));
  });

  // Filtered subscriptions
  const filteredSubscriptions = $derived.by(() => {
    if (!schema?.subscriptionType?.fields) return [];
    const search = schemaSearch.trim().toLowerCase();
    if (!search) return schema.subscriptionType.fields;
    return schema.subscriptionType.fields.filter((f) => fieldMatchesSearch(f, search));
  });

  // Filtered types (deep search)
  const filteredTypes = $derived.by(() => {
    if (!schema?.types) return [];
    const search = schemaSearch.trim().toLowerCase();
    const builtins = new Set([
      "String",
      "Int",
      "Float",
      "Boolean",
      "ID",
      "Time",
      "DateTime",
      "JSON",
    ]);

    return schema.types
      .filter((type) => {
        const name = type.name || "";
        if (!name || name.startsWith("__")) return false;
        if (builtins.has(name)) return false;
        if (!["OBJECT", "INPUT_OBJECT", "ENUM", "SCALAR"].includes(type.kind || "")) {
          return false;
        }
        if (!search) return true;

        // Deep search: type name, description, field names, field descriptions
        if (name.toLowerCase().includes(search)) return true;
        if (type.description?.toLowerCase().includes(search)) return true;

        // Search in fields
        if (
          type.fields?.some(
            (f) =>
              f.name?.toLowerCase().includes(search) ||
              f.description?.toLowerCase().includes(search)
          )
        )
          return true;

        // Search in input fields
        if (
          type.inputFields?.some(
            (f) =>
              f.name?.toLowerCase().includes(search) ||
              f.description?.toLowerCase().includes(search)
          )
        )
          return true;

        // Search in enum values
        if (
          type.enumValues?.some(
            (e) =>
              e.name?.toLowerCase().includes(search) ||
              e.description?.toLowerCase().includes(search)
          )
        )
          return true;

        return false;
      })
      .sort((a, b) => (a.name || "").localeCompare(b.name || ""));
  });

  // Total types count (for display when filtering)
  const totalTypesCount = $derived.by(() => {
    if (!schema?.types) return 0;
    const builtins = new Set([
      "String",
      "Int",
      "Float",
      "Boolean",
      "ID",
      "Time",
      "DateTime",
      "JSON",
    ]);
    return schema.types.filter(
      (t) =>
        t.name &&
        !t.name.startsWith("__") &&
        !builtins.has(t.name) &&
        ["OBJECT", "INPUT_OBJECT", "ENUM", "SCALAR"].includes(t.kind || "")
    ).length;
  });

  // Build "used-by" index: maps type name to list of fields that return it
  interface UsedByRef {
    fieldName: string;
    parentType: string;
    operationType?: "Query" | "Mutation" | "Subscription";
  }

  // Lazy type usage index - only computed when needed and cached
  let cachedTypeUsageIndex: SvelteMap<string, UsedByRef[]> | null = null;
  let cachedSchemaRef: typeof schema = null;

  function buildTypeUsageIndex(): Map<string, UsedByRef[]> {
    // Return cached if schema hasn't changed
    if (cachedTypeUsageIndex && cachedSchemaRef === schema) {
      return cachedTypeUsageIndex;
    }

    const index = new SvelteMap<string, UsedByRef[]>();
    if (!schema) return index;

    const addUsage = (typeName: string, ref: UsedByRef) => {
      if (!typeName || isScalarType(typeName)) return;
      const existing = index.get(typeName) || [];
      existing.push(ref);
      index.set(typeName, existing);
    };

    const scanFields = (
      fields: SchemaField[] | undefined,
      parentType: string,
      operationType?: "Query" | "Mutation" | "Subscription"
    ) => {
      fields?.forEach((field) => {
        const returnType = getBaseTypeName(field.type);
        addUsage(returnType, { fieldName: field.name, parentType, operationType });
        field.args?.forEach((arg) => {
          const argType = getBaseTypeName(arg.type);
          addUsage(argType, { fieldName: `${field.name}(${arg.name})`, parentType, operationType });
        });
      });
    };

    scanFields(schema.queryType?.fields, "Query", "Query");
    scanFields(schema.mutationType?.fields, "Mutation", "Mutation");
    scanFields(schema.subscriptionType?.fields, "Subscription", "Subscription");

    schema.types?.forEach((type) => {
      if (!type.name || type.name.startsWith("__")) return;
      if (type.kind === "OBJECT" || type.kind === "INPUT_OBJECT") {
        const fields = type.kind === "OBJECT" ? type.fields : type.inputFields;
        fields?.forEach((field) => {
          const returnType = getBaseTypeName(field.type);
          addUsage(returnType, { fieldName: field.name, parentType: type.name! });
        });
      }
    });

    cachedTypeUsageIndex = index;
    cachedSchemaRef = schema;
    return index;
  }

  // Get usages for a given type - lazy computation
  function getTypeUsages(typeName: string): UsedByRef[] {
    // Only compute when actually navigated into a type
    if (currentNav.kind !== "type") return [];
    return buildTypeUsageIndex().get(typeName) || [];
  }

  function toggleFieldExpanded(fieldName: string) {
    expandedField = expandedField === fieldName ? null : fieldName;
  }

  // Navigation functions for schema exploration
  function navigateToType(typeName: string) {
    // Don't navigate to scalars
    if (isScalarType(typeName)) return;
    navigationStack = [...navigationStack, { kind: "type", name: typeName }];
  }

  function _navigateToField(fieldName: string, parentType: string) {
    navigationStack = [...navigationStack, { kind: "field", name: fieldName, parentType }];
  }

  function navigateBack() {
    if (navigationStack.length > 1) {
      navigationStack = navigationStack.slice(0, -1);
    }
  }

  function navigateToBreadcrumb(index: number) {
    if (index < navigationStack.length - 1) {
      navigationStack = navigationStack.slice(0, index + 1);
    }
  }

  function _navigateToRoot() {
    navigationStack = [{ kind: "root", name: "Schema" }];
  }

  // Get type info by name
  function getTypeByName(typeName: string) {
    return schema?.types?.find((t) => t.name === typeName);
  }

  function handleSelectField(field: SchemaField, operationType: string) {
    onSelectSchemaField?.(field, operationType);
    onClose();
  }

  function handleSelectTemplate(template: Template) {
    onSelectTemplate(template);
    onClose();
  }

  function handleSelectExample(example: ResolvedExplorerExample) {
    onSelectExample(example);
    onClose();
  }

  function handleLoadFromHistory(item: QueryHistoryItem) {
    onLoadFromHistory(item);
    onClose();
  }

  // Use the schema utils for proper type formatting
  function getTypeName(type: TypeRef | undefined): string {
    return formatTypeString(type);
  }

  // Get the base type for linking/expansion
  function getTypeBaseName(type: TypeRef | undefined): string {
    return getBaseTypeName(type);
  }

  // Check if type is a scalar (no expandable fields)
  function isTypeScalar(type: TypeRef | undefined): boolean {
    const baseName = getBaseTypeName(type);
    return isScalarType(baseName);
  }

  function getReturnTypeFields(field: SchemaField): SchemaField[] {
    if (!schema || !field.type) return [];
    if (isTypeScalar(field.type)) return [];

    const baseName = getTypeBaseName(field.type);
    if (!baseName) return [];

    return getObjectTypeFields(schema, baseName) || [];
  }

  function stopPropagation(event: Event) {
    event.stopPropagation();
  }

  // Load schema when schema panel opens
  $effect(() => {
    if (view === "schema" && !schema && !loading) {
      onLoadSchema();
    }
  });
</script>

<!-- Shared Panel Content Snippet -->
{#snippet panelContent()}
  <!-- Templates View -->
  {#if view === "templates"}
    <!-- Tab switcher -->
    <div class="px-4 py-2 border-b border-border/30">
      <div class="flex items-center gap-2">
        <button
          class="px-2 py-1 text-[11px] border border-border/50 transition-colors {libraryTab ===
          'curated'
            ? 'bg-primary text-primary-foreground border-primary'
            : 'text-muted-foreground hover:text-foreground hover:bg-muted/30'}"
          onclick={() => (libraryTab = "curated")}
        >
          Curated
        </button>
        <button
          class="px-2 py-1 text-[11px] border border-border/50 transition-colors {libraryTab ===
          'all'
            ? 'bg-primary text-primary-foreground border-primary'
            : 'text-muted-foreground hover:text-foreground hover:bg-muted/30'}"
          onclick={() => (libraryTab = "all")}
        >
          All Templates
        </button>
      </div>
    </div>

    {#if libraryTab === "curated"}
      <div
        class="px-4 py-2 flex items-center justify-between text-[11px] text-muted-foreground border-t border-border/30"
      >
        <span>
          {showAdvancedGuides ? "Showing core + advanced" : "Showing core only"}
        </span>
        <button
          class="text-primary hover:text-primary/80 transition-colors"
          onclick={() => (showAdvancedGuides = !showAdvancedGuides)}
        >
          {showAdvancedGuides ? "Hide advanced" : "Show advanced"}
        </button>
      </div>
      {#if filteredCatalogSections && filteredCatalogSections.length > 0}
        <div class="border-t border-border/30">
          {#each filteredCatalogSections as section (section.id)}
            <div class="px-4 py-3 border-b border-border/20">
              <div class="flex items-center justify-between">
                <div>
                  <p class="text-xs font-semibold text-foreground">{section.title}</p>
                  <p class="text-[11px] text-muted-foreground">{section.description}</p>
                </div>
              </div>
              <div class="mt-2 space-y-2">
                {#each section.examples as example (example.id)}
                  <button
                    class="w-full text-left p-2 border border-border/40 hover:border-border/80 hover:bg-muted/20 transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
                    disabled={!example.template && !example.query}
                    onclick={() => handleSelectExample(example)}
                  >
                    <div class="flex items-start justify-between gap-3">
                      <div>
                        <p class="text-xs font-medium text-foreground">{example.title}</p>
                        <p class="text-[11px] text-muted-foreground">{example.description}</p>
                        {#if example.expectedPayload}
                          <p class="text-[10px] text-muted-foreground mt-1">
                            Payload: {example.expectedPayload}
                          </p>
                        {/if}
                      </div>
                      <span class="text-[10px] uppercase tracking-wide text-muted-foreground">
                        {example.operationType}
                      </span>
                    </div>
                  </button>
                {/each}
              </div>
            </div>
          {/each}
        </div>
      {:else}
        <div class="px-4 py-6 text-center text-muted-foreground text-sm">
          No curated guides available.
        </div>
      {/if}
    {:else if queryTemplates}
      <div class="p-3 border-b border-border/30">
        <div class="relative">
          <Search class="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-muted-foreground" />
          <Input
            type="text"
            placeholder="Search templates..."
            class="pl-9 h-8 text-sm"
            bind:value={templateSearch}
          />
        </div>
      </div>

      {#each [{ key: "queries", label: "Queries", color: "text-success", bg: "bg-success/5" }, { key: "mutations", label: "Mutations", color: "text-primary", bg: "bg-primary/5" }, { key: "subscriptions", label: "Subscriptions", color: "text-accent-purple", bg: "bg-accent-purple/5" }, { key: "fragments", label: "Fragments", color: "text-warning", bg: "bg-warning/5" }] as category (category.key)}
        {#if filteredTemplates?.[category.key as keyof TemplateGroups]?.length}
          <div class="border-t border-border/30">
            <div
              class="px-4 py-2 text-xs font-medium {category.color} uppercase tracking-wider {category.bg}"
            >
              {category.label}
              <span class="text-muted-foreground ml-1">
                ({filteredTemplates[category.key as keyof TemplateGroups].length})
              </span>
            </div>
            <div class="divide-y divide-border/20">
              {#each filteredTemplates[category.key as keyof TemplateGroups] as template (template.name)}
                <button
                  class="w-full text-left px-4 py-3 hover:bg-muted/30 transition-colors group"
                  onclick={() => handleSelectTemplate(template)}
                >
                  <div class="flex items-start justify-between gap-2">
                    <div class="flex-1 min-w-0">
                      <div class="text-sm font-medium text-foreground mb-1">
                        {template.name}
                      </div>
                      <div class="text-xs text-muted-foreground leading-relaxed">
                        {template.description}
                      </div>
                    </div>
                    <Play
                      class="w-3 h-3 text-muted-foreground opacity-0 group-hover:opacity-100 transition-opacity mt-1 flex-shrink-0"
                    />
                  </div>
                  {#if template.filePath}
                    <div class="flex items-center gap-1 mt-2 text-xs text-muted-foreground/60">
                      <FileCode class="w-3 h-3" />
                      <span class="truncate">{template.filePath}</span>
                    </div>
                  {/if}
                </button>
              {/each}
            </div>
          </div>
        {/if}
      {/each}

      {#if templateSearch && !filteredTemplates?.queries.length && !filteredTemplates?.mutations.length && !filteredTemplates?.subscriptions.length && !filteredTemplates?.fragments.length}
        <div class="px-4 py-6 text-center text-muted-foreground text-sm">
          No templates match "{templateSearch}"
        </div>
      {/if}
    {:else}
      <div class="px-4 py-6 text-center text-muted-foreground text-sm">Loading templates...</div>
    {/if}

    <!-- History in Templates view -->
    {#if queryHistory.length > 0}
      <div class="border-t border-border/30 mt-4">
        <div
          class="px-4 py-2 text-xs font-medium text-muted-foreground uppercase tracking-wider bg-muted/30 flex items-center gap-2"
        >
          <Clock class="w-3 h-3" />
          Recent History
          <span class="text-muted-foreground">({queryHistory.length})</span>
        </div>
        <div class="divide-y divide-border/20">
          {#each queryHistory.slice(0, 5) as item (item.id)}
            <button
              class="w-full text-left px-4 py-2 hover:bg-muted/30 transition-colors"
              onclick={() => handleLoadFromHistory(item)}
            >
              <div class="text-xs font-mono text-foreground truncate">
                {item.query
                  .split("\n")[0]
                  .replace(/^(query|mutation|subscription)\s+\w*\s*\{?/, "")
                  .trim() || "Query"}
              </div>
              <div class="text-[10px] text-muted-foreground mt-0.5">
                {new Date(item.timestamp).toLocaleTimeString()}
                <span class="ml-1">{item.result.statusIcon === "success" ? "✓" : "✗"}</span>
              </div>
            </button>
          {/each}
        </div>
      </div>
    {/if}

    <!-- Schema View -->
  {:else if view === "schema"}
    {#if loading}
      <div class="px-4 py-6 text-center text-muted-foreground text-sm">Loading schema...</div>
    {:else if schema}
      <!-- Breadcrumb Navigation (shows when not at root) -->
      {#if navigationStack.length > 1}
        <div
          class="px-3 py-2 border-t border-border/30 bg-muted/30 flex flex-wrap items-center gap-1 text-xs"
        >
          <button
            class="p-1 hover:bg-muted rounded transition-colors"
            onclick={navigateBack}
            title="Go back"
          >
            <ArrowLeft class="w-3.5 h-3.5" />
          </button>
          {#each navigationStack as nav, i (i)}
            {#if i > 0}
              <ChevronRight class="w-3 h-3 text-muted-foreground" />
            {/if}
            <button
              class="px-1.5 py-0.5 hover:bg-muted rounded transition-colors font-mono {i ===
              navigationStack.length - 1
                ? 'text-primary font-medium'
                : 'text-muted-foreground'}"
              onclick={() => navigateToBreadcrumb(i)}
            >
              {nav.name}
            </button>
          {/each}
        </div>
      {/if}

      <!-- Type Detail View (when navigated into a type) -->
      {#if currentNav.kind === "type"}
        {@const selectedType = getTypeByName(currentNav.name)}
        {#if selectedType}
          <div class="border-t border-border/30">
            <!-- Type Header -->
            <div class="px-4 py-3 bg-muted/20">
              <div class="flex items-center gap-2 mb-1">
                <span
                  class="text-[10px] uppercase tracking-wide text-muted-foreground px-1.5 py-0.5 bg-muted border border-border/40"
                >
                  {selectedType.kind}
                </span>
                <span class="font-mono font-semibold text-foreground">{selectedType.name}</span>
              </div>
              {#if selectedType.description}
                <p class="text-xs text-muted-foreground mt-1">{selectedType.description}</p>
              {/if}
            </div>

            <!-- Enum Values -->
            {#if selectedType.kind === "ENUM" && selectedType.enumValues?.length}
              <div class="px-4 py-3 border-t border-border/20">
                <div class="text-xs font-medium text-muted-foreground mb-2">Enum Values</div>
                <div class="space-y-1">
                  {#each selectedType.enumValues as enumVal (enumVal.name)}
                    <div class="text-xs pl-2 border-l-2 border-border/30">
                      <span class="font-mono text-foreground">{enumVal.name}</span>
                      {#if enumVal.description}
                        <p class="text-muted-foreground mt-0.5">{enumVal.description}</p>
                      {/if}
                      {#if enumVal.isDeprecated}
                        <span class="text-warning text-[10px]"
                          >Deprecated{enumVal.deprecationReason
                            ? `: ${enumVal.deprecationReason}`
                            : ""}</span
                        >
                      {/if}
                    </div>
                  {/each}
                </div>
              </div>
            {/if}

            <!-- Input Fields (for INPUT_OBJECT) -->
            {#if selectedType.inputFields?.length}
              <div class="px-4 py-3 border-t border-border/20">
                <div class="text-xs font-medium text-muted-foreground mb-2">Input Fields</div>
                <div class="space-y-2">
                  {#each selectedType.inputFields as inputField (inputField.name)}
                    <div class="text-xs pl-2 border-l-2 border-border/30">
                      <div class="flex items-center gap-2">
                        <span class="font-mono text-foreground">{inputField.name}</span>
                        <button
                          class="font-mono text-primary hover:underline"
                          onclick={() => navigateToType(getTypeBaseName(inputField.type))}
                          disabled={isTypeScalar(inputField.type)}
                        >
                          {getTypeName(inputField.type)}
                        </button>
                      </div>
                      {#if inputField.description}
                        <p class="text-muted-foreground mt-0.5">{inputField.description}</p>
                      {/if}
                    </div>
                  {/each}
                </div>
              </div>
            {/if}

            <!-- Object Fields -->
            {#if selectedType.fields?.length}
              <div class="px-4 py-3 border-t border-border/20">
                <div class="text-xs font-medium text-muted-foreground mb-2">Fields</div>
                <div class="space-y-2">
                  {#each selectedType.fields as typeField (typeField.name)}
                    <div class="text-xs pl-2 border-l-2 border-border/30">
                      <div class="flex items-center gap-2 flex-wrap">
                        <span class="font-mono text-foreground">{typeField.name}</span>
                        {#if typeField.args?.length}
                          <span class="text-muted-foreground">(</span>
                          {#each typeField.args as arg, i (arg.name)}
                            {#if i > 0}<span class="text-muted-foreground">, </span>{/if}
                            <span class="text-muted-foreground">{arg.name}:</span>
                            <button
                              class="font-mono text-primary hover:underline"
                              onclick={() => navigateToType(getTypeBaseName(arg.type))}
                              disabled={isTypeScalar(arg.type)}
                            >
                              {getTypeName(arg.type)}
                            </button>
                          {/each}
                          <span class="text-muted-foreground">)</span>
                        {/if}
                        <span class="text-muted-foreground">→</span>
                        <button
                          class="font-mono text-success hover:underline"
                          onclick={() => navigateToType(getTypeBaseName(typeField.type))}
                          disabled={isTypeScalar(typeField.type)}
                        >
                          {getTypeName(typeField.type)}
                        </button>
                      </div>
                      {#if typeField.description}
                        <p class="text-muted-foreground mt-0.5">{typeField.description}</p>
                      {/if}
                      {#if typeField.isDeprecated}
                        <div class="flex items-center gap-1 mt-0.5 text-warning">
                          <AlertTriangle class="w-3 h-3" />
                          <span class="text-[10px]"
                            >Deprecated{typeField.deprecationReason
                              ? `: ${typeField.deprecationReason}`
                              : ""}</span
                          >
                        </div>
                      {/if}
                    </div>
                  {/each}
                </div>
              </div>
            {/if}

            <!-- Used By -->
            {#if getTypeUsages(selectedType.name || "").length > 0}
              {@const usages = getTypeUsages(selectedType.name || "")}
              <div class="px-4 py-3 border-t border-border/20">
                <div class="text-xs font-medium text-muted-foreground mb-2">Used By</div>
                <div class="flex flex-wrap gap-1">
                  {#each usages.slice(0, 10) as usage, i (`${usage.parentType}.${usage.fieldName}-${i}`)}
                    <button
                      class="text-[10px] px-1.5 py-0.5 bg-muted border border-border/40 font-mono hover:bg-muted/80 transition-colors"
                      onclick={() => navigateToType(usage.parentType)}
                      disabled={usage.operationType !== undefined}
                      title={usage.operationType
                        ? `${usage.operationType} (root type)`
                        : `Navigate to ${usage.parentType}`}
                    >
                      <span class="text-muted-foreground">{usage.parentType}.</span><span
                        class="text-foreground">{usage.fieldName}</span
                      >
                    </button>
                  {/each}
                  {#if usages.length > 10}
                    <span class="text-[10px] text-muted-foreground px-1.5 py-0.5">
                      +{usages.length - 10} more
                    </span>
                  {/if}
                </div>
              </div>
            {/if}
          </div>
        {:else}
          <div class="px-4 py-6 text-center text-muted-foreground text-sm">
            Type "{currentNav.name}" not found
          </div>
        {/if}
      {:else}
        <!-- Root View: Show Query/Mutation/Subscription/Types -->
        <!-- Search Bar -->
        <div class="p-3 border-t border-border/30">
          <div class="relative">
            <Search
              class="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-muted-foreground"
            />
            <Input
              type="text"
              placeholder="Search schema..."
              class="pl-9 h-8 text-sm"
              bind:value={schemaSearch}
            />
          </div>
          {#if schemaSearch.trim()}
            <div class="mt-2 text-[10px] text-muted-foreground">
              Found: {filteredQueries.length} queries, {filteredMutations.length} mutations, {filteredSubscriptions.length}
              subscriptions, {filteredTypes.length} types
            </div>
            {#if filteredQueries.length === 0 && filteredMutations.length === 0 && filteredSubscriptions.length === 0 && filteredTypes.length === 0}
              <div class="mt-3 text-center text-xs text-muted-foreground">
                No results for "{schemaSearch}"
              </div>
            {/if}
          {/if}
        </div>

        <!-- Queries -->
        {#if filteredQueries.length > 0}
          <div class="border-t-2 border-border/50">
            <button
              class="w-full px-4 py-2.5 text-xs font-medium text-success uppercase tracking-wider bg-success/5 hover:bg-success/10 transition-colors flex items-center justify-between"
              onclick={() => toggleSection("queries")}
            >
              <span>
                Queries
                <span class="text-muted-foreground ml-1"
                  >({filteredQueries.length}{schemaSearch.trim() && schema.queryType?.fields
                    ? `/${schema.queryType.fields.length}`
                    : ""})</span
                >
              </span>
              <ChevronDown
                class="w-4 h-4 transition-transform {collapsedSections.has('queries')
                  ? '-rotate-90'
                  : ''}"
              />
            </button>
            {#if !collapsedSections.has("queries")}
              <div class="divide-y divide-border/20">
                {#each filteredQueries as field (field.name)}
                  <div class="group">
                    <button
                      class="w-full text-left px-4 py-2 text-xs hover:bg-muted/30 transition-colors"
                      onclick={() => toggleFieldExpanded(`query-${field.name}`)}
                    >
                      <div class="flex items-center justify-between gap-2">
                        <div class="flex items-center gap-2 min-w-0">
                          <span class="font-mono text-foreground truncate">{field.name}</span>
                          {#if field.isDeprecated}
                            <span title="Deprecated"
                              ><AlertTriangle class="w-3 h-3 text-warning flex-shrink-0" /></span
                            >
                          {/if}
                        </div>
                        <div class="flex items-center gap-1 flex-shrink-0">
                          <span class="font-mono text-muted-foreground text-[10px]"
                            >{getTypeName(field.type)}</span
                          >
                          <ChevronRight
                            class="w-3 h-3 text-muted-foreground transition-transform {expandedField ===
                            `query-${field.name}`
                              ? 'rotate-90'
                              : ''}"
                          />
                        </div>
                      </div>
                    </button>
                    {#if expandedField === `query-${field.name}`}
                      <div class="px-4 py-3 bg-muted/20 border-t border-border/20">
                        {#if field.isDeprecated}
                          <div
                            class="flex items-start gap-2 mb-2 p-2 bg-warning/10 border border-warning/20 text-xs"
                          >
                            <AlertTriangle class="w-3 h-3 text-warning flex-shrink-0 mt-0.5" />
                            <div>
                              <span class="font-medium text-warning">Deprecated</span>
                              {#if field.deprecationReason}
                                <span class="text-muted-foreground"
                                  >: {field.deprecationReason}</span
                                >
                              {/if}
                            </div>
                          </div>
                        {/if}
                        {#if field.description}
                          <p class="text-xs text-muted-foreground mb-2">{field.description}</p>
                        {/if}
                        {#if field.args?.length}
                          <div class="mb-2">
                            <span class="text-xs text-muted-foreground font-medium">Arguments:</span
                            >
                            <div class="mt-1 space-y-1">
                              {#each field.args as arg (arg.name)}
                                <div class="text-xs font-mono pl-2 border-l-2 border-border/30">
                                  <span class="text-foreground">{arg.name}</span>
                                  <span class="text-muted-foreground">: </span>
                                  <button
                                    class="text-primary hover:underline disabled:hover:no-underline disabled:cursor-default"
                                    onclick={() => navigateToType(getTypeBaseName(arg.type))}
                                    disabled={isTypeScalar(arg.type)}
                                  >
                                    {getTypeName(arg.type)}
                                  </button>
                                  {#if arg.description}
                                    <p class="text-muted-foreground font-sans text-[10px] mt-0.5">
                                      {arg.description}
                                    </p>
                                  {/if}
                                </div>
                              {/each}
                            </div>
                          </div>
                        {/if}
                        <div class="text-xs mb-3">
                          <span class="text-muted-foreground font-medium">Returns: </span>
                          <button
                            class="font-mono text-success hover:underline disabled:hover:no-underline disabled:cursor-default"
                            onclick={() => navigateToType(getTypeBaseName(field.type))}
                            disabled={isTypeScalar(field.type)}
                          >
                            {getTypeName(field.type)}
                          </button>
                        </div>
                        {#if getReturnTypeFields(field).length > 0}
                          {@const returnFields = getReturnTypeFields(field)}
                          <div class="mb-3 text-xs">
                            <span class="text-muted-foreground font-medium">Return fields:</span>
                            <div class="mt-1 flex flex-wrap gap-1">
                              {#each returnFields.slice(0, 8) as retField (retField.name)}
                                <code class="text-[10px] bg-muted px-1.5 py-0.5 rounded font-mono">
                                  {retField.name}
                                </code>
                              {/each}
                              {#if returnFields.length > 8}
                                <span class="text-[10px] text-muted-foreground">
                                  +{returnFields.length - 8} more
                                </span>
                              {/if}
                            </div>
                          </div>
                        {/if}
                        <Button
                          size="sm"
                          variant="outline"
                          class="w-full text-xs gap-1"
                          onclick={() => handleSelectField(field, "query")}
                        >
                          <Play class="w-3 h-3" />
                          Use this query
                        </Button>
                      </div>
                    {/if}
                  </div>
                {/each}
              </div>
            {/if}
          </div>
        {/if}

        <!-- Mutations -->
        {#if filteredMutations.length > 0}
          <div class="border-t-2 border-border/50">
            <button
              class="w-full px-4 py-2.5 text-xs font-medium text-primary uppercase tracking-wider bg-primary/5 hover:bg-primary/10 transition-colors flex items-center justify-between"
              onclick={() => toggleSection("mutations")}
            >
              <span>
                Mutations
                <span class="text-muted-foreground ml-1"
                  >({filteredMutations.length}{schemaSearch.trim() && schema.mutationType?.fields
                    ? `/${schema.mutationType.fields.length}`
                    : ""})</span
                >
              </span>
              <ChevronDown
                class="w-4 h-4 transition-transform {collapsedSections.has('mutations')
                  ? '-rotate-90'
                  : ''}"
              />
            </button>
            {#if !collapsedSections.has("mutations")}
              <div class="divide-y divide-border/20">
                {#each filteredMutations as field (field.name)}
                  <div class="group">
                    <button
                      class="w-full text-left px-4 py-2 text-xs hover:bg-muted/30 transition-colors"
                      onclick={() => toggleFieldExpanded(`mutation-${field.name}`)}
                    >
                      <div class="flex items-center justify-between gap-2">
                        <div class="flex items-center gap-2 min-w-0">
                          <span class="font-mono text-foreground truncate">{field.name}</span>
                          {#if field.isDeprecated}
                            <span title="Deprecated"
                              ><AlertTriangle class="w-3 h-3 text-warning flex-shrink-0" /></span
                            >
                          {/if}
                        </div>
                        <div class="flex items-center gap-1 flex-shrink-0">
                          <span class="font-mono text-muted-foreground text-[10px]"
                            >{getTypeName(field.type)}</span
                          >
                          <ChevronRight
                            class="w-3 h-3 text-muted-foreground transition-transform {expandedField ===
                            `mutation-${field.name}`
                              ? 'rotate-90'
                              : ''}"
                          />
                        </div>
                      </div>
                    </button>
                    {#if expandedField === `mutation-${field.name}`}
                      <div class="px-4 py-3 bg-muted/20 border-t border-border/20">
                        {#if field.isDeprecated}
                          <div
                            class="flex items-start gap-2 mb-2 p-2 bg-warning/10 border border-warning/20 text-xs"
                          >
                            <AlertTriangle class="w-3 h-3 text-warning flex-shrink-0 mt-0.5" />
                            <div>
                              <span class="font-medium text-warning">Deprecated</span>
                              {#if field.deprecationReason}
                                <span class="text-muted-foreground"
                                  >: {field.deprecationReason}</span
                                >
                              {/if}
                            </div>
                          </div>
                        {/if}
                        {#if field.description}
                          <p class="text-xs text-muted-foreground mb-2">{field.description}</p>
                        {/if}
                        {#if field.args?.length}
                          <div class="mb-2">
                            <span class="text-xs text-muted-foreground font-medium">Arguments:</span
                            >
                            <div class="mt-1 space-y-1">
                              {#each field.args as arg (arg.name)}
                                <div class="text-xs font-mono pl-2 border-l-2 border-border/30">
                                  <span class="text-foreground">{arg.name}</span>
                                  <span class="text-muted-foreground">: </span>
                                  <button
                                    class="text-primary hover:underline disabled:hover:no-underline disabled:cursor-default"
                                    onclick={() => navigateToType(getTypeBaseName(arg.type))}
                                    disabled={isTypeScalar(arg.type)}
                                  >
                                    {getTypeName(arg.type)}
                                  </button>
                                  {#if arg.description}
                                    <p class="text-muted-foreground font-sans text-[10px] mt-0.5">
                                      {arg.description}
                                    </p>
                                  {/if}
                                </div>
                              {/each}
                            </div>
                          </div>
                        {/if}
                        <div class="text-xs mb-3">
                          <span class="text-muted-foreground font-medium">Returns: </span>
                          <button
                            class="font-mono text-primary hover:underline disabled:hover:no-underline disabled:cursor-default"
                            onclick={() => navigateToType(getTypeBaseName(field.type))}
                            disabled={isTypeScalar(field.type)}
                          >
                            {getTypeName(field.type)}
                          </button>
                        </div>
                        {#if getReturnTypeFields(field).length > 0}
                          {@const returnFields = getReturnTypeFields(field)}
                          <div class="mb-3 text-xs">
                            <span class="text-muted-foreground font-medium">Return fields:</span>
                            <div class="mt-1 flex flex-wrap gap-1">
                              {#each returnFields.slice(0, 8) as retField (retField.name)}
                                <code class="text-[10px] bg-muted px-1.5 py-0.5 rounded font-mono">
                                  {retField.name}
                                </code>
                              {/each}
                              {#if returnFields.length > 8}
                                <span class="text-[10px] text-muted-foreground">
                                  +{returnFields.length - 8} more
                                </span>
                              {/if}
                            </div>
                          </div>
                        {/if}
                        <Button
                          size="sm"
                          variant="outline"
                          class="w-full text-xs gap-1"
                          onclick={() => handleSelectField(field, "mutation")}
                        >
                          <Play class="w-3 h-3" />
                          Use this mutation
                        </Button>
                      </div>
                    {/if}
                  </div>
                {/each}
              </div>
            {/if}
          </div>
        {/if}

        <!-- Subscriptions -->
        {#if filteredSubscriptions.length > 0}
          <div class="border-t-2 border-border/50">
            <button
              class="w-full px-4 py-2.5 text-xs font-medium text-accent-purple uppercase tracking-wider bg-accent-purple/5 hover:bg-accent-purple/10 transition-colors flex items-center justify-between"
              onclick={() => toggleSection("subscriptions")}
            >
              <span>
                Subscriptions
                <span class="text-muted-foreground ml-1"
                  >({filteredSubscriptions.length}{schemaSearch.trim() &&
                  schema.subscriptionType?.fields
                    ? `/${schema.subscriptionType.fields.length}`
                    : ""})</span
                >
              </span>
              <ChevronDown
                class="w-4 h-4 transition-transform {collapsedSections.has('subscriptions')
                  ? '-rotate-90'
                  : ''}"
              />
            </button>
            {#if !collapsedSections.has("subscriptions")}
              <div class="divide-y divide-border/20">
                {#each filteredSubscriptions as field (field.name)}
                  <div class="group">
                    <button
                      class="w-full text-left px-4 py-2 text-xs hover:bg-muted/30 transition-colors"
                      onclick={() => toggleFieldExpanded(`subscription-${field.name}`)}
                    >
                      <div class="flex items-center justify-between gap-2">
                        <div class="flex items-center gap-2 min-w-0">
                          <span class="font-mono text-foreground truncate">{field.name}</span>
                          {#if field.isDeprecated}
                            <span title="Deprecated"
                              ><AlertTriangle class="w-3 h-3 text-warning flex-shrink-0" /></span
                            >
                          {/if}
                        </div>
                        <div class="flex items-center gap-1 flex-shrink-0">
                          <span class="font-mono text-muted-foreground text-[10px]"
                            >{getTypeName(field.type)}</span
                          >
                          <ChevronRight
                            class="w-3 h-3 text-muted-foreground transition-transform {expandedField ===
                            `subscription-${field.name}`
                              ? 'rotate-90'
                              : ''}"
                          />
                        </div>
                      </div>
                    </button>
                    {#if expandedField === `subscription-${field.name}`}
                      <div class="px-4 py-3 bg-muted/20 border-t border-border/20">
                        {#if field.isDeprecated}
                          <div
                            class="flex items-start gap-2 mb-2 p-2 bg-warning/10 border border-warning/20 text-xs"
                          >
                            <AlertTriangle class="w-3 h-3 text-warning flex-shrink-0 mt-0.5" />
                            <div>
                              <span class="font-medium text-warning">Deprecated</span>
                              {#if field.deprecationReason}
                                <span class="text-muted-foreground"
                                  >: {field.deprecationReason}</span
                                >
                              {/if}
                            </div>
                          </div>
                        {/if}
                        {#if field.description}
                          <p class="text-xs text-muted-foreground mb-2">{field.description}</p>
                        {/if}
                        {#if field.args?.length}
                          <div class="mb-2">
                            <span class="text-xs text-muted-foreground font-medium">Arguments:</span
                            >
                            <div class="mt-1 space-y-1">
                              {#each field.args as arg (arg.name)}
                                <div class="text-xs font-mono pl-2 border-l-2 border-border/30">
                                  <span class="text-foreground">{arg.name}</span>
                                  <span class="text-muted-foreground">: </span>
                                  <button
                                    class="text-accent-purple hover:underline disabled:hover:no-underline disabled:cursor-default"
                                    onclick={() => navigateToType(getTypeBaseName(arg.type))}
                                    disabled={isTypeScalar(arg.type)}
                                  >
                                    {getTypeName(arg.type)}
                                  </button>
                                  {#if arg.description}
                                    <p class="text-muted-foreground font-sans text-[10px] mt-0.5">
                                      {arg.description}
                                    </p>
                                  {/if}
                                </div>
                              {/each}
                            </div>
                          </div>
                        {/if}
                        <div class="text-xs mb-3">
                          <span class="text-muted-foreground font-medium">Returns: </span>
                          <button
                            class="font-mono text-accent-purple hover:underline disabled:hover:no-underline disabled:cursor-default"
                            onclick={() => navigateToType(getTypeBaseName(field.type))}
                            disabled={isTypeScalar(field.type)}
                          >
                            {getTypeName(field.type)}
                          </button>
                        </div>
                        {#if getReturnTypeFields(field).length > 0}
                          {@const returnFields = getReturnTypeFields(field)}
                          <div class="mb-3 text-xs">
                            <span class="text-muted-foreground font-medium">Return fields:</span>
                            <div class="mt-1 flex flex-wrap gap-1">
                              {#each returnFields.slice(0, 8) as retField (retField.name)}
                                <code class="text-[10px] bg-muted px-1.5 py-0.5 rounded font-mono">
                                  {retField.name}
                                </code>
                              {/each}
                              {#if returnFields.length > 8}
                                <span class="text-[10px] text-muted-foreground">
                                  +{returnFields.length - 8} more
                                </span>
                              {/if}
                            </div>
                          </div>
                        {/if}
                        <Button
                          size="sm"
                          variant="outline"
                          class="w-full text-xs gap-1"
                          onclick={() => handleSelectField(field, "subscription")}
                        >
                          <Play class="w-3 h-3" />
                          Use this subscription
                        </Button>
                      </div>
                    {/if}
                  </div>
                {/each}
              </div>
            {/if}
          </div>
        {/if}

        <!-- Types -->
        {#if filteredTypes.length > 0}
          <div class="border-t-2 border-border/50">
            <button
              class="w-full px-4 py-2.5 text-xs font-medium text-muted-foreground uppercase tracking-wider bg-muted/30 hover:bg-muted/50 transition-colors flex items-center justify-between"
              onclick={() => toggleSection("types")}
            >
              <span>
                Types
                <span class="text-muted-foreground ml-1"
                  >({filteredTypes.length}{schemaSearch.trim() ? `/${totalTypesCount}` : ""})</span
                >
              </span>
              <ChevronDown
                class="w-4 h-4 transition-transform {collapsedSections.has('types')
                  ? '-rotate-90'
                  : ''}"
              />
            </button>
            {#if !collapsedSections.has("types")}
              <div class="divide-y divide-border/20">
                {#each filteredTypes as type (type.name)}
                  <div class="group">
                    <button
                      class="w-full text-left px-4 py-2 text-xs hover:bg-muted/30 transition-colors"
                      onclick={() => navigateToType(type.name || "")}
                    >
                      <div class="flex items-center justify-between gap-2">
                        <div class="flex items-center gap-2 min-w-0">
                          <span class="font-mono text-primary hover:underline truncate"
                            >{type.name}</span
                          >
                        </div>
                        <div class="flex items-center gap-1 flex-shrink-0">
                          <span class="text-[10px] uppercase tracking-wide text-muted-foreground">
                            {type.kind}
                          </span>
                          <ChevronRight class="w-3 h-3 text-muted-foreground" />
                        </div>
                      </div>
                      {#if type.description}
                        <p class="text-[10px] text-muted-foreground mt-0.5 truncate">
                          {type.description}
                        </p>
                      {/if}
                    </button>
                  </div>
                {/each}
              </div>
            {/if}
          </div>
        {/if}
      {/if}
    {:else}
      <div class="px-4 py-6 text-center">
        <Button size="sm" variant="outline" onclick={onLoadSchema}>Load Schema</Button>
      </div>
    {/if}
  {/if}
{/snippet}

<!-- Sidebar Mode -->
{#if sidebarMode && view}
  <div class="w-80 bg-card border-r border-border h-full overflow-hidden flex flex-col shrink-0">
    <div class="p-3 border-b border-border flex items-center justify-between flex-shrink-0">
      <div class="flex items-center gap-2">
        {#if view === "templates"}
          <BookOpen class="w-4 h-4 text-primary" />
          <h2 class="text-sm font-semibold text-foreground">Templates</h2>
        {:else}
          <Database class="w-4 h-4 text-primary" />
          <h2 class="text-sm font-semibold text-foreground">Schema</h2>
        {/if}
      </div>
      <button
        class="p-1 text-muted-foreground hover:text-foreground transition-colors"
        onclick={onClose}
      >
        <X class="w-4 h-4" />
      </button>
    </div>
    <div class="flex-1 overflow-y-auto">
      {@render panelContent()}
    </div>
  </div>
{:else if view}
  <!-- Drawer Mode (modal) - shows on mobile, renders same content as sidebar -->
  <div
    class="fixed inset-0 bg-black/30 z-50"
    role="button"
    tabindex="0"
    aria-label="Close panel"
    onclick={onClose}
    onkeydown={(e) => e.key === "Escape" && onClose()}
    transition:fade={{ duration: 200 }}
  >
    <div
      class="w-80 bg-card border-r border-border h-full overflow-hidden flex flex-col"
      role="dialog"
      tabindex="-1"
      aria-modal="true"
      onclick={stopPropagation}
      onkeydown={stopPropagation}
      transition:fly={{ x: -320, duration: 300 }}
    >
      <!-- Re-render same content as sidebar mode by using svelte:self or extracting -->
      <!-- For now, just duplicate the header and reuse via the sidebarMode path -->
      <div class="p-3 border-b border-border flex items-center justify-between flex-shrink-0">
        <div class="flex items-center gap-2">
          {#if view === "templates"}
            <BookOpen class="w-4 h-4 text-primary" />
            <h2 class="text-sm font-semibold text-foreground">Templates</h2>
          {:else}
            <Database class="w-4 h-4 text-primary" />
            <h2 class="text-sm font-semibold text-foreground">Schema</h2>
          {/if}
        </div>
        <button
          class="p-1 text-muted-foreground hover:text-foreground transition-colors"
          onclick={onClose}
        >
          <X class="w-4 h-4" />
        </button>
      </div>
      <div class="flex-1 overflow-y-auto">
        {@render panelContent()}
      </div>
    </div>
  </div>
{/if}

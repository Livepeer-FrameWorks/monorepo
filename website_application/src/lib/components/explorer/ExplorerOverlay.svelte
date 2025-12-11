<script lang="ts">
  import { fade, fly } from "svelte/transition";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import {
    Accordion,
    AccordionContent,
    AccordionItem,
    AccordionTrigger,
  } from "$lib/components/ui/accordion";
  import { ChevronRight, Play, X, Clock, BookOpen, Database, Search, FileCode, AlertTriangle } from "lucide-svelte";
  import type { Template, TemplateGroups } from "$lib/graphql/services/explorer";
  import { formatTypeString, getBaseTypeName, isScalarType } from "$lib/graphql/services/schemaUtils";

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

  interface Schema {
    queryType?: SchemaType;
    mutationType?: SchemaType;
    subscriptionType?: SchemaType;
  }

  interface QueryHistoryItem {
    id: number;
    query: string;
    variables: Record<string, unknown>;
    result: { statusIcon: string; [key: string]: unknown };
    timestamp: string;
  }

  interface Props {
    open: boolean;
    type?: "schema" | "templates" | null;
    schema: Schema | null;
    queryTemplates: TemplateGroups | null;
    queryHistory: QueryHistoryItem[];
    loading: boolean;
    selectedTemplate?: Template | null;
    onClose: () => void;
    onSelectTemplate: (template: Template) => void;
    onLoadFromHistory: (item: QueryHistoryItem) => void;
    onSelectSchemaField?: (field: SchemaField, operationType: string) => void;
    onLoadSchema: () => void;
  }

  let {
    open,
    type = null,
    schema,
    queryTemplates,
    queryHistory,
    loading,
    selectedTemplate = null,
    onClose,
    onSelectTemplate,
    onLoadFromHistory,
    onSelectSchemaField,
    onLoadSchema,
  }: Props = $props();

  // Track expanded field for detail view
  let expandedField = $state<string | null>(null);

  // Accordion state - schema expanded by default
  let accordionValue = $state<string[]>(["schema"]);

  // Template search state
  let templateSearch = $state("");

  // Filter templates based on search
  const filteredTemplates = $derived.by(() => {
    if (!queryTemplates) return null;
    if (!templateSearch.trim()) return queryTemplates;

    const search = templateSearch.toLowerCase();
    return {
      queries: queryTemplates.queries.filter(
        t => t.name.toLowerCase().includes(search) || t.description.toLowerCase().includes(search)
      ),
      mutations: queryTemplates.mutations.filter(
        t => t.name.toLowerCase().includes(search) || t.description.toLowerCase().includes(search)
      ),
      subscriptions: queryTemplates.subscriptions.filter(
        t => t.name.toLowerCase().includes(search) || t.description.toLowerCase().includes(search)
      ),
      fragments: queryTemplates.fragments.filter(
        t => t.name.toLowerCase().includes(search) || t.description.toLowerCase().includes(search)
      ),
    };
  });

  // Total template count
  const templateCount = $derived(
    queryTemplates
      ? queryTemplates.queries.length +
        queryTemplates.mutations.length +
        queryTemplates.subscriptions.length +
        queryTemplates.fragments.length
      : 0
  );

  function toggleFieldExpanded(fieldName: string) {
    expandedField = expandedField === fieldName ? null : fieldName;
  }

  function handleSelectField(field: SchemaField, operationType: string) {
    onSelectSchemaField?.(field, operationType);
    onClose();
  }

  function handleSelectTemplate(template: Template) {
    onSelectTemplate(template);
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

  function stopPropagation(event: Event) {
    event.stopPropagation();
  }

  // Load schema when panel opens
  $effect(() => {
    if (open && !schema && !loading) {
      onLoadSchema();
    }
  });
</script>

<!-- Query Library Drawer -->
{#if open}
  <!-- Backdrop -->
  <div
    class="fixed inset-0 bg-black/30 z-50"
    role="button"
    tabindex="0"
    aria-label="Close query library"
    onclick={onClose}
    onkeydown={(e) => e.key === "Escape" && onClose()}
    transition:fade={{ duration: 200 }}
  >
    <!-- Drawer Panel -->
    <div
      class="w-80 bg-card border-r border-border h-full overflow-hidden flex flex-col"
      role="dialog"
      tabindex="-1"
      aria-modal="true"
      aria-label="Query Library"
      onclick={stopPropagation}
      onkeydown={stopPropagation}
      transition:fly={{ x: -320, duration: 300 }}
    >
      <!-- Header -->
      <div class="p-4 border-b border-border flex items-center justify-between flex-shrink-0">
        <div class="flex items-center gap-2">
          <BookOpen class="w-5 h-5 text-primary" />
          <h2 class="text-lg font-semibold text-foreground">Query Library</h2>
        </div>
        <button
          class="p-1 text-muted-foreground hover:text-foreground transition-colors"
          onclick={onClose}
          aria-label="Close"
        >
          <X class="w-5 h-5" />
        </button>
      </div>

      <!-- Content -->
      <div class="flex-1 overflow-y-auto">
        <Accordion type="multiple" bind:value={accordionValue} class="w-full">
          <!-- Schema Section -->
          <AccordionItem value="schema" class="border-b border-border/50">
            <AccordionTrigger class="px-4 py-3 hover:bg-muted/30">
              <div class="flex items-center gap-2">
                <Database class="w-4 h-4 text-primary" />
                <span class="text-sm font-medium">Schema</span>
              </div>
            </AccordionTrigger>
            <AccordionContent class="pb-0">
              {#if loading}
                <div class="px-4 py-6 text-center text-muted-foreground text-sm">
                  Loading schema...
                </div>
              {:else if schema}
                <!-- Queries -->
                {#if schema.queryType?.fields?.length}
                  <div class="border-t border-border/30">
                    <div class="px-4 py-2 text-xs font-medium text-success uppercase tracking-wider bg-success/5">
                      Queries
                      <span class="text-muted-foreground ml-1">({schema.queryType.fields.length})</span>
                    </div>
                    <div class="divide-y divide-border/20">
                      {#each schema.queryType.fields as field (field.name)}
                        <div class="group">
                          <button
                            class="w-full text-left px-4 py-2 text-xs hover:bg-muted/30 transition-colors"
                            onclick={() => toggleFieldExpanded(`query-${field.name}`)}
                          >
                            <div class="flex items-center justify-between gap-2">
                              <div class="flex items-center gap-2 min-w-0">
                                <span class="font-mono text-foreground truncate">{field.name}</span>
                                {#if field.isDeprecated}
                                  <span title="Deprecated"><AlertTriangle class="w-3 h-3 text-warning flex-shrink-0" /></span>
                                {/if}
                              </div>
                              <div class="flex items-center gap-1 flex-shrink-0">
                                <span class="font-mono text-muted-foreground text-[10px]">{getTypeName(field.type)}</span>
                                <ChevronRight
                                  class="w-3 h-3 text-muted-foreground transition-transform {expandedField === `query-${field.name}` ? 'rotate-90' : ''}"
                                />
                              </div>
                            </div>
                          </button>
                          {#if expandedField === `query-${field.name}`}
                            <div class="px-4 py-3 bg-muted/20 border-t border-border/20">
                              {#if field.isDeprecated}
                                <div class="flex items-start gap-2 mb-2 p-2 bg-warning/10 border border-warning/20 text-xs">
                                  <AlertTriangle class="w-3 h-3 text-warning flex-shrink-0 mt-0.5" />
                                  <div>
                                    <span class="font-medium text-warning">Deprecated</span>
                                    {#if field.deprecationReason}
                                      <span class="text-muted-foreground">: {field.deprecationReason}</span>
                                    {/if}
                                  </div>
                                </div>
                              {/if}
                              {#if field.description}
                                <p class="text-xs text-muted-foreground mb-2">{field.description}</p>
                              {/if}
                              {#if field.args?.length}
                                <div class="mb-2">
                                  <span class="text-xs text-muted-foreground font-medium">Arguments:</span>
                                  <div class="mt-1 space-y-1">
                                    {#each field.args as arg (arg.name)}
                                      <div class="text-xs font-mono pl-2 border-l-2 border-border/30">
                                        <span class="text-foreground">{arg.name}</span>
                                        <span class="text-primary">: {getTypeName(arg.type)}</span>
                                        {#if arg.description}
                                          <p class="text-muted-foreground font-sans text-[10px] mt-0.5">{arg.description}</p>
                                        {/if}
                                      </div>
                                    {/each}
                                  </div>
                                </div>
                              {/if}
                              <div class="text-xs mb-3">
                                <span class="text-muted-foreground font-medium">Returns: </span>
                                <span class="font-mono text-success">{getTypeName(field.type)}</span>
                              </div>
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
                  </div>
                {/if}

                <!-- Mutations -->
                {#if schema.mutationType?.fields?.length}
                  <div class="border-t border-border/30">
                    <div class="px-4 py-2 text-xs font-medium text-primary uppercase tracking-wider bg-primary/5">
                      Mutations
                      <span class="text-muted-foreground ml-1">({schema.mutationType.fields.length})</span>
                    </div>
                    <div class="divide-y divide-border/20">
                      {#each schema.mutationType.fields as field (field.name)}
                        <div class="group">
                          <button
                            class="w-full text-left px-4 py-2 text-xs hover:bg-muted/30 transition-colors"
                            onclick={() => toggleFieldExpanded(`mutation-${field.name}`)}
                          >
                            <div class="flex items-center justify-between gap-2">
                              <div class="flex items-center gap-2 min-w-0">
                                <span class="font-mono text-foreground truncate">{field.name}</span>
                                {#if field.isDeprecated}
                                  <span title="Deprecated"><AlertTriangle class="w-3 h-3 text-warning flex-shrink-0" /></span>
                                {/if}
                              </div>
                              <div class="flex items-center gap-1 flex-shrink-0">
                                <span class="font-mono text-muted-foreground text-[10px]">{getTypeName(field.type)}</span>
                                <ChevronRight
                                  class="w-3 h-3 text-muted-foreground transition-transform {expandedField === `mutation-${field.name}` ? 'rotate-90' : ''}"
                                />
                              </div>
                            </div>
                          </button>
                          {#if expandedField === `mutation-${field.name}`}
                            <div class="px-4 py-3 bg-muted/20 border-t border-border/20">
                              {#if field.isDeprecated}
                                <div class="flex items-start gap-2 mb-2 p-2 bg-warning/10 border border-warning/20 text-xs">
                                  <AlertTriangle class="w-3 h-3 text-warning flex-shrink-0 mt-0.5" />
                                  <div>
                                    <span class="font-medium text-warning">Deprecated</span>
                                    {#if field.deprecationReason}
                                      <span class="text-muted-foreground">: {field.deprecationReason}</span>
                                    {/if}
                                  </div>
                                </div>
                              {/if}
                              {#if field.description}
                                <p class="text-xs text-muted-foreground mb-2">{field.description}</p>
                              {/if}
                              {#if field.args?.length}
                                <div class="mb-2">
                                  <span class="text-xs text-muted-foreground font-medium">Arguments:</span>
                                  <div class="mt-1 space-y-1">
                                    {#each field.args as arg (arg.name)}
                                      <div class="text-xs font-mono pl-2 border-l-2 border-border/30">
                                        <span class="text-foreground">{arg.name}</span>
                                        <span class="text-primary">: {getTypeName(arg.type)}</span>
                                        {#if arg.description}
                                          <p class="text-muted-foreground font-sans text-[10px] mt-0.5">{arg.description}</p>
                                        {/if}
                                      </div>
                                    {/each}
                                  </div>
                                </div>
                              {/if}
                              <div class="text-xs mb-3">
                                <span class="text-muted-foreground font-medium">Returns: </span>
                                <span class="font-mono text-primary">{getTypeName(field.type)}</span>
                              </div>
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
                  </div>
                {/if}

                <!-- Subscriptions -->
                {#if schema.subscriptionType?.fields?.length}
                  <div class="border-t border-border/30">
                    <div class="px-4 py-2 text-xs font-medium text-accent-purple uppercase tracking-wider bg-accent-purple/5">
                      Subscriptions
                      <span class="text-muted-foreground ml-1">({schema.subscriptionType.fields.length})</span>
                    </div>
                    <div class="divide-y divide-border/20">
                      {#each schema.subscriptionType.fields as field (field.name)}
                        <div class="group">
                          <button
                            class="w-full text-left px-4 py-2 text-xs hover:bg-muted/30 transition-colors"
                            onclick={() => toggleFieldExpanded(`subscription-${field.name}`)}
                          >
                            <div class="flex items-center justify-between gap-2">
                              <div class="flex items-center gap-2 min-w-0">
                                <span class="font-mono text-foreground truncate">{field.name}</span>
                                {#if field.isDeprecated}
                                  <span title="Deprecated"><AlertTriangle class="w-3 h-3 text-warning flex-shrink-0" /></span>
                                {/if}
                              </div>
                              <div class="flex items-center gap-1 flex-shrink-0">
                                <span class="font-mono text-muted-foreground text-[10px]">{getTypeName(field.type)}</span>
                                <ChevronRight
                                  class="w-3 h-3 text-muted-foreground transition-transform {expandedField === `subscription-${field.name}` ? 'rotate-90' : ''}"
                                />
                              </div>
                            </div>
                          </button>
                          {#if expandedField === `subscription-${field.name}`}
                            <div class="px-4 py-3 bg-muted/20 border-t border-border/20">
                              {#if field.isDeprecated}
                                <div class="flex items-start gap-2 mb-2 p-2 bg-warning/10 border border-warning/20 text-xs">
                                  <AlertTriangle class="w-3 h-3 text-warning flex-shrink-0 mt-0.5" />
                                  <div>
                                    <span class="font-medium text-warning">Deprecated</span>
                                    {#if field.deprecationReason}
                                      <span class="text-muted-foreground">: {field.deprecationReason}</span>
                                    {/if}
                                  </div>
                                </div>
                              {/if}
                              {#if field.description}
                                <p class="text-xs text-muted-foreground mb-2">{field.description}</p>
                              {/if}
                              {#if field.args?.length}
                                <div class="mb-2">
                                  <span class="text-xs text-muted-foreground font-medium">Arguments:</span>
                                  <div class="mt-1 space-y-1">
                                    {#each field.args as arg (arg.name)}
                                      <div class="text-xs font-mono pl-2 border-l-2 border-border/30">
                                        <span class="text-foreground">{arg.name}</span>
                                        <span class="text-accent-purple">: {getTypeName(arg.type)}</span>
                                        {#if arg.description}
                                          <p class="text-muted-foreground font-sans text-[10px] mt-0.5">{arg.description}</p>
                                        {/if}
                                      </div>
                                    {/each}
                                  </div>
                                </div>
                              {/if}
                              <div class="text-xs mb-3">
                                <span class="text-muted-foreground font-medium">Returns: </span>
                                <span class="font-mono text-accent-purple">{getTypeName(field.type)}</span>
                              </div>
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
                  </div>
                {/if}
              {:else}
                <div class="px-4 py-6 text-center">
                  <Button size="sm" variant="outline" onclick={onLoadSchema}>
                    Load Schema
                  </Button>
                </div>
              {/if}
            </AccordionContent>
          </AccordionItem>

          <!-- Templates Section -->
          <AccordionItem value="templates" class="border-b border-border/50">
            <AccordionTrigger class="px-4 py-3 hover:bg-muted/30">
              <div class="flex items-center gap-2">
                <BookOpen class="w-4 h-4 text-warning" />
                <span class="text-sm font-medium">Templates</span>
                {#if templateCount > 0}
                  <span class="text-xs text-muted-foreground">({templateCount})</span>
                {/if}
              </div>
            </AccordionTrigger>
            <AccordionContent class="pb-0">
              {#if queryTemplates}
                <!-- Search input -->
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

                {#each [
                  { key: "queries", label: "Queries", color: "text-success", bg: "bg-success/5" },
                  { key: "mutations", label: "Mutations", color: "text-primary", bg: "bg-primary/5" },
                  { key: "subscriptions", label: "Subscriptions", color: "text-accent-purple", bg: "bg-accent-purple/5" },
                  { key: "fragments", label: "Fragments", color: "text-warning", bg: "bg-warning/5" }
                ] as category (category.key)}
                  {#if filteredTemplates?.[category.key as keyof TemplateGroups]?.length}
                    <div class="border-t border-border/30">
                      <div class="px-4 py-2 text-xs font-medium {category.color} uppercase tracking-wider {category.bg}">
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
                              <Play class="w-3 h-3 text-muted-foreground opacity-0 group-hover:opacity-100 transition-opacity mt-1 flex-shrink-0" />
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
                <div class="px-4 py-6 text-center text-muted-foreground text-sm">
                  Loading templates...
                </div>
              {/if}
            </AccordionContent>
          </AccordionItem>

          <!-- History Section -->
          {#if queryHistory.length > 0}
            <AccordionItem value="history" class="border-b border-border/50">
              <AccordionTrigger class="px-4 py-3 hover:bg-muted/30">
                <div class="flex items-center gap-2">
                  <Clock class="w-4 h-4 text-muted-foreground" />
                  <span class="text-sm font-medium">Recent History</span>
                  <span class="text-xs text-muted-foreground">({queryHistory.length})</span>
                </div>
              </AccordionTrigger>
              <AccordionContent class="pb-0">
                <div class="divide-y divide-border/20">
                  {#each queryHistory as item (item.id)}
                    <button
                      class="w-full text-left px-4 py-3 hover:bg-muted/30 transition-colors"
                      onclick={() => handleLoadFromHistory(item)}
                    >
                      <div class="text-sm font-mono text-foreground truncate mb-1">
                        {item.query.split("\n")[0].replace(/^(query|mutation|subscription)\s+\w*\s*\{?/, "").trim() || "Query"}
                      </div>
                      <div class="text-xs text-muted-foreground">
                        {new Date(item.timestamp).toLocaleTimeString()}
                        <span class="ml-1">{item.result.statusIcon === "success" ? "✓" : "✗"}</span>
                      </div>
                    </button>
                  {/each}
                </div>
              </AccordionContent>
            </AccordionItem>
          {/if}
        </Accordion>
      </div>
    </div>
  </div>
{/if}

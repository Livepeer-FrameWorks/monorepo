/**
 * Template Loader
 * Dynamically loads all .gql files from the Houdini directory
 * using Vite's import.meta.glob for build-time loading
 */

import {
  parseGqlFile,
  formatOperationForTemplate,
  type ParsedOperation,
  type OperationType,
} from "./gqlParser";

export interface Template {
  name: string;
  description: string;
  query: string;
  variables: Record<string, unknown>;
  filePath: string;
  operationType: OperationType;
}

export interface TemplateGroups {
  queries: Template[];
  mutations: Template[];
  subscriptions: Template[];
  fragments: Template[];
}

// Use Vite's import.meta.glob to load all .gql files at build time
// The ?raw query imports the file content as a string
const gqlModules = import.meta.glob("/src/lib/houdini/**/*.gql", {
  query: "?raw",
  import: "default",
  eager: false, // Lazy load for better initial bundle size
});

// Cache for loaded templates
let cachedTemplates: TemplateGroups | null = null;

// Cache for fragment definitions (name -> raw content)
let fragmentDefinitions: Map<string, string> = new Map();

// Clear cache on HMR to pick up changes
if (import.meta.hot) {
  import.meta.hot.accept(() => {
    cachedTemplates = null;
    fragmentDefinitions.clear();
  });
}

/**
 * Load all .gql templates from the Houdini directory
 * Results are cached after first load
 */
export async function loadAllTemplates(): Promise<TemplateGroups> {
  if (cachedTemplates) {
    return cachedTemplates;
  }

  const groups: TemplateGroups = {
    queries: [],
    mutations: [],
    subscriptions: [],
    fragments: [],
  };

  // Load all modules in parallel
  const entries = Object.entries(gqlModules);
  const results = await Promise.all(
    entries.map(async ([path, loader]) => {
      try {
        const content = (await loader()) as string;
        return { path, content };
      } catch (error) {
        console.error(`Failed to load ${path}:`, error);
        return null;
      }
    }),
  );

  // Parse and categorize each file
  for (const result of results) {
    if (!result) continue;

    const { path, content } = result;

    try {
      const parsed = parseGqlFile(content, path);
      const template = createTemplate(parsed, path);

      // Add to appropriate group
      switch (parsed.type) {
        case "query":
          groups.queries.push(template);
          break;
        case "mutation":
          groups.mutations.push(template);
          break;
        case "subscription":
          groups.subscriptions.push(template);
          break;
        case "fragment":
          groups.fragments.push(template);
          break;
      }
    } catch (error) {
      console.error(`Failed to parse ${path}:`, error);
    }
  }

  // Build fragment definitions map for resolving spreads
  fragmentDefinitions.clear();
  for (const fragment of groups.fragments) {
    // Extract fragment name - handle leading whitespace/comments
    const nameMatch = fragment.query.match(/fragment\s+(\w+)/);
    if (nameMatch) {
      fragmentDefinitions.set(nameMatch[1], fragment.query.trim());
    }
  }

  if (import.meta.env.DEV) {
    console.log(
      `[templateLoader] Loaded ${groups.fragments.length} fragments, ${fragmentDefinitions.size} parsed:`,
      [...fragmentDefinitions.keys()],
    );
  }

  // Resolve fragment spreads in queries and mutations
  for (const query of groups.queries) {
    query.query = resolveFragmentSpreads(query.query);
  }
  for (const mutation of groups.mutations) {
    mutation.query = resolveFragmentSpreads(mutation.query);
  }
  for (const subscription of groups.subscriptions) {
    subscription.query = resolveFragmentSpreads(subscription.query);
  }

  // Sort each group alphabetically by name
  groups.queries.sort((a, b) => a.name.localeCompare(b.name));
  groups.mutations.sort((a, b) => a.name.localeCompare(b.name));
  groups.subscriptions.sort((a, b) => a.name.localeCompare(b.name));
  groups.fragments.sort((a, b) => a.name.localeCompare(b.name));

  cachedTemplates = groups;
  return groups;
}

/**
 * Resolve all fragment spreads in a query by appending their definitions
 * Handles nested fragments (fragments that spread other fragments)
 */
function resolveFragmentSpreads(query: string): string {
  // Find all fragment spreads: ...FragmentName (but not on __typename lines)
  const spreadPattern = /\.\.\.(\w+)(?!\s*on\s)/g;
  const spreads = [...query.matchAll(spreadPattern)];

  if (spreads.length === 0) {
    return query;
  }

  // Collect all required fragments (including nested)
  const required = new Set<string>();
  const toProcess = spreads.map((m) => m[1]);

  while (toProcess.length > 0) {
    const name = toProcess.pop()!;
    if (required.has(name)) continue;

    const definition = fragmentDefinitions.get(name);
    if (definition) {
      required.add(name);
      // Check for nested fragment spreads in this definition
      const nested = [...definition.matchAll(spreadPattern)];
      for (const match of nested) {
        if (!required.has(match[1])) {
          toProcess.push(match[1]);
        }
      }
    }
  }

  // Append all required fragment definitions
  let result = query;
  for (const name of required) {
    const def = fragmentDefinitions.get(name);
    if (def) {
      result += "\n\n" + def;
    }
  }

  return result;
}

/**
 * Create a Template from a ParsedOperation
 */
function createTemplate(parsed: ParsedOperation, filePath: string): Template {
  const formatted = formatOperationForTemplate(parsed);

  return {
    name: formatted.name,
    description: formatted.description,
    query: formatted.query,
    variables: formatted.variables,
    filePath: cleanFilePath(filePath),
    operationType: parsed.type,
  };
}

/**
 * Clean the file path for display
 * /src/lib/houdini/queries/GetStream.gql -> houdini/queries/GetStream.gql
 */
function cleanFilePath(path: string): string {
  return path.replace(/^\/src\/lib\//, "");
}

/**
 * Get the count of templates by type
 */
export async function getTemplateCounts(): Promise<Record<string, number>> {
  const templates = await loadAllTemplates();
  return {
    queries: templates.queries.length,
    mutations: templates.mutations.length,
    subscriptions: templates.subscriptions.length,
    fragments: templates.fragments.length,
    total:
      templates.queries.length +
      templates.mutations.length +
      templates.subscriptions.length +
      templates.fragments.length,
  };
}

/**
 * Search templates by name or description
 */
export async function searchTemplates(query: string): Promise<Template[]> {
  const templates = await loadAllTemplates();
  const lowerQuery = query.toLowerCase();

  const allTemplates = [
    ...templates.queries,
    ...templates.mutations,
    ...templates.subscriptions,
    ...templates.fragments,
  ];

  return allTemplates.filter(
    (t) =>
      t.name.toLowerCase().includes(lowerQuery) ||
      t.description.toLowerCase().includes(lowerQuery),
  );
}

/**
 * Get templates by operation type
 */
export async function getTemplatesByType(
  type: OperationType,
): Promise<Template[]> {
  const templates = await loadAllTemplates();

  switch (type) {
    case "query":
      return templates.queries;
    case "mutation":
      return templates.mutations;
    case "subscription":
      return templates.subscriptions;
    case "fragment":
      return templates.fragments;
    default:
      return [];
  }
}

/**
 * Clear the template cache (useful for hot reload)
 */
export function clearTemplateCache(): void {
  cachedTemplates = null;
  fragmentDefinitions.clear();
}

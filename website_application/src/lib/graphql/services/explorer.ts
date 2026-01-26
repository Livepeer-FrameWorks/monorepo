/**
 * Simple fetch-based GraphQL client for the explorer
 * Replaces Apollo for dynamic query execution
 */

import {
  loadAllTemplates,
  searchTemplates as searchTemplatesFromLoader,
  type Template,
  type TemplateGroups,
} from "./templateLoader";

import {
  type IntrospectedSchema,
  getBaseTypeName,
  findType,
  isUnionType,
  getPossibleTypes,
  isScalarType,
} from "./schemaUtils";

import { extractOperationType } from "./gqlParser";
import {
  EXPLORER_CATALOG,
  type ResolvedExplorerSection,
  type ResolvedExplorerExample,
} from "./explorerCatalog";

// Re-export template types for consumers
export type { Template, TemplateGroups };
export type {
  ResolvedExplorerSection,
  ResolvedExplorerExample,
} from "./explorerCatalog";

// Also export the search function
export { searchTemplatesFromLoader as searchTemplates };

// Cached schema for query generation
let cachedSchema: IntrospectedSchema | null = null;

const GRAPHQL_HTTP_URL = import.meta.env.VITE_GRAPHQL_HTTP_URL ?? "";

// Cached templates for field-to-template matching
let cachedTemplatesMap: Map<string, Template> | null = null;

function resolveCatalogTemplates(
  templates: TemplateGroups,
): ResolvedExplorerSection[] {
  const templateByPath = new Map<string, Template>();
  const allTemplates = [
    ...templates.queries,
    ...templates.mutations,
    ...templates.subscriptions,
  ];

  for (const template of allTemplates) {
    if (template.filePath) {
      templateByPath.set(template.filePath, template);
    }
  }

  return EXPLORER_CATALOG.map((section) => ({
    id: section.id,
    title: section.title,
    description: section.description,
    examples: section.examples.map((example) => {
      const template = example.templatePath
        ? templateByPath.get(example.templatePath)
        : undefined;
      return {
        ...example,
        template,
      } satisfies ResolvedExplorerExample;
    }),
  }));
}

/**
 * Build a map of field names to templates for quick lookup
 * Key format: "operationType:fieldName" e.g. "query:streamsConnection"
 */
async function buildTemplateMap(): Promise<Map<string, Template>> {
  if (cachedTemplatesMap) return cachedTemplatesMap;

  const templates = await loadAllTemplates();
  const map = new Map<string, Template>();

  // Extract field name from query body using regex
  const extractFieldName = (query: string): string | null => {
    // Match the first field after the operation definition
    // e.g. "query GetStreams { streamsConnection(...) { ... } }" -> "streamsConnection"
    const match = query.match(/\{\s*(\w+)/);
    return match ? match[1] : null;
  };

  for (const template of templates.queries) {
    const fieldName = extractFieldName(template.query);
    if (fieldName) {
      // Use the most specific/comprehensive template (longer query = more fields)
      const key = `query:${fieldName}`;
      const existing = map.get(key);
      if (!existing || template.query.length > existing.query.length) {
        map.set(key, template);
      }
    }
  }

  for (const template of templates.mutations) {
    const fieldName = extractFieldName(template.query);
    if (fieldName) {
      const key = `mutation:${fieldName}`;
      map.set(key, template);
    }
  }

  for (const template of templates.subscriptions) {
    const fieldName = extractFieldName(template.query);
    if (fieldName) {
      const key = `subscription:${fieldName}`;
      map.set(key, template);
    }
  }

  cachedTemplatesMap = map;
  return map;
}

interface ExplorerResult {
  data?: unknown;
  error?: Error | null;
  errors?: Array<{ message: string; locations?: unknown; path?: unknown }>;
  duration?: number;
  timestamp?: string | number | Date;
  loading?: boolean;
  demoMode?: boolean;
}

interface GraphQLResponse {
  data?: unknown;
  errors?: Array<{ message: string; locations?: unknown; path?: unknown }>;
}

async function executeGraphQL(
  query: string,
  variables: Record<string, unknown> = {},
  headers: Record<string, string> = {},
): Promise<GraphQLResponse> {
  const url = import.meta.env.VITE_GRAPHQL_HTTP_URL || "/graphql/";

  const response = await fetch(url, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      ...headers,
    },
    credentials: "include", // Send cookies for auth
    body: JSON.stringify({ query, variables }),
  });

  if (!response.ok) {
    throw new Error(`HTTP ${response.status}: ${response.statusText}`);
  }

  return response.json();
}

interface QueryTemplate {
  name: string;
  description: string;
  query: string;
  variables: Record<string, unknown>;
}

interface QueryTemplates {
  queries: QueryTemplate[];
  mutations: QueryTemplate[];
  subscriptions: QueryTemplate[];
}

interface CodeExamples {
  javascript: string;
  fetch: string;
  curl: string;
  python: string;
  go: string;
}

interface ValidationResult {
  valid: boolean;
  error: string | null;
}

interface TypeRef {
  name?: string;
  kind?: string;
  ofType?: TypeRef;
}

interface SchemaFieldArg {
  name: string;
  description?: string;
  type?: TypeRef;
}

interface SchemaField {
  name: string;
  description?: string;
  args?: SchemaFieldArg[];
  type?: TypeRef;
}

interface GeneratedQuery {
  query: string;
  variables: Record<string, unknown>;
}

interface FormattedError {
  message: string;
  graphQLErrors?: Array<{
    message: string;
    locations?: unknown;
    path?: unknown;
  }>;
  networkError?: {
    message: string;
    statusCode?: number;
  } | null;
}

interface FormattedResponse {
  status: string;
  statusIcon: string;
  timestamp: string;
  duration: string;
  data: string | null;
  error: FormattedError | null;
}

// Introspection queries as plain strings
const INTROSPECT_SCHEMA = `
  query IntrospectSchema {
    __schema {
      queryType {
        name
        fields {
          name
          description
          type {
            ...TypeRef
          }
          args {
            name
            description
            type {
              ...TypeRef
            }
            defaultValue
          }
        }
      }
      mutationType {
        name
        fields {
          name
          description
          type {
            ...TypeRef
          }
          args {
            name
            description
            type {
              ...TypeRef
            }
            defaultValue
          }
        }
      }
      subscriptionType {
        name
        fields {
          name
          description
          type {
            ...TypeRef
          }
          args {
            name
            description
            type {
              ...TypeRef
            }
            defaultValue
          }
        }
      }
      types {
        ...FullType
      }
    }
  }

  fragment FullType on __Type {
    kind
    name
    description
    fields(includeDeprecated: true) {
      name
      description
      args {
        ...InputValue
      }
      type {
        ...TypeRef
      }
      isDeprecated
      deprecationReason
    }
    inputFields {
      ...InputValue
    }
    interfaces {
      ...TypeRef
    }
    enumValues(includeDeprecated: true) {
      name
      description
      isDeprecated
      deprecationReason
    }
    possibleTypes {
      ...TypeRef
    }
  }

  fragment InputValue on __InputValue {
    name
    description
    type {
      ...TypeRef
    }
    defaultValue
  }

  fragment TypeRef on __Type {
    kind
    name
    ofType {
      kind
      name
      ofType {
        kind
        name
        ofType {
          kind
          name
          ofType {
            kind
            name
            ofType {
              kind
              name
              ofType {
                kind
                name
                ofType {
                  kind
                  name
                }
              }
            }
          }
        }
      }
    }
  }
`;

const GET_ROOT_TYPES = `
  query GetRootTypes {
    __schema {
      queryType {
        name
        fields {
          name
          description
        }
      }
      mutationType {
        name
        fields {
          name
          description
        }
      }
      subscriptionType {
        name
        fields {
          name
          description
        }
      }
    }
  }
`;

/**
 * GraphQL Explorer Service
 * Handles schema introspection and query execution for the custom explorer
 */
export const explorerService = {
  /**
   * Get the full GraphQL schema via introspection
   * Caches the schema; templates are warmed in the background for query generation
   */
  async getSchema(): Promise<IntrospectedSchema> {
    try {
      const result = await executeGraphQL(INTROSPECT_SCHEMA);

      if (result.errors?.length) {
        throw new Error(result.errors[0].message);
      }
      const schema = (result.data as { __schema: IntrospectedSchema }).__schema;
      // Cache the schema for query generation
      cachedSchema = schema;
      // Warm template map asynchronously so schema load isn't blocked
      void buildTemplateMap().catch((error) => {
        console.warn("Failed to preload template map:", error);
      });
      return schema;
    } catch (error: unknown) {
      console.error("Failed to introspect schema:", error);
      throw error instanceof Error ? error : new Error(String(error));
    }
  },

  /**
   * Get the cached schema (or fetch it if not cached)
   */
  async getCachedSchema(): Promise<IntrospectedSchema> {
    if (cachedSchema) return cachedSchema;
    return this.getSchema();
  },

  /**
   * Get just the root types for quick overview
   */
  async getRootTypes(): Promise<unknown> {
    try {
      const result = await executeGraphQL(GET_ROOT_TYPES);
      if (result.errors?.length) {
        throw new Error(result.errors[0].message);
      }
      return (result.data as { __schema: unknown }).__schema;
    } catch (error: unknown) {
      console.error("Failed to get root types:", error);
      throw error instanceof Error ? error : new Error(String(error));
    }
  },

  /**
   * Execute a GraphQL query with variables
   */
  async executeQuery(
    query: string,
    variables: Record<string, unknown> = {},
    _operationType: string = "query",
    demoMode: boolean = false,
  ): Promise<ExplorerResult> {
    try {
      const startTime = Date.now();

      // Configure headers for demo mode
      const headers: Record<string, string> = demoMode
        ? { "X-Demo-Mode": "true" }
        : {};

      const result = await executeGraphQL(query, variables, headers);

      const endTime = Date.now();
      const duration = endTime - startTime;

      return {
        data: result.data,
        errors: result.errors,
        error: result.errors?.[0] ? new Error(result.errors[0].message) : null,
        loading: false,
        duration,
        timestamp: new Date().toISOString(),
        demoMode,
      };
    } catch (error: unknown) {
      console.error("GraphQL query execution failed:", error);
      return {
        data: null,
        loading: false,
        error: error instanceof Error ? error : new Error(String(error)),
        duration: 0,
        timestamp: new Date().toISOString(),
        demoMode,
      };
    }
  },

  /**
   * Get query templates dynamically loaded from Houdini .gql files
   * Returns templates grouped by type (queries, mutations, subscriptions, fragments)
   */
  async getTemplates(): Promise<TemplateGroups> {
    return loadAllTemplates();
  },

  /**
   * Get curated catalog sections for the explorer.
   */
  async getCatalog(): Promise<ResolvedExplorerSection[]> {
    const templates = await loadAllTemplates();
    return resolveCatalogTemplates(templates);
  },

  /**
   * Search templates by name or description
   */
  async searchTemplates(query: string): Promise<Template[]> {
    return searchTemplatesFromLoader(query);
  },

  /**
   * Find an existing Houdini template that uses the given field
   * Returns the template if found, null otherwise
   * This is synchronous and uses a cached map - call ensureTemplatesLoaded() first
   */
  findTemplateForField(
    fieldName: string,
    operationType: string,
  ): Template | null {
    if (!cachedTemplatesMap) return null;
    const key = `${operationType}:${fieldName}`;
    return cachedTemplatesMap.get(key) || null;
  },

  /**
   * Ensure templates are loaded into the cache
   * Call this during initialization
   */
  async ensureTemplatesLoaded(): Promise<void> {
    await buildTemplateMap();
  },

  /**
   * Legacy sync method for backwards compatibility
   * Returns a simplified structure without fragments
   * @deprecated Use getTemplates() instead
   */
  getQueryTemplates(): QueryTemplates {
    // Return empty arrays - consumers should migrate to async getTemplates()
    console.warn(
      "getQueryTemplates() is deprecated. Use getTemplates() instead.",
    );
    return {
      queries: [],
      mutations: [],
      subscriptions: [],
    };
  },

  /**
   * Generate code examples for different languages
   */
  generateCodeExamples(
    query: string,
    variables: Record<string, unknown> = {},
    token: string | null = null,
  ): CodeExamples {
    const tokenValue = token || "your_token_here";
    const hasVariables = Object.keys(variables).length > 0;

    const examples: CodeExamples = {
      javascript: `// JavaScript (Apollo Client)
import { ApolloClient, InMemoryCache, gql } from '@apollo/client';

  const client = new ApolloClient({
  uri: '${GRAPHQL_HTTP_URL}',
  cache: new InMemoryCache(),
  headers: {
    Authorization: 'Bearer ${tokenValue}'
  }
});

const query = gql\`${query}\`;

${
  hasVariables
    ? `const variables = ${JSON.stringify(variables, null, 2)};

const { data, error } = await client.query({
  query,
  variables
});`
    : `const { data, error } = await client.query({
  query
});`
}

console.log(data);`,

      fetch: `// JavaScript (Fetch API)
const query = \`${query}\`;
${hasVariables ? `const variables = ${JSON.stringify(variables, null, 2)};` : ""}

const response = await fetch('${GRAPHQL_HTTP_URL}', {
  method: 'POST',
  headers: {
    'Content-Type': 'application/json',
    'Authorization': 'Bearer ${tokenValue}'
  },
  body: JSON.stringify({
    query${hasVariables ? ",\n    variables" : ""}
  })
});

const result = await response.json();
console.log(result.data);`,

      curl: `# cURL
curl -X POST \\
  -H "Content-Type: application/json" \\
  -H "Authorization: Bearer ${tokenValue}" \\
  -d '{"query":"${query.replace(/"/g, '\\"').replace(/\n/g, "\\n")}"${hasVariables ? `,"variables":${JSON.stringify(variables)}` : ""}}' \\
  ${GRAPHQL_HTTP_URL}`,

      python: `# Python (requests)
import requests
import json

url = "${GRAPHQL_HTTP_URL}"
headers = {
    "Content-Type": "application/json",
    "Authorization": "Bearer ${tokenValue}"
}

query = """${query}"""
${hasVariables ? `variables = ${JSON.stringify(variables, null, 4)}` : ""}

payload = {
    "query": query${hasVariables ? ',\n    "variables": variables' : ""}
}

response = requests.post(url, headers=headers, json=payload)
result = response.json()
print(result["data"])`,

      go: `// Go
package main

import (
    "bytes"
    "encoding/json"
    "fmt"
    "net/http"
)

type GraphQLRequest struct {
    Query     string      \`json:"query"\`${hasVariables ? '\n    Variables interface{} `json:"variables,omitempty"`' : ""}
}

func main() {
    url := "${GRAPHQL_HTTP_URL}"

    query := \`${query}\`
${
  hasVariables
    ? `    variables := map[string]interface{}{
${Object.entries(variables)
  .map(([key, value]) => `        "${key}": ${JSON.stringify(value)},`)
  .join("\n")}
    }
`
    : ""
}
    reqBody := GraphQLRequest{
        Query: query,${hasVariables ? "\n        Variables: variables," : ""}
    }

    jsonData, _ := json.Marshal(reqBody)

    req, _ := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
    req.Header.Set("Content-Type", "application/json")
    req.Header.Set("Authorization", "Bearer ${tokenValue}")

    client := &http.Client{}
    resp, err := client.Do(req)
    if err != nil {
        panic(err)
    }
    defer resp.Body.Close()

    var result map[string]interface{}
    json.NewDecoder(resp.Body).Decode(&result)
    fmt.Printf("%+v\\n", result["data"])
}`,
    };

    return examples;
  },

  /**
   * Lightweight heuristics to surface schema-aware hints in the explorer.
   */
  getQueryHints(query: string): Array<{ title: string; body: string }> {
    const hints: Array<{ title: string; body: string }> = [];
    const normalized = query.toLowerCase();

    if (normalized.includes("analytics")) {
      hints.push({
        title: "Analytics entry point",
        body: "Analytics live under analytics.{usage|health|lifecycle|infra}. Pick the branch that matches what you’re after.",
      });
    }

    if (normalized.includes("analytics") && normalized.includes("usage")) {
      hints.push({
        title: "Usage rollups",
        body: "For long ranges, use daily rollups. Hourly series are best for short windows.",
      });
    }

    if (normalized.includes("analytics") && normalized.includes("health")) {
      hints.push({
        title: "Health detail",
        body: "Use 5m rollups for long ranges; use raw health for short, detailed windows.",
      });
    }

    if (normalized.includes("analytics") && normalized.includes("lifecycle")) {
      hints.push({
        title: "Lifecycle scope",
        body: "Add streamId to focus events; otherwise you’ll get platform-wide noise.",
      });
    }

    if (normalized.includes("analytics") && normalized.includes("infra")) {
      hints.push({
        title: "Infra signals",
        body: "Node metrics are rollups. Routing events explain load‑balancer decisions.",
      });
    }

    if (normalized.includes("subscription")) {
      hints.push({
        title: "Live streams",
        body: "Subscriptions are live‑only. Pair with historical queries for context.",
      });
    }

    if (normalized.includes("page:")) {
      hints.push({
        title: "Pagination",
        body: "Connections use page: { first, after, last, before }. Use pageInfo.endCursor to fetch more.",
      });
    }

    if (normalized.includes("streamid")) {
      hints.push({
        title: "Stream identifiers",
        body: "streamId is a Relay global ID (Stream.id). Use Stream.streamId for public UUIDs.",
      });
    }

    if (normalized.includes("clip") || normalized.includes("vod")) {
      hints.push({
        title: "Artifacts",
        body: "Clip/VOD hashes are safe to expose. Lifecycle + storage gives the full story.",
      });
    }

    return hints;
  },

  /**
   * Generate a query from a schema field
   * First checks for existing Houdini templates that use this field,
   * then falls back to creating a query with argument placeholders and common return fields.
   * Uses cached schema for proper union type handling.
   */
  generateQueryFromField(
    field: SchemaField,
    operationType: string,
    schema?: IntrospectedSchema,
  ): GeneratedQuery {
    const fieldNamePascal =
      field.name.charAt(0).toUpperCase() + field.name.slice(1);

    // Check if we have a cached template for this field (loaded from Houdini .gql files)
    // Templates are the source of truth for comprehensive queries
    const matchingTemplate = this.findTemplateForField(
      field.name,
      operationType,
    );
    if (matchingTemplate) {
      return {
        query: matchingTemplate.query,
        variables: matchingTemplate.variables,
      };
    }

    // Build argument definitions and usage
    const args = field.args || [];
    const hasArgs = args.length > 0;

    // Generate variable definitions for the operation signature
    const varDefs = args
      .map((arg) => {
        const typeName = this.getGraphQLTypeName(arg.type);
        return `$${arg.name}: ${typeName}`;
      })
      .join(", ");

    // Generate variable usage in the field call
    const varUsage = args.map((arg) => `${arg.name}: $${arg.name}`).join(", ");

    // Generate default variables object with placeholder values
    const variables: Record<string, unknown> = {};
    for (const arg of args) {
      let value = this.getDefaultValueForType(arg.type);
      if (arg.name === "streamId") value = "stream_global_id";
      if (arg.name === "nodeId") value = "node_id";
      if (arg.name === "clusterId") value = "cluster_id";
      if (arg.name === "page") value = { first: 50 };
      if (arg.name === "timeRange")
        value = this.getDefaultForInputType("TimeRangeInput");
      variables[arg.name] = value;
    }

    // Use provided schema or fall back to cached schema
    const schemaToUse = schema || cachedSchema;

    // Determine return fields based on type (with schema-aware union handling)
    const returnFields = this.getCommonReturnFields(field.type, schemaToUse);

    // Build the query
    let query: string;
    if (operationType === "subscription") {
      query = `subscription ${fieldNamePascal}${hasArgs ? `(${varDefs})` : ""} {
  ${field.name}${hasArgs ? `(${varUsage})` : ""} ${returnFields}
}`;
    } else if (operationType === "mutation") {
      query = `mutation ${fieldNamePascal}${hasArgs ? `(${varDefs})` : ""} {
  ${field.name}${hasArgs ? `(${varUsage})` : ""} ${returnFields}
}`;
    } else {
      query = `query ${fieldNamePascal}${hasArgs ? `(${varDefs})` : ""} {
  ${field.name}${hasArgs ? `(${varUsage})` : ""} ${returnFields}
}`;
    }

    return { query, variables };
  },

  /**
   * Get the GraphQL type name from a type object
   */
  getGraphQLTypeName(type: TypeRef | undefined): string {
    if (!type) return "String";

    // Handle NON_NULL wrapper
    if (type.kind === "NON_NULL") {
      const innerType = type.ofType;
      if (innerType?.name) return `${innerType.name}!`;
      if (innerType?.kind === "LIST" && innerType.ofType?.name) {
        return `[${innerType.ofType.name}]!`;
      }
      return "String!";
    }

    // Handle LIST wrapper
    if (type.kind === "LIST") {
      const innerType = type.ofType;
      if (innerType?.name) return `[${innerType.name}]`;
      return "[String]";
    }

    // Simple named type
    if (type.name) return type.name;

    return "String";
  },

  /**
   * Get a default placeholder value for a GraphQL type
   */
  getDefaultValueForType(type: TypeRef | undefined): unknown {
    const typeName = this.getGraphQLTypeName(type).replace(/[!\[\]]/g, "");

    switch (typeName) {
      case "ID":
        return "your-id-here";
      case "String":
        return "";
      case "Int":
        return 0;
      case "Float":
        return 0.0;
      case "Boolean":
        return false;
      case "Time":
      case "DateTime":
        return new Date().toISOString();
      case "JSON":
        return {};
      default:
        // Handle known input types with proper structure
        return this.getDefaultForInputType(typeName);
    }
  },

  /**
   * Get default values for known GraphQL input types
   */
  getDefaultForInputType(typeName: string): unknown {
    const now = new Date();
    const oneDayAgo = new Date(now.getTime() - 24 * 60 * 60 * 1000);

    switch (typeName) {
      case "ConnectionInput":
        return {
          first: 50,
          after: null,
        };

      case "TimeRangeInput":
        return {
          start: oneDayAgo.toISOString(),
          end: now.toISOString(),
        };

      case "CreateStreamInput":
        return {
          name: "example-live-stream",
          description: "Example stream description",
          record: false,
        };

      case "UpdateStreamInput":
        return {
          name: "example-live-stream-updated",
          description: "Updated stream description",
          record: false,
        };

      case "CreateClipInput":
        return {
          streamId: "stream_global_id",
          title: "Example Clip",
          description: "Example clip description",
          mode: "ABSOLUTE",
          startUnix: 0,
          stopUnix: 30,
        };

      case "CreateVodUploadInput":
        return {
          filename: "example.mp4",
          sizeBytes: 1024 * 1024,
          contentType: "video/mp4",
          title: "Example VOD",
          description: "Example VOD upload",
        };

      case "CompleteVodUploadInput":
        return {
          uploadId: "upload_id",
          parts: [{ partNumber: 1, etag: "etag-value" }],
        };

      case "CreateStreamKeyInput":
        return {
          name: "primary-key",
        };

      case "CreateDeveloperTokenInput":
        return {
          name: "example-api-token",
          permissions: "read,write",
          expiresIn: null,
        };

      case "CreateBootstrapTokenInput":
        return {
          name: "bootstrap-token",
          kind: "cluster",
          expiresIn: null,
        };

      case "StartDvrInput":
        return {
          streamId: "stream_global_id",
        };

      case "StopDvrInput":
        return {
          streamId: "stream_global_id",
        };

      case "CreatePaymentInput":
        return {
          amount: 1000,
          currency: "USD",
          method: "CARD",
        };

      case "CreatePrivateClusterInput":
        return {
          name: "My Private Cluster",
          region: "us-east",
          deploymentModel: "hybrid",
        };

      case "UpdateClusterMarketplaceInput":
        return {
          shortDescription: "Low latency premium cluster",
          pricingModel: "SUBSCRIPTION",
          monthlyPriceCents: 5000,
        };

      case "CreateClusterInviteInput":
        return {
          email: "operator@example.com",
        };

      case "UpdateSubscriptionCustomTermsInput":
        return {
          monthlyPriceCents: 5000,
          billingCycle: "monthly",
        };

      case "CustomPricingInput":
        return {
          monthlyPriceCents: 5000,
          billingCycle: "monthly",
        };

      case "OverageRatesInput":
        return {
          bandwidth: { unit: "GB", unitPrice: 0.01 },
          storage: { unit: "GB", unitPrice: 0.02 },
          compute: { unit: "hour", unitPrice: 0.05 },
        };

      case "AllocationDetailsInput":
        return {
          unit: "GB",
          included: 100,
          unitPrice: 0.02,
        };

      case "BillingFeaturesInput":
        return {
          analytics: true,
          prioritySupport: false,
        };

      case "UpdateTenantInput":
        return {
          name: "My Organization",
        };

      default:
        if (typeName.endsWith("Input")) {
          return {};
        }
        return null;
    }
  },

  /**
   * Get common return fields based on the return type
   * Uses schema introspection to properly handle union types with inline fragments
   */
  getCommonReturnFields(
    type: TypeRef | undefined,
    schema?: IntrospectedSchema | null,
  ): string {
    if (!type) return "";

    // Get the base type name (unwrap NON_NULL and LIST)
    const typeName = getBaseTypeName(type);

    // For scalar types, no fields needed
    if (isScalarType(typeName)) {
      return "";
    }

    // If we have a schema, check if this is a union type
    if (schema && isUnionType(schema, typeName)) {
      return this.generateUnionFields(typeName, schema);
    }

    // If we have schema, try to get fields dynamically with nested selections
    if (schema) {
      const typeDef = findType(schema, typeName);
      const selections =
        typeDef?.fields
          ?.filter((f) => !f.name.startsWith("__"))
          .slice(0, 8)
          .map((f) => this.buildFieldSelection(f, schema, 0))
          .filter((f): f is string => Boolean(f)) || [];

      if (selections.length > 0) {
        return `{
    ${selections.join("\n    ")}
  }`;
      }
    }

    // Fallback: common fields for well-known types (when schema not available)
    const commonFieldsByType: Record<string, string> = {
      Stream: `{
    id
    name
    description
    streamKey
    playbackId
    status
    record
    createdAt
    updatedAt
  }`,
      User: `{
    id
    email
    name
    role
    createdAt
  }`,
      StreamAnalyticsSummary: `{
    streamId
    rangeTotalViews
    rangeTotalSessions
    rangePeakConcurrentViewers
    rangeAvgViewers
    rangeUniqueViewers
  }`,
      Clip: `{
    id
    title
    description
    startTime
    duration
    status
    createdAt
  }`,
      BillingStatus: `{
    status
    nextBillingDate
    outstandingAmount
    currentTier {
      id
      name
      basePrice
    }
  }`,
      StreamEvent: `{
    type
    stream
    status
    timestamp
    details
  }`,
      ViewerMetrics: `{
    timestamp
    viewerCount
  }`,
      SystemHealth: `{
    nodeId
    status
    cpuUsage
    memoryUsage
    timestamp
  }`,
    };

    // Return known fields or generic placeholder
    return (
      commonFieldsByType[typeName] ||
      `{
    id
    # Add fields here
  }`
    );
  },

  /**
   * Build a safe selection set for a field, recursing into object types
   * while avoiding validation errors (e.g., selecting connections without subfields).
   */
  buildFieldSelection(
    field: SchemaField,
    schema: IntrospectedSchema,
    depth: number = 0,
  ): string | null {
    const baseTypeName = getBaseTypeName(field.type);
    const typeDef = findType(schema, baseTypeName);

    // Scalars and enums can be selected directly
    if (isScalarType(baseTypeName) || typeDef?.kind === "ENUM") {
      return field.name;
    }

    // Stop deep recursion; fall back to typename to keep selection valid
    if (!typeDef || depth >= 3) {
      return `${field.name} { __typename }`;
    }

    // Unions use inline fragments
    if (typeDef.kind === "UNION") {
      return `${field.name} ${this.generateUnionFields(baseTypeName, schema)}`;
    }

    if (typeDef.kind === "OBJECT" || typeDef.kind === "INTERFACE") {
      // Prefer scalar children and ids to keep responses concise
      const childSelections =
        typeDef.fields
          ?.filter((f) => !f.name.startsWith("__"))
          .slice(0, 6)
          .map((child) => {
            const childBase = getBaseTypeName(child.type);
            const childTypeDef = findType(schema, childBase);

            if (
              isScalarType(childBase) ||
              childTypeDef?.kind === "ENUM" ||
              childBase === "ID"
            ) {
              return child.name;
            }

            // Recurse for one more level to satisfy non-scalar requirements
            if (depth < 2) {
              return this.buildFieldSelection(child, schema, depth + 1);
            }

            // Fallback to typename for deeper objects
            return `${child.name} { __typename }`;
          })
          .filter((f): f is string => Boolean(f)) || [];

      const body =
        childSelections.length > 0
          ? childSelections.join("\n      ")
          : "__typename";
      return `${field.name} {\n      ${body}\n    }`;
    }

    return null;
  },

  /**
   * Generate inline fragments for union types using schema introspection
   * Dynamically gets the actual member types and their fields from the schema
   */
  generateUnionFields(
    unionTypeName: string,
    schema: IntrospectedSchema,
  ): string {
    // Get all possible types in this union from the schema
    const possibleTypes = getPossibleTypes(schema, unionTypeName);

    if (possibleTypes.length === 0) {
      // Fallback if we can't find the union members
      return `{
    __typename
    # Union type - add inline fragments for member types
  }`;
    }

    // Build inline fragments for each member type
    const fragments = possibleTypes.map((memberTypeName) => {
      // Get fields for this member type from the schema
      const memberType = findType(schema, memberTypeName);
      const fields =
        memberType?.fields
          ?.filter((f) => !f.name.startsWith("__"))
          .slice(0, 6)
          .map((f) => this.buildFieldSelection(f, schema, 1))
          .filter((f): f is string => Boolean(f)) || [];

      if (fields.length === 0) {
        return `    ... on ${memberTypeName} {\n      __typename\n    }`;
      }

      const fieldLines = fields.map((f) => `      ${f}`).join("\n");
      return `    ... on ${memberTypeName} {
${fieldLines}
    }`;
    });

    return `{
    __typename
${fragments.join("\n")}
  }`;
  },

  /**
   * Validate GraphQL query syntax
   */
  validateQuery(query: string): ValidationResult {
    try {
      // Basic validation - check for balanced braces and basic structure
      const braceCount =
        (query.match(/\{/g) || []).length - (query.match(/\}/g) || []).length;
      if (braceCount !== 0) {
        return {
          valid: false,
          error: "Unbalanced braces in query",
        };
      }

      // Use extractOperationType which correctly handles comment-prefixed queries
      // Returns 'query', 'mutation', 'subscription', or 'fragment'
      const opType = extractOperationType(query);

      // Fragment definitions aren't executable operations
      if (opType === "fragment") {
        return {
          valid: false,
          error: "Fragment definitions cannot be executed directly",
        };
      }

      return {
        valid: true,
        error: null,
      };
    } catch (error: unknown) {
      return {
        valid: false,
        error:
          error instanceof Error
            ? error.message
            : String(error || "Unknown error"),
      };
    }
  },

  /**
   * Format query response for display
   */
  formatResponse(result: ExplorerResult | null): FormattedResponse {
    const { data, error, errors, duration, timestamp, loading } = result || {};

    // Note: These will be rendered as HTML strings in the GraphQL Explorer UI
    // The component consuming this will handle the HTML rendering
    let status = "success";
    let statusIcon = "success"; // Will be mapped to proper Lucide icon in UI

    if (error || errors?.length) {
      status = "error";
      statusIcon = "error";
    } else if (loading) {
      status = "loading";
      statusIcon = "loading";
    }

    const response: FormattedResponse = {
      status,
      statusIcon,
      timestamp: new Date(timestamp || Date.now()).toLocaleTimeString(),
      duration: `${duration || 0}ms`,
      data: data ? JSON.stringify(data, null, 2) : null,
      error: error
        ? {
            message: error.message,
            graphQLErrors: errors?.map((e) => ({
              message: e.message,
              locations: e.locations,
              path: e.path,
            })),
            networkError: null,
          }
        : null,
    };

    return response;
  },
};

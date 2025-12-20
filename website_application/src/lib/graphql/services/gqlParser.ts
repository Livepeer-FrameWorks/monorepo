/**
 * GraphQL Query Parser
 * Lightweight parser for extracting operation metadata from .gql files
 * without requiring a full GraphQL AST parser
 */

export type OperationType = "query" | "mutation" | "subscription" | "fragment";

export interface VariableDefinition {
  name: string;
  type: string;
  required: boolean;
  isList: boolean;
  defaultValue?: unknown;
}

export interface ParsedOperation {
  name: string;
  type: OperationType;
  query: string;
  variables: VariableDefinition[];
  description?: string;
  filePath?: string;
}

/**
 * Extract the operation type from a GraphQL query string
 */
export function extractOperationType(query: string): OperationType {
  // Strip comments first to handle files with comment headers
  const withoutComments = query
    .split("\n")
    .filter((line) => !line.trim().startsWith("#"))
    .join("\n")
    .trim();

  if (withoutComments.startsWith("mutation")) return "mutation";
  if (withoutComments.startsWith("subscription")) return "subscription";
  if (withoutComments.startsWith("fragment")) return "fragment";
  if (withoutComments.startsWith("query") || withoutComments.startsWith("{"))
    return "query";

  // Check for shorthand query (no keyword)
  if (/^\s*\{/.test(withoutComments)) return "query";

  return "query";
}

/**
 * Extract the operation name from a GraphQL query string
 */
export function extractOperationName(query: string): string {
  // Strip comments and empty lines to find the actual operation
  const withoutComments = stripComments(query);

  // Match: query|mutation|subscription|fragment OperationName
  const match = withoutComments.match(
    /^(query|mutation|subscription|fragment)\s+(\w+)/,
  );
  if (match) {
    return match[2];
  }

  // Anonymous query
  return "Anonymous";
}

/**
 * Strip comments from GraphQL content
 */
function stripComments(content: string): string {
  return content
    .split("\n")
    .filter((line) => !line.trim().startsWith("#"))
    .join("\n")
    .trim();
}

/**
 * Extract variable definitions from a GraphQL query string
 */
export function extractVariableDefinitions(
  query: string,
): VariableDefinition[] {
  const variables: VariableDefinition[] = [];

  // Match the variable definition block: ($var1: Type!, $var2: Type)
  const varBlockMatch = query.match(/\(([^)]+)\)\s*\{/);
  if (!varBlockMatch) return variables;

  const varBlock = varBlockMatch[1];

  // Split by comma, handling nested types
  const varDefs = splitVariableDefinitions(varBlock);

  for (const varDef of varDefs) {
    const parsed = parseVariableDefinition(varDef.trim());
    if (parsed) {
      variables.push(parsed);
    }
  }

  return variables;
}

/**
 * Split variable definitions by comma, respecting nested brackets
 */
function splitVariableDefinitions(block: string): string[] {
  const parts: string[] = [];
  let current = "";
  let depth = 0;

  for (const char of block) {
    if (char === "[" || char === "(") {
      depth++;
      current += char;
    } else if (char === "]" || char === ")") {
      depth--;
      current += char;
    } else if (char === "," && depth === 0) {
      parts.push(current);
      current = "";
    } else {
      current += char;
    }
  }

  if (current.trim()) {
    parts.push(current);
  }

  return parts;
}

/**
 * Parse a single variable definition: $name: Type! = default
 */
function parseVariableDefinition(def: string): VariableDefinition | null {
  // Match: $name: Type! = default
  const match = def.match(/^\$(\w+)\s*:\s*(.+?)(?:\s*=\s*(.+))?$/);
  if (!match) return null;

  const [, name, typeStr, defaultStr] = match;
  const type = typeStr.trim();

  return {
    name,
    type,
    required: type.endsWith("!"),
    isList: type.startsWith("["),
    defaultValue: defaultStr
      ? parseDefaultValue(defaultStr.trim(), type)
      : undefined,
  };
}

/**
 * Parse a default value string into a typed value
 */
function parseDefaultValue(value: string, type: string): unknown {
  // Handle null
  if (value === "null") return null;

  // Handle boolean
  if (value === "true") return true;
  if (value === "false") return false;

  // Handle numbers
  if (/^-?\d+$/.test(value)) return parseInt(value, 10);
  if (/^-?\d+\.\d+$/.test(value)) return parseFloat(value);

  // Handle strings (quoted)
  if (value.startsWith('"') && value.endsWith('"')) {
    return value.slice(1, -1);
  }

  // Handle enums (unquoted strings)
  return value;
}

/**
 * Extract description from comment at the top of a .gql file
 */
export function extractDescription(content: string): string | undefined {
  const lines = content.trim().split("\n");
  const comments: string[] = [];

  for (const line of lines) {
    const trimmed = line.trim();
    if (trimmed.startsWith("#")) {
      comments.push(trimmed.slice(1).trim());
    } else if (trimmed === "" && comments.length > 0) {
      // Allow blank lines within comment block
      continue;
    } else {
      // Stop at first non-comment line
      break;
    }
  }

  return comments.length > 0 ? comments.join(" ") : undefined;
}

/**
 * Generate default variable values based on type
 */
export function generateDefaultVariables(
  variables: VariableDefinition[],
): Record<string, unknown> {
  const defaults: Record<string, unknown> = {};

  for (const v of variables) {
    if (v.defaultValue !== undefined) {
      defaults[v.name] = v.defaultValue;
      continue;
    }

    defaults[v.name] = getDefaultForType(v.type);
  }

  return defaults;
}

/**
 * Get a sensible default value for a GraphQL type
 */
function getDefaultForType(type: string): unknown {
  // Remove non-null markers and list brackets for base type
  const baseType = type.replace(/[!\[\]]/g, "").trim();

  // Handle list types
  if (type.startsWith("[")) {
    return [];
  }

  // Handle known scalar types
  switch (baseType) {
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
      return getDefaultForInputType(baseType);
  }
}

/**
 * Get default values for known GraphQL input types
 * These match the schema definitions in pkg/graphql/schema.graphql
 */
function getDefaultForInputType(typeName: string): unknown {
  // Calculate sensible time ranges
  const now = new Date();
  const oneDayAgo = new Date(now.getTime() - 24 * 60 * 60 * 1000);

  switch (typeName) {
    case "TimeRangeInput":
      return {
        start: oneDayAgo.toISOString(),
        end: now.toISOString(),
      };

    case "PaginationInput":
      return {
        limit: 50,
        offset: 0,
      };

    case "CreateStreamInput":
      return {
        name: "my-stream",
        description: "Stream description",
        record: false,
      };

    case "UpdateStreamInput":
      return {
        name: "updated-stream-name",
        description: "Updated description",
        record: false,
      };

    case "CreateClipInput":
      return {
        stream: "stream-name",
        title: "My Clip",
        description: "Clip description",
        startTime: 0,
        endTime: 30,
      };

    case "CreateStreamKeyInput":
      return {
        name: "primary-key",
      };

    case "CreateDeveloperTokenInput":
      return {
        name: "my-api-token",
        permissions: "read,write",
        expiresIn: null,
      };

    case "StartDvrInput":
      return {
        streamId: "your-stream-id",
      };

    case "StopDvrInput":
      return {
        streamId: "your-stream-id",
      };

    case "PaymentInput":
      return {
        amount: 1000,
        currency: "USD",
        method: "CARD",
      };

    case "UpdateTenantInput":
      return {
        name: "My Organization",
      };

    default:
      // For unknown input types, return empty object with a hint
      if (typeName.endsWith("Input")) {
        return {};
      }
      return null;
  }
}

/**
 * Parse a complete .gql file content into a ParsedOperation
 */
export function parseGqlFile(
  content: string,
  filePath?: string,
): ParsedOperation {
  const description = extractDescription(content);
  const type = extractOperationType(content);
  const name = extractOperationName(content);
  const variables = extractVariableDefinitions(content);

  return {
    name,
    type,
    query: content.trim(),
    variables,
    description,
    filePath,
  };
}

/**
 * Strip Houdini/client-only directives from a query
 * These directives are only valid on the client and will cause server errors
 */
export function stripClientDirectives(query: string): string {
  // List of Houdini-specific directives to remove
  const clientDirectives = [
    "@paginate",
    "@list",
    "@prepend",
    "@append",
    "@allLists",
    "@parentID",
    "@loading",
    "@required",
    "@optimisticKey",
    "@blocking",
    "@cache",
    "@mask",
  ];

  let result = query;

  for (const directive of clientDirectives) {
    // Remove directive with optional arguments: @directive or @directive(...)
    // This regex handles @directive, @directive(...), and preserves surrounding whitespace
    const pattern = new RegExp(
      `\\s*${directive.replace("@", "@")}(?:\\([^)]*\\))?`,
      "g",
    );
    result = result.replace(pattern, "");
  }

  // Clean up trailing spaces on lines (safe)
  result = result.replace(/ +$/gm, "");

  return result;
}

/**
 * Extract fragment spreads from a query (e.g., ...StreamCoreFields)
 */
export function extractFragmentSpreads(query: string): string[] {
  const fragments: string[] = [];
  const regex = /\.\.\.(\w+)/g;
  let match;
  while ((match = regex.exec(query)) !== null) {
    if (!fragments.includes(match[1])) {
      fragments.push(match[1]);
    }
  }
  return fragments;
}

/**
 * Get helpful description for common variable patterns
 */
export function getVariableHint(
  name: string,
  type: string,
): string | undefined {
  // Pagination variables
  if (name === "first" && type.includes("Int")) {
    return "Number of items per page";
  }
  if (name === "after" && type.includes("String")) {
    return "Cursor from pageInfo.endCursor";
  }
  if (name === "last" && type.includes("Int")) {
    return "Number of items from end";
  }
  if (name === "before" && type.includes("String")) {
    return "Cursor from pageInfo.startCursor";
  }

  // Common ID patterns
  if (name === "id" && type.includes("ID")) {
    return "Unique identifier";
  }
  if (name.endsWith("Id") && type.includes("ID")) {
    return `${name.replace(/Id$/, "")} identifier`;
  }

  // Time range
  if (type.includes("TimeRangeInput")) {
    return "{ start: ISO8601, end: ISO8601 }";
  }

  // Common filters
  if (name === "stream" && type.includes("String")) {
    return "Stream internal name filter";
  }
  if (name === "status") {
    return "Filter by status";
  }

  return undefined;
}

/**
 * Format a ParsedOperation for display as a template
 */
export function formatOperationForTemplate(op: ParsedOperation): {
  name: string;
  description: string;
  query: string;
  variables: Record<string, unknown>;
} {
  return {
    name: formatOperationName(op.name),
    description: op.description || `${capitalizeFirst(op.type)} operation`,
    query: stripClientDirectives(op.query),
    variables: generateDefaultVariables(op.variables),
  };
}

/**
 * Convert operation name to human-readable format
 * GetStreamAnalytics -> Get Stream Analytics
 */
function formatOperationName(name: string): string {
  return name
    .replace(/([A-Z])/g, " $1")
    .replace(/^./, (str) => str.toUpperCase())
    .trim();
}

function capitalizeFirst(str: string): string {
  return str.charAt(0).toUpperCase() + str.slice(1);
}

/**
 * SDK code snippets for the GraphQL playground.
 *
 * The operation list comes from the SDKs' own sources: the TypeScript SDK's
 * generated manifest names every operation the current SDK line ships, and
 * the public operation documents in pkg/graphql/public (the documents all
 * three SDKs are generated from) give each operation's variables in the
 * order the Go SDK takes them as positional arguments. The Python SDK's
 * public `livepeer_frameworks.graphql` module decides which input models a
 * snippet can import.
 */

import { Kind, parse, type OperationDefinitionNode, type TypeNode } from "graphql";

import type { IntrospectedSchema, TypeRef } from "./schemaUtils";

export type SdkOperationKind = "query" | "mutation" | "subscription";

export interface SdkVariable {
  name: string;
  type: TypeRef;
}

export interface SdkOperation {
  name: string;
  kind: SdkOperationKind;
  variables: SdkVariable[];
}

export interface SdkCatalog {
  operations: Map<string, SdkOperation>;
  /** Names exported by the Python SDK's public livepeer_frameworks.graphql module. */
  pythonExports: Set<string>;
}

export interface SdkSnippetInput {
  query: string;
  variables: Record<string, unknown>;
  token: string;
  catalog: SdkCatalog;
  /** Introspected schema; lets Go snippets spell out input structs field by field. */
  schema?: IntrospectedSchema | null;
  httpUrl: string;
  wsUrl: string;
  docsUrl: string;
}

export interface SdkSnippets {
  tsSdk: string;
  goSdk: string;
  pythonSdk: string;
}

// Lazy like the template loader: the documents load when the code panel opens.
const operationSources = import.meta.glob(
  [
    "../../../../../pkg/graphql/public/queries/**/*.graphql",
    "../../../../../pkg/graphql/public/mutations/**/*.graphql",
    "../../../../../pkg/graphql/public/subscriptions/**/*.graphql",
    "../../../../../pkg/graphql/public/generated/queries.graphql",
    "../../../../../pkg/graphql/public/generated/mutations.graphql",
    "../../../../../pkg/graphql/public/generated/subscriptions.graphql",
  ],
  { query: "?raw", import: "default", eager: false }
) as Record<string, () => Promise<string>>;

// livepeer_frameworks.graphql star-imports this generated module and its __all__.
const pythonGraphqlModule = import.meta.glob(
  "../../../../../sdk_python/src/livepeer_frameworks/_generated/public_exports.py",
  { query: "?raw", import: "default", eager: false }
) as Record<string, () => Promise<string>>;

let catalogPromise: Promise<SdkCatalog> | null = null;

/** Loads the SDK operation catalog once per page. */
export function loadSdkCatalog(): Promise<SdkCatalog> {
  if (!catalogPromise) {
    catalogPromise = (async () => {
      const [{ operations: manifest }, documents, pythonSources] = await Promise.all([
        import("../../../../../npm_api/src/generated/manifest"),
        Promise.all(Object.values(operationSources).map((load) => load())),
        Promise.all(Object.values(pythonGraphqlModule).map((load) => load())),
      ]);
      return buildSdkCatalog(
        Object.entries(manifest).map(([name, info]) => ({ name, kind: info.kind })),
        documents,
        pythonSources.join("\n")
      );
    })().catch((err) => {
      catalogPromise = null;
      throw err;
    });
  }
  return catalogPromise;
}

function typeNodeToRef(node: TypeNode): TypeRef {
  if (node.kind === Kind.NON_NULL_TYPE)
    return { kind: "NON_NULL", ofType: typeNodeToRef(node.type) };
  if (node.kind === Kind.LIST_TYPE) return { kind: "LIST", ofType: typeNodeToRef(node.type) };
  return { name: node.name.value };
}

/**
 * Builds the catalog from the manifest's operation list, the public operation
 * documents, and the source of the Python SDK's generated export list of
 * livepeer_frameworks.graphql.
 * An operation is in the catalog only when the manifest ships it and a
 * document defines it.
 */
export function buildSdkCatalog(
  manifest: ReadonlyArray<{ name: string; kind: string }>,
  documents: ReadonlyArray<string>,
  pythonGraphqlSource: string
): SdkCatalog {
  const shipped = new Map(manifest.map((op) => [op.name, op.kind]));
  const operations = new Map<string, SdkOperation>();
  for (const source of documents) {
    for (const def of parse(source).definitions) {
      if (def.kind !== Kind.OPERATION_DEFINITION || !def.name) continue;
      const name = def.name.value;
      if (shipped.get(name) !== def.operation) continue;
      operations.set(name, {
        name,
        kind: def.operation,
        variables: (def.variableDefinitions ?? []).map((v) => ({
          name: v.variable.name.value,
          type: typeNodeToRef(v.type),
        })),
      });
    }
  }
  return { operations, pythonExports: parsePythonAll(pythonGraphqlSource) };
}

function parsePythonAll(source: string): Set<string> {
  const block = /__all__\s*=\s*\[([\s\S]*?)\]/.exec(source);
  const names = new Set<string>();
  if (!block) return names;
  for (const match of block[1].matchAll(/"([A-Za-z_][A-Za-z0-9_]*)"/g)) {
    names.add(match[1]);
  }
  return names;
}

// ---------------------------------------------------------------------------
// Naming
// ---------------------------------------------------------------------------

const PYTHON_KEYWORDS = new Set([
  "False",
  "None",
  "True",
  "and",
  "as",
  "assert",
  "async",
  "await",
  "break",
  "class",
  "continue",
  "def",
  "del",
  "elif",
  "else",
  "except",
  "finally",
  "for",
  "from",
  "global",
  "if",
  "import",
  "in",
  "is",
  "lambda",
  "nonlocal",
  "not",
  "or",
  "pass",
  "raise",
  "return",
  "try",
  "while",
  "with",
  "yield",
]);

// Lowercase names of Python builtin types, which ariadne-codegen also suffixes.
const PYTHON_BUILTIN_TYPES = new Set([
  "bool",
  "bytearray",
  "bytes",
  "classmethod",
  "complex",
  "dict",
  "enumerate",
  "filter",
  "float",
  "frozenset",
  "int",
  "list",
  "map",
  "memoryview",
  "object",
  "property",
  "range",
  "reversed",
  "set",
  "slice",
  "staticmethod",
  "str",
  "super",
  "tuple",
  "type",
  "zip",
]);

/**
 * ariadne-codegen's process_name (str_to_snake_case plus a trailing
 * underscore for keywords and builtin type names), which names the Python
 * SDK's methods and keyword arguments: GetNodeMetrics1hConnection becomes
 * get_node_metrics_1_h_connection and filter becomes filter_.
 */
export function pythonSnakeCase(name: string): string {
  const words = name.match(/[A-Z]?[a-z]+|[A-Z]+(?=[A-Z][a-z]|\d|\W|_|$)|\d+/g) ?? [];
  const snake = words.map((w) => w.toLowerCase()).join("_");
  return PYTHON_KEYWORDS.has(snake) || PYTHON_BUILTIN_TYPES.has(snake) ? `${snake}_` : snake;
}

function upperFirst(s: string): string {
  return s.charAt(0).toUpperCase() + s.slice(1);
}

/** genqlient's enum constant: type name plus each underscore-separated word capitalized. */
function goEnumConst(typeName: string, value: string): string {
  const words = value
    .split("_")
    .filter(Boolean)
    .map((w) => upperFirst(w.toLowerCase()));
  return `frameworks.${typeName}${words.join("")}`;
}

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

const GO_SCALARS: Record<string, string> = {
  String: "string",
  ID: "string",
  Int: "int",
  Float: "float64",
  Boolean: "bool",
  Time: "time.Time",
  JSON: "json.RawMessage",
  Currency: "string",
  Money: "string",
};

type NamedKind = "scalar" | "enum" | "input";

function namedKind(name: string, schema: IntrospectedSchema | null | undefined): NamedKind {
  if (name in GO_SCALARS) return "scalar";
  const found = schema?.types?.find((t) => t.name === name);
  if (found?.kind === "ENUM") return "enum";
  if (found?.kind === "INPUT_OBJECT") return "input";
  if (found?.kind === "SCALAR") return "scalar";
  // Without a schema: public input types end in Input or Filter; every other
  // non-scalar variable type is an enum.
  return /(Input|Filter)$/.test(name) ? "input" : "enum";
}

function isNonNull(type: TypeRef): boolean {
  return type.kind === "NON_NULL";
}

function unwrapNonNull(type: TypeRef): TypeRef {
  return type.kind === "NON_NULL" && type.ofType ? type.ofType : type;
}

// ---------------------------------------------------------------------------
// Value placeholders for required variables the editor leaves out
// ---------------------------------------------------------------------------

/**
 * A placeholder for a required value: an input object gets its required
 * fields (non-null without a default), so the snippet type-checks once the
 * values are filled in.
 */
function placeholderValue(
  type: TypeRef,
  schema: IntrospectedSchema | null | undefined,
  depth = 0
): unknown {
  const inner = unwrapNonNull(type);
  if (inner.kind === "LIST") return [];
  const name = inner.name ?? "String";
  switch (namedKind(name, schema)) {
    case "input": {
      const out: Record<string, unknown> = {};
      const fields = schema?.types?.find((t) => t.name === name)?.inputFields ?? [];
      if (depth >= 4) return out;
      for (const field of fields) {
        if (field.type && isNonNull(field.type) && field.defaultValue == null) {
          out[field.name] = placeholderValue(field.type, schema, depth + 1);
        }
      }
      return out;
    }
    case "enum": {
      const values = schema?.types?.find((t) => t.name === name)?.enumValues;
      return values?.[0]?.name ?? "";
    }
    default:
      if (name === "Int" || name === "Float") return 0;
      if (name === "Boolean") return false;
      return "";
  }
}

/**
 * The value each SDK variable gets: the editor's value, a placeholder for a
 * required variable it leaves out, or undefined for an optional one.
 */
function argumentValues(
  op: SdkOperation,
  variables: Record<string, unknown>,
  schema: IntrospectedSchema | null | undefined
): Array<{ variable: SdkVariable; value: unknown; provided: boolean }> {
  return op.variables.map((variable) => {
    const provided = Object.prototype.hasOwnProperty.call(variables, variable.name);
    if (provided) return { variable, value: variables[variable.name], provided };
    if (isNonNull(variable.type)) {
      return { variable, value: placeholderValue(variable.type, schema), provided };
    }
    return { variable, value: undefined, provided };
  });
}

function ignoredVariables(op: SdkOperation, variables: Record<string, unknown>): string[] {
  const known = new Set(op.variables.map((v) => v.name));
  return Object.keys(variables).filter((k) => !known.has(k));
}

// ---------------------------------------------------------------------------
// Language renderers: values
// ---------------------------------------------------------------------------

function indentLines(text: string, prefix: string): string {
  return text
    .split("\n")
    .map((line, i) => (i === 0 ? line : prefix + line))
    .join("\n");
}

function tsLiteral(value: unknown): string {
  return JSON.stringify(value, null, 2);
}

function pyLiteral(value: unknown, indent = ""): string {
  if (value === null || value === undefined) return "None";
  if (value === true) return "True";
  if (value === false) return "False";
  if (typeof value === "number") return String(value);
  if (typeof value === "string") return JSON.stringify(value);
  const next = indent + "    ";
  if (Array.isArray(value)) {
    if (value.length === 0) return "[]";
    return `[\n${value.map((v) => `${next}${pyLiteral(v, next)},`).join("\n")}\n${indent}]`;
  }
  const entries = Object.entries(value as Record<string, unknown>);
  if (entries.length === 0) return "{}";
  return `{\n${entries
    .map(([k, v]) => `${next}${JSON.stringify(k)}: ${pyLiteral(v, next)},`)
    .join("\n")}\n${indent}}`;
}

interface GoContext {
  schema: IntrospectedSchema | null | undefined;
  imports: Set<string>;
  /** Statements that run before the call, e.g. decoding an input without a schema. */
  preamble: string[];
}

const GO_MONTHS = [
  "January",
  "February",
  "March",
  "April",
  "May",
  "June",
  "July",
  "August",
  "September",
  "October",
  "November",
  "December",
];

function goString(value: string): string {
  if (!value.includes("`") && !value.includes("\r") && value.includes("\n")) return `\`${value}\``;
  return JSON.stringify(value);
}

function goTime(value: unknown, ctx: GoContext): string {
  ctx.imports.add("time");
  const date = typeof value === "string" ? new Date(value) : null;
  if (!date || Number.isNaN(date.getTime())) return "time.Time{}";
  const ns = date.getUTCMilliseconds() * 1_000_000;
  return `time.Date(${date.getUTCFullYear()}, time.${GO_MONTHS[date.getUTCMonth()]}, ${date.getUTCDate()}, ${date.getUTCHours()}, ${date.getUTCMinutes()}, ${date.getUTCSeconds()}, ${ns}, time.UTC)`;
}

function goScalar(name: string, value: unknown, ctx: GoContext): string {
  switch (name) {
    case "Int":
      return typeof value === "number" ? String(Math.trunc(value)) : "0";
    case "Float":
      if (typeof value !== "number") return "0.0";
      return Number.isInteger(value) ? `${value}.0` : String(value);
    case "Boolean":
      return value === true ? "true" : "false";
    case "Time":
      return goTime(value, ctx);
    case "JSON": {
      ctx.imports.add("encoding/json");
      const raw = JSON.stringify(value ?? null);
      return raw.includes("`")
        ? `json.RawMessage(${JSON.stringify(raw)})`
        : `json.RawMessage(\`${raw}\`)`;
    }
    default:
      return goString(typeof value === "string" ? value : String(value ?? ""));
  }
}

/** The Go type genqlient generates for a GraphQL type under optional: pointer. */
function goTypeName(type: TypeRef, ctx: GoContext): string {
  if (type.kind === "NON_NULL" && type.ofType) {
    const inner = type.ofType;
    if (inner.kind === "LIST" && inner.ofType) return `[]${goTypeName(inner.ofType, ctx)}`;
    return goNamedType(inner.name ?? "String", ctx);
  }
  if (type.kind === "LIST" && type.ofType) return `[]${goTypeName(type.ofType, ctx)}`;
  return `*${goNamedType(type.name ?? "String", ctx)}`;
}

function goNamedType(name: string, ctx: GoContext): string {
  const scalar = GO_SCALARS[name];
  if (scalar) {
    if (scalar.startsWith("time.")) ctx.imports.add("time");
    if (scalar.startsWith("json.")) ctx.imports.add("encoding/json");
    return scalar;
  }
  return `frameworks.${name}`;
}

/**
 * Renders value as a Go expression of the type genqlient generates for type:
 * nullable scalars and enums become pointers (new(v)), nullable inputs &T{},
 * lists slices. Returns null for an input object the schema cannot describe.
 */
function goValue(type: TypeRef, value: unknown, ctx: GoContext, indent: string): string | null {
  if (type.kind === "LIST" || (type.kind === "NON_NULL" && type.ofType?.kind === "LIST")) {
    const listType = unwrapNonNull(type);
    if (value === null || value === undefined) return "nil";
    const items = Array.isArray(value) ? value : [value];
    const elemType = listType.ofType ?? { name: "String" };
    const rendered: string[] = [];
    for (const item of items) {
      const r = goValue(elemType, item, ctx, indent + "\t");
      if (r === null) return null;
      rendered.push(r);
    }
    const typeName = `[]${goTypeName(elemType, ctx)}`;
    if (rendered.length === 0) return `${typeName}{}`;
    const oneLine = `${typeName}{${rendered.join(", ")}}`;
    if (oneLine.length <= 80 && !oneLine.includes("\n")) return oneLine;
    return `${typeName}{\n${rendered.map((r) => `${indent}\t${r},`).join("\n")}\n${indent}}`;
  }

  const nonNull = isNonNull(type);
  const named = unwrapNonNull(type);
  const name = named.name ?? "String";
  if (!nonNull && (value === null || value === undefined)) return "nil";

  const kind = namedKind(name, ctx.schema);
  if (kind === "input") {
    const literal = goStruct(name, value, ctx, indent);
    if (literal === null) return null;
    return nonNull ? literal : `&${literal}`;
  }
  const plain = kind === "enum" ? goEnum(name, value) : goScalar(name, value, ctx);
  return nonNull ? plain : `new(${plain})`;
}

function goEnum(typeName: string, value: unknown): string {
  if (typeof value !== "string" || value === "") return `frameworks.${typeName}("")`;
  return goEnumConst(typeName, value);
}

function goStruct(name: string, value: unknown, ctx: GoContext, indent: string): string | null {
  const inputType = ctx.schema?.types?.find((t) => t.name === name && t.kind === "INPUT_OBJECT");
  if (!inputType) return null;
  const fields = new Map((inputType.inputFields ?? []).map((f) => [f.name, f]));
  const obj = value && typeof value === "object" && !Array.isArray(value) ? value : {};
  const entries: Array<{ key: string; value: string }> = [];
  for (const [key, fieldValue] of Object.entries(obj as Record<string, unknown>)) {
    const field = fields.get(key);
    if (!field?.type) continue;
    const rendered = goValue(field.type, fieldValue, ctx, indent + "\t");
    if (rendered === null) return null;
    entries.push({ key: `${upperFirst(key)}:`, value: rendered });
  }
  if (entries.length === 0) return `frameworks.${name}{}`;
  return `frameworks.${name}{\n${alignGoKeys(entries, `${indent}\t`)}\n${indent}}`;
}

/**
 * Lays out key: value lines the way gofmt aligns them: values line up
 * within a run of single-line entries; an entry with a multi-line value
 * stands alone.
 */
function alignGoKeys(entries: Array<{ key: string; value: string }>, indent: string): string {
  const lines: string[] = [];
  let run: Array<{ key: string; value: string }> = [];
  const flush = () => {
    const width = Math.max(...run.map((e) => e.key.length));
    for (const e of run) lines.push(`${indent}${e.key.padEnd(width)} ${e.value},`);
    run = [];
  };
  for (const entry of entries) {
    if (entry.value.includes("\n")) {
      if (run.length) flush();
      run.push(entry);
      flush();
    } else {
      run.push(entry);
    }
  }
  if (run.length) flush();
  return lines.join("\n");
}

/** Renders one Go argument; an input the schema cannot describe is decoded from JSON first. */
function goArgument(variable: SdkVariable, value: unknown, ctx: GoContext, indent: string): string {
  const rendered = goValue(variable.type, value, ctx, indent);
  if (rendered !== null) return rendered;
  ctx.imports.add("encoding/json");
  const typeName = goTypeName(variable.type, ctx).replace(/^\*/, "");
  const raw = JSON.stringify(value ?? null);
  const quoted = raw.includes("`") ? JSON.stringify(raw) : `\`${raw}\``;
  ctx.preamble.push(
    `var ${variable.name} ${typeName}`,
    `if err := json.Unmarshal([]byte(${quoted}), &${variable.name}); err != nil {`,
    `\tlog.Fatal(err)`,
    `}`
  );
  const pointer = !isNonNull(variable.type) && unwrapNonNull(variable.type).kind !== "LIST";
  return pointer ? `&${variable.name}` : variable.name;
}

/** An untyped Go literal (map[string]any, []any) for a custom document's variables. */
function goAny(value: unknown, indent: string): string {
  if (value === null || value === undefined) return "nil";
  if (typeof value === "string") return goString(value);
  if (typeof value === "number" || typeof value === "boolean") return String(value);
  const next = indent + "\t";
  if (Array.isArray(value)) {
    if (value.length === 0) return "[]any{}";
    return `[]any{\n${value.map((v) => `${next}${goAny(v, next)},`).join("\n")}\n${indent}}`;
  }
  const entries = Object.entries(value as Record<string, unknown>);
  if (entries.length === 0) return "map[string]any{}";
  return `map[string]any{\n${entries
    .map(([k, v]) => `${next}${JSON.stringify(k)}: ${goAny(v, next)},`)
    .join("\n")}\n${indent}}`;
}

function goImports(ctx: GoContext, extra: string[]): string {
  const std = new Set(["context", "fmt", "log", ...ctx.imports]);
  const thirdParty = ['frameworks "github.com/Livepeer-FrameWorks/sdk-go"', ...extra];
  const stdLines = [...std].sort().map((p) => `\t"${p}"`);
  const importPath = (p: string) => p.replace(/^\S+ /, "").replace(/"/g, "");
  const thirdLines = thirdParty
    .sort((a, b) => importPath(a).localeCompare(importPath(b)))
    .map((p) => `\t${p.includes(" ") ? p : `"${p}"`}`);
  return `import (\n${stdLines.join("\n")}\n\n${thirdLines.join("\n")}\n)`;
}

// ---------------------------------------------------------------------------
// Snippets
// ---------------------------------------------------------------------------

interface ParsedDocument {
  name: string | null;
  kind: SdkOperationKind;
}

function parseDocument(query: string): ParsedDocument | null {
  try {
    const def = parse(query).definitions.find(
      (d): d is OperationDefinitionNode => d.kind === Kind.OPERATION_DEFINITION
    );
    if (!def) return null;
    return { name: def.name?.value ?? null, kind: def.operation };
  } catch {
    return null;
  }
}

/** The SDK operation the editor's document runs, matched by operation name and kind. */
export function matchSdkOperation(query: string, catalog: SdkCatalog): SdkOperation | null {
  const parsed = parseDocument(query);
  if (!parsed?.name) return null;
  const op = catalog.operations.get(parsed.name);
  return op && op.kind === parsed.kind ? op : null;
}

function header(
  comment: string,
  language: string,
  install: string,
  docsUrl: string,
  note?: string
): string {
  const lines = [`${comment} ${language}: ${install}`, `${comment} Guide: ${docsUrl}`];
  if (note) lines.push(...note.split("\n").map((l) => `${comment} ${l}`));
  return lines.join("\n");
}

const MATCHED_NOTE = (name: string) =>
  `${name} is a generated SDK operation;\nit returns the SDK's default selection set.`;
const CUSTOM_NOTE =
  "No SDK operation has this document's name, so it runs as a custom document.\n" +
  "Give it a name of your own; the server version check matches SDK operation names.";

function ignoredNote(ignored: string[]): string | undefined {
  if (ignored.length === 0) return undefined;
  return `Not a variable of the SDK operation, left out: ${ignored.join(", ")}.`;
}

function joinNotes(...notes: Array<string | undefined>): string | undefined {
  const present = notes.filter((n): n is string => Boolean(n));
  return present.length ? present.join("\n") : undefined;
}

function tsSnippet(input: SdkSnippetInput, op: SdkOperation | null, doc: ParsedDocument | null) {
  const { token, httpUrl, wsUrl, docsUrl } = input;
  const guide = `${docsUrl}/builders/sdks`;
  const isSubscription = (op?.kind ?? doc?.kind) === "subscription";
  const install = isSubscription
    ? "npm install @livepeer-frameworks/api graphql-ws"
    : "npm install @livepeer-frameworks/api";

  let vars: Record<string, unknown> = input.variables;
  let note: string | undefined;
  if (op) {
    vars = {};
    for (const { variable, value, provided } of argumentValues(op, input.variables, input.schema)) {
      if (provided || value !== undefined) vars[variable.name] = value;
    }
    note = joinNotes(MATCHED_NOTE(op.name), ignoredNote(ignoredVariables(op, input.variables)));
  } else {
    note = CUSTOM_NOTE;
  }
  const hasVars = Object.keys(vars).length > 0;
  const docRef = op ? `${op.name}Document` : "query";
  const customQuery = op
    ? ""
    : `const query = \`${input.query.replace(/[`\\]/g, "\\$&").replace(/\$\{/g, "\\${")}\`;\n\n`;
  const head = header("//", "TypeScript SDK", install, guide, note);

  if (isSubscription) {
    const imports = op
      ? `import { ${docRef} } from "@livepeer-frameworks/api";\nimport { createSubscriptionClient } from "@livepeer-frameworks/api/subscriptions";`
      : `import { createSubscriptionClient } from "@livepeer-frameworks/api/subscriptions";`;
    const call = hasVars
      ? `subscriptions.subscribe(${docRef}, ${tsLiteral(vars)})`
      : `subscriptions.subscribe(${docRef})`;
    return `${head}
${imports}

const subscriptions = createSubscriptionClient({
  url: ${JSON.stringify(wsUrl)},
  token: ${JSON.stringify(token)},
});

${customQuery}for await (const data of ${call}) {
  console.log(data);
}`;
  }

  const imports = op
    ? `import { createClient, ${docRef} } from "@livepeer-frameworks/api";`
    : `import { createClient } from "@livepeer-frameworks/api";`;
  const call = hasVars
    ? `client.request(${docRef}, ${tsLiteral(vars)})`
    : `client.request(${docRef})`;
  return `${head}
${imports}

const client = createClient({
  url: ${JSON.stringify(httpUrl)},
  token: ${JSON.stringify(token)},
});

${customQuery}const data = await ${call};
console.log(data);`;
}

function goSnippet(input: SdkSnippetInput, op: SdkOperation | null, doc: ParsedDocument | null) {
  const { token, httpUrl, wsUrl, docsUrl } = input;
  const guide = `${docsUrl}/builders/sdks`;
  const ctx: GoContext = { schema: input.schema, imports: new Set(), preamble: [] };
  const isSubscription = (op?.kind ?? doc?.kind) === "subscription";
  const extraImports: string[] = [];

  const clientSetup = isSubscription
    ? `\tsc, err := frameworks.NewSubscriptionClient(frameworks.SubscriptionOptions{
\t\tURL:   ${JSON.stringify(wsUrl)},
\t\tToken: ${JSON.stringify(token)},
\t})
\tif err != nil {
\t\tlog.Fatal(err)
\t}`
    : `\tclient, err := frameworks.NewClient(frameworks.ClientOptions{
\t\tURL:   ${JSON.stringify(httpUrl)},
\t\tToken: ${JSON.stringify(token)},
\t})
\tif err != nil {
\t\tlog.Fatal(err)
\t}`;

  let body: string;
  let note: string | undefined;
  if (op) {
    note = joinNotes(MATCHED_NOTE(op.name), ignoredNote(ignoredVariables(op, input.variables)));
    const args = argumentValues(op, input.variables, input.schema).map(({ variable, value }) =>
      goArgument(variable, value, ctx, "\t")
    );
    const receiver = isSubscription ? "sc" : "client";
    const fn = isSubscription ? `frameworks.Subscribe${op.name}` : `frameworks.${op.name}`;
    const allArgs = ["ctx", receiver, ...args];
    const oneLine = allArgs.join(", ");
    const argList =
      oneLine.length <= 60 && !oneLine.includes("\n")
        ? oneLine
        : `\n${allArgs.map((a) => `\t\t${indentLines(a, "\t")},`).join("\n")}\n\t`;
    const preamble = ctx.preamble.length ? `${ctx.preamble.map((l) => `\t${l}`).join("\n")}\n` : "";
    body = isSubscription
      ? `${preamble}\tfor event, err := range ${fn}(${argList}) {
\t\tif err != nil {
\t\t\tlog.Fatal(err)
\t\t}
\t\tfmt.Printf("%+v\\n", event)
\t}`
      : `${preamble}\tresp, err := ${fn}(${argList})
\tif err != nil {
\t\tlog.Fatal(err)
\t}
\tfmt.Printf("%+v\\n", resp)`;
  } else {
    note = CUSTOM_NOTE;
    const queryLiteral = input.query.includes("`")
      ? JSON.stringify(input.query)
      : `\`${input.query}\``;
    const varsLiteral =
      Object.keys(input.variables).length > 0 ? goAny(input.variables, "\t") : "nil";
    const opName = JSON.stringify(doc?.name ?? "");
    if (isSubscription) {
      const varsSetup = varsLiteral === "nil" ? "" : `\tvariables := ${varsLiteral}\n`;
      const varsArg = varsLiteral === "nil" ? "nil" : "variables";
      body = `\tquery := ${queryLiteral}
${varsSetup}\tfor event, err := range frameworks.Subscribe[map[string]any](ctx, sc, ${opName}, query, ${varsArg}) {
\t\tif err != nil {
\t\t\tlog.Fatal(err)
\t\t}
\t\tfmt.Printf("%+v\\n", *event)
\t}`;
    } else {
      extraImports.push("github.com/Khan/genqlient/graphql");
      body = `\tvar data map[string]any
\terr = client.MakeRequest(ctx, &graphql.Request{
${alignGoKeys(
  [
    { key: "Query:", value: queryLiteral },
    { key: "OpName:", value: opName },
    { key: "Variables:", value: indentLines(varsLiteral, "\t") },
  ],
  "\t\t"
)}
\t}, &graphql.Response{Data: &data})
\tif err != nil {
\t\tlog.Fatal(err)
\t}
\tfmt.Printf("%+v\\n", data)`;
    }
  }

  return `${header("//", "Go SDK", "go get github.com/Livepeer-FrameWorks/sdk-go", guide, note)}
package main

${goImports(ctx, extraImports)}

func main() {
\tctx := context.Background()
${clientSetup}

${body}
}`;
}

interface PyContext {
  schema: IntrospectedSchema | null | undefined;
  exports: Set<string>;
  modelImports: Set<string>;
  stdImports: Set<string>;
}

/**
 * Renders a Python SDK argument: exported input models through
 * model_validate, exported enums as members, Time as datetime. Inputs and
 * enums the public livepeer_frameworks.graphql module does not export are
 * passed as their JSON values, which the transport sends unchanged.
 */
function pyValue(type: TypeRef, value: unknown, indent: string, ctx: PyContext): string {
  if (value === null || value === undefined) return "None";
  const inner = unwrapNonNull(type);
  if (inner.kind === "LIST") {
    const elemType = inner.ofType ?? { name: "String" };
    const items = (Array.isArray(value) ? value : [value]).map((v) =>
      pyValue(elemType, v, indent + "    ", ctx)
    );
    if (items.length === 0) return "[]";
    const oneLine = `[${items.join(", ")}]`;
    if (oneLine.length <= 60 && !oneLine.includes("\n")) return oneLine;
    return `[\n${items.map((i) => `${indent}    ${i},`).join("\n")}\n${indent}]`;
  }
  const name = inner.name ?? "String";
  const kind = namedKind(name, ctx.schema);
  if (kind === "input" && ctx.exports.has(name)) {
    ctx.modelImports.add(name);
    const literal = pyLiteral(value, indent + "    ");
    return literal.includes("\n")
      ? `${name}.model_validate(\n${indent}    ${literal}\n${indent})`
      : `${name}.model_validate(${literal})`;
  }
  if (
    kind === "enum" &&
    ctx.exports.has(name) &&
    typeof value === "string" &&
    /^[A-Z_][A-Z0-9_]*$/.test(value)
  ) {
    ctx.modelImports.add(name);
    return `${name}.${value}`;
  }
  if (name === "Time" && typeof value === "string") {
    ctx.stdImports.add("from datetime import datetime");
    return `datetime.fromisoformat(${JSON.stringify(value)})`;
  }
  return pyLiteral(value, indent);
}

function pySnippet(input: SdkSnippetInput, op: SdkOperation | null, doc: ParsedDocument | null) {
  const { token, httpUrl, wsUrl, docsUrl, catalog } = input;
  const guide = `${docsUrl}/builders/sdks`;
  const isSubscription = (op?.kind ?? doc?.kind) === "subscription";
  const modelImports = new Set<string>();
  const stdImports = new Set<string>();

  const defaultWs = httpUrl.replace(/^http(s?):\/\//, "ws$1://").replace(/\/+$/, "") + "/ws";
  const clientArgs = [`url=${JSON.stringify(httpUrl)}`];
  if (isSubscription && wsUrl !== defaultWs) clientArgs.push(`ws_url=${JSON.stringify(wsUrl)}`);
  clientArgs.push(`token=${JSON.stringify(token)}`);
  // One line when it fits ruff's 88 columns, one argument per line otherwise.
  const clientCall = (indent: string, withKeyword: string, className: string) => {
    const oneLine = `${indent}${withKeyword} ${className}(${clientArgs.join(", ")}) as fw:`;
    if (oneLine.length <= 88) return oneLine;
    const args = clientArgs.map((a) => `${indent}    ${a},`).join("\n");
    return `${indent}${withKeyword} ${className}(\n${args}\n${indent}) as fw:`;
  };

  let call: string;
  let note: string | undefined;
  if (op) {
    note = joinNotes(MATCHED_NOTE(op.name), ignoredNote(ignoredVariables(op, input.variables)));
    const kwargs: string[] = [];
    const indent = isSubscription ? "            " : "        ";
    for (const { variable, value, provided } of argumentValues(op, input.variables, input.schema)) {
      if (!provided && value === undefined) continue;
      const kw = pythonSnakeCase(variable.name);
      const rendered = pyValue(variable.type, value, indent, {
        schema: input.schema,
        exports: catalog.pythonExports,
        modelImports,
        stdImports,
      });
      kwargs.push(`${kw}=${rendered}`);
    }
    const method = pythonSnakeCase(op.name);
    const oneLine = kwargs.join(", ");
    // The call line as ruff would keep it within its default 88 columns.
    const linePrefix = isSubscription ? "        async for event in " : "    result = ";
    const lineSuffix = isSubscription ? "):" : ")";
    const argList =
      `${linePrefix}fw.${method}(${oneLine}${lineSuffix}`.length <= 88 && !oneLine.includes("\n")
        ? oneLine
        : `\n${kwargs.map((k) => `${indent}${k},`).join("\n")}\n${indent.slice(4)}`;
    call = `fw.${method}(${argList})`;
  } else {
    note = CUSTOM_NOTE;
    call = "";
  }

  const hasVars = Object.keys(input.variables).length > 0;
  const customSetup = op
    ? ""
    : `query = """${input.query.replace(/\\/g, "\\\\").replace(/"""/g, '\\"\\"\\"')}"""\n${
        hasVars ? `variables = ${pyLiteral(input.variables)}\n` : ""
      }\n`;
  const opNameArg = doc?.name ? `, operation_name=${JSON.stringify(doc.name)}` : "";
  const varsArg = hasVars ? ", variables=variables" : "";
  const head = header("#", "Python SDK", "pip install livepeer-frameworks", guide, note);
  const modelLine = modelImports.size
    ? `\nfrom livepeer_frameworks.graphql import ${[...modelImports].sort().join(", ")}`
    : "";

  if (isSubscription) {
    const iterator = op ? call : `fw.execute_ws(query${opNameArg}${varsArg})`;
    const std = ["import asyncio", ...stdImports].sort().join("\n");
    return `${head}
${std}

from livepeer_frameworks import AsyncFrameWorksClient${modelLine}

${customSetup}
async def main() -> None:
${clientCall("    ", "async with", "AsyncFrameWorksClient")}
        async for event in ${iterator}:
            print(event)


asyncio.run(main())`;
  }

  const body = op
    ? `    result = ${call}\n    print(result)`
    : `    response = fw.execute(query${opNameArg}${varsArg})\n    print(fw.get_data(response))`;
  const std = stdImports.size ? `${[...stdImports].sort().join("\n")}\n\n` : "";
  return `${head}
${std}from livepeer_frameworks import FrameWorksClient${modelLine}

${customSetup}${clientCall("", "with", "FrameWorksClient")}
${body}`;
}

/**
 * Generates the TypeScript, Go, and Python SDK snippets for the editor's
 * document: a typed call when its operation name and kind match an SDK
 * operation, the SDK's custom-document path otherwise.
 */
export function generateSdkSnippets(input: SdkSnippetInput): SdkSnippets {
  const doc = parseDocument(input.query);
  const op = matchSdkOperation(input.query, input.catalog);
  return {
    tsSdk: tsSnippet(input, op, doc),
    goSdk: goSnippet(input, op, doc),
    pythonSdk: pySnippet(input, op, doc),
  };
}

/**
 * SDK snippets share the generated public catalog's documents and identities.
 * Variable order follows the Go SDK; Python model imports follow its public exports.
 */

import {
  Kind,
  parse,
  getOperationAST,
  type FragmentDefinitionNode,
  type OperationDefinitionNode,
  visit,
} from "graphql";

import { documentIdentity } from "../../../../../npm_api/src/identity";
import { version as sdkVersion } from "../../../../../pkg/graphql/public/catalog.json";
import type { IntrospectedSchema, TypeRef } from "./schemaUtils";

export type SdkOperationKind = "query" | "mutation" | "subscription";

export interface SdkVariable {
  name: string;
  type: TypeRef;
  hasDefault: boolean;
  required: boolean;
}

export interface SdkOperation {
  name: string;
  kind: SdkOperationKind;
  variables: SdkVariable[];
  identity: string;
  document: string;
}

export interface SdkCatalog {
  operations: Map<string, SdkOperation>;
  /** Names exported by the Python SDK's public livepeer_frameworks.graphql module. */
  pythonExports: Set<string>;
}

export interface SdkSnippetInput {
  query: string;
  operationName?: string;
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
      const [catalog, pythonSources] = await Promise.all([
        import("../../../../../pkg/graphql/public/catalog.json"),
        Promise.all(Object.values(pythonGraphqlModule).map((load) => load())),
      ]);
      const operations = catalog.operations as unknown as SdkOperation[];
      return {
        operations: new Map(operations.map((operation) => [operation.name, operation])),
        pythonExports: parsePythonAll(pythonSources.join("\n")),
      };
    })().catch((err) => {
      catalogPromise = null;
      throw err;
    });
  }
  return catalogPromise;
}

function reachableFragments(
  operation: OperationDefinitionNode,
  fragments: Map<string, FragmentDefinitionNode>
): FragmentDefinitionNode[] {
  const used = new Set<string>();
  const collect = (node: OperationDefinitionNode | FragmentDefinitionNode): void => {
    visit(node, {
      FragmentSpread(spread) {
        const name = spread.name.value;
        if (used.has(name)) return;
        const fragment = fragments.get(name);
        if (!fragment) throw new TypeError(`Missing fragment ${name}`);
        used.add(name);
        collect(fragment);
      },
    });
  };
  collect(operation);
  return [...used].sort().map((name) => fragments.get(name)!);
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
// Values supplied by the editor
// ---------------------------------------------------------------------------

/** Distinguish a supplied value from an absent optional variable. */
function argumentValues(
  op: SdkOperation,
  variables: Record<string, unknown>,
  _schema: IntrospectedSchema | null | undefined
): Array<{ variable: SdkVariable; value: unknown; provided: boolean }> {
  return op.variables.map((variable) => {
    const provided = Object.prototype.hasOwnProperty.call(variables, variable.name);
    if (provided) return { variable, value: variables[variable.name], provided };
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
  // Computed keys preserve own __proto__ properties without changing prototypes.
  return JSON.stringify(value, null, 2).replaceAll('"__proto__":', '["__proto__"]:');
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

export function goString(value: string): string {
  // Go source forbids embedded BOMs and JSON's surrogate escape syntax.
  return (
    '"' +
    Array.from(value, (character) => {
      const point = character.codePointAt(0)!;
      if (point >= 0xd800 && point <= 0xdfff) return "\\ufffd";
      if (point < 0x7f) return JSON.stringify(character).slice(1, -1);
      return point > 0xffff
        ? `\\U${point.toString(16).padStart(8, "0")}`
        : `\\u${point.toString(16).padStart(4, "0")}`;
    }).join("") +
    '"'
  );
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
      return Number.isInteger(value) && !String(value).includes("e") ? `${value}.0` : String(value);
    case "Boolean":
      return value === true ? "true" : "false";
    case "Time":
      return goTime(value, ctx);
    case "JSON": {
      ctx.imports.add("encoding/json");
      const raw = JSON.stringify(value ?? null);
      return `json.RawMessage(${goString(raw)})`;
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
  const quoted = goString(raw);
  ctx.preamble.push(
    `var ${variable.name} ${typeName}`,
    `if err := json.Unmarshal([]byte(${quoted}), &${variable.name}); err != nil {`,
    `\tlog.Fatal(err)`,
    `}`
  );
  const pointer = !isNonNull(variable.type) && unwrapNonNull(variable.type).kind !== "LIST";
  return pointer ? `&${variable.name}` : variable.name;
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

function parseDocument(query: string, operationName?: string): ParsedDocument | null {
  try {
    const def = getOperationAST(parse(query), operationName);
    return def ? { name: def.name?.value ?? null, kind: def.operation } : null;
  } catch {
    return null;
  }
}

/** A packaged operation is usable only for the same executable document. */
export function matchSdkOperation(
  query: string,
  catalog: SdkCatalog,
  operationName?: string
): SdkOperation | null {
  const parsed = parseDocument(query, operationName);
  if (!parsed?.name) return null;
  const definitions = parse(query).definitions;
  if (
    definitions.filter((item) => item.kind === Kind.OPERATION_DEFINITION).length !== 1 ||
    definitions.some(
      (item) => item.kind !== Kind.OPERATION_DEFINITION && item.kind !== Kind.FRAGMENT_DEFINITION
    )
  )
    return null;
  const operation = getOperationAST({ kind: Kind.DOCUMENT, definitions }, operationName)!;
  const fragments = new Map(
    definitions
      .filter((item): item is FragmentDefinitionNode => item.kind === Kind.FRAGMENT_DEFINITION)
      .map((item) => [item.name.value, item])
  );
  try {
    if (reachableFragments(operation, fragments).length !== fragments.size) return null;
  } catch {
    return null;
  }
  const op = catalog.operations.get(parsed.name);
  if (!op || op.kind !== parsed.kind) return null;
  try {
    return documentIdentity(query, operationName) === op.identity ? op : null;
  } catch {
    return null;
  }
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
  `${name} matches this document and uses a typed SDK operation.`;
const CUSTOM_NOTE = "Runs your selected GraphQL document unchanged through the SDK.";

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
  const install = `npm install @livepeer-frameworks/api@^${sdkVersion}${isSubscription ? " graphql-ws" : ""}`;

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
  const selection = doc?.name ? `{ operationName: ${JSON.stringify(doc.name)} }` : undefined;
  const docRef = op ? `${op.name}Document` : "query";
  const customQuery = op
    ? ""
    : `const query = \`${input.query.replace(/[`\\]/g, "\\$&").replace(/\$\{/g, "\\${")}\`;\n\n`;
  const head = header("//", "TypeScript SDK", install, guide, note);

  if (isSubscription) {
    const imports = op
      ? `import { ${docRef} } from "@livepeer-frameworks/api";\nimport { createSubscriptionClient } from "@livepeer-frameworks/api/subscriptions";`
      : `import { createSubscriptionClient } from "@livepeer-frameworks/api/subscriptions";`;
    const args = [docRef];
    if (hasVars || selection) args.push(hasVars ? tsLiteral(vars) : "undefined");
    if (selection) args.push(selection);
    const call = `subscriptions.subscribe(${args.join(", ")})`;
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
    ? 'import { createClient } from "@livepeer-frameworks/api";\nimport { getSdk } from "@livepeer-frameworks/api/operations";'
    : 'import { createClient } from "@livepeer-frameworks/api";';
  const call = op
    ? `sdk.${op.name[0].toLowerCase()}${op.name.slice(1)}(${hasVars ? tsLiteral(vars) : ""})`
    : `client.request(query, ${tsLiteral(vars)}${selection ? `, ${selection}` : ""})`;
  return `${head}
${imports}

const client = createClient({
  url: ${JSON.stringify(httpUrl)},
  token: ${JSON.stringify(token)},
});
${op ? "const sdk = getSdk(client);\n" : ""}
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
\t\tURL:   ${goString(wsUrl)},
\t\tToken: ${goString(token)},
\t})
\tif err != nil {
\t\tlog.Fatal(err)
\t}`
    : `\tclient, err := frameworks.NewClient(frameworks.ClientOptions{
\t\tURL:   ${goString(httpUrl)},
\t\tToken: ${goString(token)},
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
    const queryLiteral = goString(input.query);
    const varsLiteral =
      Object.keys(input.variables).length > 0
        ? `json.RawMessage(${goString(JSON.stringify(input.variables))})`
        : "nil";
    if (varsLiteral !== "nil") ctx.imports.add("encoding/json");
    const opName = goString(doc?.name ?? "");
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

  return `${header("//", "Go SDK", `go get github.com/Livepeer-FrameWorks/sdk-go@v${sdkVersion}`, guide, note)}
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
  if (!op) stdImports.add("import json");
  const customSetup = op
    ? ""
    : `query = json.loads(${JSON.stringify(JSON.stringify(input.query))})\n${
        hasVars
          ? `variables = json.loads(${JSON.stringify(JSON.stringify(input.variables))})\n`
          : ""
      }\n`;
  const opNameArg = doc?.name ? `, operation_name=${JSON.stringify(doc.name)}` : "";
  const varsArg = hasVars ? ", variables=variables" : "";
  const head = header(
    "#",
    "Python SDK",
    `pip install livepeer-frameworks~=${sdkVersion}`,
    guide,
    note
  );
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

function canRenderValue(
  type: TypeRef,
  value: unknown,
  schema: IntrospectedSchema | null | undefined
): boolean {
  if (typeof value === "string" && /[\ud800-\udfff]/u.test(value)) return false;
  if (value === null) return false;
  const base = unwrapNonNull(type);
  if (base.kind === "LIST")
    return (
      Array.isArray(value) &&
      !!base.ofType &&
      value.every((item) => canRenderValue(base.ofType!, item, schema))
    );
  const name = base.name ?? "";
  // Host date types can truncate precision or accept/rewrite invalid input.
  if (name === "Time") return false;
  if (["ID", "String", "Currency", "Money"].includes(name)) return typeof value === "string";
  if (name === "Boolean") return typeof value === "boolean";
  if (name === "Int")
    return (
      typeof value === "number" &&
      Number.isInteger(value) &&
      value >= -2147483648 &&
      value <= 2147483647
    );
  if (name === "Float") return typeof value === "number" && Number.isFinite(value);
  if (name === "JSON") return true;
  const definition = schema?.types?.find((item) => item.name === name);
  if (definition?.kind === "ENUM")
    return definition.enumValues?.some((item) => item.name === value) ?? false;
  if (definition?.kind !== "INPUT_OBJECT" || typeof value !== "object" || Array.isArray(value))
    return false;
  const fields = definition.inputFields ?? [];
  return (
    Object.entries(value as Record<string, unknown>).every(([key, item]) => {
      const field = fields.find((candidate) => candidate.name === key);
      return !!field?.type && canRenderValue(field.type, item, schema);
    }) &&
    // Omitted non-null defaults can become Go scalar zero values in structs.
    fields.every(
      (field) => !field.type || !isNonNull(field.type) || Object.hasOwn(value, field.name)
    )
  );
}

/**
 * Generates the TypeScript, Go, and Python SDK snippets for the editor's
 * document: a typed call only when the document and variables can be represented
 * faithfully by that language's generated operation, a raw SDK request otherwise.
 */
export function generateSdkSnippets(input: SdkSnippetInput): SdkSnippets {
  assertVariablesObject(input.variables);
  const doc = parseDocument(input.query, input.operationName);
  let op = matchSdkOperation(input.query, input.catalog, input.operationName);
  if (!doc) throw new TypeError("Select exactly one GraphQL operation before exporting examples");
  if (op) {
    const missing = op.variables.filter(
      (v) => v.required && !Object.hasOwn(input.variables, v.name)
    );
    if (missing.length)
      throw new TypeError(`Fill in required variables: ${missing.map((v) => v.name).join(", ")}`);
  }
  if (
    op &&
    Object.entries(input.variables).some(([name, value]) => {
      const variable = op!.variables.find((item) => item.name === name);
      return !variable || !canRenderValue(variable.type, value, input.schema);
    })
  )
    op = null;
  return {
    tsSdk: tsSnippet(input, op, doc),
    goSdk: goSnippet(input, op, doc),
    pythonSdk: pySnippet(input, op, doc),
  };
}

export function assertVariablesObject(
  variables: unknown
): asserts variables is Record<string, unknown> {
  if (variables === null || typeof variables !== "object" || Array.isArray(variables))
    throw new TypeError("Variables must be a JSON object");
}

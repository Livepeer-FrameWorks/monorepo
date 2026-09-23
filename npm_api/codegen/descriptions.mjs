import { readFileSync, readdirSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import {
  buildSchema,
  getNamedType,
  isCompositeType,
  isInterfaceType,
  isObjectType,
  isUnionType,
  Kind,
  parse,
} from "graphql";
import ts from "typescript";

const packageRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const repositoryRoot = resolve(packageRoot, "..");
const generatedPath = join(packageRoot, "src/generated/graphql.ts");
const schema = buildSchema(
  readFileSync(join(repositoryRoot, "pkg/graphql/public/schema.public.graphql"), "utf8")
);
const operationDirectories = ["fragments", "queries", "mutations", "subscriptions", "generated"];

function graphqlFiles(directory) {
  return readdirSync(directory, { withFileTypes: true }).flatMap((entry) => {
    const path = join(directory, entry.name);
    return entry.isDirectory() ? graphqlFiles(path) : entry.name.endsWith(".graphql") ? [path] : [];
  });
}

const definitions = operationDirectories
  .flatMap((directory) => graphqlFiles(join(repositoryRoot, "pkg/graphql/public", directory)))
  .flatMap((path) => parse(readFileSync(path, "utf8")).definitions);
const fragments = new Map();
const operations = new Map();
for (const definition of definitions) {
  if (definition.kind === Kind.FRAGMENT_DEFINITION) {
    fragments.set(definition.name.value, definition);
  } else if (definition.kind === Kind.OPERATION_DEFINITION && definition.name) {
    operations.set(definition.name.value, definition);
  }
}

function operationRoot(operation) {
  if (operation.operation === "query") return schema.getQueryType();
  if (operation.operation === "mutation") return schema.getMutationType();
  return schema.getSubscriptionType();
}

const aliases = new Map();
function collectAliases(selectionSet, parentType) {
  if (!selectionSet || !parentType || !isCompositeType(parentType)) return;
  for (const selection of selectionSet.selections) {
    if (selection.kind === Kind.INLINE_FRAGMENT) {
      const narrowed = selection.typeCondition
        ? schema.getType(selection.typeCondition.name.value)
        : parentType;
      collectAliases(selection.selectionSet, narrowed);
      continue;
    }
    if (selection.kind !== Kind.FIELD) continue;
    const fields =
      isObjectType(parentType) || isInterfaceType(parentType) ? parentType.getFields() : {};
    const field = fields[selection.name.value];
    if (!field) continue;
    if (selection.alias)
      aliases.set(`${parentType.name}:${selection.alias.value}`, selection.name.value);
    collectAliases(selection.selectionSet, getNamedType(field.type));
  }
}
for (const fragment of fragments.values()) {
  collectAliases(fragment.selectionSet, schema.getType(fragment.typeCondition.name.value));
}
for (const operation of operations.values())
  collectAliases(operation.selectionSet, operationRoot(operation));

// The description of the field an operation targets: its root field, or,
// for an operation that selects a path such as analytics { usage { streaming
// { streamAnalyticsSummary(...) } } }, the last field of that path (the rule
// of operationTarget in scripts/sdkcontract/ops.go).
function rootFieldDescription(operation) {
  let parent = operationRoot(operation);
  let selections = operation.selectionSet.selections;
  let description;
  while (parent && selections.length === 1 && selections[0].kind === Kind.FIELD) {
    const fields = isObjectType(parent) || isInterfaceType(parent) ? parent.getFields() : {};
    const field = fields[selections[0].name.value];
    if (!field) break;
    description = field.description;
    const next = selections[0].selectionSet?.selections ?? [];
    if (next.length !== 1 || next[0].kind !== Kind.FIELD || !next[0].selectionSet) break;
    parent = getNamedType(field.type);
    selections = next;
  }
  return description;
}

function propertyName(member) {
  if (!member.name) return undefined;
  if (ts.isIdentifier(member.name) || ts.isStringLiteral(member.name)) return member.name.text;
  return undefined;
}

function concreteType(typeNode, fallback) {
  if (!ts.isTypeLiteralNode(typeNode) || (!isUnionType(fallback) && !isInterfaceType(fallback))) {
    return fallback;
  }
  const typename = typeNode.members.find((member) => propertyName(member) === "__typename");
  if (!typename || !ts.isPropertySignature(typename) || !typename.type) return fallback;
  let node = typename.type;
  while (ts.isParenthesizedTypeNode(node)) node = node.type;
  if (!ts.isLiteralTypeNode(node) || !ts.isStringLiteral(node.literal)) return fallback;
  return schema.getType(node.literal.text) ?? fallback;
}

const source = readFileSync(generatedPath, "utf8");
const sourceFile = ts.createSourceFile(
  generatedPath,
  source,
  ts.ScriptTarget.Latest,
  true,
  ts.ScriptKind.TS
);
const insertions = new Map();

function addDescription(position, description) {
  if (!description || insertions.has(position)) return;
  const text = description.replace(/\s+/g, " ").replaceAll("*/", "* /").trim();
  if (text) insertions.set(position, `/** ${text} */\n`);
}

function documentType(node, parentType) {
  if (!node || !parentType) return;
  if (ts.isParenthesizedTypeNode(node) || ts.isArrayTypeNode(node)) {
    documentType(node.type ?? node.elementType, parentType);
    return;
  }
  if (ts.isUnionTypeNode(node) || ts.isIntersectionTypeNode(node)) {
    for (const member of node.types) documentType(member, parentType);
    return;
  }
  if (!ts.isTypeLiteralNode(node)) return;

  const resolvedParent = concreteType(node, parentType);
  if (!isObjectType(resolvedParent) && !isInterfaceType(resolvedParent)) return;
  const fields = resolvedParent.getFields();
  for (const member of node.members) {
    if (!ts.isPropertySignature(member) || !member.type) continue;
    const renderedName = propertyName(member);
    if (!renderedName || renderedName === "__typename") continue;
    const schemaName = aliases.get(`${resolvedParent.name}:${renderedName}`) ?? renderedName;
    const field = fields[schemaName];
    if (!field) continue;
    addDescription(member.getStart(sourceFile), field.description);
    documentType(member.type, getNamedType(field.type));
  }
}

for (const statement of sourceFile.statements) {
  if (ts.isTypeAliasDeclaration(statement)) {
    const name = statement.name.text;
    const fragment = [...fragments.values()].find(
      (value) => `${value.name.value}Fragment` === name
    );
    if (fragment) {
      const parent = schema.getType(fragment.typeCondition.name.value);
      addDescription(statement.getStart(sourceFile), parent?.description);
      documentType(statement.type, parent);
      continue;
    }
    const operation = [...operations.values()].find((value) => {
      const suffix = value.operation[0].toUpperCase() + value.operation.slice(1);
      return `${value.name.value}${suffix}` === name;
    });
    if (operation) {
      addDescription(statement.getStart(sourceFile), rootFieldDescription(operation));
      documentType(statement.type, operationRoot(operation));
    }
    continue;
  }
  if (ts.isVariableStatement(statement)) {
    for (const declaration of statement.declarationList.declarations) {
      if (!ts.isIdentifier(declaration.name)) continue;
      const name = declaration.name.text;
      if (name.endsWith("Document")) {
        const operation = operations.get(name.slice(0, -"Document".length));
        if (operation)
          addDescription(statement.getStart(sourceFile), rootFieldDescription(operation));
      }
      if (name.endsWith("FragmentDoc")) {
        const fragment = fragments.get(name.slice(0, -"FragmentDoc".length));
        const type = fragment && schema.getType(fragment.typeCondition.name.value);
        if (type) addDescription(statement.getStart(sourceFile), type.description);
      }
    }
  }
}

if (insertions.size === 0)
  throw new Error("no schema descriptions were added to generated TypeScript");
let documented = source;
for (const [position, comment] of [...insertions].sort(([left], [right]) => right - left)) {
  documented = documented.slice(0, position) + comment + documented.slice(position);
}
writeFileSync(generatedPath, documented);

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

// Each operation's target from scripts/sdkcontract (make generate-ops):
// kind.field.field, with a member type name after a field of union or
// interface type (query.node.InfrastructureNode.metricsConnection).
// Its experimental map carries the note for each operation whose target path
// has an @experimental field.
const targetsFile = JSON.parse(
  readFileSync(join(repositoryRoot, "pkg/graphql/public/generated/targets.json"), "utf8")
);
const targets = targetsFile.targets;
const experimental = targetsFile.experimental ?? {};

// The operation's doc comment: its target field's description, then the
// experimental note when the target is @experimental.
function operationDescription(operation) {
  const description = rootFieldDescription(operation);
  const note = experimental[operation.name.value]?.note;
  if (!note) return description;
  return description ? `${description} ${note}` : note;
}

// The description of the field an operation targets.
function rootFieldDescription(operation) {
  const target = targets[operation.name.value];
  if (!target)
    throw new Error(`descriptions: no target for ${operation.name.value}; run make generate-ops`);
  const [, ...segments] = target.split(".");
  let parent = operationRoot(operation);
  let description;
  for (const segment of segments) {
    if (isUnionType(parent) || isInterfaceType(parent)) {
      const member = schema.getPossibleTypes(parent).find((type) => type.name === segment);
      if (member) {
        parent = member;
        continue;
      }
    }
    const field =
      isObjectType(parent) || isInterfaceType(parent) ? parent.getFields()[segment] : undefined;
    if (!field)
      throw new Error(`descriptions: ${operation.name.value}: ${target} is not in the schema`);
    description = field.description;
    parent = getNamedType(field.type);
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
      addDescription(statement.getStart(sourceFile), operationDescription(operation));
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
          addDescription(statement.getStart(sourceFile), operationDescription(operation));
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

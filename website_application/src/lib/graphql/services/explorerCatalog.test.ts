import { existsSync, readdirSync, readFileSync } from "node:fs";
import path from "node:path";
import {
  buildSchema,
  introspectionFromSchema,
  Kind,
  parse,
  print,
  specifiedRules,
  validate,
  visit,
  type DefinitionNode,
  type DocumentNode,
  type FragmentDefinitionNode,
  type OperationDefinitionNode,
} from "graphql";
import { describe, expect, it } from "vitest";

import { EXPLORER_CATALOG, type ExplorerExample } from "./explorerCatalog";
import { explorerService } from "./explorer";
import { stripClientDirectives } from "./gqlParser";
import type { IntrospectedSchema, SchemaField } from "./schemaUtils";

const repoRoot = existsSync(path.resolve(process.cwd(), "pkg/graphql"))
  ? process.cwd()
  : path.resolve(process.cwd(), "..");
const graphqlRoot = path.join(repoRoot, "pkg/graphql");
const operationsRoot = path.join(graphqlRoot, "operations");

const clientDirectiveStubs = `
  enum PaginationMode {
    Infinite
    SinglePage
  }

  directive @paginate(mode: PaginationMode) on FIELD
  directive @mask_disable on FIELD | FRAGMENT_SPREAD | INLINE_FRAGMENT
`;

const schema = buildSchema(
  `${clientDirectiveStubs}\n${readFileSync(path.join(graphqlRoot, "schema.graphql"), "utf8")}`
);
const introspectedSchema = introspectionFromSchema(schema)
  .__schema as unknown as IntrospectedSchema;

function walkGqlFiles(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const fullPath = path.join(dir, entry.name);
    if (entry.isDirectory()) return walkGqlFiles(fullPath);
    if (entry.isFile() && entry.name.endsWith(".gql")) return [fullPath];
    return [];
  });
}

function getOperation(document: DocumentNode): OperationDefinitionNode | undefined {
  return document.definitions.find(
    (definition): definition is OperationDefinitionNode =>
      definition.kind === Kind.OPERATION_DEFINITION
  );
}

function getFragmentDefinitions(): Map<string, FragmentDefinitionNode> {
  const fragments = new Map<string, FragmentDefinitionNode>();

  for (const file of walkGqlFiles(path.join(operationsRoot, "fragments"))) {
    const document = parse(readFileSync(file, "utf8"));
    for (const definition of document.definitions) {
      if (definition.kind === Kind.FRAGMENT_DEFINITION) {
        fragments.set(definition.name.value, definition);
      }
    }
  }

  return fragments;
}

const fragmentDefinitions = getFragmentDefinitions();

function collectFragmentSpreads(document: DocumentNode): string[] {
  const spreads = new Set<string>();

  visit(document, {
    FragmentSpread(node) {
      spreads.add(node.name.value);
    },
  });

  return [...spreads];
}

function appendRequiredFragments(source: string): string {
  const document = parse(source);
  const required = new Set<string>();
  const pending = collectFragmentSpreads(document);

  while (pending.length > 0) {
    const name = pending.pop();
    if (!name || required.has(name)) continue;

    const fragment = fragmentDefinitions.get(name);
    if (!fragment) continue;

    required.add(name);
    const nestedDocument: DocumentNode = {
      kind: Kind.DOCUMENT,
      definitions: [fragment],
    };
    pending.push(...collectFragmentSpreads(nestedDocument));
  }

  const fragmentSource = [...required]
    .map((name) => fragmentDefinitions.get(name))
    .filter((definition): definition is FragmentDefinitionNode => Boolean(definition))
    .map((definition) => print(definition))
    .join("\n\n");

  return fragmentSource ? `${source}\n\n${fragmentSource}` : source;
}

function getTemplatePath(example: ExplorerExample): string | undefined {
  return example.templatePath;
}

function readTemplate(templatePath: string): string {
  return readFileSync(path.join(graphqlRoot, templatePath), "utf8");
}

function getRequiredVariableNames(document: DocumentNode): string[] {
  const operation = getOperation(document);
  if (!operation) return [];

  return (operation.variableDefinitions ?? [])
    .filter((definition) => definition.type.kind === Kind.NON_NULL_TYPE && !definition.defaultValue)
    .map((definition) => definition.variable.name.value);
}

function getDefinitionPath(definition: DefinitionNode): string {
  if (definition.kind === Kind.OPERATION_DEFINITION) return definition.operation;
  if (definition.kind === Kind.FRAGMENT_DEFINITION) return "fragment";
  return definition.kind;
}

describe("EXPLORER_CATALOG", () => {
  it("uses existing templates with matching operation types", () => {
    const seenSectionIds = new Set<string>();
    const seenExampleIds = new Set<string>();

    for (const section of EXPLORER_CATALOG) {
      expect(seenSectionIds.has(section.id), `duplicate section id: ${section.id}`).toBe(false);
      seenSectionIds.add(section.id);

      for (const example of section.examples) {
        expect(seenExampleIds.has(example.id), `duplicate example id: ${example.id}`).toBe(false);
        seenExampleIds.add(example.id);

        const templatePath = getTemplatePath(example);
        if (!templatePath) continue;

        const fullPath = path.join(graphqlRoot, templatePath);
        expect(existsSync(fullPath), `missing template for ${example.id}: ${templatePath}`).toBe(
          true
        );

        const document = parse(readTemplate(templatePath));
        const operation = getOperation(document);
        expect(operation, `no operation in ${templatePath}`).toBeTruthy();
        expect(operation?.operation).toBe(example.operationType);

        if (example.variables) {
          const requiredVariables = getRequiredVariableNames(document);
          for (const variableName of requiredVariables) {
            expect(
              Object.hasOwn(example.variables, variableName),
              `${example.id} overrides variables but omits required $${variableName}`
            ).toBe(true);
          }
        }
      }
    }
  });
});

describe("shared GraphQL operations", () => {
  it("validate against the schema when client directives are stubbed", () => {
    const operationFiles = walkGqlFiles(operationsRoot).filter(
      (file) => !file.includes(`${path.sep}fragments${path.sep}`)
    );

    for (const file of operationFiles) {
      const source = appendRequiredFragments(readFileSync(file, "utf8"));
      const document = parse(source);
      const errors = validate(schema, document, specifiedRules);
      const relativePath = path.relative(graphqlRoot, file);

      expect(errors, relativePath).toEqual([]);

      const operation = getOperation(document);
      expect(
        operation,
        `${relativePath} must contain an operation, got ${document.definitions
          .map(getDefinitionPath)
          .join(", ")}`
      ).toBeTruthy();
    }
  });
});

describe("playground query normalization and generation", () => {
  it("removes Houdini/client directives without corrupting fragment names", () => {
    const source = `
      mutation CreateToken($input: CreateDeveloperTokenInput!) {
        createDeveloperToken(input: $input) {
          __typename
          ... on DeveloperToken {
            id
          }
          ... on ValidationError {
            ...ValidationErrorFields @mask_disable
          }
          ... on RateLimitError {
            message
          }
        }
      }

      query Tokens {
        developerTokensConnection(page: { first: 10 }) @paginate(mode: Infinite) {
          edges {
            node {
              id
            }
          }
        }
      }
    `;

    const stripped = stripClientDirectives(source);

    expect(stripped).toContain("...ValidationErrorFields");
    expect(stripped).not.toContain("@mask");
    expect(stripped).not.toContain("@paginate");
    expect(stripped).not.toContain("ValidationErrorFields_disable");
    expect(validate(schema, parse(appendRequiredFragments(stripped)), specifiedRules)).toEqual([]);
  });

  it("generates schema-valid fallback queries for every root field", () => {
    const rootFields: Array<{
      operationType: "query" | "mutation" | "subscription";
      fields: SchemaField[];
    }> = [
      { operationType: "query", fields: introspectedSchema.queryType?.fields ?? [] },
      { operationType: "mutation", fields: introspectedSchema.mutationType?.fields ?? [] },
      {
        operationType: "subscription",
        fields: introspectedSchema.subscriptionType?.fields ?? [],
      },
    ];

    for (const { operationType, fields } of rootFields) {
      for (const field of fields) {
        const { query } = explorerService.generateQueryFromField(
          field,
          operationType,
          introspectedSchema
        );
        const document = parse(stripClientDirectives(query));
        const errors = validate(schema, document, specifiedRules);

        expect(errors, `${operationType}.${field.name}\n${query}`).toEqual([]);
      }
    }
  });
});

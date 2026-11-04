import { readFileSync } from "node:fs";
import { buildSchema, introspectionFromSchema } from "graphql";
import { beforeAll, describe, expect, it } from "vitest";
import {
  generateSdkSnippets,
  loadSdkCatalog,
  matchSdkOperation,
  type SdkCatalog,
} from "../src/lib/graphql/services/sdkSnippets";
import { loadAllTemplates } from "../src/lib/graphql/services/templateLoader";
import { explorerService } from "../src/lib/graphql/services/explorer";
import type { IntrospectedSchema } from "../src/lib/graphql/services/schemaUtils";

let catalog: SdkCatalog;
let schema: IntrospectedSchema;
beforeAll(async () => {
  catalog = await loadSdkCatalog();
  schema = introspectionFromSchema(
    buildSchema(readFileSync("../pkg/graphql/public/schema.public.graphql", "utf8"))
  ).__schema as unknown as IntrospectedSchema;
});
function snippets(query: string, variables: Record<string, unknown> = {}) {
  return generateSdkSnippets({
    query,
    variables,
    catalog,
    schema,
    token: "YOUR_API_TOKEN",
    httpUrl: "https://bridge.example.com/graphql",
    wsUrl: "wss://bridge.example.com/graphql/ws",
    docsUrl: "https://logbook.frameworks.network",
  });
}
describe("snippets preserve the editor operation", () => {
  it.each([null, [1, 2], "abc", 42, false])("rejects non-object variables: %j", (variables) => {
    expect(() =>
      explorerService.generateCodeExamples("query Q { tenant { id } }", variables as never)
    ).toThrow("JSON object");
    expect(() => snippets("query Q { tenant { id } }", variables as never)).toThrow("JSON object");
  });
  it("keeps raw exports available when packaged variables are incomplete", () => {
    const operation = catalog.operations.get("GetStream")!;
    const out = explorerService.generateCodeExamples(operation.document, {}, null, {
      catalog,
      schema,
      docsUrl: "https://docs.test",
    });
    expect(out.tsSdk).toContain("Fill in required variables");
    expect(out.fetch).toContain("await fetch(");
    expect(out.go).toContain("package main");
    expect(out.curl).toContain("curl --request POST");
  });
  it("preserves the original GetStreamsConnection custom query", () => {
    const query =
      "query GetStreamsConnection { streamsConnection(page: { first: 10 }) { totalCount } }";
    expect(snippets(query).tsSdk).toContain(query);
  });
  it("omits operationName for anonymous operations in TypeScript exports", () => {
    for (const query of ["{ tenant { id } }", "subscription { tenantEvents { __typename } }"]) {
      expect(snippets(query).tsSdk).not.toContain("operationName:");
    }
  });
  it("does not substitute a generated document for different literal arguments", () => {
    const query = 'query GetStream { stream(id: "actual-id") { id } }';
    expect(matchSdkOperation(query, catalog)).toBeNull();
  });
  it("does not discard renamed variables", () => {
    const query = "query GetStream($streamId: ID!) { stream(id: $streamId) { id } }";
    const out = snippets(query, { streamId: "actual-id" });
    expect(out.tsSdk).toContain('"actual-id"');
  });
  it("preserves mutation input literals", () => {
    const query =
      'mutation CreateStream { createStream(input: { name: "Keep this name", record: true }) { __typename } }';
    expect(snippets(query).tsSdk).toContain("Keep this name");
  });
  it("preserves the extra analytics root in the built-in GetStream template", async () => {
    const templates = await loadAllTemplates();
    const template = templates.queries.find(
      (item) => item.filePath === "operations/queries/GetStream.gql"
    )!;
    expect(template.query).toContain("analytics");
    expect(matchSdkOperation(template.query, catalog)).toBeNull();
  });
  it("does not select one operation from a multi-operation document without an explicit choice", () => {
    const query =
      "query GetStream($id: ID!) { stream(id: $id) { id } } query ListStreams { streamsConnection { totalCount } }";
    expect(matchSdkOperation(query, catalog)).toBeNull();
  });
  it("preserves an explicitly selected operation in the original multi-operation document", () => {
    const query = "query First { tenant { id } } query Second { tenant { name } }";
    const out = generateSdkSnippets({
      query,
      operationName: "Second",
      variables: {},
      catalog,
      schema,
      token: "test",
      httpUrl: "https://bridge.test/graphql",
      wsUrl: "wss://bridge.test/graphql/ws",
      docsUrl: "https://docs.test",
    });
    expect(out.tsSdk).toContain(query);
    expect(out.tsSdk).toContain('operationName: "Second"');
    expect(out.goSdk).toContain('"Second"');
    expect(out.pythonSdk).toContain('operation_name="Second"');
  });
  it("does not discard unused fragments or unselected operations through substitution", () => {
    const query = catalog.operations.get("ListStreams")!.document;
    expect(matchSdkOperation(query + "\nfragment Unused on Stream { id }", catalog)).toBeNull();
    expect(
      matchSdkOperation(query + "\nquery Other { tenant { unknownField } }", catalog, "ListStreams")
    ).toBeNull();
  });
  it("retains explicit null and unknown input fields through the raw path", () => {
    const query = catalog.operations.get("CreateStream")!.document;
    const out = snippets(query, { input: { name: "name", record: null, unknown: true } });
    expect(out.tsSdk).toContain("client.request(query");
    expect(out.tsSdk).toContain('"unknown": true');
    expect(out.goSdk).toContain("client.MakeRequest");
  });
  it("preserves timestamp precision and invalid values without date coercion", () => {
    const query = catalog.operations.get("GetWebhookDeliveriesConnection")!.document;
    for (const createdAfter of ["2026-11-03T12:00:00.123456789Z", "invalid-time"]) {
      const out = snippets(query, { createdAfter });
      expect(out.goSdk).toContain("client.MakeRequest");
      expect(out.goSdk).toContain(createdAfter);
      expect(out.pythonSdk).toContain(createdAfter);
      expect(out.pythonSdk).not.toContain("datetime.fromisoformat");
    }
  });
  it("does not replace omitted non-null input defaults with Go zero values", () => {
    const query = catalog.operations.get("GetPreviewMediaPlacement")!.document;
    const out = snippets(query, {
      input: {
        scope: { kind: "TENANT" },
        verb: "INGEST",
        draftUpdate: { verb: "INGEST", kind: "SET", rules: { constraints: {} } },
      },
    });
    expect(out.goSdk).toContain("client.MakeRequest");
    expect(out.goSdk).not.toContain("schemaVersion");
  });
  it("exports out-of-range Int inputs for server validation without Go overflow", () => {
    const query = catalog.operations.get("ListStreams")!.document;
    expect(snippets(query, { page: { first: 1e40 } }).goSdk).toContain("client.MakeRequest");
  });
  it("uses Python boolean literals in the requests snippet", () => {
    const out = explorerService.generateCodeExamples(
      "mutation MyCreate($input: CreateStreamInput!) { createStream(input: $input) { __typename } }",
      { input: { name: "audit", record: true } }
    );
    expect(out.python).not.toContain('"record": true');
  });
  it("uses Go expressions for nested inputs in the net/http snippet", () => {
    const out = explorerService.generateCodeExamples(
      "mutation MyCreate($input: CreateStreamInput!) { createStream(input: $input) { __typename } }",
      { input: { name: "audit", record: true } }
    );
    expect(out.go).not.toContain('"input": {"name":"audit","record":true}');
  });
  it("uses the mutation API for the Apollo mutation snippet", () => {
    const out = explorerService.generateCodeExamples(
      'mutation MyCreate { createStream(input: { name: "audit" }) { __typename } }'
    );
    expect(out.javascript).toContain("client.mutate(");
  });
  it("rejects ambiguous definition names before extracting an Apollo operation", () => {
    for (const query of [
      "mutation Q { first } mutation Q { second }",
      "mutation Q { ...F } fragment F on Mutation { first } fragment F on Mutation { second }",
      "{ tenant { id } } query Q { tenant { name } }",
    ]) {
      expect(() =>
        explorerService.generateCodeExamples(query, {}, null, {
          catalog: null,
          docsUrl: "https://docs.test",
          operationName: "Q",
        })
      ).toThrow(/unique|anonymous/i);
    }
  });
});

import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { documentIdentity } from "../src/identity.js";
import * as documents from "../src/generated/graphql.js";
import { operations } from "../src/generated/manifest.js";
import { createClient } from "../src/client.js";
import { getSdk } from "../src/generated/methods.js";
import { loadFixture } from "./fixtures.js";
import { UnsupportedOperationError } from "../src/errors.js";

const catalog = JSON.parse(
  readFileSync(new URL("../../pkg/graphql/public/catalog.json", import.meta.url), "utf8")
) as {
  operations: Array<{ name: string; document: string; identity: string; kind: string }>;
};

describe("document identity", () => {
  const shared = loadFixture<{
    cases: Array<{ name: string; document: string; operationName?: string; identity: string }>;
  }>("document_identity.json");
  for (const item of shared.cases)
    it(item.name, () =>
      expect(documentIdentity(item.document, item.operationName ?? undefined)).toBe(item.identity)
    );
  for (const op of catalog.operations) {
    it(`${op.name}: matches the shared catalog and actual emitted document`, () => {
      expect(documentIdentity(op.document, op.name)).toBe(op.identity);
      const document = (documents as unknown as Record<string, unknown>)[`${op.name}Document`];
      expect(documentIdentity(String(document), op.name)).toBe(op.identity);
    });
  }
  it("normalizes comments and strings while preserving literals and selection order", () => {
    const query = 'query Q { stream(id: "café😀") { id name } }';
    expect(documentIdentity('query Q{stream(id:"caf\\u00e9😀"){id,name}} # comment')).toBe(
      documentIdentity(query)
    );
    expect(documentIdentity('query Q { stream(id: "different") { id name } }')).not.toBe(
      documentIdentity(query)
    );
    expect(documentIdentity('query Q { stream(id: "café😀") { name id } }')).not.toBe(
      documentIdentity(query)
    );
  });
  it("sorts reachable fragment definitions and excludes unrelated operations", () => {
    const a =
      'query Q { stream(id: "x") { ...B ...A } } fragment B on Stream { name } fragment A on Stream { id }';
    const b =
      'fragment A on Stream { id } fragment B on Stream { name } query Q { stream(id: "x") { ...B ...A } } query Other { tenant { id } }';
    expect(documentIdentity(a, "Q")).toBe(documentIdentity(b, "Q"));
    expect(() => documentIdentity(b)).toThrow();
  });
  it("does not borrow the version gate of a same-name custom document", async () => {
    const sent: string[] = [];
    const client = createClient({
      url: "https://identity-gate.test/graphql",
      fetch: async (_url, init) => {
        const body = JSON.parse(String(init?.body)) as { operationName: string };
        sent.push(body.operationName);
        return new Response(
          JSON.stringify({
            data:
              body.operationName === "ServerInfo"
                ? { serverInfo: { version: "v0.3.11", features: [] } }
                : { tenant: { id: "tenant" } },
          })
        );
      },
    });
    await client.request("query GetBillingLifecycle { tenant { id } }");
    await expect(
      client.request("query GetBillingLifecycle { tenant { ...Missing } }")
    ).resolves.toBeDefined();
    expect(sent).toContain("GetBillingLifecycle");
    sent.length = 0;
    await expect(getSdk(client).getBillingLifecycle()).rejects.toBeInstanceOf(
      UnsupportedOperationError
    );
    expect(sent).not.toContain("GetBillingLifecycle");
    expect(operations.GetBillingLifecycle.since).toBe("v0.3.14");
  });
  it("named methods retain per-call options and request/response types", async () => {
    let headers: Headers | undefined;
    const client = createClient({
      url: "https://methods.test/graphql",
      checkServer: false,
      fetch: async (_url, init) => {
        headers = new Headers(init?.headers);
        return new Response(
          JSON.stringify({
            data: {
              streamsConnection: {
                nodes: [],
                totalCount: 0,
                pageInfo: { hasNextPage: false, endCursor: null },
              },
            },
          })
        );
      },
    });
    const result = await getSdk(client).listStreams(
      { page: { first: 10 } },
      { headers: { "x-test": "method" } }
    );
    expect(result.streamsConnection.totalCount).toBe(0);
    expect(headers?.get("x-test")).toBe("method");
    if (false) {
      // @ts-expect-error required variables are checked on named methods
      getSdk(client).getStream();
      // @ts-expect-error unknown input fields are rejected
      getSdk(client).createStream({ input: { name: "x", nonexistent: true } });
    }
  });
});

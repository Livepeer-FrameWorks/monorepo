import { afterEach, describe, expect, it, vi } from "vitest";

import * as gatewayProbe from "@livepeer-frameworks/api/gateway-probe";
import { IngestClient } from "../src/core/IngestClient";

// The real gate runs; the spies record which requirement StreamCrafter asks for.
vi.mock("@livepeer-frameworks/api/gateway-probe", async (importOriginal) => {
  const actual = await importOriginal<typeof gatewayProbe>();
  return {
    ...actual,
    checkGateway: vi.fn(actual.checkGateway),
    recheckGateway: vi.fn(actual.recheckGateway),
    checkGatewayFor: vi.fn(actual.checkGatewayFor),
    recheckGatewayFor: vi.fn(actual.recheckGatewayFor),
  };
});

const GATEWAY_URL = "https://gate.example/graphql";
const INGEST_OPERATIONS = ["ServerInfo", "ResolveIngestEndpoint"];

function isProbe(init?: RequestInit): boolean {
  return typeof init?.body === "string" && init.body.includes("serverInfo");
}

function sentOperations(fetcher: ReturnType<typeof vi.fn>): string[] {
  return fetcher.mock.calls.map(
    ([, init]) =>
      (JSON.parse(String((init as RequestInit).body)) as { operationName: string }).operationName
  );
}

const SUPPORTED = { data: { serverInfo: { version: "v0.3.11", features: [] } } };

afterEach(() => {
  vi.unstubAllGlobals();
  vi.clearAllMocks();
  gatewayProbe.clearServerInfoProbes();
});

describe("IngestClient operation gate", () => {
  it("gates the gateway on the operations it sends, not the SDK line minimum", async () => {
    const fetcher = vi.fn(async (_url: RequestInfo | URL, init?: RequestInit) =>
      isProbe(init)
        ? new Response(JSON.stringify(SUPPORTED))
        : new Response(
            JSON.stringify({
              data: {
                resolveIngestEndpoint: {
                  primary: {
                    nodeId: "node-1",
                    baseUrl: "https://ingest.example",
                    whipUrl: "https://ingest.example/whip/key",
                  },
                  fallbacks: [],
                  metadata: null,
                },
              },
            })
          )
    );
    vi.stubGlobal("fetch", fetcher);
    const client = new IngestClient({ gatewayUrl: GATEWAY_URL, streamKey: "key" });
    await client.resolve();

    expect(gatewayProbe.checkGatewayFor).toHaveBeenCalledWith(GATEWAY_URL, INGEST_OPERATIONS);
    expect(gatewayProbe.checkGateway).not.toHaveBeenCalled();
    for (const name of sentOperations(fetcher)) {
      expect(INGEST_OPERATIONS).toContain(name);
    }
    client.destroy();
  });

  it("asks again with the same operations when the gateway rejects the resolve's schema", async () => {
    const fetcher = vi.fn(async (_url: RequestInfo | URL, init?: RequestInit) =>
      isProbe(init)
        ? new Response(JSON.stringify(SUPPORTED))
        : new Response(
            JSON.stringify({
              data: null,
              errors: [
                {
                  message: 'Cannot query field "resolveIngestEndpoint" on type "Query".',
                  extensions: { code: "GRAPHQL_VALIDATION_FAILED" },
                },
              ],
            }),
            { status: 422 }
          )
    );
    vi.stubGlobal("fetch", fetcher);
    const client = new IngestClient({ gatewayUrl: GATEWAY_URL, streamKey: "key", maxRetries: 0 });
    await client.resolve().catch(() => undefined);

    expect(gatewayProbe.recheckGatewayFor).toHaveBeenCalledWith(GATEWAY_URL, INGEST_OPERATIONS);
    expect(gatewayProbe.recheckGateway).not.toHaveBeenCalled();
    client.destroy();
  });
});

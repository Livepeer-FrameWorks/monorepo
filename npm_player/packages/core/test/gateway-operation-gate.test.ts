import { afterEach, describe, expect, it, vi } from "vitest";

import * as gatewayProbe from "@livepeer-frameworks/api/gateway-probe";
import { GatewayClient } from "../src/core/GatewayClient";

// The real gate runs; the spies record which requirement the player asks for.
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

const GATEWAY_URL = "https://gw.example.com/graphql";
const PLAYER_OPERATIONS = ["ServerInfo", "ResolveViewerEndpoint"];

function isProbe(init?: RequestInit): boolean {
  return typeof init?.body === "string" && init.body.includes("serverInfo");
}

function sentOperations(fetcher: ReturnType<typeof vi.fn>): string[] {
  return fetcher.mock.calls.map(
    ([, init]) =>
      (JSON.parse(String((init as RequestInit).body)) as { operationName: string }).operationName
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.clearAllMocks();
  gatewayProbe.clearServerInfoProbes();
});

describe("GatewayClient operation gate", () => {
  it("gates the gateway on the operations it sends, not the SDK line minimum", async () => {
    const fetcher = vi.fn(async (_url: RequestInfo | URL, init?: RequestInit) =>
      isProbe(init)
        ? new Response(
            JSON.stringify({ data: { serverInfo: { version: "v0.3.11", features: [] } } })
          )
        : new Response(
            JSON.stringify({
              data: {
                resolveViewerEndpoint: {
                  primary: { nodeId: "n1", baseUrl: "https://n1.example.com" },
                  fallbacks: [],
                  metadata: null,
                },
              },
            })
          )
    );
    vi.stubGlobal("fetch", fetcher);
    const client = new GatewayClient({ gatewayUrl: GATEWAY_URL, contentId: "pk_test" });
    await client.resolve();

    expect(gatewayProbe.checkGatewayFor).toHaveBeenCalledWith(GATEWAY_URL, PLAYER_OPERATIONS);
    expect(gatewayProbe.checkGateway).not.toHaveBeenCalled();
    for (const name of sentOperations(fetcher)) {
      expect(PLAYER_OPERATIONS).toContain(name);
    }
    client.destroy();
  });

  it("asks again with the same operations when the gateway rejects the resolve's schema", async () => {
    const fetcher = vi.fn(async (_url: RequestInfo | URL, init?: RequestInit) =>
      isProbe(init)
        ? new Response(
            JSON.stringify({ data: { serverInfo: { version: "v0.3.11", features: [] } } })
          )
        : new Response(
            JSON.stringify({
              data: null,
              errors: [
                {
                  message: 'Cannot query field "resolveViewerEndpoint" on type "Query".',
                  extensions: { code: "GRAPHQL_VALIDATION_FAILED" },
                },
              ],
            }),
            { status: 422 }
          )
    );
    vi.stubGlobal("fetch", fetcher);
    const client = new GatewayClient({
      gatewayUrl: GATEWAY_URL,
      contentId: "pk_test",
      maxRetries: 1,
    });
    await client.resolve().catch(() => undefined);

    expect(gatewayProbe.recheckGatewayFor).toHaveBeenCalledWith(GATEWAY_URL, PLAYER_OPERATIONS);
    expect(gatewayProbe.recheckGateway).not.toHaveBeenCalled();
    client.destroy();
  });
});

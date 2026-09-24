import { readFileSync } from "node:fs";
import { ServerTooOldError } from "@livepeer-frameworks/api";
import { afterEach, describe, expect, it, vi } from "vitest";

import { IngestClient } from "../src/core/IngestClient";
import { checkGatewayFor, clearServerInfoProbes } from "@livepeer-frameworks/api/gateway-probe";

const GATEWAY_URL = "https://gate.example/graphql";

function isProbe(init?: RequestInit): boolean {
  return typeof init?.body === "string" && init.body.includes("serverInfo");
}

function ingestReply() {
  return {
    ok: true,
    json: async () => ({
      data: {
        resolveIngestEndpoint: {
          primary: {
            nodeId: "node-1",
            baseUrl: "https://ingest.example",
            whipUrl: "https://ingest.example/whip/key",
            rtmpUrl: null,
            srtUrl: null,
            region: "eu",
            loadScore: 0.1,
            kind: "EDGE",
            clusterId: "c1",
          },
          fallbacks: [],
          metadata: null,
        },
      },
    }),
  };
}

function resolveCalls(fetcher: ReturnType<typeof vi.fn>): unknown[][] {
  return fetcher.mock.calls.filter(([, init]) => !isProbe(init as RequestInit | undefined));
}

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  clearServerInfoProbes();
});

describe("IngestClient gateway version gate", () => {
  it.each([
    [
      "a release below the minimum",
      { status: 200, body: { data: { serverInfo: { version: "v0.3.10", features: [] } } } },
      "v0.3.10",
    ],
    [
      "a gateway without serverInfo",
      {
        status: 422,
        body: {
          errors: [
            {
              message: 'Cannot query field "serverInfo" on type "Query".',
              extensions: { code: "GRAPHQL_VALIDATION_FAILED" },
            },
          ],
        },
      },
      null,
    ],
  ])("refuses %s without retrying or publishing", async (_case, probeAnswer, serverVersion) => {
    const fetcher = vi.fn(async (_url: RequestInfo | URL, init?: RequestInit) =>
      isProbe(init)
        ? new Response(JSON.stringify(probeAnswer.body), { status: probeAnswer.status })
        : ingestReply()
    );
    vi.stubGlobal("fetch", fetcher);
    const client = new IngestClient({ gatewayUrl: GATEWAY_URL, streamKey: "key", maxRetries: 3 });
    const statuses = vi.fn();
    const published = vi.fn();
    client.on("statusChange", statuses);
    client.on("endpointsResolved", published);
    const error = await client.resolve().catch((e: unknown) => e);
    expect(error).toBeInstanceOf(ServerTooOldError);
    expect((error as ServerTooOldError).serverVersion).toBe(serverVersion);
    expect((error as ServerTooOldError).minimumVersion).toBe("v0.3.11");
    expect(statuses).toHaveBeenLastCalledWith({
      status: "error",
      error: (error as Error).message,
    });
    expect(published).not.toHaveBeenCalled();
    expect(client.getEndpoints()).toBeNull();
    expect(resolveCalls(fetcher)).toHaveLength(1);
    client.destroy();
  });

  it("sends the resolve alongside the probe and holds the destination until it settles", async () => {
    let answerProbe!: () => void;
    const probeHeld = new Promise<void>((resolve) => {
      answerProbe = resolve;
    });
    const fetcher = vi.fn(async (_url: RequestInfo | URL, init?: RequestInit) => {
      if (!isProbe(init)) return ingestReply();
      await probeHeld;
      return new Response(
        JSON.stringify({ data: { serverInfo: { version: "v0.3.11", features: [] } } })
      );
    });
    vi.stubGlobal("fetch", fetcher);
    const client = new IngestClient({ gatewayUrl: GATEWAY_URL, streamKey: "key" });
    const published = vi.fn();
    client.on("endpointsResolved", published);
    const pending = client.resolve();
    await vi.waitFor(() => expect(resolveCalls(fetcher)).toHaveLength(1));
    expect(fetcher.mock.calls.filter(([, init]) => isProbe(init as RequestInit))).toHaveLength(1);
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(published).not.toHaveBeenCalled();
    answerProbe();
    const endpoints = await pending;
    expect(endpoints.primary).toEqual({
      nodeId: "node-1",
      baseUrl: "https://ingest.example",
      whipUrl: "https://ingest.example/whip/key",
      rtmpUrl: null,
      srtUrl: null,
      region: "eu",
      loadScore: 0.1,
    });
    expect(published).toHaveBeenCalledOnce();
    client.destroy();
  });

  it("asks the gateway again at once when it rejects the resolve against its schema", async () => {
    const versions = ["v0.3.11", "v0.3.10"];
    const fetcher = vi.fn(async (_url: RequestInfo | URL, init?: RequestInit) => {
      if (isProbe(init)) {
        const version = versions.shift() ?? "v0.3.11";
        return new Response(JSON.stringify({ data: { serverInfo: { version, features: [] } } }));
      }
      return new Response(
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
      );
    });
    vi.stubGlobal("fetch", fetcher);
    const client = new IngestClient({ gatewayUrl: GATEWAY_URL, streamKey: "key", maxRetries: 3 });
    const error = await client.resolve().catch((e: unknown) => e);
    expect(error).toBeInstanceOf(ServerTooOldError);
    expect((error as ServerTooOldError).serverVersion).toBe("v0.3.10");
    expect(fetcher.mock.calls.filter(([, init]) => isProbe(init as RequestInit))).toHaveLength(2);
    expect(resolveCalls(fetcher)).toHaveLength(1);
    client.destroy();
  });

  it("proceeds when the probe does not answer and probes again next time", async () => {
    vi.useFakeTimers();
    let probeAnswers = false;
    const fetcher = vi.fn(async (_url: RequestInfo | URL, init?: RequestInit) => {
      if (!isProbe(init)) return ingestReply();
      if (!probeAnswers) return new Promise<Response>(() => {});
      return new Response(
        JSON.stringify({ data: { serverInfo: { version: "v0.3.10", features: [] } } })
      );
    });
    vi.stubGlobal("fetch", fetcher);
    const client = new IngestClient({ gatewayUrl: GATEWAY_URL, streamKey: "key" });
    const first = client.resolve();
    await vi.advanceTimersByTimeAsync(3000);
    await expect(first).resolves.toMatchObject({ primary: { nodeId: "node-1" } });

    probeAnswers = true;
    await expect(client.resolve()).rejects.toBeInstanceOf(ServerTooOldError);
    expect(fetcher.mock.calls.filter(([, init]) => isProbe(init as RequestInit))).toHaveLength(2);
    client.destroy();
  });
});

interface ServerInfoFixture {
  cases: Array<{
    name: string;
    minServer?: string;
    operationSince?: Record<string, string>;
    scope?: "client";
    responses: Record<string, Array<{ status?: number; body?: unknown; networkError?: boolean }>>;
    calls: Array<string | { advanceMs: number }>;
    expect: {
      calls: Array<{ ok: true } | { error: Record<string, unknown> }>;
      sent?: Record<string, number>;
      verified?: boolean;
    };
  }>;
}

// The SDK's serverInfo gate cases (sdk_conformance/server_info.json) that
// concern the server itself: StreamCrafter applies the same rules to its
// gateway. Cases about per-operation `since`, a custom minimum, and the SDK
// client's operation path do not apply; StreamCrafter's minimum is the newest
// `since` of the operations it sends, which equals the fixture's default
// minimum. Each call is one checkGatewayFor with those operations.
const fixture = JSON.parse(
  readFileSync(new URL("../../../../sdk_conformance/server_info.json", import.meta.url), "utf8")
) as ServerInfoFixture;
const cases = fixture.cases.filter((c) => !c.operationSince && !c.minServer && !c.scope);

describe("gateway version gate (sdk_conformance/server_info.json)", () => {
  it("covers the server cases", () => {
    expect(cases.length).toBeGreaterThanOrEqual(8);
  });

  for (const tc of cases) {
    it(tc.name, async () => {
      const queue = [...(tc.responses.ServerInfo ?? [])];
      const fetcher = vi.fn(async () => {
        const next = queue.shift();
        if (!next) throw new Error("no scripted serverInfo response left");
        if (next.networkError) throw new TypeError("fetch failed");
        return new Response(next.body === undefined ? "" : JSON.stringify(next.body), {
          status: next.status ?? 200,
        });
      });
      vi.stubGlobal("fetch", fetcher);
      let clock = Date.parse("2026-09-19T12:00:00Z");
      vi.useFakeTimers({ toFake: ["Date"], now: clock });

      let i = -1;
      for (const step of tc.calls) {
        if (typeof step === "object") {
          clock += step.advanceMs;
          vi.setSystemTime(clock);
          continue;
        }
        i++;
        const want = tc.expect.calls[i];
        const outcome = await checkGatewayFor(GATEWAY_URL, [
          "ServerInfo",
          "ResolveIngestEndpoint",
        ]).then(
          (status) => ({ status }),
          (error: unknown) => ({ error })
        );
        if ("ok" in want) {
          expect("error" in outcome ? outcome.error : null, `call ${i}`).toBeNull();
          if (tc.expect.verified !== undefined && "status" in outcome && outcome.status) {
            expect(outcome.status.verified).toBe(tc.expect.verified);
          }
        } else {
          const error = (outcome as { error?: unknown }).error;
          expect(error, `call ${i}`).toBeInstanceOf(ServerTooOldError);
          expect(want.error.kind).toBe("ServerTooOldError");
          expect((error as ServerTooOldError).serverVersion).toBe(want.error.serverVersion);
          expect((error as ServerTooOldError).minimumVersion).toBe(want.error.minimumVersion);
        }
      }
      if (tc.expect.sent?.ServerInfo !== undefined) {
        expect(fetcher).toHaveBeenCalledTimes(tc.expect.sent.ServerInfo);
      }
    });
  }
});

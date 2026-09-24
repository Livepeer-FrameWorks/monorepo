import { readFileSync } from "node:fs";
import { ServerTooOldError } from "@livepeer-frameworks/api";
import { afterEach, describe, expect, it, vi } from "vitest";

import { checkGatewayFor, clearServerInfoProbes } from "@livepeer-frameworks/api/gateway-probe";

interface FixtureResponse {
  status?: number;
  body?: unknown;
  networkError?: boolean;
}

interface ServerInfoFixture {
  cases: Array<{
    name: string;
    minServer?: string;
    operationSince?: Record<string, string>;
    scope?: "client";
    responses: Record<string, FixtureResponse[]>;
    calls: Array<string | { advanceMs: number }>;
    expect: {
      calls: Array<{ ok: true } | { error: Record<string, unknown> }>;
      sent?: Record<string, number>;
      verified?: boolean;
    };
  }>;
}

// The SDK's serverInfo gate cases (sdk_conformance/server_info.json) that
// concern the server itself: the player applies the same rules to its
// gateway. Cases about per-operation `since`, a custom minimum, and the SDK
// client's operation path do not apply; the player's minimum is the newest
// `since` of the operations it sends, which equals the fixture's default
// minimum. Each call is one checkGatewayFor with those operations.
const fixture = JSON.parse(
  readFileSync(new URL("../../../../sdk_conformance/server_info.json", import.meta.url), "utf8")
) as ServerInfoFixture;
const cases = fixture.cases.filter((c) => !c.operationSince && !c.minServer && !c.scope);

const URL_UNDER_TEST = "https://gate.example/graphql";

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  clearServerInfoProbes();
});

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
        const outcome = await checkGatewayFor(URL_UNDER_TEST, [
          "ServerInfo",
          "ResolveViewerEndpoint",
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
          expect("error" in outcome, `call ${i}`).toBe(true);
          const error = (outcome as { error: unknown }).error;
          expect(error).toBeInstanceOf(ServerTooOldError);
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

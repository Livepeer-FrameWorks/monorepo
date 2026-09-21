import { afterEach, describe, expect, it, vi } from "vitest";

import { createClientWith } from "../src/client.js";
import { DeleteStreamDocument, GetStreamDocument } from "../src/generated/graphql.js";
import {
  errorMismatches,
  type ExpectedError,
  type FixtureResponse,
  loadFixture,
  scriptedFetch,
} from "./fixtures.js";

interface ServerInfoFixture {
  cases: Array<{
    name: string;
    operationSince?: Record<string, string>;
    responses: Record<string, FixtureResponse[]>;
    scope?: "client";
    calls: Array<"GetStream" | "DeleteStream" | { advanceMs: number }>;
    expect: {
      calls: Array<{ ok: true } | { error: ExpectedError }>;
      sent?: Record<string, number>;
      serverVersion?: string | null;
      verified?: boolean;
    };
  }>;
}

const fixture = loadFixture<ServerInfoFixture>("server_info.json");

afterEach(() => {
  vi.useRealTimers();
});

it("cancels a mutation waiting for a shared probe without cancelling another caller", async () => {
  let resolveProbe!: (response: Response) => void;
  const requests: string[] = [];
  const fetch = vi.fn<typeof globalThis.fetch>(async (_url, init) => {
    const { operationName } = JSON.parse(String(init?.body)) as { operationName: string };
    requests.push(operationName);
    if (operationName === "ServerInfo") {
      return new Promise<Response>((resolve) => {
        resolveProbe = resolve;
      });
    }
    return new Response(JSON.stringify({ data: { deleteStream: true } }));
  });
  const client = createClientWith({ url: "https://cancel-probe.test/graphql", fetch }, {});
  const controller = new AbortController();
  const cancelled = client.request(
    DeleteStreamDocument,
    { id: "cancelled" },
    { signal: controller.signal }
  );
  const surviving = client.request(DeleteStreamDocument, { id: "surviving" });
  const rejected = expect(cancelled).rejects.toThrow("caller cancelled");
  controller.abort(new Error("caller cancelled"));
  await rejected;
  expect(requests).toEqual(["ServerInfo"]);
  resolveProbe(
    new Response(JSON.stringify({ data: { serverInfo: { version: "v99.0.0", features: [] } } }))
  );
  await surviving;
  expect(requests).toEqual(["ServerInfo", "DeleteStream"]);
});

describe("serverInfo gate (sdk_conformance/server_info.json)", () => {
  fixture.cases.forEach((tc, index) => {
    it(tc.name, async () => {
      const queues = Object.fromEntries(Object.entries(tc.responses).map(([k, v]) => [k, [...v]]));
      const { fetch, requests } = scriptedFetch(queues);
      const client = createClientWith(
        { url: `https://server-info-${index}.test/graphql`, fetch, retry: { maxAttempts: 1 } },
        { operationSince: tc.operationSince }
      );
      let clock = Date.parse("2026-09-19T12:00:00Z");
      vi.useFakeTimers({ toFake: ["Date"], now: clock });
      let i = 0;
      for (const name of tc.calls) {
        if (typeof name === "object") {
          clock += name.advanceMs;
          vi.setSystemTime(clock);
          continue;
        }
        const call =
          name === "GetStream"
            ? client.request(GetStreamDocument, { id: "s1" })
            : client.request(DeleteStreamDocument, { id: "s1" });
        const want = tc.expect.calls[i];
        const outcome = await call.then(
          () => ({ ok: true as const }),
          (err: unknown) => ({ err })
        );
        if (want && "error" in want) {
          expect("err" in outcome, `call ${i} succeeded, want ${want.error.kind}`).toBe(true);
          expect(errorMismatches((outcome as { err: unknown }).err, want.error)).toEqual([]);
        } else {
          expect(outcome, `call ${i}`).toEqual({ ok: true });
        }
        i++;
      }
      if (tc.expect.sent) {
        const sent: Record<string, number> = {};
        for (const name of Object.keys(tc.expect.sent)) {
          sent[name] = requests.filter((r) => r.operationName === name).length;
        }
        expect(sent).toEqual(tc.expect.sent);
      }
      if (tc.expect.verified !== undefined || tc.expect.serverVersion !== undefined) {
        const status = await client.serverInfo();
        if (tc.expect.verified !== undefined) {
          expect(status.verified).toBe(tc.expect.verified);
        }
        if (tc.expect.serverVersion !== undefined) {
          expect(status.version).toBe(tc.expect.serverVersion);
        }
      }
    });
  });
});

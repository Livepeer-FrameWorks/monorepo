import { describe, expect, it } from "vitest";

import { createClientWith } from "../src/client.js";
import type { RetryPolicy } from "../src/retry.js";
import {
  errorMismatches,
  type ExpectedError,
  type FixtureResponse,
  loadFixture,
  scriptedFetch,
} from "./fixtures.js";

interface RetryFixture {
  policy: RetryPolicy;
  cases: Array<{
    name: string;
    kind: "query" | "mutation";
    idempotencyKey?: string;
    responses: FixtureResponse[];
    expect: {
      attempts: number;
      delaysMs: number[];
      data?: unknown;
      error?: ExpectedError;
      idempotencyKeys?: string[];
    };
  }>;
}

const fixture = loadFixture<RetryFixture>("retry.json");

describe("retry matrix (sdk_conformance/retry.json)", () => {
  for (const tc of fixture.cases) {
    it(tc.name, async () => {
      const { fetch, requests } = scriptedFetch({ "*": [...tc.responses] });
      const delays: number[] = [];
      const client = createClientWith(
        { url: "https://retry.test/graphql", fetch, retry: fixture.policy, checkServer: false },
        { sleep: async (ms) => void delays.push(ms) }
      );
      const document = `${tc.kind} RetryProbe { ok }`;
      const call = client.request(
        document,
        {},
        tc.idempotencyKey ? { idempotencyKey: tc.idempotencyKey } : {}
      );
      if (tc.expect.error) {
        const err = await call.then(
          () => null,
          (e: unknown) => e
        );
        expect(errorMismatches(err, tc.expect.error)).toEqual([]);
      } else {
        await expect(call).resolves.toEqual(tc.expect.data);
      }
      expect(requests).toHaveLength(tc.expect.attempts);
      expect(delays).toEqual(tc.expect.delaysMs);
      if (tc.expect.idempotencyKeys) {
        expect(requests.map((r) => r.headers["idempotency-key"])).toEqual(
          tc.expect.idempotencyKeys
        );
      }
    });
  }
});

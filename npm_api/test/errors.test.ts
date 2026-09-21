import { describe, expect, it } from "vitest";

import { createClientWith } from "../src/client.js";
import { expectResult } from "../src/results.js";
import {
  errorMismatches,
  type ExpectedError,
  type FixtureResponse,
  loadFixture,
  scriptedFetch,
} from "./fixtures.js";

interface ErrorsFixture {
  cases: Array<{
    name: string;
    response: FixtureResponse;
    expectResult?: { field: string; success: string[] };
    error?: ExpectedError;
    result?: unknown;
  }>;
}

const fixture = loadFixture<ErrorsFixture>("errors.json");

describe("error shapes (sdk_conformance/errors.json)", () => {
  for (const tc of fixture.cases) {
    it(tc.name, async () => {
      const { fetch } = scriptedFetch({ "*": [tc.response] });
      const client = createClientWith(
        {
          url: "https://errors.test/graphql",
          fetch,
          checkServer: false,
          retry: { maxAttempts: 1 },
        },
        {}
      );
      const call = client
        .request<
          Record<string, { __typename: string }>,
          Record<string, never>
        >("query ErrorProbe { a }")
        .then((data) =>
          tc.expectResult
            ? expectResult(data[tc.expectResult.field], ...tc.expectResult.success)
            : data
        );
      if (tc.error) {
        const err = await call.then(
          () => null,
          (e: unknown) => e
        );
        expect(errorMismatches(err, tc.error)).toEqual([]);
      } else {
        await expect(call).resolves.toEqual(tc.result);
      }
    });
  }
});

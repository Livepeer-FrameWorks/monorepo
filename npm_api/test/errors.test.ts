import { describe, expect, it } from "vitest";

import { createClientWith } from "../src/client.js";
import type { GraphQLErrorEntry, PartialErrors } from "../src/errors.js";
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
    partialErrors?: GraphQLErrorEntry[];
  }>;
}

const fixture = loadFixture<ErrorsFixture>("errors.json");

describe("error shapes (sdk_conformance/errors.json)", () => {
  for (const tc of fixture.cases) {
    it(tc.name, async () => {
      const { fetch } = scriptedFetch({ "*": [tc.response] });
      const clientSeen: PartialErrors[] = [];
      const callSeen: PartialErrors[] = [];
      const client = createClientWith(
        {
          url: "https://errors.test/graphql",
          fetch,
          checkServer: false,
          retry: { maxAttempts: 1 },
          onPartialErrors: (p) => clientSeen.push(p),
        },
        {}
      );
      const call = client
        .request<
          Record<string, { __typename: string }>,
          Record<string, never>
        >("query ErrorProbe { a }", {}, { onPartialErrors: (p) => callSeen.push(p) })
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
        expect(callSeen).toEqual([]);
        expect(clientSeen).toEqual([]);
      } else {
        await expect(call).resolves.toEqual(tc.result);
        const expected = tc.partialErrors
          ? [{ operationName: "ErrorProbe", errors: tc.partialErrors }]
          : [];
        expect(callSeen).toEqual(expected);
        expect(clientSeen).toEqual(expected);
      }
    });
  }
});

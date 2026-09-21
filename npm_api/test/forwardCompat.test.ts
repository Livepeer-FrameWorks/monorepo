import { describe, expect, it } from "vitest";

import { createClientWith } from "../src/client.js";
import * as documents from "../src/generated/graphql.js";
import { expectResult } from "../src/results.js";
import { errorMismatches, type ExpectedError, loadFixture } from "./fixtures.js";

interface ForwardCompatFixture {
  cases: Array<{
    name: string;
    operation: string;
    variables: Record<string, unknown>;
    response: { data: Record<string, unknown> };
    unknownMember?: { path: string[]; typename: string; raw: Record<string, unknown> };
    expectResult?: { field: string; success: string[] };
    error?: ExpectedError;
    enumValue?: { path: string[]; value: string };
  }>;
}

const fixture = loadFixture<ForwardCompatFixture>("forward_compat.json");

function at(value: unknown, path: string[]): unknown {
  return path.reduce<unknown>((v, name) => (v as Record<string, unknown>)[name], value);
}

// The TypeScript client returns response data as parsed JSON, so an unknown
// union member is the member's own object and an unknown enum value its string.
describe("forward compatibility (sdk_conformance/forward_compat.json)", () => {
  for (const tc of fixture.cases) {
    it(tc.name, async () => {
      const fetch = (async () =>
        new Response(JSON.stringify(tc.response), { status: 200 })) as typeof globalThis.fetch;
      const client = createClientWith(
        { url: "https://forward.test/graphql", fetch, checkServer: false },
        {}
      );
      const doc = String((documents as Record<string, unknown>)[`${tc.operation}Document`]);
      const data = await client.request<Record<string, unknown>, Record<string, unknown>>(
        doc,
        tc.variables
      );
      expect(data).toEqual(tc.response.data);
      if (tc.unknownMember) {
        const member = at(data, tc.unknownMember.path) as { __typename: string };
        expect(member.__typename).toBe(tc.unknownMember.typename);
        expect(member).toEqual(tc.unknownMember.raw);
      }
      if (tc.enumValue) {
        expect(at(data, tc.enumValue.path)).toBe(tc.enumValue.value);
      }
      if (tc.expectResult) {
        const field = data[tc.expectResult.field] as { __typename: string };
        const run = () => expectResult(field, ...tc.expectResult!.success);
        if (tc.error) {
          let err: unknown = null;
          try {
            run();
          } catch (e) {
            err = e;
          }
          expect(errorMismatches(err, tc.error)).toEqual([]);
        } else {
          expect(run()).toBe(field);
        }
      }
    });
  }
});

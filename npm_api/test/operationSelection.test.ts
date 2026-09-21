import { describe, expect, it } from "vitest";

import { createClientWith, parseOperation } from "../src/client.js";
import { loadFixture, scriptedFetch } from "./fixtures.js";

const fixture = loadFixture<{
  cases: Array<{
    name: string;
    document: string;
    operationName?: string;
    kind?: string;
    selectedName?: string | null;
    invalid?: boolean;
  }>;
}>("operation-selection.json");

describe("operation selection and retry safety", () => {
  for (const tc of fixture.cases) {
    it(tc.name, async () => {
      if (tc.invalid) {
        expect(() => parseOperation(tc.document, tc.operationName)).toThrow();
      } else {
        expect(parseOperation(tc.document, tc.operationName)).toEqual({
          kind: tc.kind,
          name: tc.selectedName,
        });
      }
      const { fetch, requests } = scriptedFetch({
        "*": [{ status: 503 }, { status: 503 }, { status: 503 }],
      });
      const client = createClientWith(
        { url: "https://selection.test/graphql", fetch, checkServer: false },
        { sleep: async () => {} }
      );
      await expect(
        client.request(tc.document, {}, { operationName: tc.operationName })
      ).rejects.toThrow();
      const attempts = tc.invalid || tc.kind === "subscription" ? 0 : tc.kind === "query" ? 3 : 1;
      expect(requests).toHaveLength(attempts);
      if (attempts) {
        expect(requests[0]!.operationName).toBe(tc.selectedName);
      }
    });
  }
});

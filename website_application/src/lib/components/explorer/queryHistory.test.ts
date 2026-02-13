import { describe, expect, it } from "vitest";

import { normalizeQueryHistory } from "./queryHistory";

describe("normalizeQueryHistory", () => {
  it("returns an empty list for non-array payloads", () => {
    expect(normalizeQueryHistory({})).toEqual([]);
  });

  it("filters out malformed entries", () => {
    const normalized = normalizeQueryHistory([
      {
        id: 1,
        query: "query { health }",
        variables: {},
        result: { statusIcon: "success" },
        timestamp: "2026-01-01T00:00:00.000Z",
      },
      {
        id: "bad",
        query: "query { invalid }",
        variables: {},
        timestamp: "2026-01-01T00:00:00.000Z",
      },
    ]);

    expect(normalized).toHaveLength(1);
    expect(normalized[0]?.id).toBe(1);
  });

  it("limits history to the latest 10 entries", () => {
    const entries = Array.from({ length: 12 }, (_, index) => ({
      id: index,
      query: `query ${index}`,
      variables: {},
      result: null,
      timestamp: "2026-01-01T00:00:00.000Z",
    }));

    const normalized = normalizeQueryHistory(entries);

    expect(normalized).toHaveLength(10);
    expect(normalized[0]?.id).toBe(0);
    expect(normalized.at(-1)?.id).toBe(9);
  });
});

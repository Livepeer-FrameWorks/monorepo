import { describe, expect, it } from "vitest";

import { paginateOffset, paginatePageToken, paginateRelay } from "../src/pagination.js";
import { loadFixture } from "./fixtures.js";

interface PaginationFixture {
  cases: Array<{
    name: string;
    strategy: "relay" | "offset" | "pageToken";
    pageSize: number;
    maxItems?: number;
    itemsField?: string;
    pages: Array<Record<string, unknown>>;
    expectRequests: unknown[];
    expectItems: unknown[];
  }>;
}

const fixture = loadFixture<PaginationFixture>("pagination.json");

describe("pagination strategies (sdk_conformance/pagination.json)", () => {
  for (const tc of fixture.cases) {
    it(tc.name, async () => {
      const requests: unknown[] = [];
      const pages = [...tc.pages];
      const nextPage = (request: unknown) => {
        requests.push({ ...(request as object) });
        const page = pages.shift();
        if (!page) {
          throw new Error("paginator requested a page past the last one");
        }
        return Promise.resolve(page);
      };
      const options = { pageSize: tc.pageSize, maxItems: tc.maxItems };
      let iterator: AsyncGenerator<unknown>;
      switch (tc.strategy) {
        case "relay":
          iterator = paginateRelay(nextPage as never, options);
          break;
        case "offset":
          iterator = paginateOffset(nextPage as never, options);
          break;
        case "pageToken":
          iterator = paginatePageToken(
            (request) =>
              nextPage(request).then((page) => ({
                items: page[tc.itemsField ?? "items"] as unknown[],
                nextPageToken: page.nextPageToken as string | null,
              })),
            options
          );
          break;
      }
      const items: unknown[] = [];
      for await (const item of iterator) {
        items.push(item);
      }
      expect(items).toEqual(tc.expectItems);
      expect(requests).toEqual(tc.expectRequests);
    });
  }
});

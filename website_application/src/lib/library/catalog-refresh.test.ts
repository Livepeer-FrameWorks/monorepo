import { describe, expect, it, vi } from "vitest";
import { createCatalogRefresh } from "./catalog-refresh";

function fixture() {
  let scope: string | null = "tenant-a/filter-a/2";
  const fetchPage = vi.fn(async (offset: number) => ({
    nodes: [offset + 1],
    hasNextPage: offset === 0,
    totalCount: 2,
  }));
  const apply = vi.fn();
  const onError = vi.fn();
  const refresh = createCatalogRefresh({
    scope: () => scope,
    count: () => 2,
    pageSize: 1,
    key: String,
    fetchPage,
    apply,
    onError,
  });
  return {
    refresh,
    fetchPage,
    apply,
    onError,
    setScope: (value: string | null) => (scope = value),
  };
}

describe("catalog background refresh", () => {
  it("refreshes every loaded page and installs rows and metadata together", async () => {
    const f = fixture();
    await f.refresh.run();
    expect(f.fetchPage.mock.calls).toEqual([[0], [1]]);
    expect(f.apply).toHaveBeenCalledExactlyOnceWith([1, 2], {
      nodes: [2],
      hasNextPage: false,
      totalCount: 2,
    });
  });

  it("stops when deletions shrink the window", async () => {
    const f = fixture();
    f.fetchPage.mockResolvedValue({ nodes: [], hasNextPage: false, totalCount: 0 });
    await f.refresh.run();
    expect(f.fetchPage).toHaveBeenCalledTimes(1);
    expect(f.apply).toHaveBeenCalledWith([], expect.objectContaining({ totalCount: 0 }));
  });

  it("deduplicates rows shifted across offset pages by concurrent inserts", async () => {
    const f = fixture();
    f.fetchPage.mockResolvedValueOnce({ nodes: [1], hasNextPage: true, totalCount: 2 });
    f.fetchPage.mockResolvedValueOnce({ nodes: [1], hasNextPage: true, totalCount: 3 });
    await f.refresh.run();
    expect(f.apply).toHaveBeenCalledWith([1], expect.objectContaining({ hasNextPage: true }));
  });

  it.each(["tenant-b/filter-a/2", "tenant-a/filter-b/2", "tenant-a/filter-a/3", null])(
    "drops responses when scope changes to %s",
    async (nextScope) => {
      const f = fixture();
      f.fetchPage.mockImplementationOnce(async () => {
        f.setScope(nextScope);
        return { nodes: [9], hasNextPage: true, totalCount: 2 };
      });
      await f.refresh.run();
      expect(f.apply).not.toHaveBeenCalled();
      expect(f.fetchPage).toHaveBeenCalledTimes(1);
    }
  );

  it("preserves the complete prior window if a later page fails, then retries", async () => {
    const f = fixture();
    f.fetchPage.mockResolvedValueOnce({ nodes: [9], hasNextPage: true, totalCount: 2 });
    f.fetchPage.mockRejectedValueOnce(new Error("offline"));
    await f.refresh.run();
    expect(f.apply).not.toHaveBeenCalled();
    expect(f.onError).toHaveBeenCalledTimes(1);
    await f.refresh.run();
    expect(f.apply).toHaveBeenCalledTimes(1);
  });

  it("coalesces concurrent refreshes and cancels installation on navigation", async () => {
    const f = fixture();
    let complete!: (page: { nodes: number[]; hasNextPage: boolean; totalCount: number }) => void;
    f.fetchPage.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          complete = resolve;
        })
    );
    const first = f.refresh.run();
    await f.refresh.run();
    expect(f.fetchPage).toHaveBeenCalledTimes(1);
    f.refresh.dispose();
    complete({ nodes: [9], hasNextPage: false, totalCount: 1 });
    await first;
    await f.refresh.run();
    expect(f.apply).not.toHaveBeenCalled();
    expect(f.fetchPage).toHaveBeenCalledTimes(1);
  });

  it("does not request data while hidden or unauthenticated", async () => {
    const f = fixture();
    f.setScope(null);
    await f.refresh.run();
    expect(f.fetchPage).not.toHaveBeenCalled();
  });
});

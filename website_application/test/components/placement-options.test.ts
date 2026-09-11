import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import PlacementSelectorEditor from "$lib/components/placement/PlacementSelectorEditor.svelte";

const { fetchOptions } = vi.hoisted(() => ({ fetchOptions: vi.fn() }));
vi.mock("$houdini", () => ({
  GetMediaPlacementOptionsStore: class {
    fetch = fetchOptions;
  },
}));

function page(ids: string[], hasNextPage = false, endCursor: string | null = null) {
  return {
    data: {
      mediaPlacementOptions: {
        __typename: "MediaPlacementOptionsConnection",
        nodes: ids.map((id) => ({ id, name: `Cluster ${id}`, kind: "CLUSTER", eligible: true })),
        pageInfo: { hasNextPage, endCursor },
      },
    },
  };
}

async function openSelector() {
  const onchange = vi.fn();
  const component = render(PlacementSelectorEditor, {
    value: { clusterIds: ["saved"] },
    scope: { kind: "TENANT" },
    onchange,
  });
  await fireEvent.click(screen.getByText("Choose specific clusters, operators or regions"));
  return { ...component, onchange };
}

beforeEach(() => vi.resetAllMocks());
afterEach(cleanup);

describe("authorized placement options", () => {
  it("paginates using the returned cursor and adds stable IDs without subscribing", async () => {
    fetchOptions.mockResolvedValueOnce(page(["us"], true, "page-two"));
    fetchOptions.mockResolvedValueOnce(page(["eu"]));
    const { onchange } = await openSelector();
    await fireEvent.click(screen.getByRole("button", { name: "Search" }));
    await screen.findByText("Cluster us");
    await fireEvent.click(screen.getByRole("button", { name: "Load more" }));
    await screen.findByText("Cluster eu");
    expect(fetchOptions.mock.calls[1][0]).toEqual({
      policy: "NetworkOnly",
      variables: {
        scope: { kind: "TENANT" },
        filter: { kind: "CLUSTER", query: "" },
        first: 30,
        after: "page-two",
      },
    });
    await fireEvent.click(screen.getAllByRole("button", { name: "Add" })[0]);
    expect(onchange).toHaveBeenCalledWith(expect.objectContaining({ clusterIds: ["saved", "us"] }));
    expect(screen.queryByRole("button", { name: "Load more" })).toBeNull();
  });

  it("drops stale pagination after catalogue changes but retains draft selections", async () => {
    fetchOptions.mockResolvedValueOnce(page(["us"], true, "stale"));
    fetchOptions.mockResolvedValueOnce({
      data: {
        mediaPlacementOptions: {
          __typename: "MediaPlacementError",
          code: "REVISION_CONFLICT",
          message: "Available options changed. Restart the search.",
        },
      },
    });
    fetchOptions.mockResolvedValueOnce(page([]));
    const { onchange } = await openSelector();
    await fireEvent.click(screen.getByRole("button", { name: "Search" }));
    await screen.findByText("Cluster us");
    await fireEvent.click(screen.getByRole("button", { name: "Load more" }));
    expect((await screen.findByRole("alert")).textContent).toContain("Search again");
    expect(screen.queryByText("Cluster us")).toBeNull();
    expect(screen.queryByRole("button", { name: "Load more" })).toBeNull();
    expect(screen.getByRole("button", { name: "Remove saved" })).toBeTruthy();
    expect(onchange).not.toHaveBeenCalled();
    await fireEvent.click(screen.getByRole("button", { name: "Search" }));
    await screen.findByText("No authorized options match your search.");
    expect(fetchOptions.mock.calls[2][0].variables.after).toBeNull();
  });

  it("ignores an old response after the search filter changes", async () => {
    let resolveOld!: (value: ReturnType<typeof page>) => void;
    fetchOptions.mockImplementationOnce(() => new Promise((resolve) => (resolveOld = resolve)));
    fetchOptions.mockResolvedValueOnce(page(["new"]));
    await openSelector();
    await fireEvent.click(screen.getByRole("button", { name: "Search" }));
    await fireEvent.input(screen.getByLabelText("Search authorized capacity"), {
      target: { value: "new" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Search" }));
    await screen.findByText("Cluster new");
    resolveOld(page(["old"], true, "old-cursor"));
    await waitFor(() => expect(fetchOptions).toHaveBeenCalledTimes(2));
    expect(screen.queryByText("Cluster old")).toBeNull();
    expect(screen.queryByRole("button", { name: "Load more" })).toBeNull();
    expect(fetchOptions.mock.calls[1][0].variables.filter.query).toBe("new");
  });

  it("removes cached options when authorization is refused on another page", async () => {
    fetchOptions.mockResolvedValueOnce(page(["private"], true, "next"));
    fetchOptions.mockResolvedValueOnce({
      data: { mediaPlacementOptions: { __typename: "AuthError", message: "Access denied." } },
    });
    await openSelector();
    await fireEvent.click(screen.getByRole("button", { name: "Search" }));
    await screen.findByText("Cluster private");
    await fireEvent.click(screen.getByRole("button", { name: "Load more" }));
    await screen.findByText("Access denied.");
    expect(screen.queryByText("Cluster private")).toBeNull();
    expect(screen.queryByRole("button", { name: "Load more" })).toBeNull();
  });
});

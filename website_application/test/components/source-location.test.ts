import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import SourceLocationControl from "$lib/components/stream-details/SourceLocationControl.svelte";
import SourceLocationSummary from "$lib/components/stream-details/SourceLocationSummary.svelte";
import type { SourceLocationDraft } from "$lib/source-location";

const { fetchOptions } = vi.hoisted(() => ({ fetchOptions: vi.fn() }));
vi.mock("$houdini", () => ({
  GetMediaPlacementOptionsStore: class {
    fetch = fetchOptions;
  },
}));

const clusters = [
  {
    clusterId: "home",
    clusterName: "Home cluster",
    accessLevel: "owner",
    allowPrivatePullSources: true,
  },
  {
    clusterId: "market",
    clusterName: "Marketplace cluster",
    accessLevel: "subscriber",
    allowPrivatePullSources: false,
  },
];

const any: SourceLocationDraft = { mode: "ANY", clusters: [], avoidNodeIds: [] };

function nodes(clusterId: string, ids: string[]) {
  return {
    data: {
      mediaPlacementOptions: {
        __typename: "MediaPlacementOptionsConnection",
        nodes: ids.map((id) => ({
          id,
          name: `Node ${id}`,
          kind: "NODE",
          clusterId,
          eligible: true,
        })),
        pageInfo: { hasNextPage: false, endCursor: null },
      },
    },
  };
}

beforeEach(() => vi.resetAllMocks());
afterEach(cleanup);

describe("source location control", () => {
  it("lets a public source choose any cluster or restrict to listed clusters", async () => {
    const onchange = vi.fn();
    const view = render(SourceLocationControl, {
      value: any,
      clusters,
      sourceClass: "public",
      onchange,
    });
    const anyRadio = screen.getByRole("radio", { name: /Any cluster FrameWorks chooses/ });
    expect((anyRadio as HTMLInputElement).checked).toBe(true);
    expect((anyRadio as HTMLInputElement).disabled).toBe(false);
    expect(screen.queryByRole("alert")).toBeNull();

    await fireEvent.click(screen.getByRole("radio", { name: /Only these clusters/ }));
    expect(onchange).toHaveBeenLastCalledWith({
      mode: "RESTRICTED",
      clusters: [],
      avoidNodeIds: [],
    });

    await view.rerender({ value: { mode: "RESTRICTED", clusters: [], avoidNodeIds: [] } });
    expect(screen.getByRole("alert").textContent).toContain("Choose at least one cluster.");
    await fireEvent.click(screen.getByRole("checkbox", { name: /Marketplace cluster/ }));
    expect(onchange).toHaveBeenLastCalledWith({
      mode: "RESTRICTED",
      clusters: [{ clusterId: "market", nodeIds: [] }],
      avoidNodeIds: [],
    });
    expect(fetchOptions).not.toHaveBeenCalled();
  });

  it("offers private sources only clusters that allow private pulls", async () => {
    render(SourceLocationControl, {
      value: { mode: "RESTRICTED", clusters: [], avoidNodeIds: [] },
      clusters,
      sourceClass: "private",
      onchange: vi.fn(),
    });
    expect(
      (screen.getByRole("radio", { name: /Any cluster FrameWorks chooses/ }) as HTMLInputElement)
        .disabled
    ).toBe(true);
    expect(screen.getByRole("checkbox", { name: /Home cluster/ })).toBeTruthy();
    expect(screen.queryByRole("checkbox", { name: /Marketplace cluster/ })).toBeNull();
    expect(
      screen.getByText(/1 cluster is hidden because private and multicast sources/)
    ).toBeTruthy();
  });

  it("flags a private source left on any cluster", () => {
    render(SourceLocationControl, {
      value: any,
      clusters,
      sourceClass: "private",
      onchange: vi.fn(),
    });
    expect(screen.getByRole("alert").textContent).toContain(
      "Private and multicast sources need specific clusters"
    );
  });

  it("narrows an owned cluster to specific nodes and avoided nodes", async () => {
    fetchOptions.mockResolvedValue(nodes("home", ["edge-1", "edge-2"]));
    const onchange = vi.fn();
    const selected: SourceLocationDraft = {
      mode: "RESTRICTED",
      clusters: [{ clusterId: "home", nodeIds: [] }],
      avoidNodeIds: [],
    };
    const view = render(SourceLocationControl, {
      value: selected,
      clusters,
      sourceClass: "private",
      onchange,
    });
    await screen.findByText("Avoid nodes");
    expect(fetchOptions.mock.calls[0][0].variables).toMatchObject({
      scope: { kind: "TENANT" },
      filter: { kind: "NODE", clusterId: "home" },
    });

    await fireEvent.click(screen.getByRole("radio", { name: "Only specific nodes" }));
    await fireEvent.click(screen.getAllByRole("checkbox", { name: "Node edge-1" })[0]);
    expect(onchange).toHaveBeenLastCalledWith({
      mode: "RESTRICTED",
      clusters: [{ clusterId: "home", nodeIds: ["edge-1"] }],
      avoidNodeIds: [],
    });

    await view.rerender({
      value: { ...selected, clusters: [{ clusterId: "home", nodeIds: ["edge-1"] }] },
    });
    await fireEvent.click(screen.getByText("Avoid nodes"));
    const avoid = screen.getAllByRole("checkbox", { name: "Node edge-2" }).at(-1)!;
    await fireEvent.click(avoid);
    expect(onchange).toHaveBeenLastCalledWith({
      mode: "RESTRICTED",
      clusters: [{ clusterId: "home", nodeIds: ["edge-1"] }],
      avoidNodeIds: ["edge-2"],
    });
  });

  it("does not load nodes for clusters the tenant does not own", async () => {
    render(SourceLocationControl, {
      value: {
        mode: "RESTRICTED",
        clusters: [{ clusterId: "market", nodeIds: [] }],
        avoidNodeIds: [],
      },
      clusters,
      sourceClass: "public",
      onchange: vi.fn(),
    });
    expect(screen.queryByRole("radio", { name: "Only specific nodes" })).toBeNull();
    expect(fetchOptions).not.toHaveBeenCalled();
  });
});

describe("source location summary", () => {
  it("summarizes restricted clusters with node counts", () => {
    render(SourceLocationSummary, {
      location: {
        mode: "RESTRICTED",
        clusters: [{ clusterId: "home", nodeIds: ["edge-1", "edge-2"] }],
        avoidNodeIds: ["edge-3"],
      },
      clusterName: () => "Home cluster",
    });
    expect(screen.getByText("Home cluster · 2 nodes")).toBeTruthy();
    expect(screen.getByText(/Avoids 1\s+node/)).toBeTruthy();
  });

  it("links a custom location to the placement rules", () => {
    render(SourceLocationSummary, {
      location: { mode: "CUSTOM", clusters: [], avoidNodeIds: [] },
      placementHref: "/streams/s1/placement?verb=ingest",
    });
    expect(screen.getByText("Custom placement rules")).toBeTruthy();
    expect(
      screen
        .getByRole("link", { name: /View and change them in the stream's placement rules/ })
        .getAttribute("href")
    ).toBe("/streams/s1/placement?verb=ingest");
  });
});

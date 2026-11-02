import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor, within } from "@testing-library/svelte";
import NetworkPage from "../../src/routes/infrastructure/network/+page.svelte";
import ClustersPage from "../../src/routes/infrastructure/clusters/+page.svelte";

const mocks = vi.hoisted(() => ({
  nodeCounts: new Map<string, number>(),
  nodeFetches: [] as string[],
}));

vi.mock("$lib/components/charts/RoutingMap.svelte", async () => ({
  default: (await import("./fixtures/StreamCrafterStub.svelte")).default,
}));

vi.mock("$houdini", async () => {
  const { readable } = await import("svelte/store");
  const clusters = [
    {
      clusterId: "cluster-new",
      clusterName: "New private cluster",
      healthStatus: "unknown",
      isDefaultCluster: false,
      isPlatformOfficial: false,
    },
    {
      clusterId: "cluster-shared",
      clusterName: "Shared cluster",
      healthStatus: "unknown",
      isDefaultCluster: false,
      isPlatformOfficial: false,
    },
    {
      clusterId: "cluster-live",
      clusterName: "Live cluster",
      healthStatus: "healthy",
      isDefaultCluster: true,
      isPlatformOfficial: true,
    },
  ];
  const results: Record<string, unknown> = {
    GetMySubscriptionsStore: { mySubscriptions: clusters },
    GetClustersAccessStore: {
      clustersAccess: [
        { clusterId: "cluster-new", accessLevel: "owner" },
        { clusterId: "cluster-shared", accessLevel: "subscriber" },
        { clusterId: "cluster-live", accessLevel: "owner" },
      ],
    },
    GetMyClusterInvitesStore: { myClusterInvites: [] },
    GetNetworkStatusStore: { networkStatus: { nodes: [] } },
    GetPendingSubscriptionsStore: { pendingSubscriptions: [] },
    GetMarketplaceClustersStore: { marketplaceClusters: [] },
  };
  function storeClass(name: string) {
    return class {
      private value = { data: results[name] ?? null, errors: null, fetching: false };
      private readonlyStore = readable(this.value);
      subscribe = this.readonlyStore.subscribe;
      async fetch(args?: { variables?: { clusterId?: string } }) {
        if (name === "GetNodesConnectionStore") {
          const clusterId = args?.variables?.clusterId ?? "";
          mocks.nodeFetches.push(clusterId);
          return {
            data: { nodesConnection: { totalCount: mocks.nodeCounts.get(clusterId) ?? 0 } },
          };
        }
        return this.value;
      }
      async mutate() {
        return this.value;
      }
    };
  }
  return new Proxy(
    { fragment: (value: unknown) => readable(value) },
    {
      get(target, prop: string) {
        if (prop in target) return target[prop as keyof typeof target];
        if (prop.endsWith("Store")) return storeClass(prop);
        return undefined;
      },
      has: () => true,
    }
  );
});

afterEach(() => {
  cleanup();
  mocks.nodeCounts.clear();
  mocks.nodeFetches.length = 0;
});

function statusFor(clusterName: string) {
  const row = screen
    .getAllByText(clusterName)
    .map((element) => element.closest("tr"))
    .find(Boolean);
  if (!row) throw new Error(`no row for ${clusterName}`);
  const statusCell = row.querySelectorAll("td")[1];
  return within(statusCell as HTMLElement).getByText(/\S/, { selector: "span" });
}

describe.each([
  ["network", NetworkPage],
  ["clusters", ClustersPage],
])("%s page cluster status", (_name, Page) => {
  it("shows an owned cluster with no nodes as waiting for its first edge", async () => {
    render(Page);
    await screen.findByText("New private cluster");
    await waitFor(() =>
      expect(statusFor("New private cluster").textContent?.trim()).toBe("Waiting for first edge")
    );
    expect(statusFor("New private cluster").className).not.toContain("text-success");
  });

  it("keeps the reported status and only looks up nodes the tenant owns", async () => {
    render(Page);
    await screen.findByText("Shared cluster");
    expect(statusFor("Live cluster").textContent?.trim()).toBe("healthy");
    expect(statusFor("Live cluster").className).toContain("text-success");
    await waitFor(() => expect(mocks.nodeFetches).toContain("cluster-new"));
    expect(statusFor("Shared cluster").textContent?.trim()).toBe("unknown");
    expect(mocks.nodeFetches).not.toContain("cluster-shared");
    expect(mocks.nodeFetches).not.toContain("cluster-live");
  });
});

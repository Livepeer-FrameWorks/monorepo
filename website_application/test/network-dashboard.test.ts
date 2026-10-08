// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { mountNetworkDashboard } from "../../packages/map-core/network-dashboard";
import { workFixture } from "./fixtures/network-work";

const mapHarness = vi.hoisted(() => ({ module: undefined as object | undefined }));
vi.mock("../../packages/map-core/index", () => ({
  loadMapLibre: () =>
    mapHarness.module
      ? Promise.resolve(mapHarness.module)
      : Promise.reject(new Error("Map disabled in DOM test")),
  readRuntimePublicConfig: () => ({}),
  basemapMapOptions: () => ({}),
  addRequiredAttribution: () => {},
}));
let stop: (() => void) | undefined;
afterEach(() => {
  stop?.();
  vi.unstubAllGlobals();
  document.body.replaceChildren();
  window.history.replaceState({}, "", "/");
  mapHarness.module = undefined;
});
function fixture() {
  const now = new Date().toISOString();
  const cohort = {
    bucket: now,
    kind: "segment",
    resource_id: "0xabc",
    ip: "192.0.2.1",
    region: "eu",
    profile_key: "720p",
    samples: 1,
    qualified_samples: 1,
    successes: 1,
    failures: 0,
    p50_ms: 100,
    p95_ms: 150,
    p99_ms: 180,
    duration_samples: 1,
    p95_duration_ratio: 0.05,
    retries: 0,
    fallbacks: 0,
    deadline_misses: 0,
  };
  return {
    history_status: "available",
    inventory: {
      generated_at: now,
      resources: [{ id: "0xabc", uri: "https://orch.example", active: true }],
      endpoints: [
        {
          id: "endpoint",
          resource: { id: "0xabc", uri: "https://orch.example", active: true },
          ip: "192.0.2.1",
          status: "up",
          location: { known: false },
          cadence_seconds: 60,
          last_check: now,
          last_success: now,
        },
      ],
    },
    history: {
      generated_at: now,
      start: new Date(Date.now() - 86400000).toISOString(),
      end: now,
      interval_seconds: 300,
      cohorts: [cohort],
      summaries: [{ ...cohort, samples: 10, qualified_samples: 10 }],
      observations: [],
      truncated: true,
      rolled_up: false,
    },
  };
}
function mount(data: ReturnType<typeof fixture>) {
  const fetch = vi
    .fn()
    .mockImplementation(() =>
      Promise.resolve({ ok: true, json: async () => structuredClone(data) })
    );
  vi.stubGlobal("fetch", fetch);
  const container = document.createElement("div");
  document.body.append(container);
  stop = mountNetworkDashboard(container, { apiURL: "https://api.example/v1/network/livepeer" });
  return { container, fetch };
}
describe("shared network dashboard", () => {
  it("does not present missing media duration as zero minutes", async () => {
    const base = fixture();
    const data = {
      ...base,
      history: {
        ...base.history,
        work: {
          scope: "frameworks-gateways",
          start: base.history.start,
          end: base.history.end,
          interval_seconds: 0,
          truncated: false,
          summaries: [
            workFixture({
              accepted_source_ms: "0",
              dispatched_source_ms: "0",
              accepted_duration_samples: 0,
              dispatched_duration_samples: 0,
            }),
          ],
        },
      },
    };
    const { container } = mount(data);
    await vi.waitFor(() =>
      expect(
        container.querySelector('[aria-label="Orchestrator workload totals"] tbody tr')
      ).not.toBeNull()
    );
    const row = container.querySelector(
      '[aria-label="Orchestrator workload totals"] tbody tr'
    ) as HTMLTableRowElement;
    expect(row.cells[1].textContent).toBe("Not accounted");
    expect(row.cells[2].textContent).toBe("Not accounted");
    expect(row.textContent).toContain("Partial accounting");
  });
  it("narrows the server-side work query when the observer region changes", async () => {
    const base = fixture();
    const data = {
      ...base,
      history: {
        ...base.history,
        work: {
          scope: "frameworks-gateways",
          start: base.history.start,
          end: base.history.end,
          interval_seconds: 0,
          truncated: true,
          summaries: [],
        },
      },
    };
    const { container, fetch } = mount(data);
    await vi.waitFor(() => expect(container.querySelector("tbody button")).not.toBeNull());
    const region = container.querySelectorAll("select")[1] as HTMLSelectElement;
    region.value = "eu";
    region.dispatchEvent(new Event("change"));
    await vi.waitFor(() => expect(fetch).toHaveBeenCalledTimes(2));
    expect((fetch.mock.calls[1][0] as URL).searchParams.get("region")).toBe("eu");
  });
  it("aggregates orchestrator work across sibling endpoints independently of truncated charts", async () => {
    const base = fixture();
    const data = {
      ...base,
      history: {
        ...base.history,
        work: {
          scope: "frameworks-gateways",
          start: base.history.start,
          end: base.history.end,
          interval_seconds: 0,
          truncated: false,
          summaries: [
            workFixture(),
            workFixture({ ip: "192.0.2.2", accepted_source_ms: "120000", unaccounted_samples: 3 }),
          ],
        },
      },
    };
    const { container } = mount(data);
    await vi.waitFor(() =>
      expect(
        container.querySelector('[aria-label="Orchestrator workload totals"] tbody tr')
      ).not.toBeNull()
    );
    const row = container.querySelector('[aria-label="Orchestrator workload totals"] tbody tr')!;
    expect(row.textContent).toContain("3.00");
    expect(row.textContent).toContain("18014398509481986");
    expect(row.textContent).toContain("Partial accounting");
    (row.querySelector("button") as HTMLButtonElement).click();
    await vi.waitFor(() =>
      expect(container.querySelector("aside")?.textContent).toContain(
        "Orchestrator work across all observed endpoints"
      )
    );
    expect(container.querySelector("aside")?.textContent).toContain(
      "Accepted source minutes: 3.00"
    );
    expect(container.querySelector("aside")?.textContent).toContain("redeemed earnings");
  });
  it("ranks failures by qualifying outcomes rather than unrelated recorded events", async () => {
    const data = fixture();
    data.inventory.endpoints.push({
      ...data.inventory.endpoints[0],
      id: "reliable",
      ip: "192.0.2.2",
      resource: { ...data.inventory.endpoints[0].resource, id: "0xdef" },
    });
    data.history.summaries = [
      { ...data.history.summaries[0], samples: 100, qualified_samples: 10, failures: 5 },
      {
        ...data.history.summaries[0],
        resource_id: "0xdef",
        ip: "192.0.2.2",
        samples: 20,
        qualified_samples: 20,
        failures: 2,
      },
    ];
    window.history.replaceState(
      {},
      "",
      "/network?view=livepeer&lp_region=eu&lp_profile=720p&lp_sort=failures"
    );
    const { container } = mount(data);
    await vi.waitFor(() => expect(container.querySelector("tbody button")).not.toBeNull());
    expect(container.querySelector("tbody button")?.textContent).toBe("192.0.2.2");
  });
  it("keeps fresh price ordering exact above JavaScript's integer precision", async () => {
    const data = fixture();
    const q = {
      numerator: "9007199254740993",
      denominator: "3",
      currency: "WEI",
      unit: "pixel",
      observed_at: new Date().toISOString(),
    };
    const expensive = { ...data.inventory.endpoints[0], price: q };
    const cheap = {
      ...expensive,
      id: "cheap",
      ip: "192.0.2.2",
      resource: { ...expensive.resource, id: "0xdef" },
      price: { ...q, numerator: "9007199254740992" },
    };
    const stale = {
      ...cheap,
      id: "stale",
      ip: "192.0.2.3",
      price: { ...q, numerator: "1", observed_at: new Date(Date.now() - 86400000).toISOString() },
    };
    data.inventory.endpoints = [expensive, stale, cheap];
    const { container } = mount(data);
    await vi.waitFor(() => expect(container.querySelector("tbody button")).not.toBeNull());
    const order = container.querySelector(
      'select[aria-label="Sort endpoints"]'
    ) as HTMLSelectElement;
    order.value = "price";
    order.dispatchEvent(new Event("change"));
    const rows = Array.from(container.querySelectorAll(".list tbody tr"));
    expect(rows.map((row) => row.querySelector("button")?.textContent)).toEqual([
      "192.0.2.2",
      "192.0.2.1",
      "192.0.2.3",
    ]);
    expect(rows[2].textContent).toContain("Stale / undated quote");
  });
  it("renders independently toggleable gateway and prober layers without inventing unknown locations", async () => {
    const sources = new Map<
      string,
      { data: { features: unknown[] }; setData: (data: { features: unknown[] }) => void }
    >();
    const visibility = vi.fn();
    class TestMap {
      on(event: string, callback: unknown) {
        if (event === "load") queueMicrotask(callback as () => void);
      }
      addControl() {}
      isStyleLoaded() {
        return true;
      }
      getSource(id: string) {
        return sources.get(id);
      }
      addSource(id: string, source: { data: { features: unknown[] } }) {
        sources.set(id, {
          data: source.data,
          setData: (next) => {
            sources.get(id)!.data = next;
          },
        });
      }
      addLayer() {}
      setLayoutProperty = visibility;
      remove() {}
    }
    mapHarness.module = { Map: TestMap, NavigationControl: class {} };
    const data = {
      ...fixture(),
      inventory: {
        ...fixture().inventory,
        observers: [
          {
            id: "site-a",
            kind: "prober",
            region: "eu",
            last_seen: new Date().toISOString(),
            location: { known: true, latitude: 52, longitude: 5, source: "mmdb" },
          },
          { id: "site-b", kind: "prober", region: "us", location: { known: false } },
        ],
      },
    };
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((url) =>
        Promise.resolve({
          ok: true,
          json: async () =>
            typeof url === "string"
              ? {
                  data: {
                    networkStatus: {
                      nodes: [],
                      clusters: [
                        {
                          clusterId: "eu",
                          name: "Europe",
                          region: "eu",
                          latitude: 52,
                          longitude: 5,
                          services: ["livepeer-gateway"],
                        },
                      ],
                    },
                  },
                }
              : structuredClone(data),
        })
      )
    );
    const container = document.createElement("div");
    document.body.append(container);
    stop = mountNetworkDashboard(container, {
      apiURL: "https://api.example/v1/network/livepeer",
      graphqlURL: "https://api.example/graphql",
    });
    await vi.waitFor(() => expect(sources.get("gateway-clusters")?.data.features).toHaveLength(1));
    expect(sources.get("prober-sites")?.data.features).toHaveLength(1);
    expect(container.querySelector(".observation-sites")?.textContent).toContain(
      "Location unknown"
    );
    const label = Array.from(container.querySelectorAll("label")).find(
      (entry) => entry.textContent === "Probers"
    );
    const toggle = label?.querySelector("input") as HTMLInputElement;
    toggle.checked = false;
    toggle.dispatchEvent(new Event("change"));
    expect(visibility).toHaveBeenCalledWith("prober-sites", "visibility", "none");
    expect(visibility).toHaveBeenCalledWith("gateway-clusters", "visibility", "visible");
  });
  it("starts the new regional history query while the previous region is still pending", async () => {
    const data = fixture();
    data.history.summaries.push({ ...data.history.summaries[0], region: "us" });
    const fetch = vi
      .fn()
      .mockResolvedValueOnce({ ok: true, json: async () => structuredClone(data) })
      .mockImplementation(() => new Promise(() => {}));
    vi.stubGlobal("fetch", fetch);
    const container = document.createElement("div");
    document.body.append(container);
    stop = mountNetworkDashboard(container, { apiURL: "https://api.example/v1/network/livepeer" });
    await vi.waitFor(() => expect(container.querySelector("tbody button")).not.toBeNull());
    (container.querySelector("tbody button") as HTMLButtonElement).click();
    expect(fetch).toHaveBeenCalledTimes(2);
    const region = container.querySelectorAll("select")[1] as HTMLSelectElement;
    region.value = "us";
    region.dispatchEvent(new Event("change"));
    expect(fetch).toHaveBeenCalledTimes(3);
    expect((fetch.mock.calls[2][0] as URL).searchParams.get("region")).toBe("us");
    expect(container.querySelector("aside")?.textContent).not.toContain("eu / 720p");
  });
  it("restores a shared Livepeer view and switches presentation without refetching", async () => {
    window.history.replaceState(
      {},
      "",
      "/network?view=livepeer&lp_mode=map&lp_region=eu&lp_profile=720p&lp_endpoint=endpoint"
    );
    const { container, fetch } = mount(fixture());
    await vi.waitFor(() =>
      expect(container.querySelector("aside.detail")?.textContent).toContain("192.0.2.1")
    );
    const mapButton = Array.from(container.querySelectorAll("button")).find(
      (button) => button.textContent === "Map"
    );
    expect(mapButton?.getAttribute("aria-pressed")).toBe("true");
    const count = fetch.mock.calls.length;
    const compare = Array.from(container.querySelectorAll("button")).find(
      (button) => button.textContent === "Compare"
    );
    compare?.click();
    expect(container.querySelector(".map")?.getAttribute("data-mode")).toBe("compare");
    expect(fetch).toHaveBeenCalledTimes(count);
    expect(container.querySelector("aside.detail")?.textContent).toContain("192.0.2.1");
  });
  it("compares performance only within one region and output profile", async () => {
    const data = fixture();
    data.inventory.endpoints.push({
      ...data.inventory.endpoints[0],
      id: "faster",
      ip: "192.0.2.2",
      resource: { ...data.inventory.endpoints[0].resource, id: "0xdef" },
    });
    const attempt = { ...data.history.summaries[0], kind: "attempt" };
    data.history.summaries.push(
      { ...attempt, p95_ms: 300 },
      { ...attempt, resource_id: "0xdef", ip: "192.0.2.2", p95_ms: 100 }
    );
    const { container } = mount(data);
    await vi.waitFor(() => expect(container.querySelector("tbody button")).not.toBeNull());
    const sort = container.querySelector(
      'select[aria-label="Sort endpoints"]'
    ) as HTMLSelectElement;
    expect(sort).not.toBeNull();
    sort.value = "latency";
    sort.dispatchEvent(new Event("change"));
    expect(container.textContent).toContain("Choose one observer region and profile");
    const selects = container.querySelectorAll("select");
    const region = selects[1] as HTMLSelectElement;
    const profiles = selects[2] as HTMLSelectElement;
    region.value = "eu";
    region.dispatchEvent(new Event("change"));
    profiles.value = "720p";
    profiles.dispatchEvent(new Event("change"));
    expect(container.querySelector("tbody button")?.textContent).toBe("192.0.2.2");
  });
  it("uses window totals instead of bounded chart samples and loads endpoint history", async () => {
    const { container, fetch } = mount(fixture());
    await vi.waitFor(() =>
      expect(container.querySelector("tbody tr")?.textContent).toContain("10")
    );
    expect(container.textContent).toContain("Geography unknown");
    expect(container.textContent).toContain("truncated");
    (container.querySelector("tbody button") as HTMLButtonElement).click();
    await vi.waitFor(() => expect(fetch).toHaveBeenCalledTimes(2));
    const url = fetch.mock.calls[1][0] as URL;
    expect(url.searchParams.get("resource")).toBe("0xabc");
    expect(url.searchParams.get("ip")).toBe("192.0.2.1");
  });
  it("keeps current inventory visible when history is unavailable", async () => {
    const data = fixture();
    data.history_status = "unavailable";
    data.history.cohorts = [];
    data.history.summaries = [];
    const { container } = mount(data);
    await vi.waitFor(() =>
      expect(container.textContent).toContain("History is temporarily unavailable")
    );
    expect(container.textContent).toContain("192.0.2.1");
    expect(container.textContent).toContain("Unknown");
  });
  it("renders public resource metadata as text", async () => {
    const data = fixture();
    data.inventory.endpoints[0].resource.id = '<img src=x onerror="alert(1)">';
    const { container } = mount(data);
    await vi.waitFor(() => expect(container.textContent).toContain("<img src=x"));
    expect(container.querySelector("img")).toBeNull();
  });
  it("shows retained discovery cohorts and coverage while filtering workload profiles", async () => {
    const data = fixture();
    const discovery = {
      ...data.history.cohorts[0],
      bucket: new Date(Date.now() - 3600000).toISOString(),
      kind: "discovery",
      profile_key: "",
      samples: 10,
      qualified_samples: 10,
      successes: 9,
      failures: 1,
    };
    data.history.rolled_up = true;
    data.history.cohorts.push(discovery);
    data.history.summaries.push(discovery);
    const { container } = mount(data);
    await vi.waitFor(() => expect(container.querySelector("tbody button")).not.toBeNull());
    const profiles = container.querySelectorAll("select")[2] as HTMLSelectElement;
    profiles.value = "720p";
    profiles.dispatchEvent(new Event("change"));
    (container.querySelector("tbody button") as HTMLButtonElement).click();
    await vi.waitFor(() => expect(container.textContent).toContain("Regional discovery latency"));
    expect(container.querySelector("aside.detail")?.textContent).toContain("9 / 10");
    expect(container.querySelector(".timeline span.up")).not.toBeNull();
  });
});

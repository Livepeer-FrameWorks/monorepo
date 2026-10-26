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
  bindModifierScrollZoom: () => () => {},
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
async function openWorker(container: HTMLElement, id = "endpoint") {
  await vi.waitFor(() => expect(container.querySelector(".network-counts")).not.toBeNull());
  Array.from(container.querySelectorAll("button"))
    .find((b) => b.textContent === "Browse workers")!
    .click();
  (container.querySelector(`[data-endpoint="${id}"]`) as HTMLButtonElement).click();
  return container.querySelector(".worker-inspector") as HTMLElement;
}
function tab(inspector: HTMLElement, name: string) {
  Array.from(inspector.querySelectorAll("button"))
    .find((b) => b.textContent === name)!
    .click();
}
function withWork(summaries = [workFixture()], extra = {}) {
  const base = fixture();
  return {
    ...base,
    history: {
      ...base.history,
      work: {
        scope: "frameworks-gateways",
        start: base.history.start,
        end: base.history.end,
        interval_seconds: 0,
        truncated: false,
        summaries,
        ...extra,
      },
    },
  };
}
function fact(container: Element, name: string) {
  return Array.from(container.querySelectorAll("dl > div"))
    .find((row) => row.querySelector("dt")?.textContent === name)
    ?.querySelector("dd")?.textContent;
}
describe("shared public network evidence", () => {
  it("keeps work totals visible when ticket credit exceeds its independent query bound", async () => {
    const { container } = mount(
      withWork([workFixture({ ticket_credit_sent: undefined, ticket_credit_incomplete: true })], {
        ticket_credit_truncated: true,
      })
    );
    const inspector = await openWorker(container);
    tab(inspector, "Work");
    expect(inspector.textContent).toContain("Ticket totals are incomplete");
    expect(fact(inspector, "Accepted source minutes")).toBe("1.00");
    expect(fact(inspector, "Expected ticket credit sent")).toBe("Incomplete");
  });
  it("does not present missing media duration as zero minutes", async () => {
    const { container } = mount(
      withWork([
        workFixture({
          accepted_source_ms: "0",
          dispatched_source_ms: "0",
          accepted_duration_samples: 0,
          dispatched_duration_samples: 0,
        }),
      ])
    );
    const inspector = await openWorker(container);
    tab(inspector, "Work");
    expect(fact(inspector, "Accepted source minutes")).toBe("No data");
    expect(fact(inspector, "Submitted source minutes")).toBe("No data");
    expect(inspector.textContent).toContain("Some work data is missing");
  });
  it("narrows the server-side work query when the observer region changes", async () => {
    const { container, fetch } = mount(withWork([], { truncated: true }));
    await vi.waitFor(() => expect(container.querySelector(".network-counts")).not.toBeNull());
    const region = container.querySelector('[aria-label="History region"]') as HTMLSelectElement;
    region.value = "eu";
    region.dispatchEvent(new Event("change"));
    await vi.waitFor(() => expect(fetch).toHaveBeenCalledTimes(2));
    expect((fetch.mock.calls[1][0] as URL).searchParams.get("region")).toBe("eu");
  });
  it("aggregates orchestrator work across siblings independently of truncated charts", async () => {
    const { container } = mount(
      withWork([
        workFixture(),
        workFixture({ ip: "192.0.2.2", accepted_source_ms: "120000", unaccounted_samples: 3 }),
      ])
    );
    const inspector = await openWorker(container);
    tab(inspector, "Work");
    const total = Array.from(inspector.querySelectorAll("section")).find((s) =>
      s.textContent?.includes("Orchestrator across all observed workers")
    )!;
    expect(fact(total, "Accepted source minutes")).toBe("3.00");
    expect(total.textContent).toContain("18014398509481986");
    expect(total.textContent).toContain("Some work data is missing");
    expect(inspector.textContent).toContain("confirmed earnings");
  });
  it("uses window summary failures instead of unrelated events or bounded chart samples", async () => {
    const data = fixture();
    data.history.summaries[0] = {
      ...data.history.summaries[0],
      samples: 100,
      qualified_samples: 10,
      failures: 5,
    };
    const { container } = mount(data);
    const inspector = await openWorker(container);
    expect(fact(inspector, "Observed segments")).toBe("100");
    expect(fact(inspector, "Observed errors")).toBe("5");
  });
  it("formats a large rational price without floating-point rounding and marks stale quotes", async () => {
    const data = {
      ...fixture(),
      inventory: {
        ...fixture().inventory,
        endpoints: [
          {
            ...fixture().inventory.endpoints[0],
            price: {
              numerator: "9007199254740993",
              denominator: "3",
              currency: "WEI",
              unit: "pixel",
              observed_at: new Date(Date.now() - 86400000).toISOString(),
            },
          },
        ],
      },
    };
    const { container } = mount(data);
    const inspector = await openWorker(container);
    expect(fact(inspector, "Advertised price")).toBe("3002399751580.331 gwei / MP");
    expect(inspector.textContent).toContain("Stale / undated quote");
  });
  it("does not label a tiny positive quote as free", async () => {
    const data = {
      ...fixture(),
      inventory: {
        ...fixture().inventory,
        endpoints: [
          {
            ...fixture().inventory.endpoints[0],
            price: {
              numerator: "1",
              denominator: "1000000",
              currency: "wei",
              unit: "pixel",
              observed_at: new Date().toISOString(),
            },
          },
        ],
      },
    };
    const { container } = mount(data);
    const inspector = await openWorker(container);
    expect(fact(inspector, "Advertised price")).toBe("<0.0001 gwei / MP");
  });
  it("toggles gateway and prober layers independently without inventing unknown locations", async () => {
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
      getLayer() {
        return undefined;
      }
      hasImage() {
        return false;
      }
      addImage() {}
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
    (container.querySelector('[data-region="us"]') as HTMLButtonElement).click();
    (container.querySelector(".site-row") as HTMLButtonElement).click();
    expect(container.querySelector(".worker-inspector")?.textContent).toContain("Location unknown");
    const toggle = container.querySelector(".map-check input") as HTMLInputElement;
    const label = Array.from(container.querySelectorAll("label")).find(
      (l) => l.textContent === "Probers"
    )!;
    (label.querySelector("input") as HTMLInputElement).checked = false;
    label.querySelector("input")!.dispatchEvent(new Event("change"));
    expect(toggle).not.toBeNull();
    expect(visibility).toHaveBeenCalledWith("prober-sites", "visibility", "none");
    expect(visibility).toHaveBeenCalledWith("gateway-clusters", "visibility", "visible");
  });
  it("starts the new regional query while previous worker history is pending", async () => {
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
    await openWorker(container);
    expect(fetch).toHaveBeenCalledTimes(2);
    const region = container.querySelector('[aria-label="History region"]') as HTMLSelectElement;
    region.value = "us";
    region.dispatchEvent(new Event("change"));
    expect(fetch).toHaveBeenCalledTimes(3);
    expect((fetch.mock.calls[2][0] as URL).searchParams.get("region")).toBe("us");
  });
  it("restores a shared worker and changes map emphasis without refetching", async () => {
    window.history.replaceState(
      {},
      "",
      "/network?view=livepeer&lp_mode=map&lp_region=eu&lp_profile=720p&lp_endpoint=endpoint"
    );
    const { container, fetch } = mount(fixture());
    await vi.waitFor(() =>
      expect(container.querySelector(".worker-inspector")?.textContent).toContain("192.0.2.1")
    );
    await vi.waitFor(() => expect(fetch).toHaveBeenCalledTimes(2));
    const metric = container.querySelector('[aria-label="Map shows"]') as HTMLSelectElement;
    metric.value = "errors";
    metric.dispatchEvent(new Event("change"));
    expect(fetch).toHaveBeenCalledTimes(2);
    expect(window.location.search).toContain("lp_metric=errors");
    expect(container.querySelector(".worker-inspector")?.textContent).toContain("192.0.2.1");
  });
  it("shows worker performance immediately with local profile and region controls", async () => {
    const data = fixture();
    data.history.truncated = false;
    data.history.cohorts.push({ ...data.history.cohorts[0], region: "us", p95_ms: 450 });
    data.history.summaries.push({ ...data.history.summaries[0], region: "us", p95_ms: 450 });
    const { container } = mount(data);
    const inspector = await openWorker(container);
    expect(inspector.textContent).toContain("Transcoding performance");
    expect(inspector.textContent).not.toContain("Choose one measurement region");
    const profile = inspector.querySelector(
      '[aria-label="Worker performance profile"]'
    ) as HTMLSelectElement;
    expect(profile.value).toBe("720p");
    const region = inspector.querySelector(
      '[aria-label="Worker performance region"]'
    ) as HTMLSelectElement;
    expect(region.value).toBe("eu");
    expect(fact(inspector, "p95 response time")).toBe("150 ms");
    expect(inspector.querySelector('[aria-label="Response time (ms)"]')).not.toBeNull();
    expect(inspector.querySelector('[aria-label="Time (local)"]')).not.toBeNull();
    expect(inspector.querySelector("svg.observation-chart")).not.toBeNull();
    region.value = "us";
    region.dispatchEvent(new Event("change"));
    expect(fact(inspector, "p95 response time")).toBe("450 ms");
    expect(
      (container.querySelector('[aria-label="History region"]') as HTMLSelectElement).value
    ).toBe("");
  });
  it("keeps response timing visible when source duration was not reported", async () => {
    const data = fixture();
    data.history.cohorts[0].duration_samples = 0;
    data.history.summaries[0].duration_samples = 0;
    const { container } = mount(data);
    const inspector = await openWorker(container);
    expect(fact(inspector, "p95 response time")).toBe("150 ms");
    expect(fact(inspector, "Video time used (p95)")).toBe("No duration samples");
    expect(inspector.querySelector("svg.observation-chart")).not.toBeNull();
  });
  it("separates output profiles in the worker's own performance controls", async () => {
    const data = fixture();
    data.history.cohorts.push({ ...data.history.cohorts[0], profile_key: "1080p", p95_ms: 900 });
    data.history.summaries.push({
      ...data.history.summaries[0],
      profile_key: "1080p",
      p95_ms: 900,
    });
    const { container } = mount(data);
    const inspector = await openWorker(container);
    const profile = inspector.querySelector(
      '[aria-label="Worker performance profile"]'
    ) as HTMLSelectElement;
    profile.value = "1080p";
    profile.dispatchEvent(new Event("change"));
    expect(fact(inspector, "p95 response time")).toBe("900 ms");
    const next = inspector.querySelector(
      '[aria-label="Worker performance profile"]'
    ) as HTMLSelectElement;
    next.value = "720p";
    next.dispatchEvent(new Event("change"));
    expect(fact(inspector, "p95 response time")).toBe("150 ms");
  });
  it("uses window totals independently of truncated samples and loads selected history", async () => {
    const { container, fetch } = mount(fixture());
    const inspector = await openWorker(container);
    expect(fact(inspector, "Observed segments")).toBe("10");
    expect(inspector.textContent).toContain("Location unknown");
    await vi.waitFor(() => expect(fetch).toHaveBeenCalledTimes(2));
    const url = fetch.mock.calls[1][0] as URL;
    expect(url.searchParams.get("resource")).toBe("0xabc");
    expect(url.searchParams.get("ip")).toBe("192.0.2.1");
    tab(inspector, "Observations");
    expect(inspector.textContent).toContain("Some history was left out");
  });
  it("keeps inventory browsable when history is unavailable", async () => {
    const data = fixture();
    data.history_status = "unavailable";
    data.history.cohorts = [];
    data.history.summaries = [];
    const { container } = mount(data);
    await vi.waitFor(() => expect(container.textContent).toContain("History unavailable"));
    const inspector = await openWorker(container);
    expect(inspector.textContent).toContain("192.0.2.1");
    expect(fact(inspector, "Observed segments")).toBe("No returned samples");
  });
  it("renders public metadata as text", async () => {
    const data = fixture();
    data.inventory.endpoints[0].resource.id = '<img src=x onerror="alert(1)">';
    const { container } = mount(data);
    const inspector = await openWorker(container);
    expect(inspector.textContent).toContain("<img src=x");
    expect(container.querySelector("img")).toBeNull();
  });
  it("retains discovery coverage when filtering workload profiles", async () => {
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
    const inspector = await openWorker(container);
    const profile = container.querySelector('[aria-label="Output profile"]') as HTMLSelectElement;
    profile.value = "720p";
    profile.dispatchEvent(new Event("change"));
    tab(inspector, "Observations");
    expect(inspector.textContent).toContain("Regional discovery latency");
    expect(inspector.querySelector(".timeline span.mixed")).not.toBeNull();
    expect(inspector.querySelector(".timeline span.mixed")?.getAttribute("title")).toContain(
      "9 passed · 1 failed · 10 checks"
    );
  });
});

// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { mountNetworkDashboard } from "../../packages/map-core/network-dashboard";
import type { Response } from "../../packages/map-core/network-data";

const harness = vi.hoisted(() => ({
  options: {} as Record<string, unknown>,
  events: new Map<string, (event: unknown) => void>(),
  scroll: vi.fn(() => vi.fn()),
  sources: new Map<string, { features: unknown[] }>(),
}));
vi.mock("../../packages/map-core/index", () => ({
  loadMapLibre: async () => ({
    Map: class {
      sources = new Map<string, { setData(data: { features: unknown[] }): void }>();
      constructor(options: Record<string, unknown>) {
        harness.options = options;
      }
      on(event: string, layer: unknown, handler?: (event: unknown) => void) {
        if (event === "load") queueMicrotask(layer as () => void);
        else harness.events.set(`${event}:${layer}`, handler!);
      }
      addControl() {}
      isStyleLoaded() {
        return true;
      }
      getSource(id: string) {
        return this.sources.get(id);
      }
      getLayer() {
        return undefined;
      }
      addSource(id: string, source: { data: { features: unknown[] } }) {
        harness.sources.set(id, source.data);
        this.sources.set(id, {
          setData(data: { features: unknown[] }) {
            harness.sources.set(id, data);
          },
        });
      }
      addLayer() {}
      setLayoutProperty() {}
      resize() {}
      easeTo() {}
      getCanvas() {
        return document.createElement("canvas");
      }
      remove() {}
    },
    NavigationControl: class {},
  }),
  readRuntimePublicConfig: () => ({}),
  basemapMapOptions: () => ({}),
  bindModifierScrollZoom: harness.scroll,
  addRequiredAttribution: () => {},
}));
let stop: (() => void) | undefined;
afterEach(() => {
  stop?.();
  vi.unstubAllGlobals();
  harness.events.clear();
  harness.sources.clear();
  harness.scroll.mockClear();
  document.body.replaceChildren();
  window.history.replaceState({}, "", "/");
});
function mount(workerCount = 1, observers: NonNullable<Response["inventory"]["observers"]> = []) {
  const now = new Date().toISOString();
  const data = {
    history_status: "available",
    inventory: {
      generated_at: now,
      resources: [{ id: "0xabc", uri: "https://orch.example", active: true }],
      observers,
      endpoints: [
        {
          id: "worker-a",
          resource: { id: "0xabc", uri: "https://orch.example", active: true },
          ip: "192.0.2.1",
          status: "up",
          location: { known: true, latitude: 52, longitude: 5, country: "NL" },
          last_check: now,
          last_success: now,
          cadence_seconds: 60,
        },
      ],
    },
    history: {
      start: new Date(Date.now() - 86400000).toISOString(),
      end: now,
      generated_at: now,
      interval_seconds: 300,
      cohorts: [],
      summaries: [],
      observations: [
        {
          timestamp: now,
          kind: "discovery",
          region: "eu-west",
          resource_id: "0xabc",
          ip: "192.0.2.1",
          failure_kind: "timeout",
          success: false,
          qualifying: true,
        },
      ],
      truncated: false,
      rolled_up: false,
    },
  };
  if (workerCount > 1)
    data.inventory.endpoints.push({
      ...data.inventory.endpoints[0],
      id: "worker-b",
      ip: "192.0.2.2",
    });
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => ({ ok: true, json: async () => structuredClone(data) }))
  );
  const container = document.createElement("div");
  document.body.append(container);
  stop = mountNetworkDashboard(container, { apiURL: "https://api.example/v1/network/livepeer" });
  return container;
}
describe("public network map", () => {
  it("keeps current any-region reachability distinct from a regional history filter", async () => {
    window.history.replaceState({}, "", "/network?view=livepeer&lp_region=us-east");
    const container = mount();
    await vi.waitFor(() => expect(harness.events.has("click:livepeer-endpoints")).toBe(true));
    expect(container.querySelector(".map-legend")?.textContent).toContain("Reachable (any region)");
    expect(
      (container.querySelector('[aria-label="History region"]') as HTMLSelectElement)?.value
    ).toBe("us-east");
  });
  it("keeps keyboard focus on the chosen tab when worker history arrives", async () => {
    const container = mount();
    await vi.waitFor(() => expect(harness.events.has("click:livepeer-endpoints")).toBe(true));
    const firstResponse = await vi.mocked(fetch).mock.results[0].value;
    const payload = await firstResponse.json();
    payload.history.observations[0].failure_kind = "retry_exhausted";
    let finish!: (value: globalThis.Response) => void;
    vi.mocked(fetch).mockImplementationOnce(
      () =>
        new Promise<globalThis.Response>((resolve) => {
          finish = resolve;
        })
    );
    harness.events.get("click:livepeer-endpoints")!({
      features: [{ properties: { id: "worker-a" } }],
    });
    const observations = container.querySelector<HTMLButtonElement>("#worker-tab-observations")!;
    observations.click();
    expect(document.activeElement?.id).toBe("worker-tab-observations");
    finish(new globalThis.Response(JSON.stringify(payload), { status: 200 }));
    await vi.waitFor(() =>
      expect(container.querySelector(".worker-inspector")?.textContent).toContain("retry exhausted")
    );
    expect(document.activeElement?.id).toBe("worker-tab-observations");
  });
  it("maps reporting gateways even when the separate topology API is unavailable", async () => {
    mount(1, [
      {
        id: "gateway-a",
        kind: "gateway",
        region: "eu",
        last_seen: new Date().toISOString(),
        location: { known: true, latitude: 52, longitude: 5, source: "node" },
      },
    ]);
    await vi.waitFor(() =>
      expect(harness.sources.get("gateway-clusters")?.features).toHaveLength(1)
    );
  });
  it("lets visitors inspect every worker when several markers overlap", async () => {
    const container = mount(2);
    await vi.waitFor(() => expect(harness.events.has("click:livepeer-endpoints")).toBe(true));
    harness.events.get("click:livepeer-endpoints")!({
      features: [{ properties: { id: "worker-a" } }, { properties: { id: "worker-b" } }],
    });
    expect(container.querySelector(".worker-directory")?.hasAttribute("hidden")).toBe(false);
    expect(container.querySelectorAll(".worker-row")).toHaveLength(2);
    (container.querySelector('[data-endpoint="worker-b"]') as HTMLButtonElement).click();
    expect(container.querySelector(".worker-inspector")?.textContent).toContain("192.0.2.2");
  });
  it("opens on the map with an overview instead of comparison controls or accounting tables", async () => {
    const container = mount();
    await vi.waitFor(() => expect(container.textContent).not.toContain("Refreshing network"));
    expect(container.querySelector(".map-viewport")).not.toBeNull();
    expect(container.querySelector('[aria-label="Network overview"]')).not.toBeNull();
    expect(
      Array.from(container.querySelectorAll("button")).map((b) => b.textContent)
    ).not.toContain("Compare");
    expect(container.querySelector('[aria-label="Orchestrator workload totals"]')).toBeNull();
    expect(container.querySelector(".worker-inspector")?.hasAttribute("hidden")).toBe(true);
  });
  it("uses the FrameWorks modifier-scroll policy and starts with wheel zoom disabled", async () => {
    mount();
    await vi.waitFor(() => expect(harness.scroll).toHaveBeenCalledTimes(1));
    expect(harness.options.scrollZoom).toBe(false);
    expect(harness.options.doubleClickZoom).toBe(false);
  });
  it("opens an individual worker from the map and closes it with Escape without losing the map", async () => {
    const container = mount();
    await vi.waitFor(() => expect(harness.events.has("click:livepeer-endpoints")).toBe(true));
    harness.events.get("click:livepeer-endpoints")!({
      features: [{ properties: { id: "worker-a" } }],
    });
    const inspector = container.querySelector(".worker-inspector")!;
    expect(inspector.hasAttribute("hidden")).toBe(false);
    expect(inspector.textContent).toContain("192.0.2.1");
    expect(inspector.textContent).not.toContain("Advertised hardware");
    expect(window.location.search).toContain("lp_endpoint=worker-a");
    container.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    expect(inspector.hasAttribute("hidden")).toBe(true);
    expect(window.location.search).not.toContain("lp_endpoint");
    expect(container.querySelector(".map-viewport")).not.toBeNull();
  });
  it("shows region-labelled errors in contextual observations rather than a raw public dump", async () => {
    const container = mount();
    await vi.waitFor(() => expect(harness.events.has("click:livepeer-endpoints")).toBe(true));
    harness.events.get("click:livepeer-endpoints")!({
      features: [{ properties: { id: "worker-a" } }],
    });
    const inspector = container.querySelector(".worker-inspector")!;
    const observations = Array.from(inspector.querySelectorAll("button")).find(
      (b) => b.textContent === "Observations"
    );
    expect(observations).toBeDefined();
    observations!.click();
    expect(inspector.textContent).toContain("eu-west");
    expect(inspector.textContent).toContain("timeout");
    expect(inspector.querySelector("pre")).toBeNull();
  });
});

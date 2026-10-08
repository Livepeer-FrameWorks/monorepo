// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { mountNetworkDashboard } from "../../packages/map-core/network-dashboard";

vi.mock("../../packages/map-core/index", () => ({
  loadMapLibre: () => Promise.reject(new Error("Map disabled in DOM test")),
  readRuntimePublicConfig: () => ({}),
  basemapMapOptions: () => ({}),
  addRequiredAttribution: () => {},
}));
let stop: (() => void) | undefined;
afterEach(() => {
  stop?.();
  vi.unstubAllGlobals();
  document.body.replaceChildren();
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

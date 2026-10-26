import { describe, expect, it } from "vitest";
import {
  associationPath,
  observingRegions,
  orchestratorGroups,
} from "../../packages/map-core/network-map-model";
import type { Response, Endpoint } from "../../packages/map-core/network-data";

function fixture(): Response {
  const now = new Date().toISOString();
  const endpoint: Endpoint = {
    id: "one",
    resource: { id: "0xAbC", uri: "https://one.example", active: true },
    ip: "192.0.2.1",
    status: "up",
    location: { known: false, latitude: 0, longitude: 0, source: "unknown" },
    last_check: now,
    last_success: now,
    cadence_seconds: 60,
  };
  return {
    inventory: {
      generated_at: now,
      resources: [endpoint.resource],
      endpoints: [endpoint],
      observers: [],
    },
    history: {
      generated_at: now,
      start: now,
      end: now,
      interval_seconds: 300,
      cohorts: [],
      summaries: [],
      observations: [],
      rolled_up: false,
      truncated: false,
    },
  };
}
describe("network exploration model", () => {
  it("retains published sites without inventing an unknown region's coordinates", () => {
    const data = fixture();
    data.inventory.observers!.push({
      id: "site",
      kind: "prober",
      region: "",
      last_seen: data.inventory.generated_at,
      location: { known: false, latitude: 0, longitude: 0, source: "unknown" },
    });
    const groups = observingRegions(data);
    expect(groups).toHaveLength(1);
    expect(groups[0].sites[0].id).toBe("site");
    expect(groups[0].anchor).toBeUndefined();
  });
  it("does not associate unobserved siblings with a region or duplicate repeated observations", () => {
    const data = fixture();
    data.inventory.endpoints.push({ ...data.inventory.endpoints[0], id: "two", ip: "192.0.2.2" });
    const sample = {
      timestamp: data.inventory.generated_at,
      kind: "discovery",
      region: "eu",
      resource_id: "0xabc",
      ip: "192.0.2.1",
      success: true,
      qualifying: true,
      cadence_seconds: 60,
    };
    data.history.observations.push(sample, sample);
    expect(observingRegions(data)[0].endpoints.map((e) => e.id)).toEqual(["one"]);
  });
  it("groups case-insensitive onchain identities while retaining distinct endpoints", () => {
    const data = fixture();
    data.inventory.endpoints.push({
      ...data.inventory.endpoints[0],
      id: "two",
      resource: { ...data.inventory.endpoints[0].resource, id: "0xabc" },
      ip: "192.0.2.2",
    });
    expect(orchestratorGroups(data)).toHaveLength(1);
    expect(orchestratorGroups(data)[0].endpoints).toHaveLength(2);
  });
  it("keeps date-line regional anchors and arcs near their actual endpoints", () => {
    const data = fixture();
    data.inventory.observers = [179, -179].map((longitude, i) => ({
      id: String(i),
      kind: "gateway",
      region: "pacific",
      last_seen: data.inventory.generated_at,
      location: { known: true, latitude: 20, longitude, source: "geo" },
    }));
    expect(Math.abs(observingRegions(data)[0].anchor![0])).toBeCloseTo(180);
    const arc = associationPath([179, 20], [-179, 25]);
    expect(arc[0]).toEqual([179, 20]);
    expect(arc.at(-1)).toEqual([181, 25]);
    expect(arc.every((point) => Math.abs(point[0] - 179) < 5)).toBe(true);
  });
});

import type { Cohort, Endpoint, Response } from "./network-data";

export type Site = NonNullable<Response["inventory"]["observers"]>[number];
export type RegionGroup = {
  id: string;
  sites: Site[];
  endpoints: Endpoint[];
  anchor?: [number, number];
};
export function observingRegions(data: Response): RegionGroup[] {
  const regions = new Map<string, RegionGroup>();
  const endpoints = new Map<string, Endpoint[]>();
  const observed = new Map<string, Set<string>>();
  const key = (resource: string, ip: string) => `${resource.toLowerCase()}|${ip}`;
  for (const endpoint of data.inventory.endpoints) {
    const id = key(endpoint.resource.id, endpoint.ip);
    endpoints.set(id, [...(endpoints.get(id) ?? []), endpoint]);
  }
  const get = (id: string) => {
    if (!regions.has(id)) regions.set(id, { id, sites: [], endpoints: [] });
    return regions.get(id)!;
  };
  for (const site of data.inventory.observers ?? []) get(site.region).sites.push(site);
  for (const row of [...data.history.summaries, ...data.history.observations]) {
    if (!row.region || ("samples" in row && !row.samples)) continue;
    const region = get(row.region);
    if (!observed.has(row.region)) observed.set(row.region, new Set());
    const ids = observed.get(row.region)!;
    for (const endpoint of endpoints.get(key(row.resource_id, row.ip)) ?? []) {
      if (!ids.has(endpoint.id)) region.endpoints.push(endpoint);
      ids.add(endpoint.id);
    }
  }
  for (const group of regions.values()) {
    const sites = group.sites.filter(
      (s) =>
        s.location.known &&
        Number.isFinite(s.location.latitude) &&
        Number.isFinite(s.location.longitude)
    );
    if (sites.length) {
      // Circular longitude averaging keeps a regional anchor near the date line.
      const x = sites.reduce((n, s) => n + Math.cos((s.location.longitude * Math.PI) / 180), 0);
      const y = sites.reduce((n, s) => n + Math.sin((s.location.longitude * Math.PI) / 180), 0);
      if (Math.hypot(x, y) > 1e-8)
        group.anchor = [
          (Math.atan2(y, x) * 180) / Math.PI,
          sites.reduce((n, s) => n + s.location.latitude, 0) / sites.length,
        ];
    }
  }
  return [...regions.values()].sort((a, b) => a.id.localeCompare(b.id));
}
export function orchestratorGroups(data: Response) {
  const groups = new Map<string, { id: string; uri: string; endpoints: Endpoint[] }>();
  for (const resource of data.inventory.resources) {
    const id = resource.id.toLowerCase();
    if (!groups.has(id)) groups.set(id, { id, uri: resource.uri, endpoints: [] });
  }
  for (const endpoint of data.inventory.endpoints) {
    const id = endpoint.resource.id.toLowerCase();
    if (!groups.has(id)) groups.set(id, { id, uri: endpoint.resource.uri, endpoints: [] });
    groups.get(id)!.endpoints.push(endpoint);
  }
  return [...groups.values()].sort(
    (a, b) => b.endpoints.length - a.endpoints.length || a.id.localeCompare(b.id)
  );
}
export function regionalEvidence(
  data: Response,
  region: string,
  endpoint: Endpoint,
  profile = ""
): Cohort[] {
  return data.history.summaries.filter(
    (row) =>
      row.region === region &&
      row.resource_id.toLowerCase() === endpoint.resource.id.toLowerCase() &&
      row.ip === endpoint.ip &&
      (!profile ||
        ["discovery", "state", "coverage"].includes(row.kind) ||
        row.profile_key === profile)
  );
}
export function associationPath(from: [number, number], to: [number, number]): number[][] {
  let longitude = to[0];
  while (longitude - from[0] > 180) longitude -= 360;
  while (longitude - from[0] < -180) longitude += 360;
  const dx = longitude - from[0],
    dy = to[1] - from[1];
  const control = [(from[0] + longitude) / 2 - dy * 0.12, (from[1] + to[1]) / 2 + dx * 0.12];
  return Array.from({ length: 33 }, (_, i) => {
    const t = i / 32,
      u = 1 - t;
    return [
      u * u * from[0] + 2 * u * t * control[0] + t * t * longitude,
      Math.max(-85, Math.min(85, u * u * from[1] + 2 * u * t * control[1] + t * t * to[1])),
    ];
  });
}
export function roleImage(kind: "gateway" | "prober") {
  const width = 32,
    data = new Uint8Array(width * width * 4);
  const color = kind === "gateway" ? [125, 207, 255] : [224, 175, 104];
  for (let y = 0; y < width; y++)
    for (let x = 0; x < width; x++) {
      const distance =
        kind === "gateway"
          ? Math.max(Math.abs(x - 15.5), Math.abs(y - 15.5))
          : Math.abs(x - 15.5) + Math.abs(y - 15.5);
      const edge = kind === "gateway" ? 11 : 15;
      if (distance > edge) continue;
      const offset = (y * width + x) * 4;
      data.set(distance > edge - 2 ? [22, 22, 30, 255] : [...color, 255], offset);
    }
  return { width, height: width, data };
}

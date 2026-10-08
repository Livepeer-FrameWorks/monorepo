import {
  addRequiredAttribution,
  basemapMapOptions,
  loadMapLibre,
  readRuntimePublicConfig,
} from "./index";
import type { Map as MapLibreMap, GeoJSONSource } from "maplibre-gl";

type Location = {
  known: boolean;
  latitude: number;
  longitude: number;
  source: string;
  country?: string;
};
type Endpoint = {
  id: string;
  resource: { id: string; uri: string; active: boolean };
  ip: string;
  status: string;
  location: Location;
  last_success: string;
  last_check: string;
  cadence_seconds: number;
  capabilities?: string[];
  hardware?: string;
  price?: {
    numerator: string;
    denominator: string;
    currency: string;
    unit: string;
    observed_at: string;
  };
};
type Cohort = {
  bucket: string;
  kind: string;
  resource_id: string;
  ip: string;
  region: string;
  profile_key: string;
  samples: number;
  qualified_samples: number;
  successes: number;
  failures: number;
  p50_ms: number;
  p95_ms: number;
  p99_ms: number;
  duration_samples: number;
  p95_duration_ratio: number;
  retries: number;
  fallbacks: number;
  deadline_misses: number;
};
type Observation = {
  timestamp: string;
  kind: string;
  region: string;
  resource_id: string;
  ip: string;
  failure_kind?: string;
  disposition?: string;
  success: boolean;
  qualifying: boolean;
  cadence_seconds: number;
  price?: Endpoint["price"];
};
type Response = {
  history_status?: string;
  inventory: {
    generated_at: string;
    endpoints: Endpoint[];
    resources: Array<{ id: string; uri: string; active: boolean }>;
  };
  history: {
    generated_at: string;
    start: string;
    end: string;
    interval_seconds: number;
    cohorts: Cohort[];
    summaries: Cohort[];
    truncated: boolean;
    rolled_up: boolean;
    details?: Array<Record<string, unknown>>;
    observations: Observation[];
  };
};
type Options = {
  apiURL: string;
  infrastructureURL?: string;
  graphqlURL?: string;
  headers?: () => Record<string, string>;
};
type InfrastructureNode = {
  nodeId: string;
  name: string;
  latitude: number | null;
  longitude: number | null;
  status: string;
};

const styles = `.fw-network{color:#c0caf5;background:#16161e;font:14px/1.5 Inter,system-ui,sans-serif;border:1px solid #414868}.fw-network *{box-sizing:border-box}.fw-network header,.fw-network .toolbar,.fw-network .summary,.fw-network .detail{padding:16px;border-bottom:1px solid #414868}.fw-network h2,.fw-network h3{margin:0 0 12px;font-weight:600}.fw-network button,.fw-network select,.fw-network input{color:inherit;background:#24283b;border:1px solid #414868;border-radius:0;padding:8px}.fw-network button{cursor:pointer}.fw-network button:focus-visible,.fw-network select:focus-visible{outline:2px solid #7aa2f7}.fw-network .toolbar{display:flex;flex-wrap:wrap;align-items:center;gap:12px}.fw-network .toolbar label{display:flex;align-items:center;gap:6px}.fw-network .summary{display:grid;grid-template-columns:repeat(4,minmax(0,1fr))}.fw-network .summary strong{display:block;font-size:24px}.fw-network .map{height:420px}.fw-network .body{display:grid;grid-template-columns:minmax(0,2fr) minmax(320px,1fr)}.fw-network .list{overflow:auto;max-height:620px;border-right:1px solid #414868}.fw-network table{width:100%;border-collapse:collapse}.fw-network th,.fw-network td{text-align:left;padding:10px;border-bottom:1px solid #414868;vertical-align:top}.fw-network th{color:#a9b1d6;font-size:12px;text-transform:uppercase}.fw-network .muted{color:#a9b1d6}.fw-network .good{color:#9ece6a}.fw-network .bad{color:#f7768e}.fw-network code{font-size:11px;overflow-wrap:anywhere}.fw-network svg.chart{width:100%;height:190px}.fw-network .chart-key{display:flex;gap:16px;color:#a9b1d6}.fw-network .timeline{display:flex;gap:2px;min-height:18px;overflow:hidden;margin:8px 0}.fw-network .timeline span{flex:1;min-width:2px;height:18px;background:#414868}.fw-network .timeline span.up{background:#9ece6a}.fw-network .timeline span.unavailable{background:#f7768e}.fw-network .detail{overflow:auto}.fw-network .detail pre{white-space:pre-wrap;overflow-wrap:anywhere;font-size:11px}.fw-network a{color:#7aa2f7}.fw-network [hidden]{display:none!important}@media(max-width:1000px){.fw-network .summary{grid-template-columns:repeat(2,minmax(0,1fr))}.fw-network .body{grid-template-columns:1fr}.fw-network .list{border-right:0;max-height:420px}}`;

function node<K extends keyof HTMLElementTagNameMap>(
  name: K,
  text?: string,
  className?: string
): HTMLElementTagNameMap[K] {
  const element = document.createElement(name);
  if (text !== undefined) element.textContent = text;
  if (className) element.className = className;
  return element;
}
function time(value?: string) {
  if (!value || value.startsWith("0001-")) return "Never observed";
  return new Date(value).toLocaleString();
}
function price(endpoint: Endpoint) {
  const q = endpoint.price;
  return q ? `${q.numerator} / ${q.denominator} ${q.currency}/${q.unit}` : "Not observed";
}
function select(label: string, entries: Array<[string, string]>) {
  const wrapper = node("label", label);
  const input = node("select");
  for (const [value, text] of entries) {
    const option = node("option", text);
    option.value = value;
    input.append(option);
  }
  wrapper.append(input);
  return { wrapper, input };
}

// Both frontends mount the same public renderer and consume the same server definitions.
export function mountNetworkDashboard(container: HTMLElement, options: Options): () => void {
  const root = node("section", undefined, "fw-network");
  const style = node("style", styles);
  root.append(style);
  const header = node("header");
  header.append(
    node("h2", "Livepeer network"),
    node(
      "p",
      "Public onchain inventory, discovery coverage and real transcoding workloads. Results are specific to their observer region and output profiles.",
      "muted"
    )
  );
  root.append(header);
  const toolbar = node("div", undefined, "toolbar");
  const windowChoice = select("Window", [
    ["24", "24 hours"],
    ["168", "7 days"],
    ["720", "30 days"],
    ["2160", "90 days"],
    ["8760", "1 year"],
    ["17520", "2 years"],
  ]);
  const regionChoice = select("Observer region", [["", "All regions"]]);
  const profileChoice = select("Profiles", [["", "All profiles"]]);
  const inactiveLabel = node("label", "Show inactive");
  const inactive = node("input");
  inactive.type = "checkbox";
  inactiveLabel.append(inactive);
  const refresh = node("button", "Refresh");
  refresh.type = "button";
  const maximize = node("button", "Maximize");
  maximize.type = "button";
  toolbar.append(
    windowChoice.wrapper,
    regionChoice.wrapper,
    profileChoice.wrapper,
    inactiveLabel,
    refresh,
    maximize
  );
  root.append(toolbar);
  const message = node("p", "Loading network observations…", "summary muted");
  message.setAttribute("role", "status");
  message.setAttribute("aria-live", "polite");
  const summary = node("div", undefined, "summary");
  root.append(message, summary);
  const diagnostics = node("details", undefined, "detail");
  diagnostics.hidden = true;
  root.append(diagnostics);
  const mapContainer = node("div", undefined, "map");
  mapContainer.setAttribute("aria-label", "Map of observed Livepeer endpoints");
  root.append(mapContainer);
  const body = node("div", undefined, "body");
  const list = node("div", undefined, "list");
  const detail = node("aside", undefined, "detail");
  detail.append(
    node(
      "p",
      "Select an endpoint to inspect its regional performance, price and discovery coverage."
    )
  );
  body.append(list, detail);
  root.append(body);
  const foot = node("footer", undefined, "detail");
  foot.append(
    node(
      "p",
      "An IP identifies an observed endpoint. Shared proxies do not establish physical worker identity. Unknown geography stays in the list; gaps in observation coverage are unknown availability.",
      "muted"
    )
  );
  if (options.infrastructureURL) {
    const link = node("a", "Explore FrameWorks infrastructure");
    link.href = options.infrastructureURL;
    foot.append(link);
  }
  root.append(foot);
  container.replaceChildren(root);
  let data: Response | undefined;
  let selected = "";
  let map: MapLibreMap | undefined;
  let disposed = false;
  let controller: AbortController | undefined;
  let fetchVersion = 0;
  let infrastructure: InfrastructureNode[] = [];
  const infrastructureController = new AbortController();
  const endpointHistory = new Map<string, Response["history"]>();
  const pendingHistory = new Set<string>();
  const filtered = (values = data?.history.cohorts ?? []): Cohort[] =>
    values.filter(
      (c) =>
        (!regionChoice.input.value || c.region === regionChoice.input.value) &&
        (!profileChoice.input.value ||
          c.kind === "discovery" ||
          c.kind === "state" ||
          c.kind === "coverage" ||
          c.profile_key === profileChoice.input.value)
    );
  const endpointCohorts = (p: Endpoint, summaries = false) =>
    filtered(
      summaries
        ? ((endpointHistory.get(p.id) ?? data?.history)?.summaries ?? [])
        : ((endpointHistory.get(p.id) ?? data?.history)?.cohorts ?? [])
    ).filter((c) => c.resource_id.toLowerCase() === p.resource.id.toLowerCase() && c.ip === p.ip);
  function renderDetail(p: Endpoint) {
    detail.replaceChildren(
      node("h3", p.ip || "Unknown endpoint"),
      node("code", p.resource.id),
      node("p", p.resource.uri),
      node("p", `Availability: ${p.status}`),
      node("p", `Last success: ${time(p.last_success)}`),
      node("p", `Last check: ${time(p.last_check)} · scheduled cadence ${p.cadence_seconds}s`),
      node(
        "p",
        `Geography: ${p.location.known ? `${p.location.country ?? ""} · ${p.location.source}` : "unknown"}`
      ),
      node("p", `Current price: ${price(p)}`)
    );
    const cohorts = endpointCohorts(p);
    const paths = new Set(cohorts.map((c) => `${c.region} | ${c.profile_key || "discovery"}`));
    detail.append(
      node(
        "p",
        `${paths.size} observed region/profile cohorts. No production samples means workload performance is unknown.`,
        "muted"
      )
    );
    const production = cohorts.filter((c) => c.kind === "attempt" || c.kind === "segment");
    const totals = endpointCohorts(p, true).filter(
      (c) => c.kind === "attempt" || c.kind === "segment" || c.kind === "discovery"
    );
    const history = endpointHistory.get(p.id) ?? data!.history;
    const table = node("table");
    const heading = node("tr");
    for (const text of [
      "Region / profiles",
      "Kind",
      "Qualifying / recorded",
      "Successful checks / qualifying",
      "p50 / p95 / p99 ms",
      "p95 / duration",
    ]) {
      heading.append(node("th", text));
    }
    const thead = node("thead");
    thead.append(heading);
    table.append(thead);
    const tbody = node("tbody");
    for (const c of totals) {
      const row = node("tr");
      for (const value of [
        `${c.region} / ${c.profile_key}`,
        c.kind,
        `${c.qualified_samples} / ${c.samples}`,
        `${c.successes} / ${c.qualified_samples}`,
        `${Math.round(c.p50_ms)} / ${Math.round(c.p95_ms)} / ${Math.round(c.p99_ms)}`,
        c.duration_samples ? c.p95_duration_ratio.toFixed(2) : "Unknown",
      ]) {
        row.append(node("td", value));
      }
      tbody.append(row);
    }
    table.append(tbody);
    detail.append(
      table,
      node(
        "p",
        "Percentiles cover the selected window for each region/profile cohort. Discovery success is sampled reachability from that region. Response headers/body timings include transport and waiting; they are not GPU compute time.",
        "muted"
      )
    );
    const chart = node("div");
    drawChart(chart, production);
    detail.append(chart);
    detail.append(node("h3", "Regional discovery latency"));
    const discoveryChart = node("div");
    drawChart(
      discoveryChart,
      cohorts.filter((c) => c.kind === "discovery")
    );
    detail.append(discoveryChart);
    detail.append(node("h3", "Discovery timeline"));
    const timeline = node("div", undefined, "timeline");
    const observations = (history.observations ?? []).filter(
      (o) =>
        o.kind === "discovery" &&
        o.resource_id.toLowerCase() === p.resource.id.toLowerCase() &&
        o.ip === p.ip &&
        (!regionChoice.input.value || o.region === regionChoice.input.value)
    );
    const start = Date.parse(data!.history.start),
      end = Date.parse(data!.history.end);
    for (let i = 0; i < 96; i++) {
      const a = start + ((end - start) * i) / 96,
        b = start + ((end - start) * (i + 1)) / 96;
      const points = observations.filter(
        (o) => Date.parse(o.timestamp) >= a && Date.parse(o.timestamp) < b
      );
      const cell = node("span");
      const buckets = history.rolled_up
        ? cohorts.filter(
            (c) => c.kind === "discovery" && Date.parse(c.bucket) >= a && Date.parse(c.bucket) < b
          )
        : [];
      const sampleCount = history.rolled_up
        ? buckets.reduce((sum, c) => sum + c.samples, 0)
        : points.length;
      const successes = history.rolled_up
        ? buckets.reduce((sum, c) => sum + c.successes, 0)
        : points.filter((o) => o.success).length;
      const qualified = history.rolled_up
        ? buckets.reduce((sum, c) => sum + c.qualified_samples, 0)
        : points.filter((o) => o.qualifying).length;
      cell.className =
        successes > 0
          ? "up"
          : sampleCount > 0 && qualified === sampleCount
            ? "unavailable"
            : "unknown";
      cell.title = `${new Date(a).toLocaleString()}: ${sampleCount} checks, ${cell.className} from sampled locations`;
      timeline.append(cell);
    }
    detail.append(
      timeline,
      node(
        "p",
        "Green: successful discovery observed. Red: qualifying failed checks in this bucket. Grey: unknown or missing coverage. Raw checks and prices are retained for 30 days; this sampled timeline is not continuous uptime.",
        "muted"
      )
    );
    const prices = (history.observations ?? []).filter(
      (o) =>
        o.kind === "state" &&
        o.price &&
        o.resource_id.toLowerCase() === p.resource.id.toLowerCase() &&
        o.ip === p.ip
    );
    detail.append(node("h3", "Observed prices"));
    for (const o of prices.slice(0, 20)) {
      const q = o.price!;
      detail.append(
        node(
          "p",
          `${time(o.timestamp)} · ${q.numerator} / ${q.denominator} ${q.currency}/${q.unit}`
        )
      );
    }
    const errors = new Map<string, number>();
    for (const o of history.observations ?? []) {
      if (
        o.resource_id.toLowerCase() !== p.resource.id.toLowerCase() ||
        (o.ip !== p.ip && !(o.kind === "decision" && !o.ip)) ||
        o.success ||
        !o.failure_kind
      )
        continue;
      const key = `${o.region}: ${o.failure_kind} (${o.disposition || o.kind})`;
      errors.set(key, (errors.get(key) ?? 0) + 1);
    }
    detail.append(node("h3", "Observed errors"));
    for (const [kind, count] of errors)
      detail.append(node("p", `${kind}: ${count} returned samples`));
    if (history.truncated)
      detail.append(
        node(
          "p",
          "Chart, timeline, prices and error samples are truncated. Narrow the window or observer region for more detail.",
          "muted"
        )
      );
    if (p.capabilities?.length) {
      detail.append(node("h3", "Capabilities"), node("pre", p.capabilities.join("\n")));
    }
    if (p.hardware) {
      detail.append(node("h3", "Advertised hardware"), node("pre", p.hardware));
    }
    if (history.details?.length) {
      detail.append(node("h3", "Operator diagnostics"));
      for (let i = 0; i < history.observations.length; i++) {
        const extra = history.details[i];
        if (extra && Object.keys(extra).length)
          detail.append(
            node(
              "pre",
              JSON.stringify({ event: history.observations[i], correlation: extra }, null, 2)
            )
          );
      }
    }
  }
  function drawChart(target: HTMLElement, cohorts: Cohort[]) {
    const ns = "http://www.w3.org/2000/svg";
    const svg = document.createElementNS(ns, "svg");
    svg.setAttribute("viewBox", "0 0 600 190");
    svg.setAttribute("class", "chart");
    svg.setAttribute("role", "img");
    svg.setAttribute(
      "aria-label",
      "p95 latency by region, profile and outcome type; gaps indicate missing samples"
    );
    const groups = new Map<string, Cohort[]>();
    for (const c of cohorts) {
      const key = `${c.kind} / ${c.region} / ${c.profile_key}`;
      groups.set(key, [...(groups.get(key) ?? []), c]);
    }
    const max = Math.max(1, ...cohorts.map((c) => c.p95_ms));
    const start = Date.parse(data!.history.start),
      end = Date.parse(data!.history.end);
    const colors = ["#7aa2f7", "#9ece6a", "#e0af68", "#bb9af7", "#7dcfff"];
    let index = 0;
    const axis = document.createElementNS(ns, "text");
    axis.setAttribute("x", "0");
    axis.setAttribute("y", "14");
    axis.setAttribute("fill", "#a9b1d6");
    axis.textContent = `p95 latency · 0–${Math.round(max)} ms`;
    svg.append(axis);
    for (const [name, series] of groups) {
      const color = colors[index++ % colors.length];
      const ordered = series.sort((a, b) => Date.parse(a.bucket) - Date.parse(b.bucket));
      let previous: Cohort | undefined;
      for (const c of ordered) {
        if (
          previous &&
          Date.parse(c.bucket) - Date.parse(previous.bucket) <=
            data!.history.interval_seconds * 1500
        ) {
          const line = document.createElementNS(ns, "line");
          line.setAttribute(
            "x1",
            String(10 + (580 * (Date.parse(previous.bucket) - start)) / (end - start))
          );
          line.setAttribute("y1", String(180 - (150 * previous.p95_ms) / max));
          line.setAttribute(
            "x2",
            String(10 + (580 * (Date.parse(c.bucket) - start)) / (end - start))
          );
          line.setAttribute("y2", String(180 - (150 * c.p95_ms) / max));
          line.setAttribute("stroke", color);
          line.setAttribute("stroke-width", "2");
          svg.append(line);
        }
        const point = document.createElementNS(ns, "circle");
        point.setAttribute(
          "cx",
          String(10 + (580 * (Date.parse(c.bucket) - start)) / (end - start))
        );
        point.setAttribute("cy", String(180 - (150 * c.p95_ms) / max));
        point.setAttribute("r", "2");
        point.setAttribute("fill", color);
        const title = document.createElementNS(ns, "title");
        title.textContent = `${name}: ${Math.round(c.p95_ms)} ms, ${c.samples} samples at ${time(c.bucket)}`;
        point.append(title);
        svg.append(point);
        previous = c;
      }
      const legend = node("p", name, "muted");
      legend.style.color = color;
      target.append(legend);
    }
    target.prepend(svg);
    if (!cohorts.length)
      target.append(node("p", "No samples for this chart in the selected window.", "muted"));
  }
  function pick(id: string) {
    selected = id;
    const endpoint = data?.inventory.endpoints.find((p) => p.id === id);
    if (endpoint) renderDetail(endpoint);
    if (endpoint && !endpointHistory.has(id) && !pendingHistory.has(id)) {
      const version = fetchVersion;
      pendingHistory.add(id);
      const api = requestURL();
      api.searchParams.set("resource", endpoint.resource.id);
      api.searchParams.set("ip", endpoint.ip);
      if (regionChoice.input.value) api.searchParams.set("region", regionChoice.input.value);
      void fetch(api, { signal: controller?.signal, headers: options.headers?.() })
        .then(async (response) => {
          if (!response.ok) throw new Error("Endpoint history unavailable");
          const next = (await response.json()) as Response;
          if (disposed || version !== fetchVersion) return;
          if (next.history_status === "unavailable")
            throw new Error("Endpoint history unavailable");
          endpointHistory.set(id, next.history);
          if (selected === id) renderDetail(endpoint);
        })
        .catch(() => {
          if (!disposed && version === fetchVersion && selected === id)
            detail.append(
              node(
                "p",
                "Endpoint history unavailable; showing samples from the network response.",
                "muted"
              )
            );
        })
        .finally(() => {
          if (version === fetchVersion) pendingHistory.delete(id);
        });
    }
  }
  function render() {
    if (!data) return;
    const endpoints = data.inventory.endpoints;
    diagnostics.hidden = !data.history.details?.length;
    diagnostics.replaceChildren(node("summary", "Latest gateway diagnostics"));
    for (let i = 0; i < Math.min(100, data.history.observations.length); i++) {
      const extra = data.history.details?.[i];
      if (extra && Object.keys(extra).length)
        diagnostics.append(
          node(
            "pre",
            JSON.stringify({ event: data.history.observations[i], correlation: extra }, null, 2)
          )
        );
    }
    summary.replaceChildren();
    const counts = [
      ["Catalog identities", new Set(data.inventory.resources.map((r) => r.id)).size],
      ["Observed endpoints", endpoints.length],
      ["Available now", endpoints.filter((p) => p.status === "up").length],
      ["Unknown geography", endpoints.filter((p) => !p.location.known).length],
    ];
    for (const [name, count] of counts) {
      const slab = node("div", String(name));
      slab.append(node("strong", String(count)));
      summary.append(slab);
    }
    const table = node("table");
    const thead = node("thead");
    const heading = node("tr");
    for (const label of [
      "Endpoint / identity",
      "Availability",
      "Observed segments",
      "Final failures / budget misses",
      "Regional p95 / media duration",
      "Price",
    ]) {
      heading.append(node("th", label));
    }
    thead.append(heading);
    table.append(thead);
    const tbody = node("tbody");
    for (const p of endpoints) {
      const row = node("tr");
      const identity = node("td");
      const button = node("button", p.ip || "Unknown IP");
      button.type = "button";
      button.onclick = () => pick(p.id);
      identity.append(
        button,
        node("br"),
        node("code", p.resource.id),
        node(
          "p",
          p.location.known ? (p.location.country ?? p.location.source) : "Geography unknown",
          "muted"
        )
      );
      row.append(
        identity,
        node(
          "td",
          p.status,
          p.status === "up" ? "good" : p.status === "unavailable" ? "bad" : "muted"
        )
      );
      const segments = endpointCohorts(p, true).filter((c) => c.kind === "segment");
      const samples = segments.reduce((n, c) => n + c.samples, 0);
      row.append(
        node("td", samples ? String(samples) : "No returned samples"),
        node(
          "td",
          samples
            ? `${segments.reduce((n, c) => n + c.failures, 0)} / ${segments.reduce((n, c) => n + c.deadline_misses, 0)}`
            : "Unknown"
        )
      );
      const latency = node("td");
      const attempts = endpointCohorts(p, true).filter(
        (c) => c.kind === "attempt" && c.qualified_samples > 0
      );
      for (const c of attempts)
        latency.append(
          node(
            "p",
            `${c.region} / ${c.profile_key || "unspecified"}: ${Math.round(c.p95_ms)} ms${c.duration_samples ? ` / ${c.p95_duration_ratio.toFixed(2)}×` : ""}`
          )
        );
      if (!attempts.length) latency.textContent = "Unknown";
      row.append(latency, node("td", price(p)));
      tbody.append(row);
    }
    table.append(tbody);
    list.replaceChildren(table);
    const discovered = new Set(endpoints.map((p) => p.resource.uri));
    const undiscovered = data.inventory.resources.filter((r) => r.active && !discovered.has(r.uri));
    if (undiscovered.length) {
      list.append(node("h3", "Catalog entries without a visible endpoint", "detail"));
      for (const r of undiscovered)
        list.append(node("p", `${r.id} · ${r.uri} · availability unknown`, "detail muted"));
    }
    if (!endpoints.length) list.append(node("p", "No endpoints discovered yet.", "detail muted"));
    if (selected) pick(selected);
    updateMap();
    message.textContent = `Updated ${time(data.inventory.generated_at)}. ${data.history_status === "unavailable" ? "History is temporarily unavailable; current inventory remains visible." : `History ${time(data.history.start)} – ${time(data.history.end)}. ${data.history.rolled_up ? "Hourly rollups refresh every five minutes. Discovery history expires after one year; workload history after two years." : "Comparison totals cover this window."} ${data.history.truncated ? "Returned cohorts and observation samples are truncated; select an endpoint to load its history." : ""}`}`;
  }
  function updateMap() {
    if (!map?.isStyleLoaded() || !data) return;
    const infrastructureData = {
      type: "FeatureCollection" as const,
      features: infrastructure
        .filter(
          (p) =>
            p.latitude !== null &&
            p.longitude !== null &&
            Number.isFinite(p.latitude) &&
            Number.isFinite(p.longitude)
        )
        .map((p) => ({
          type: "Feature" as const,
          geometry: { type: "Point" as const, coordinates: [p.longitude!, p.latitude!] },
          properties: { name: p.name, status: p.status },
        })),
    };
    const infrastructureSource = map.getSource("frameworks-nodes") as GeoJSONSource | undefined;
    if (infrastructureSource) infrastructureSource.setData(infrastructureData);
    else {
      map.addSource("frameworks-nodes", { type: "geojson", data: infrastructureData });
      map.addLayer({
        id: "frameworks-nodes",
        type: "circle",
        source: "frameworks-nodes",
        paint: {
          "circle-radius": 10,
          "circle-color": "#7aa2f7",
          "circle-opacity": 0.6,
          "circle-stroke-width": 2,
          "circle-stroke-color": "#7aa2f7",
        },
      });
    }
    const features = data.inventory.endpoints
      .filter(
        (p) =>
          p.location.known &&
          Number.isFinite(p.location.latitude) &&
          Number.isFinite(p.location.longitude)
      )
      .map((p) => ({
        type: "Feature" as const,
        geometry: {
          type: "Point" as const,
          coordinates: [p.location.longitude, p.location.latitude],
        },
        properties: {
          id: p.id,
          status: p.status,
          stale: !p.last_success || Date.now() - Date.parse(p.last_success) > 7 * 86400000,
        },
      }));
    const source = map.getSource("livepeer-endpoints") as GeoJSONSource | undefined;
    const geo = { type: "FeatureCollection" as const, features };
    if (source) source.setData(geo);
    else {
      map.addSource("livepeer-endpoints", { type: "geojson", data: geo });
      map.addLayer({
        id: "livepeer-endpoints",
        type: "circle",
        source: "livepeer-endpoints",
        paint: {
          "circle-radius": ["case", ["get", "stale"], 3, 7],
          "circle-color": [
            "match",
            ["get", "status"],
            "up",
            "#9ece6a",
            "unavailable",
            "#f7768e",
            "#737aa2",
          ],
          "circle-opacity": 0.85,
          "circle-stroke-color": "#16161e",
          "circle-stroke-width": 1,
        },
      });
      map.on("click", "livepeer-endpoints", (event) => {
        const id = event.features?.[0]?.properties?.id;
        if (id) pick(String(id));
      });
      map.on("mouseenter", "livepeer-endpoints", () => {
        map!.getCanvas().style.cursor = "pointer";
      });
      map.on("mouseleave", "livepeer-endpoints", () => {
        map!.getCanvas().style.cursor = "";
      });
    }
  }
  function fillOptions(input: HTMLSelectElement, values: string[], emptyLabel: string) {
    const current = input.value;
    input.replaceChildren();
    for (const value of ["", ...values]) {
      const option = node("option", value || emptyLabel);
      option.value = value;
      input.append(option);
    }
    if (values.includes(current)) input.value = current;
  }
  async function fetchData() {
    const version = ++fetchVersion;
    controller?.abort();
    controller = new AbortController();
    refresh.disabled = true;
    endpointHistory.clear();
    pendingHistory.clear();
    message.textContent = "Refreshing network observations…";
    try {
      const api = requestURL();
      const response = await fetch(api, {
        signal: controller.signal,
        headers: options.headers?.(),
      });
      if (!response.ok)
        throw new Error(`Network observations unavailable (HTTP ${response.status})`);
      const next = (await response.json()) as Response;
      if (disposed || version !== fetchVersion) return;
      if (!Array.isArray(next.inventory?.endpoints) || !Array.isArray(next.history?.cohorts))
        throw new Error("Invalid network response");
      data = next;
      fillOptions(
        regionChoice.input,
        [...new Set(data.history.summaries.map((c) => c.region))].sort(),
        "All regions"
      );
      fillOptions(
        profileChoice.input,
        [...new Set(data.history.summaries.map((c) => c.profile_key).filter(Boolean))].sort(),
        "All profiles"
      );
      render();
    } catch (error) {
      if (disposed || version !== fetchVersion || controller.signal.aborted) return;
      message.textContent =
        error instanceof Error ? error.message : "Network observations unavailable";
    } finally {
      if (!disposed && version === fetchVersion) refresh.disabled = false;
    }
  }
  function requestURL() {
    const api = new URL(options.apiURL, window.location.origin);
    const end = Date.now();
    api.searchParams.set("end", new Date(end).toISOString());
    api.searchParams.set(
      "start",
      new Date(end - Number(windowChoice.input.value) * 3600000).toISOString()
    );
    api.searchParams.set("include_inactive", String(inactive.checked));
    api.searchParams.set("interval", windowChoice.input.value === "24" ? "5m" : "1h");
    return api;
  }
  refresh.onclick = () => void fetchData();
  windowChoice.input.onchange = () => void fetchData();
  inactive.onchange = () => void fetchData();
  regionChoice.input.onchange = () => {
    endpointHistory.clear();
    render();
  };
  profileChoice.input.onchange = render;
  maximize.onclick = async () => {
    if (document.fullscreenElement === root) await document.exitFullscreen();
    else await root.requestFullscreen();
    map?.resize();
  };
  void loadMapLibre()
    .then((module) => {
      if (disposed) return;
      const basemap = readRuntimePublicConfig().basemap;
      map = new module.Map({
        container: mapContainer,
        ...basemapMapOptions(basemap),
        center: [10, 25],
        zoom: 1.4,
      });
      map.addControl(new module.NavigationControl({ showCompass: false }));
      addRequiredAttribution(map, basemap);
      map.on("load", updateMap);
    })
    .catch((error) => {
      if (!disposed) {
        mapContainer.replaceChildren(
          node(
            "p",
            `Map unavailable: ${error instanceof Error ? error.message : "unknown error"}. Endpoints remain in the list.`,
            "detail muted"
          )
        );
      }
    });
  void fetchData();
  async function fetchInfrastructure() {
    if (!options.graphqlURL) return;
    try {
      const response = await fetch(options.graphqlURL, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        signal: infrastructureController.signal,
        body: JSON.stringify({
          query:
            "query NetworkMap { networkStatus { nodes { nodeId name latitude longitude status } } }",
          operationName: "NetworkMap",
        }),
      });
      if (!response.ok) return;
      const next = await response.json();
      if (!disposed && Array.isArray(next.data?.networkStatus?.nodes)) {
        infrastructure = next.data.networkStatus.nodes;
        updateMap();
      }
    } catch {
      /* Public infrastructure and Livepeer have independent availability. */
    }
  }
  foot.append(
    node(
      "p",
      "Map: blue circles show published FrameWorks nodes; green/red/grey circles show Livepeer endpoints.",
      "muted"
    )
  );
  void fetchInfrastructure();
  const timer = setInterval(() => void fetchData(), 60000);
  const infrastructureTimer = setInterval(() => void fetchInfrastructure(), 60000);
  return () => {
    disposed = true;
    clearInterval(timer);
    clearInterval(infrastructureTimer);
    infrastructureController.abort();
    controller?.abort();
    map?.remove();
    container.replaceChildren();
  };
}

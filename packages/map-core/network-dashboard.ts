import {
  addRequiredAttribution,
  basemapMapOptions,
  bindModifierScrollZoom,
  loadMapLibre,
  readRuntimePublicConfig,
} from "./index";
import { bindExplorerViewport } from "./network-explorer";
import { sumWork, workMinutes, workAmount, isPartialWork, type WorkSummary } from "./network-work";
import type {
  Map as MapLibreMap,
  GeoJSONSource,
  MapLayerMouseEvent,
  CircleLayerSpecification,
} from "maplibre-gl";
import type {
  Endpoint,
  Cohort,
  Response,
  Options,
  GatewayCluster,
  InfrastructureNode,
} from "./network-data";
import "./network-dashboard.css";

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
  if (!value || value.startsWith("0001-") || !Number.isFinite(Date.parse(value)))
    return "Not observed";
  return new Date(value).toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}
function short(value: string) {
  return value.length > 22 ? `${value.slice(0, 10)}…${value.slice(-6)}` : value;
}
function button(label: string, action: () => void, className = "") {
  const result = node("button", label, className);
  result.type = "button";
  result.onclick = () => action();
  return result;
}
const icons = {
  refresh:
    '<path d="M20 7v5h-5M4 17v-5h5"/><path d="M6 7a7 7 0 0 1 12-1l2 3M4 15l2 3a7 7 0 0 0 12-1"/>',
  reset: '<path d="m3 10 9-7 9 7v10H3Z"/><path d="M9 20v-7h6v7"/>',
  expand: '<path d="M8 3H3v5m13-5h5v5M3 16v5h5m13-5v5h-5"/>',
  close: '<path d="m6 6 12 12M6 18 18 6"/>',
};
function iconButton(label: string, icon: keyof typeof icons, action: () => void) {
  const result = button("", action, "map-icon-button");
  result.setAttribute("aria-label", label);
  result.title = label;
  // These paths are static UI assets; response data always goes through textContent.
  result.innerHTML = `<svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${icons[icon]}</svg>`;
  return result;
}
function select(label: string, entries: Array<[string, string]>) {
  const wrapper = node("label", undefined, "map-filter");
  const input = node("select");
  input.setAttribute("aria-label", label);
  for (const [value, title] of entries) {
    const option = node("option", title);
    option.value = value;
    input.append(option);
  }
  wrapper.append(node("span", label, "filter-label"), input);
  return { wrapper, input };
}
function disclosure(label: string, className = "") {
  const element = node("details", undefined, className);
  element.append(node("summary", label));
  return element;
}
function facts(entries: Array<[string, string]>, className = "map-facts") {
  const list = node("dl", undefined, className);
  for (const [label, value] of entries) {
    const row = node("div");
    row.append(node("dt", label), node("dd", value));
    list.append(row);
  }
  return list;
}
function statusLabel(status: string) {
  return status === "up"
    ? "Reachable"
    : status === "unavailable"
      ? "Unreachable"
      : "Coverage unknown";
}
function formatPrice(p: Endpoint) {
  const q = p.price;
  if (!q) return "No price observed";
  if (
    !/^\d{1,20}$/.test(q.numerator) ||
    !/^\d{1,20}$/.test(q.denominator) ||
    BigInt(q.denominator) === 0n
  )
    return "Price unavailable";
  const n = BigInt(q.numerator),
    d = BigInt(q.denominator);
  if (q.currency.toLowerCase() === "wei" && q.unit === "pixel") {
    const scaled = (n * 10000n) / (d * 1000n);
    if (n > 0n && scaled === 0n) return "<0.0001 gwei / MP";
    return `${scaled / 10000n}.${
      String(scaled % 10000n)
        .padStart(4, "0")
        .replace(/0+$/, "") || "0"
    } gwei / MP`;
  }
  return `${q.numerator} / ${q.denominator} ${q.currency}/${q.unit}`;
}
function freshPrice(p: Endpoint) {
  if (!p.price) return false;
  const age = Date.now() - Date.parse(p.price.observed_at);
  return (
    Number.isFinite(age) && age >= -60000 && age <= Math.max(180, 2 * p.cadence_seconds) * 1000
  );
}

// React and Svelte mount the same public view so map behavior and evidence stay consistent.
export function mountNetworkDashboard(container: HTMLElement, options: Options): () => void {
  const initial = new URLSearchParams(window.location.search);
  const root = node("section", undefined, "fw-network");
  root.setAttribute("aria-label", "Livepeer network observations");
  const header = node("header", undefined, "network-map-header");
  const title = node("div");
  title.append(node("span", "PUBLIC NETWORK", "map-eyebrow"), node("h2", "Livepeer"));
  const message = node("span", "Loading observations…", "map-status");
  message.setAttribute("role", "status");
  message.setAttribute("aria-live", "polite");
  const refresh = iconButton("Refresh observations", "refresh", () => void fetchData());
  header.append(title, message, refresh);
  const toolbar = node("div", undefined, "map-toolbar");
  const windowChoice = select("Period", [
    ["24", "24 hours"],
    ["168", "7 days"],
    ["720", "30 days"],
    ["2160", "90 days"],
    ["8760", "1 year"],
    ["17520", "2 years"],
  ]);
  const regionChoice = select("History region", [["", "All regions"]]);
  const metricChoice = select("Map shows", [
    ["availability", "Availability"],
    ["errors", "Observed errors"],
    ["activity", "Transcoding activity"],
  ]);
  const profileChoice = select("Output profile", [["", "All profiles"]]);
  const settings = disclosure("Layers & filters", "map-settings");
  const settingsBody = node("div", undefined, "map-settings-body");
  const layerControls = new Map<string, HTMLInputElement>();
  for (const [id, label, enabled] of [
    ["livepeer-endpoints", "Workers", true],
    ["gateway-clusters", "Gateways", true],
    ["prober-sites", "Probers", true],
    ["frameworks-nodes", "FrameWorks nodes", false],
  ] as const) {
    const field = node("label", undefined, "map-check");
    const input = node("input");
    input.type = "checkbox";
    input.checked = initial.has(`lp_layer_${id}`)
      ? initial.get(`lp_layer_${id}`) === "true"
      : enabled;
    field.append(input, node("span", label));
    settingsBody.append(field);
    layerControls.set(id, input);
    input.onchange = () => {
      updateMap();
      persist();
    };
  }
  const inactive = node("input");
  inactive.type = "checkbox";
  inactive.checked = initial.get("lp_inactive") === "true";
  const inactiveLabel = node("label", undefined, "map-check");
  inactiveLabel.append(inactive, node("span", "Include inactive workers"));
  settingsBody.append(profileChoice.wrapper, inactiveLabel);
  settings.append(settingsBody);
  toolbar.append(windowChoice.wrapper, regionChoice.wrapper, metricChoice.wrapper, settings);
  const workspace = node("div", undefined, "map-workspace");
  const viewport = node("div", undefined, "map-viewport");
  const mapContainer = node("div", undefined, "map");
  mapContainer.dataset.mode = "map";
  mapContainer.setAttribute("aria-label", "Map of Livepeer workers and measurement locations");
  const mapControls = node("div", undefined, "map-controls");
  const reset = iconButton("Reset map view", "reset", () =>
    map?.easeTo({ center: [10, 25], zoom: 2, duration: 400 })
  );
  const maximize = iconButton("Open fullscreen map", "expand", () =>
    fullscreen(!root.classList.contains("is-fullscreen"))
  );
  mapControls.append(reset, maximize);
  const legend = node("div", undefined, "map-legend");
  legend.setAttribute("aria-label", "Map legend");
  const zoomHint = node("span", "Hold Alt or Ctrl + scroll to zoom", "map-zoom-hint");
  const tooltip = node("div", undefined, "map-tooltip");
  tooltip.hidden = true;
  tooltip.setAttribute("role", "tooltip");
  const mapNotice = node("div", undefined, "map-notice");
  mapNotice.hidden = true;
  mapNotice.setAttribute("role", "status");
  viewport.append(mapContainer, mapControls, legend, zoomHint, tooltip, mapNotice);
  const rail = node("div", undefined, "map-rail");
  const overview = node("aside", undefined, "network-overview");
  overview.setAttribute("aria-label", "Network overview");
  const overviewBody = node("div");
  const browse = button("Browse workers", () => showDirectory(), "rail-action");
  overview.append(overviewBody, browse);
  const directory = node("aside", undefined, "worker-directory");
  directory.hidden = true;
  directory.setAttribute("aria-label", "Worker directory");
  const directoryHeader = node("div", undefined, "inspector-header");
  directoryHeader.append(
    node("h3", "Workers"),
    iconButton("Back to network overview", "close", closeSelection)
  );
  const searchLabel = node("label", undefined, "worker-search");
  searchLabel.append(node("span", "Find a worker", "filter-label"));
  const search = node("input");
  search.type = "search";
  search.placeholder = "Address, IP or hostname";
  search.setAttribute("aria-label", "Find a worker");
  searchLabel.append(search);
  const list = node("div", undefined, "worker-list");
  directory.append(directoryHeader, searchLabel, list);
  const detail = node("aside", undefined, "worker-inspector detail");
  detail.hidden = true;
  detail.setAttribute("aria-label", "Selected worker");
  rail.append(overview, directory, detail);
  workspace.append(viewport, rail);
  const foot = disclosure("About these observations", "map-methodology");
  foot.append(
    node(
      "p",
      "Current reachability uses all measurement regions. History region and profile filters narrow returned errors, work and performance samples. An error in one region does not establish a global failure. Grey means unknown coverage. IP addresses identify observed endpoints, not physical GPU counts. Prices are observed offers; work is FrameWorks traffic."
    )
  );
  root.append(header, toolbar, workspace, foot);
  container.replaceChildren(root);
  let data: Response | undefined;
  let selected = initial.get("lp_endpoint") ?? "";
  let directoryIDs: Set<string> | undefined;
  let detailTab = "overview";
  let restoreFilters = true;
  let disposed = false;
  let map: MapLibreMap | undefined;
  let controller: AbortController | undefined;
  let fetchVersion = 0;
  let infrastructure: InfrastructureNode[] = [];
  let gatewayClusters: GatewayCluster[] = [];
  let unbindViewport = () => {};
  let unbindScroll = () => {};
  let oldOverflow: string | undefined;
  const infrastructureController = new AbortController();
  const endpointHistory = new Map<string, Response["history"]>();
  const pendingHistory = new Set<string>();
  const knownRegions = new Set<string>(initial.get("lp_region") ? [initial.get("lp_region")!] : []);
  for (const [input, key] of [
    [windowChoice.input, "lp_window"],
    [metricChoice.input, "lp_metric"],
  ] as const) {
    const value = initial.get(key);
    if (value && Array.from(input.options).some((o) => o.value === value)) input.value = value;
  }
  function persist() {
    const query = new URLSearchParams(window.location.search);
    query.set("view", "livepeer");
    query.set("lp_mode", "map");
    query.delete("lp_sort");
    for (const [key, value] of Object.entries({
      lp_window: windowChoice.input.value,
      lp_region: regionChoice.input.value,
      lp_profile: profileChoice.input.value,
      lp_inactive: String(inactive.checked),
      lp_endpoint: selected,
      lp_metric: metricChoice.input.value,
    })) {
      if (value) query.set(key, value);
      else query.delete(key);
    }
    for (const [id, input] of layerControls) query.set(`lp_layer_${id}`, String(input.checked));
    if (query.toString() === window.location.search.slice(1)) return;
    if (options.onQueryChange) options.onQueryChange(query);
    else window.history.replaceState(window.history.state, "", `?${query}`);
  }
  function fullscreen(enabled: boolean) {
    root.classList.toggle("is-fullscreen", enabled);
    maximize.setAttribute("aria-label", enabled ? "Exit fullscreen map" : "Open fullscreen map");
    maximize.title = enabled ? "Exit fullscreen map" : "Open fullscreen map";
    if (enabled) {
      oldOverflow = document.body.style.overflow;
      document.body.style.overflow = "hidden";
      root.tabIndex = -1;
      root.focus();
    } else if (oldOverflow !== undefined) {
      document.body.style.overflow = oldOverflow;
      oldOverflow = undefined;
    }
    map?.resize();
  }
  function closeSelection() {
    selected = "";
    directoryIDs = undefined;
    detail.hidden = directory.hidden = true;
    overview.hidden = false;
    tooltip.hidden = true;
    persist();
    updateMap();
  }
  function showDirectory(ids?: string[], title?: string) {
    selected = "";
    directoryIDs = ids ? new Set(ids) : undefined;
    directoryHeader.querySelector("h3")!.textContent =
      title || (ids ? `${ids.length} workers here` : "Workers");
    persist();
    search.value = "";
    detail.hidden = overview.hidden = true;
    directory.hidden = false;
    rail.scrollTop = 0;
    renderList();
  }
  function escape(event: KeyboardEvent) {
    if (event.key !== "Escape") return;
    if (settings.open) settings.open = false;
    else if (!detail.hidden || !directory.hidden) closeSelection();
    else if (root.classList.contains("is-fullscreen")) fullscreen(false);
  }
  container.addEventListener("keydown", escape);
  const filtered = (values: Cohort[] = data?.history.cohorts ?? []) =>
    values.filter(
      (c) =>
        (!regionChoice.input.value || c.region === regionChoice.input.value) &&
        (!profileChoice.input.value ||
          ["discovery", "state", "coverage"].includes(c.kind) ||
          c.profile_key === profileChoice.input.value)
    );
  function historyKey(id: string) {
    return `${id}|${regionChoice.input.value}`;
  }
  function endpointHistoryFor(p: Endpoint) {
    return endpointHistory.get(historyKey(p.id)) ?? data!.history;
  }
  function endpointCohorts(p: Endpoint, summaries = false) {
    const history = endpointHistoryFor(p);
    return filtered(summaries ? history.summaries : history.cohorts).filter(
      (c) => c.resource_id.toLowerCase() === p.resource.id.toLowerCase() && c.ip === p.ip
    );
  }
  function endpointErrors(p: Endpoint) {
    return endpointCohorts(p, true)
      .filter((c) => c.kind === "segment" || c.kind === "discovery")
      .reduce((n, c) => n + c.failures, 0);
  }
  function segments(p: Endpoint) {
    return endpointCohorts(p, true)
      .filter((c) => c.kind === "segment")
      .reduce((n, c) => n + c.samples, 0);
  }
  function observations(p?: Endpoint) {
    return (p ? endpointHistoryFor(p).observations : (data?.history.observations ?? [])).filter(
      (o) =>
        (!p || (o.resource_id.toLowerCase() === p.resource.id.toLowerCase() && o.ip === p.ip)) &&
        (!regionChoice.input.value || o.region === regionChoice.input.value)
    );
  }
  function renderErrors(target: HTMLElement, p?: Endpoint) {
    const errors = observations(p)
      .filter((o) => !o.success && o.failure_kind)
      .sort((a, b) => Date.parse(b.timestamp) - Date.parse(a.timestamp));
    const rows = node("div", undefined, "observation-events");
    if (!errors.length)
      rows.append(node("p", "No error samples returned for this selection.", "muted"));
    for (const error of errors.slice(0, 6)) {
      const row = node("div", undefined, "observation-event");
      const title = node("div");
      title.append(
        node("strong", error.failure_kind!.replace(/_/g, " ")),
        node("span", error.region || "Region unknown", "region-label")
      );
      row.append(
        title,
        node(
          "p",
          `${time(error.timestamp)} · ${error.kind}${error.qualifying ? " · qualifying check" : " · not counted as worker downtime"}`,
          "muted"
        )
      );
      if (!p) {
        const endpoint = data?.inventory.endpoints.find(
          (e) =>
            e.resource.id.toLowerCase() === error.resource_id.toLowerCase() && e.ip === error.ip
        );
        if (endpoint)
          row.append(button(`${endpoint.ip} →`, () => pick(endpoint.id), "observation-worker"));
        else row.append(node("code", error.uri || short(error.resource_id), "observation-target"));
      }
      rows.append(row);
    }
    target.append(rows);
  }
  function renderOverview() {
    if (!data) return;
    overviewBody.replaceChildren();
    const endpoints = data.inventory.endpoints;
    const summary = facts(
      [
        ["Observed workers", String(endpoints.length)],
        ["Reachable", String(endpoints.filter((p) => p.status === "up").length)],
        ["Unreachable", String(endpoints.filter((p) => p.status === "unavailable").length)],
        [
          "Coverage unknown",
          String(endpoints.filter((p) => !["up", "unavailable"].includes(p.status)).length),
        ],
      ],
      "network-counts"
    );
    const heading = node("div", undefined, "rail-heading");
    heading.append(node("span", "NETWORK OVERVIEW", "map-eyebrow"), node("h3", "Livepeer workers"));
    overviewBody.append(heading, summary);
    const coverage = node("section", undefined, "rail-section");
    const sites = data.inventory.observers ?? [];
    const regions = new Set(sites.map((site) => site.region).filter(Boolean));
    coverage.append(
      node("h4", "Measurement coverage"),
      node(
        "p",
        `${new Set(data.inventory.resources.map((r) => r.id)).size} catalog identities · ${regions.size} reporting regions`,
        "muted"
      )
    );
    const sitesPanel = disclosure(`${sites.length} gateways & probers`, "measurement-sites");
    for (const site of sites) {
      const row = button(
        `${site.kind === "prober" ? "Prober" : "Gateway"} · ${site.region || "Region unknown"}`,
        () =>
          showMeasurement(
            site.region,
            site.kind === "prober" ? "Prober" : "Gateway",
            site.last_seen,
            site.location.known
          ),
        "measurement-site"
      );
      sitesPanel.append(row);
    }
    coverage.append(sitesPanel);
    const unknown = endpoints.filter((p) => !p.location.known).length;
    if (unknown)
      coverage.append(
        button(
          `${unknown} ${unknown === 1 ? "worker" : "workers"} without a known location →`,
          () =>
            showDirectory(
              endpoints.filter((p) => !p.location.known).map((p) => p.id),
              "Location unknown"
            ),
          "text-action"
        )
      );
    overviewBody.append(coverage);
    const activity = node("section", undefined, "rail-section");
    activity.append(node("h4", "Recent error observations"));
    renderErrors(activity);
    overviewBody.append(activity);
  }
  function visibleEndpoints() {
    const query = search.value.trim().toLowerCase();
    return [...(data?.inventory.endpoints ?? [])]
      .filter((p) => !directoryIDs || directoryIDs.has(p.id))
      .filter(
        (p) =>
          !query ||
          `${p.ip} ${p.resource.id} ${p.resource.uri} ${p.location.country ?? ""}`
            .toLowerCase()
            .includes(query)
      )
      .sort((a, b) => a.resource.id.localeCompare(b.resource.id) || a.ip.localeCompare(b.ip));
  }
  function renderList() {
    list.replaceChildren();
    for (const p of visibleEndpoints()) {
      const row = button("", () => pick(p.id), "worker-row");
      row.dataset.endpoint = p.id;
      const identity = node("div");
      identity.append(node("strong", p.ip || "Unknown IP"), node("code", short(p.resource.id)));
      const address = node("span", p.authority || p.resource.uri, "worker-address");
      const signal = node("div");
      signal.append(
        node("span", statusLabel(p.status), `worker-status status-${p.status}`),
        node(
          "span",
          p.location.known ? p.location.country || "Location known" : "Location unknown",
          "muted"
        )
      );
      row.append(identity, address, signal);
      list.append(row);
    }
    if (!list.childElementCount)
      list.append(
        node(
          "p",
          data ? "No workers match this search." : "Waiting for network observations…",
          "rail-section muted"
        )
      );
    const discovered = new Set(data?.inventory.endpoints.map((p) => p.resource.id.toLowerCase()));
    const missing =
      data?.inventory.resources.filter((r) => r.active && !discovered.has(r.id.toLowerCase())) ??
      [];
    if (missing.length && !search.value && !directoryIDs) {
      const entries = disclosure(
        `${missing.length} identities without an observed worker`,
        "catalog-unknown"
      );
      entries.append(
        node(
          "p",
          "These identities are in the catalog; no visible endpoint has been observed. Availability is unknown.",
          "muted"
        )
      );
      for (const resource of missing) entries.append(node("code", short(resource.id)));
      list.append(entries);
    }
  }
  function appendWork(target: HTMLElement, title: string, total?: WorkSummary) {
    const section = node("section", undefined, "inspector-section");
    section.append(node("h4", title));
    if (!total) {
      section.append(node("p", "No work accounting observations for this selection.", "muted"));
      target.append(section);
      return;
    }
    const known = total.accounted_attempts > 0 || total.accepted_segments > 0;
    const acceptedKnown = total.accepted_segments ? total.accepted_duration_samples > 0 : known;
    const dispatchedKnown = total.dispatched_attempts
      ? total.dispatched_duration_samples > 0
      : known;
    section.append(
      facts([
        [
          "Accepted source minutes",
          acceptedKnown ? workMinutes(total.accepted_source_ms) : "Not accounted",
        ],
        [
          "Submitted source minutes",
          dispatchedKnown ? workMinutes(total.dispatched_source_ms) : "Not accounted",
        ],
        ["Accepted segments", String(total.accepted_segments)],
        [
          "Returned output pixels",
          total.returned_attempts ? total.returned_pixels : "Not observed",
        ],
        ["Known service charges", workAmount(total.service_charge)],
        [
          "Expected ticket credit sent",
          total.ticket_credit_incomplete
            ? "Unknown: query bound"
            : workAmount(total.ticket_credit_sent),
        ],
      ])
    );
    if (isPartialWork(total))
      section.append(
        node(
          "p",
          "Partial accounting. Missing duration, output or pricing is unknown, not zero.",
          "evidence-note"
        )
      );
    target.append(section);
  }
  function renderDetail(p: Endpoint) {
    const focusedTab = detail.querySelector<HTMLButtonElement>('[role="tab"]:focus')?.id;
    detail.hidden = false;
    overview.hidden = directory.hidden = true;
    const top = node("div", undefined, "inspector-header");
    top.append(
      node("span", "WORKER", "map-eyebrow"),
      iconButton("Close worker details", "close", closeSelection)
    );
    const heading = node("div", undefined, "worker-heading");
    heading.append(
      node("h3", p.ip || "Unknown endpoint"),
      node("span", statusLabel(p.status), `worker-status status-${p.status}`),
      node("code", p.resource.id)
    );
    const tabs = node("div", undefined, "inspector-tabs");
    tabs.setAttribute("role", "tablist");
    tabs.setAttribute("aria-label", "Worker information");
    for (const [id, label] of [
      ["overview", "Overview"],
      ["observations", "Observations"],
      ["work", "Work"],
    ]) {
      const tab = button(
        label,
        () => {
          detailTab = id;
          renderDetail(p);
          detail.querySelector<HTMLButtonElement>(`#worker-tab-${id}`)?.focus();
        },
        "inspector-tab"
      );
      tab.setAttribute("role", "tab");
      tab.setAttribute("aria-selected", String(detailTab === id));
      tab.id = `worker-tab-${id}`;
      tab.setAttribute("aria-controls", "worker-tab-content");
      tab.tabIndex = detailTab === id ? 0 : -1;
      tab.onkeydown = (event) => {
        const keys = ["overview", "observations", "work"];
        const next =
          event.key === "Home"
            ? 0
            : event.key === "End"
              ? 2
              : event.key === "ArrowRight"
                ? (keys.indexOf(id) + 1) % 3
                : event.key === "ArrowLeft"
                  ? (keys.indexOf(id) + 2) % 3
                  : -1;
        if (next < 0) return;
        event.preventDefault();
        detailTab = keys[next];
        renderDetail(p);
        detail.querySelector<HTMLButtonElement>(`#worker-tab-${detailTab}`)?.focus();
      };
      tabs.append(tab);
    }
    const content = node("div", undefined, "inspector-content");
    content.id = "worker-tab-content";
    content.setAttribute("role", "tabpanel");
    content.setAttribute("aria-labelledby", `worker-tab-${detailTab}`);
    content.tabIndex = 0;
    detail.replaceChildren(top, heading, tabs, content);
    if (focusedTab)
      detail.querySelector<HTMLButtonElement>(`#${focusedTab}`)?.focus({ preventScroll: true });
    const history = endpointHistoryFor(p);
    if (detailTab === "work") {
      const work = data?.history.work;
      if (!work || work.truncated) {
        content.append(
          node(
            "p",
            work?.truncated
              ? "Work totals exceed the query bound. Narrow the period or measurement region."
              : "Work accounting is unavailable for this selection.",
            "evidence-note"
          )
        );
        return;
      }
      const values = work.summaries.filter(
        (v) =>
          v.resource_id.toLowerCase() === p.resource.id.toLowerCase() &&
          (!regionChoice.input.value || v.region === regionChoice.input.value) &&
          (!profileChoice.input.value || v.profile_key === profileChoice.input.value)
      );
      if (work.ticket_credit_truncated)
        content.append(
          node(
            "p",
            "Ticket-credit totals exceed the query bound. Work and service charges remain available.",
            "evidence-note"
          )
        );
      appendWork(content, "This worker", sumWork(values.filter((v) => v.ip === p.ip)));
      appendWork(content, "Orchestrator across all observed workers", sumWork(values));
      content.append(
        node(
          "p",
          `FrameWorks gateway traffic · ${time(work.start)} – ${time(work.end)}. Accepted work excludes discarded results. Submitted work includes retries and hedges. Charges and ticket credit do not establish redeemed earnings.`,
          "evidence-note"
        )
      );
      return;
    }
    if (detailTab === "overview") {
      content.append(
        facts([
          [
            "Location",
            p.location.known
              ? [p.location.city, p.location.country].filter(Boolean).join(", ") || "Known location"
              : "Location unknown",
          ],
          ["Last successful check", time(p.last_success)],
          ["Last checked", time(p.last_check)],
          ["Observed segments", segments(p) ? String(segments(p)) : "No returned samples"],
          [
            "Observed errors",
            endpointCohorts(p, true).some((c) => c.kind === "segment" || c.kind === "discovery")
              ? String(endpointErrors(p))
              : "No returned samples",
          ],
          ["Advertised price", formatPrice(p)],
        ])
      );
      if (p.price)
        content.append(
          node(
            "p",
            `${freshPrice(p) ? "Price observed" : "Stale / undated quote"} ${time(p.price.observed_at)}`,
            "evidence-note"
          )
        );
      const address = disclosure("Orchestrator & discovery", "inspector-disclosure");
      address.append(
        facts([
          ["Onchain identity", p.resource.id],
          ["Advertised URI", p.resource.uri],
          ["Scheduled checks", `${p.cadence_seconds}s`],
        ])
      );
      const siblings = data!.inventory.endpoints.filter(
        (entry) => entry.resource.id.toLowerCase() === p.resource.id.toLowerCase()
      );
      for (const sibling of siblings)
        address.append(
          button(
            `${sibling.ip} · ${statusLabel(sibling.status)}`,
            () => pick(sibling.id),
            "measurement-site"
          )
        );
      content.append(address);
      const regions = [
        ...new Set(
          endpointCohorts(p, true)
            .map((c) => c.region)
            .filter(Boolean)
        ),
      ];
      content.append(node("h4", "Observed from"));
      const chips = node("div", undefined, "region-chips");
      for (const region of regions)
        chips.append(button(region, () => chooseRegion(region), "region-chip"));
      if (!regions.length) chips.append(node("p", "No regional history returned.", "muted"));
      content.append(
        chips,
        node(
          "p",
          "Reachability is sampled from measurement sites. Missing coverage does not establish downtime; a regional failure is not a global performance verdict.",
          "evidence-note"
        )
      );
      return;
    }
    content.append(node("h4", "Recent errors"));
    renderErrors(content, p);
    content.append(node("h4", "Discovery over time"));
    const timeline = node("div", undefined, "timeline");
    const start = Date.parse(history.start),
      end = Date.parse(history.end);
    const checks = observations(p).filter((o) => o.kind === "discovery");
    const cohorts = endpointCohorts(p);
    for (let i = 0; i < 48; i++) {
      const a = start + ((end - start) * i) / 48,
        b = start + ((end - start) * (i + 1)) / 48;
      const raw = checks.filter((c) => Date.parse(c.timestamp) >= a && Date.parse(c.timestamp) < b);
      const buckets = cohorts.filter(
        (c) => c.kind === "discovery" && Date.parse(c.bucket) >= a && Date.parse(c.bucket) < b
      );
      const samples = history.rolled_up ? buckets.reduce((n, c) => n + c.samples, 0) : raw.length;
      const successes = history.rolled_up
        ? buckets.reduce((n, c) => n + c.successes, 0)
        : raw.filter((c) => c.success).length;
      const qualified = history.rolled_up
        ? buckets.reduce((n, c) => n + c.qualified_samples, 0)
        : raw.filter((c) => c.qualifying).length;
      const cell = node(
        "span",
        undefined,
        successes ? "up" : samples > 0 && samples === qualified ? "unavailable" : "unknown"
      );
      cell.title = `${time(new Date(a).toISOString())}: ${samples} checks · ${successes} successful`;
      timeline.append(cell);
    }
    content.append(
      timeline,
      node(
        "p",
        "Green: successful checks · red: qualifying failures · grey: unknown. This is sampled coverage, not continuous uptime.",
        "evidence-note"
      )
    );
    content.append(node("h4", "Regional discovery latency"));
    drawChart(
      content,
      cohorts.filter((c) => c.kind === "discovery")
    );
    content.append(node("h4", "Transcoding response time"));
    if (regionChoice.input.value && profileChoice.input.value)
      drawChart(
        content,
        cohorts.filter((c) => c.kind === "attempt")
      );
    else
      content.append(
        node(
          "p",
          "Choose one measurement region and output profile in Layers & filters to inspect transcoding latency. Different paths and profiles are not comparable.",
          "evidence-note"
        )
      );
    if (history.truncated)
      content.append(
        node(
          "p",
          "Returned chart and error samples are truncated. Narrow the period or measurement region.",
          "evidence-note"
        )
      );
    if (options.headers && history.details?.length) {
      const diagnostics = disclosure("Operator diagnostics", "inspector-disclosure");
      for (let i = 0; i < history.observations.length; i++) {
        const event = history.observations[i],
          extra = history.details[i];
        if (
          event.resource_id.toLowerCase() === p.resource.id.toLowerCase() &&
          event.ip === p.ip &&
          extra &&
          Object.keys(extra).length
        )
          diagnostics.append(node("pre", JSON.stringify({ event, correlation: extra }, null, 2)));
      }
      content.append(diagnostics);
    }
  }
  function drawChart(target: HTMLElement, cohorts: Cohort[]) {
    if (!cohorts.length) {
      target.append(node("p", "No latency samples returned for this selection.", "evidence-note"));
      return;
    }
    const ns = "http://www.w3.org/2000/svg";
    const svg = document.createElementNS(ns, "svg");
    svg.setAttribute("viewBox", "0 0 300 140");
    svg.setAttribute("class", "observation-chart");
    svg.setAttribute("role", "img");
    svg.setAttribute(
      "aria-label",
      "Regional p95 response latency over time; gaps mean missing observations"
    );
    const start = Date.parse(data!.history.start),
      end = Date.parse(data!.history.end),
      max = Math.max(1, ...cohorts.map((c) => c.p95_ms));
    const groups = new Map<string, Cohort[]>();
    for (const c of cohorts) {
      const key = `${c.region} · ${c.profile_key || "discovery"}`;
      groups.set(key, [...(groups.get(key) ?? []), c]);
    }
    const colors = ["#7aa2f7", "#9ece6a", "#e0af68", "#7dcfff"];
    const legend = node("div", undefined, "chart-key");
    let index = 0;
    for (const [label, values] of groups) {
      const color = colors[index++ % colors.length];
      let previous: Cohort | undefined;
      for (const c of values.sort((a, b) => Date.parse(a.bucket) - Date.parse(b.bucket))) {
        const x = 8 + (284 * (Date.parse(c.bucket) - start)) / Math.max(1, end - start),
          y = 120 - (100 * c.p95_ms) / max;
        if (
          previous &&
          Date.parse(c.bucket) - Date.parse(previous.bucket) <=
            data!.history.interval_seconds * 1500
        ) {
          const line = document.createElementNS(ns, "line");
          for (const [key, value] of Object.entries({
            x1: 8 + (284 * (Date.parse(previous.bucket) - start)) / Math.max(1, end - start),
            y1: 120 - (100 * previous.p95_ms) / max,
            x2: x,
            y2: y,
          }))
            line.setAttribute(key, String(value));
          line.setAttribute("stroke", color);
          line.setAttribute("stroke-width", "2");
          svg.append(line);
        }
        const dot = document.createElementNS(ns, "circle");
        dot.setAttribute("cx", String(x));
        dot.setAttribute("cy", String(y));
        dot.setAttribute("r", "2.5");
        dot.setAttribute("fill", color);
        const title = document.createElementNS(ns, "title");
        title.textContent = `${label}: ${Math.round(c.p95_ms)} ms · ${c.samples} samples · ${time(c.bucket)}`;
        dot.append(title);
        svg.append(dot);
        previous = c;
      }
      const key = node("span", label);
      key.style.color = color;
      legend.append(key);
    }
    target.append(
      node(
        "p",
        `p95 · 0–${Math.round(max)} ms · ${time(data!.history.start)} – ${time(data!.history.end)}`,
        "chart-scale"
      ),
      svg,
      legend
    );
  }
  function pick(id: string) {
    if (!data) return;
    if (selected !== id) {
      detailTab = "overview";
      rail.scrollTop = 0;
    }
    selected = id;
    tooltip.hidden = true;
    persist();
    const endpoint = data.inventory.endpoints.find((p) => p.id === id);
    if (!endpoint) {
      closeSelection();
      return;
    }
    renderDetail(endpoint);
    updateMap();
    const key = historyKey(id);
    if (endpointHistory.has(key) || pendingHistory.has(key)) return;
    const version = fetchVersion;
    pendingHistory.add(key);
    const api = requestURL();
    api.searchParams.set("resource", endpoint.resource.id);
    api.searchParams.set("ip", endpoint.ip);
    void fetch(api, { signal: controller?.signal, headers: options.headers?.() })
      .then(async (response) => {
        if (!response.ok) throw new Error("History unavailable");
        const next = (await response.json()) as Response;
        if (disposed || version !== fetchVersion) return;
        if (next.history_status === "unavailable") throw new Error("History unavailable");
        endpointHistory.set(key, next.history);
        if (selected === id && historyKey(id) === key) {
          renderDetail(endpoint);
          updateMap();
        }
      })
      .catch(() => {
        if (!disposed && version === fetchVersion && selected === id && historyKey(id) === key)
          detail
            .querySelector(".inspector-content")
            ?.append(
              node(
                "p",
                "Worker history is unavailable; showing returned network samples.",
                "evidence-note"
              )
            );
      })
      .finally(() => {
        if (version === fetchVersion) pendingHistory.delete(key);
      });
  }
  function showMeasurement(region: string, kind: string, lastSeen?: string, known = true) {
    selected = "";
    persist();
    detail.hidden = false;
    overview.hidden = directory.hidden = true;
    const top = node("div", undefined, "inspector-header");
    top.append(
      node("h3", `${kind} measurement site`),
      iconButton("Close measurement details", "close", closeSelection)
    );
    detail.replaceChildren(
      top,
      facts([
        ["Region", region || "Unknown"],
        ["Location", known ? "Published location" : "Location unknown"],
        ["Last seen", time(lastSeen)],
      ]),
      node(
        "p",
        "Measurements describe the path from this region. Reporting sites supply their locations; gateway-cluster markers show cluster centres.",
        "evidence-note"
      )
    );
    if (region)
      detail.append(
        button(`Show observations from ${region}`, () => chooseRegion(region), "rail-action")
      );
  }
  function chooseRegion(region: string) {
    regionChoice.input.value = region;
    regionChoice.input.dispatchEvent(new Event("change"));
  }
  function render() {
    if (!data) return;
    renderOverview();
    renderList();
    if (selected) pick(selected);
    message.textContent =
      data.history_status === "unavailable"
        ? "History unavailable · inventory visible"
        : `Updated ${time(data.inventory.generated_at)}`;
    message.classList.toggle("is-warning", data.history_status === "unavailable");
    updateMap();
  }
  function showTooltip(event: MapLayerMouseEvent, title: string, lines: string[]) {
    tooltip.replaceChildren(node("strong", title));
    for (const line of lines) tooltip.append(node("span", line));
    const x = event.point?.x ?? 12,
      y = event.point?.y ?? 12;
    tooltip.style.left = `${Math.max(8, Math.min(x + 14, (viewport.clientWidth || 800) - 254))}px`;
    tooltip.style.top = `${Math.max(8, Math.min(y + 14, (viewport.clientHeight || 600) - 132))}px`;
    tooltip.hidden = false;
  }
  function updateMap() {
    if (!data || !map?.isStyleLoaded()) return;
    const put = (
      id: string,
      features: GeoJSON.Feature[],
      paint: CircleLayerSpecification["paint"],
      setup?: () => void
    ) => {
      const collection: GeoJSON.FeatureCollection = { type: "FeatureCollection", features };
      const source = map!.getSource(id) as GeoJSONSource | undefined;
      if (source) source.setData(collection);
      else {
        map!.addSource(id, { type: "geojson", data: collection });
        map!.addLayer({ id, source: id, type: "circle", paint });
        setup?.();
      }
      map!.setLayoutProperty(id, "visibility", layerControls.get(id)?.checked ? "visible" : "none");
    };
    const point = (
      longitude: number,
      latitude: number,
      properties: Record<string, unknown>
    ): GeoJSON.Feature => ({
      type: "Feature",
      geometry: { type: "Point", coordinates: [longitude, latitude] },
      properties,
    });
    put(
      "frameworks-nodes",
      infrastructure
        .filter(
          (p) =>
            p.latitude !== null &&
            p.longitude !== null &&
            Number.isFinite(p.latitude) &&
            Number.isFinite(p.longitude)
        )
        .map((p) => point(p.longitude!, p.latitude!, { name: p.name, status: p.status })),
      { "circle-radius": 4, "circle-color": "#7aa2f7", "circle-opacity": 0.55 }
    );
    const siteLayer = (id: string, features: GeoJSON.Feature[], color: string) => {
      put(
        id,
        features,
        {
          "circle-radius": 11,
          "circle-color": color,
          "circle-opacity": 0.18,
          "circle-stroke-width": 2,
          "circle-stroke-color": color,
        },
        () => {
          map!.on("click", id, (event) => {
            const p = event.features?.[0]?.properties;
            if (p)
              showMeasurement(String(p.region || ""), String(p.name), String(p.last_seen || ""));
          });
          map!.on("mousemove", id, (event) => {
            const p = event.features?.[0]?.properties;
            if (p)
              showTooltip(event, String(p.name), [
                String(p.region || "Region unknown"),
                "Click to inspect measurement coverage",
              ]);
          });
          map!.on("mouseleave", id, () => {
            tooltip.hidden = true;
          });
        }
      );
    };
    const reportingGateways = (data.inventory.observers ?? []).filter(
      (p) =>
        p.kind === "gateway" &&
        p.location.known &&
        Number.isFinite(p.location.latitude) &&
        Number.isFinite(p.location.longitude)
    );
    siteLayer(
      "gateway-clusters",
      [
        ...reportingGateways.map((p) =>
          point(p.location.longitude, p.location.latitude, {
            name: "Gateway",
            region: p.region,
            last_seen: p.last_seen,
          })
        ),
        ...gatewayClusters
          .filter(
            (p) =>
              !reportingGateways.some((site) => site.region === p.region) &&
              Number.isFinite(p.latitude) &&
              Number.isFinite(p.longitude) &&
              (p.latitude !== 0 || p.longitude !== 0)
          )
          .map((p) =>
            point(p.longitude, p.latitude, { name: `${p.name} gateway cluster`, region: p.region })
          ),
      ],
      "#7dcfff"
    );
    siteLayer(
      "prober-sites",
      (data.inventory.observers ?? [])
        .filter(
          (p) =>
            p.kind === "prober" &&
            p.location.known &&
            Number.isFinite(p.location.latitude) &&
            Number.isFinite(p.location.longitude)
        )
        .map((p) =>
          point(p.location.longitude, p.location.latitude, {
            name: "Prober",
            region: p.region,
            last_seen: p.last_seen,
          })
        ),
      "#e0af68"
    );
    const metric = metricChoice.input.value;
    const endpoints = data.inventory.endpoints.filter(
      (p) =>
        p.location.known &&
        Number.isFinite(p.location.latitude) &&
        Number.isFinite(p.location.longitude)
    );
    const features = endpoints.map((p) => {
      const count = metric === "activity" ? segments(p) : endpointErrors(p);
      const color =
        metric === "errors"
          ? count
            ? "#f7768e"
            : "#737aa2"
          : metric === "activity"
            ? count
              ? "#7aa2f7"
              : "#737aa2"
            : p.status === "up"
              ? "#9ece6a"
              : p.status === "unavailable"
                ? "#f7768e"
                : "#737aa2";
      return point(p.location.longitude, p.location.latitude, {
        id: p.id,
        color,
        selected: p.id === selected,
        radius:
          metric === "availability"
            ? p.last_success && Date.now() - Date.parse(p.last_success) < 7 * 86400000
              ? 6
              : 3
            : Math.min(13, 5 + Math.log2(1 + count)),
        status: p.status,
      });
    });
    put(
      "livepeer-endpoints",
      features,
      {
        "circle-radius": ["get", "radius"],
        "circle-color": ["get", "color"],
        "circle-opacity": 0.9,
        "circle-stroke-width": ["case", ["get", "selected"], 3, 1.5],
        "circle-stroke-color": ["case", ["get", "selected"], "#c0caf5", "#16161e"],
      },
      () => {
        map!.on("click", "livepeer-endpoints", (event) => {
          const ids = [
            ...new Set(
              (event.features ?? [])
                .map((feature) => String(feature.properties?.id || ""))
                .filter(Boolean)
            ),
          ];
          tooltip.hidden = true;
          if (ids.length > 1) showDirectory(ids);
          else if (ids[0]) pick(ids[0]);
        });
        map!.on("mousemove", "livepeer-endpoints", (event) => {
          const p = data?.inventory.endpoints.find(
            (p) => p.id === event.features?.[0]?.properties?.id
          );
          if (p)
            showTooltip(event, p.ip, [
              statusLabel(p.status),
              `${p.location.country || "Location known"} · ${segments(p)} observed segments`,
              (event.features?.length ?? 0) > 1
                ? `${event.features!.length} workers here · click to choose`
                : `${endpointErrors(p)} error observations · click to inspect`,
            ]);
        });
        map!.on("mouseenter", "livepeer-endpoints", () => {
          map!.getCanvas().style.cursor = "pointer";
        });
        map!.on("mouseleave", "livepeer-endpoints", () => {
          tooltip.hidden = true;
          map!.getCanvas().style.cursor = "";
        });
      }
    );
    legend.replaceChildren();
    const entries =
      metric === "availability"
        ? [
            ["good", "Reachable (any region)"],
            ["bad", "Unreachable"],
            ["unknown", "Unknown"],
          ]
        : metric === "errors"
          ? [
              ["bad", "Observed errors"],
              ["unknown", "No returned errors"],
            ]
          : [
              ["activity", "Observed segments"],
              ["unknown", "No returned work"],
            ];
    for (const [kind, label] of entries)
      legend.append(node("span", label, `legend-key legend-${kind}`));
    for (const [id, kind, label] of [
      ["gateway-clusters", "gateway", "Gateways"],
      ["prober-sites", "prober", "Probers"],
    ]) {
      if (layerControls.get(id)?.checked)
        legend.append(node("span", label, `legend-key legend-${kind}`));
    }
    legend.append(
      node(
        "span",
        `${endpoints.length} mapped / ${data.inventory.endpoints.length} observed`,
        "legend-count"
      )
    );
  }
  function fillOptions(input: HTMLSelectElement, values: string[], label: string) {
    const current = input.value;
    input.replaceChildren();
    for (const value of ["", ...values]) {
      const option = node("option", value || label);
      option.value = value;
      input.append(option);
    }
    if (values.includes(current)) input.value = current;
  }
  function requestURL() {
    const api = new URL(options.apiURL, window.location.origin);
    const region = regionChoice.input.value || (restoreFilters ? initial.get("lp_region") : "");
    if (region) api.searchParams.set("region", region);
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
  async function fetchData() {
    const version = ++fetchVersion;
    controller?.abort();
    controller = new AbortController();
    refresh.disabled = true;
    endpointHistory.clear();
    pendingHistory.clear();
    message.textContent = "Refreshing network observations…";
    try {
      const response = await fetch(requestURL(), {
        signal: controller.signal,
        headers: options.headers?.(),
      });
      if (!response.ok) throw new Error(`Observations unavailable (HTTP ${response.status})`);
      const next = (await response.json()) as Response;
      if (disposed || version !== fetchVersion) return;
      if (!Array.isArray(next.inventory?.endpoints) || !Array.isArray(next.history?.cohorts))
        throw new Error("Invalid network response");
      data = next;
      for (const entry of [
        ...data.history.summaries,
        ...(data.history.work?.summaries ?? []),
        ...(data.inventory.observers ?? []),
      ])
        if (entry.region) knownRegions.add(entry.region);
      if (regionChoice.input.value) knownRegions.add(regionChoice.input.value);
      fillOptions(regionChoice.input, [...knownRegions].sort(), "All regions");
      fillOptions(
        profileChoice.input,
        [
          ...new Set(
            [...data.history.summaries, ...(data.history.work?.summaries ?? [])]
              .map((c) => c.profile_key)
              .filter(Boolean)
          ),
        ].sort(),
        "All profiles"
      );
      if (restoreFilters) {
        for (const [input, key] of [
          [regionChoice.input, "lp_region"],
          [profileChoice.input, "lp_profile"],
        ] as const) {
          const value = initial.get(key);
          if (value && Array.from(input.options).some((o) => o.value === value))
            input.value = value;
        }
        restoreFilters = false;
      }
      render();
    } catch (error) {
      if (disposed || version !== fetchVersion || controller.signal.aborted) return;
      message.textContent = error instanceof Error ? error.message : "Observations unavailable";
      message.classList.add("is-warning");
      if (!data) {
        overviewBody.replaceChildren(
          node(
            "div",
            "Network observations are temporarily unavailable. Use Refresh to try again.",
            "rail-section evidence-note"
          )
        );
      }
    } finally {
      if (!disposed && version === fetchVersion) refresh.disabled = false;
    }
  }
  search.oninput = renderList;
  windowChoice.input.onchange = inactive.onchange = () => {
    persist();
    void fetchData();
  };
  regionChoice.input.onchange = () => {
    persist();
    void fetchData();
    if (data) {
      renderOverview();
      renderList();
      const p = data.inventory.endpoints.find((p) => p.id === selected);
      if (p) renderDetail(p);
      updateMap();
    }
  };
  profileChoice.input.onchange = () => {
    persist();
    render();
  };
  metricChoice.input.onchange = () => {
    persist();
    updateMap();
  };
  void loadMapLibre()
    .then((module) => {
      if (disposed) return;
      const basemap = readRuntimePublicConfig().basemap;
      map = new module.Map({
        container: mapContainer,
        ...basemapMapOptions(basemap),
        center: [10, 25],
        zoom: 2,
        minZoom: 2,
        maxZoom: 10,
        scrollZoom: false,
        doubleClickZoom: false,
        ...options.viewport?.camera,
      });
      unbindViewport = bindExplorerViewport(map, options.viewport);
      unbindScroll = bindModifierScrollZoom(map, () => {
        zoomHint.hidden = true;
      });
      map.addControl(new module.NavigationControl({ showCompass: false }));
      addRequiredAttribution(map, basemap);
      map.on("load", updateMap);
    })
    .catch(() => {
      if (!disposed) {
        mapNotice.hidden = false;
        mapNotice.append(
          node("strong", "Map temporarily unavailable"),
          node("p", "Worker observations are still available in the directory."),
          button("Browse workers", showDirectory, "text-action")
        );
      }
    });
  async function fetchInfrastructure() {
    if (!options.graphqlURL) return;
    try {
      const response = await fetch(options.graphqlURL, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        signal: infrastructureController.signal,
        body: JSON.stringify({
          query:
            "query NetworkMap { networkStatus { nodes { nodeId name latitude longitude status } clusters { clusterId name region latitude longitude services } } }",
          operationName: "NetworkMap",
        }),
      });
      if (!response.ok) return;
      const next = await response.json();
      if (!disposed && Array.isArray(next.data?.networkStatus?.nodes)) {
        infrastructure = next.data.networkStatus.nodes;
        gatewayClusters = (next.data.networkStatus.clusters ?? []).filter((c: GatewayCluster) =>
          c.services?.some((s) => s === "livepeer-gateway" || s.startsWith("livepeer-gateway-"))
        );
        updateMap();
      }
    } catch {
      /* Infrastructure and worker observations have independent availability. */
    }
  }
  void fetchData();
  void fetchInfrastructure();
  const timer = setInterval(() => void fetchData(), 60000);
  const infrastructureTimer = setInterval(() => void fetchInfrastructure(), 60000);
  return () => {
    disposed = true;
    clearInterval(timer);
    clearInterval(infrastructureTimer);
    infrastructureController.abort();
    controller?.abort();
    container.removeEventListener("keydown", escape);
    if (oldOverflow !== undefined) document.body.style.overflow = oldOverflow;
    unbindScroll();
    unbindViewport();
    map?.remove();
    container.replaceChildren();
  };
}

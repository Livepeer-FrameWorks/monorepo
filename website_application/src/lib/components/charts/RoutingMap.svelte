<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { SvelteMap } from "svelte/reactivity";
  import type { Map as LeafletMap, LayerGroup, Marker } from "leaflet";
  import { getIconComponent } from "$lib/iconUtils";
  import { spreadOverlappingMarkers, type Spreadable } from "./spreadOverlap";
  import { pointOnPath, samplePath } from "./arc";
  import "leaflet/dist/leaflet.css";
  import markerIconUrl from "leaflet/dist/images/marker-icon.png";
  import markerIconRetinaUrl from "leaflet/dist/images/marker-icon-2x.png";
  import markerShadowUrl from "leaflet/dist/images/marker-shadow.png";

  // Leaflet is client-side only
  let L: typeof import("leaflet") | null = null;

  // Icons for controls
  const MaximizeIcon = getIconComponent("Maximize2");
  const MinimizeIcon = getIconComponent("Minimize2");
  const HomeIcon = getIconComponent("Home");
  const CpuIcon = getIconComponent("Cpu");

  interface Route {
    from: [number, number]; // [lat, lng]
    to: [number, number]; // [lat, lng]
    score?: number; // 0-100 routing score
    status: string; // 'success' | 'failed'
    details?: string;
  }

  interface NodeLocation {
    id: string;
    lat: number;
    lng: number;
    name: string;
    clusterId?: string;
    nodeType?: string;
    status?: string;
  }

  type BucketType = "client" | "node";

  interface BucketPolygon {
    id: string;
    coords: [number, number][];
    kind: BucketType;
    stats?: {
      count?: number;
      successRate?: number;
      avgDistance?: number;
    };
  }

  interface Flow {
    from: [number, number];
    to: [number, number];
    weight?: number;
    color?: string;
  }

  interface ClusterMarker {
    id: string;
    name: string;
    region: string;
    lat: number;
    lng: number;
    nodeCount: number;
    healthyNodeCount: number;
    status: string;
    activeStreams: number;
    activeViewers: number;
    peerCount?: number;
    clusterType?: string;
    shortDescription?: string;
    currentStreams?: number;
    currentViewers?: number;
    egressMbps?: number;
    egressCapacityMbps?: number;
    ingressMbps?: number;
    services?: string[];
  }

  interface RelationshipLine {
    sourceClusterId?: string;
    targetClusterId?: string;
    from: [number, number];
    to: [number, number];
    type: "peering" | "traffic" | "replication" | "assignment";
    active: boolean;
    weight?: number;
    metrics?: {
      eventCount?: number;
      avgLatencyMs?: number;
      successRate?: number;
    };
  }

  interface ServiceInstance {
    instanceId?: string;
    serviceId: string;
    nodeId?: string | null;
    clusterId?: string | null;
    healthStatus?: string | null;
    status?: string | null;
  }

  // Orchestrator vantage pin: one per (gateway, orch_addr, resolved_ip).
  // Multi-IP orchs surface as multiple pins; the detail panel groups by orch.
  interface OrchestratorVantagePin {
    orchAddr: string;
    resolvedIp: string;
    gatewayId: string;
    gatewayRegion: string;
    lat: number;
    lng: number;
    latestLatencyMs: number;
    score: number;
    dialedRecently: boolean;
    instanceCount?: number;
  }

  interface DetailRow {
    label: string;
    value: string;
    code?: boolean;
  }

  interface DetailSection {
    title: string;
    rows: DetailRow[];
  }

  interface MapDetail {
    title: string;
    rows: DetailRow[];
    sections?: DetailSection[];
    tags?: string[];
    description?: string;
  }

  interface Props {
    routes: Route[];
    nodes: NodeLocation[];
    buckets?: BucketPolygon[];
    onBucketClick?: (id: string) => void;
    flows?: Flow[];
    clusters?: ClusterMarker[];
    relationships?: RelationshipLine[];
    serviceInstances?: ServiceInstance[];
    orchestratorVantages?: OrchestratorVantagePin[];
    onOrchestratorClick?: (orchAddr: string) => void;
    height?: number;
    zoom?: number;
    center?: [number, number];
    tileLayerUrl?: string;
  }

  let {
    routes = [],
    nodes = [],
    buckets = [],
    onBucketClick = undefined,
    flows = [],
    clusters = [],
    relationships = [],
    serviceInstances = [],
    orchestratorVantages = [],
    onOrchestratorClick = undefined,
    height = 500,
    zoom = 2,
    center = [20, 0],
    tileLayerUrl = "https://{s}.basemaps.cartocdn.com/dark_all/{z}/{x}/{y}{r}.png",
  }: Props = $props();

  let mapContainer = $state<HTMLElement>();
  let mapWrapper = $state<HTMLElement>();
  let mapControls = $state<HTMLElement>();
  let map: LeafletMap | null = null;
  let layerGroup: LayerGroup | null = null;
  let bucketLayer: LayerGroup | null = null;
  let flowLayer: LayerGroup | null = null;
  let clusterLayer: LayerGroup | null = null;
  let relationshipLayer: LayerGroup | null = null;
  let membershipLayer: LayerGroup | null = null;
  let serviceLayer: LayerGroup | null = null;
  let orchestratorLayer: LayerGroup | null = null;
  let pulseTimers: number[] = [];
  let nodeSpreadables: Spreadable[] = [];
  let clusterSpreadables: Spreadable[] = [];
  let orchestratorSpreadables: Spreadable[] = [];
  let nodeMarkersByID: Record<string, Marker> = {};
  let aggregateNodeMarkersByClusterID: Record<string, Marker> = {};
  let clusterMarkersByID: Record<string, Marker> = {};
  let renderedNodes: NodeLocation[] = [];
  let renderedClusters: ClusterMarker[] = [];
  let renderedRelationships: RelationshipLine[] = [];
  let renderedServicesByNode: Record<string, string[]> = {};
  let zoomHandlerAttached = false;

  const ROLE_COLORS: Record<string, string> = {
    core: "rgb(249, 115, 22)",
    central: "rgb(249, 115, 22)",
    media: "rgb(59, 130, 246)",
    edge: "rgb(59, 130, 246)",
    compute: "rgb(34, 197, 94)",
    worker: "rgb(34, 197, 94)",
    livepeer: "rgb(34, 197, 94)",
    "livepeer-gateway": "rgb(34, 197, 94)",
    orchestrator: "rgb(34, 197, 94)",
    default: "rgb(148, 163, 184)",
  };

  // UX state
  let isFullscreen = $state(false);
  let showScrollHint = $state(true);
  let showOrchestrators = $state(true);
  let selectedDetail = $state<MapDetail | null>(null);

  onMount(async () => {
    const leafletModule = await import("leaflet");
    L = leafletModule.default;

    const iconDefaultPrototype = L.Icon.Default.prototype as typeof L.Icon.Default.prototype & {
      _getIconUrl?: unknown;
    };
    delete iconDefaultPrototype._getIconUrl;
    L.Icon.Default.mergeOptions({
      iconRetinaUrl: markerIconRetinaUrl,
      iconUrl: markerIconUrl,
      shadowUrl: markerShadowUrl,
    });

    if (mapContainer) {
      initMap();
    }
  });

  onDestroy(() => {
    pulseTimers.forEach(clearInterval);
    pulseTimers = [];
    if (map) {
      map.remove();
      map = null;
    }
  });

  function initMap() {
    if (!L || !mapContainer) return;

    map = L.map(mapContainer, {
      center: center,
      zoom: zoom,
      minZoom: 2,
      maxZoom: 8,
      zoomControl: false,
      attributionControl: false,
      scrollWheelZoom: false, // Disabled by default - use modifier key
    });

    L.tileLayer(tileLayerUrl, {
      maxZoom: 19,
      subdomains: "abcd",
    }).addTo(map);

    // Enable scroll zoom only with modifier key (Alt/Option or Ctrl)
    mapContainer.addEventListener("wheel", handleWheel, { passive: false });
    if (mapControls) {
      L.DomEvent.disableClickPropagation(mapControls);
      L.DomEvent.disableScrollPropagation(mapControls);
    }

    // Order matters: first added is at the bottom
    bucketLayer = L.layerGroup().addTo(map);
    flowLayer = L.layerGroup().addTo(map);
    membershipLayer = L.layerGroup().addTo(map);
    relationshipLayer = L.layerGroup().addTo(map);
    layerGroup = L.layerGroup().addTo(map);
    serviceLayer = L.layerGroup().addTo(map);
    clusterLayer = L.layerGroup().addTo(map);
    // Orchestrator layer sits on top: pins are interactive and should be
    // clickable through other markers when stacked.
    orchestratorLayer = L.layerGroup().addTo(map);

    drawMap(routes, nodes, buckets, flows, clusters, relationships);
    drawOrchestrators(showOrchestrators ? orchestratorVantages : []);
  }

  function handleWheel(e: WheelEvent) {
    if (!map) return;
    if (e.altKey || e.ctrlKey || e.metaKey) {
      e.preventDefault();
      map.scrollWheelZoom.enable();
      showScrollHint = false;
    } else {
      map.scrollWheelZoom.disable();
    }
  }

  function toggleFullscreen() {
    isFullscreen = !isFullscreen;
    // Invalidate map size after transition
    setTimeout(() => map?.invalidateSize(), 310);
  }

  function resetView() {
    map?.setView(center, zoom);
  }

  function toggleOrchestrators(event: MouseEvent) {
    event.stopPropagation();
    const next = !showOrchestrators;
    showOrchestrators = next;
    if (!next) selectedDetail = null;
    drawOrchestrators(next ? orchestratorVantages : []);
  }

  function formatLoad(current: number | undefined, max: number | undefined): string {
    if (!max) return `${current ?? 0}`;
    return `${current ?? 0} / ${max}`;
  }

  function detailRow(label: string, value: string, code = false): DetailRow {
    return { label, value, code };
  }

  function openDetail(detail: MapDetail): void {
    selectedDetail = detail;
  }

  function stopPulseTimers(): void {
    pulseTimers.forEach(clearInterval);
    pulseTimers.forEach(clearTimeout);
    pulseTimers = [];
  }

  function shouldAggregateNodes(): boolean {
    return (map?.getZoom() ?? zoom) <= 3;
  }

  function nodeDetail(node: NodeLocation, services: string[] | undefined): MapDetail {
    const rows = [
      detailRow("Type", node.nodeType || "node"),
      detailRow("Status", node.status || "active"),
    ];
    if (node.clusterId) rows.push(detailRow("Cluster", node.clusterId));
    if (services?.length) rows.push(detailRow("Services", services.join(", ")));
    return {
      title: node.name,
      rows,
      tags: services,
    };
  }

  function aggregateNodeDetail(
    nodesInGroup: NodeLocation[],
    servicesByNode: Record<string, string[]>
  ): MapDetail {
    const sorted = [...nodesInGroup].sort((a, b) => a.name.localeCompare(b.name));
    return {
      title: `${nodesInGroup.length} nodes`,
      rows: sorted.map((node) => {
        const services = servicesByNode[node.id];
        const meta = [
          node.nodeType || "node",
          node.status || "active",
          services?.length ? services.join(", ") : "",
        ]
          .filter(Boolean)
          .join(" · ");
        return detailRow(node.name, meta);
      }),
    };
  }

  function markerLatLng(marker: Marker | undefined, fallback: [number, number]): [number, number] {
    if (!marker) return fallback;
    const ll = marker.getLatLng();
    return [ll.lat, ll.lng];
  }

  function roleColor(role: string | undefined, status?: string): string {
    if (status === "offline" || status === "down") return "rgb(100, 116, 139)";
    const normalized = (role ?? "").toLowerCase();
    return ROLE_COLORS[normalized] ?? ROLE_COLORS.default;
  }

  function serviceRole(services: string[] | undefined): string | undefined {
    if (!services?.length) return undefined;
    if (services.some((s) => s === "livepeer-gateway" || s.startsWith("livepeer-"))) {
      return "compute";
    }
    return undefined;
  }

  function nodeRole(node: NodeLocation, services: string[] | undefined): string {
    return serviceRole(services) ?? node.nodeType ?? "default";
  }

  function aggregateRole(
    nodesInGroup: NodeLocation[],
    servicesByNode: Record<string, string[]>
  ): string {
    if (nodesInGroup.some((node) => serviceRole(servicesByNode[node.id]) === "compute")) {
      return "compute";
    }
    if (nodesInGroup.some((node) => (node.nodeType ?? "").toLowerCase() === "core")) {
      return "core";
    }
    return "media";
  }

  function withAlpha(rgb: string, alpha: number): string {
    return rgb.replace("rgb(", "rgba(").replace(")", `, ${alpha})`);
  }

  function convexHull(pts: Array<{ x: number; y: number }>): Array<{ x: number; y: number }> {
    if (pts.length < 3) return pts.slice();
    const sorted = [...pts].sort((a, b) => a.x - b.x || a.y - b.y);
    const cross = (
      o: { x: number; y: number },
      a: { x: number; y: number },
      b: { x: number; y: number }
    ) => (a.x - o.x) * (b.y - o.y) - (a.y - o.y) * (b.x - o.x);
    const lower: Array<{ x: number; y: number }> = [];
    for (const p of sorted) {
      while (lower.length >= 2 && cross(lower[lower.length - 2], lower[lower.length - 1], p) <= 0) {
        lower.pop();
      }
      lower.push(p);
    }
    const upper: Array<{ x: number; y: number }> = [];
    for (let i = sorted.length - 1; i >= 0; i--) {
      const p = sorted[i];
      while (upper.length >= 2 && cross(upper[upper.length - 2], upper[upper.length - 1], p) <= 0) {
        upper.pop();
      }
      upper.push(p);
    }
    lower.pop();
    upper.pop();
    return lower.concat(upper);
  }

  function inflateHull(
    points: Array<{ x: number; y: number }>,
    padding: number
  ): Array<{ x: number; y: number }> {
    if (!points.length) return points;
    let cx = 0;
    let cy = 0;
    points.forEach((p) => {
      cx += p.x;
      cy += p.y;
    });
    cx /= points.length;
    cy /= points.length;
    return points.map((p) => {
      const dx = p.x - cx;
      const dy = p.y - cy;
      const len = Math.hypot(dx, dy) || 1;
      return { x: p.x + (dx / len) * padding, y: p.y + (dy / len) * padding };
    });
  }

  // Rounds each polygon vertex with a quadratic Bezier so the hull reads as a
  // soft blob. cornerRadius is in pixels; clamped per-vertex to half the
  // adjacent edge length so tight clusters don't fold in on themselves.
  function smoothPolygon(
    points: Array<{ x: number; y: number }>,
    cornerRadius: number,
    samplesPerCorner = 6
  ): Array<{ x: number; y: number }> {
    const n = points.length;
    if (n < 3) return points;
    const out: Array<{ x: number; y: number }> = [];
    for (let i = 0; i < n; i++) {
      const prev = points[(i - 1 + n) % n];
      const curr = points[i];
      const next = points[(i + 1) % n];
      const vpx = prev.x - curr.x;
      const vpy = prev.y - curr.y;
      const vnx = next.x - curr.x;
      const vny = next.y - curr.y;
      const lp = Math.hypot(vpx, vpy) || 1;
      const ln = Math.hypot(vnx, vny) || 1;
      const r = Math.min(cornerRadius, lp / 2, ln / 2);
      const sx = curr.x + (vpx / lp) * r;
      const sy = curr.y + (vpy / lp) * r;
      const ex = curr.x + (vnx / ln) * r;
      const ey = curr.y + (vny / ln) * r;
      for (let s = 0; s <= samplesPerCorner; s++) {
        const t = s / samplesPerCorner;
        const u = 1 - t;
        out.push({
          x: u * u * sx + 2 * u * t * curr.x + t * t * ex,
          y: u * u * sy + 2 * u * t * curr.y + t * t * ey,
        });
      }
    }
    return out;
  }

  function shouldDrawClusterHull(points: Array<{ x: number; y: number }>): boolean {
    if (points.length < 3) return false;
    let minX = Number.POSITIVE_INFINITY;
    let maxX = Number.NEGATIVE_INFINITY;
    let minY = Number.POSITIVE_INFINITY;
    let maxY = Number.NEGATIVE_INFINITY;
    points.forEach((p) => {
      minX = Math.min(minX, p.x);
      maxX = Math.max(maxX, p.x);
      minY = Math.min(minY, p.y);
      maxY = Math.max(maxY, p.y);
    });
    const width = maxX - minX;
    const height = maxY - minY;
    const major = Math.max(width, height);
    const minor = Math.max(1, Math.min(width, height));
    if (major > 360) return false;
    if (major / minor > 4) return false;
    if (major > 220 && minor < 56) return false;
    return true;
  }

  function relationshipColor(type: RelationshipLine["type"]): string {
    if (type === "traffic") return "rgba(34, 197, 94, 0.6)";
    if (type === "assignment") return "rgba(168, 85, 247, 0.72)";
    if (type === "replication") return "rgba(168, 85, 247, 0.7)";
    return "rgba(125, 207, 255, 0.72)";
  }

  function startPulse(
    from: [number, number],
    to: [number, number],
    color: string = "rgb(125, 207, 255)"
  ) {
    if (!L || !relationshipLayer) return;
    const steps = 60;
    const interval = 50;
    const layer = relationshipLayer;
    const leaflet = L;

    function createPulse(delay: number) {
      let step = 0;
      let marker: ReturnType<typeof leaflet.circleMarker> | null = null;

      const timerId = window.setTimeout(() => {
        const id = window.setInterval(() => {
          const t = step / steps;
          const [lat, lng] = pointOnPath(from, to, t);

          if (!marker) {
            marker = leaflet
              .circleMarker([lat, lng], {
                radius: 3,
                fillColor: color,
                fillOpacity: 0.9,
                stroke: false,
                interactive: false,
              })
              .addTo(layer);
          } else {
            marker.setLatLng([lat, lng]);
          }

          const opacity = t < 0.1 ? t / 0.1 : t > 0.9 ? (1 - t) / 0.1 : 0.9;
          marker.setStyle({ fillOpacity: opacity });

          step++;
          if (step > steps) step = 0;
        }, interval);

        pulseTimers.push(id);
      }, delay);

      pulseTimers.push(timerId);
    }

    createPulse(0);
    createPulse(1500);
  }

  function drawMap(
    currentRoutes: Route[],
    currentNodes: NodeLocation[],
    currentBuckets: BucketPolygon[],
    currentFlows: Flow[],
    currentClusters: ClusterMarker[] = [],
    currentRelationships: RelationshipLine[] = []
  ) {
    if (!map || !L) return;
    const leaflet = L;

    stopPulseTimers();
    nodeSpreadables = [];
    clusterSpreadables = [];
    nodeMarkersByID = {};
    aggregateNodeMarkersByClusterID = {};
    clusterMarkersByID = {};
    renderedNodes = currentNodes;
    renderedClusters = currentClusters;
    renderedRelationships = currentRelationships;

    // Remove old layer groups from map entirely
    if (bucketLayer) map.removeLayer(bucketLayer);
    if (flowLayer) map.removeLayer(flowLayer);
    if (membershipLayer) map.removeLayer(membershipLayer);
    if (relationshipLayer) map.removeLayer(relationshipLayer);
    if (layerGroup) map.removeLayer(layerGroup);
    if (serviceLayer) map.removeLayer(serviceLayer);
    if (clusterLayer) map.removeLayer(clusterLayer);

    // Recreate layer groups (order = z-order)
    bucketLayer = leaflet.layerGroup().addTo(map);
    flowLayer = leaflet.layerGroup().addTo(map);
    membershipLayer = leaflet.layerGroup().addTo(map);
    relationshipLayer = leaflet.layerGroup().addTo(map);
    layerGroup = leaflet.layerGroup().addTo(map);
    serviceLayer = leaflet.layerGroup().addTo(map);
    clusterLayer = leaflet.layerGroup().addTo(map);

    // 0. Draw bucket polygons first (optional)
    // Build a simple count map for heat intensity
    const bucketCounts: Record<string, number> = {};
    currentBuckets.forEach((b) => {
      bucketCounts[b.id] = (bucketCounts[b.id] || 0) + 1;
    });

    currentBuckets.forEach((b) => {
      // log-like scaling so sparse buckets still show
      const count = bucketCounts[b.id] ?? 1;
      const intensity = Math.min(
        1,
        Math.log1p(count) / Math.log1p(Math.max(...Object.values(bucketCounts)))
      );
      // Leaflet expects [lat, lng]
      const poly = leaflet.polygon(b.coords, {
        color: b.kind === "client" ? "rgba(59,130,246,0.55)" : "rgba(16,185,129,0.55)",
        weight: 1,
        fillOpacity: 0.12 + intensity * 0.35,
        opacity: 0.8,
      });
      // Bucket popup with stats
      const bucketRows = [detailRow("Type", b.kind === "client" ? "Viewer Bucket" : "Node Bucket")];
      if (b.stats?.count != null) bucketRows.push(detailRow("Events", `${b.stats.count}`));
      if (b.stats?.successRate != null)
        bucketRows.push(detailRow("Success", `${(b.stats.successRate * 100).toFixed(1)}%`));
      if (b.stats?.avgDistance != null)
        bucketRows.push(detailRow("Avg Distance", `${b.stats.avgDistance.toFixed(0)}km`));
      poly.on("click", () => {
        openDetail({
          title: b.kind === "client" ? "Viewer Bucket" : "Node Bucket",
          rows: bucketRows,
        });
        if (onBucketClick) onBucketClick(b.id);
      });
      poly.on("mouseover", () => poly.setStyle({ weight: 2 }));
      poly.on("mouseout", () => poly.setStyle({ weight: 1 }));
      poly.addTo(bucketLayer!);
    });

    // 1b. Draw flows (client bucket -> node bucket centroids)
    currentFlows.forEach((f) => {
      const color = f.color || "rgba(168, 85, 247, 0.5)"; // purple
      const weight = f.weight || 1.2;
      leaflet
        .polyline([f.from, f.to], {
          color,
          weight,
          opacity: 0.7,
          smoothFactor: 1,
        })
        .addTo(flowLayer!);
    });

    // Build cluster lookup for membership lines
    const clusterMap: Record<string, ClusterMarker> = {};
    currentClusters.forEach((c) => {
      clusterMap[c.id] = c;
    });

    // Build per-node service list (populated when serviceInstances prop is provided)
    const servicesByNode: Record<string, string[]> = {};
    const pushUniqueService = (
      target: Record<string, string[]>,
      key: string,
      serviceID: string
    ) => {
      if (!target[key]) target[key] = [];
      if (!target[key].includes(serviceID)) {
        target[key].push(serviceID);
      }
    };
    serviceInstances.forEach((si) => {
      if (!si.nodeId) return;
      pushUniqueService(servicesByNode, si.nodeId, si.serviceId);
    });
    Object.values(servicesByNode).forEach((svcs) => svcs.sort());
    renderedServicesByNode = servicesByNode;

    const aggregateNodes = shouldAggregateNodes();
    const nodesByCluster: Record<string, NodeLocation[]> = {};

    // 1. Draw Infrastructure Nodes
    const nodeMap: Record<string, NodeLocation> = {};
    const nodeTypeCountByCluster: Record<string, { core: number; edge: number; other: number }> =
      {};
    currentNodes.forEach((node) => {
      nodeMap[node.id] = node;
      const clusterID = node.clusterId ?? "";
      if (clusterID) {
        if (!nodeTypeCountByCluster[clusterID]) {
          nodeTypeCountByCluster[clusterID] = { core: 0, edge: 0, other: 0 };
        }
        const normalizedType = (node.nodeType ?? "").toLowerCase();
        if (normalizedType === "core") nodeTypeCountByCluster[clusterID].core++;
        else if (normalizedType === "edge") nodeTypeCountByCluster[clusterID].edge++;
        else nodeTypeCountByCluster[clusterID].other++;
        const group = nodesByCluster[clusterID] ?? [];
        group.push(node);
        nodesByCluster[clusterID] = group;
      }
    });

    const collapsedNodeIDs: Record<string, true> = {};
    if (aggregateNodes) {
      Object.entries(nodesByCluster).forEach(([clusterID, nodesInGroup]) => {
        if (nodesInGroup.length < 4) return;
        const avgLat = nodesInGroup.reduce((sum, node) => sum + node.lat, 0) / nodesInGroup.length;
        const avgLng = nodesInGroup.reduce((sum, node) => sum + node.lng, 0) / nodesInGroup.length;
        const activeCount = nodesInGroup.filter(
          (node) => (node.status ?? "active") === "active"
        ).length;
        const size = Math.max(24, Math.min(42, 18 + nodesInGroup.length * 3));
        const color = roleColor(
          aggregateRole(nodesInGroup, servicesByNode),
          activeCount === 0 ? "offline" : "active"
        );
        const icon = leaflet.divIcon({
          className: "node-dot-marker",
          html: `<div style="background-color: color-mix(in srgb, ${color} 20%, rgb(15, 23, 42)); width: ${size}px; height: ${size}px; border-radius: 8px; border: 2px solid ${color}; display: flex; align-items: center; justify-content: center; color: ${color}; font-size: 11px; font-weight: 700; box-shadow: 0 0 10px color-mix(in srgb, ${color} 35%, transparent);">${nodesInGroup.length}</div>`,
          iconSize: [size, size],
          iconAnchor: [size / 2, size / 2],
        });
        const marker = leaflet.marker([avgLat, avgLng], { icon }).addTo(layerGroup!);
        marker.on("click", () => openDetail(aggregateNodeDetail(nodesInGroup, servicesByNode)));
        aggregateNodeMarkersByClusterID[clusterID] = marker;
        nodeSpreadables.push({ marker, iconRadius: size / 2 });
        nodesInGroup.forEach((node) => {
          collapsedNodeIDs[node.id] = true;
        });
      });
    }

    currentNodes.forEach((node) => {
      if (collapsedNodeIDs[node.id]) return;

      const nodeSvcs = servicesByNode[node.id];
      const isComputeNode = serviceRole(nodeSvcs) === "compute";
      const color = roleColor(nodeRole(node, nodeSvcs), node.status);
      const nt = (node.nodeType ?? "").toLowerCase();
      const isCoreNode = !isComputeNode && (nt === "core" || nt === "central");

      let size: number;
      let html: string;
      if (isComputeNode) {
        size = 9;
        html = `<div class="node-shape node-shape--ring" style="width:${size}px; height:${size}px; --node-color:${color}; box-shadow: 0 0 7px ${color};"></div>`;
      } else if (isCoreNode) {
        size = 14;
        html = `<div class="shape-wrap shape-wrap--glow" style="--glow-color:${color};"><div class="node-shape node-shape--diamond" style="width:${size}px; height:${size}px; --node-color:${color};"></div></div>`;
      } else {
        size = 10;
        html = `<div class="node-shape node-shape--circle" style="width:${size}px; height:${size}px; --node-color:${color}; box-shadow: 0 0 6px ${color};"></div>`;
      }

      const nodeIcon = leaflet.divIcon({
        className: "node-dot-marker",
        html,
        iconSize: [size, size],
        iconAnchor: [size / 2, size / 2],
      });

      const nodeMarker: Marker = leaflet
        .marker([node.lat, node.lng], { icon: nodeIcon })
        .addTo(layerGroup!);
      nodeMarker.on("click", () => openDetail(nodeDetail(node, nodeSvcs)));
      nodeMarkersByID[node.id] = nodeMarker;
      nodeSpreadables.push({ marker: nodeMarker, iconRadius: size / 2 });
    });

    // Build per-cluster service list from node-owned services.
    // This keeps service display tied to node ownership instead of detached markers.
    const servicesByClusterFromNodes: Record<string, string[]> = {};
    Object.entries(servicesByNode).forEach(([nodeID, nodeServices]) => {
      const clusterID = nodeMap[nodeID]?.clusterId;
      if (!clusterID) return;
      nodeServices.forEach((serviceID) => {
        pushUniqueService(servicesByClusterFromNodes, clusterID, serviceID);
      });
    });
    Object.values(servicesByClusterFromNodes).forEach((svcs) => svcs.sort());

    // 2. Draw Routes (Bezier curves or straight lines)
    currentRoutes.forEach((route) => {
      const isSuccess = route.status === "success" || route.status === "SUCCESS";
      const color = isSuccess ? "rgba(34, 197, 94, 0.4)" : "rgba(239, 68, 68, 0.4)";
      const weight = 1;

      // Draw line
      const line = leaflet.polyline([route.from, route.to], {
        color: color,
        weight: weight,
        opacity: 0.6,
        smoothFactor: 1,
      });

      const routeRows = [detailRow("Status", isSuccess ? "Success" : "Failed")];
      if (route.score != null) routeRows.push(detailRow("Score", `${route.score}`));
      if (route.details) routeRows.push(detailRow("Details", route.details));
      line.on("click", () => openDetail({ title: "Route", rows: routeRows }));
      line.addTo(layerGroup!);

      // Draw Client (Origin) dot
      const clientIcon = leaflet.divIcon({
        className: "custom-client-icon",
        html: `<div style="background-color: ${isSuccess ? "rgb(34, 197, 94)" : "rgb(239, 68, 68)"}; width: 6px; height: 6px; border-radius: 50%;"></div>`,
        iconSize: [6, 6],
        iconAnchor: [3, 3],
      });

      const clientRows = [
        detailRow("Status", isSuccess ? "Success" : "Failed"),
        detailRow("Location", `${route.from[0].toFixed(2)}, ${route.from[1].toFixed(2)}`),
      ];
      if (route.score != null) clientRows.splice(1, 0, detailRow("Score", `${route.score}`));
      const clientMarker = leaflet.marker(route.from, { icon: clientIcon }).addTo(layerGroup!);
      clientMarker.on("click", () => openDetail({ title: "Viewer", rows: clientRows }));
    });

    // 4. Draw cluster markers
    currentClusters.forEach((cluster) => {
      const color = roleColor(cluster.clusterType, cluster.status);
      const radius = Math.max(10, Math.min(24, 10 + cluster.nodeCount * 2));
      const ct = (cluster.clusterType ?? "").toLowerCase();
      const isCore = ct === "central" || ct === "core";
      const size = radius * 2;

      const clusterHtml = isCore
        ? `<div class="cluster-shape cluster-shape--core cluster-marker--glow" style="width:${size}px; height:${size}px; --node-color:${color};">${cluster.nodeCount}</div>`
        : `<div class="cluster-shape cluster-shape--edge" style="width:${size}px; height:${size}px; --node-color:${color};">
            <svg class="cluster-shape__hex" viewBox="0 0 100 100" preserveAspectRatio="none">
              <polygon points="50,6 92,30 92,70 50,94 8,70 8,30"
                fill="color-mix(in srgb, ${color} 22%, rgba(15,23,42,0.7))"
                stroke="${color}" stroke-width="3"
                stroke-dasharray="6 4" stroke-linejoin="round" stroke-linecap="round" />
            </svg>
            <span class="cluster-shape__count" style="color:${color};">${cluster.nodeCount}</span>
          </div>`;

      const icon = leaflet.divIcon({
        className: "cluster-marker",
        html: clusterHtml,
        iconSize: [size, size],
        iconAnchor: [radius, radius],
      });

      // Build cluster popup with structured table layout
      const infoRows: DetailRow[] = [];
      if (cluster.region) infoRows.push(detailRow("Region", cluster.region));
      if (cluster.clusterType) infoRows.push(detailRow("Type", cluster.clusterType));
      infoRows.push(detailRow("Nodes", `${cluster.healthyNodeCount} / ${cluster.nodeCount}`));
      if (cluster.peerCount != null) infoRows.push(detailRow("Peers", `${cluster.peerCount}`));
      infoRows.push(detailRow("Status", cluster.status));
      const nodeTypeCounts = nodeTypeCountByCluster[cluster.id];
      if (nodeTypeCounts) {
        if (nodeTypeCounts.core > 0)
          infoRows.push(detailRow("Core Nodes", `${nodeTypeCounts.core}`));
        if (nodeTypeCounts.edge > 0)
          infoRows.push(detailRow("Edge Nodes", `${nodeTypeCounts.edge}`));
      }

      const sections: DetailSection[] = [];

      if (
        (cluster.currentStreams ?? 0) > 0 ||
        (cluster.currentViewers ?? 0) > 0 ||
        (cluster.egressMbps ?? 0) > 0 ||
        (cluster.ingressMbps ?? 0) > 0 ||
        (cluster.egressCapacityMbps ?? 0) > 0
      ) {
        sections.push({
          title: "Load",
          rows: [
            detailRow("Streams", `${cluster.currentStreams ?? 0}`),
            detailRow("Viewers", `${cluster.currentViewers ?? 0}`),
            detailRow(
              "Egress",
              `${formatLoad(cluster.egressMbps, cluster.egressCapacityMbps)} Mbps`
            ),
            detailRow("Ingress", `${cluster.ingressMbps ?? 0} Mbps`),
          ],
        });
      }

      const clusterNodeServices = servicesByClusterFromNodes[cluster.id] ?? [];
      const clusterDetail: MapDetail = {
        title: cluster.name,
        rows: infoRows,
        sections,
        tags: clusterNodeServices,
        description: cluster.shortDescription,
      };

      const clusterMarker: Marker = leaflet
        .marker([cluster.lat, cluster.lng], { icon, zIndexOffset: 1000 })
        .addTo(clusterLayer!);
      clusterMarker.on("click", () => openDetail(clusterDetail));
      clusterMarkersByID[cluster.id] = clusterMarker;
      clusterSpreadables.push({ marker: clusterMarker, iconRadius: radius });
    });

    applySpread();

    if (!zoomHandlerAttached && map) {
      map.on("zoomend", () => {
        drawMap(routes, nodes, buckets, flows, clusters, relationships);
        drawOrchestrators(showOrchestrators ? orchestratorVantages : []);
      });
      zoomHandlerAttached = true;
    }
  }

  function applySpread() {
    if (!map || !L) return;
    const zoomLevel = map.getZoom();
    const spreadOrchs = zoomLevel >= 5;
    spreadOverlappingMarkers(
      map,
      [...nodeSpreadables, ...clusterSpreadables, ...(spreadOrchs ? orchestratorSpreadables : [])],
      {
        groupThresholdMultiplier: zoomLevel >= 6 ? 1.55 : 2.15,
        maxExpandedGroupSize: 24,
        denseStepScale: 0.82,
      }
    );
    drawTopologyLines(renderedNodes, renderedClusters, renderedRelationships);
  }

  function drawTopologyLines(
    currentNodes: NodeLocation[],
    currentClusters: ClusterMarker[],
    currentRelationships: RelationshipLine[]
  ): void {
    if (!L || !membershipLayer || !relationshipLayer) return;
    const leaflet = L;
    stopPulseTimers();
    membershipLayer.clearLayers();
    relationshipLayer.clearLayers();

    // Group nodes by cluster: clusters with ≥2 visible nodes get a hull halo;
    // solo nodes keep a single radial line for legibility.
    const nodesByCluster: Record<string, NodeLocation[]> = {};
    const aggregateClusters: Record<string, true> = {};
    currentNodes.forEach((node) => {
      if (!node.clusterId) return;
      if (!clusterMarkersByID[node.clusterId]) return;
      if (aggregateNodeMarkersByClusterID[node.clusterId]) {
        aggregateClusters[node.clusterId] = true;
        return;
      }
      if (!nodeMarkersByID[node.id]) return;
      (nodesByCluster[node.clusterId] ??= []).push(node);
    });

    // Aggregate-node clusters: one line from cluster center to the aggregate
    // marker (drawing a hull around a single aggregate marker is pointless).
    Object.keys(aggregateClusters).forEach((clusterId) => {
      const clusterMarker = clusterMarkersByID[clusterId];
      const aggregateMarker = aggregateNodeMarkersByClusterID[clusterId];
      if (!clusterMarker || !aggregateMarker) return;
      const clusterCenter = clusterMarker.getLatLng();
      const from = markerLatLng(aggregateMarker, [clusterCenter.lat, clusterCenter.lng]);
      const to = markerLatLng(clusterMarker, from);
      if (from[0] === to[0] && from[1] === to[1]) return;
      const repNode = currentNodes.find((n) => n.clusterId === clusterId);
      const baseColor = repNode
        ? roleColor(nodeRole(repNode, renderedServicesByNode[repNode.id]), repNode.status)
        : "rgb(148, 163, 184)";
      leaflet
        .polyline([from, to], {
          color: withAlpha(baseColor, 0.42),
          weight: 2,
          opacity: 0.7,
          smoothFactor: 1,
          interactive: false,
        })
        .addTo(membershipLayer!);
    });

    Object.entries(nodesByCluster).forEach(([clusterId, members]) => {
      const clusterMarker = clusterMarkersByID[clusterId];
      if (!clusterMarker) return;
      const cluster = currentClusters.find((c) => c.id === clusterId);
      const clusterColor = cluster
        ? roleColor(cluster.clusterType, cluster.status)
        : "rgb(148, 163, 184)";

      const drawMemberLine = (node: NodeLocation) => {
        const from = markerLatLng(nodeMarkersByID[node.id], [node.lat, node.lng]);
        const to = markerLatLng(clusterMarker, from);
        if (from[0] === to[0] && from[1] === to[1]) return;
        const lineColor = withAlpha(
          roleColor(nodeRole(node, renderedServicesByNode[node.id]), node.status),
          0.3
        );
        leaflet
          .polyline([from, to], {
            color: lineColor,
            weight: 1.4,
            opacity: 0.7,
            smoothFactor: 1,
            interactive: false,
          })
          .addTo(membershipLayer!);
      };

      if (members.length === 1) {
        drawMemberLine(members[0]);
        return;
      }

      if (!map) return;
      const markersForHull = [clusterMarker, ...members.map((n) => nodeMarkersByID[n.id])].filter(
        Boolean
      );
      const pts = markersForHull.map((m) => map!.latLngToContainerPoint(m.getLatLng()));
      if (pts.length < 3) return;
      if (!shouldDrawClusterHull(pts)) {
        members.forEach(drawMemberLine);
        return;
      }
      const hull = convexHull(pts);
      const inflated = inflateHull(hull, 10);
      const smoothed = smoothPolygon(inflated, 14);
      const ring = smoothed.map((p) => {
        const ll = map!.containerPointToLatLng([p.x, p.y]);
        return [ll.lat, ll.lng] as [number, number];
      });
      leaflet
        .polygon(ring, {
          className: "cluster-hull",
          color: withAlpha(clusterColor, 0.5),
          weight: 1,
          fillColor: clusterColor,
          fillOpacity: 0.12,
          smoothFactor: 1,
          interactive: false,
        })
        .addTo(membershipLayer!);
    });

    currentRelationships.forEach((rel) => {
      const from = rel.sourceClusterId
        ? markerLatLng(clusterMarkersByID[rel.sourceClusterId], rel.from)
        : rel.from;
      const to = rel.targetClusterId
        ? markerLatLng(clusterMarkersByID[rel.targetClusterId], rel.to)
        : rel.to;
      const eventCount = rel.metrics?.eventCount ?? 0;
      // Log-scale traffic into a weight: a single event still draws, busy
      // pairs go thick. Inactive links collapse to a hair-thin trace.
      const trafficWeight = Math.min(5, 1.2 + Math.log10(1 + eventCount) * 1.1);
      const lineWeight = rel.active
        ? Math.max(trafficWeight, Math.min(4, (rel.weight ?? 1) * 0.5))
        : 1.2;
      const successRate = rel.metrics?.successRate ?? 1;
      const baseOpacity = rel.active ? 0.55 + 0.35 * successRate : 0.4;
      const dashMap: Record<RelationshipLine["type"], string | undefined> = {
        peering: "8 4",
        assignment: "12 6",
        replication: "12 6",
        traffic: undefined,
      };

      const line = leaflet.polyline(samplePath(from, to), {
        color: relationshipColor(rel.type),
        weight: lineWeight,
        opacity: baseOpacity,
        dashArray: dashMap[rel.type],
        smoothFactor: 1,
      });

      const rows = [detailRow("Type", rel.type)];
      if (rel.metrics?.eventCount != null)
        rows.push(detailRow("Events", rel.metrics.eventCount.toLocaleString()));
      if (rel.metrics?.avgLatencyMs != null)
        rows.push(detailRow("Latency", `${rel.metrics.avgLatencyMs.toFixed(1)}ms`));
      if (rel.metrics?.successRate != null)
        rows.push(detailRow("Success", `${(rel.metrics.successRate * 100).toFixed(1)}%`));
      line.on("click", () => openDetail({ title: "Topology Link", rows }));
      line.addTo(relationshipLayer!);

      if (
        rel.active &&
        (rel.type === "peering" || rel.type === "assignment" || rel.type === "replication")
      ) {
        const pulseColor = rel.type === "peering" ? "rgb(125, 207, 255)" : "rgb(192, 132, 252)";
        startPulse(from, to, pulseColor);
      }
    });
  }

  let drawTrigger = $derived({
    routesKey: routes
      .map((r) => `${r.from[0]}:${r.from[1]}:${r.to[0]}:${r.to[1]}:${r.status}:${r.score ?? ""}`)
      .sort()
      .join("|"),
    nodesKey: nodes
      .map(
        (n) =>
          `${n.id}:${n.lat}:${n.lng}:${n.clusterId ?? ""}:${n.nodeType ?? ""}:${n.status ?? ""}`
      )
      .sort()
      .join("|"),
    bucketsKey: buckets
      .map(
        (b) =>
          `${b.id}:${b.kind}:${b.coords.length}:${b.stats?.count ?? ""}:${b.stats?.successRate ?? ""}`
      )
      .sort()
      .join("|"),
    flowsKey: flows
      .map(
        (f) => `${f.from[0]}:${f.from[1]}:${f.to[0]}:${f.to[1]}:${f.weight ?? ""}:${f.color ?? ""}`
      )
      .sort()
      .join("|"),
    clustersKey: clusters
      .map(
        (c) =>
          `${c.id}:${c.lat}:${c.lng}:${c.status}:${c.nodeCount}:${c.healthyNodeCount}:${c.peerCount ?? ""}:${(c.services ?? []).join(",")}`
      )
      .sort()
      .join("|"),
    relationshipsKey: relationships
      .map(
        (r) =>
          `${r.from[0]}:${r.from[1]}:${r.to[0]}:${r.to[1]}:${r.type}:${r.active}:${r.weight ?? ""}:${r.metrics?.eventCount ?? ""}:${r.metrics?.avgLatencyMs ?? ""}:${r.metrics?.successRate ?? ""}`
      )
      .sort()
      .join("|"),
    servicesKey: serviceInstances
      .map(
        (s) =>
          `${s.instanceId ?? ""}:${s.serviceId}:${s.nodeId ?? ""}:${s.clusterId ?? ""}:${s.healthStatus ?? ""}:${s.status ?? ""}`
      )
      .sort()
      .join("|"),
  });

  $effect(() => {
    const _trigger = drawTrigger;
    if (map) {
      drawMap(routes, nodes, buckets, flows, clusters, relationships);
    }
  });

  // Orchestrator pins react independently — their data source (Periscope
  // discovery rollups) updates on a different cadence than nodes/clusters.
  $effect(() => {
    if (map) {
      drawOrchestrators(showOrchestrators ? orchestratorVantages : []);
    }
  });

  // At low zoom many vantages pile across a region; spread + glow can paint
  // over a continent. Scale icon and shadow with zoom so they only get loud
  // once the user is close enough to disambiguate them.
  function orchestratorSizeForZoom(z: number): { size: number; glow: number } {
    if (z <= 3) return { size: 8, glow: 2 };
    if (z <= 5) return { size: 11, glow: 4 };
    return { size: 14, glow: 6 };
  }

  function orchestratorPinColor(vantage: OrchestratorVantagePin): string {
    if (vantage.latestLatencyMs >= 750) return "rgb(74, 111, 91)";
    if (vantage.latestLatencyMs >= 250) return "rgb(45, 150, 96)";
    return "rgb(34, 197, 94)";
  }

  function dedupeOrchestratorVantages(
    vantages: OrchestratorVantagePin[]
  ): OrchestratorVantagePin[] {
    const byInstance = new SvelteMap<string, OrchestratorVantagePin>();
    for (const v of vantages) {
      if (!v.dialedRecently) continue;
      const key = `${v.orchAddr}:${v.resolvedIp}`;
      const current = byInstance.get(key);
      if (
        !current ||
        v.latestLatencyMs < current.latestLatencyMs ||
        (v.latestLatencyMs === current.latestLatencyMs && v.score > current.score)
      ) {
        byInstance.set(key, v);
      }
    }
    return [...byInstance.values()];
  }

  function drawOrchestrators(vantages: OrchestratorVantagePin[]): void {
    if (!L || !orchestratorLayer || !map) return;
    const leaflet = L;
    orchestratorLayer.clearLayers();
    orchestratorSpreadables = [];
    if (!vantages.length) {
      applySpread();
      return;
    }

    const visibleVantages = dedupeOrchestratorVantages(
      vantages.filter(
        (v) => Number.isFinite(v.lat) && Number.isFinite(v.lng) && (v.lat !== 0 || v.lng !== 0)
      )
    );
    const { size: orchSize, glow: orchGlow } = orchestratorSizeForZoom(map.getZoom());
    for (const v of visibleVantages) {
      const orchColor = orchestratorPinColor(v);
      const orchIcon = leaflet.divIcon({
        className: "node-dot-marker",
        html: `<div class="shape-wrap" style="filter: drop-shadow(0 0 ${orchGlow}px ${orchColor});"><div class="orch-triangle" style="width:${orchSize}px; height:${orchSize}px; --glow-color:${orchColor};"></div></div>`,
        iconSize: [orchSize, orchSize],
        iconAnchor: [orchSize / 2, orchSize / 2 + 1],
      });
      const marker = leaflet.marker([v.lat, v.lng], { icon: orchIcon });
      const rows = [
        detailRow("Orch", v.orchAddr, true),
        detailRow("IP", v.resolvedIp),
        detailRow("Gateway", `${v.gatewayId} (${v.gatewayRegion})`),
        detailRow("Latency", `${v.latestLatencyMs} ms`),
        detailRow("Score", v.score.toFixed(2)),
      ];
      const instanceRows: DetailRow[] = visibleVantages
        .filter((candidate) => candidate.orchAddr === v.orchAddr)
        .sort((a, b) => a.resolvedIp.localeCompare(b.resolvedIp))
        .map((candidate) =>
          detailRow(
            candidate.resolvedIp || "unknown",
            `${candidate.gatewayId} (${candidate.gatewayRegion}) · ${candidate.latestLatencyMs} ms`
          )
        );
      const sections = instanceRows.length
        ? [{ title: "Observed Instances", rows: instanceRows }]
        : [];
      const detail = { title: "Orchestrator", rows, sections };
      marker.on("click", () => {
        openDetail(detail);
        if (onOrchestratorClick) onOrchestratorClick(v.orchAddr);
      });
      marker.addTo(orchestratorLayer);
      orchestratorSpreadables.push({ marker, iconRadius: orchSize / 2 });
    }
    applySpread();
  }
</script>

<div
  bind:this={mapWrapper}
  class="map-wrapper"
  class:map-wrapper--fullscreen={isFullscreen}
  style="height: {isFullscreen ? '100%' : `${height}px`};"
>
  {#if routes.length === 0 && nodes.length === 0 && clusters.length === 0}
    <div class="empty-state">
      <span class="text-muted-foreground text-sm">No routing data available</span>
    </div>
  {/if}

  <!-- Map Controls -->
  <div bind:this={mapControls} class="map-controls">
    <button class="map-control-btn" type="button" onclick={resetView} title="Reset view">
      <HomeIcon class="w-4 h-4" />
    </button>
    <button
      class="map-control-btn"
      type="button"
      class:map-control-btn--active={showOrchestrators}
      onclick={toggleOrchestrators}
      title={showOrchestrators ? "Hide Livepeer compute" : "Show Livepeer compute"}
    >
      <CpuIcon class="w-4 h-4" />
    </button>
    <button
      class="map-control-btn"
      type="button"
      onclick={toggleFullscreen}
      title={isFullscreen ? "Exit fullscreen" : "Fullscreen"}
    >
      {#if isFullscreen}
        <MinimizeIcon class="w-4 h-4" />
      {:else}
        <MaximizeIcon class="w-4 h-4" />
      {/if}
    </button>
  </div>

  <!-- Scroll Hint Overlay -->
  {#if showScrollHint && !isFullscreen}
    <button class="scroll-hint" type="button" onclick={() => (showScrollHint = false)}>
      <span>Hold <kbd>⌥</kbd> or <kbd>Ctrl</kbd> + scroll to zoom</span>
    </button>
  {/if}

  <div bind:this={mapContainer} class="map-container"></div>

  {#if selectedDetail}
    <aside class="map-detail-panel" aria-label="Map selection details">
      <button
        class="map-detail-panel__close"
        type="button"
        onclick={() => (selectedDetail = null)}
        aria-label="Close details"
      >
        ×
      </button>
      <div class="map-detail-panel__body">
        <div class="map-popup">
          <div class="map-popup__title">{selectedDetail.title}</div>
          <table class="map-popup__table">
            <tbody>
              {#each selectedDetail.rows as row (`${row.label}:${row.value}`)}
                <tr>
                  <td class="map-popup__label">{row.label}</td>
                  <td class="map-popup__value">
                    {#if row.code}
                      <code class="map-popup__code">{row.value}</code>
                    {:else}
                      {row.value}
                    {/if}
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
          {#each selectedDetail.sections ?? [] as section (section.title)}
            <div class="map-popup__section-title">{section.title}</div>
            <table class="map-popup__table">
              <tbody>
                {#each section.rows as row (`${section.title}:${row.label}:${row.value}`)}
                  <tr>
                    <td class="map-popup__label">{row.label}</td>
                    <td class="map-popup__value">
                      {#if row.code}
                        <code class="map-popup__code">{row.value}</code>
                      {:else}
                        {row.value}
                      {/if}
                    </td>
                  </tr>
                {/each}
              </tbody>
            </table>
          {/each}
          {#if selectedDetail.tags?.length}
            <div class="map-popup__tags">
              {#each selectedDetail.tags as tag (tag)}
                <span class="map-popup__tag">{tag}</span>
              {/each}
            </div>
          {/if}
          {#if selectedDetail.description}
            <div class="map-popup__desc">{selectedDetail.description}</div>
          {/if}
        </div>
      </div>
    </aside>
  {/if}
</div>

<style>
  .map-wrapper {
    position: relative;
    width: 100%;
    border-radius: 0.5rem;
    background-color: rgb(15, 23, 42);
    transition: all 0.3s ease;
  }

  .map-wrapper--fullscreen {
    position: fixed;
    inset: 0;
    z-index: 50;
    border-radius: 0;
    height: 100% !important;
  }

  .map-container {
    width: 100%;
    height: 100%;
    z-index: 1;
  }

  .map-detail-panel {
    position: absolute;
    top: 0.75rem;
    right: 3.25rem;
    bottom: 0.75rem;
    z-index: 25;
    width: min(360px, calc(100% - 4.75rem));
    overflow: hidden;
    border: 1px solid rgba(51, 65, 85, 0.78);
    border-radius: 8px;
    background: rgba(15, 23, 42, 0.94);
    box-shadow: 0 20px 50px rgba(0, 0, 0, 0.36);
    backdrop-filter: blur(10px);
  }

  .map-detail-panel__close {
    position: absolute;
    top: 0.45rem;
    right: 0.55rem;
    z-index: 1;
    width: 1.5rem;
    height: 1.5rem;
    border: 1px solid rgba(71, 85, 105, 0.72);
    border-radius: 6px;
    background: rgba(30, 41, 59, 0.92);
    color: rgb(148, 163, 184);
    font-size: 1rem;
    line-height: 1;
    cursor: pointer;
  }

  .map-detail-panel__close:hover {
    color: rgb(226, 232, 240);
    border-color: rgba(148, 163, 184, 0.72);
  }

  .map-detail-panel__body {
    height: 100%;
    overflow-y: auto;
    overscroll-behavior: contain;
  }

  .map-controls {
    position: absolute;
    top: 0.75rem;
    right: 0.75rem;
    z-index: 20;
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
  }

  .map-control-btn {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 2rem;
    height: 2rem;
    background-color: rgba(30, 41, 59, 0.9);
    border: 1px solid rgba(51, 65, 85, 0.6);
    border-radius: 0.375rem;
    color: rgb(148, 163, 184);
    cursor: pointer;
    transition: all 0.15s ease;
  }

  .map-control-btn:hover {
    background-color: rgba(51, 65, 85, 0.9);
    color: rgb(226, 232, 240);
  }

  .map-control-btn--active {
    border-color: rgba(34, 197, 94, 0.65);
    color: rgb(134, 239, 172);
    box-shadow: 0 0 0 1px rgba(34, 197, 94, 0.18);
  }

  .scroll-hint {
    position: absolute;
    bottom: 0.75rem;
    left: 50%;
    transform: translateX(-50%);
    z-index: 15;
    background-color: rgba(30, 41, 59, 0.95);
    border: 1px solid rgba(51, 65, 85, 0.6);
    border-radius: 0.375rem;
    padding: 0.375rem 0.75rem;
    font-size: 0.75rem;
    color: rgb(148, 163, 184);
    cursor: pointer;
    transition: opacity 0.2s ease;
  }

  .scroll-hint:hover {
    opacity: 0.7;
  }

  .scroll-hint kbd {
    display: inline-block;
    padding: 0.125rem 0.375rem;
    background-color: rgba(51, 65, 85, 0.8);
    border-radius: 0.25rem;
    font-family: inherit;
    font-size: 0.7rem;
  }

  .empty-state {
    position: absolute;
    top: 0;
    left: 0;
    width: 100%;
    height: 100%;
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 10;
    pointer-events: none;
    background-color: rgba(15, 23, 42, 0.5);
  }

  :global(.leaflet-container) {
    background-color: rgb(15, 23, 42) !important;
  }

  /* Undo Tailwind Preflight resets that break Leaflet rendering */
  :global(.leaflet-container img) {
    max-width: none !important;
    max-height: none !important;
  }

  :global(.leaflet-container svg) {
    max-width: none !important;
    max-height: none !important;
  }

  :global(.leaflet-container *) {
    border-style: none;
  }

  :global(.map-popup) {
    padding: 0.75rem 1rem;
    max-height: none;
    overflow: visible;
  }

  :global(.map-popup__title) {
    font-weight: 600;
    font-size: 0.85rem;
    margin-bottom: 0.5rem;
    padding-bottom: 0.4rem;
    border-bottom: 1px solid rgba(51, 65, 85, 0.6);
    color: rgb(241, 245, 249);
  }

  :global(.map-popup__table) {
    width: 100%;
    border-collapse: collapse;
    font-size: 0.78rem;
  }

  :global(.map-popup__table tr + tr) {
    border-top: 1px solid rgba(51, 65, 85, 0.3);
  }

  :global(.map-popup__label) {
    color: rgb(148, 163, 184);
    padding: 0.2rem 0.75rem 0.2rem 0;
    white-space: nowrap;
    vertical-align: top;
  }

  :global(.map-popup__value) {
    color: rgb(226, 232, 240);
    padding: 0.2rem 0;
    text-align: right;
    white-space: normal;
    font-variant-numeric: tabular-nums;
    overflow-wrap: anywhere;
    word-break: break-word;
  }

  :global(.map-popup__code) {
    display: block;
    max-width: 100%;
    font-size: 0.72rem;
    line-height: 1.25;
    white-space: normal;
    overflow-wrap: anywhere;
    word-break: break-all;
  }

  :global(.map-popup__section-title) {
    font-size: 0.7rem;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: rgb(100, 116, 139);
    margin-top: 0.6rem;
    margin-bottom: 0.25rem;
  }

  :global(.map-popup__tags) {
    display: flex;
    flex-wrap: wrap;
    gap: 0.25rem;
    margin-top: 0.5rem;
  }

  :global(.map-popup__tag) {
    display: inline-block;
    padding: 0.1rem 0.4rem;
    font-size: 0.65rem;
    background: rgba(59, 130, 246, 0.15);
    border: 1px solid rgba(59, 130, 246, 0.3);
    border-radius: 3px;
    color: rgb(147, 197, 253);
  }

  :global(.map-popup__desc) {
    margin-top: 0.5rem;
    font-size: 0.75rem;
    font-style: italic;
    color: rgb(148, 163, 184);
  }

  :global(.cluster-marker--glow) {
    animation: cluster-glow 3s ease-in-out infinite alternate;
  }

  @keyframes cluster-glow {
    from {
      box-shadow: 0 0 8px color-mix(in srgb, var(--node-color) 20%, transparent);
    }
    to {
      box-shadow: 0 0 16px color-mix(in srgb, var(--node-color) 40%, transparent);
    }
  }

  @media (prefers-reduced-motion: reduce) {
    :global(.cluster-marker--glow) {
      animation: none;
    }
  }

  /* --- Marker shape vocabulary (parallel to website_marketing/network.css) --- */
  :global(.shape-wrap) {
    display: inline-flex;
    align-items: center;
    justify-content: center;
  }
  /* drop-shadow respects clip-path; box-shadow doesn't. */
  :global(.shape-wrap--glow) {
    filter: drop-shadow(0 0 6px var(--glow-color, currentColor));
  }
  :global(.node-shape) {
    background-color: var(--node-color);
  }
  :global(.node-shape--circle) {
    border-radius: 50%;
  }
  :global(.node-shape--ring) {
    background-color: rgba(15, 23, 42, 0.85);
    border: 2px solid var(--node-color);
    border-radius: 50%;
  }
  :global(.node-shape--diamond) {
    clip-path: polygon(50% 0, 100% 50%, 50% 100%, 0 50%);
  }
  :global(.orch-triangle) {
    background-color: var(--glow-color);
    clip-path: polygon(50% 8%, 100% 92%, 0 92%);
  }
  :global(.cluster-shape) {
    position: relative;
    display: flex;
    align-items: center;
    justify-content: center;
    font-size: 10px;
    font-weight: 600;
    color: var(--node-color);
  }
  :global(.cluster-shape--core) {
    border-radius: 10px;
    background: color-mix(in srgb, var(--node-color) 22%, rgba(15, 23, 42, 0.85));
    border: 2px solid var(--node-color);
    box-shadow: 0 0 12px color-mix(in srgb, var(--node-color) 40%, transparent);
  }
  :global(.cluster-shape--edge) {
    filter: drop-shadow(0 0 10px color-mix(in srgb, var(--node-color) 35%, transparent));
  }
  :global(.cluster-shape__hex) {
    position: absolute;
    inset: 0;
    pointer-events: none;
  }
  :global(.cluster-shape__count) {
    position: relative;
    z-index: 1;
  }
</style>

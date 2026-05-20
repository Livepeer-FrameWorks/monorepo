<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import type { Map as LeafletMap, LayerGroup, Marker } from "leaflet";
  import { getIconComponent } from "$lib/iconUtils";
  import { spreadOverlappingMarkers, type Spreadable } from "./spreadOverlap";
  import "leaflet/dist/leaflet.css";
  import markerIconUrl from "leaflet/dist/images/marker-icon.png";
  import markerIconRetinaUrl from "leaflet/dist/images/marker-icon-2x.png";
  import markerShadowUrl from "leaflet/dist/images/marker-shadow.png";

  // Leaflet is client-side only
  let L: typeof import("leaflet") | null = null;

  function escapeHtml(s: string): string {
    return s
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;");
  }

  // Icons for controls
  const MaximizeIcon = getIconComponent("Maximize2");
  const MinimizeIcon = getIconComponent("Minimize2");
  const HomeIcon = getIconComponent("Home");

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
    maxStreams?: number;
    currentStreams?: number;
    maxViewers?: number;
    currentViewers?: number;
    maxBandwidthMbps?: number;
    currentBandwidthMbps?: number;
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
  // Multi-IP orchs surface as multiple pins; the side panel groups by orch.
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
  let nodeMarkersByID: Record<string, Marker> = {};
  let aggregateNodeMarkersByClusterID: Record<string, Marker> = {};
  let clusterMarkersByID: Record<string, Marker> = {};
  let renderedNodes: NodeLocation[] = [];
  let renderedRelationships: RelationshipLine[] = [];
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
      maxZoom: 10,
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
    drawOrchestrators(orchestratorVantages);
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

  function formatLoad(current: number | undefined, max: number | undefined): string {
    if (!max) return `${current ?? 0}`;
    return `${current ?? 0} / ${max}`;
  }

  function popupRow(label: string, value: string): string {
    return `<tr><td class="map-popup__label">${escapeHtml(label)}</td><td class="map-popup__value">${value}</td></tr>`;
  }

  function popupSection(title: string, rows: string): string {
    return `<div class="map-popup__section-title">${escapeHtml(title)}</div><table class="map-popup__table">${rows}</table>`;
  }

  function stopPulseTimers(): void {
    pulseTimers.forEach(clearInterval);
    pulseTimers.forEach(clearTimeout);
    pulseTimers = [];
  }

  function shouldAggregateNodes(): boolean {
    return (map?.getZoom() ?? zoom) <= 3;
  }

  function nodePopupRows(node: NodeLocation, services: string[] | undefined): string {
    let rows =
      popupRow("Type", escapeHtml(node.nodeType || "node")) +
      popupRow("Status", escapeHtml(node.status || "active"));
    if (node.clusterId) rows += popupRow("Cluster", escapeHtml(node.clusterId));
    if (services?.length) rows += popupRow("Services", escapeHtml(services.join(", ")));
    return rows;
  }

  function aggregateNodePopup(
    nodesInGroup: NodeLocation[],
    servicesByNode: Record<string, string[]>
  ): string {
    const sorted = [...nodesInGroup].sort((a, b) => a.name.localeCompare(b.name));
    const rows = sorted
      .map((node) => {
        const services = servicesByNode[node.id];
        const meta = [
          node.nodeType || "node",
          node.status || "active",
          services?.length ? services.join(", ") : "",
        ]
          .filter(Boolean)
          .join(" · ");
        return `<tr><td class="map-popup__label">${escapeHtml(node.name)}</td><td class="map-popup__value">${escapeHtml(meta)}</td></tr>`;
      })
      .join("");
    return `<div class="map-popup"><div class="map-popup__title">${nodesInGroup.length} nodes</div><table class="map-popup__table">${rows}</table></div>`;
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

  function withAlpha(rgb: string, alpha: number): string {
    return rgb.replace("rgb(", "rgba(").replace(")", `, ${alpha})`);
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
          const lat = from[0] + (to[0] - from[0]) * t;
          const lng = from[1] + (to[1] - from[1]) * t;

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
      let bucketRows = popupRow("Type", b.kind === "client" ? "Viewer Bucket" : "Node Bucket");
      if (b.stats?.count != null) bucketRows += popupRow("Events", `${b.stats.count}`);
      if (b.stats?.successRate != null)
        bucketRows += popupRow("Success", `${(b.stats.successRate * 100).toFixed(1)}%`);
      if (b.stats?.avgDistance != null)
        bucketRows += popupRow("Avg Distance", `${b.stats.avgDistance.toFixed(0)}km`);
      poly.bindPopup(
        `<div class="map-popup"><table class="map-popup__table">${bucketRows}</table></div>`,
        { className: "dark-popup", maxWidth: 280, minWidth: 160 }
      );

      poly.on("click", () => {
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
        const color = roleColor("media", activeCount === 0 ? "offline" : "active");
        const icon = leaflet.divIcon({
          className: "node-dot-marker",
          html: `<div style="background-color: color-mix(in srgb, ${color} 20%, rgb(15, 23, 42)); width: ${size}px; height: ${size}px; border-radius: 8px; border: 2px solid ${color}; display: flex; align-items: center; justify-content: center; color: ${color}; font-size: 11px; font-weight: 700; box-shadow: 0 0 10px color-mix(in srgb, ${color} 35%, transparent);">${nodesInGroup.length}</div>`,
          iconSize: [size, size],
          iconAnchor: [size / 2, size / 2],
        });
        const marker = leaflet
          .marker([avgLat, avgLng], { icon })
          .bindPopup(aggregateNodePopup(nodesInGroup, servicesByNode), {
            className: "dark-popup",
            maxWidth: 520,
            minWidth: 260,
          })
          .addTo(layerGroup!);
        aggregateNodeMarkersByClusterID[clusterID] = marker;
        nodeSpreadables.push({ marker, iconRadius: size / 2 });
        nodesInGroup.forEach((node) => {
          collapsedNodeIDs[node.id] = true;
        });
      });
    }

    currentNodes.forEach((node) => {
      if (collapsedNodeIDs[node.id]) return;

      const color = roleColor(node.nodeType, node.status);
      const isEdge = (node.nodeType ?? "").toLowerCase() === "edge";
      const size = isEdge ? 10 : 14;
      const glow = isEdge ? "6px" : "12px";

      const nodeIcon = leaflet.divIcon({
        className: "node-dot-marker",
        html: `<div style="background-color: ${color}; width: ${size}px; height: ${size}px; border-radius: 50%; box-shadow: 0 0 ${glow} ${color};"></div>`,
        iconSize: [size, size],
        iconAnchor: [size / 2, size / 2],
      });

      const nodeSvcs = servicesByNode[node.id];
      let nodePopup =
        `<div class="map-popup"><div class="map-popup__title">${escapeHtml(node.name)}</div>` +
        `<table class="map-popup__table">${nodePopupRows(node, nodeSvcs)}</table>`;
      if (nodeSvcs?.length) {
        nodePopup += `<div class="map-popup__tags">${nodeSvcs.map((s) => `<span class="map-popup__tag">${escapeHtml(s)}</span>`).join("")}</div>`;
      }
      nodePopup += "</div>";

      const nodeMarker: Marker = leaflet
        .marker([node.lat, node.lng], { icon: nodeIcon })
        .bindPopup(nodePopup, { className: "dark-popup", maxWidth: 400, minWidth: 200 })
        .addTo(layerGroup!);
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

      let routeRows = popupRow("Status", isSuccess ? "Success" : "Failed");
      if (route.score != null) routeRows += popupRow("Score", `${route.score}`);
      if (route.details) routeRows += popupRow("Details", escapeHtml(route.details));
      line.bindPopup(
        `<div class="map-popup"><table class="map-popup__table">${routeRows}</table></div>`,
        { className: "dark-popup", maxWidth: 280, minWidth: 160 }
      );
      line.addTo(layerGroup!);

      // Draw Client (Origin) dot
      const clientIcon = leaflet.divIcon({
        className: "custom-client-icon",
        html: `<div style="background-color: ${isSuccess ? "rgb(34, 197, 94)" : "rgb(239, 68, 68)"}; width: 6px; height: 6px; border-radius: 50%;"></div>`,
        iconSize: [6, 6],
        iconAnchor: [3, 3],
      });

      let clientRows = popupRow("Status", isSuccess ? "Success" : "Failed");
      if (route.score != null) clientRows += popupRow("Score", `${route.score}`);
      clientRows += popupRow(
        "Location",
        `${route.from[0].toFixed(2)}, ${route.from[1].toFixed(2)}`
      );
      leaflet
        .marker(route.from, { icon: clientIcon })
        .bindPopup(
          `<div class="map-popup"><div class="map-popup__title">Viewer</div><table class="map-popup__table">${clientRows}</table></div>`,
          { className: "dark-popup", maxWidth: 280, minWidth: 160 }
        )
        .addTo(layerGroup!);
    });

    // 4. Draw cluster markers
    currentClusters.forEach((cluster) => {
      const color = roleColor(cluster.clusterType, cluster.status);
      const radius = Math.max(10, Math.min(24, 10 + cluster.nodeCount * 2));
      const ct = (cluster.clusterType ?? "").toLowerCase();
      const isCore = ct === "central" || ct === "core";
      const borderRadius = isCore ? "6px" : "50%";
      const borderStyle = isCore ? `3px solid ${color}` : `2px dashed ${color}`;

      const icon = leaflet.divIcon({
        className: "cluster-marker",
        html: `<div class="cluster-marker--glow" style="
          width: ${radius * 2}px; height: ${radius * 2}px; border-radius: ${borderRadius};
          background: radial-gradient(circle, color-mix(in srgb, ${color} 25%, transparent), color-mix(in srgb, ${color} 9%, transparent));
          border: ${borderStyle};
          display: flex; align-items: center; justify-content: center;
          font-size: 10px; font-weight: 600; color: ${color};
          box-shadow: 0 0 12px color-mix(in srgb, ${color} 30%, transparent);
          --node-color: ${color};
        ">${cluster.nodeCount}</div>`,
        iconSize: [radius * 2, radius * 2],
        iconAnchor: [radius, radius],
      });

      // Build cluster popup with structured table layout
      let infoRows =
        (cluster.region ? popupRow("Region", escapeHtml(cluster.region)) : "") +
        (cluster.clusterType ? popupRow("Type", escapeHtml(cluster.clusterType)) : "") +
        popupRow("Nodes", `${cluster.healthyNodeCount} / ${cluster.nodeCount}`) +
        (cluster.peerCount != null ? popupRow("Peers", `${cluster.peerCount}`) : "") +
        popupRow("Status", escapeHtml(cluster.status));
      const nodeTypeCounts = nodeTypeCountByCluster[cluster.id];
      if (nodeTypeCounts) {
        if (nodeTypeCounts.core > 0) infoRows += popupRow("Core Nodes", `${nodeTypeCounts.core}`);
        if (nodeTypeCounts.edge > 0) infoRows += popupRow("Edge Nodes", `${nodeTypeCounts.edge}`);
      }

      let popup =
        `<div class="map-popup"><div class="map-popup__title">${escapeHtml(cluster.name)}</div>` +
        `<table class="map-popup__table">${infoRows}</table>`;

      if ((cluster.maxStreams ?? 0) > 0 || (cluster.currentStreams ?? 0) > 0) {
        const loadRows =
          popupRow("Streams", formatLoad(cluster.currentStreams, cluster.maxStreams)) +
          popupRow("Viewers", formatLoad(cluster.currentViewers, cluster.maxViewers)) +
          popupRow(
            "Bandwidth",
            `${formatLoad(cluster.currentBandwidthMbps, cluster.maxBandwidthMbps)} Mbps`
          );
        popup += popupSection("Load", loadRows);
      }

      const clusterNodeServices = servicesByClusterFromNodes[cluster.id] ?? [];
      if (clusterNodeServices.length) {
        popup += `<div class="map-popup__tags">${clusterNodeServices.map((s) => `<span class="map-popup__tag">${escapeHtml(s)}</span>`).join("")}</div>`;
      }

      if (cluster.shortDescription) {
        popup += `<div class="map-popup__desc">${escapeHtml(cluster.shortDescription)}</div>`;
      }

      popup += "</div>";

      const clusterMarker: Marker = leaflet
        .marker([cluster.lat, cluster.lng], { icon, zIndexOffset: 1000 })
        .bindPopup(popup, { className: "dark-popup", maxWidth: 400, minWidth: 220 })
        .addTo(clusterLayer!);
      clusterMarkersByID[cluster.id] = clusterMarker;
      clusterSpreadables.push({ marker: clusterMarker, iconRadius: radius });
    });

    applySpread();

    if (!zoomHandlerAttached && map) {
      map.on("zoomend", () => drawMap(routes, nodes, buckets, flows, clusters, relationships));
      zoomHandlerAttached = true;
    }
  }

  function applySpread() {
    if (!map || !L) return;
    spreadOverlappingMarkers(map, [...nodeSpreadables, ...clusterSpreadables]);
    drawTopologyLines(renderedNodes, renderedRelationships);
  }

  function drawTopologyLines(
    currentNodes: NodeLocation[],
    currentRelationships: RelationshipLine[]
  ): void {
    if (!L || !membershipLayer || !relationshipLayer) return;
    const leaflet = L;
    stopPulseTimers();
    membershipLayer.clearLayers();
    relationshipLayer.clearLayers();

    const aggregateMembershipDrawn: Record<string, true> = {};
    currentNodes.forEach((node) => {
      if (!node.clusterId) return;
      const clusterMarker = clusterMarkersByID[node.clusterId];
      if (!clusterMarker) return;

      const aggregateMarker = aggregateNodeMarkersByClusterID[node.clusterId];
      if (aggregateMarker) {
        if (aggregateMembershipDrawn[node.clusterId]) return;
        aggregateMembershipDrawn[node.clusterId] = true;
      }
      const nodeMarker = aggregateMarker ?? nodeMarkersByID[node.id];
      if (!nodeMarker) return;

      const from = markerLatLng(nodeMarker, [node.lat, node.lng]);
      const to = markerLatLng(clusterMarker, from);
      if (from[0] === to[0] && from[1] === to[1]) return;

      const color = withAlpha(roleColor(node.nodeType, node.status), aggregateMarker ? 0.42 : 0.3);
      leaflet
        .polyline([from, to], {
          color,
          weight: aggregateMarker ? 2 : 1.4,
          opacity: 0.7,
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
      const lineWeight = rel.active ? Math.max(2, Math.min(4, (rel.weight ?? 1) * 0.5)) : 1.5;
      const dashMap: Record<RelationshipLine["type"], string | undefined> = {
        peering: "8 4",
        assignment: "12 6",
        replication: "12 6",
        traffic: undefined,
      };

      const line = leaflet.polyline([from, to], {
        color: relationshipColor(rel.type),
        weight: lineWeight,
        opacity: rel.active ? 0.8 : 0.4,
        dashArray: dashMap[rel.type],
        smoothFactor: 1,
      });

      let rows = popupRow("Type", escapeHtml(rel.type));
      if (rel.metrics?.eventCount != null)
        rows += popupRow("Events", rel.metrics.eventCount.toLocaleString());
      if (rel.metrics?.avgLatencyMs != null)
        rows += popupRow("Latency", `${rel.metrics.avgLatencyMs.toFixed(1)}ms`);
      if (rel.metrics?.successRate != null)
        rows += popupRow("Success", `${(rel.metrics.successRate * 100).toFixed(1)}%`);
      line.bindPopup(
        `<div class="map-popup"><table class="map-popup__table">${rows}</table></div>`,
        {
          className: "dark-popup",
          maxWidth: 280,
          minWidth: 160,
        }
      );
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
      drawOrchestrators(orchestratorVantages);
    }
  });

  // Cheap deterministic hash so unknown-geo pins land at stable
  // off-map positions across refreshes. Not cryptographic — just enough
  // spread to avoid stacking pins for different orchs at one coord.
  function hashStringForSpread(s: string): number {
    let h = 2166136261;
    for (let i = 0; i < s.length; i++) {
      h ^= s.charCodeAt(i);
      h = Math.imul(h, 16777619);
    }
    return h >>> 0;
  }

  // Color a vantage pin by recent reachability + latency. Failed-recently
  // (dialed_recently=false) is amber even when latency is unknown.
  function orchestratorPinColor(vantage: OrchestratorVantagePin): string {
    if (!vantage.dialedRecently) return "rgb(245, 158, 11)"; // amber — stale or last attempt failed
    if (vantage.latestLatencyMs >= 500) return "rgb(248, 113, 113)"; // red — slow
    if (vantage.latestLatencyMs >= 200) return "rgb(250, 204, 21)"; // yellow — degraded
    return "rgb(74, 222, 128)"; // green — healthy
  }

  // Visible Pacific gutter for vantages without resolved geo.
  const UNKNOWN_GEO_ANCHOR: [number, number] = [-42, -145];

  function drawOrchestrators(vantages: OrchestratorVantagePin[]): void {
    if (!L || !orchestratorLayer || !map) return;
    const leaflet = L;
    orchestratorLayer.clearLayers();
    if (!vantages.length) return;

    for (const v of vantages) {
      let lat = v.lat;
      let lng = v.lng;
      const unknownGeo = lat === 0 && lng === 0;
      if (unknownGeo) {
        // Spread unknown-geo pins around the off-map anchor so they don't
        // stack on a single coordinate. Spread is deterministic per
        // (orchAddr + resolvedIp) so the same pin stays in place across
        // refresh cycles.
        const seed = hashStringForSpread(v.orchAddr + ":" + v.resolvedIp);
        lat = UNKNOWN_GEO_ANCHOR[0] + ((seed >> 8) & 0xff) / 64.0;
        lng = UNKNOWN_GEO_ANCHOR[1] + (seed & 0xff) / 64.0;
      }
      const marker = leaflet.circleMarker([lat, lng], {
        radius: 6,
        color: orchestratorPinColor(v),
        weight: 2,
        fillColor: orchestratorPinColor(v),
        fillOpacity: 0.65,
      });
      const rows = [
        popupRow("Orch", `<code>${escapeHtml(v.orchAddr)}</code>`),
        popupRow("IP", escapeHtml(v.resolvedIp)),
        popupRow("Gateway", `${escapeHtml(v.gatewayId)} (${escapeHtml(v.gatewayRegion)})`),
        popupRow(
          "Latency",
          v.dialedRecently ? `${v.latestLatencyMs} ms` : "stale (last dial failed)"
        ),
        popupRow("Score", v.score.toFixed(2)),
      ];
      if (unknownGeo) {
        rows.unshift(
          popupRow(
            "Geo",
            "unknown — pinned to off-map anchor (check GEOIP_MMDB_PATH or DNS resolution)"
          )
        );
      }
      if (v.instanceCount && v.instanceCount > 1) {
        rows.push(popupRow("Instances", `${v.instanceCount} (multi-IP — see side panel)`));
      }
      marker.bindPopup(popupSection("Orchestrator", rows.join("")));
      if (onOrchestratorClick) {
        marker.on("click", () => onOrchestratorClick!(v.orchAddr));
      }
      marker.addTo(orchestratorLayer);
    }
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
  <div class="map-controls">
    <button class="map-control-btn" onclick={resetView} title="Reset view">
      <HomeIcon class="w-4 h-4" />
    </button>
    <button
      class="map-control-btn"
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

  :global(.dark-popup) {
    filter: drop-shadow(0 4px 12px rgba(0, 0, 0, 0.5));
  }

  :global(.dark-popup .leaflet-popup-content-wrapper) {
    background-color: rgb(30, 41, 59) !important;
    border: 1px solid rgb(51, 65, 85) !important;
    color: rgb(226, 232, 240) !important;
    border-radius: 6px !important;
    font-size: 0.8rem;
    line-height: 1.5;
    padding: 0 !important;
  }

  :global(.dark-popup .leaflet-popup-content) {
    margin: 0 !important;
    width: auto !important;
  }

  :global(.dark-popup .leaflet-popup-tip) {
    background-color: rgb(30, 41, 59) !important;
    border: 1px solid rgb(51, 65, 85) !important;
    border-top: none !important;
    border-left: none !important;
  }

  :global(.dark-popup .leaflet-popup-close-button) {
    color: rgb(148, 163, 184) !important;
    font-size: 1.1rem !important;
    top: 6px !important;
    right: 8px !important;
  }

  :global(.dark-popup .leaflet-popup-close-button:hover) {
    color: rgb(226, 232, 240) !important;
  }

  :global(.map-popup) {
    padding: 0.75rem 1rem;
    max-height: 300px;
    overflow-y: auto;
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
    white-space: nowrap;
    font-variant-numeric: tabular-nums;
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
</style>

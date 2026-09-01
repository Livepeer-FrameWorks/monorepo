<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { SvelteMap, SvelteSet } from "svelte/reactivity";
  import type { Feature, Geometry } from "geojson";
  import type { GeoJSONSource, Map as MapLibreMap, Marker } from "maplibre-gl";
  import {
    addRequiredAttribution,
    basemapMapOptions,
    bindModifierScrollZoom,
    featureCollection,
    firstBasemapSymbolLayer,
    readRuntimePublicConfig,
  } from "@frameworks/map-core";
  import { getIconComponent } from "$lib/iconUtils";
  import { spreadOverlappingMarkers, type Spreadable } from "./spreadOverlap";
  import { pointOnPath, samplePath } from "./arc";

  interface Route {
    from: [number, number];
    to: [number, number];
    score?: number;
    status: string;
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
    stats?: { count?: number; successRate?: number; avgDistance?: number };
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
    metrics?: { eventCount?: number; avgLatencyMs?: number; successRate?: number };
  }
  interface ServiceInstance {
    instanceId?: string;
    serviceId: string;
    nodeId?: string | null;
    clusterId?: string | null;
    healthStatus?: string | null;
    status?: string | null;
  }
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
  interface ClusterWorkload {
    clusterId: string;
    nodeId: string;
    workKind: string;
    eventCount: number;
    activeCount: number;
    bytes: number;
    mediaSeconds: number;
    errorCount: number;
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
    workloads?: ClusterWorkload[];
    orchestratorVantages?: OrchestratorVantagePin[];
    onOrchestratorClick?: (orchAddr: string) => void;
    height?: number;
    zoom?: number;
    center?: [number, number];
  }

  let {
    routes = [],
    nodes = [],
    buckets = [],
    onBucketClick,
    flows = [],
    clusters = [],
    relationships = [],
    serviceInstances = [],
    workloads = [],
    orchestratorVantages = [],
    onOrchestratorClick,
    height = 500,
    zoom = 2,
    center = [20, 0],
  }: Props = $props();

  const MaximizeIcon = getIconComponent("Maximize2");
  const MinimizeIcon = getIconComponent("Minimize2");
  const HomeIcon = getIconComponent("Home");
  const CpuIcon = getIconComponent("Cpu");
  const ROLE_COLORS: Record<string, string> = {
    core: "rgb(249, 115, 22)",
    central: "rgb(249, 115, 22)",
    media: "rgb(122, 162, 247)",
    edge: "rgb(122, 162, 247)",
    compute: "rgb(158, 206, 106)",
    worker: "rgb(158, 206, 106)",
    livepeer: "rgb(158, 206, 106)",
    "livepeer-gateway": "rgb(158, 206, 106)",
    orchestrator: "rgb(158, 206, 106)",
    default: "rgb(169, 177, 214)",
  };

  let mapContainer = $state<HTMLElement>();
  let map: MapLibreMap | null = null;
  let maplibre: typeof import("maplibre-gl") | null = null;
  let ready = $state(false);
  let unavailable = $state(false);
  let isFullscreen = $state(false);
  let showScrollHint = $state(true);
  let showOrchestrators = $state(true);
  let selectedDetail = $state<MapDetail | null>(null);
  let markers: Marker[] = [];
  let animationFrame: number | null = null;
  let unbindWheel = () => {};
  let pulsePaths: Array<{ from: [number, number]; to: [number, number]; color: string }> = [];
  const detailRegistry = new SvelteMap<string, MapDetail>();

  onMount(async () => {
    let runtime;
    try {
      runtime = readRuntimePublicConfig();
    } catch {
      console.error("Map unavailable: invalid basemap runtime configuration");
      unavailable = true;
      return;
    }
    try {
      const [module] = await Promise.all([
        import("maplibre-gl"),
        import("maplibre-gl/dist/maplibre-gl.css"),
      ]);
      if (!mapContainer) return;
      maplibre = module;
      map = new module.Map({
        container: mapContainer,
        center: [center[1], center[0]],
        zoom,
        minZoom: 2,
        maxZoom: 8,
        scrollZoom: false,
        ...basemapMapOptions(runtime.basemap),
      });
      addRequiredAttribution(map, runtime.basemap);
      unbindWheel = bindModifierScrollZoom(map, () => (showScrollHint = false));
      map.once("load", () => {
        if (!map) return;
        initializeLayers(map);
        bindLayerInteractions(map);
        ready = true;
        drawMap();
        map.on("zoomend", drawMap);
        startPulseAnimation();
      });
    } catch {
      console.error("Map unavailable: renderer initialization failed");
      unavailable = true;
    }
  });

  onDestroy(() => {
    unbindWheel();
    if (animationFrame !== null) cancelAnimationFrame(animationFrame);
    animationFrame = null;
    markers.forEach((marker) => marker.remove());
    markers = [];
    map?.remove();
    map = null;
  });

  function source(id: string): GeoJSONSource | null {
    return (map?.getSource(id) as GeoJSONSource | undefined) ?? null;
  }

  function initializeLayers(currentMap: MapLibreMap) {
    const empty = featureCollection([]);
    const beforeLabels = firstBasemapSymbolLayer(currentMap);
    for (const id of [
      "buckets",
      "flows",
      "workloads",
      "membership",
      "relationships",
      "routes",
      "clients",
      "pulses",
    ]) {
      currentMap.addSource(`routing-${id}`, { type: "geojson", data: empty });
    }
    currentMap.addLayer(
      {
        id: "routing-buckets-fill",
        type: "fill",
        source: "routing-buckets",
        paint: {
          "fill-color": ["match", ["get", "kind"], "client", "rgb(59,130,246)", "rgb(16,185,129)"],
          "fill-opacity": ["get", "opacity"],
        },
      },
      beforeLabels
    );
    currentMap.addLayer(
      {
        id: "routing-buckets-line",
        type: "line",
        source: "routing-buckets",
        paint: {
          "line-color": [
            "match",
            ["get", "kind"],
            "client",
            "rgba(59,130,246,0.55)",
            "rgba(16,185,129,0.55)",
          ],
          "line-width": ["case", ["boolean", ["feature-state", "hover"], false], 2, 1],
          "line-opacity": 0.8,
        },
      },
      beforeLabels
    );
    currentMap.addLayer(
      {
        id: "routing-flows",
        type: "line",
        source: "routing-flows",
        paint: {
          "line-color": ["get", "color"],
          "line-width": ["get", "weight"],
          "line-opacity": 0.7,
        },
      },
      beforeLabels
    );
    currentMap.addLayer(
      {
        id: "routing-workloads",
        type: "circle",
        source: "routing-workloads",
        paint: {
          "circle-radius": ["get", "radius"],
          "circle-color": ["get", "color"],
          "circle-opacity": 0.18,
          "circle-stroke-color": ["get", "color"],
          "circle-stroke-opacity": 0.75,
          "circle-stroke-width": 1.5,
        },
      },
      beforeLabels
    );
    currentMap.addLayer(
      {
        id: "routing-membership-fill",
        type: "fill",
        source: "routing-membership",
        filter: ["==", ["geometry-type"], "Polygon"],
        paint: { "fill-color": ["get", "color"], "fill-opacity": 0.12 },
      },
      beforeLabels
    );
    currentMap.addLayer(
      {
        id: "routing-membership-outline",
        type: "line",
        source: "routing-membership",
        filter: ["==", ["geometry-type"], "Polygon"],
        paint: { "line-color": ["get", "outline"], "line-width": 1 },
      },
      beforeLabels
    );
    currentMap.addLayer(
      {
        id: "routing-membership-lines",
        type: "line",
        source: "routing-membership",
        filter: ["==", ["geometry-type"], "LineString"],
        paint: {
          "line-color": ["get", "color"],
          "line-width": ["get", "weight"],
          "line-opacity": 0.7,
        },
      },
      beforeLabels
    );
    for (const [type, dash] of [
      ["peering", [4, 2]],
      ["assignment", [6, 3]],
      ["replication", [6, 3]],
      ["traffic", [1, 0]],
    ] as const) {
      currentMap.addLayer(
        {
          id: `routing-relationship-${type}`,
          type: "line",
          source: "routing-relationships",
          filter: ["==", ["get", "type"], type],
          layout: { "line-cap": "round", "line-join": "round" },
          paint: {
            "line-color": ["get", "color"],
            "line-width": ["get", "weight"],
            "line-opacity": ["get", "opacity"],
            ...(type === "traffic" ? {} : { "line-dasharray": [...dash] }),
          },
        },
        beforeLabels
      );
    }
    currentMap.addLayer(
      {
        id: "routing-routes",
        type: "line",
        source: "routing-routes",
        paint: {
          "line-color": [
            "case",
            ["boolean", ["get", "success"], false],
            "rgba(158,206,106,0.4)",
            "rgba(247,118,142,0.4)",
          ],
          "line-width": 1,
          "line-opacity": 0.6,
        },
      },
      beforeLabels
    );
    currentMap.addLayer({
      id: "routing-clients",
      type: "circle",
      source: "routing-clients",
      paint: {
        "circle-radius": 3,
        "circle-color": [
          "case",
          ["boolean", ["get", "success"], false],
          "rgb(158,206,106)",
          "rgb(247,118,142)",
        ],
      },
    });
    currentMap.addLayer({
      id: "routing-pulses",
      type: "circle",
      source: "routing-pulses",
      paint: {
        "circle-radius": 3,
        "circle-color": ["get", "color"],
        "circle-opacity": ["get", "opacity"],
      },
    });
  }

  function bindLayerInteractions(currentMap: MapLibreMap) {
    const clickable = [
      "routing-buckets-fill",
      "routing-workloads",
      "routing-routes",
      "routing-clients",
      "routing-relationship-peering",
      "routing-relationship-assignment",
      "routing-relationship-replication",
      "routing-relationship-traffic",
    ];
    for (const layer of clickable) {
      currentMap.on("mouseenter", layer, () => (currentMap.getCanvas().style.cursor = "pointer"));
      currentMap.on("mouseleave", layer, () => (currentMap.getCanvas().style.cursor = ""));
      currentMap.on("click", layer, (event) => {
        const feature = event.features?.[0];
        const detailID = feature?.properties?.detailID;
        if (typeof detailID === "string") selectedDetail = detailRegistry.get(detailID) ?? null;
        if (layer === "routing-buckets-fill") {
          const id = feature?.properties?.bucketID;
          if (typeof id === "string") onBucketClick?.(id);
        }
      });
    }
  }

  function detailRow(label: string, value: string, code = false): DetailRow {
    return { label, value, code };
  }

  function formatLoad(current: number | undefined, max: number | undefined): string {
    return max ? `${current ?? 0} / ${max}` : `${current ?? 0}`;
  }

  function formatBytes(bytes: number): string {
    if (bytes < 1_000) return `${bytes.toFixed(0)} B`;
    if (bytes < 1_000_000) return `${(bytes / 1_000).toFixed(1)} kB`;
    if (bytes < 1_000_000_000) return `${(bytes / 1_000_000).toFixed(1)} MB`;
    return `${(bytes / 1_000_000_000).toFixed(1)} GB`;
  }

  function workloadColor(workKind: string): string {
    const kind = workKind.toLowerCase();
    if (kind.includes("deliver") || kind.includes("connection")) return "rgb(158, 206, 106)";
    if (kind.includes("process") || kind.includes("transcod")) return "rgb(187, 154, 247)";
    if (kind.includes("store") || kind.includes("storage")) return "rgb(125, 207, 255)";
    if (kind.includes("origin") || kind.includes("federat")) return "rgb(224, 175, 104)";
    return "rgb(169, 177, 214)";
  }

  function workloadSection(items: ClusterWorkload[]): DetailSection | null {
    if (items.length === 0) return null;
    const byKind = new SvelteMap<string, ClusterWorkload>();
    for (const item of items) {
      const current = byKind.get(item.workKind);
      if (current) {
        current.eventCount += item.eventCount;
        current.activeCount += item.activeCount;
        current.bytes += item.bytes;
        current.mediaSeconds += item.mediaSeconds;
        current.errorCount += item.errorCount;
      } else {
        byKind.set(item.workKind, { ...item });
      }
    }
    return {
      title: "Confirmed Workload",
      rows: [...byKind.values()]
        .sort((left, right) => right.eventCount - left.eventCount)
        .map((item) =>
          detailRow(
            item.workKind.replaceAll("_", " "),
            [
              `${item.eventCount.toLocaleString()} events`,
              item.activeCount ? `${item.activeCount.toLocaleString()} active` : "",
              item.bytes ? formatBytes(item.bytes) : "",
              item.mediaSeconds ? `${item.mediaSeconds.toLocaleString()} media s` : "",
              item.errorCount ? `${item.errorCount.toLocaleString()} errors` : "",
            ]
              .filter(Boolean)
              .join(" · ")
          )
        ),
    };
  }

  function roleColor(role: string | undefined, status?: string): string {
    if (status === "offline" || status === "down") return "rgb(100, 116, 139)";
    return ROLE_COLORS[(role ?? "").toLowerCase()] ?? ROLE_COLORS.default;
  }

  function serviceRole(services: string[] | undefined): string | undefined {
    return services?.some(
      (service) => service === "livepeer-gateway" || service.startsWith("livepeer-")
    )
      ? "compute"
      : undefined;
  }

  function nodeRole(node: NodeLocation, services: string[] | undefined): string {
    return serviceRole(services) ?? node.nodeType ?? "default";
  }

  function withAlpha(rgb: string, alpha: number): string {
    return rgb.replace("rgb(", "rgba(").replace(")", `, ${alpha})`);
  }

  function relationshipColor(type: RelationshipLine["type"]): string {
    if (type === "traffic") return "rgba(158,206,106,0.6)";
    if (type === "assignment" || type === "replication") return "rgba(187,154,247,0.72)";
    return "rgba(125,207,255,0.72)";
  }

  function markerLatLng(marker: Marker | undefined, fallback: [number, number]): [number, number] {
    if (!marker) return fallback;
    const point = marker.getLngLat();
    return [point.lat, point.lng];
  }

  function createMarkerElement(html: string, label: string): HTMLButtonElement {
    const element = document.createElement("button");
    element.type = "button";
    element.className = "routing-marker";
    element.setAttribute("aria-label", label);
    element.innerHTML = html;
    return element;
  }

  function servicesByNode(): Record<string, string[]> {
    const output: Record<string, string[]> = {};
    for (const instance of serviceInstances) {
      if (!instance.nodeId) continue;
      output[instance.nodeId] ??= [];
      if (!output[instance.nodeId].includes(instance.serviceId))
        output[instance.nodeId].push(instance.serviceId);
    }
    Object.values(output).forEach((services) => services.sort());
    return output;
  }

  function nodeDetail(
    node: NodeLocation,
    services: string[] | undefined,
    nodeWorkloads: ClusterWorkload[]
  ): MapDetail {
    const rows = [
      detailRow("Type", node.nodeType || "node"),
      detailRow("Status", node.status || "active"),
    ];
    if (node.clusterId) rows.push(detailRow("Cluster", node.clusterId));
    const section = workloadSection(nodeWorkloads);
    return { title: node.name, rows, sections: section ? [section] : [], tags: services };
  }

  function aggregateNodeDetail(
    group: NodeLocation[],
    byNode: Record<string, string[]>,
    workloadsByNode: Record<string, ClusterWorkload[]>
  ): MapDetail {
    const section = workloadSection(group.flatMap((node) => workloadsByNode[node.id] ?? []));
    return {
      title: `${group.length} nodes`,
      rows: [...group]
        .sort((left, right) => left.name.localeCompare(right.name))
        .map((node) =>
          detailRow(
            node.name,
            [node.nodeType || "node", node.status || "active", byNode[node.id]?.join(", ")]
              .filter(Boolean)
              .join(" · ")
          )
        ),
      sections: section ? [section] : [],
    };
  }

  function clusterDetail(
    cluster: ClusterMarker,
    counts: { core: number; edge: number },
    tags: string[],
    clusterWorkloads: ClusterWorkload[]
  ): MapDetail {
    const rows: DetailRow[] = [];
    if (cluster.region) rows.push(detailRow("Region", cluster.region));
    if (cluster.clusterType) rows.push(detailRow("Type", cluster.clusterType));
    rows.push(detailRow("Nodes", `${cluster.healthyNodeCount} / ${cluster.nodeCount}`));
    if (cluster.peerCount != null) rows.push(detailRow("Peers", `${cluster.peerCount}`));
    rows.push(detailRow("Status", cluster.status));
    if (counts.core > 0) rows.push(detailRow("Core Nodes", `${counts.core}`));
    if (counts.edge > 0) rows.push(detailRow("Edge Nodes", `${counts.edge}`));
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
          detailRow("Egress", `${formatLoad(cluster.egressMbps, cluster.egressCapacityMbps)} Mbps`),
          detailRow("Ingress", `${cluster.ingressMbps ?? 0} Mbps`),
        ],
      });
    }
    const confirmedWorkload = workloadSection(clusterWorkloads);
    if (confirmedWorkload) sections.push(confirmedWorkload);
    return { title: cluster.name, rows, sections, tags, description: cluster.shortDescription };
  }

  function convexHull(points: Array<{ x: number; y: number }>): Array<{ x: number; y: number }> {
    if (points.length < 3) return points.slice();
    const sorted = [...points].sort((left, right) => left.x - right.x || left.y - right.y);
    const cross = (
      origin: { x: number; y: number },
      left: { x: number; y: number },
      right: { x: number; y: number }
    ) => (left.x - origin.x) * (right.y - origin.y) - (left.y - origin.y) * (right.x - origin.x);
    const half = (items: Array<{ x: number; y: number }>) => {
      const output: Array<{ x: number; y: number }> = [];
      for (const point of items) {
        while (output.length >= 2 && cross(output.at(-2)!, output.at(-1)!, point) <= 0)
          output.pop();
        output.push(point);
      }
      output.pop();
      return output;
    };
    return half(sorted).concat(half([...sorted].reverse()));
  }

  function smoothHull(
    points: Array<{ x: number; y: number }>,
    padding = 10
  ): Array<[number, number]> {
    if (!map || points.length < 3) return [];
    const centerPoint = points.reduce(
      (sum, point) => ({ x: sum.x + point.x / points.length, y: sum.y + point.y / points.length }),
      { x: 0, y: 0 }
    );
    const inflated = convexHull(points).map((point) => {
      const dx = point.x - centerPoint.x;
      const dy = point.y - centerPoint.y;
      const length = Math.hypot(dx, dy) || 1;
      return { x: point.x + (dx / length) * padding, y: point.y + (dy / length) * padding };
    });
    const output: Array<{ x: number; y: number }> = [];
    for (let index = 0; index < inflated.length; index++) {
      const previous = inflated[(index - 1 + inflated.length) % inflated.length];
      const current = inflated[index];
      const next = inflated[(index + 1) % inflated.length];
      const previousLength = Math.hypot(previous.x - current.x, previous.y - current.y) || 1;
      const nextLength = Math.hypot(next.x - current.x, next.y - current.y) || 1;
      const radius = Math.min(14, previousLength / 2, nextLength / 2);
      const start = {
        x: current.x + ((previous.x - current.x) / previousLength) * radius,
        y: current.y + ((previous.y - current.y) / previousLength) * radius,
      };
      const end = {
        x: current.x + ((next.x - current.x) / nextLength) * radius,
        y: current.y + ((next.y - current.y) / nextLength) * radius,
      };
      for (let sample = 0; sample <= 6; sample++) {
        const t = sample / 6;
        const u = 1 - t;
        output.push({
          x: u * u * start.x + 2 * u * t * current.x + t * t * end.x,
          y: u * u * start.y + 2 * u * t * current.y + t * t * end.y,
        });
      }
    }
    const ring = output.map((point) => {
      const lngLat = map!.unproject([point.x, point.y]);
      return [lngLat.lng, lngLat.lat] as [number, number];
    });
    if (ring.length) ring.push(ring[0]);
    return ring;
  }

  function drawMap() {
    if (!map || !maplibre || !ready) return;
    markers.forEach((marker) => marker.remove());
    markers = [];
    detailRegistry.clear();
    pulsePaths = [];

    const bucketsMax = Math.max(buckets.length, 1);
    source("routing-buckets")?.setData(
      featureCollection(
        buckets.map((bucket, index) => {
          const ring = bucket.coords.map(([lat, lng]) => [lng, lat]);
          if (ring.length) ring.push(ring[0]);
          const detailID = `bucket:${index}`;
          const rows = [
            detailRow("Type", bucket.kind === "client" ? "Viewer Bucket" : "Node Bucket"),
          ];
          if (bucket.stats?.count != null) rows.push(detailRow("Events", `${bucket.stats.count}`));
          if (bucket.stats?.successRate != null)
            rows.push(detailRow("Success", `${(bucket.stats.successRate * 100).toFixed(1)}%`));
          if (bucket.stats?.avgDistance != null)
            rows.push(detailRow("Avg Distance", `${bucket.stats.avgDistance.toFixed(0)}km`));
          detailRegistry.set(detailID, { title: rows[0].value, rows });
          return {
            id: bucket.id,
            type: "Feature" as const,
            properties: {
              bucketID: bucket.id,
              kind: bucket.kind,
              opacity: 0.12 + (Math.log1p(index + 1) / Math.log1p(bucketsMax)) * 0.35,
              detailID,
            },
            geometry: { type: "Polygon" as const, coordinates: [ring] },
          };
        })
      )
    );
    source("routing-flows")?.setData(
      featureCollection(
        flows.map((flow) => ({
          type: "Feature",
          properties: { color: flow.color || "rgba(187,154,247,0.5)", weight: flow.weight || 1.2 },
          geometry: {
            type: "LineString",
            coordinates: [
              [flow.from[1], flow.from[0]],
              [flow.to[1], flow.to[0]],
            ],
          },
        }))
      )
    );

    const byNode = servicesByNode();
    const nodesByCluster: Record<string, NodeLocation[]> = {};
    const nodeCounts: Record<string, { core: number; edge: number }> = {};
    const servicesByCluster: Record<string, string[]> = {};
    const workloadsByNode: Record<string, ClusterWorkload[]> = {};
    const workloadsByCluster: Record<string, ClusterWorkload[]> = {};
    for (const workload of workloads) {
      (workloadsByNode[workload.nodeId] ??= []).push(workload);
      (workloadsByCluster[workload.clusterId] ??= []).push(workload);
    }
    for (const node of nodes) {
      if (!node.clusterId) continue;
      (nodesByCluster[node.clusterId] ??= []).push(node);
      nodeCounts[node.clusterId] ??= { core: 0, edge: 0 };
      const type = (node.nodeType || "").toLowerCase();
      if (type === "core") nodeCounts[node.clusterId].core++;
      else if (type === "edge") nodeCounts[node.clusterId].edge++;
      servicesByCluster[node.clusterId] ??= [];
      for (const service of byNode[node.id] || []) {
        if (!servicesByCluster[node.clusterId].includes(service))
          servicesByCluster[node.clusterId].push(service);
      }
    }
    Object.values(servicesByCluster).forEach((services) => services.sort());

    const nodesByID = new SvelteMap(nodes.map((node) => [node.id, node]));
    const clustersByID = new SvelteMap(clusters.map((cluster) => [cluster.id, cluster]));
    source("routing-workloads")?.setData(
      featureCollection(
        Object.entries(workloadsByNode).flatMap(([nodeID, nodeWorkloads], index) => {
          const node = nodesByID.get(nodeID);
          const cluster = clustersByID.get(nodeWorkloads[0]?.clusterId ?? "");
          const location = node ?? cluster;
          if (!location || !Number.isFinite(location.lat) || !Number.isFinite(location.lng))
            return [];
          const dominant = [...nodeWorkloads].sort(
            (left, right) => right.eventCount - left.eventCount
          )[0];
          const magnitude = nodeWorkloads.reduce(
            (sum, item) => sum + item.eventCount + item.activeCount + item.errorCount,
            0
          );
          const detailID = `workload:${index}`;
          const section = workloadSection(nodeWorkloads);
          detailRegistry.set(detailID, {
            title: node?.name ?? nodeID,
            rows: [detailRow("Node", nodeID, true), detailRow("Cluster", dominant.clusterId, true)],
            sections: section ? [section] : [],
          });
          return [
            {
              type: "Feature" as const,
              properties: {
                radius: Math.min(30, 7 + Math.log10(magnitude + 1) * 4),
                color: workloadColor(dominant.workKind),
                detailID,
              },
              geometry: {
                type: "Point" as const,
                coordinates: [location.lng, location.lat],
              },
            },
          ];
        })
      )
    );

    const nodeMarkers: Record<string, Marker> = {};
    const aggregateMarkers: Record<string, Marker> = {};
    const clusterMarkers: Record<string, Marker> = {};
    const spreadables: Spreadable[] = [];
    const collapsed = new SvelteSet<string>();
    if (map.getZoom() <= 3) {
      for (const [clusterID, group] of Object.entries(nodesByCluster)) {
        if (group.length < 4) continue;
        const lat = group.reduce((sum, node) => sum + node.lat, 0) / group.length;
        const lng = group.reduce((sum, node) => sum + node.lng, 0) / group.length;
        const size = Math.max(24, Math.min(42, 18 + group.length * 3));
        const role = group.some((node) => serviceRole(byNode[node.id]) === "compute")
          ? "compute"
          : group.some((node) => (node.nodeType || "").toLowerCase() === "core")
            ? "core"
            : "media";
        const color = roleColor(
          role,
          group.some((node) => (node.status || "active") === "active") ? "active" : "offline"
        );
        const element = createMarkerElement(
          `<span class="aggregate-shape" style="width:${size}px;height:${size}px;--node-color:${color}">${group.length}</span>`,
          `${group.length} nodes`
        );
        element.addEventListener(
          "click",
          () => (selectedDetail = aggregateNodeDetail(group, byNode, workloadsByNode))
        );
        const marker = new maplibre.Marker({ element, anchor: "center" })
          .setLngLat([lng, lat])
          .addTo(map);
        aggregateMarkers[clusterID] = marker;
        markers.push(marker);
        spreadables.push({ marker, iconRadius: size / 2 });
        group.forEach((node) => collapsed.add(node.id));
      }
    }

    for (const node of nodes) {
      if (collapsed.has(node.id)) continue;
      const services = byNode[node.id];
      const compute = serviceRole(services) === "compute";
      const color = roleColor(nodeRole(node, services), node.status);
      const type = (node.nodeType || "").toLowerCase();
      const core = !compute && (type === "core" || type === "central");
      const size = compute ? 9 : core ? 14 : 10;
      const html = compute
        ? `<span class="node-shape node-shape--ring" style="width:${size}px;height:${size}px;--node-color:${color};box-shadow:0 0 7px ${color}"></span>`
        : core
          ? `<span class="shape-wrap shape-wrap--glow" style="--glow-color:${color}"><span class="node-shape node-shape--diamond" style="width:${size}px;height:${size}px;--node-color:${color}"></span></span>`
          : `<span class="node-shape node-shape--circle" style="width:${size}px;height:${size}px;--node-color:${color};box-shadow:0 0 6px ${color}"></span>`;
      const element = createMarkerElement(html, node.name);
      element.addEventListener(
        "click",
        () => (selectedDetail = nodeDetail(node, services, workloadsByNode[node.id] ?? []))
      );
      const marker = new maplibre.Marker({ element, anchor: "center" })
        .setLngLat([node.lng, node.lat])
        .addTo(map);
      nodeMarkers[node.id] = marker;
      markers.push(marker);
      spreadables.push({ marker, iconRadius: size / 2 });
    }

    for (const cluster of clusters) {
      const color = roleColor(cluster.clusterType, cluster.status);
      const radius = Math.max(10, Math.min(24, 10 + cluster.nodeCount * 2));
      const size = radius * 2;
      const core = ["central", "core"].includes((cluster.clusterType || "").toLowerCase());
      const html = core
        ? `<span class="cluster-shape cluster-shape--core cluster-marker--glow" style="width:${size}px;height:${size}px;--node-color:${color}">${cluster.nodeCount}</span>`
        : `<span class="cluster-shape cluster-shape--edge" style="width:${size}px;height:${size}px;--node-color:${color}"><svg class="cluster-shape__hex" viewBox="0 0 100 100" preserveAspectRatio="none"><polygon points="50,6 92,30 92,70 50,94 8,70 8,30" fill="color-mix(in srgb, ${color} 22%, rgba(15,23,42,0.7))" stroke="${color}" stroke-width="3" stroke-dasharray="6 4" stroke-linejoin="round" stroke-linecap="round"/></svg><span class="cluster-shape__count" style="color:${color}">${cluster.nodeCount}</span></span>`;
      const element = createMarkerElement(html, cluster.name);
      element.addEventListener(
        "click",
        () =>
          (selectedDetail = clusterDetail(
            cluster,
            nodeCounts[cluster.id] || { core: 0, edge: 0 },
            servicesByCluster[cluster.id] || [],
            workloadsByCluster[cluster.id] || []
          ))
      );
      const marker = new maplibre.Marker({ element, anchor: "center" })
        .setLngLat([cluster.lng, cluster.lat])
        .addTo(map);
      clusterMarkers[cluster.id] = marker;
      markers.push(marker);
      spreadables.push({ marker, iconRadius: radius });
    }

    if (showOrchestrators) {
      const deduped = new SvelteMap<string, OrchestratorVantagePin>();
      for (const vantage of orchestratorVantages) {
        if (
          !vantage.dialedRecently ||
          !Number.isFinite(vantage.lat) ||
          !Number.isFinite(vantage.lng) ||
          (vantage.lat === 0 && vantage.lng === 0)
        )
          continue;
        const key = `${vantage.orchAddr}:${vantage.resolvedIp}`;
        const current = deduped.get(key);
        if (
          !current ||
          vantage.latestLatencyMs < current.latestLatencyMs ||
          (vantage.latestLatencyMs === current.latestLatencyMs && vantage.score > current.score)
        )
          deduped.set(key, vantage);
      }
      const visible = [...deduped.values()];
      const size = map.getZoom() <= 3 ? 8 : map.getZoom() <= 5 ? 11 : 14;
      const glow = map.getZoom() <= 3 ? 2 : map.getZoom() <= 5 ? 4 : 6;
      for (const vantage of visible) {
        const color =
          vantage.latestLatencyMs >= 750
            ? "rgb(74,111,91)"
            : vantage.latestLatencyMs >= 250
              ? "rgb(45,150,96)"
              : "rgb(158,206,106)";
        const element = createMarkerElement(
          `<span class="shape-wrap" style="filter:drop-shadow(0 0 ${glow}px ${color})"><span class="orch-triangle" style="width:${size}px;height:${size}px;--glow-color:${color}"></span></span>`,
          "Livepeer orchestrator"
        );
        const peerRows = visible
          .filter((candidate) => candidate.orchAddr === vantage.orchAddr)
          .sort((left, right) => left.resolvedIp.localeCompare(right.resolvedIp))
          .map((candidate) =>
            detailRow(
              candidate.resolvedIp || "unknown",
              `${candidate.gatewayId} (${candidate.gatewayRegion}) · ${candidate.latestLatencyMs} ms`
            )
          );
        element.addEventListener("click", () => {
          selectedDetail = {
            title: "Orchestrator",
            rows: [
              detailRow("Orch", vantage.orchAddr, true),
              detailRow("IP", vantage.resolvedIp),
              detailRow("Gateway", `${vantage.gatewayId} (${vantage.gatewayRegion})`),
              detailRow("Latency", `${vantage.latestLatencyMs} ms`),
              detailRow("Score", vantage.score.toFixed(2)),
            ],
            sections: peerRows.length ? [{ title: "Observed Instances", rows: peerRows }] : [],
          };
          onOrchestratorClick?.(vantage.orchAddr);
        });
        const marker = new maplibre.Marker({ element, anchor: "center" })
          .setLngLat([vantage.lng, vantage.lat])
          .addTo(map);
        markers.push(marker);
        if (map.getZoom() >= 5) spreadables.push({ marker, iconRadius: size / 2 });
      }
    }

    spreadOverlappingMarkers(map, spreadables, {
      groupThresholdMultiplier: map.getZoom() >= 6 ? 1.55 : 2.15,
      maxExpandedGroupSize: 24,
      denseStepScale: 0.82,
    });

    const membershipFeatures: Feature<Geometry>[] = [];
    for (const [clusterID, group] of Object.entries(nodesByCluster)) {
      const clusterMarker = clusterMarkers[clusterID];
      if (!clusterMarker) continue;
      const aggregateMarker = aggregateMarkers[clusterID];
      if (aggregateMarker) {
        const from = markerLatLng(aggregateMarker, [group[0].lat, group[0].lng]);
        const to = markerLatLng(clusterMarker, from);
        if (from[0] !== to[0] || from[1] !== to[1]) {
          membershipFeatures.push({
            type: "Feature",
            properties: {
              color: withAlpha(
                roleColor(nodeRole(group[0], byNode[group[0].id]), group[0].status),
                0.42
              ),
              weight: 2,
            },
            geometry: {
              type: "LineString",
              coordinates: [
                [from[1], from[0]],
                [to[1], to[0]],
              ],
            },
          });
        }
        continue;
      }
      const visible = group.filter((node) => nodeMarkers[node.id]);
      const lineFor = (node: NodeLocation) => {
        const from = markerLatLng(nodeMarkers[node.id], [node.lat, node.lng]);
        const to = markerLatLng(clusterMarker, from);
        if (from[0] === to[0] && from[1] === to[1]) return;
        membershipFeatures.push({
          type: "Feature",
          properties: {
            color: withAlpha(roleColor(nodeRole(node, byNode[node.id]), node.status), 0.3),
            weight: 1.4,
          },
          geometry: {
            type: "LineString",
            coordinates: [
              [from[1], from[0]],
              [to[1], to[0]],
            ],
          },
        });
      };
      const points = [clusterMarker, ...visible.map((node) => nodeMarkers[node.id])].map((marker) =>
        map!.project(marker.getLngLat())
      );
      const xs = points.map((point) => point.x);
      const ys = points.map((point) => point.y);
      const major = Math.max(Math.max(...xs) - Math.min(...xs), Math.max(...ys) - Math.min(...ys));
      const minor = Math.max(
        1,
        Math.min(Math.max(...xs) - Math.min(...xs), Math.max(...ys) - Math.min(...ys))
      );
      if (points.length < 3 || major > 360 || major / minor > 4 || (major > 220 && minor < 56)) {
        visible.forEach(lineFor);
      } else {
        const cluster = clusters.find((item) => item.id === clusterID);
        const color = roleColor(cluster?.clusterType, cluster?.status);
        membershipFeatures.push({
          type: "Feature",
          properties: { color, outline: withAlpha(color, 0.5), weight: 1 },
          geometry: { type: "Polygon", coordinates: [smoothHull(points)] },
        });
      }
    }
    source("routing-membership")?.setData(featureCollection(membershipFeatures));

    const relationshipFeatures: Feature<Geometry>[] = relationships.map((relationship, index) => {
      const from = relationship.sourceClusterId
        ? markerLatLng(clusterMarkers[relationship.sourceClusterId], relationship.from)
        : relationship.from;
      const to = relationship.targetClusterId
        ? markerLatLng(clusterMarkers[relationship.targetClusterId], relationship.to)
        : relationship.to;
      const count = relationship.metrics?.eventCount ?? 0;
      const trafficWeight = Math.min(5, 1.2 + Math.log10(1 + count) * 1.1);
      const weight = relationship.active
        ? Math.max(trafficWeight, Math.min(4, (relationship.weight ?? 1) * 0.5))
        : 1.2;
      const opacity = relationship.active
        ? 0.55 + 0.35 * (relationship.metrics?.successRate ?? 1)
        : 0.4;
      const detailID = `relationship:${index}`;
      const rows = [detailRow("Type", relationship.type)];
      if (relationship.metrics?.eventCount != null)
        rows.push(detailRow("Events", relationship.metrics.eventCount.toLocaleString()));
      if (relationship.metrics?.avgLatencyMs != null)
        rows.push(detailRow("Latency", `${relationship.metrics.avgLatencyMs.toFixed(1)}ms`));
      if (relationship.metrics?.successRate != null)
        rows.push(detailRow("Success", `${(relationship.metrics.successRate * 100).toFixed(1)}%`));
      detailRegistry.set(detailID, { title: "Topology Link", rows });
      if (
        relationship.active &&
        ["peering", "assignment", "replication"].includes(relationship.type)
      ) {
        pulsePaths.push({
          from,
          to,
          color: relationship.type === "peering" ? "rgb(125,207,255)" : "rgb(192,132,252)",
        });
      }
      return {
        type: "Feature",
        properties: {
          type: relationship.type,
          color: relationshipColor(relationship.type),
          weight,
          opacity,
          detailID,
        },
        geometry: {
          type: "LineString",
          coordinates: samplePath(from, to).map(([lat, lng]) => [lng, lat]),
        },
      };
    });
    source("routing-relationships")?.setData(featureCollection(relationshipFeatures));

    const routeFeatures: Feature<Geometry>[] = [];
    const clientFeatures: Feature<Geometry>[] = [];
    routes.forEach((route, index) => {
      const success = route.status === "success" || route.status === "SUCCESS";
      const detailID = `route:${index}`;
      const rows = [detailRow("Status", success ? "Success" : "Failed")];
      if (route.score != null) rows.push(detailRow("Score", `${route.score}`));
      if (route.details) rows.push(detailRow("Details", route.details));
      detailRegistry.set(detailID, { title: "Route", rows });
      routeFeatures.push({
        type: "Feature",
        properties: { success, detailID },
        geometry: {
          type: "LineString",
          coordinates: [
            [route.from[1], route.from[0]],
            [route.to[1], route.to[0]],
          ],
        },
      });
      const clientDetailID = `client:${index}`;
      detailRegistry.set(clientDetailID, {
        title: "Viewer",
        rows: [
          detailRow("Status", success ? "Success" : "Failed"),
          ...(route.score != null ? [detailRow("Score", `${route.score}`)] : []),
          detailRow("Location", `${route.from[0].toFixed(2)}, ${route.from[1].toFixed(2)}`),
        ],
      });
      clientFeatures.push({
        type: "Feature",
        properties: { success, detailID: clientDetailID },
        geometry: { type: "Point", coordinates: [route.from[1], route.from[0]] },
      });
    });
    source("routing-routes")?.setData(featureCollection(routeFeatures));
    source("routing-clients")?.setData(featureCollection(clientFeatures));
  }

  function startPulseAnimation() {
    const reducedMotion = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    const animate = (now: number) => {
      const cycle = reducedMotion ? 0.5 : (now % 3000) / 3000;
      const opacity = reducedMotion
        ? 0.75
        : cycle < 0.1
          ? cycle / 0.1
          : cycle > 0.9
            ? (1 - cycle) / 0.1
            : 0.9;
      const features = pulsePaths.flatMap((path) =>
        [cycle, (cycle + 0.5) % 1].map((position) => {
          const [lat, lng] = pointOnPath(path.from, path.to, position);
          return {
            type: "Feature" as const,
            properties: { color: path.color, opacity },
            geometry: { type: "Point" as const, coordinates: [lng, lat] },
          };
        })
      );
      source("routing-pulses")?.setData(featureCollection(features));
      if (!reducedMotion) animationFrame = requestAnimationFrame(animate);
    };
    animate(performance.now());
  }

  $effect(() => {
    void routes;
    void nodes;
    void buckets;
    void flows;
    void clusters;
    void relationships;
    void serviceInstances;
    void workloads;
    void orchestratorVantages;
    void showOrchestrators;
    if (ready) drawMap();
  });

  function toggleFullscreen() {
    isFullscreen = !isFullscreen;
    setTimeout(() => map?.resize(), 310);
  }

  function resetView() {
    map?.easeTo({ center: [center[1], center[0]], zoom, duration: 500 });
  }

  function toggleOrchestrators(event: MouseEvent) {
    event.stopPropagation();
    showOrchestrators = !showOrchestrators;
    if (!showOrchestrators) selectedDetail = null;
  }
</script>

<div
  class="map-wrapper"
  class:map-wrapper--fullscreen={isFullscreen}
  style="height: {isFullscreen ? '100%' : `${height}px`};"
>
  {#if routes.length === 0 && nodes.length === 0 && clusters.length === 0 && workloads.length === 0}
    <div class="empty-state"><span>No routing data available</span></div>
  {/if}
  {#if unavailable}
    <div class="map-unavailable" role="status">Basemap unavailable</div>
  {/if}

  <div class="map-controls">
    <button
      class="map-control-btn"
      type="button"
      onclick={resetView}
      title="Reset view"
      disabled={!ready}
    >
      <HomeIcon class="w-4 h-4" />
    </button>
    <button
      class="map-control-btn"
      type="button"
      class:map-control-btn--active={showOrchestrators}
      onclick={toggleOrchestrators}
      title={showOrchestrators ? "Hide Livepeer compute" : "Show Livepeer compute"}
      disabled={!ready}
    >
      <CpuIcon class="w-4 h-4" />
    </button>
    <button
      class="map-control-btn"
      type="button"
      onclick={toggleFullscreen}
      title={isFullscreen ? "Exit fullscreen" : "Fullscreen"}
    >
      {#if isFullscreen}<MinimizeIcon class="w-4 h-4" />{:else}<MaximizeIcon class="w-4 h-4" />{/if}
    </button>
  </div>

  {#if showScrollHint && !isFullscreen && ready}
    <button class="scroll-hint" type="button" onclick={() => (showScrollHint = false)}>
      Hold <kbd>⌥</kbd> or <kbd>Ctrl</kbd> + scroll to zoom
    </button>
  {/if}

  <div bind:this={mapContainer} class="map-container"></div>

  {#if selectedDetail}
    <aside class="map-detail-panel" aria-label="Map selection details">
      <button
        class="map-detail-panel__close"
        type="button"
        onclick={() => (selectedDetail = null)}
        aria-label="Close details">×</button
      >
      <div class="map-detail-panel__body">
        <div class="map-popup">
          <div class="map-popup__title">{selectedDetail.title}</div>
          <table class="map-popup__table">
            <tbody>
              {#each selectedDetail.rows as row, index (`${row.label}:${row.value}:${index}`)}
                <tr>
                  <td class="map-popup__label">{row.label}</td>
                  <td class="map-popup__value"
                    >{#if row.code}<code class="map-popup__code">{row.value}</code
                      >{:else}{row.value}{/if}</td
                  >
                </tr>
              {/each}
            </tbody>
          </table>
          {#each selectedDetail.sections ?? [] as section, sectionIndex (`${section.title}:${sectionIndex}`)}
            <div class="map-popup__section-title">{section.title}</div>
            <table class="map-popup__table">
              <tbody>
                {#each section.rows as row, rowIndex (`${section.title}:${row.label}:${rowIndex}`)}
                  <tr
                    ><td class="map-popup__label">{row.label}</td><td class="map-popup__value"
                      >{row.value}</td
                    ></tr
                  >
                {/each}
              </tbody>
            </table>
          {/each}
          {#if selectedDetail.tags?.length}
            <div class="map-popup__tags">
              {#each selectedDetail.tags as tag, tagIndex (`${tag}:${tagIndex}`)}<span
                  class="map-popup__tag">{tag}</span
                >{/each}
            </div>
          {/if}
          {#if selectedDetail.description}<div class="map-popup__desc">
              {selectedDetail.description}
            </div>{/if}
        </div>
      </div>
    </aside>
  {/if}
</div>

<style>
  .map-wrapper {
    position: relative;
    width: 100%;
    overflow: hidden;
    border-radius: 0.5rem;
    background: rgb(22, 22, 30);
    transition: all 0.3s ease;
  }
  .map-wrapper--fullscreen {
    position: fixed;
    inset: 0;
    z-index: 50;
    height: 100% !important;
    border-radius: 0;
  }
  .map-container {
    width: 100%;
    height: 100%;
    z-index: 1;
  }
  .map-wrapper :global(.maplibregl-map) {
    background: rgb(22, 22, 30) !important;
    font-family: inherit;
  }
  .map-wrapper :global(.frameworks-map-attribution) {
    padding: 0.1rem 0.3rem;
    border-radius: 0.2rem;
    background: rgb(22 22 30 / 78%);
    color: rgb(169 177 214 / 82%);
    font-size: 0.58rem;
    line-height: 1.25;
  }
  .map-wrapper :global(.frameworks-map-attribution a) {
    color: inherit;
    text-decoration: none;
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
    padding: 0;
    border: 1px solid rgba(59, 66, 97, 0.6);
    border-radius: 0.375rem;
    background: rgba(36, 40, 59, 0.9);
    color: rgb(169, 177, 214);
    cursor: pointer;
  }
  .map-control-btn:hover {
    background: rgba(59, 66, 97, 0.9);
    color: rgb(192, 202, 245);
  }
  .map-control-btn--active {
    border-color: rgba(158, 206, 106, 0.65);
    color: rgb(134, 239, 172);
    box-shadow: 0 0 0 1px rgba(158, 206, 106, 0.18);
  }
  .map-control-btn:disabled {
    cursor: not-allowed;
    opacity: 0.5;
  }
  .scroll-hint {
    position: absolute;
    bottom: 0.75rem;
    left: 50%;
    transform: translateX(-50%);
    z-index: 15;
    padding: 0.375rem 0.75rem;
    border: 1px solid rgba(59, 66, 97, 0.6);
    border-radius: 0.375rem;
    background: rgba(36, 40, 59, 0.95);
    color: rgb(169, 177, 214);
    font-size: 0.75rem;
    cursor: pointer;
  }
  .scroll-hint kbd {
    padding: 0.125rem 0.375rem;
    border-radius: 0.25rem;
    background: rgba(59, 66, 97, 0.8);
    font: inherit;
  }
  .empty-state,
  .map-unavailable {
    position: absolute;
    inset: 0;
    z-index: 10;
    display: grid;
    place-items: center;
    pointer-events: none;
    background: rgb(22 22 30 / 55%);
    color: rgb(169, 177, 214);
    font-size: 0.8rem;
  }
  .map-unavailable {
    z-index: 11;
    background: rgb(22, 22, 30);
  }
  .map-detail-panel {
    position: absolute;
    top: 0.75rem;
    right: 3.25rem;
    bottom: 0.75rem;
    z-index: 25;
    width: min(360px, calc(100% - 4.75rem));
    overflow: hidden;
    border: 1px solid rgba(59, 66, 97, 0.78);
    border-radius: 8px;
    background: rgba(22, 22, 30, 0.94);
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
    background: rgba(36, 40, 59, 0.92);
    color: rgb(169, 177, 214);
    cursor: pointer;
  }
  .map-detail-panel__body {
    height: 100%;
    overflow-y: auto;
    overscroll-behavior: contain;
  }
  .map-popup {
    padding: 0.75rem 1rem;
  }
  .map-popup__title {
    margin-bottom: 0.5rem;
    padding-bottom: 0.4rem;
    border-bottom: 1px solid rgba(59, 66, 97, 0.6);
    color: rgb(241, 245, 249);
    font-size: 0.85rem;
    font-weight: 600;
  }
  .map-popup__table {
    width: 100%;
    border-collapse: collapse;
    font-size: 0.78rem;
  }
  .map-popup__table tr + tr {
    border-top: 1px solid rgba(59, 66, 97, 0.3);
  }
  .map-popup__label {
    padding: 0.2rem 0.75rem 0.2rem 0;
    color: rgb(169, 177, 214);
    white-space: nowrap;
    vertical-align: top;
  }
  .map-popup__value {
    padding: 0.2rem 0;
    color: rgb(192, 202, 245);
    text-align: right;
    overflow-wrap: anywhere;
  }
  .map-popup__code {
    display: block;
    max-width: 100%;
    white-space: normal;
    word-break: break-all;
  }
  .map-popup__section-title {
    margin: 0.6rem 0 0.25rem;
    color: rgb(100, 116, 139);
    font-size: 0.7rem;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }
  .map-popup__tags {
    display: flex;
    flex-wrap: wrap;
    gap: 0.25rem;
    margin-top: 0.5rem;
  }
  .map-popup__tag {
    padding: 0.1rem 0.4rem;
    border: 1px solid rgba(122, 162, 247, 0.3);
    border-radius: 3px;
    background: rgba(122, 162, 247, 0.15);
    color: rgb(147, 197, 253);
    font-size: 0.65rem;
  }
  .map-popup__desc {
    margin-top: 0.5rem;
    color: rgb(169, 177, 214);
    font-size: 0.75rem;
    font-style: italic;
  }
  :global(.routing-marker) {
    display: grid;
    place-items: center;
    padding: 0;
    border: 0;
    background: transparent;
    color: inherit;
    cursor: pointer;
  }
  :global(.shape-wrap) {
    display: inline-flex;
    align-items: center;
    justify-content: center;
  }
  :global(.shape-wrap--glow) {
    filter: drop-shadow(0 0 6px var(--glow-color, currentColor));
  }
  :global(.node-shape) {
    display: block;
    background: var(--node-color);
  }
  :global(.node-shape--circle) {
    border-radius: 50%;
  }
  :global(.node-shape--ring) {
    border: 2px solid var(--node-color);
    border-radius: 50%;
    background: rgba(22, 22, 30, 0.85);
  }
  :global(.node-shape--diamond) {
    clip-path: polygon(50% 0, 100% 50%, 50% 100%, 0 50%);
  }
  :global(.orch-triangle) {
    display: block;
    background: var(--glow-color);
    clip-path: polygon(50% 8%, 100% 92%, 0 92%);
  }
  :global(.aggregate-shape) {
    display: flex;
    align-items: center;
    justify-content: center;
    border: 2px solid var(--node-color);
    border-radius: 8px;
    background: color-mix(in srgb, var(--node-color) 20%, rgb(22, 22, 30));
    color: var(--node-color);
    box-shadow: 0 0 10px color-mix(in srgb, var(--node-color) 35%, transparent);
    font-size: 11px;
    font-weight: 700;
  }
  :global(.cluster-shape) {
    position: relative;
    display: flex;
    align-items: center;
    justify-content: center;
    color: var(--node-color);
    font-size: 10px;
    font-weight: 600;
  }
  :global(.cluster-shape--core) {
    border: 2px solid var(--node-color);
    border-radius: 10px;
    background: color-mix(in srgb, var(--node-color) 22%, rgba(22, 22, 30, 0.85));
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
  :global(.cluster-marker--glow) {
    animation: cluster-glow 3s ease-in-out infinite alternate;
  }
  @keyframes cluster-glow {
    from {
      filter: brightness(0.9);
    }
    to {
      filter: brightness(1.15);
    }
  }
  @media (prefers-reduced-motion: reduce) {
    :global(.cluster-marker--glow) {
      animation: none;
    }
  }
</style>

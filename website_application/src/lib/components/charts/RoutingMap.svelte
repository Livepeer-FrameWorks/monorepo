<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import type { Map, LayerGroup } from "leaflet";
  import { getIconComponent } from "$lib/iconUtils";
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
  }

  interface RelationshipLine {
    from: [number, number];
    to: [number, number];
    type: "peering" | "traffic" | "replication";
    active: boolean;
    weight?: number;
    metrics?: {
      eventCount?: number;
      avgLatencyMs?: number;
      successRate?: number;
    };
  }

  interface Props {
    routes: Route[];
    nodes: NodeLocation[];
    buckets?: BucketPolygon[];
    onBucketClick?: (id: string) => void;
    flows?: Flow[];
    clusters?: ClusterMarker[];
    relationships?: RelationshipLine[];
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
    height = 500,
    zoom = 2,
    center = [20, 0],
    tileLayerUrl = "https://{s}.basemaps.cartocdn.com/dark_all/{z}/{x}/{y}{r}.png",
  }: Props = $props();

  let mapContainer = $state<HTMLElement>();
  let mapWrapper = $state<HTMLElement>();
  let map: Map | null = null;
  let layerGroup: LayerGroup | null = null;
  let bucketLayer: LayerGroup | null = null;
  let flowLayer: LayerGroup | null = null;
  let clusterLayer: LayerGroup | null = null;
  let relationshipLayer: LayerGroup | null = null;
  let membershipLayer: LayerGroup | null = null;
  let pulseTimers: number[] = [];

  const MEMBERSHIP_COLOR = "rgba(148, 163, 184, 0.15)";
  const NODE_STATUS_COLORS: Record<string, string> = {
    active: "rgb(59, 130, 246)",
    offline: "rgb(100, 116, 139)",
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
    clusterLayer = L.layerGroup().addTo(map);

    drawMap(routes, nodes, buckets, flows, clusters, relationships);
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

  function startPulse(from: [number, number], to: [number, number]) {
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
                fillColor: "rgb(125, 207, 255)",
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

    // Clean up pulse timers
    pulseTimers.forEach(clearInterval);
    pulseTimers = [];

    // Remove old layer groups from map entirely
    if (bucketLayer) map.removeLayer(bucketLayer);
    if (flowLayer) map.removeLayer(flowLayer);
    if (membershipLayer) map.removeLayer(membershipLayer);
    if (relationshipLayer) map.removeLayer(relationshipLayer);
    if (layerGroup) map.removeLayer(layerGroup);
    if (clusterLayer) map.removeLayer(clusterLayer);

    // Recreate layer groups (order = z-order)
    bucketLayer = L.layerGroup().addTo(map);
    flowLayer = L.layerGroup().addTo(map);
    membershipLayer = L.layerGroup().addTo(map);
    relationshipLayer = L.layerGroup().addTo(map);
    layerGroup = L.layerGroup().addTo(map);
    clusterLayer = L.layerGroup().addTo(map);

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
      const poly = L.polygon(b.coords, {
        color: b.kind === "client" ? "rgba(59,130,246,0.55)" : "rgba(16,185,129,0.55)",
        weight: 1,
        fillOpacity: 0.12 + intensity * 0.35,
        opacity: 0.8,
      });
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
      L.polyline([f.from, f.to], {
        color,
        weight,
        opacity: 0.7,
        smoothFactor: 1,
      }).addTo(flowLayer!);
    });

    // Build cluster lookup for membership lines
    const clusterMap: Record<string, ClusterMarker> = {};
    currentClusters.forEach((c) => {
      clusterMap[c.id] = c;
    });

    // 0b. Node-to-cluster membership lines
    currentNodes.forEach((node) => {
      if (!node.clusterId) return;
      const cluster = clusterMap[node.clusterId];
      if (!cluster) return;
      const from: [number, number] = [node.lat, node.lng];
      const to: [number, number] = [cluster.lat, cluster.lng];
      if (from[0] === to[0] && from[1] === to[1]) return;

      L.polyline([from, to], {
        color: MEMBERSHIP_COLOR,
        weight: 1,
        opacity: 0.4,
        smoothFactor: 1,
        interactive: false,
      }).addTo(membershipLayer!);
    });

    // 1. Draw Infrastructure Nodes
    const nodeMap: Record<string, NodeLocation> = {};
    currentNodes.forEach((node) => {
      nodeMap[node.id] = node;
      const color = NODE_STATUS_COLORS[node.status ?? "active"] || NODE_STATUS_COLORS.active;

      const nodeIcon = L.divIcon({
        className: "node-dot-marker",
        html: `<div style="background-color: ${color}; width: 12px; height: 12px; border-radius: 50%; box-shadow: 0 0 10px ${color};"></div>`,
        iconSize: [12, 12],
        iconAnchor: [6, 6],
      });

      L.marker([node.lat, node.lng], { icon: nodeIcon })
        .bindTooltip(
          `<b>${escapeHtml(node.name)}</b><br>` +
            `${escapeHtml(node.nodeType || "node")}<br>` +
            `Status: ${escapeHtml(node.status || "active")}<br>` +
            (node.clusterId ? `Cluster: ${escapeHtml(node.clusterId)}` : ""),
          { direction: "top", className: "dark-tooltip" }
        )
        .addTo(layerGroup!);
    });

    // 2. Draw Routes (Bezier curves or straight lines)
    currentRoutes.forEach((route) => {
      const isSuccess = route.status === "success" || route.status === "SUCCESS";
      const color = isSuccess ? "rgba(34, 197, 94, 0.4)" : "rgba(239, 68, 68, 0.4)"; // Green vs Red
      const weight = 1;

      // Draw line
      L.polyline([route.from, route.to], {
        color: color,
        weight: weight,
        opacity: 0.6,
        smoothFactor: 1,
      }).addTo(layerGroup!);

      // Draw Client (Origin) dot - smaller
      const clientIcon = L.divIcon({
        className: "custom-client-icon",
        html: `<div style="background-color: ${isSuccess ? "rgb(34, 197, 94)" : "rgb(239, 68, 68)"}; width: 6px; height: 6px; border-radius: 50%;"></div>`,
        iconSize: [6, 6],
        iconAnchor: [3, 3],
      });

      L.marker(route.from, { icon: clientIcon }).addTo(layerGroup!);
    });

    // 3. Draw relationship lines between clusters
    currentRelationships.forEach((rel) => {
      const colorMap = {
        peering: "rgba(59, 130, 246, 0.6)",
        traffic: "rgba(34, 197, 94, 0.5)",
        replication: "rgba(168, 85, 247, 0.5)",
      };
      const lineColor = colorMap[rel.type] || "rgba(148, 163, 184, 0.4)";
      const lineWeight = rel.active ? Math.max(1.5, Math.min(4, (rel.weight ?? 1) * 0.5)) : 1;

      const line = L.polyline([rel.from, rel.to], {
        color: lineColor,
        weight: lineWeight,
        opacity: rel.active ? 0.7 : 0.3,
        dashArray: rel.type === "peering" ? "8 4" : undefined,
        smoothFactor: 1,
      });

      if (rel.metrics) {
        const parts: string[] = [];
        if (rel.metrics.eventCount != null) parts.push(`Events: ${rel.metrics.eventCount}`);
        if (rel.metrics.avgLatencyMs != null)
          parts.push(`Latency: ${rel.metrics.avgLatencyMs.toFixed(1)}ms`);
        if (rel.metrics.successRate != null)
          parts.push(`Success: ${(rel.metrics.successRate * 100).toFixed(1)}%`);
        if (parts.length > 0) {
          line.bindTooltip(parts.join("<br>"), {
            direction: "center",
            className: "dark-tooltip",
          });
        }
      }

      line.addTo(relationshipLayer!);

      // Pulse animation on active peering lines
      if (rel.active && rel.type === "peering") {
        startPulse(rel.from, rel.to);
      }
    });

    // 4. Draw cluster markers
    const statusColors: Record<string, string> = {
      healthy: "rgb(34, 197, 94)",
      operational: "rgb(34, 197, 94)",
      degraded: "rgb(234, 179, 8)",
      down: "rgb(239, 68, 68)",
    };

    currentClusters.forEach((cluster) => {
      const color = statusColors[cluster.status] || "rgb(148, 163, 184)";
      const radius = Math.max(10, Math.min(24, 10 + cluster.nodeCount * 2));

      const icon = L.divIcon({
        className: "cluster-marker",
        html: `<div class="cluster-marker--glow" style="
          width: ${radius * 2}px; height: ${radius * 2}px; border-radius: 50%;
          background: radial-gradient(circle, color-mix(in srgb, ${color} 25%, transparent), color-mix(in srgb, ${color} 9%, transparent));
          border: 2px solid ${color};
          display: flex; align-items: center; justify-content: center;
          font-size: 10px; font-weight: 600; color: ${color};
          box-shadow: 0 0 12px color-mix(in srgb, ${color} 30%, transparent);
          --node-color: ${color};
        ">${cluster.nodeCount}</div>`,
        iconSize: [radius * 2, radius * 2],
        iconAnchor: [radius, radius],
      });

      // Build enriched tooltip
      let tooltip =
        `<b>${escapeHtml(cluster.name)}</b><br>` +
        `${escapeHtml(cluster.clusterType || cluster.region)}<br>` +
        `Nodes: ${cluster.healthyNodeCount}/${cluster.nodeCount}<br>` +
        (cluster.peerCount != null ? `Peers: ${cluster.peerCount}<br>` : "") +
        `Status: ${escapeHtml(cluster.status)}`;

      if ((cluster.maxStreams ?? 0) > 0 || (cluster.currentStreams ?? 0) > 0) {
        tooltip +=
          `<br><br><b>Load</b><br>` +
          `Streams: ${formatLoad(cluster.currentStreams, cluster.maxStreams)}<br>` +
          `Viewers: ${formatLoad(cluster.currentViewers, cluster.maxViewers)}<br>` +
          `Bandwidth: ${formatLoad(cluster.currentBandwidthMbps, cluster.maxBandwidthMbps)} Mbps`;
      }

      if (cluster.shortDescription) {
        tooltip += `<br><br><i>${escapeHtml(cluster.shortDescription)}</i>`;
      }

      L.marker([cluster.lat, cluster.lng], { icon, zIndexOffset: 1000 })
        .bindTooltip(tooltip, { direction: "top", className: "dark-tooltip" })
        .addTo(clusterLayer!);
    });
  }

  let drawTrigger = $derived({
    routesLen: routes.length,
    nodesLen: nodes.length,
    bucketsLen: buckets.length,
    flowsLen: flows.length,
    clustersLen: clusters.length,
    relationshipsLen: relationships.length,
  });

  $effect(() => {
    const _trigger = drawTrigger;
    if (map) {
      drawMap(routes, nodes, buckets, flows, clusters, relationships);
    }
  });
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
    overflow: hidden;
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

  :global(.dark-tooltip) {
    background-color: rgb(30, 41, 59) !important;
    border: 1px solid rgb(51, 65, 85) !important;
    color: rgb(226, 232, 240) !important;
    border-radius: 4px !important;
    font-size: 0.75rem;
    line-height: 1.5;
    padding: 0.4rem 0.6rem !important;
    box-shadow: 0 4px 12px rgba(0, 0, 0, 0.4) !important;
  }

  :global(.dark-tooltip::before) {
    border-top-color: rgb(51, 65, 85) !important;
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

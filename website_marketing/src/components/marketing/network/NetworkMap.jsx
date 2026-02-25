import { useCallback, useEffect, useRef, useState, useSyncExternalStore } from "react";
import { motion } from "framer-motion";
import { useNetworkStatus } from "./useNetworkStatus";
import "leaflet/dist/leaflet.css";

const TILE_URL = "https://{s}.basemaps.cartocdn.com/dark_all/{z}/{x}/{y}{r}.png";

const CLUSTER_STATUS_COLORS = {
  healthy: "rgb(34, 197, 94)",
  degraded: "rgb(234, 179, 8)",
  unhealthy: "rgb(234, 179, 8)",
  down: "rgb(239, 68, 68)",
  unknown: "rgb(148, 163, 184)",
};

const NODE_STATUS_COLORS = {
  active: "rgb(59, 130, 246)",
  offline: "rgb(100, 116, 139)",
};

const SERVICE_HEALTH_COLORS = {
  healthy: "rgb(34, 197, 94)",
  unhealthy: "rgb(234, 179, 8)",
  unknown: "rgb(148, 163, 184)",
};

const CONNECTION_COLOR = "rgba(125, 207, 255, 0.5)";
const MEMBERSHIP_COLOR = "rgba(148, 163, 184, 0.15)";

function escapeHtml(s) {
  return s
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

function overallStatus(clusters) {
  if (clusters.every((c) => c.status === "healthy")) return "healthy";
  if (clusters.some((c) => c.status === "down")) return "down";
  return "degraded";
}

function statusLabel(status) {
  if (status === "healthy") return "OPERATIONAL";
  if (status === "degraded") return "DEGRADED";
  if (status === "down") return "DOWN";
  return "UNKNOWN";
}

function usePrefersReducedMotion() {
  const subscribe = useCallback((cb) => {
    const mq = window.matchMedia("(prefers-reduced-motion: reduce)");
    mq.addEventListener("change", cb);
    return () => mq.removeEventListener("change", cb);
  }, []);
  const getSnapshot = useCallback(
    () => window.matchMedia("(prefers-reduced-motion: reduce)").matches,
    []
  );
  return useSyncExternalStore(subscribe, getSnapshot, () => false);
}

function formatLoad(current, max) {
  if (!max) return `${current}`;
  return `${current} / ${max}`;
}

function popupRow(label, value) {
  return `<tr><td class="map-popup__label">${escapeHtml(label)}</td><td class="map-popup__value">${value}</td></tr>`;
}

function popupSection(title, rows) {
  return `<div class="map-popup__section-title">${escapeHtml(title)}</div><table class="map-popup__table">${rows}</table>`;
}

function clusterPopupHtml(cluster) {
  const infoRows =
    (cluster.region ? popupRow("Region", escapeHtml(cluster.region)) : "") +
    (cluster.clusterType ? popupRow("Type", escapeHtml(cluster.clusterType)) : "") +
    popupRow("Nodes", `${cluster.healthyNodeCount} / ${cluster.nodeCount}`) +
    popupRow("Peers", `${cluster.peerCount}`) +
    popupRow("Status", escapeHtml(cluster.status));

  let html =
    `<div class="map-popup"><div class="map-popup__title">${escapeHtml(cluster.name)}</div>` +
    `<table class="map-popup__table">${infoRows}</table>`;

  if (cluster.maxStreams > 0 || cluster.currentStreams > 0) {
    const loadRows =
      popupRow("Streams", formatLoad(cluster.currentStreams, cluster.maxStreams)) +
      popupRow("Viewers", formatLoad(cluster.currentViewers, cluster.maxViewers)) +
      popupRow(
        "Bandwidth",
        `${formatLoad(cluster.currentBandwidthMbps, cluster.maxBandwidthMbps)} Mbps`
      );
    html += popupSection("Load", loadRows);
  }

  if (cluster.services?.length) {
    html += `<div class="map-popup__tags">${cluster.services.map((s) => `<span class="map-popup__tag">${escapeHtml(s)}</span>`).join("")}</div>`;
  }

  if (cluster.shortDescription) {
    html += `<div class="map-popup__desc">${escapeHtml(cluster.shortDescription)}</div>`;
  }

  return html + "</div>";
}

function nodePopupHtml(node, services) {
  const rows =
    popupRow("Type", escapeHtml(node.nodeType)) +
    popupRow("Status", escapeHtml(node.status)) +
    popupRow("Cluster", escapeHtml(node.clusterId));
  let html =
    `<div class="map-popup"><div class="map-popup__title">${escapeHtml(node.name)}</div>` +
    `<table class="map-popup__table">${rows}</table>`;
  if (services?.length) {
    html += `<div class="map-popup__tags">${services.map((s) => `<span class="map-popup__tag">${escapeHtml(s)}</span>`).join("")}</div>`;
  }
  return html + "</div>";
}

// Draws all layers onto the Leaflet map
function drawLayers(L, layersRef, pulseTimersRef, data) {
  const {
    membership: memberLayer,
    clusters: clusterLayer,
    connections: connLayer,
    nodes: nodeLayer,
    services: serviceLayer,
    pulses: pulseLayer,
  } = layersRef.current;
  if (!clusterLayer || !connLayer || !nodeLayer || !pulseLayer) return;

  // Clear everything
  pulseTimersRef.current.forEach(clearInterval);
  pulseTimersRef.current = [];
  memberLayer?.clearLayers();
  clusterLayer.clearLayers();
  connLayer.clearLayers();
  nodeLayer.clearLayers();
  serviceLayer.clearLayers();
  pulseLayer.clearLayers();

  const clusterMap = {};
  data.clusters.forEach((c) => {
    clusterMap[c.clusterId] = c;
  });

  // Index nodes by nodeId for service instance placement
  const nodeMap = {};
  (data.nodes || []).forEach((n) => {
    nodeMap[n.nodeId] = n;
  });

  // 0. Node-to-cluster membership lines
  if (memberLayer) {
    (data.nodes || []).forEach((node) => {
      if (!node.latitude && !node.longitude) return;
      const cluster = clusterMap[node.clusterId];
      if (!cluster) return;
      const from = [node.latitude, node.longitude];
      const to = [cluster.latitude, cluster.longitude];
      if (from[0] === to[0] && from[1] === to[1]) return;

      L.polyline([from, to], {
        color: MEMBERSHIP_COLOR,
        weight: 1,
        opacity: 0.4,
        smoothFactor: 1,
        interactive: false,
      }).addTo(memberLayer);
    });
  }

  // 1. Peer connection lines
  data.peerConnections.forEach((pc) => {
    const src = clusterMap[pc.sourceCluster];
    const tgt = clusterMap[pc.targetCluster];
    if (!src || !tgt) return;

    const from = [src.latitude, src.longitude];
    const to = [tgt.latitude, tgt.longitude];

    L.polyline([from, to], {
      color: CONNECTION_COLOR,
      weight: 1.5,
      opacity: pc.connected ? 0.7 : 0.15,
      dashArray: "8 12",
      smoothFactor: 1,
    }).addTo(connLayer);

    if (pc.connected) {
      startPulse(L, pulseLayer, pulseTimersRef, from, to);
    }
  });

  // Build per-node service list from service instances
  const servicesByNode = {};
  (data.serviceInstances || []).forEach((si) => {
    if (!si.nodeId) return;
    if (!servicesByNode[si.nodeId]) servicesByNode[si.nodeId] = [];
    if (!servicesByNode[si.nodeId].includes(si.serviceId)) {
      servicesByNode[si.nodeId].push(si.serviceId);
    }
  });

  // 2. Individual nodes
  (data.nodes || []).forEach((node) => {
    if (!node.latitude && !node.longitude) return;

    const color = NODE_STATUS_COLORS[node.status] || NODE_STATUS_COLORS.offline;

    const icon = L.divIcon({
      className: "network-viz__marker",
      html: `<div class="network-viz__node-dot" style="--node-dot-color: ${color};"></div>`,
      iconSize: [12, 12],
      iconAnchor: [6, 6],
    });

    const nodeSvcs = servicesByNode[node.nodeId];

    L.marker([node.latitude, node.longitude], { icon, interactive: true })
      .bindPopup(nodePopupHtml(node, nodeSvcs), {
        className: "network-viz__popup",
        maxWidth: 400,
        minWidth: 200,
      })
      .addTo(nodeLayer);
  });

  // 3. Service instances (placed near their host node, fallback to cluster geo)
  (data.serviceInstances || []).forEach((svc) => {
    let lat, lng;
    const node = svc.nodeId ? nodeMap[svc.nodeId] : null;
    if (node && (node.latitude || node.longitude)) {
      lat = node.latitude + 0.3;
      lng = node.longitude + 0.3;
    } else {
      const cluster = clusterMap[svc.clusterId];
      if (!cluster) return;
      lat = cluster.latitude + 0.3;
      lng = cluster.longitude + 0.3;
    }

    const color = SERVICE_HEALTH_COLORS[svc.healthStatus] || SERVICE_HEALTH_COLORS.unknown;

    const icon = L.divIcon({
      className: "network-viz__marker",
      html: `<div class="network-viz__service-dot" style="--svc-color: ${color};"></div>`,
      iconSize: [8, 8],
      iconAnchor: [4, 4],
    });

    const svcRows =
      popupRow("Health", escapeHtml(svc.healthStatus)) +
      popupRow(svc.nodeId ? "Node" : "Cluster", escapeHtml(svc.nodeId || svc.clusterId));
    L.marker([lat, lng], { icon, interactive: true })
      .bindPopup(
        `<div class="map-popup"><div class="map-popup__title">${escapeHtml(svc.serviceId)}</div><table class="map-popup__table">${svcRows}</table></div>`,
        { className: "network-viz__popup", maxWidth: 300, minWidth: 180 }
      )
      .addTo(serviceLayer);
  });

  // 4. Cluster markers (on top)
  data.clusters.forEach((cluster) => {
    const color = CLUSTER_STATUS_COLORS[cluster.status] || CLUSTER_STATUS_COLORS.unknown;
    const radius = Math.max(10, Math.min(24, 10 + cluster.nodeCount * 2));

    const icon = L.divIcon({
      className: "network-viz__marker",
      html: `<div class="network-viz__node" style="
        width: ${radius * 2}px; height: ${radius * 2}px;
        --node-color: ${color};
      ">${cluster.nodeCount}</div>`,
      iconSize: [radius * 2, radius * 2],
      iconAnchor: [radius, radius],
    });

    L.marker([cluster.latitude, cluster.longitude], {
      icon,
      interactive: true,
      zIndexOffset: 1000,
    })
      .bindPopup(clusterPopupHtml(cluster), {
        className: "network-viz__popup",
        maxWidth: 400,
        minWidth: 220,
      })
      .addTo(clusterLayer);
  });
}

function startPulse(L, layer, pulseTimersRef, from, to) {
  const steps = 60;
  const interval = 50;

  function createPulse(delay) {
    let step = 0;
    let marker = null;

    const timerId = setTimeout(() => {
      const id = setInterval(() => {
        const t = step / steps;
        const lat = from[0] + (to[0] - from[0]) * t;
        const lng = from[1] + (to[1] - from[1]) * t;

        if (!marker) {
          marker = L.circleMarker([lat, lng], {
            radius: 3,
            fillColor: "rgb(125, 207, 255)",
            fillOpacity: 0.9,
            stroke: false,
            className: "network-viz__pulse",
            interactive: false,
          }).addTo(layer);
        } else {
          marker.setLatLng([lat, lng]);
        }

        const opacity = t < 0.1 ? t / 0.1 : t > 0.9 ? (1 - t) / 0.1 : 0.9;
        marker.setStyle({ fillOpacity: opacity });

        step++;
        if (step > steps) {
          step = 0;
        }
      }, interval);

      pulseTimersRef.current.push(id);
    }, delay);

    pulseTimersRef.current.push(timerId);
  }

  createPulse(0);
  createPulse(1500);
}

const ICON_MAXIMIZE = `<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="15 3 21 3 21 9"/><polyline points="9 21 3 21 3 15"/><line x1="21" y1="3" x2="14" y2="10"/><line x1="3" y1="21" x2="10" y2="14"/></svg>`;
const ICON_MINIMIZE = `<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="4 14 10 14 10 20"/><polyline points="20 10 14 10 14 4"/><line x1="14" y1="10" x2="21" y2="3"/><line x1="3" y1="21" x2="10" y2="14"/></svg>`;
const ICON_HOME = `<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 9l9-7 9 7v11a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/><polyline points="9 22 9 12 15 12 15 22"/></svg>`;

function NetworkMapInner({ data }) {
  const containerRef = useRef(null);
  const wrapperRef = useRef(null);
  const mapRef = useRef(null);
  const leafletRef = useRef(null);
  const layersRef = useRef({
    membership: null,
    clusters: null,
    connections: null,
    nodes: null,
    services: null,
    pulses: null,
  });
  const pulseTimersRef = useRef([]);
  const [mapReady, setMapReady] = useState(false);
  const [isFullscreen, setIsFullscreen] = useState(false);

  // Init map once
  useEffect(() => {
    let cancelled = false;

    import("leaflet").then((L) => {
      if (cancelled || !containerRef.current || mapRef.current) return;

      const map = L.map(containerRef.current, {
        center: [25, 10],
        zoom: 2,
        minZoom: 2,
        maxZoom: 10,
        zoomControl: false,
        attributionControl: false,
        scrollWheelZoom: false,
        doubleClickZoom: true,
        touchZoom: true,
        boxZoom: true,
        keyboard: true,
        dragging: true,
      });

      L.tileLayer(TILE_URL, { maxZoom: 19, subdomains: "abcd" }).addTo(map);

      // Modifier-key scroll zoom (same UX as webapp RoutingMap)
      containerRef.current.addEventListener(
        "wheel",
        (e) => {
          if (e.altKey || e.ctrlKey || e.metaKey) {
            e.preventDefault();
            map.scrollWheelZoom.enable();
          } else {
            map.scrollWheelZoom.disable();
          }
        },
        { passive: false }
      );

      // Layer order: membership → connections → nodes → services → pulses → clusters (on top)
      layersRef.current.membership = L.layerGroup().addTo(map);
      layersRef.current.connections = L.layerGroup().addTo(map);
      layersRef.current.nodes = L.layerGroup().addTo(map);
      layersRef.current.services = L.layerGroup().addTo(map);
      layersRef.current.pulses = L.layerGroup().addTo(map);
      layersRef.current.clusters = L.layerGroup().addTo(map);

      leafletRef.current = L;
      mapRef.current = map;
      setMapReady(true);
    });

    return () => {
      cancelled = true;
      pulseTimersRef.current.forEach(clearInterval);
      pulseTimersRef.current = [];
      if (mapRef.current) {
        mapRef.current.remove();
        mapRef.current = null;
      }
    };
  }, []);

  // Redraw when data changes or map becomes ready
  useEffect(() => {
    const L = leafletRef.current;
    if (!L || !mapRef.current) return;
    drawLayers(L, layersRef, pulseTimersRef, data);
  }, [data, mapReady]);

  const toggleFullscreen = useCallback(() => {
    setIsFullscreen((v) => !v);
    setTimeout(() => mapRef.current?.invalidateSize(), 310);
  }, []);

  const resetView = useCallback(() => {
    mapRef.current?.setView([25, 10], 2);
  }, []);

  return (
    <div
      ref={wrapperRef}
      className={`network-viz__map-wrapper${isFullscreen ? " network-viz__map-wrapper--fullscreen" : ""}`}
    >
      <div ref={containerRef} className="network-viz__map" />
      <div className="network-viz__controls">
        <button
          type="button"
          className="network-viz__control-btn"
          onClick={resetView}
          title="Reset view"
          dangerouslySetInnerHTML={{ __html: ICON_HOME }}
        />
        <button
          type="button"
          className="network-viz__control-btn"
          onClick={toggleFullscreen}
          title={isFullscreen ? "Exit fullscreen" : "Fullscreen"}
          dangerouslySetInnerHTML={{ __html: isFullscreen ? ICON_MINIMIZE : ICON_MAXIMIZE }}
        />
      </div>
      {!isFullscreen && (
        <button
          type="button"
          className="network-viz__scroll-hint"
          onClick={(e) => e.currentTarget.remove()}
        >
          Hold <kbd>⌥</kbd> or <kbd>Ctrl</kbd> + scroll to zoom
        </button>
      )}
    </div>
  );
}

export function NetworkMap() {
  const { data, loading } = useNetworkStatus();
  const prefersReducedMotion = usePrefersReducedMotion();

  if (loading || !data) return null;

  const status = overallStatus(data.clusters);
  const color = CLUSTER_STATUS_COLORS[status] || CLUSTER_STATUS_COLORS.unknown;

  return (
    <motion.div
      className={`network-viz${prefersReducedMotion ? " network-viz--reduced-motion" : ""}`}
      initial={{ opacity: 0, y: 24 }}
      whileInView={{ opacity: 1, y: 0 }}
      viewport={{ once: true }}
      transition={{ duration: 0.55, delay: 0.15 }}
    >
      <div className="network-viz__header">
        <div className="network-viz__header-left">
          <span className="network-viz__dot" style={{ background: color }} />
          <span className="network-viz__name">Live Network</span>
        </div>
        <span className="network-viz__status-badge" style={{ borderColor: color, color }}>
          {statusLabel(status)}
        </span>
      </div>

      <NetworkMapInner data={data} />

      <div className="network-viz__summary">
        <span>
          {data.healthyNodes}/{data.totalNodes} Nodes
        </span>
        <span className="network-viz__summary-sep" />
        <span>{data.clusters.length} Clusters</span>
        <span className="network-viz__summary-sep" />
        <span>{data.peerConnections.filter((p) => p.connected).length} Peered</span>
      </div>
    </motion.div>
  );
}

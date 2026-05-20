import { useCallback, useEffect, useRef, useState, useSyncExternalStore } from "react";
import { motion } from "framer-motion";
import { useNetworkStatus } from "./useNetworkStatus";
import { spreadOverlappingMarkers } from "./spreadOverlap";
import "leaflet/dist/leaflet.css";

const TILE_URL = "https://{s}.basemaps.cartocdn.com/dark_all/{z}/{x}/{y}{r}.png";

const ROLE_COLORS = {
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

const NETWORK_STATUS_COLORS = {
  healthy: "rgb(34, 197, 94)",
  degraded: "rgb(234, 179, 8)",
  down: "rgb(239, 68, 68)",
  unknown: "rgb(148, 163, 184)",
};

const ASSIGNMENT_COLOR = "rgba(168, 85, 247, 0.7)";
const FEDERATION_COLOR = "rgba(59, 130, 246, 0.7)";
const MEMBERSHIP_COLORS = {
  core: "rgba(249, 115, 22, 0.3)",
  edge: "rgba(59, 130, 246, 0.3)",
  media: "rgba(59, 130, 246, 0.3)",
  compute: "rgba(34, 197, 94, 0.3)",
  livepeer: "rgba(34, 197, 94, 0.3)",
  "livepeer-gateway": "rgba(34, 197, 94, 0.3)",
};
const UNKNOWN_GEO_ANCHOR = [-42, -145];

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

function roleColor(role, status) {
  if (status === "offline" || status === "down") return "rgb(100, 116, 139)";
  return ROLE_COLORS[(role || "").toLowerCase()] || ROLE_COLORS.default;
}

function serviceRole(services) {
  if (!services?.length) return undefined;
  if (services.some((s) => s === "livepeer-gateway" || s.startsWith("livepeer-"))) {
    return "compute";
  }
  return undefined;
}

function nodeRole(node, services) {
  return serviceRole(services) || node.nodeType || "default";
}

function withAlpha(rgb, alpha) {
  return rgb.replace("rgb(", "rgba(").replace(")", `, ${alpha})`);
}

function hashString(value) {
  let hash = 0;
  for (let i = 0; i < value.length; i++) {
    hash = (hash * 31 + value.charCodeAt(i)) | 0;
  }
  return Math.abs(hash);
}

function unknownGeoLatLng(key) {
  const h = hashString(key);
  return [UNKNOWN_GEO_ANCHOR[0] + (h % 700) / 100, UNKNOWN_GEO_ANCHOR[1] + ((h >> 4) % 1000) / 100];
}

function vantageLatLng(v) {
  const lat = Number(v.latitude ?? 0);
  const lng = Number(v.longitude ?? 0);
  if (Number.isFinite(lat) && Number.isFinite(lng) && !(lat === 0 && lng === 0)) {
    return [lat, lng];
  }
  return unknownGeoLatLng(`${v.orchAddr}:${v.resolvedIp}:${v.gatewayId}`);
}

function markerLatLng(marker, fallback) {
  if (!marker) return fallback;
  const ll = marker.getLatLng();
  return [ll.lat, ll.lng];
}

function clusterPopupHtml(cluster, nodeTypeCounts, clusterServices) {
  let infoRows =
    (cluster.region ? popupRow("Region", escapeHtml(cluster.region)) : "") +
    (cluster.clusterType ? popupRow("Type", escapeHtml(cluster.clusterType)) : "") +
    popupRow("Nodes", `${cluster.healthyNodeCount} / ${cluster.nodeCount}`) +
    popupRow("Peers", `${cluster.peerCount}`) +
    popupRow("Status", escapeHtml(cluster.status));

  if (nodeTypeCounts) {
    if (nodeTypeCounts.core > 0) infoRows += popupRow("Core Nodes", `${nodeTypeCounts.core}`);
    if (nodeTypeCounts.edge > 0) infoRows += popupRow("Edge Nodes", `${nodeTypeCounts.edge}`);
  }

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

  const services = clusterServices?.length ? clusterServices : cluster.services;
  if (services?.length) {
    html += `<div class="map-popup__tags">${services.map((s) => `<span class="map-popup__tag">${escapeHtml(s)}</span>`).join("")}</div>`;
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

function orchestratorPopupHtml(vantage) {
  const hasGeo = Number(vantage.latitude) !== 0 || Number(vantage.longitude) !== 0;
  const rows =
    popupRow("Instance IP", escapeHtml(vantage.resolvedIp || "unknown")) +
    popupRow("Gateway", escapeHtml(vantage.gatewayId || "unknown")) +
    popupRow("Region", escapeHtml(vantage.gatewayRegion || "unknown")) +
    popupRow("Latency", `${Number(vantage.latestLatencyMs || 0).toFixed(0)}ms`) +
    popupRow("Score", `${Number(vantage.score || 0).toFixed(2)}`) +
    popupRow("Geo", hasGeo ? escapeHtml(vantage.geoSource || "mmdb") : "unknown");
  return (
    `<div class="map-popup"><div class="map-popup__title">${escapeHtml(vantage.orchAddr || "Orchestrator")}</div>` +
    `<table class="map-popup__table">${rows}</table>` +
    (!hasGeo
      ? `<div class="map-popup__desc">Unknown geo: check gateway GeoIP and DNS resolution.</div>`
      : "") +
    "</div>"
  );
}

function orchestratorColor(vantage) {
  if (!vantage.dialedRecently) return ROLE_COLORS.default;
  const latency = Number(vantage.latestLatencyMs || 0);
  if (latency >= 1000) return "rgb(239, 68, 68)";
  if (latency >= 250) return "rgb(234, 179, 8)";
  return ROLE_COLORS.compute;
}

function drawLayers(L, map, layersRef, pulseTimersRef, spreadablesRef, data) {
  const {
    membership: memberLayer,
    clusters: clusterLayer,
    connections: connLayer,
    nodes: nodeLayer,
    orchestrators: orchestratorLayer,
    pulses: pulseLayer,
  } = layersRef.current;
  if (!clusterLayer || !connLayer || !nodeLayer || !orchestratorLayer || !pulseLayer) return;

  pulseTimersRef.current.forEach(clearInterval);
  pulseTimersRef.current = [];
  memberLayer?.clearLayers();
  clusterLayer.clearLayers();
  connLayer.clearLayers();
  nodeLayer.clearLayers();
  orchestratorLayer.clearLayers();
  pulseLayer.clearLayers();
  spreadablesRef.current.nodes = [];
  spreadablesRef.current.clusters = [];

  const clusterMap = {};
  data.clusters.forEach((c) => {
    clusterMap[c.clusterId] = c;
  });
  const nodeMarkersById = {};
  const clusterMarkersById = {};

  const nodeMap = {};
  (data.nodes || []).forEach((n) => {
    nodeMap[n.nodeId] = n;
  });

  const servicesByNode = {};
  (data.serviceInstances || []).forEach((si) => {
    if (!si.nodeId) return;
    if (!servicesByNode[si.nodeId]) servicesByNode[si.nodeId] = [];
    if (!servicesByNode[si.nodeId].includes(si.serviceId)) {
      servicesByNode[si.nodeId].push(si.serviceId);
    }
  });
  Object.values(servicesByNode).forEach((svcs) => svcs.sort());

  const servicesByCluster = {};
  const nodeTypeCountByCluster = {};
  (data.nodes || []).forEach((node) => {
    const cid = node.clusterId;
    if (!cid) return;
    if (!nodeTypeCountByCluster[cid]) nodeTypeCountByCluster[cid] = { core: 0, edge: 0 };
    const nt = (node.nodeType || "").toLowerCase();
    if (nt === "core") nodeTypeCountByCluster[cid].core++;
    else if (nt === "edge") nodeTypeCountByCluster[cid].edge++;
    const nodeSvcs = servicesByNode[node.nodeId] || [];
    nodeSvcs.forEach((s) => {
      if (!servicesByCluster[cid]) servicesByCluster[cid] = [];
      if (!servicesByCluster[cid].includes(s)) servicesByCluster[cid].push(s);
    });
  });
  Object.values(servicesByCluster).forEach((svcs) => svcs.sort());

  (data.nodes || []).forEach((node) => {
    if (!node.latitude && !node.longitude) return;

    const nodeSvcs = servicesByNode[node.nodeId];
    const color = roleColor(nodeRole(node, nodeSvcs), node.status);
    const isEdge = (node.nodeType || "").toLowerCase() === "edge";
    const size = isEdge ? 10 : 14;
    const glow = isEdge ? "6px" : "12px";

    const icon = L.divIcon({
      className: "network-viz__marker",
      html: `<div class="network-viz__node-dot" style="--node-dot-color: ${color}; width: ${size}px; height: ${size}px; box-shadow: 0 0 ${glow} ${color};"></div>`,
      iconSize: [size, size],
      iconAnchor: [size / 2, size / 2],
    });

    const nodeMarker = L.marker([node.latitude, node.longitude], { icon, interactive: true })
      .bindPopup(nodePopupHtml(node, nodeSvcs), {
        className: "network-viz__popup",
        maxWidth: 400,
        minWidth: 200,
      })
      .addTo(nodeLayer);
    nodeMarkersById[node.nodeId] = nodeMarker;
    spreadablesRef.current.nodes.push({ marker: nodeMarker, iconRadius: size / 2 });
  });

  data.clusters.forEach((cluster) => {
    const color = roleColor(cluster.clusterType, cluster.status);
    const radius = Math.max(10, Math.min(24, 10 + cluster.nodeCount * 2));
    const ct = (cluster.clusterType || "").toLowerCase();
    const isCore = ct === "central" || ct === "core";
    const borderRadius = isCore ? "6px" : "50%";
    const borderStyle = isCore ? `3px solid ${color}` : `2px dashed ${color}`;

    const icon = L.divIcon({
      className: "network-viz__marker",
      html: `<div style="
        width: ${radius * 2}px; height: ${radius * 2}px; border-radius: ${borderRadius};
        background: radial-gradient(circle, color-mix(in srgb, ${color} 25%, transparent), color-mix(in srgb, ${color} 9%, transparent));
        border: ${borderStyle};
        display: flex; align-items: center; justify-content: center;
        font-size: 10px; font-weight: 600; color: ${color};
        box-shadow: 0 0 12px color-mix(in srgb, ${color} 30%, transparent);
      ">${cluster.nodeCount}</div>`,
      iconSize: [radius * 2, radius * 2],
      iconAnchor: [radius, radius],
    });

    const clusterMarker = L.marker([cluster.latitude, cluster.longitude], {
      icon,
      interactive: true,
      zIndexOffset: 1000,
    })
      .bindPopup(
        clusterPopupHtml(
          cluster,
          nodeTypeCountByCluster[cluster.clusterId],
          servicesByCluster[cluster.clusterId]
        ),
        { className: "network-viz__popup", maxWidth: 400, minWidth: 220 }
      )
      .addTo(clusterLayer);
    clusterMarkersById[cluster.clusterId] = clusterMarker;
    spreadablesRef.current.clusters.push({ marker: clusterMarker, iconRadius: radius });
  });

  (data.orchestratorVantages || []).forEach((vantage) => {
    const [lat, lng] = vantageLatLng(vantage);
    const color = orchestratorColor(vantage);
    L.circleMarker([lat, lng], {
      radius: 7,
      fillColor: color,
      fillOpacity: 0.9,
      color: withAlpha(color, 0.7),
      weight: 2,
      interactive: true,
    })
      .bindPopup(orchestratorPopupHtml(vantage), {
        className: "network-viz__popup",
        maxWidth: 420,
        minWidth: 240,
      })
      .addTo(orchestratorLayer);
  });

  spreadOverlappingMarkers(map, [
    ...spreadablesRef.current.nodes,
    ...spreadablesRef.current.clusters,
  ]);
  redrawNetworkLines(L, layersRef, pulseTimersRef, data, nodeMarkersById, clusterMarkersById);
}

function startPulse(L, layer, pulseTimersRef, from, to, color = "rgb(125, 207, 255)") {
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
            fillColor: color,
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

function redrawNetworkLines(
  L,
  layersRef,
  pulseTimersRef,
  data,
  nodeMarkersById,
  clusterMarkersById
) {
  const { membership: memberLayer, connections: connLayer, pulses: pulseLayer } = layersRef.current;
  if (!memberLayer || !connLayer || !pulseLayer) return;

  pulseTimersRef.current.forEach(clearInterval);
  pulseTimersRef.current.forEach(clearTimeout);
  pulseTimersRef.current = [];
  memberLayer.clearLayers();
  connLayer.clearLayers();
  pulseLayer.clearLayers();

  const clusterMap = {};
  data.clusters.forEach((c) => {
    clusterMap[c.clusterId] = c;
  });
  const servicesByNode = {};
  (data.serviceInstances || []).forEach((si) => {
    if (!si.nodeId) return;
    if (!servicesByNode[si.nodeId]) servicesByNode[si.nodeId] = [];
    if (!servicesByNode[si.nodeId].includes(si.serviceId)) {
      servicesByNode[si.nodeId].push(si.serviceId);
    }
  });

  (data.nodes || []).forEach((node) => {
    if (!node.latitude && !node.longitude) return;
    const nodeMarker = nodeMarkersById[node.nodeId];
    const clusterMarker = clusterMarkersById[node.clusterId];
    if (!nodeMarker || !clusterMarker) return;
    const from = markerLatLng(nodeMarker, [node.latitude, node.longitude]);
    const to = markerLatLng(clusterMarker, [
      clusterMap[node.clusterId]?.latitude,
      clusterMap[node.clusterId]?.longitude,
    ]);
    if (!Number.isFinite(to[0]) || !Number.isFinite(to[1])) return;
    if (from[0] === to[0] && from[1] === to[1]) return;
    const role = nodeRole(node, servicesByNode[node.nodeId]);
    const lineColor = MEMBERSHIP_COLORS[role] || withAlpha(roleColor(role, node.status), 0.3);
    L.polyline([from, to], {
      color: lineColor,
      weight: 1.5,
      opacity: 0.65,
      smoothFactor: 1,
      interactive: false,
    }).addTo(memberLayer);
  });

  data.peerConnections.forEach((pc) => {
    const src = clusterMap[pc.sourceCluster];
    const tgt = clusterMap[pc.targetCluster];
    const srcMarker = clusterMarkersById[pc.sourceCluster];
    const tgtMarker = clusterMarkersById[pc.targetCluster];
    if (!src || !tgt || !srcMarker || !tgtMarker) return;
    const from = markerLatLng(srcMarker, [src.latitude, src.longitude]);
    const to = markerLatLng(tgtMarker, [tgt.latitude, tgt.longitude]);
    const isFederation = pc.connectionType === "federation";

    L.polyline([from, to], {
      color: isFederation ? FEDERATION_COLOR : ASSIGNMENT_COLOR,
      weight: isFederation ? 2 : 1.5,
      opacity: pc.connected ? 0.8 : 0.4,
      dashArray: isFederation ? "8 4" : "12 6",
      smoothFactor: 1,
    }).addTo(connLayer);

    if (pc.connected) {
      const pulseColor = isFederation ? "rgb(125, 207, 255)" : "rgb(192, 132, 252)";
      startPulse(L, pulseLayer, pulseTimersRef, from, to, pulseColor);
    }
  });
}

const ICON_MAXIMIZE = `<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="15 3 21 3 21 9"/><polyline points="9 21 3 21 3 15"/><line x1="21" y1="3" x2="14" y2="10"/><line x1="3" y1="21" x2="10" y2="14"/></svg>`;
const ICON_MINIMIZE = `<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="4 14 10 14 10 20"/><polyline points="20 10 14 10 14 4"/><line x1="14" y1="10" x2="21" y2="3"/><line x1="3" y1="21" x2="10" y2="14"/></svg>`;
const ICON_HOME = `<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 9l9-7 9 7v11a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/><polyline points="9 22 9 12 15 12 15 22"/></svg>`;

function NetworkMapInner({ data }) {
  const containerRef = useRef(null);
  const wrapperRef = useRef(null);
  const mapRef = useRef(null);
  const leafletRef = useRef(null);
  const dataRef = useRef(data);
  const layersRef = useRef({
    membership: null,
    clusters: null,
    connections: null,
    nodes: null,
    orchestrators: null,
    pulses: null,
  });
  const pulseTimersRef = useRef([]);
  const spreadablesRef = useRef({ nodes: [], clusters: [] });
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

      layersRef.current.membership = L.layerGroup().addTo(map);
      layersRef.current.connections = L.layerGroup().addTo(map);
      layersRef.current.nodes = L.layerGroup().addTo(map);
      layersRef.current.pulses = L.layerGroup().addTo(map);
      layersRef.current.clusters = L.layerGroup().addTo(map);
      layersRef.current.orchestrators = L.layerGroup().addTo(map);

      map.on("zoomend", () =>
        drawLayers(L, map, layersRef, pulseTimersRef, spreadablesRef, dataRef.current)
      );

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
    dataRef.current = data;
    const L = leafletRef.current;
    if (!L || !mapRef.current) return;
    drawLayers(L, mapRef.current, layersRef, pulseTimersRef, spreadablesRef, data);
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
  const color = NETWORK_STATUS_COLORS[status] || NETWORK_STATUS_COLORS.unknown;

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

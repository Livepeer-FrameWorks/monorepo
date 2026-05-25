import { useCallback, useEffect, useRef, useState, useSyncExternalStore } from "react";
import { motion } from "framer-motion";
import { useNetworkStatus } from "./useNetworkStatus";
import { spreadOverlappingMarkers } from "./spreadOverlap";
import { pointOnPath, samplePath } from "./arc";
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

function overallStatus(clusters) {
  if (!clusters?.length) return "unknown";
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

function detailRow(label, value, code = false) {
  return { label, value, code };
}

function renderDetail(detail) {
  if (!detail) return null;
  return (
    <div className="map-popup">
      <div className="map-popup__title">{detail.title}</div>
      <table className="map-popup__table">
        <tbody>
          {detail.rows.map((row) => (
            <tr key={`${row.label}:${row.value}`}>
              <td className="map-popup__label">{row.label}</td>
              <td className="map-popup__value">
                {row.code ? <code className="map-popup__code">{row.value}</code> : row.value}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      {(detail.sections || []).map((section) => (
        <div key={section.title}>
          <div className="map-popup__section-title">{section.title}</div>
          <table className="map-popup__table">
            <tbody>
              {section.rows.map((row) => (
                <tr key={`${section.title}:${row.label}:${row.value}`}>
                  <td className="map-popup__label">{row.label}</td>
                  <td className="map-popup__value">
                    {row.code ? <code className="map-popup__code">{row.value}</code> : row.value}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ))}
      {!!detail.tags?.length && (
        <div className="map-popup__tags">
          {detail.tags.map((tag) => (
            <span className="map-popup__tag" key={tag}>
              {tag}
            </span>
          ))}
        </div>
      )}
      {detail.description && <div className="map-popup__desc">{detail.description}</div>}
    </div>
  );
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

function clusterDetail(cluster, nodeTypeCounts, clusterServices) {
  const rows = [
    ...(cluster.region ? [detailRow("Region", cluster.region)] : []),
    ...(cluster.clusterType ? [detailRow("Type", cluster.clusterType)] : []),
    detailRow("Nodes", `${cluster.healthyNodeCount} / ${cluster.nodeCount}`),
    detailRow("Peers", `${cluster.peerCount}`),
    detailRow("Status", cluster.status),
  ];

  if (nodeTypeCounts) {
    if (nodeTypeCounts.core > 0) rows.push(detailRow("Core Nodes", `${nodeTypeCounts.core}`));
    if (nodeTypeCounts.edge > 0) rows.push(detailRow("Edge Nodes", `${nodeTypeCounts.edge}`));
  }

  const sections = [];

  if (
    cluster.currentStreams > 0 ||
    cluster.currentViewers > 0 ||
    cluster.egressMbps > 0 ||
    cluster.ingressMbps > 0 ||
    cluster.egressCapacityMbps > 0
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

  const services = clusterServices?.length ? clusterServices : cluster.services;
  return {
    title: cluster.name,
    rows,
    sections,
    tags: services,
    description: cluster.shortDescription,
  };
}

function nodeDetail(node, services) {
  return {
    title: node.name,
    rows: [
      detailRow("Type", node.nodeType),
      detailRow("Status", node.status),
      detailRow("Cluster", node.clusterId),
    ],
    tags: services,
  };
}

function orchestratorDetail(vantage, peerVantages = []) {
  const hasGeo = Number(vantage.latitude) !== 0 || Number(vantage.longitude) !== 0;
  const rows = [
    detailRow("Orch", vantage.orchAddr || "unknown", true),
    detailRow("Instance IP", vantage.resolvedIp || "unknown"),
    detailRow("Gateway", vantage.gatewayId || "unknown"),
    detailRow("Region", vantage.gatewayRegion || "unknown"),
    detailRow("Latency", `${Number(vantage.latestLatencyMs || 0).toFixed(0)}ms`),
    detailRow("Score", `${Number(vantage.score || 0).toFixed(2)}`),
  ];
  if (hasGeo) rows.push(detailRow("Geo", vantage.geoSource || "mmdb"));
  const instanceRows = [...peerVantages]
    .sort((a, b) => String(a.resolvedIp || "").localeCompare(String(b.resolvedIp || "")))
    .map((peer) =>
      detailRow(
        peer.resolvedIp || "unknown",
        `${peer.gatewayId || "unknown"} (${peer.gatewayRegion || "unknown"}) · ${Number(peer.latestLatencyMs || 0).toFixed(0)}ms`
      )
    );
  return {
    title: "Orchestrator",
    rows,
    sections: instanceRows.length ? [{ title: "Observed Instances", rows: instanceRows }] : [],
  };
}

function orchestratorColor(vantage) {
  const latency = Number(vantage.latestLatencyMs || 0);
  if (latency >= 750) return "rgb(74, 111, 91)";
  if (latency >= 250) return "rgb(45, 150, 96)";
  return ROLE_COLORS.compute;
}

// At low zoom many orchestrator vantages pile up across a region; the spread
// fan plus the glow halo can paint over a whole continent. Scale the icon and
// its drop-shadow blur with zoom so they only get loud once the user is close
// enough to disambiguate them.
function orchestratorSizeForZoom(zoom) {
  if (zoom <= 3) return { size: 8, glow: 2 };
  if (zoom <= 5) return { size: 11, glow: 4 };
  return { size: 14, glow: 6 };
}

function dedupeOrchestratorVantages(vantages) {
  const byInstance = new Map();
  (vantages || []).forEach((vantage) => {
    if (!vantage.dialedRecently) return;
    const key = `${vantage.orchAddr}:${vantage.resolvedIp}`;
    const current = byInstance.get(key);
    if (
      !current ||
      Number(vantage.latestLatencyMs || 0) < Number(current.latestLatencyMs || 0) ||
      (Number(vantage.latestLatencyMs || 0) === Number(current.latestLatencyMs || 0) &&
        Number(vantage.score || 0) > Number(current.score || 0))
    ) {
      byInstance.set(key, vantage);
    }
  });
  return [...byInstance.values()];
}

function drawLayers(L, map, layersRef, pulseTimersRef, spreadablesRef, data, onSelectFeature) {
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
  spreadablesRef.current.orchestrators = [];

  const clusterMap = {};
  (data.clusters || []).forEach((c) => {
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
    const isComputeNode = serviceRole(nodeSvcs) === "compute";
    const color = roleColor(nodeRole(node, nodeSvcs), node.status);
    const nt = (node.nodeType || "").toLowerCase();
    const isCoreNode = !isComputeNode && (nt === "core" || nt === "central");

    let size;
    let html;
    if (isComputeNode) {
      size = 9;
      html = `<div class="network-viz__node-dot network-viz__node-dot--compute-ring" style="width:${size}px; height:${size}px; --node-dot-color: ${color}; box-shadow: 0 0 7px ${color};"></div>`;
    } else if (isCoreNode) {
      size = 14;
      html = `<div class="network-viz__shape-wrap network-viz__shape-wrap--glow" style="--glow-color: ${color};"><div class="network-viz__node-dot network-viz__node-dot--core" style="width:${size}px; height:${size}px; --node-dot-color: ${color};"></div></div>`;
    } else {
      size = 10;
      html = `<div class="network-viz__node-dot" style="width:${size}px; height:${size}px; --node-dot-color: ${color}; box-shadow: 0 0 6px ${color};"></div>`;
    }

    const icon = L.divIcon({
      className: "network-viz__marker",
      html,
      iconSize: [size, size],
      iconAnchor: [size / 2, size / 2],
    });

    const nodeMarker = L.marker([node.latitude, node.longitude], {
      icon,
      interactive: true,
    }).addTo(nodeLayer);
    nodeMarker.on("click", () => onSelectFeature(nodeDetail(node, nodeSvcs)));
    nodeMarkersById[node.nodeId] = nodeMarker;
    spreadablesRef.current.nodes.push({ marker: nodeMarker, iconRadius: size / 2 });
  });

  (data.clusters || []).forEach((cluster) => {
    const color = roleColor(cluster.clusterType, cluster.status);
    const radius = Math.max(10, Math.min(24, 10 + cluster.nodeCount * 2));
    const ct = (cluster.clusterType || "").toLowerCase();
    const isCore = ct === "central" || ct === "core";
    const size = radius * 2;

    const html = isCore
      ? `<div class="network-viz__cluster network-viz__cluster--core" style="width:${size}px; height:${size}px; --cluster-color: ${color};">${cluster.nodeCount}</div>`
      : `<div class="network-viz__cluster network-viz__cluster--edge" style="width:${size}px; height:${size}px; --cluster-color: ${color};">
          <svg class="network-viz__cluster-hex" viewBox="0 0 100 100" preserveAspectRatio="none">
            <polygon points="50,6 92,30 92,70 50,94 8,70 8,30"
              fill="color-mix(in srgb, ${color} 22%, rgba(15,23,42,0.7))"
              stroke="${color}" stroke-width="3"
              stroke-dasharray="6 4" stroke-linejoin="round" stroke-linecap="round" />
          </svg>
          <span class="network-viz__cluster-count" style="color:${color};">${cluster.nodeCount}</span>
        </div>`;

    const icon = L.divIcon({
      className: "network-viz__marker",
      html,
      iconSize: [size, size],
      iconAnchor: [radius, radius],
    });

    const clusterMarker = L.marker([cluster.latitude, cluster.longitude], {
      icon,
      interactive: true,
      zIndexOffset: 1000,
    }).addTo(clusterLayer);
    clusterMarker.on("click", () =>
      onSelectFeature(
        clusterDetail(
          cluster,
          nodeTypeCountByCluster[cluster.clusterId],
          servicesByCluster[cluster.clusterId]
        )
      )
    );
    clusterMarkersById[cluster.clusterId] = clusterMarker;
    spreadablesRef.current.clusters.push({ marker: clusterMarker, iconRadius: radius });
  });

  const visibleOrchestrators = dedupeOrchestratorVantages(data.orchestratorVantages);
  const orchSizing = orchestratorSizeForZoom(map.getZoom());
  visibleOrchestrators.forEach((vantage) => {
    const [lat, lng] = vantageLatLng(vantage);
    const color = orchestratorColor(vantage);
    const { size, glow } = orchSizing;
    const icon = L.divIcon({
      className: "network-viz__marker",
      html: `<div class="network-viz__shape-wrap" style="filter: drop-shadow(0 0 ${glow}px ${color});"><div class="network-viz__orch-triangle" style="width:${size}px; height:${size}px; --glow-color: ${color};"></div></div>`,
      iconSize: [size, size],
      iconAnchor: [size / 2, size / 2 + 1],
    });
    const marker = L.marker([lat, lng], { icon, interactive: true }).addTo(orchestratorLayer);
    marker.on("click", () =>
      onSelectFeature(
        orchestratorDetail(
          vantage,
          visibleOrchestrators.filter((candidate) => candidate.orchAddr === vantage.orchAddr)
        )
      )
    );
    spreadablesRef.current.orchestrators.push({ marker, iconRadius: size / 2 });
  });

  const zoomLevel = map.getZoom();
  const spreadOrchs = zoomLevel >= 5;
  spreadOverlappingMarkers(
    map,
    [
      ...spreadablesRef.current.nodes,
      ...spreadablesRef.current.clusters,
      ...(spreadOrchs ? spreadablesRef.current.orchestrators : []),
    ],
    {
      groupThresholdMultiplier: zoomLevel >= 6 ? 1.55 : 2.15,
      maxExpandedGroupSize: 24,
      denseStepScale: 0.82,
    }
  );
  redrawNetworkLines(L, map, layersRef, pulseTimersRef, data, nodeMarkersById, clusterMarkersById);
}

function convexHull(points) {
  if (points.length < 3) return points.slice();
  const pts = [...points].sort((a, b) => a.x - b.x || a.y - b.y);
  const cross = (o, a, b) => (a.x - o.x) * (b.y - o.y) - (a.y - o.y) * (b.x - o.x);
  const lower = [];
  for (const p of pts) {
    while (lower.length >= 2 && cross(lower[lower.length - 2], lower[lower.length - 1], p) <= 0) {
      lower.pop();
    }
    lower.push(p);
  }
  const upper = [];
  for (let i = pts.length - 1; i >= 0; i--) {
    const p = pts[i];
    while (upper.length >= 2 && cross(upper[upper.length - 2], upper[upper.length - 1], p) <= 0) {
      upper.pop();
    }
    upper.push(p);
  }
  lower.pop();
  upper.pop();
  return lower.concat(upper);
}

function inflateHull(points, padding) {
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
// soft blob instead of a sharp polygon. cornerRadius is in pixels; per vertex
// it's clamped to half the adjacent edge length so tight clusters don't fold.
function smoothPolygon(points, cornerRadius, samplesPerCorner = 6) {
  const n = points.length;
  if (n < 3) return points;
  const out = [];
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

function shouldDrawClusterHull(points) {
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

function startPulse(L, layer, pulseTimersRef, from, to, color = "rgb(125, 207, 255)") {
  const steps = 60;
  const interval = 50;

  function createPulse(delay) {
    let step = 0;
    let marker = null;

    const timerId = setTimeout(() => {
      const id = setInterval(() => {
        const t = step / steps;
        const [lat, lng] = pointOnPath(from, to, t);

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
  map,
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
  (data.clusters || []).forEach((c) => {
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

  // Group nodes by cluster so we can draw a hull per cluster (≥2 members) or
  // fall back to a single radial line for solo nodes.
  const nodesByCluster = {};
  (data.nodes || []).forEach((node) => {
    if (!node.latitude && !node.longitude) return;
    if (!node.clusterId) return;
    if (!nodeMarkersById[node.nodeId] || !clusterMarkersById[node.clusterId]) return;
    if (!nodesByCluster[node.clusterId]) nodesByCluster[node.clusterId] = [];
    nodesByCluster[node.clusterId].push(node);
  });

  Object.entries(nodesByCluster).forEach(([clusterId, members]) => {
    const clusterMarker = clusterMarkersById[clusterId];
    const cluster = clusterMap[clusterId];
    if (!clusterMarker || !cluster) return;
    const clusterColor = roleColor(cluster.clusterType, cluster.status);

    const drawMemberLine = (node) => {
      const from = markerLatLng(nodeMarkersById[node.nodeId], [node.latitude, node.longitude]);
      const to = markerLatLng(clusterMarker, [cluster.latitude, cluster.longitude]);
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
    };

    if (members.length === 1) {
      drawMemberLine(members[0]);
      return;
    }

    const pts = [clusterMarker, ...members.map((n) => nodeMarkersById[n.nodeId])]
      .filter(Boolean)
      .map((m) => map.latLngToContainerPoint(m.getLatLng()));
    if (pts.length < 3) return;
    if (!shouldDrawClusterHull(pts)) {
      members.forEach(drawMemberLine);
      return;
    }
    const hull = convexHull(pts);
    const inflated = inflateHull(hull, 10);
    const smoothed = smoothPolygon(inflated, 14);
    const ring = smoothed.map((p) => {
      const ll = map.containerPointToLatLng([p.x, p.y]);
      return [ll.lat, ll.lng];
    });
    L.polygon(ring, {
      className: "network-viz__hull",
      color: withAlpha(clusterColor, 0.5),
      weight: 1,
      fillColor: clusterColor,
      fillOpacity: 0.12,
      smoothFactor: 1,
      interactive: false,
    }).addTo(memberLayer);
  });

  (data.peerConnections || []).forEach((pc) => {
    const src = clusterMap[pc.sourceCluster];
    const tgt = clusterMap[pc.targetCluster];
    const srcMarker = clusterMarkersById[pc.sourceCluster];
    const tgtMarker = clusterMarkersById[pc.targetCluster];
    if (!src || !tgt || !srcMarker || !tgtMarker) return;
    const from = markerLatLng(srcMarker, [src.latitude, src.longitude]);
    const to = markerLatLng(tgtMarker, [tgt.latitude, tgt.longitude]);
    const isFederation = pc.connectionType === "federation";

    L.polyline(samplePath(from, to), {
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
const ICON_CPU = `<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect width="16" height="16" x="4" y="4" rx="2"/><rect width="6" height="6" x="9" y="9" rx="1"/><path d="M15 2v2"/><path d="M15 20v2"/><path d="M2 15h2"/><path d="M2 9h2"/><path d="M20 15h2"/><path d="M20 9h2"/><path d="M9 2v2"/><path d="M9 20v2"/></svg>`;

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
  const spreadablesRef = useRef({ nodes: [], clusters: [], orchestrators: [] });
  const [mapReady, setMapReady] = useState(false);
  const [isFullscreen, setIsFullscreen] = useState(false);
  const [showOrchestrators, setShowOrchestrators] = useState(true);
  const [selectedDetail, setSelectedDetail] = useState(null);
  const selectFeatureRef = useRef((html) => setSelectedDetail(html));

  selectFeatureRef.current = (html) => setSelectedDetail(html);

  // Init map once
  useEffect(() => {
    let cancelled = false;

    import("leaflet").then((L) => {
      if (cancelled || !containerRef.current || mapRef.current) return;

      const map = L.map(containerRef.current, {
        center: [25, 10],
        zoom: 2,
        minZoom: 2,
        maxZoom: 8,
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
        drawLayers(
          L,
          map,
          layersRef,
          pulseTimersRef,
          spreadablesRef,
          dataRef.current,
          selectFeatureRef.current
        )
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
    dataRef.current = {
      ...data,
      orchestratorVantages: showOrchestrators ? data.orchestratorVantages : [],
    };
    const L = leafletRef.current;
    if (!L || !mapRef.current) return;
    drawLayers(
      L,
      mapRef.current,
      layersRef,
      pulseTimersRef,
      spreadablesRef,
      dataRef.current,
      selectFeatureRef.current
    );
  }, [data, mapReady, showOrchestrators]);

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
      {selectedDetail && (
        <aside className="network-viz__detail-panel" aria-label="Map selection details">
          <button
            type="button"
            className="network-viz__detail-close"
            aria-label="Close details"
            onClick={() => setSelectedDetail(null)}
          >
            ×
          </button>
          <div className="network-viz__detail-body">{renderDetail(selectedDetail)}</div>
        </aside>
      )}
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
          className={`network-viz__control-btn${showOrchestrators ? " network-viz__control-btn--active" : ""}`}
          onClick={() => {
            setShowOrchestrators((value) => {
              if (value) setSelectedDetail(null);
              return !value;
            });
          }}
          title={showOrchestrators ? "Hide Livepeer compute" : "Show Livepeer compute"}
          dangerouslySetInnerHTML={{ __html: ICON_CPU }}
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

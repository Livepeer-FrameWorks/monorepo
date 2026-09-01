import { useCallback, useEffect, useRef, useState, useSyncExternalStore } from "react";
import { motion } from "framer-motion";
import {
  addRequiredAttribution,
  basemapMapOptions,
  bindModifierScrollZoom,
  featureCollection,
  firstBasemapSymbolLayer,
  readRuntimePublicConfig,
} from "@frameworks/map-core";
import { useNetworkStatus } from "./useNetworkStatus";
import { spreadOverlappingMarkers } from "./spreadOverlap";
import { pointOnPath, samplePath } from "./arc";

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

const ICON_MAXIMIZE = `<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="15 3 21 3 21 9"/><polyline points="9 21 3 21 3 15"/><line x1="21" y1="3" x2="14" y2="10"/><line x1="3" y1="21" x2="10" y2="14"/></svg>`;
const ICON_MINIMIZE = `<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="4 14 10 14 10 20"/><polyline points="20 10 14 10 14 4"/><line x1="14" y1="10" x2="21" y2="3"/><line x1="3" y1="21" x2="10" y2="14"/></svg>`;
const ICON_HOME = `<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 9l9-7 9 7v11a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/><polyline points="9 22 9 12 15 12 15 22"/></svg>`;
const ICON_CPU = `<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect width="16" height="16" x="4" y="4" rx="2"/><rect width="6" height="6" x="9" y="9" rx="1"/><path d="M15 2v2"/><path d="M15 20v2"/><path d="M2 15h2"/><path d="M2 9h2"/><path d="M20 15h2"/><path d="M20 9h2"/><path d="M9 2v2"/><path d="M9 20v2"/></svg>`;

function overallStatus(clusters) {
  if (!clusters?.length) return "unknown";
  if (clusters.every((cluster) => cluster.status === "healthy")) return "healthy";
  if (clusters.some((cluster) => cluster.status === "down")) return "down";
  return "degraded";
}

function statusLabel(status) {
  if (status === "healthy") return "OPERATIONAL";
  if (status === "degraded") return "DEGRADED";
  if (status === "down") return "DOWN";
  return "UNKNOWN";
}

function usePrefersReducedMotion() {
  const subscribe = useCallback((callback) => {
    const query = window.matchMedia("(prefers-reduced-motion: reduce)");
    query.addEventListener("change", callback);
    return () => query.removeEventListener("change", callback);
  }, []);
  const getSnapshot = useCallback(
    () => window.matchMedia("(prefers-reduced-motion: reduce)").matches,
    []
  );
  return useSyncExternalStore(subscribe, getSnapshot, () => false);
}

function formatLoad(current, max) {
  return max ? `${current} / ${max}` : `${current}`;
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
  return services.some(
    (service) => service === "livepeer-gateway" || service.startsWith("livepeer-")
  )
    ? "compute"
    : undefined;
}

function nodeRole(node, services) {
  return serviceRole(services) || node.nodeType || "default";
}

function withAlpha(rgb, alpha) {
  return rgb.replace("rgb(", "rgba(").replace(")", `, ${alpha})`);
}

function hashString(value) {
  let hash = 0;
  for (let index = 0; index < value.length; index++) {
    hash = (hash * 31 + value.charCodeAt(index)) | 0;
  }
  return Math.abs(hash);
}

function unknownGeoLatLng(key) {
  const hash = hashString(key);
  return [
    UNKNOWN_GEO_ANCHOR[0] + (hash % 700) / 100,
    UNKNOWN_GEO_ANCHOR[1] + ((hash >> 4) % 1000) / 100,
  ];
}

function vantageLatLng(vantage) {
  const lat = Number(vantage.latitude ?? 0);
  const lng = Number(vantage.longitude ?? 0);
  return Number.isFinite(lat) && Number.isFinite(lng) && !(lat === 0 && lng === 0)
    ? [lat, lng]
    : unknownGeoLatLng(`${vantage.orchAddr}:${vantage.resolvedIp}:${vantage.gatewayId}`);
}

function markerLatLng(marker, fallback) {
  if (!marker) return fallback;
  const point = marker.getLngLat();
  return [point.lat, point.lng];
}

function clusterDetail(cluster, nodeTypeCounts, clusterServices) {
  const rows = [
    ...(cluster.region ? [detailRow("Region", cluster.region)] : []),
    ...(cluster.clusterType ? [detailRow("Type", cluster.clusterType)] : []),
    detailRow("Nodes", `${cluster.healthyNodeCount} / ${cluster.nodeCount}`),
    detailRow("Peers", `${cluster.peerCount}`),
    detailRow("Status", cluster.status),
  ];
  if (nodeTypeCounts?.core > 0) rows.push(detailRow("Core Nodes", `${nodeTypeCounts.core}`));
  if (nodeTypeCounts?.edge > 0) rows.push(detailRow("Edge Nodes", `${nodeTypeCounts.edge}`));
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
  return {
    title: cluster.name,
    rows,
    sections,
    tags: clusterServices?.length ? clusterServices : cluster.services,
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
    .sort((left, right) =>
      String(left.resolvedIp || "").localeCompare(String(right.resolvedIp || ""))
    )
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

function orchestratorSizeForZoom(zoom) {
  if (zoom <= 3) return { size: 8, glow: 2 };
  if (zoom <= 5) return { size: 11, glow: 4 };
  return { size: 14, glow: 6 };
}

function dedupeOrchestratorVantages(vantages) {
  const byInstance = new Map();
  for (const vantage of vantages || []) {
    if (!vantage.dialedRecently) continue;
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
  }
  return [...byInstance.values()];
}

function convexHull(points) {
  if (points.length < 3) return points.slice();
  const sorted = [...points].sort((left, right) => left.x - right.x || left.y - right.y);
  const cross = (origin, left, right) =>
    (left.x - origin.x) * (right.y - origin.y) - (left.y - origin.y) * (right.x - origin.x);
  const half = (items) => {
    const output = [];
    for (const point of items) {
      while (
        output.length >= 2 &&
        cross(output[output.length - 2], output[output.length - 1], point) <= 0
      ) {
        output.pop();
      }
      output.push(point);
    }
    output.pop();
    return output;
  };
  return half(sorted).concat(half([...sorted].reverse()));
}

function inflateHull(points, padding) {
  if (!points.length) return points;
  const center = points.reduce(
    (sum, point) => ({ x: sum.x + point.x / points.length, y: sum.y + point.y / points.length }),
    { x: 0, y: 0 }
  );
  return points.map((point) => {
    const dx = point.x - center.x;
    const dy = point.y - center.y;
    const length = Math.hypot(dx, dy) || 1;
    return { x: point.x + (dx / length) * padding, y: point.y + (dy / length) * padding };
  });
}

function smoothPolygon(points, cornerRadius, samplesPerCorner = 6) {
  if (points.length < 3) return points;
  const output = [];
  for (let index = 0; index < points.length; index++) {
    const previous = points[(index - 1 + points.length) % points.length];
    const current = points[index];
    const next = points[(index + 1) % points.length];
    const toPrevious = { x: previous.x - current.x, y: previous.y - current.y };
    const toNext = { x: next.x - current.x, y: next.y - current.y };
    const previousLength = Math.hypot(toPrevious.x, toPrevious.y) || 1;
    const nextLength = Math.hypot(toNext.x, toNext.y) || 1;
    const radius = Math.min(cornerRadius, previousLength / 2, nextLength / 2);
    const start = {
      x: current.x + (toPrevious.x / previousLength) * radius,
      y: current.y + (toPrevious.y / previousLength) * radius,
    };
    const end = {
      x: current.x + (toNext.x / nextLength) * radius,
      y: current.y + (toNext.y / nextLength) * radius,
    };
    for (let sample = 0; sample <= samplesPerCorner; sample++) {
      const t = sample / samplesPerCorner;
      const u = 1 - t;
      output.push({
        x: u * u * start.x + 2 * u * t * current.x + t * t * end.x,
        y: u * u * start.y + 2 * u * t * current.y + t * t * end.y,
      });
    }
  }
  return output;
}

function shouldDrawClusterHull(points) {
  if (points.length < 3) return false;
  const xs = points.map((point) => point.x);
  const ys = points.map((point) => point.y);
  const width = Math.max(...xs) - Math.min(...xs);
  const height = Math.max(...ys) - Math.min(...ys);
  const major = Math.max(width, height);
  const minor = Math.max(1, Math.min(width, height));
  return major <= 360 && major / minor <= 4 && !(major > 220 && minor < 56);
}

function createMarkerElement(html) {
  const element = document.createElement("button");
  element.type = "button";
  element.className = "network-viz__marker";
  element.innerHTML = html;
  return element;
}

function initializeTopologyLayers(map) {
  const empty = featureCollection([]);
  const beforeLabels = firstBasemapSymbolLayer(map);
  for (const id of ["membership", "connections", "pulses"]) {
    map.addSource(`network-${id}`, { type: "geojson", data: empty });
  }
  map.addLayer(
    {
      id: "network-membership-fill",
      type: "fill",
      source: "network-membership",
      filter: ["==", ["geometry-type"], "Polygon"],
      paint: { "fill-color": ["get", "color"], "fill-opacity": 0.12 },
    },
    beforeLabels
  );
  map.addLayer(
    {
      id: "network-membership-outline",
      type: "line",
      source: "network-membership",
      filter: ["==", ["geometry-type"], "Polygon"],
      paint: { "line-color": ["get", "outline"], "line-width": 1 },
    },
    beforeLabels
  );
  map.addLayer(
    {
      id: "network-membership-lines",
      type: "line",
      source: "network-membership",
      filter: ["==", ["geometry-type"], "LineString"],
      paint: { "line-color": ["get", "color"], "line-width": 1.5, "line-opacity": 0.65 },
    },
    beforeLabels
  );
  map.addLayer(
    {
      id: "network-federation",
      type: "line",
      source: "network-connections",
      filter: ["==", ["get", "type"], "federation"],
      layout: { "line-cap": "round", "line-join": "round" },
      paint: {
        "line-color": FEDERATION_COLOR,
        "line-width": 2,
        "line-opacity": ["case", ["boolean", ["get", "connected"], false], 0.8, 0.4],
        "line-dasharray": [4, 2],
      },
    },
    beforeLabels
  );
  map.addLayer(
    {
      id: "network-assignments",
      type: "line",
      source: "network-connections",
      filter: ["!=", ["get", "type"], "federation"],
      layout: { "line-cap": "round", "line-join": "round" },
      paint: {
        "line-color": ASSIGNMENT_COLOR,
        "line-width": 1.5,
        "line-opacity": ["case", ["boolean", ["get", "connected"], false], 0.8, 0.4],
        "line-dasharray": [6, 3],
      },
    },
    beforeLabels
  );
  map.addLayer({
    id: "network-pulses",
    type: "circle",
    source: "network-pulses",
    paint: {
      "circle-radius": 3,
      "circle-color": ["get", "color"],
      "circle-opacity": ["get", "opacity"],
    },
  });
}

function drawTopology(maplibre, map, markersRef, pulsePathsRef, data, showOrchestrators, onSelect) {
  for (const marker of markersRef.current) marker.remove();
  markersRef.current = [];

  const clusterMap = Object.fromEntries(
    (data.clusters || []).map((cluster) => [cluster.clusterId, cluster])
  );
  const servicesByNode = {};
  for (const instance of data.serviceInstances || []) {
    if (!instance.nodeId) continue;
    servicesByNode[instance.nodeId] ||= [];
    if (!servicesByNode[instance.nodeId].includes(instance.serviceId)) {
      servicesByNode[instance.nodeId].push(instance.serviceId);
    }
  }
  Object.values(servicesByNode).forEach((services) => services.sort());

  const servicesByCluster = {};
  const nodeTypeCountByCluster = {};
  for (const node of data.nodes || []) {
    if (!node.clusterId) continue;
    nodeTypeCountByCluster[node.clusterId] ||= { core: 0, edge: 0 };
    const type = (node.nodeType || "").toLowerCase();
    if (type === "core") nodeTypeCountByCluster[node.clusterId].core++;
    else if (type === "edge") nodeTypeCountByCluster[node.clusterId].edge++;
    servicesByCluster[node.clusterId] ||= [];
    for (const service of servicesByNode[node.nodeId] || []) {
      if (!servicesByCluster[node.clusterId].includes(service))
        servicesByCluster[node.clusterId].push(service);
    }
  }
  Object.values(servicesByCluster).forEach((services) => services.sort());

  const nodeMarkersById = {};
  const clusterMarkersById = {};
  const spreadables = [];

  for (const node of data.nodes || []) {
    if (!node.latitude && !node.longitude) continue;
    const services = servicesByNode[node.nodeId];
    const isCompute = serviceRole(services) === "compute";
    const color = roleColor(nodeRole(node, services), node.status);
    const type = (node.nodeType || "").toLowerCase();
    const isCore = !isCompute && (type === "core" || type === "central");
    const size = isCompute ? 9 : isCore ? 14 : 10;
    const html = isCompute
      ? `<span class="network-viz__node-dot network-viz__node-dot--compute-ring" style="width:${size}px;height:${size}px;--node-dot-color:${color};box-shadow:0 0 7px ${color}"></span>`
      : isCore
        ? `<span class="network-viz__shape-wrap network-viz__shape-wrap--glow" style="--glow-color:${color}"><span class="network-viz__node-dot network-viz__node-dot--core" style="width:${size}px;height:${size}px;--node-dot-color:${color}"></span></span>`
        : `<span class="network-viz__node-dot" style="width:${size}px;height:${size}px;--node-dot-color:${color};box-shadow:0 0 6px ${color}"></span>`;
    const element = createMarkerElement(html);
    element.setAttribute("aria-label", node.name || "Network node");
    element.addEventListener("click", () => onSelect(nodeDetail(node, services)));
    const marker = new maplibre.Marker({ element, anchor: "center" })
      .setLngLat([node.longitude, node.latitude])
      .addTo(map);
    nodeMarkersById[node.nodeId] = marker;
    markersRef.current.push(marker);
    spreadables.push({ marker, iconRadius: size / 2 });
  }

  for (const cluster of data.clusters || []) {
    const color = roleColor(cluster.clusterType, cluster.status);
    const radius = Math.max(10, Math.min(24, 10 + cluster.nodeCount * 2));
    const isCore = ["central", "core"].includes((cluster.clusterType || "").toLowerCase());
    const size = radius * 2;
    const html = isCore
      ? `<span class="network-viz__cluster network-viz__cluster--core" style="width:${size}px;height:${size}px;--cluster-color:${color}">${cluster.nodeCount}</span>`
      : `<span class="network-viz__cluster network-viz__cluster--edge" style="width:${size}px;height:${size}px;--cluster-color:${color}"><svg class="network-viz__cluster-hex" viewBox="0 0 100 100" preserveAspectRatio="none"><polygon points="50,6 92,30 92,70 50,94 8,70 8,30" fill="color-mix(in srgb, ${color} 22%, rgba(15,23,42,0.7))" stroke="${color}" stroke-width="3" stroke-dasharray="6 4" stroke-linejoin="round" stroke-linecap="round" /></svg><span class="network-viz__cluster-count" style="color:${color}">${cluster.nodeCount}</span></span>`;
    const element = createMarkerElement(html);
    element.setAttribute("aria-label", cluster.name || "Network cluster");
    element.addEventListener("click", () =>
      onSelect(
        clusterDetail(
          cluster,
          nodeTypeCountByCluster[cluster.clusterId],
          servicesByCluster[cluster.clusterId]
        )
      )
    );
    const marker = new maplibre.Marker({ element, anchor: "center" })
      .setLngLat([cluster.longitude, cluster.latitude])
      .addTo(map);
    clusterMarkersById[cluster.clusterId] = marker;
    markersRef.current.push(marker);
    spreadables.push({ marker, iconRadius: radius });
  }

  const visibleOrchestrators = showOrchestrators
    ? dedupeOrchestratorVantages(data.orchestratorVantages)
    : [];
  const sizing = orchestratorSizeForZoom(map.getZoom());
  for (const vantage of visibleOrchestrators) {
    const [lat, lng] = vantageLatLng(vantage);
    const color = orchestratorColor(vantage);
    const html = `<span class="network-viz__shape-wrap" style="filter:drop-shadow(0 0 ${sizing.glow}px ${color})"><span class="network-viz__orch-triangle" style="width:${sizing.size}px;height:${sizing.size}px;--glow-color:${color}"></span></span>`;
    const element = createMarkerElement(html);
    element.setAttribute("aria-label", "Livepeer orchestrator");
    element.addEventListener("click", () =>
      onSelect(
        orchestratorDetail(
          vantage,
          visibleOrchestrators.filter((candidate) => candidate.orchAddr === vantage.orchAddr)
        )
      )
    );
    const marker = new maplibre.Marker({ element, anchor: "center" })
      .setLngLat([lng, lat])
      .addTo(map);
    markersRef.current.push(marker);
    if (map.getZoom() >= 5) spreadables.push({ marker, iconRadius: sizing.size / 2 });
  }

  spreadOverlappingMarkers(map, spreadables, {
    groupThresholdMultiplier: map.getZoom() >= 6 ? 1.55 : 2.15,
    maxExpandedGroupSize: 24,
    denseStepScale: 0.82,
  });

  const membership = [];
  const nodesByCluster = {};
  for (const node of data.nodes || []) {
    if ((!node.latitude && !node.longitude) || !node.clusterId) continue;
    if (!nodeMarkersById[node.nodeId] || !clusterMarkersById[node.clusterId]) continue;
    nodesByCluster[node.clusterId] ||= [];
    nodesByCluster[node.clusterId].push(node);
  }
  for (const [clusterID, members] of Object.entries(nodesByCluster)) {
    const cluster = clusterMap[clusterID];
    const clusterMarker = clusterMarkersById[clusterID];
    if (!cluster || !clusterMarker) continue;
    const clusterColor = roleColor(cluster.clusterType, cluster.status);
    const memberLine = (node) => {
      const from = markerLatLng(nodeMarkersById[node.nodeId], [node.latitude, node.longitude]);
      const to = markerLatLng(clusterMarker, [cluster.latitude, cluster.longitude]);
      if (from[0] === to[0] && from[1] === to[1]) return;
      const role = nodeRole(node, servicesByNode[node.nodeId]);
      membership.push({
        type: "Feature",
        properties: {
          color: MEMBERSHIP_COLORS[role] || withAlpha(roleColor(role, node.status), 0.3),
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
    if (members.length === 1) {
      memberLine(members[0]);
      continue;
    }
    const points = [clusterMarker, ...members.map((node) => nodeMarkersById[node.nodeId])]
      .filter(Boolean)
      .map((marker) => map.project(marker.getLngLat()));
    if (!shouldDrawClusterHull(points)) {
      members.forEach(memberLine);
      continue;
    }
    const ring = smoothPolygon(inflateHull(convexHull(points), 10), 14).map((point) => {
      const lngLat = map.unproject(point);
      return [lngLat.lng, lngLat.lat];
    });
    if (ring.length) ring.push(ring[0]);
    membership.push({
      type: "Feature",
      properties: { color: clusterColor, outline: withAlpha(clusterColor, 0.5) },
      geometry: { type: "Polygon", coordinates: [ring] },
    });
  }
  map.getSource("network-membership")?.setData(featureCollection(membership));

  const connections = [];
  const pulsePaths = [];
  for (const connection of data.peerConnections || []) {
    const sourceCluster = clusterMap[connection.sourceCluster];
    const targetCluster = clusterMap[connection.targetCluster];
    if (!sourceCluster || !targetCluster) continue;
    const from = markerLatLng(clusterMarkersById[connection.sourceCluster], [
      sourceCluster.latitude,
      sourceCluster.longitude,
    ]);
    const to = markerLatLng(clusterMarkersById[connection.targetCluster], [
      targetCluster.latitude,
      targetCluster.longitude,
    ]);
    const path = samplePath(from, to);
    const isFederation = connection.connectionType === "federation";
    connections.push({
      type: "Feature",
      properties: { type: connection.connectionType, connected: connection.connected },
      geometry: { type: "LineString", coordinates: path.map(([lat, lng]) => [lng, lat]) },
    });
    if (connection.connected) {
      pulsePaths.push({
        from,
        to,
        color: isFederation ? "rgb(125, 207, 255)" : "rgb(192, 132, 252)",
      });
    }
  }
  map.getSource("network-connections")?.setData(featureCollection(connections));
  pulsePathsRef.current = pulsePaths;
}

function NetworkMapInner({ data }) {
  const containerRef = useRef(null);
  const mapRef = useRef(null);
  const maplibreRef = useRef(null);
  const markersRef = useRef([]);
  const pulsePathsRef = useRef([]);
  const animationRef = useRef(null);
  const dataRef = useRef(data);
  const showOrchestratorsRef = useRef(true);
  const selectFeatureRef = useRef(() => {});
  const [mapReady, setMapReady] = useState(false);
  const [unavailable, setUnavailable] = useState(false);
  const [isFullscreen, setIsFullscreen] = useState(false);
  const [showOrchestrators, setShowOrchestrators] = useState(true);
  const [selectedDetail, setSelectedDetail] = useState(null);
  selectFeatureRef.current = setSelectedDetail;

  useEffect(() => {
    let cancelled = false;
    let unbindWheel = () => {};
    (async () => {
      let runtime;
      try {
        runtime = readRuntimePublicConfig();
      } catch {
        console.error("Map unavailable: invalid basemap runtime configuration");
        if (!cancelled) setUnavailable(true);
        return;
      }
      try {
        const [maplibre] = await Promise.all([
          import("maplibre-gl"),
          import("maplibre-gl/dist/maplibre-gl.css"),
        ]);
        if (cancelled || !containerRef.current) return;
        const map = new maplibre.Map({
          container: containerRef.current,
          center: [10, 25],
          zoom: 2,
          minZoom: 2,
          maxZoom: 8,
          scrollZoom: false,
          ...basemapMapOptions(runtime.basemap),
        });
        mapRef.current = map;
        maplibreRef.current = maplibre;
        addRequiredAttribution(map, runtime.basemap);
        unbindWheel = bindModifierScrollZoom(map);
        map.once("load", () => {
          if (cancelled) return;
          initializeTopologyLayers(map);
          setMapReady(true);
          drawTopology(
            maplibre,
            map,
            markersRef,
            pulsePathsRef,
            dataRef.current,
            showOrchestratorsRef.current,
            selectFeatureRef.current
          );
          map.on("zoomend", () =>
            drawTopology(
              maplibre,
              map,
              markersRef,
              pulsePathsRef,
              dataRef.current,
              showOrchestratorsRef.current,
              selectFeatureRef.current
            )
          );

          const reducedMotion = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
          const animate = (now) => {
            const cycle = reducedMotion ? 0.5 : (now % 3000) / 3000;
            const opacity = reducedMotion
              ? 0.75
              : cycle < 0.1
                ? cycle / 0.1
                : cycle > 0.9
                  ? (1 - cycle) / 0.1
                  : 0.9;
            const pulses = pulsePathsRef.current.flatMap((path) =>
              [cycle, (cycle + 0.5) % 1].map((offset) => {
                const [lat, lng] = pointOnPath(path.from, path.to, offset);
                return {
                  type: "Feature",
                  properties: { color: path.color, opacity },
                  geometry: { type: "Point", coordinates: [lng, lat] },
                };
              })
            );
            map.getSource("network-pulses")?.setData(featureCollection(pulses));
            if (!reducedMotion) animationRef.current = requestAnimationFrame(animate);
          };
          animate(performance.now());
        });
      } catch {
        console.error("Map unavailable: renderer initialization failed");
        if (!cancelled) setUnavailable(true);
      }
    })();
    return () => {
      cancelled = true;
      unbindWheel();
      if (animationRef.current) cancelAnimationFrame(animationRef.current);
      for (const marker of markersRef.current) marker.remove();
      markersRef.current = [];
      mapRef.current?.remove();
      mapRef.current = null;
    };
  }, []);

  useEffect(() => {
    dataRef.current = data;
    showOrchestratorsRef.current = showOrchestrators;
    if (!mapReady || !mapRef.current || !maplibreRef.current) return;
    drawTopology(
      maplibreRef.current,
      mapRef.current,
      markersRef,
      pulsePathsRef,
      data,
      showOrchestrators,
      selectFeatureRef.current
    );
  }, [data, mapReady, showOrchestrators]);

  const toggleFullscreen = useCallback(() => {
    setIsFullscreen((value) => !value);
    setTimeout(() => mapRef.current?.resize(), 310);
  }, []);
  const resetView = useCallback(
    () => mapRef.current?.easeTo({ center: [10, 25], zoom: 2, duration: 500 }),
    []
  );

  return (
    <div
      className={`network-viz__map-wrapper${isFullscreen ? " network-viz__map-wrapper--fullscreen" : ""}`}
    >
      <div ref={containerRef} className="network-viz__map" />
      {unavailable ? (
        <div className="network-viz__unavailable" role="status">
          Basemap unavailable
        </div>
      ) : null}
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
          disabled={!mapReady}
          aria-label="Reset map view"
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
          disabled={!mapReady}
          aria-label={showOrchestrators ? "Hide Livepeer compute" : "Show Livepeer compute"}
          title={showOrchestrators ? "Hide Livepeer compute" : "Show Livepeer compute"}
          dangerouslySetInnerHTML={{ __html: ICON_CPU }}
        />
        <button
          type="button"
          className="network-viz__control-btn"
          onClick={toggleFullscreen}
          aria-label={isFullscreen ? "Exit fullscreen map" : "Open fullscreen map"}
          title={isFullscreen ? "Exit fullscreen" : "Fullscreen"}
          dangerouslySetInnerHTML={{ __html: isFullscreen ? ICON_MINIMIZE : ICON_MAXIMIZE }}
        />
      </div>
      {!isFullscreen && mapReady ? (
        <button
          type="button"
          className="network-viz__scroll-hint"
          onClick={(event) => event.currentTarget.remove()}
          aria-label="Dismiss map zoom instructions"
        >
          Hold <kbd>⌥</kbd> or <kbd>Ctrl</kbd> + scroll to zoom
        </button>
      ) : null}
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
        <span>
          {data.peerConnections.filter((connection) => connection.connected).length} Peered
        </span>
      </div>
    </motion.div>
  );
}

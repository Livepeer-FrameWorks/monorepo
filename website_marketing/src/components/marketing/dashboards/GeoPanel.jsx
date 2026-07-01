import { useCallback, useEffect, useRef, useState } from "react";
import { geo } from "./fixtures";
import "leaflet/dist/leaflet.css";

const TILE_URL = "https://{s}.basemaps.cartocdn.com/dark_all/{z}/{x}/{y}{r}.png";
const HEAT_GRADIENT = {
  0.05: "rgba(122, 162, 247, 0.55)",
  0.25: "rgba(125, 207, 255, 0.75)",
  0.45: "rgba(158, 206, 106, 0.85)",
  0.65: "rgba(224, 175, 104, 0.9)",
  0.9: "rgba(247, 118, 142, 0.95)",
};
const FLOW_COLORS = { success: "rgb(158, 206, 106)", degraded: "rgb(224, 175, 104)" };

const ICON_HOME = `<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 9l9-7 9 7v11a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/><polyline points="9 22 9 12 15 12 15 22"/></svg>`;
const ICON_MAX = `<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="15 3 21 3 21 9"/><polyline points="9 21 3 21 3 15"/><line x1="21" y1="3" x2="14" y2="10"/><line x1="3" y1="21" x2="10" y2="14"/></svg>`;
const ICON_MIN = `<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="4 14 10 14 10 20"/><polyline points="20 10 14 10 14 4"/><line x1="14" y1="10" x2="21" y2="3"/><line x1="3" y1="21" x2="10" y2="14"/></svg>`;

// Quadratic-bezier great-circle-ish arc between two [lat,lng] points, lifted
// perpendicular to the chord so routes read as curved flows, not straight lines.
function arcPoints(from, to, segments = 36) {
  const [lat1, lng1] = from;
  const [lat2, lng2] = to;
  const dx = lng2 - lng1;
  const dy = lat2 - lat1;
  const dist = Math.hypot(dx, dy) || 1;
  const lift = Math.min(dist * 0.2, 26);
  const cLat = (lat1 + lat2) / 2 + (dx / dist) * lift;
  const cLng = (lng1 + lng2) / 2 - (dy / dist) * lift;
  const pts = [];
  for (let i = 0; i <= segments; i++) {
    const t = i / segments;
    const u = 1 - t;
    pts.push([
      u * u * lat1 + 2 * u * t * cLat + t * t * lat2,
      u * u * lng1 + 2 * u * t * cLng + t * t * lng2,
    ]);
  }
  return pts;
}

export function GeoPanel({ height = 440 }) {
  const containerRef = useRef(null);
  const mapRef = useRef(null);
  const leafletRef = useRef(null);
  const routeLayerRef = useRef(null);
  const pulseLayerRef = useRef(null);
  const timersRef = useRef([]);
  const [ready, setReady] = useState(false);
  const [showRoutes, setShowRoutes] = useState(true);
  const [isFullscreen, setIsFullscreen] = useState(false);
  const [showHint, setShowHint] = useState(true);

  const clearTimers = () => {
    timersRef.current.forEach(clearInterval);
    timersRef.current = [];
  };

  // Init map + static layers (heat, edge markers, H3 demand hexes) once.
  useEffect(() => {
    let cancelled = false;

    (async () => {
      const Lmod = await import("leaflet");
      await import("leaflet.heat");
      const { latLngToCell, cellToBoundary } = await import("h3-js");
      const L = Lmod.default ?? Lmod;
      if (cancelled || !containerRef.current || mapRef.current) return;

      const map = L.map(containerRef.current, {
        center: [28, 4],
        zoom: 2,
        minZoom: 2,
        maxZoom: 8,
        zoomControl: false,
        attributionControl: false,
        scrollWheelZoom: false,
      });
      L.tileLayer(TILE_URL, { maxZoom: 19, subdomains: "abcd" }).addTo(map);

      containerRef.current.addEventListener(
        "wheel",
        (e) => {
          if (e.altKey || e.ctrlKey || e.metaKey) {
            e.preventDefault();
            map.scrollWheelZoom.enable();
            setShowHint(false);
          } else {
            map.scrollWheelZoom.disable();
          }
        },
        { passive: false }
      );

      // H3 demand hexes around the busiest viewer cells (resolution 3 ≈ regional).
      const hexLayer = L.layerGroup().addTo(map);
      const seen = new Set();
      geo.viewers
        .filter((v) => v.intensity >= 0.6)
        .forEach((v) => {
          const cell = latLngToCell(v.lat, v.lng, 3);
          if (seen.has(cell)) return;
          seen.add(cell);
          L.polygon(cellToBoundary(cell), {
            color: "rgba(125, 207, 255, 0.5)",
            weight: 1,
            fill: true,
            fillColor: "rgba(125, 207, 255, 1)",
            fillOpacity: 0.05,
            interactive: false,
          }).addTo(hexLayer);
        });

      // Viewer heat.
      L.heatLayer(
        geo.viewers.map((v) => [v.lat, v.lng, v.intensity]),
        { radius: 30, blur: 18, minOpacity: 0.3, maxZoom: 8, gradient: HEAT_GRADIENT }
      ).addTo(map);

      // Edge cluster markers.
      const markerLayer = L.layerGroup().addTo(map);
      geo.clusters.forEach((c) => {
        const icon = L.divIcon({
          className: "geo-panel__marker",
          html: `<span class="geo-panel__node" style="box-shadow:0 0 8px rgba(125,207,255,0.9)"></span>`,
          iconSize: [12, 12],
          iconAnchor: [6, 6],
        });
        L.marker([c.lat, c.lng], { icon })
          .addTo(markerLayer)
          .bindTooltip(c.name, { direction: "top", className: "geo-panel__tip", offset: [0, -6] });
      });

      routeLayerRef.current = L.layerGroup();
      pulseLayerRef.current = L.layerGroup();
      leafletRef.current = L;
      mapRef.current = map;
      setReady(true);
    })();

    return () => {
      cancelled = true;
      clearTimers();
      if (mapRef.current) {
        mapRef.current.remove();
        mapRef.current = null;
      }
    };
  }, []);

  // Routing flows + animated pulses, toggled on/off.
  useEffect(() => {
    const L = leafletRef.current;
    const map = mapRef.current;
    if (!L || !map || !ready) return;

    const routeLayer = routeLayerRef.current;
    const pulseLayer = pulseLayerRef.current;
    clearTimers();
    routeLayer.clearLayers();
    pulseLayer.clearLayers();

    if (!showRoutes) {
      map.removeLayer(routeLayer);
      map.removeLayer(pulseLayer);
      return;
    }
    routeLayer.addTo(map);
    pulseLayer.addTo(map);

    geo.flows.forEach((f) => {
      const dist = Math.hypot(f.to[1] - f.from[1], f.to[0] - f.from[0]);
      if (dist < 0.5) return; // local route: viewer served from its own region
      const color = FLOW_COLORS[f.status] ?? FLOW_COLORS.success;
      const pts = arcPoints(f.from, f.to);
      L.polyline(pts, {
        color,
        weight: 1.5,
        opacity: f.status === "degraded" ? 0.55 : 0.8,
        dashArray: "7 6",
        interactive: false,
      }).addTo(routeLayer);

      // Pulse traveling from client to edge.
      let step = 0;
      let pulse = null;
      const id = setInterval(() => {
        const idx = Math.floor((step / 60) * (pts.length - 1));
        const at = pts[idx];
        if (!pulse) {
          pulse = L.circleMarker(at, {
            radius: 3,
            fillColor: color,
            fillOpacity: 0.9,
            stroke: false,
            interactive: false,
          }).addTo(pulseLayer);
        } else {
          pulse.setLatLng(at);
        }
        const t = step / 60;
        pulse.setStyle({ fillOpacity: t < 0.1 ? t / 0.1 : t > 0.9 ? (1 - t) / 0.1 : 0.9 });
        step = (step + 1) % 60;
      }, 50);
      timersRef.current.push(id);
    });
  }, [ready, showRoutes]);

  const resetView = useCallback(() => mapRef.current?.setView([28, 4], 2), []);
  const toggleFullscreen = useCallback(() => {
    setIsFullscreen((v) => !v);
    setTimeout(() => mapRef.current?.invalidateSize(), 310);
  }, []);

  return (
    <div
      className={`geo-panel${isFullscreen ? " geo-panel--fullscreen" : ""}`}
      style={{ height: isFullscreen ? "100%" : height }}
    >
      <div ref={containerRef} className="geo-panel__map" />

      <div className="geo-panel__controls">
        <button
          type="button"
          className="geo-panel__btn"
          onClick={resetView}
          title="Reset view"
          dangerouslySetInnerHTML={{ __html: ICON_HOME }}
        />
        <button
          type="button"
          className="geo-panel__btn"
          onClick={toggleFullscreen}
          title={isFullscreen ? "Exit fullscreen" : "Fullscreen"}
          dangerouslySetInnerHTML={{ __html: isFullscreen ? ICON_MIN : ICON_MAX }}
        />
      </div>

      <div className="geo-panel__legend">
        <button
          type="button"
          className={`geo-panel__toggle${showRoutes ? " geo-panel__toggle--on" : ""}`}
          onClick={() => setShowRoutes((v) => !v)}
        >
          {showRoutes ? "Hide routing" : "Show routing"}
        </button>
        <span className="geo-panel__key">
          <i className="geo-panel__key-dot geo-panel__key-dot--heat" /> viewer demand
        </span>
        <span className="geo-panel__key">
          <i className="geo-panel__key-dot geo-panel__key-dot--edge" /> edge cluster
        </span>
      </div>

      {showHint && !isFullscreen ? (
        <button type="button" className="geo-panel__scroll-hint" onClick={() => setShowHint(false)}>
          Hold <kbd>⌥</kbd> or <kbd>Ctrl</kbd> + scroll to zoom
        </button>
      ) : null}
    </div>
  );
}

export default GeoPanel;

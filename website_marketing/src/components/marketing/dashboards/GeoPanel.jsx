import { useCallback, useEffect, useRef, useState } from "react";
import {
  addRequiredAttribution,
  basemapMapOptions,
  bindModifierScrollZoom,
  featureCollection,
  readRuntimePublicConfig,
  setLayerVisibility,
} from "@frameworks/map-core";
import { geo } from "./fixtures";

const FLOW_COLORS = { success: "rgb(158, 206, 106)", degraded: "rgb(224, 175, 104)" };
const ROUTE_LAYERS = ["geo-routes", "geo-pulses"];

const ICON_HOME = `<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 9l9-7 9 7v11a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/><polyline points="9 22 9 12 15 12 15 22"/></svg>`;
const ICON_MAX = `<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="15 3 21 3 21 9"/><polyline points="9 21 3 21 3 15"/><line x1="21" y1="3" x2="14" y2="10"/><line x1="3" y1="21" x2="10" y2="14"/></svg>`;
const ICON_MIN = `<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="4 14 10 14 10 20"/><polyline points="20 10 14 10 14 4"/><line x1="14" y1="10" x2="21" y2="3"/><line x1="3" y1="21" x2="10" y2="14"/></svg>`;

// Quadratic-bezier geographic arc between two [lat,lng] points.
function arcPoints(from, to, segments = 36) {
  const [lat1, lng1] = from;
  const [lat2, lng2] = to;
  const dx = lng2 - lng1;
  const dy = lat2 - lat1;
  const dist = Math.hypot(dx, dy) || 1;
  const lift = Math.min(dist * 0.2, 26);
  const cLat = (lat1 + lat2) / 2 + (dx / dist) * lift;
  const cLng = (lng1 + lng2) / 2 - (dy / dist) * lift;
  return Array.from({ length: segments + 1 }, (_, index) => {
    const t = index / segments;
    const u = 1 - t;
    return [
      u * u * lng1 + 2 * u * t * cLng + t * t * lng2,
      u * u * lat1 + 2 * u * t * cLat + t * t * lat2,
    ];
  });
}

export function GeoPanel({ height = 440 }) {
  const containerRef = useRef(null);
  const mapRef = useRef(null);
  const animationRef = useRef(null);
  const [ready, setReady] = useState(false);
  const [unavailable, setUnavailable] = useState("");
  const [showRoutes, setShowRoutes] = useState(true);
  const [isFullscreen, setIsFullscreen] = useState(false);
  const [showHint, setShowHint] = useState(true);

  useEffect(() => {
    let cancelled = false;
    let unbindWheel = () => {};
    let popup;

    (async () => {
      let runtime;
      try {
        runtime = readRuntimePublicConfig();
      } catch {
        console.error("Map unavailable: invalid basemap runtime configuration");
        if (!cancelled) setUnavailable("Basemap configuration unavailable");
        return;
      }
      try {
        const [maplibregl, { latLngToCell, cellToBoundary }] = await Promise.all([
          import("maplibre-gl"),
          import("h3-js"),
          import("maplibre-gl/dist/maplibre-gl.css"),
        ]);
        if (cancelled || !containerRef.current || mapRef.current) return;

        const map = new maplibregl.Map({
          container: containerRef.current,
          center: [4, 28],
          zoom: 2,
          minZoom: 2,
          maxZoom: 8,
          scrollZoom: false,
          ...basemapMapOptions(runtime.basemap),
        });
        mapRef.current = map;
        addRequiredAttribution(map, runtime.basemap);
        unbindWheel = bindModifierScrollZoom(map, () => setShowHint(false));

        map.once("load", () => {
          if (cancelled) return;
          const seen = new Set();
          const hexes = [];
          for (const viewer of geo.viewers.filter((value) => value.intensity >= 0.6)) {
            const cell = latLngToCell(viewer.lat, viewer.lng, 3);
            if (seen.has(cell)) continue;
            seen.add(cell);
            const ring = cellToBoundary(cell).map(([lat, lng]) => [lng, lat]);
            ring.push(ring[0]);
            hexes.push({
              type: "Feature",
              properties: { cell },
              geometry: { type: "Polygon", coordinates: [ring] },
            });
          }

          const heat = geo.viewers.map((viewer) => ({
            type: "Feature",
            properties: { intensity: viewer.intensity },
            geometry: { type: "Point", coordinates: [viewer.lng, viewer.lat] },
          }));
          const nodes = geo.clusters.map((cluster) => ({
            type: "Feature",
            properties: { name: cluster.name },
            geometry: { type: "Point", coordinates: [cluster.lng, cluster.lat] },
          }));
          const routePaths = geo.flows
            .filter(
              (flow) => Math.hypot(flow.to[1] - flow.from[1], flow.to[0] - flow.from[0]) >= 0.5
            )
            .map((flow, index) => ({
              id: index,
              status: flow.status,
              points: arcPoints(flow.from, flow.to),
            }));
          const routes = routePaths.map((route) => ({
            type: "Feature",
            properties: { status: route.status },
            geometry: { type: "LineString", coordinates: route.points },
          }));

          map.addSource("geo-hexes", { type: "geojson", data: featureCollection(hexes) });
          map.addLayer({
            id: "geo-hex-fill",
            type: "fill",
            source: "geo-hexes",
            paint: { "fill-color": "rgb(125, 207, 255)", "fill-opacity": 0.05 },
          });
          map.addLayer({
            id: "geo-hex-outline",
            type: "line",
            source: "geo-hexes",
            paint: { "line-color": "rgba(125, 207, 255, 0.5)", "line-width": 1 },
          });

          map.addSource("geo-heat", { type: "geojson", data: featureCollection(heat) });
          map.addLayer({
            id: "geo-heat",
            type: "heatmap",
            source: "geo-heat",
            maxzoom: 8,
            paint: {
              "heatmap-weight": ["get", "intensity"],
              "heatmap-intensity": ["interpolate", ["linear"], ["zoom"], 2, 0.8, 8, 1.5],
              "heatmap-radius": ["interpolate", ["linear"], ["zoom"], 2, 18, 8, 34],
              "heatmap-opacity": 0.82,
              "heatmap-color": [
                "interpolate",
                ["linear"],
                ["heatmap-density"],
                0,
                "rgba(122,162,247,0)",
                0.2,
                "rgba(122,162,247,0.55)",
                0.4,
                "rgba(125,207,255,0.75)",
                0.6,
                "rgba(158,206,106,0.85)",
                0.8,
                "rgba(224,175,104,0.9)",
                1,
                "rgba(247,118,142,0.95)",
              ],
            },
          });

          map.addSource("geo-routes", { type: "geojson", data: featureCollection(routes) });
          map.addLayer({
            id: "geo-routes",
            type: "line",
            source: "geo-routes",
            layout: { "line-cap": "round", "line-join": "round" },
            paint: {
              "line-color": [
                "match",
                ["get", "status"],
                "degraded",
                FLOW_COLORS.degraded,
                FLOW_COLORS.success,
              ],
              "line-width": 1.5,
              "line-opacity": ["match", ["get", "status"], "degraded", 0.55, 0.8],
              "line-dasharray": [3.5, 3],
            },
          });
          map.addSource("geo-pulses", { type: "geojson", data: featureCollection([]) });
          map.addLayer({
            id: "geo-pulses",
            type: "circle",
            source: "geo-pulses",
            paint: {
              "circle-radius": 3,
              "circle-color": [
                "match",
                ["get", "status"],
                "degraded",
                FLOW_COLORS.degraded,
                FLOW_COLORS.success,
              ],
              "circle-opacity": ["get", "opacity"],
            },
          });

          map.addSource("geo-nodes", { type: "geojson", data: featureCollection(nodes) });
          map.addLayer({
            id: "geo-nodes-glow",
            type: "circle",
            source: "geo-nodes",
            paint: {
              "circle-radius": 9,
              "circle-color": "rgba(125,207,255,0.25)",
              "circle-blur": 0.7,
            },
          });
          map.addLayer({
            id: "geo-nodes",
            type: "circle",
            source: "geo-nodes",
            paint: {
              "circle-radius": 5.5,
              "circle-color": "rgb(125,207,255)",
              "circle-stroke-color": "rgba(22,22,30,0.8)",
              "circle-stroke-width": 2,
            },
          });

          popup = new maplibregl.Popup({ closeButton: false, closeOnClick: false, offset: 8 });
          map.on("mouseenter", "geo-nodes", (event) => {
            map.getCanvas().style.cursor = "pointer";
            const feature = event.features?.[0];
            if (!feature) return;
            popup
              .setLngLat(event.lngLat)
              .setText(String(feature.properties?.name ?? "Edge cluster"))
              .addTo(map);
          });
          map.on("mouseleave", "geo-nodes", () => {
            map.getCanvas().style.cursor = "";
            popup?.remove();
          });

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
            const pulses = routePaths.map((route) => {
              const at = route.points[Math.floor(cycle * (route.points.length - 1))];
              return {
                type: "Feature",
                properties: { status: route.status, opacity },
                geometry: { type: "Point", coordinates: at },
              };
            });
            map.getSource("geo-pulses")?.setData(featureCollection(pulses));
            if (!reducedMotion) animationRef.current = requestAnimationFrame(animate);
          };
          animate(performance.now());
          setReady(true);
        });
      } catch {
        console.error("Map unavailable: renderer initialization failed");
        if (!cancelled) setUnavailable("Map renderer unavailable");
      }
    })();

    return () => {
      cancelled = true;
      unbindWheel();
      popup?.remove();
      if (animationRef.current) cancelAnimationFrame(animationRef.current);
      animationRef.current = null;
      mapRef.current?.remove();
      mapRef.current = null;
    };
  }, []);

  useEffect(() => {
    const map = mapRef.current;
    if (map && ready) setLayerVisibility(map, ROUTE_LAYERS, showRoutes);
  }, [ready, showRoutes]);

  const resetView = useCallback(
    () => mapRef.current?.easeTo({ center: [4, 28], zoom: 2, duration: 500 }),
    []
  );
  const toggleFullscreen = useCallback(() => {
    setIsFullscreen((value) => !value);
    setTimeout(() => mapRef.current?.resize(), 310);
  }, []);

  return (
    <div
      className={`geo-panel${isFullscreen ? " geo-panel--fullscreen" : ""}`}
      style={{ height: isFullscreen ? "100%" : height }}
    >
      <div ref={containerRef} className="geo-panel__map" />
      {unavailable ? (
        <div className="geo-panel__unavailable" role="status">
          Basemap unavailable
        </div>
      ) : null}

      <div className="geo-panel__controls">
        <button
          type="button"
          className="geo-panel__btn"
          onClick={resetView}
          aria-label="Reset map view"
          title="Reset view"
          disabled={!ready}
          dangerouslySetInnerHTML={{ __html: ICON_HOME }}
        />
        <button
          type="button"
          className="geo-panel__btn"
          onClick={toggleFullscreen}
          aria-label={isFullscreen ? "Exit fullscreen map" : "Open fullscreen map"}
          title={isFullscreen ? "Exit fullscreen" : "Fullscreen"}
          dangerouslySetInnerHTML={{ __html: isFullscreen ? ICON_MIN : ICON_MAX }}
        />
      </div>

      <div className="geo-panel__legend">
        <button
          type="button"
          className={`geo-panel__toggle${showRoutes ? " geo-panel__toggle--on" : ""}`}
          onClick={() => setShowRoutes((value) => !value)}
          disabled={!ready}
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

      {showHint && !isFullscreen && ready ? (
        <button type="button" className="geo-panel__scroll-hint" onClick={() => setShowHint(false)}>
          Hold <kbd>⌥</kbd> or <kbd>Ctrl</kbd> + scroll to zoom
        </button>
      ) : null}
    </div>
  );
}

export default GeoPanel;

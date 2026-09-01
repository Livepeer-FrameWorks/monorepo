<script lang="ts">
  import { onMount, onDestroy, untrack } from "svelte";
  import { SvelteMap } from "svelte/reactivity";
  import type { Feature } from "geojson";
  import type { GeoJSONSource, Map as MapLibreMap, Popup } from "maplibre-gl";
  import {
    addRequiredAttribution,
    basemapMapOptions,
    bindModifierScrollZoom,
    featureCollection,
    firstBasemapSymbolLayer,
    readRuntimePublicConfig,
    setLayerVisibility,
  } from "@frameworks/map-core";
  import { getIconComponent } from "$lib/iconUtils";
  import { iso2ToIso3, iso3ToIso2, getCountryName } from "$lib/utils/country-names";
  import { samplePath } from "./arc";
  import { palette } from "./theme";

  interface HeatPoint {
    lat: number;
    lng: number;
    intensity: number;
  }
  interface CountryDatum {
    countryCode: string;
    viewerCount: number;
  }
  interface Route {
    from: [number, number];
    to: [number, number];
    status: string;
  }
  interface NodeLoc {
    id: string;
    name: string;
    lat: number;
    lng: number;
    count?: number;
  }
  interface BucketPoly {
    id: string;
    coords: [number, number][];
    kind: "client" | "node";
    stats?: { count?: number; successRate?: number; avgDistance?: number };
  }
  type BaseKind = "heat" | "countries";

  interface Props {
    heat?: HeatPoint[];
    countries?: CountryDatum[];
    routes?: Route[];
    nodes?: NodeLoc[];
    buckets?: BucketPoly[];
    height?: number;
    selectedBucket?: string | null;
    onBucketClick?: (id: string) => void;
    initialView?: "heat" | "countries" | "routes" | "buckets";
    variant?: "audience" | "placement";
  }

  let {
    heat = [],
    countries = [],
    routes = [],
    nodes = [],
    buckets = [],
    height = 400,
    selectedBucket = null,
    onBucketClick,
    initialView = "heat",
    variant = "audience",
  }: Props = $props();

  const HomeIcon = getIconComponent("Home");
  const MaximizeIcon = getIconComponent("Maximize2");
  const MinimizeIcon = getIconComponent("Minimize2");
  let mapContainer = $state<HTMLElement>();
  let map: MapLibreMap | null = null;
  let popup: Popup | null = null;
  let worldFeatures: Feature[] = [];
  let animationFrame: number | null = null;
  let unbindWheel = () => {};
  let ready = $state(false);
  let unavailable = $state(false);
  let base = $state<BaseKind>(
    untrack(() =>
      initialView === "countries" ? "countries" : heat.length > 0 ? "heat" : "countries"
    )
  );
  let showRouting = $state(true);
  let isFullscreen = $state(false);
  let showHint = $state(true);

  const heatAvailable = $derived(heat.length > 0);
  const countriesAvailable = $derived(countries.length > 0);
  const routingAvailable = $derived(routes.length > 0 || buckets.length > 0 || nodes.length > 0);

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
      const [maplibreModule, geo] = await Promise.all([
        import("maplibre-gl"),
        import("$lib/data/countries.geo.json"),
        import("maplibre-gl/dist/maplibre-gl.css"),
      ]);
      if (!mapContainer) return;
      worldFeatures = geo.default.features as Feature[];
      map = new maplibreModule.Map({
        container: mapContainer,
        center: [0, 20],
        zoom: 2,
        minZoom: 1,
        maxZoom: 10,
        scrollZoom: false,
        ...basemapMapOptions(runtime.basemap),
      });
      addRequiredAttribution(map, runtime.basemap);
      unbindWheel = bindModifierScrollZoom(map, () => (showHint = false));
      popup = new maplibreModule.Popup({ closeButton: false, closeOnClick: false, offset: 8 });

      map.once("load", () => {
        if (!map) return;
        initializeLayers(map);
        bindFeatureInteractions(map);
        ready = true;
        drawBase();
        drawArcs();
        drawBuckets();
        drawNodes();
        startPulseAnimation();
      });
    } catch {
      console.error("Map unavailable: renderer initialization failed");
      unavailable = true;
    }
  });

  onDestroy(() => {
    unbindWheel();
    popup?.remove();
    if (animationFrame !== null) cancelAnimationFrame(animationFrame);
    animationFrame = null;
    map?.remove();
    map = null;
  });

  function initializeLayers(currentMap: MapLibreMap) {
    const empty = featureCollection([]);
    const beforeLabels = firstBasemapSymbolLayer(currentMap);
    for (const sourceID of ["countries", "heat", "routes", "pulses", "buckets", "nodes"]) {
      currentMap.addSource(`geoview-${sourceID}`, { type: "geojson", data: empty });
    }

    currentMap.addLayer(
      {
        id: "geoview-countries-fill",
        type: "fill",
        source: "geoview-countries",
        paint: {
          "fill-color": ["get", "color"],
          "fill-opacity": ["get", "opacity"],
        },
      },
      beforeLabels
    );
    currentMap.addLayer(
      {
        id: "geoview-countries-line",
        type: "line",
        source: "geoview-countries",
        paint: { "line-color": "rgba(169,177,214,0.4)", "line-width": 0.5 },
      },
      beforeLabels
    );
    currentMap.addLayer(
      {
        id: "geoview-heat",
        type: "heatmap",
        source: "geoview-heat",
        maxzoom: 10,
        paint: {
          "heatmap-weight": ["get", "intensity"],
          "heatmap-intensity": ["interpolate", ["linear"], ["zoom"], 1, 0.7, 10, 1.7],
          "heatmap-radius": ["interpolate", ["linear"], ["zoom"], 1, 20, 10, 38],
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
      },
      beforeLabels
    );
    currentMap.addLayer(
      {
        id: "geoview-buckets-fill",
        type: "fill",
        source: "geoview-buckets",
        paint: {
          "fill-color": [
            "match",
            ["get", "kind"],
            "client",
            "rgb(125,207,255)",
            "rgb(122,162,247)",
          ],
          "fill-opacity": ["case", ["boolean", ["get", "selected"], false], 0.25, 0.08],
        },
      },
      beforeLabels
    );
    currentMap.addLayer(
      {
        id: "geoview-buckets-line",
        type: "line",
        source: "geoview-buckets",
        paint: {
          "line-color": [
            "match",
            ["get", "kind"],
            "client",
            "rgba(125,207,255,0.6)",
            "rgba(122,162,247,0.6)",
          ],
          "line-width": ["case", ["boolean", ["get", "selected"], false], 2.5, 1],
        },
      },
      beforeLabels
    );
    currentMap.addLayer(
      {
        id: "geoview-routes",
        type: "line",
        source: "geoview-routes",
        layout: { "line-cap": "round", "line-join": "round" },
        paint: {
          "line-color": ["case", ["boolean", ["get", "ok"], false], palette.green, palette.red],
          "line-width": 1.5,
          "line-opacity": ["case", ["boolean", ["get", "ok"], false], 0.75, 0.5],
          "line-dasharray": [3.5, 3],
        },
      },
      beforeLabels
    );
    currentMap.addLayer({
      id: "geoview-pulses",
      type: "circle",
      source: "geoview-pulses",
      paint: {
        "circle-radius": 3,
        "circle-color": ["case", ["boolean", ["get", "ok"], false], palette.green, palette.red],
        "circle-opacity": ["get", "opacity"],
      },
    });
    currentMap.addLayer({
      id: "geoview-nodes-glow",
      type: "circle",
      source: "geoview-nodes",
      paint: {
        "circle-radius": 9,
        "circle-color": "rgba(125,207,255,0.25)",
        "circle-blur": 0.7,
      },
    });
    currentMap.addLayer({
      id: "geoview-nodes",
      type: "circle",
      source: "geoview-nodes",
      paint: {
        "circle-radius": 5.5,
        "circle-color": "rgb(125,207,255)",
        "circle-stroke-color": "rgba(22,22,30,0.8)",
        "circle-stroke-width": 2,
      },
    });
  }

  function bindFeatureInteractions(currentMap: MapLibreMap) {
    for (const layer of ["geoview-countries-fill", "geoview-buckets-fill", "geoview-nodes"]) {
      currentMap.on("mouseenter", layer, (event) => {
        const item = event.features?.[0];
        if (!item || !popup) return;
        currentMap.getCanvas().style.cursor =
          layer === "geoview-buckets-fill" ? "pointer" : "default";
        const properties = item.properties ?? {};
        let label = "";
        if (layer === "geoview-countries-fill" && Number(properties.viewerCount) > 0) {
          label = `${String(properties.name)} · ${Number(properties.viewerCount).toLocaleString()} viewers`;
        } else if (layer === "geoview-buckets-fill" && properties.count != null) {
          label = `${Number(properties.count).toLocaleString()} events`;
        } else if (layer === "geoview-nodes") {
          label = String(properties.label ?? "Edge node");
        }
        if (label) popup.setLngLat(event.lngLat).setText(label).addTo(currentMap);
      });
      currentMap.on("mouseleave", layer, () => {
        currentMap.getCanvas().style.cursor = "";
        popup?.remove();
      });
    }
    currentMap.on("click", "geoview-buckets-fill", (event) => {
      const id = event.features?.[0]?.properties?.id;
      if (typeof id === "string") onBucketClick?.(id);
    });
  }

  function source(id: string): GeoJSONSource | null {
    return (map?.getSource(id) as GeoJSONSource | undefined) ?? null;
  }

  function drawBase() {
    if (!map) return;
    source("geoview-heat")?.setData(
      featureCollection(
        heat.map((point) => ({
          type: "Feature",
          properties: { intensity: point.intensity },
          geometry: { type: "Point", coordinates: [point.lng, point.lat] },
        }))
      )
    );

    const values = new SvelteMap<string, number>();
    for (const datum of countries) {
      const iso3 = iso2ToIso3[datum.countryCode.toUpperCase()];
      if (iso3) values.set(iso3, datum.viewerCount);
    }
    const max = Math.max(...countries.map((datum) => datum.viewerCount), 1);
    const countryFeatures = worldFeatures.map((item) => {
      const iso3 = String(item.id ?? "").toUpperCase();
      const viewerCount = values.get(iso3) ?? 0;
      const intensity = Math.log1p(viewerCount) / Math.log1p(max);
      const color =
        intensity < 0.33
          ? "rgb(115,218,202)"
          : intensity < 0.66
            ? "rgb(122,162,247)"
            : "rgb(255,158,100)";
      return {
        ...item,
        properties: {
          ...(item.properties ?? {}),
          viewerCount,
          name: getCountryName(iso3ToIso2[iso3] || iso3),
          color,
          opacity: viewerCount === 0 ? 0.08 : 0.35,
        },
      } as Feature;
    });
    source("geoview-countries")?.setData(featureCollection(countryFeatures));
    setLayerVisibility(map, ["geoview-heat"], base === "heat");
    setLayerVisibility(
      map,
      ["geoview-countries-fill", "geoview-countries-line"],
      base === "countries"
    );
  }

  function routePaths() {
    return routes.map((route) => ({
      ok: route.status === "success" || route.status === "ok",
      points: samplePath(route.from, route.to).map(([lat, lng]) => [lng, lat]),
    }));
  }

  function drawArcs() {
    const paths = routePaths();
    source("geoview-routes")?.setData(
      featureCollection(
        paths.map((route) => ({
          type: "Feature",
          properties: { ok: route.ok },
          geometry: { type: "LineString", coordinates: route.points },
        }))
      )
    );
    if (map) setLayerVisibility(map, ["geoview-routes", "geoview-pulses"], showRouting);
  }

  function drawBuckets() {
    const items = buckets.map((bucket) => {
      const ring = bucket.coords.map(([lat, lng]) => [lng, lat]);
      if (ring.length > 0) ring.push(ring[0]);
      return {
        type: "Feature" as const,
        properties: {
          id: bucket.id,
          kind: bucket.kind,
          selected: selectedBucket === bucket.id,
          count: bucket.stats?.count ?? null,
        },
        geometry: { type: "Polygon" as const, coordinates: [ring] },
      };
    });
    source("geoview-buckets")?.setData(featureCollection(items));
    if (map) {
      setLayerVisibility(map, ["geoview-buckets-fill", "geoview-buckets-line"], showRouting);
    }
  }

  function drawNodes() {
    const items = nodes.map((node) => ({
      type: "Feature" as const,
      properties: {
        id: node.id,
        label: node.count != null ? `${node.name} (${node.count})` : node.name,
      },
      geometry: { type: "Point" as const, coordinates: [node.lng, node.lat] },
    }));
    source("geoview-nodes")?.setData(featureCollection(items));
    if (map) {
      setLayerVisibility(map, ["geoview-nodes-glow", "geoview-nodes"], showRouting);
    }
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
      const items = routePaths().map((route) => ({
        type: "Feature" as const,
        properties: { ok: route.ok, opacity },
        geometry: {
          type: "Point" as const,
          coordinates: route.points[Math.floor(cycle * (route.points.length - 1))],
        },
      }));
      source("geoview-pulses")?.setData(featureCollection(items));
      if (!reducedMotion) animationFrame = requestAnimationFrame(animate);
    };
    animate(performance.now());
  }

  $effect(() => {
    void base;
    void heat;
    void countries;
    if (ready) drawBase();
  });

  $effect(() => {
    void showRouting;
    void routes;
    if (ready) drawArcs();
  });

  $effect(() => {
    void showRouting;
    void buckets;
    void selectedBucket;
    if (ready) drawBuckets();
  });

  $effect(() => {
    void showRouting;
    void nodes;
    if (ready) drawNodes();
  });

  $effect(() => {
    if (base === "heat" && !heatAvailable && countriesAvailable) base = "countries";
    else if (base === "countries" && !countriesAvailable && heatAvailable) base = "heat";
  });

  function resetView() {
    map?.easeTo({ center: [0, 20], zoom: 2, duration: 500 });
  }

  function toggleFullscreen() {
    isFullscreen = !isFullscreen;
    setTimeout(() => map?.resize(), 310);
  }
</script>

<div
  class="geoview"
  class:geoview--fullscreen={isFullscreen}
  style="height: {isFullscreen ? '100%' : `${height}px`};"
>
  <div bind:this={mapContainer} class="geoview__map"></div>
  {#if unavailable}
    <div class="geoview__unavailable" role="status">Basemap unavailable</div>
  {/if}

  {#if heatAvailable && countriesAvailable}
    <div class="geoview__base">
      <button
        type="button"
        class="geoview__seg"
        class:geoview__seg--on={base === "heat"}
        onclick={() => (base = "heat")}
        disabled={!ready}
      >
        Heatmap
      </button>
      <button
        type="button"
        class="geoview__seg"
        class:geoview__seg--on={base === "countries"}
        onclick={() => (base = "countries")}
        disabled={!ready}
      >
        Countries
      </button>
    </div>
  {/if}

  <div class="geoview__controls">
    <button class="geoview__ctrl" onclick={resetView} title="Reset view" disabled={!ready}
      ><HomeIcon class="w-4 h-4" /></button
    >
    <button
      class="geoview__ctrl"
      onclick={toggleFullscreen}
      title={isFullscreen ? "Exit fullscreen" : "Fullscreen"}
    >
      {#if isFullscreen}<MinimizeIcon class="w-4 h-4" />{:else}<MaximizeIcon class="w-4 h-4" />{/if}
    </button>
  </div>

  {#if showHint && !isFullscreen && ready}
    <button class="geoview__hint" type="button" onclick={() => (showHint = false)}>
      Hold <kbd>⌥</kbd> or <kbd>Ctrl</kbd> + scroll to zoom
    </button>
  {/if}

  <div class="geoview__legend">
    {#if variant === "placement"}
      <span><i class="geoview__lg-dot"></i> edge holding asset</span>
    {:else}
      {#if routingAvailable}
        <button
          type="button"
          class="geoview__toggle"
          class:geoview__toggle--on={showRouting}
          onclick={() => (showRouting = !showRouting)}
          disabled={!ready}
        >
          {showRouting ? "Hide routing" : "Show routing"}
        </button>
      {/if}
      {#if base === "heat"}
        <span><i class="geoview__lg-grad"></i> viewer demand</span>
      {:else}
        <span><i class="geoview__lg-grad geoview__lg-grad--country"></i> viewers / country</span>
      {/if}
      {#if showRouting && routingAvailable}
        <span><i class="geoview__lg-line geoview__lg-line--ok"></i> served</span>
        <span><i class="geoview__lg-dot"></i> edge node</span>
      {/if}
    {/if}
  </div>
</div>

<style>
  .geoview {
    position: relative;
    width: 100%;
    border-radius: 0.5rem;
    overflow: hidden;
    background: rgb(22, 22, 30);
  }
  .geoview--fullscreen {
    position: fixed;
    inset: 0;
    z-index: 50;
    border-radius: 0;
    height: 100% !important;
  }
  .geoview__map {
    width: 100%;
    height: 100%;
    z-index: 1;
  }
  .geoview :global(.maplibregl-map) {
    background: rgb(22, 22, 30) !important;
    font-family: inherit;
  }
  .geoview :global(.frameworks-map-attribution) {
    padding: 0.1rem 0.3rem;
    border-radius: 0.2rem;
    background: rgb(22 22 30 / 78%);
    color: rgb(169 177 214 / 80%);
    font-size: 0.58rem;
    line-height: 1.25;
  }
  .geoview :global(.frameworks-map-attribution a) {
    color: inherit;
    text-decoration: none;
  }
  .geoview :global(.maplibregl-popup-content) {
    padding: 0.35rem 0.55rem;
    border: 1px solid hsl(var(--tn-blue) / 0.3);
    border-radius: 0.4rem;
    background: hsl(var(--tn-bg-highlight));
    color: hsl(var(--tn-fg));
    font-size: 0.72rem;
  }
  .geoview :global(.maplibregl-popup-tip) {
    border-top-color: hsl(var(--tn-bg-highlight));
  }
  .geoview__unavailable {
    position: absolute;
    inset: 0;
    z-index: 4;
    display: grid;
    place-items: center;
    color: hsl(var(--tn-fg-dark));
    background: rgb(22, 22, 30);
    font-size: 0.8rem;
  }
  .geoview__legend {
    position: absolute;
    bottom: 0.75rem;
    right: 0.75rem;
    z-index: 18;
    display: flex;
    align-items: center;
    gap: 0.6rem;
    padding: 0.3rem 0.6rem;
    border-radius: 0.4rem;
    font-size: 0.66rem;
    color: hsl(var(--tn-fg-dark));
    background: hsl(var(--tn-bg-dark) / 0.82);
    border: 1px solid hsl(var(--tn-blue) / 0.22);
    backdrop-filter: blur(6px);
  }
  .geoview__legend span {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
  }
  .geoview__lg-grad {
    width: 3rem;
    height: 0.45rem;
    border-radius: 999px;
    background: linear-gradient(
      90deg,
      rgb(122, 162, 247),
      rgb(125, 207, 255),
      rgb(158, 206, 106),
      rgb(224, 175, 104),
      rgb(247, 118, 142)
    );
  }
  .geoview__lg-grad--country {
    background: linear-gradient(90deg, rgb(115, 218, 202), rgb(122, 162, 247), rgb(255, 158, 100));
  }
  .geoview__lg-line {
    width: 1.1rem;
    height: 0.14rem;
    border-radius: 999px;
  }
  .geoview__lg-line--ok {
    background: hsl(var(--tn-green));
  }
  .geoview__lg-dot {
    width: 0.55rem;
    height: 0.55rem;
    border-radius: 999px;
    background: hsl(var(--tn-cyan));
    box-shadow: 0 0 6px hsl(var(--tn-cyan));
  }
  .geoview__toggle {
    padding: 0.15rem 0.5rem;
    border-radius: 0.3rem;
    font-size: 0.66rem;
    font-weight: 600;
    color: hsl(var(--tn-fg-dark));
    background: transparent;
    border: 1px solid hsl(var(--tn-blue) / 0.3);
    cursor: pointer;
  }
  .geoview__toggle--on {
    background: hsl(var(--tn-green) / 0.16);
    border-color: hsl(var(--tn-green) / 0.4);
    color: hsl(var(--tn-green));
  }
  .geoview__base {
    position: absolute;
    top: 0.75rem;
    left: 0.75rem;
    z-index: 20;
    display: flex;
    gap: 0.2rem;
    padding: 0.2rem;
    border-radius: 0.5rem;
    background: hsl(var(--tn-bg-dark) / 0.82);
    border: 1px solid hsl(var(--tn-blue) / 0.22);
    backdrop-filter: blur(6px);
  }
  .geoview__seg {
    padding: 0.2rem 0.7rem;
    border-radius: 0.35rem;
    font-size: 0.72rem;
    font-weight: 600;
    color: hsl(var(--tn-fg-dark));
    background: transparent;
    border: none;
    cursor: pointer;
  }
  .geoview__seg--on {
    background: hsl(var(--tn-blue) / 0.18);
    color: hsl(var(--tn-blue));
  }
  .geoview__controls {
    position: absolute;
    top: 0.75rem;
    right: 0.75rem;
    z-index: 20;
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
  }
  .geoview__ctrl {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 2rem;
    height: 2rem;
    border-radius: 0.375rem;
    background: hsl(var(--tn-bg-highlight) / 0.9);
    border: 1px solid hsl(var(--tn-blue) / 0.28);
    color: hsl(var(--tn-fg-dark));
    cursor: pointer;
  }
  .geoview__ctrl:hover {
    color: hsl(var(--tn-fg));
  }
  .geoview__ctrl:disabled,
  .geoview__seg:disabled,
  .geoview__toggle:disabled {
    cursor: not-allowed;
    opacity: 0.5;
  }
  .geoview__hint {
    position: absolute;
    bottom: 0.75rem;
    left: 50%;
    transform: translateX(-50%);
    z-index: 15;
    padding: 0.35rem 0.7rem;
    border-radius: 0.375rem;
    font-size: 0.72rem;
    background: hsl(var(--tn-bg-highlight) / 0.92);
    border: 1px solid hsl(var(--tn-blue) / 0.22);
    color: hsl(var(--tn-fg-dark));
    cursor: pointer;
  }
  .geoview__hint kbd {
    display: inline-block;
    padding: 0.05rem 0.3rem;
    border-radius: 0.25rem;
    background: hsl(var(--tn-blue) / 0.2);
    font-size: 0.68rem;
  }
</style>

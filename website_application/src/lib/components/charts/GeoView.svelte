<script lang="ts">
  import { onMount, onDestroy, untrack } from "svelte";
  import { SvelteMap } from "svelte/reactivity";
  import type { Map as LeafletMap, LayerGroup, GeoJSON as LeafletGeoJSON, Layer } from "leaflet";
  import type { Feature } from "geojson";
  import { getIconComponent } from "$lib/iconUtils";
  import { iso2ToIso3, iso3ToIso2, getCountryName } from "$lib/utils/country-names";
  import { samplePath } from "./arc";
  import { palette, heatGradient } from "./theme";
  import "leaflet/dist/leaflet.css";

  // One layered geo view for the audience analytics maps, with a layer toggle:
  // viewer-demand heat, country choropleth, client-to-edge routes, and H3 buckets.
  // Driven by the audience geographic + routing queries.
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
  type ViewKind = "heat" | "countries" | "routes" | "buckets";

  interface Props {
    heat?: HeatPoint[];
    countries?: CountryDatum[];
    routes?: Route[];
    nodes?: NodeLoc[];
    buckets?: BucketPoly[];
    height?: number;
    selectedBucket?: string | null;
    onBucketClick?: (id: string) => void;
    initialView?: ViewKind;
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
  }: Props = $props();

  const TILE = "https://{s}.basemaps.cartocdn.com/dark_all/{z}/{x}/{y}{r}.png";
  const HomeIcon = getIconComponent("Home");
  const MaximizeIcon = getIconComponent("Maximize2");
  const MinimizeIcon = getIconComponent("Minimize2");

  let mapContainer = $state<HTMLElement>();
  let map: LeafletMap | null = null;
  let L: typeof import("leaflet") | null = null;
  let worldFeatures: Feature[] = [];
  let ready = $state(false);

  let view = $state<ViewKind>(untrack(() => initialView));
  let isFullscreen = $state(false);
  let showHint = $state(true);

  // One layer group per concern; only the active view's group is on the map.
  let heatGroup: LayerGroup | null = null;
  let routeGroup: LayerGroup | null = null;
  let geoLayer: LeafletGeoJSON | null = null;

  const AVAILABLE = $derived<{ id: ViewKind; label: string; on: boolean }[]>([
    { id: "heat", label: "Heat", on: heat.length > 0 },
    { id: "countries", label: "Countries", on: countries.length > 0 },
    { id: "routes", label: "Routes", on: routes.length > 0 },
    { id: "buckets", label: "Buckets", on: buckets.length > 0 },
  ]);

  onMount(async () => {
    const mod = await import("leaflet");
    L = mod.default;
    await import("leaflet.heat");
    const geo = await import("$lib/data/countries.geo.json");
    worldFeatures = geo.default.features as Feature[];
    if (mapContainer) initMap();
  });

  onDestroy(() => {
    if (map) {
      map.remove();
      map = null;
    }
  });

  function initMap() {
    if (!L || !mapContainer) return;
    map = L.map(mapContainer, {
      center: [20, 0],
      zoom: 2,
      minZoom: 1,
      maxZoom: 10,
      zoomControl: false,
      attributionControl: false,
      scrollWheelZoom: false,
    });
    L.tileLayer(TILE, { maxZoom: 19, subdomains: "abcd" }).addTo(map);
    mapContainer.addEventListener(
      "wheel",
      (e) => {
        if (!map) return;
        if (e.altKey || e.ctrlKey || e.metaKey) {
          e.preventDefault();
          map.scrollWheelZoom.enable();
          showHint = false;
        } else {
          map.scrollWheelZoom.disable();
        }
      },
      { passive: false }
    );
    ready = true;
    draw();
  }

  function clearLayers() {
    if (!map) return;
    if (heatGroup) {
      map.removeLayer(heatGroup);
      heatGroup = null;
    }
    if (routeGroup) {
      map.removeLayer(routeGroup);
      routeGroup = null;
    }
    if (geoLayer) {
      map.removeLayer(geoLayer);
      geoLayer = null;
    }
  }

  function drawNodes(group: LayerGroup) {
    if (!L) return;
    nodes.forEach((n) => {
      const icon = L!.divIcon({
        className: "geoview__marker",
        html: `<span class="geoview__node"></span>`,
        iconSize: [12, 12],
        iconAnchor: [6, 6],
      });
      L!
        .marker([n.lat, n.lng], { icon })
        .addTo(group)
        .bindTooltip(`${n.name} (${n.count ?? 0})`, {
          direction: "top",
          className: "dark-tooltip",
        });
    });
  }

  function draw() {
    if (!map || !L || !ready) return;
    clearLayers();

    if (view === "heat") {
      heatGroup = L.layerGroup().addTo(map);
      // leaflet.heat augments L.heatLayer at runtime
      (L as typeof L & { heatLayer: (pts: [number, number, number][], opts: object) => Layer })
        .heatLayer(
          heat.map((p) => [p.lat, p.lng, p.intensity]),
          { radius: 30, blur: 18, minOpacity: 0.3, maxZoom: 10, gradient: heatGradient }
        )
        .addTo(heatGroup);
    } else if (view === "countries") {
      const valueMap = new SvelteMap<string, number>();
      countries.forEach((d) => {
        const iso3 = iso2ToIso3[d.countryCode.toUpperCase()];
        if (iso3) valueMap.set(iso3, d.viewerCount);
      });
      const maxVal = Math.max(...countries.map((d) => d.viewerCount), 1);
      const color = (v: number) => {
        const t = Math.log1p(v) / Math.log1p(maxVal);
        if (t < 0.33) return "rgba(115, 218, 202, 0.45)";
        if (t < 0.66) return "rgba(122, 162, 247, 0.55)";
        return "rgba(255, 158, 100, 0.6)";
      };
      geoLayer = L.geoJSON(worldFeatures as GeoJSON.Feature[], {
        style: (f) => {
          const iso3 = String(f?.id ?? "").toUpperCase();
          const val = valueMap.get(iso3) || 0;
          return {
            fillColor: color(val),
            fillOpacity: val === 0 ? 0.08 : 0.35,
            weight: 0.4,
            color: "rgba(169, 177, 214, 0.4)",
          };
        },
        onEachFeature: (f: Feature, layer: Layer) => {
          const iso3 = String(f?.id ?? "").toUpperCase();
          const val = valueMap.get(iso3) || 0;
          if (val > 0) {
            layer.bindTooltip(
              `${getCountryName(iso3ToIso2[iso3] || iso3)}<br>${val.toLocaleString()} viewers`,
              {
                direction: "top",
                className: "dark-tooltip",
              }
            );
          }
        },
      }).addTo(map);
    } else if (view === "routes") {
      routeGroup = L.layerGroup().addTo(map);
      routes.forEach((r) => {
        const ok = r.status === "success" || r.status === "ok";
        L!
          .polyline(samplePath(r.from, r.to), {
            color: ok ? palette.green : palette.red,
            weight: 1.5,
            opacity: ok ? 0.7 : 0.5,
            dashArray: "7 6",
            interactive: false,
          })
          .addTo(routeGroup!);
      });
      drawNodes(routeGroup);
    } else if (view === "buckets") {
      routeGroup = L.layerGroup().addTo(map);
      buckets.forEach((b) => {
        const isClient = b.kind === "client";
        const selected = selectedBucket === b.id;
        const poly = L!
          .polygon(b.coords, {
            color: isClient ? "rgba(125, 207, 255, 0.6)" : "rgba(122, 162, 247, 0.6)",
            weight: selected ? 2.5 : 1,
            fillColor: isClient ? "rgb(125, 207, 255)" : "rgb(122, 162, 247)",
            fillOpacity: selected ? 0.25 : 0.08,
          })
          .addTo(routeGroup!);
        if (b.stats?.count != null) {
          poly.bindTooltip(`${b.stats.count} events`, {
            direction: "top",
            className: "dark-tooltip",
          });
        }
        poly.on("click", () => onBucketClick?.(b.id));
      });
      drawNodes(routeGroup);
    }
  }

  // Redraw on view change or data change once the map exists.
  $effect(() => {
    void view;
    void heat;
    void countries;
    void routes;
    void buckets;
    void selectedBucket;
    if (ready) draw();
  });

  function resetView() {
    map?.setView([20, 0], 2);
  }
  function toggleFullscreen() {
    isFullscreen = !isFullscreen;
    setTimeout(() => map?.invalidateSize(), 310);
  }
</script>

<div
  class="geoview"
  class:geoview--fullscreen={isFullscreen}
  style="height: {isFullscreen ? '100%' : `${height}px`};"
>
  <div bind:this={mapContainer} class="geoview__map"></div>

  <div class="geoview__layers">
    {#each AVAILABLE.filter((a) => a.on) as a (a.id)}
      <button
        type="button"
        class="geoview__layer-btn"
        class:geoview__layer-btn--on={view === a.id}
        onclick={() => (view = a.id)}
      >
        {a.label}
      </button>
    {/each}
  </div>

  <div class="geoview__controls">
    <button class="geoview__ctrl" onclick={resetView} title="Reset view"
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

  {#if showHint && !isFullscreen}
    <button class="geoview__hint" type="button" onclick={() => (showHint = false)}>
      Hold <kbd>⌥</kbd> or <kbd>Ctrl</kbd> + scroll to zoom
    </button>
  {/if}

  <div class="geoview__legend">
    {#if view === "heat" || view === "countries"}
      <span class="geoview__lg-grad"></span>
      <span>fewer</span>
      <span>more viewers</span>
    {:else if view === "routes"}
      <span><i class="geoview__lg-line geoview__lg-line--ok"></i> success</span>
      <span><i class="geoview__lg-line geoview__lg-line--fail"></i> failed</span>
      <span><i class="geoview__lg-dot"></i> edge node</span>
    {:else}
      <span><i class="geoview__lg-sq geoview__lg-sq--client"></i> client</span>
      <span><i class="geoview__lg-sq geoview__lg-sq--node"></i> node</span>
      <span><i class="geoview__lg-dot"></i> edge node</span>
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
  .geoview__lg-line {
    width: 1.1rem;
    height: 0.14rem;
    border-radius: 999px;
  }
  .geoview__lg-line--ok {
    background: hsl(var(--tn-green));
  }
  .geoview__lg-line--fail {
    background: hsl(var(--tn-red));
  }
  .geoview__lg-dot {
    width: 0.55rem;
    height: 0.55rem;
    border-radius: 999px;
    background: hsl(var(--tn-cyan));
    box-shadow: 0 0 6px hsl(var(--tn-cyan));
  }
  .geoview__lg-sq {
    width: 0.6rem;
    height: 0.6rem;
    border-radius: 2px;
  }
  .geoview__lg-sq--client {
    background: rgba(125, 207, 255, 0.5);
    border: 1px solid rgb(125, 207, 255);
  }
  .geoview__lg-sq--node {
    background: rgba(122, 162, 247, 0.5);
    border: 1px solid rgb(122, 162, 247);
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
  .geoview :global(.leaflet-container) {
    background: rgb(22, 22, 30) !important;
  }
  :global(.geoview__node) {
    display: block;
    width: 11px;
    height: 11px;
    border-radius: 999px;
    background: hsl(var(--tn-cyan));
    border: 2px solid rgba(22, 22, 30, 0.8);
    box-shadow: 0 0 8px hsl(var(--tn-cyan) / 0.9);
  }
  .geoview__layers {
    position: absolute;
    top: 0.75rem;
    left: 0.75rem;
    z-index: 20;
    display: flex;
    gap: 0.25rem;
    padding: 0.2rem;
    border-radius: 0.5rem;
    background: hsl(var(--tn-bg-dark) / 0.82);
    border: 1px solid hsl(var(--tn-blue) / 0.22);
    backdrop-filter: blur(6px);
  }
  .geoview__layer-btn {
    padding: 0.2rem 0.6rem;
    border-radius: 0.35rem;
    font-size: 0.72rem;
    font-weight: 600;
    color: hsl(var(--tn-fg-dark));
    background: transparent;
    border: none;
    cursor: pointer;
  }
  .geoview__layer-btn--on {
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

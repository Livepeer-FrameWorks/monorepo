<script lang="ts">
  import { onMount, onDestroy, untrack } from "svelte";
  import { SvelteMap } from "svelte/reactivity";
  import type {
    Map as LeafletMap,
    LayerGroup,
    GeoJSON as LeafletGeoJSON,
    Layer,
    CircleMarker,
  } from "leaflet";
  import type { Feature } from "geojson";
  import { getIconComponent } from "$lib/iconUtils";
  import { iso2ToIso3, iso3ToIso2, getCountryName } from "$lib/utils/country-names";
  import { samplePath } from "./arc";
  import { palette, heatGradient } from "./theme";
  import "leaflet/dist/leaflet.css";

  // One alive audience map. A base layer (viewer-demand heat OR country choropleth,
  // mutually exclusive) with a routing overlay toggled on top: curved client-to-edge
  // arcs with animated traveling pulses, H3 routing buckets, and glowing edge nodes.
  // Mirrors the marketing GeoPanel; driven by the audience geographic + routing queries.
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
    // Only "countries" flips the default base off heat.
    initialView?: "heat" | "countries" | "routes" | "buckets";
    // "audience" (default): viewer heat/countries + routing overlay. "placement":
    // a plain node map (edges holding an asset) — no base legend or routing toggle.
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

  const TILE = "https://{s}.basemaps.cartocdn.com/dark_all/{z}/{x}/{y}{r}.png";
  const HomeIcon = getIconComponent("Home");
  const MaximizeIcon = getIconComponent("Maximize2");
  const MinimizeIcon = getIconComponent("Minimize2");

  let mapContainer = $state<HTMLElement>();
  let map: LeafletMap | null = null;
  let L: typeof import("leaflet") | null = null;
  let worldFeatures: Feature[] = [];
  let ready = $state(false);

  // Mutually-exclusive base; routing rides on top.
  let base = $state<BaseKind>(
    untrack(() =>
      initialView === "countries" ? "countries" : heat.length > 0 ? "heat" : "countries"
    )
  );
  let showRouting = $state(true);
  let isFullscreen = $state(false);
  let showHint = $state(true);

  // Base layer (only one at a time) plus one group per routing concern so bucket
  // reselection doesn't restart the pulse animation.
  let heatGroup: LayerGroup | null = null;
  let geoLayer: LeafletGeoJSON | null = null;
  let routeGroup: LayerGroup | null = null;
  let pulseGroup: LayerGroup | null = null;
  let bucketGroup: LayerGroup | null = null;
  let nodeGroup: LayerGroup | null = null;
  let pulseTimers: ReturnType<typeof setInterval>[] = [];

  const heatAvailable = $derived(heat.length > 0);
  const countriesAvailable = $derived(countries.length > 0);
  const routingAvailable = $derived(routes.length > 0 || buckets.length > 0 || nodes.length > 0);

  onMount(async () => {
    const mod = await import("leaflet");
    L = mod.default;
    await import("leaflet.heat");
    const geo = await import("$lib/data/countries.geo.json");
    worldFeatures = geo.default.features as Feature[];
    if (mapContainer) initMap();
  });

  onDestroy(() => {
    clearPulses();
    if (map) {
      map.remove();
      map = null;
    }
  });

  function clearPulses() {
    pulseTimers.forEach(clearInterval);
    pulseTimers = [];
  }

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
    drawBase();
    drawArcs();
    drawBuckets();
    drawNodes();
  }

  function drawBase() {
    if (!map || !L) return;
    if (heatGroup) {
      map.removeLayer(heatGroup);
      heatGroup = null;
    }
    if (geoLayer) {
      map.removeLayer(geoLayer);
      geoLayer = null;
    }

    if (base === "heat") {
      heatGroup = L.layerGroup().addTo(map);
      // leaflet.heat augments L.heatLayer at runtime.
      (L as typeof L & { heatLayer: (pts: [number, number, number][], opts: object) => Layer })
        .heatLayer(
          heat.map((p) => [p.lat, p.lng, p.intensity]),
          { radius: 30, blur: 18, minOpacity: 0.3, maxZoom: 10, gradient: heatGradient }
        )
        .addTo(heatGroup);
    } else {
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
              { direction: "top", className: "dark-tooltip" }
            );
          }
        },
      }).addTo(map);
    }
  }

  function drawArcs() {
    if (!map || !L) return;
    clearPulses();
    if (routeGroup) {
      map.removeLayer(routeGroup);
      routeGroup = null;
    }
    if (pulseGroup) {
      map.removeLayer(pulseGroup);
      pulseGroup = null;
    }
    if (!showRouting || routes.length === 0) return;

    routeGroup = L.layerGroup().addTo(map);
    pulseGroup = L.layerGroup().addTo(map);
    routes.forEach((r) => {
      const ok = r.status === "success" || r.status === "ok";
      const color = ok ? palette.green : palette.red;
      const pts = samplePath(r.from, r.to);
      L!
        .polyline(pts, {
          color,
          weight: 1.5,
          opacity: ok ? 0.75 : 0.5,
          dashArray: "7 6",
          interactive: false,
        })
        .addTo(routeGroup!);

      // Pulse traveling client -> edge along the visible arc, fading at the ends.
      let step = 0;
      let pulse: CircleMarker | null = null;
      const id = setInterval(() => {
        if (!L || !pulseGroup) return;
        const idx = Math.floor((step / 60) * (pts.length - 1));
        const at = pts[idx];
        if (!pulse) {
          pulse = L.circleMarker(at, {
            radius: 3,
            fillColor: color,
            fillOpacity: 0.9,
            stroke: false,
            interactive: false,
          }).addTo(pulseGroup);
        } else {
          pulse.setLatLng(at);
        }
        const t = step / 60;
        pulse.setStyle({ fillOpacity: t < 0.1 ? t / 0.1 : t > 0.9 ? (1 - t) / 0.1 : 0.9 });
        step = (step + 1) % 60;
      }, 50);
      pulseTimers.push(id);
    });
  }

  function drawBuckets() {
    if (!map || !L) return;
    if (bucketGroup) {
      map.removeLayer(bucketGroup);
      bucketGroup = null;
    }
    if (!showRouting || buckets.length === 0) return;

    bucketGroup = L.layerGroup().addTo(map);
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
        .addTo(bucketGroup!);
      if (b.stats?.count != null) {
        poly.bindTooltip(`${b.stats.count} events`, {
          direction: "top",
          className: "dark-tooltip",
        });
      }
      poly.on("click", () => onBucketClick?.(b.id));
    });
  }

  function drawNodes() {
    if (!map || !L) return;
    if (nodeGroup) {
      map.removeLayer(nodeGroup);
      nodeGroup = null;
    }
    if (!showRouting || nodes.length === 0) return;

    nodeGroup = L.layerGroup().addTo(map);
    nodes.forEach((n) => {
      const icon = L!.divIcon({
        className: "geoview__marker",
        html: `<span class="geoview__node"></span>`,
        iconSize: [12, 12],
        iconAnchor: [6, 6],
      });
      L!
        .marker([n.lat, n.lng], { icon })
        .addTo(nodeGroup!)
        .bindTooltip(n.count != null ? `${n.name} (${n.count})` : n.name, {
          direction: "top",
          className: "dark-tooltip",
        });
    });
  }

  // Redraw the base when its layer or data changes.
  $effect(() => {
    void base;
    void heat;
    void countries;
    if (ready) drawBase();
  });

  // Arcs + pulses depend only on routing toggle + route data (not bucket selection),
  // so reselecting a bucket never restarts the animation.
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

  // If the current base loses its data, fall back to the other one.
  $effect(() => {
    if (base === "heat" && !heatAvailable && countriesAvailable) base = "countries";
    else if (base === "countries" && !countriesAvailable && heatAvailable) base = "heat";
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

  <!-- Base toggle: heatmap vs countries (mutually exclusive) -->
  {#if heatAvailable && countriesAvailable}
    <div class="geoview__base">
      <button
        type="button"
        class="geoview__seg"
        class:geoview__seg--on={base === "heat"}
        onclick={() => (base = "heat")}
      >
        Heatmap
      </button>
      <button
        type="button"
        class="geoview__seg"
        class:geoview__seg--on={base === "countries"}
        onclick={() => (base = "countries")}
      >
        Countries
      </button>
    </div>
  {/if}

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
    {#if variant === "placement"}
      <span><i class="geoview__lg-dot"></i> edge holding asset</span>
    {:else}
      {#if routingAvailable}
        <button
          type="button"
          class="geoview__toggle"
          class:geoview__toggle--on={showRouting}
          onclick={() => (showRouting = !showRouting)}
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

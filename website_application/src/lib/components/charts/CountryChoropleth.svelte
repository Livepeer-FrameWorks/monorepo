<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { SvelteMap } from "svelte/reactivity";
  import type { Map as LeafletMap, GeoJSON, Layer } from "leaflet";
  import { getIconComponent } from "$lib/iconUtils";
  import { iso2ToIso3, iso3ToIso2, getCountryName } from "$lib/utils/country-names";

  // Icons for controls
  const MaximizeIcon = getIconComponent("Maximize2");
  const MinimizeIcon = getIconComponent("Minimize2");
  const HomeIcon = getIconComponent("Home");

  interface CountryDatum {
    countryCode: string;
    viewerCount: number;
  }

  interface Props {
    data: CountryDatum[];
    height?: number;
    tileLayerUrl?: string;
  }

  let {
    data = [],
    height = 320,
    tileLayerUrl = "https://{s}.basemaps.cartocdn.com/dark_all/{z}/{x}/{y}{r}.png",
  }: Props = $props();

  let mapContainer: HTMLElement;
  let map: LeafletMap | null = null;
  let geoLayer: GeoJSON | null = null;
  let L: typeof import("leaflet") | null = null;

  let worldFeatures: GeoJSON.Feature[] = [];

  // UX state
  let isFullscreen = $state(false);
  let showScrollHint = $state(true);
  const defaultCenter: [number, number] = [20, 0];
  const defaultZoom = 2;

  onMount(async () => {
    const leafletModule = await import("leaflet");
    L = leafletModule.default;

    const geoJsonData = await import("$lib/data/countries.geo.json");
    worldFeatures = geoJsonData.default.features;

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
      center: defaultCenter,
      zoom: defaultZoom,
      minZoom: 1,
      maxZoom: 6,
      zoomControl: false,
      attributionControl: false,
      scrollWheelZoom: false, // Disabled by default - use modifier key
    });

    L.tileLayer(tileLayerUrl, {
      maxZoom: 19,
      subdomains: "abcd",
    }).addTo(map);

    // Enable scroll zoom only with modifier key
    mapContainer.addEventListener("wheel", handleWheel, { passive: false });

    drawChoropleth();
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
    setTimeout(() => map?.invalidateSize(), 310);
  }

  function resetView() {
    map?.setView(defaultCenter, defaultZoom);
  }

  function drawChoropleth() {
    if (!map || !L) return;

    if (geoLayer) {
      map.removeLayer(geoLayer);
      geoLayer = null;
    }

    if (!data.length || !worldFeatures.length) return;

    // Build value map keyed by ISO3 code (what GeoJSON uses)
    const valueMap = new SvelteMap<string, number>();
    data.forEach((d) => {
      const iso3 = iso2ToIso3[d.countryCode.toUpperCase()];
      if (iso3) {
        valueMap.set(iso3, d.viewerCount);
      }
    });

    const maxVal = Math.max(...data.map((d) => d.viewerCount), 1);

    const getColor = (v: number) => {
      const t = Math.log1p(v) / Math.log1p(maxVal);
      // gradient teal -> primary -> orange
      if (t < 0.33) return "rgba(20, 184, 166, 0.45)";
      if (t < 0.66) return "rgba(59, 130, 246, 0.55)";
      return "rgba(249, 115, 22, 0.6)";
    };

    geoLayer = L.geoJSON(worldFeatures as GeoJSON.Feature[], {
      style: (feature: GeoJSON.Feature | undefined) => {
        // GeoJSON uses 3-letter ISO code as feature.id (e.g., "USA")
        const iso3 = (feature?.id || "").toUpperCase();
        const val = valueMap.get(iso3) || 0;
        return {
          fillColor: getColor(val),
          fillOpacity: val === 0 ? 0.08 : 0.35,
          weight: 0.4,
          color: "rgba(148, 163, 184, 0.4)",
        };
      },
      onEachFeature: (feature: GeoJSON.Feature, layer: Layer) => {
        const iso3 = (feature?.id || "").toUpperCase();
        const iso2 = iso3ToIso2[iso3] || iso3;
        const val = valueMap.get(iso3) || 0;
        const name = getCountryName(iso2);
        if (val > 0) {
          layer.bindTooltip(`${name}<br>${val.toLocaleString()} viewers`, {
            direction: "top",
            className: "dark-tooltip",
          });
        }
      },
    }).addTo(map);
  }

  $effect(() => {
    if (map) {
      drawChoropleth();
    }
  });
</script>

<div
  class="map-wrapper"
  class:map-wrapper--fullscreen={isFullscreen}
  style="height: {isFullscreen ? '100%' : `${height}px`};"
>
  {#if data.length === 0}
    <div class="empty-state">
      <span class="text-muted-foreground text-sm">No country data</span>
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
    inset: 0;
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
</style>

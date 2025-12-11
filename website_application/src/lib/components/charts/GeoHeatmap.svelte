<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import type { Map as LeafletMap } from "leaflet";
  import { getIconComponent } from "$lib/iconUtils";

  // Leaflet is client-side only
  let L: any;
  let leafletHeat: any;

  // Icons for controls
  const MaximizeIcon = getIconComponent("Maximize2");
  const MinimizeIcon = getIconComponent("Minimize2");
  const HomeIcon = getIconComponent("Home");

  interface HeatmapPoint {
    lat: number;
    lng: number;
    intensity: number; // 0.0 to 1.0
  }

  interface Props {
    data: HeatmapPoint[];
    height?: number;
    zoom?: number;
    center?: [number, number];
    tileLayerUrl?: string;
  }

  let {
    data = [],
    height = 400,
    zoom = 2,
    center = [20, 0],
    tileLayerUrl = "https://{s}.basemaps.cartocdn.com/dark_all/{z}/{x}/{y}{r}.png"
  }: Props = $props();

  let mapContainer = $state<HTMLElement>();
  let map: LeafletMap | null = null;
  let heatLayer: any = null;

  // UX state
  let isFullscreen = $state(false);
  let showScrollHint = $state(true);

  onMount(async () => {
    // Dynamic import for client-side only
    const leafletModule = await import("leaflet");
    L = leafletModule.default;
    await import("leaflet.heat");

    // Fix for default marker icons if we were using markers (we aren't, but good practice)
    // delete (L.Icon.Default.prototype as any)._getIconUrl;
    // L.Icon.Default.mergeOptions({
    //   iconRetinaUrl: '/marker-icon-2x.png',
    //   iconUrl: '/marker-icon.png',
    //   shadowUrl: '/marker-shadow.png',
    // });

    if (mapContainer) {
      initMap();
    }
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
      center: center,
      zoom: zoom,
      minZoom: 2,
      maxZoom: 10,
      zoomControl: false,
      attributionControl: false,
      scrollWheelZoom: false, // Disabled by default - use modifier key
    });

    // Dark theme map tiles
    L.tileLayer(tileLayerUrl, {
      maxZoom: 19,
      subdomains: 'abcd',
    }).addTo(map);

    // Enable scroll zoom only with modifier key
    mapContainer.addEventListener('wheel', handleWheel, { passive: false });

    updateHeatLayer();
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
    map?.setView(center, zoom);
  }

  function updateHeatLayer() {
    if (!map || !L) return;

    // Remove existing layer if any
    if (heatLayer) {
      map.removeLayer(heatLayer);
    }

    if (data.length === 0) return;

    // Convert data to format expected by leaflet.heat: [lat, lng, intensity]
    const points = data.map(p => [p.lat, p.lng, p.intensity]);

    // Create heat layer with our brand colors
    // Gradient: blue -> cyan -> green -> yellow -> red
    heatLayer = L.heatLayer(points, {
      radius: 32,
      blur: 18,
      minOpacity: 0.32,
      maxOpacity: 0.9,
      maxZoom: 10,
      gradient: {
        0.05: "rgba(59, 130, 246, 0.55)", // Primary base shows up sooner
        0.25: "rgba(6, 182, 212, 0.75)",  // Cyan
        0.45: "rgba(34, 197, 94, 0.85)",  // Green (Success)
        0.65: "rgba(250, 204, 21, 0.9)",  // Amber (Warning)
        0.9: "rgba(239, 68, 68, 0.95)"    // Red (High density)
      }
    }).addTo(map);
  }

  // React to data changes
  $effect(() => {
    if (data && map) {
      updateHeatLayer();
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
      <span class="text-muted-foreground text-sm">No geographic data available</span>
    </div>
  {/if}

  <!-- Map Controls -->
  <div class="map-controls">
    <button class="map-control-btn" onclick={resetView} title="Reset view">
      <HomeIcon class="w-4 h-4" />
    </button>
    <button class="map-control-btn" onclick={toggleFullscreen} title={isFullscreen ? "Exit fullscreen" : "Fullscreen"}>
      {#if isFullscreen}
        <MinimizeIcon class="w-4 h-4" />
      {:else}
        <MaximizeIcon class="w-4 h-4" />
      {/if}
    </button>
  </div>

  <!-- Scroll Hint Overlay -->
  {#if showScrollHint && !isFullscreen}
    <div class="scroll-hint" onclick={() => showScrollHint = false}>
      <span>Hold <kbd>⌥</kbd> or <kbd>Ctrl</kbd> + scroll to zoom</span>
    </div>
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
    top: 0;
    left: 0;
    width: 100%;
    height: 100%;
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 10;
    pointer-events: none;
    background-color: rgba(15, 23, 42, 0.5);
  }

  /* Global Leaflet Overrides for Dark Theme */
  :global(.leaflet-container) {
    background-color: rgb(15, 23, 42) !important;
    font-family: inherit;
  }
</style>

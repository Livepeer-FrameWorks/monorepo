<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import type { Map, LayerGroup } from "leaflet";
  import { getIconComponent } from "$lib/iconUtils";

  // Leaflet is client-side only
  let L: any;

  // Icons for controls
  const MaximizeIcon = getIconComponent("Maximize2");
  const MinimizeIcon = getIconComponent("Minimize2");
  const HomeIcon = getIconComponent("Home");

  interface Route {
    from: [number, number]; // [lat, lng]
    to: [number, number];   // [lat, lng]
    score?: number;         // 0-100 routing score
    status: string;         // 'success' | 'failed'
    details?: string;
  }

  interface NodeLocation {
    id: string;
    lat: number;
    lng: number;
    name: string;
  }

  type BucketType = 'client' | 'node';

  interface BucketPolygon {
    id: string;
    coords: [number, number][];
    kind: BucketType;
  }

  interface Flow {
    from: [number, number];
    to: [number, number];
    weight?: number;
    color?: string;
  }

  interface Props {
    routes: Route[];
    nodes: NodeLocation[];
    buckets?: BucketPolygon[];
    onBucketClick?: (id: string) => void;
    flows?: Flow[];
    height?: number;
    zoom?: number;
    center?: [number, number];
    tileLayerUrl?: string;
  }

  let {
    routes = [],
    nodes = [],
    buckets = [],
    onBucketClick = undefined,
    flows = [],
    height = 500,
    zoom = 2,
    center = [20, 0],
    tileLayerUrl = "https://{s}.basemaps.cartocdn.com/dark_all/{z}/{x}/{y}{r}.png"
  }: Props = $props();

  let mapContainer = $state<HTMLElement>();
  let mapWrapper = $state<HTMLElement>();
  let map: Map | null = null;
  let layerGroup: LayerGroup | null = null;
  let bucketLayer: LayerGroup | null = null;
  let flowLayer: LayerGroup | null = null;

  // UX state
  let isFullscreen = $state(false);
  let showScrollHint = $state(true);

  onMount(async () => {
    const leafletModule = await import("leaflet");
    L = leafletModule.default;
    
    // Fix for default markers
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

    L.tileLayer(tileLayerUrl, {
      maxZoom: 19,
      subdomains: 'abcd',
    }).addTo(map);

    // Enable scroll zoom only with modifier key (Alt/Option or Ctrl)
    mapContainer.addEventListener('wheel', handleWheel, { passive: false });

    // Order matters: First added is at the bottom
    bucketLayer = L.layerGroup().addTo(map);
    flowLayer = L.layerGroup().addTo(map);
    layerGroup = L.layerGroup().addTo(map);
    
    drawMap(routes, nodes, buckets, flows);
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
    // Invalidate map size after transition
    setTimeout(() => map?.invalidateSize(), 310);
  }

  function resetView() {
    map?.setView(center, zoom);
  }

  function drawMap(
    currentRoutes: Route[],
    currentNodes: NodeLocation[],
    currentBuckets: BucketPolygon[],
    currentFlows: Flow[]
  ) {
    console.log('[drawMap] called with:', {
      routes: currentRoutes.length,
      nodes: currentNodes.length,
      buckets: currentBuckets.length,
      flows: currentFlows.length
    });

    if (!map || !L) {
      console.log('[drawMap] early return - no map or L');
      return;
    }

    console.log('[drawMap] removing and recreating layers...');

    // Remove old layer groups from map entirely
    if (bucketLayer) {
      map.removeLayer(bucketLayer);
    }
    if (flowLayer) {
      map.removeLayer(flowLayer);
    }
    if (layerGroup) {
      map.removeLayer(layerGroup);
    }

    // Recreate layer groups
    bucketLayer = L.layerGroup().addTo(map);
    flowLayer = L.layerGroup().addTo(map);
    layerGroup = L.layerGroup().addTo(map);

    // 0. Draw bucket polygons first (optional)
    // Build a simple count map for heat intensity
    const bucketCounts: Record<string, number> = {};
    currentBuckets.forEach((b) => {
      bucketCounts[b.id] = (bucketCounts[b.id] || 0) + 1;
    });

    currentBuckets.forEach((b) => {
      // log-like scaling so sparse buckets still show
      const count = bucketCounts[b.id] ?? 1;
      const intensity = Math.min(1, Math.log1p(count) / Math.log1p(Math.max(...Object.values(bucketCounts))));
      // Leaflet expects [lat, lng]
      const poly = L.polygon(b.coords, {
        color: b.kind === 'client' ? 'rgba(59,130,246,0.55)' : 'rgba(16,185,129,0.55)',
        weight: 1,
        fillOpacity: 0.12 + intensity * 0.35,
        opacity: 0.8,
      });
      poly.on('click', () => {
        if (onBucketClick) onBucketClick(b.id);
      });
      poly.on('mouseover', () => poly.setStyle({ weight: 2 }));
      poly.on('mouseout', () => poly.setStyle({ weight: 1 }));
      poly.addTo(bucketLayer!);
    });

    // 1b. Draw flows (client bucket -> node bucket centroids)
    currentFlows.forEach(f => {
      const color = f.color || 'rgba(168, 85, 247, 0.5)'; // purple
      const weight = f.weight || 1.2;
      L.polyline([f.from, f.to], {
        color,
        weight,
        opacity: 0.7,
        smoothFactor: 1
      }).addTo(flowLayer!);
    });

    // 1. Draw Infrastructure Nodes (Destinations)
    const nodeIcon = L.divIcon({
      className: 'custom-node-icon',
      html: `<div style="background-color: rgb(59, 130, 246); width: 12px; height: 12px; border-radius: 50%; box-shadow: 0 0 10px rgb(59, 130, 246);"></div>`,
      iconSize: [12, 12],
      iconAnchor: [6, 6]
    });

    currentNodes.forEach(node => {
      L.marker([node.lat, node.lng], { icon: nodeIcon })
        .bindTooltip(`<b>${node.name}</b><br>${node.id}`, { 
          direction: 'top',
          className: 'dark-tooltip'
        })
        .addTo(layerGroup!);
    });

    // 2. Draw Routes (Bezier curves or straight lines)
    currentRoutes.forEach(route => {
      const isSuccess = route.status === 'success' || route.status === 'SUCCESS';
      const color = isSuccess ? 'rgba(34, 197, 94, 0.4)' : 'rgba(239, 68, 68, 0.4)'; // Green vs Red
      const weight = 1;

      // Draw line
      L.polyline([route.from, route.to], {
        color: color,
        weight: weight,
        opacity: 0.6,
        smoothFactor: 1
      }).addTo(layerGroup!);

      // Draw Client (Origin) dot - smaller
      const clientIcon = L.divIcon({
        className: 'custom-client-icon',
        html: `<div style="background-color: ${isSuccess ? 'rgb(34, 197, 94)' : 'rgb(239, 68, 68)'}; width: 6px; height: 6px; border-radius: 50%;"></div>`,
        iconSize: [6, 6],
        iconAnchor: [3, 3]
      });

      L.marker(route.from, { icon: clientIcon }).addTo(layerGroup!);
    });
  }

  // Use $derived to force reactivity tracking
  let drawTrigger = $derived({
    routesLen: routes.length,
    nodesLen: nodes.length,
    bucketsLen: buckets.length,
    flowsLen: flows.length
  });

  $effect(() => {
    // Access drawTrigger to create dependency
    const trigger = drawTrigger;

    console.log('[RoutingMap] Effect triggered', {
      mapExists: !!map,
      trigger
    });

    if (map) {
      drawMap(routes, nodes, buckets, flows);
    }
  });
</script>

<div
  bind:this={mapWrapper}
  class="map-wrapper"
  class:map-wrapper--fullscreen={isFullscreen}
  style="height: {isFullscreen ? '100%' : `${height}px`};"
>
  {#if routes.length === 0 && nodes.length === 0}
    <div class="empty-state">
      <span class="text-muted-foreground text-sm">No routing data available</span>
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
    <button class="scroll-hint" type="button" onclick={() => showScrollHint = false}>
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

  :global(.leaflet-container) {
    background-color: rgb(15, 23, 42) !important;
  }

  :global(.dark-tooltip) {
    background-color: rgb(30, 41, 59);
    border: 1px solid rgb(51, 65, 85);
    color: rgb(226, 232, 240);
    border-radius: 4px;
  }
</style>

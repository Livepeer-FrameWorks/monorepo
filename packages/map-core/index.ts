import type {
  GeoJSONSource,
  IControl,
  Map as MapLibreMap,
  RequestParameters,
  ResourceType,
  StyleSpecification,
} from "maplibre-gl";
import type { Feature, FeatureCollection, Geometry, GeoJsonProperties } from "geojson";

export type BasemapProvider = "carto" | "openfreemap";
export type BasemapStyle = "dark-matter" | "dark";

export interface RuntimeBasemapConfig {
  provider: BasemapProvider;
  style: BasemapStyle;
  key?: string;
}

export interface RuntimePublicConfig {
  version: 1;
  basemap: RuntimeBasemapConfig;
}

declare global {
  // This value is generated from an allowlisted subset of the container
  // environment. It is intentionally browser-visible and is not a server secret.
  // eslint-disable-next-line no-var
  var __FRAMEWORKS_RUNTIME_CONFIG__: unknown;
}

const CARTO_STYLE_URL = "https://basemaps.cartocdn.com/gl/dark-matter-gl-style/style.json";
const OPENFREEMAP_STYLE_URL = "https://tiles.openfreemap.org/styles/dark";

const CARTO_RESOURCES: Readonly<Record<string, readonly RegExp[]>> = Object.freeze({
  "basemaps.cartocdn.com": [/^\/gl\/dark-matter-gl-style\/style\.json$/],
  "tiles.basemaps.cartocdn.com": [
    /^\/vector\/carto\.streets\/v1\/tiles\.json$/,
    /^\/gl\/dark-matter-gl-style\/sprite(?:@2x)?\.(?:json|png)$/,
    /^\/fonts\/[^/]+\/\d+-\d+\.pbf$/,
  ],
  "tiles-a.basemaps.cartocdn.com": [/^\/vectortiles\/carto\.streets\/v1\/\d+\/\d+\/\d+\.mvt$/],
  "tiles-b.basemaps.cartocdn.com": [/^\/vectortiles\/carto\.streets\/v1\/\d+\/\d+\/\d+\.mvt$/],
  "tiles-c.basemaps.cartocdn.com": [/^\/vectortiles\/carto\.streets\/v1\/\d+\/\d+\/\d+\.mvt$/],
  "tiles-d.basemaps.cartocdn.com": [/^\/vectortiles\/carto\.streets\/v1\/\d+\/\d+\/\d+\.mvt$/],
});

export const EMPTY_FEATURE_COLLECTION: FeatureCollection = Object.freeze({
  type: "FeatureCollection",
  features: [],
});

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

export function parseRuntimePublicConfig(value: unknown): RuntimePublicConfig {
  if (!isRecord(value) || value.version !== 1 || !isRecord(value.basemap)) {
    throw new Error("Basemap runtime configuration is missing or malformed");
  }

  const provider = value.basemap.provider;
  const style = value.basemap.style;
  const key = value.basemap.key;

  if (provider === "carto" && style === "dark-matter") {
    if (typeof key !== "string" || key.trim() === "") {
      throw new Error("CARTO basemap configuration requires a browser key");
    }
    return { version: 1, basemap: { provider, style, key: key.trim() } };
  }

  if (provider === "openfreemap" && style === "dark") {
    return { version: 1, basemap: { provider, style } };
  }

  throw new Error("Unsupported basemap provider and style combination");
}

export function readRuntimePublicConfig(): RuntimePublicConfig {
  return parseRuntimePublicConfig(globalThis.__FRAMEWORKS_RUNTIME_CONFIG__);
}

export function resolveBasemapStyle(config: RuntimeBasemapConfig): string {
  if (config.provider === "carto" && config.style === "dark-matter") {
    return CARTO_STYLE_URL;
  }
  if (config.provider === "openfreemap" && config.style === "dark") {
    return OPENFREEMAP_STYLE_URL;
  }
  throw new Error("Unsupported basemap provider and style combination");
}

export function isReviewedCartoResource(url: URL): boolean {
  if (url.protocol !== "https:") return false;
  const paths = CARTO_RESOURCES[url.hostname];
  return paths?.some((pattern) => pattern.test(url.pathname)) ?? false;
}

export function createBasemapRequestTransform(
  config: RuntimeBasemapConfig
): (url: string, resourceType?: ResourceType) => RequestParameters {
  if (config.provider !== "carto") return (url) => ({ url });
  const key = config.key?.trim();
  if (!key) throw new Error("CARTO basemap configuration requires a browser key");

  return (rawURL) => {
    let url: URL;
    try {
      url = new URL(rawURL);
    } catch {
      return { url: rawURL };
    }
    if (!isReviewedCartoResource(url)) return { url: rawURL };
    if (!url.searchParams.has("key")) url.searchParams.set("key", key);
    return { url: url.toString() };
  };
}

export function basemapMapOptions(config: RuntimeBasemapConfig) {
  return {
    style: resolveBasemapStyle(config),
    transformRequest: createBasemapRequestTransform(config),
    attributionControl: false,
    pitchWithRotate: false,
    dragRotate: false,
    touchPitch: false,
  } as const;
}

export function addRequiredAttribution(
  map: MapLibreMap,
  config: RuntimeBasemapConfig,
  compact = true
): void {
  const container = map.getContainer().ownerDocument.createElement("div");
  container.className = "maplibregl-ctrl frameworks-map-attribution";
  if (config.provider === "carto") {
    container.innerHTML = compact
      ? '<a href="https://www.openstreetmap.org/copyright" target="_blank" rel="noopener noreferrer">© OpenStreetMap</a> <a href="https://carto.com/attributions" target="_blank" rel="noopener noreferrer">© CARTO</a>'
      : '<a href="https://www.openstreetmap.org/copyright" target="_blank" rel="noopener noreferrer">© OpenStreetMap contributors</a> · <a href="https://carto.com/attributions" target="_blank" rel="noopener noreferrer">© CARTO</a>';
  } else {
    container.innerHTML = compact
      ? '<a href="https://openfreemap.org" target="_blank" rel="noopener noreferrer">© OpenFreeMap</a> <a href="https://openmaptiles.org" target="_blank" rel="noopener noreferrer">© OpenMapTiles</a> <a href="https://www.openstreetmap.org/copyright" target="_blank" rel="noopener noreferrer">© OpenStreetMap</a>'
      : '<a href="https://openfreemap.org" target="_blank" rel="noopener noreferrer">© OpenFreeMap</a> · <a href="https://openmaptiles.org" target="_blank" rel="noopener noreferrer">© OpenMapTiles</a> · <a href="https://www.openstreetmap.org/copyright" target="_blank" rel="noopener noreferrer">© OpenStreetMap contributors</a>';
  }
  const control: IControl = {
    onAdd: () => container,
    onRemove: () => container.remove(),
  };
  map.addControl(control, "bottom-right");
}

export function firstBasemapSymbolLayer(map: MapLibreMap): string | undefined {
  return map.getStyle().layers?.find((layer) => layer.type === "symbol")?.id;
}

export function featureCollection<G extends Geometry = Geometry, P = GeoJsonProperties>(
  features: Array<Feature<G, P>>
): FeatureCollection<G, P> {
  return { type: "FeatureCollection", features };
}

export function upsertGeoJSONSource(
  map: MapLibreMap,
  id: string,
  data: FeatureCollection
): GeoJSONSource {
  const existing = map.getSource<GeoJSONSource>(id);
  if (existing) {
    existing.setData(data);
    return existing;
  }
  map.addSource(id, { type: "geojson", data });
  const source = map.getSource<GeoJSONSource>(id);
  if (!source) throw new Error(`MapLibre did not register GeoJSON source ${id}`);
  return source;
}

export function setLayerVisibility(
  map: MapLibreMap,
  layerIDs: readonly string[],
  visible: boolean
): void {
  for (const id of layerIDs) {
    if (map.getLayer(id)) {
      map.setLayoutProperty(id, "visibility", visible ? "visible" : "none");
    }
  }
}

export function bindModifierScrollZoom(map: MapLibreMap, onEnabled?: () => void): () => void {
  const container = map.getContainer();
  const onWheel = (event: WheelEvent) => {
    if (event.altKey || event.ctrlKey || event.metaKey) {
      event.preventDefault();
      map.scrollZoom.enable();
      onEnabled?.();
    } else {
      map.scrollZoom.disable();
    }
  };
  container.addEventListener("wheel", onWheel, { passive: false });
  return () => container.removeEventListener("wheel", onWheel);
}

export function styleHasUsableLayers(style: StyleSpecification): boolean {
  return Array.isArray(style.layers) && style.layers.length > 0;
}

// Type augmentation for leaflet with heat layer support
import "leaflet";

declare module "leaflet" {
  type HeatLatLngTuple = [number, number, number];

  interface ColorGradientConfig {
    [key: number]: string;
  }

  interface HeatMapOptions {
    minOpacity?: number;
    maxOpacity?: number;
    maxZoom?: number;
    max?: number;
    radius?: number;
    blur?: number;
    gradient?: ColorGradientConfig;
  }

  interface HeatLayer extends TileLayer {
    setOptions(options: HeatMapOptions): HeatLayer;
    addLatLng(latlng: LatLng | HeatLatLngTuple): HeatLayer;
    setLatLngs(latlngs: Array<LatLng | HeatLatLngTuple>): HeatLayer;
  }

  function heatLayer(latlngs: Array<LatLng | HeatLatLngTuple>, options?: HeatMapOptions): HeatLayer;
}

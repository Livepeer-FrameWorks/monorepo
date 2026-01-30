declare module "leaflet.heat" {
  import type * as L from "leaflet";

  // Minimal typings for leaflet.heat plugin
  function heatLayer(
    latlngs: Array<[number, number, number?]>,
    options?: {
      radius?: number;
      blur?: number;
      maxZoom?: number;
      gradient?: Record<number, string>;
      minOpacity?: number;
      max?: number;
    }
  ): L.Layer;

  export { heatLayer };
}

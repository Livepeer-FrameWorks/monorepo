import type { Map as MapLibreMap } from "maplibre-gl";

export type NetworkView = "frameworks" | "livepeer";
export type ExplorerViewport = {
  camera?: { center: [number, number]; zoom: number; bearing: number; pitch: number };
};

export function networkView(search: URLSearchParams): NetworkView {
  return search.get("view") === "livepeer" ? "livepeer" : "frameworks";
}

export function networkViewURL(
  search: URLSearchParams,
  view: NetworkView
): "/network" | `/network?${string}` {
  const next = new URLSearchParams(search);
  if (view === "livepeer") next.set("view", view);
  else next.delete("view");
  return next.size ? `/network?${next}` : "/network";
}

export function networkTabKey(view: NetworkView, key: string): NetworkView | undefined {
  if (key === "Home") return "frameworks";
  if (key === "End") return "livepeer";
  if (key === "ArrowLeft" || key === "ArrowRight")
    return view === "frameworks" ? "livepeer" : "frameworks";
}

export function bindExplorerViewport(map: MapLibreMap, viewport?: ExplorerViewport): () => void {
  if (!viewport) return () => {};
  const capture = () => {
    const center = map.getCenter();
    viewport.camera = {
      center: [center.lng, center.lat],
      zoom: map.getZoom(),
      bearing: map.getBearing(),
      pitch: map.getPitch(),
    };
  };
  map.on("moveend", capture);
  return () => {
    capture();
    map.off("moveend", capture);
  };
}

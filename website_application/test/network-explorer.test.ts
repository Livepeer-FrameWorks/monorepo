import { describe, expect, it } from "vitest";
import type { Map as MapLibreMap } from "maplibre-gl";
import {
  bindExplorerViewport,
  networkTabKey,
  networkView,
  networkViewURL,
  type ExplorerViewport,
} from "../../packages/map-core/network-explorer";

describe("network explorer navigation", () => {
  it("keeps Livepeer filters when selecting either tab and defaults unknown views to topology", () => {
    const query = new URLSearchParams("view=livepeer&lp_region=eu&lp_endpoint=instance");
    expect(networkView(query)).toBe("livepeer");
    const topology = networkViewURL(query, "frameworks");
    expect(topology).toBe("/network?lp_region=eu&lp_endpoint=instance");
    expect(
      networkViewURL(new URL(topology, "https://example.com").searchParams, "livepeer")
    ).toContain("lp_endpoint=instance&view=livepeer");
    expect(networkView(new URLSearchParams("view=unknown"))).toBe("frameworks");
    expect(networkTabKey("frameworks", "ArrowLeft")).toBe("livepeer");
    expect(networkTabKey("livepeer", "Home")).toBe("frameworks");
    expect(networkTabKey("frameworks", "End")).toBe("livepeer");
    expect(networkTabKey("livepeer", "Tab")).toBeUndefined();
  });
  it("captures pan, zoom and orientation before a tab destroys its map", () => {
    const viewport: ExplorerViewport = {};
    let capture: (() => void) | undefined;
    const removed: string[] = [];
    let lng = 5;
    const map = {
      on: (event: string, callback: () => void) => {
        expect(event).toBe("moveend");
        capture = callback;
      },
      off: (event: string) => removed.push(event),
      getCenter: () => ({ lng, lat: 52 }),
      getZoom: () => 6,
      getBearing: () => 30,
      getPitch: () => 20,
    } as unknown as MapLibreMap;
    const stop = bindExplorerViewport(map, viewport);
    capture?.();
    expect(viewport.camera).toEqual({ center: [5, 52], zoom: 6, bearing: 30, pitch: 20 });
    lng = 7;
    stop();
    expect(viewport.camera?.center).toEqual([7, 52]);
    expect(removed).toEqual(["moveend"]);
  });
});

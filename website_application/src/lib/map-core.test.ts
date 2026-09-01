import { describe, expect, it } from "vitest";
import {
  createBasemapRequestTransform,
  isReviewedCartoResource,
  parseRuntimePublicConfig,
} from "@frameworks/map-core";

describe("basemap runtime configuration", () => {
  it("accepts the two supported provider and style pairs", () => {
    expect(
      parseRuntimePublicConfig({
        version: 1,
        basemap: { provider: "carto", style: "dark-matter", key: " browser-key " },
      })
    ).toEqual({
      version: 1,
      basemap: { provider: "carto", style: "dark-matter", key: "browser-key" },
    });
    expect(
      parseRuntimePublicConfig({
        version: 1,
        basemap: { provider: "openfreemap", style: "dark", key: "ignored" },
      })
    ).toEqual({ version: 1, basemap: { provider: "openfreemap", style: "dark" } });
  });

  it("rejects missing keys and unsupported combinations", () => {
    expect(() =>
      parseRuntimePublicConfig({
        version: 1,
        basemap: { provider: "carto", style: "dark-matter" },
      })
    ).toThrow(/browser key/);
    expect(() =>
      parseRuntimePublicConfig({
        version: 1,
        basemap: { provider: "carto", style: "dark" },
      })
    ).toThrow(/Unsupported/);
  });
});

describe("CARTO request transformation", () => {
  const transform = createBasemapRequestTransform({
    provider: "carto",
    style: "dark-matter",
    key: "browser-key",
  });

  it("adds the key to reviewed CARTO style, tile, sprite, and glyph requests", () => {
    const urls = [
      "https://basemaps.cartocdn.com/gl/dark-matter-gl-style/style.json",
      "https://tiles.basemaps.cartocdn.com/vector/carto.streets/v1/tiles.json",
      "https://tiles.basemaps.cartocdn.com/gl/dark-matter-gl-style/sprite@2x.png",
      "https://tiles.basemaps.cartocdn.com/fonts/Open%20Sans/0-255.pbf",
      "https://tiles-c.basemaps.cartocdn.com/vectortiles/carto.streets/v1/4/8/5.mvt",
    ];

    for (const rawURL of urls) {
      expect(isReviewedCartoResource(new URL(rawURL))).toBe(true);
      expect(new URL(transform(rawURL).url).searchParams.get("key")).toBe("browser-key");
    }
  });

  it("never leaks the key to unreviewed hosts, protocols, or paths", () => {
    const urls = [
      "https://example.com/vectortiles/carto.streets/v1/4/8/5.mvt",
      "http://tiles-a.basemaps.cartocdn.com/vectortiles/carto.streets/v1/4/8/5.mvt",
      "https://tiles-a.basemaps.cartocdn.com/unreviewed/4/8/5.mvt",
      "data:application/json,{}",
    ];

    for (const rawURL of urls) expect(transform(rawURL).url).toBe(rawURL);
  });

  it("preserves a provider-supplied key", () => {
    const result = transform(
      "https://basemaps.cartocdn.com/gl/dark-matter-gl-style/style.json?key=provider"
    );
    expect(new URL(result.url).searchParams.get("key")).toBe("provider");
  });

  it("preserves unrelated query parameters while adding the key", () => {
    const result = transform(
      "https://basemaps.cartocdn.com/gl/dark-matter-gl-style/style.json?language=en"
    );
    const url = new URL(result.url);
    expect(url.searchParams.get("language")).toBe("en");
    expect(url.searchParams.get("key")).toBe("browser-key");
  });
});

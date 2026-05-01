import { describe, expect, it } from "vitest";
import { ChandlerAssetSource } from "../src/core/ChandlerAssetSource";

describe("ChandlerAssetSource", () => {
  it("builds three sibling URLs from a base + assetKey", () => {
    const got = ChandlerAssetSource.fromBase("https://chandler.example.com", "stream-uuid-123");
    expect(got).toEqual({
      posterUrl: "https://chandler.example.com/assets/stream-uuid-123/poster.jpg",
      spriteVttUrl: "https://chandler.example.com/assets/stream-uuid-123/sprite.vtt",
      spriteJpgUrl: "https://chandler.example.com/assets/stream-uuid-123/sprite.jpg",
      assetKey: "stream-uuid-123",
    });
  });

  it("strips trailing slash from base", () => {
    const got = ChandlerAssetSource.fromBase("https://chandler.example.com/", "abc123");
    expect(got?.posterUrl).toBe("https://chandler.example.com/assets/abc123/poster.jpg");
  });

  it("returns null for empty base", () => {
    expect(ChandlerAssetSource.fromBase("", "key")).toBeNull();
  });

  it("returns null for empty assetKey", () => {
    expect(ChandlerAssetSource.fromBase("https://chandler.example.com", "")).toBeNull();
  });
});

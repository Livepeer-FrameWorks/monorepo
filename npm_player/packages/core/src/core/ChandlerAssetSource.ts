import type { ThumbnailAssetUrls } from "../types";

/**
 * Pure URL builder for Chandler-served thumbnail assets.
 *
 * Chandler exposes three files per assetKey under /assets/<assetKey>/:
 *   - poster.jpg   (full-resolution single-frame JPEG)
 *   - sprite.jpg   (10x10 grid sprite sheet)
 *   - sprite.vtt   (VTT cues with #xywh fragments)
 *
 * For live, assetKey is the streamId. For DVR/clip, assetKey is the artifactHash.
 */
export class ChandlerAssetSource {
  static fromBase(baseUrl: string, assetKey: string): ThumbnailAssetUrls | null {
    if (!baseUrl || !assetKey) return null;
    const base = baseUrl.replace(/\/$/, "") + "/assets/" + assetKey;
    return {
      posterUrl: base + "/poster.jpg",
      spriteVttUrl: base + "/sprite.vtt",
      spriteJpgUrl: base + "/sprite.jpg",
      assetKey,
    };
  }
}

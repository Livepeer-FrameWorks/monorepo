import { parseThumbnailVtt, type ThumbnailCue } from "@livepeer-frameworks/player-core";
import { getAssetUrl } from "$lib/config";

interface SpriteData {
  cues: ThumbnailCue[];
  spriteUrl: string;
}

const cache = new Map<string, SpriteData>();
const inflight = new Map<string, Promise<SpriteData | null>>();

export async function getSpriteCues(assetId: string): Promise<SpriteData | null> {
  if (!assetId) return null;

  const cached = cache.get(assetId);
  if (cached) return cached;

  const existing = inflight.get(assetId);
  if (existing) return existing;

  const promise = (async (): Promise<SpriteData | null> => {
    const vttUrl = getAssetUrl(assetId, "sprite.vtt");
    if (!vttUrl) return null;
    try {
      const resp = await fetch(vttUrl);
      if (!resp.ok) return null;
      const text = await resp.text();
      const cues = parseThumbnailVtt(text);
      if (cues.length === 0) return null;
      const spriteUrl = getAssetUrl(assetId, "sprite.jpg");
      const data: SpriteData = { cues, spriteUrl };
      cache.set(assetId, data);
      return data;
    } catch {
      return null;
    } finally {
      inflight.delete(assetId);
    }
  })();

  inflight.set(assetId, promise);
  return promise;
}

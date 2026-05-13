import { parseThumbnailVtt, type ThumbnailCue } from "@livepeer-frameworks/player-core";

interface SpriteData {
  cues: ThumbnailCue[];
  spriteUrl: string;
}

const cache = new Map<string, SpriteData>();
const inflight = new Map<string, Promise<SpriteData | null>>();

export async function getSpriteCues(
  assetId: string,
  spriteVttUrl: string,
  spriteJpgUrl: string
): Promise<SpriteData | null> {
  if (!assetId || !spriteVttUrl || !spriteJpgUrl) return null;

  const cached = cache.get(assetId);
  if (cached) return cached;

  const existing = inflight.get(assetId);
  if (existing) return existing;

  const promise = (async (): Promise<SpriteData | null> => {
    try {
      const resp = await fetch(spriteVttUrl);
      if (!resp.ok) return null;
      const text = await resp.text();
      const cues = parseThumbnailVtt(text);
      if (cues.length === 0) return null;
      const data: SpriteData = { cues, spriteUrl: spriteJpgUrl };
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

import type { StudioLayout } from "@livepeer-frameworks/streamcrafter-core";

/**
 * The studio's scenes, overlays and layout preset per stream, as the studio
 * reports them through onLayoutChange. It is a per-viewer preference in this
 * browser: storage can be blocked, full or cleared, and the studio then starts
 * from its default layout. The studio validates the layout when it applies it.
 */
const KEY_PREFIX = "fw.goLive.studioLayout.";

function storageKey(streamId: string): string {
  return `${KEY_PREFIX}${streamId}`;
}

export interface StudioTarget {
  streamId: string;
  whipUrl: string | null;
  layout: StudioLayout | null;
}

/**
 * What the studio is opened for once a destination of `streamId` resolved.
 * The same stream keeps its WHIP URL and layout object, so the studio keeps
 * its controller and current scenes. Another stream gets the new URL and its
 * saved layout together, which the SDK takes as a stream switch.
 */
export function studioTarget(
  current: StudioTarget,
  streamId: string,
  whipUrl: string,
  load: (streamId: string) => StudioLayout | null
): StudioTarget {
  if (current.whipUrl && current.streamId === streamId) return current;
  return { streamId, whipUrl, layout: load(streamId) };
}

export function loadStudioLayout(
  storage: Storage | undefined,
  streamId: string
): StudioLayout | null {
  if (!streamId) return null;
  try {
    const raw = storage?.getItem(storageKey(streamId));
    if (!raw) return null;
    const parsed: unknown = JSON.parse(raw);
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return null;
    return parsed as StudioLayout;
  } catch {
    return null;
  }
}

export function saveStudioLayout(
  storage: Storage | undefined,
  streamId: string,
  layout: StudioLayout
): void {
  if (!streamId) return;
  try {
    storage?.setItem(storageKey(streamId), JSON.stringify(layout));
  } catch {
    // Blocked or full storage (a picked image is carried as a data URL) keeps
    // the layout for this page view only.
  }
}

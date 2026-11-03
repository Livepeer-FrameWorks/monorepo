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
 * The studio takes `streamId` as its layout key: the same stream keeps its
 * current scenes, and another stream gets its saved layout, or the default
 * one, on the running studio, with its sources kept.
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

/**
 * Store a layout the studio reported under the layout key it was reported
 * for. The selected stream can already be another one by the time a report
 * arrives.
 */
export function saveReportedLayout(
  storage: Storage | undefined,
  layout: StudioLayout,
  layoutKey: string | undefined
): void {
  if (layoutKey) saveStudioLayout(storage, layoutKey, layout);
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

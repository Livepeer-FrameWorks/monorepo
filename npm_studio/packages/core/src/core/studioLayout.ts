/**
 * A studio layout as plain data: the scenes, their image and text overlays and
 * the layout preset. It holds no camera, screen or media-file layers, because
 * those belong to the captures of one session; sources added after an import
 * join the active scene and the preset places them.
 */

import type { LayoutConfig, LayoutMode } from "../types";
import type { TextOverlayConfig } from "./TextOverlaySource";

export const STUDIO_LAYOUT_VERSION = 1;

export interface StudioLayoutScene {
  name: string;
  backgroundColor: string;
}

interface StudioLayoutOverlayBase {
  /** Indexes into `scenes` of every scene that shows this overlay. */
  scenes: number[];
  /** Position in the free space of the frame: 0 is the left or top edge, 1 the right or bottom. */
  x: number;
  y: number;
}

export interface StudioLayoutTextOverlay extends StudioLayoutOverlayBase {
  kind: "text";
  text: TextOverlayConfig;
}

export interface StudioLayoutImageOverlay extends StudioLayoutOverlayBase {
  kind: "image";
  /** A URL, or a data URL for an image that was picked from disk. */
  image: string;
  label: string;
  /** Width as a share of the frame width. */
  size: number;
}

export type StudioLayoutOverlay = StudioLayoutTextOverlay | StudioLayoutImageOverlay;

export interface StudioLayout {
  version: typeof STUDIO_LAYOUT_VERSION;
  layout: LayoutConfig;
  /** Index into `scenes`. */
  activeScene: number;
  scenes: StudioLayoutScene[];
  /** Bottom to top. */
  overlays: StudioLayoutOverlay[];
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isFiniteNumber(value: unknown): value is number {
  return typeof value === "number" && Number.isFinite(value);
}

const LAYOUT_MODES = new Set<string>(
  Object.keys({
    solo: true,
    "pip-br": true,
    "pip-bl": true,
    "pip-tr": true,
    "pip-tl": true,
    "split-h": true,
    "split-v": true,
    "focus-l": true,
    "focus-r": true,
    grid: true,
    stack: true,
    "pip-dual-br": true,
    "pip-dual-bl": true,
    "split-pip-l": true,
    "split-pip-r": true,
    featured: true,
    "featured-r": true,
    fullscreen: true,
    pip: true,
    "side-by-side": true,
  } satisfies Record<LayoutMode, true>)
);

/** Optional fields of the layout preset and their checks. */
const LAYOUT_FIELDS: Record<string, (value: unknown) => boolean> = {
  scalingMode: (v) => v === "stretch" || v === "letterbox" || v === "crop",
  pipScale: (v) => isFiniteNumber(v) && v > 0 && v <= 1,
  splitRatio: (v) => isFiniteNumber(v) && v > 0 && v < 1,
  pipPosition: (v) =>
    v === "top-left" || v === "top-right" || v === "bottom-left" || v === "bottom-right",
  splitDirection: (v) => v === "horizontal" || v === "vertical",
};

/** Optional fields of a text overlay and their checks. */
const TEXT_FIELDS: Record<string, (value: unknown) => boolean> = {
  fontFamily: (v) => typeof v === "string",
  fontSize: (v) => isFiniteNumber(v) && v > 0,
  fontWeight: (v) => typeof v === "string",
  color: (v) => typeof v === "string",
  backgroundColor: (v) => typeof v === "string",
  padding: (v) => isFiniteNumber(v) && v >= 0,
  textAlign: (v) => v === "left" || v === "center" || v === "right",
  borderRadius: (v) => isFiniteNumber(v) && v >= 0,
  style: (v) => v === "plain" || v === "lower-third" || v === "banner",
};

/** The known fields of `value`, each checked; undefined ones are left out. */
function checkedFields(
  value: Record<string, unknown>,
  fields: Record<string, (value: unknown) => boolean>,
  problem: string
): Record<string, unknown> {
  const result: Record<string, unknown> = {};
  for (const [name, valid] of Object.entries(fields)) {
    if (value[name] === undefined) continue;
    if (!valid(value[name])) throw new Error(`${problem} has an invalid ${name}`);
    result[name] = value[name];
  }
  return result;
}

function sceneIndexes(value: unknown, sceneCount: number): number[] | null {
  if (!Array.isArray(value) || value.length === 0) return null;
  const valid = value.every((i) => Number.isInteger(i) && i >= 0 && i < sceneCount);
  return valid ? [...new Set(value as number[])] : null;
}

/**
 * Check a layout that came from storage or another page before the studio
 * applies it. Throws with the first problem found.
 */
export function parseStudioLayout(value: unknown): StudioLayout {
  if (!isRecord(value)) throw new Error("Studio layout must be an object");
  if (value.version !== STUDIO_LAYOUT_VERSION) {
    throw new Error(`Unsupported studio layout version: ${String(value.version)}`);
  }
  if (!isRecord(value.layout) || typeof value.layout.mode !== "string") {
    throw new Error("Studio layout has no layout mode");
  }
  if (!LAYOUT_MODES.has(value.layout.mode)) {
    throw new Error(`Studio layout has an unknown layout mode: ${value.layout.mode}`);
  }
  const layout = {
    mode: value.layout.mode,
    ...checkedFields(value.layout, LAYOUT_FIELDS, "Studio layout preset"),
  } as LayoutConfig;
  if (!Array.isArray(value.scenes) || value.scenes.length === 0) {
    throw new Error("Studio layout has no scenes");
  }
  const scenes = value.scenes.map((scene, i): StudioLayoutScene => {
    if (!isRecord(scene) || typeof scene.name !== "string") {
      throw new Error(`Studio layout scene ${i} has no name`);
    }
    return {
      name: scene.name,
      backgroundColor:
        typeof scene.backgroundColor === "string" ? scene.backgroundColor : "#000000",
    };
  });
  const activeScene = value.activeScene;
  if (!Number.isInteger(activeScene) || (activeScene as number) < 0) {
    throw new Error("Studio layout has no active scene");
  }
  if ((activeScene as number) >= scenes.length) {
    throw new Error("Studio layout's active scene is not one of its scenes");
  }

  const overlays = (Array.isArray(value.overlays) ? value.overlays : []).map(
    (overlay, i): StudioLayoutOverlay => {
      const problem = `Studio layout overlay ${i}`;
      if (!isRecord(overlay)) throw new Error(`${problem} must be an object`);
      const inScenes = sceneIndexes(overlay.scenes, scenes.length);
      if (!inScenes) throw new Error(`${problem} names no scene of the layout`);
      if (!isFiniteNumber(overlay.x) || !isFiniteNumber(overlay.y)) {
        throw new Error(`${problem} has no position`);
      }
      if (overlay.kind === "text") {
        if (!isRecord(overlay.text) || typeof overlay.text.text !== "string") {
          throw new Error(`${problem} has no text`);
        }
        return {
          kind: "text",
          scenes: inScenes,
          x: overlay.x,
          y: overlay.y,
          text: {
            text: overlay.text.text,
            ...checkedFields(overlay.text, TEXT_FIELDS, `${problem} text`),
          } as TextOverlayConfig,
        };
      }
      if (overlay.kind === "image") {
        if (typeof overlay.image !== "string" || overlay.image === "") {
          throw new Error(`${problem} has no image`);
        }
        if (!isFiniteNumber(overlay.size) || overlay.size <= 0) {
          throw new Error(`${problem} has no size`);
        }
        return {
          kind: "image",
          scenes: inScenes,
          x: overlay.x,
          y: overlay.y,
          image: overlay.image,
          label: typeof overlay.label === "string" ? overlay.label : "Image",
          size: overlay.size,
        };
      }
      throw new Error(`${problem} is neither text nor an image`);
    }
  );

  return {
    version: STUDIO_LAYOUT_VERSION,
    layout,
    activeScene: activeScene as number,
    scenes,
    overlays,
  };
}

/** A picked image travels inside the layout, so it needs no upload. */
export async function blobToDataUrl(blob: Blob): Promise<string> {
  const bytes = new Uint8Array(await blob.arrayBuffer());
  let binary = "";
  const chunk = 0x8000;
  for (let i = 0; i < bytes.length; i += chunk) {
    binary += String.fromCharCode(...bytes.subarray(i, i + chunk));
  }
  return `data:${blob.type || "application/octet-stream"};base64,${btoa(binary)}`;
}

/**
 * Decode a data URL here rather than through fetch, which a page's
 * connect-src policy may refuse.
 */
export function dataUrlToBlob(dataUrl: string): Blob | null {
  const match = /^data:([^;,]*)(;base64)?,(.*)$/s.exec(dataUrl);
  if (!match) return null;
  const [, type, base64, payload] = match;
  if (!base64) return new Blob([decodeURIComponent(payload)], { type });
  const binary = atob(payload);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
  return new Blob([bytes], { type });
}

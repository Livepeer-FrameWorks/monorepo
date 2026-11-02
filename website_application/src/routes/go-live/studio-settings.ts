export type CompositorRenderer = "auto" | "webgl" | "webgpu" | "canvas2d";

export interface StudioSettings {
  enableCompositor: boolean;
  compositorRenderer: CompositorRenderer;
}

export const RENDERER_LABELS: Record<CompositorRenderer, string> = {
  auto: "Auto (recommended)",
  webgpu: "WebGPU",
  webgl: "WebGL",
  canvas2d: "Canvas 2D (most compatible)",
};

export const DEFAULT_STUDIO_SETTINGS: StudioSettings = {
  enableCompositor: true,
  compositorRenderer: "auto",
};

const STORAGE_KEY = "fw.goLive.studioSettings";

function isRenderer(value: unknown): value is CompositorRenderer {
  return typeof value === "string" && value in RENDERER_LABELS;
}

/** Per-viewer studio preference. Storage can be blocked or empty; the defaults apply then. */
export function loadStudioSettings(storage: Storage | undefined): StudioSettings {
  try {
    const raw = storage?.getItem(STORAGE_KEY);
    if (!raw) return { ...DEFAULT_STUDIO_SETTINGS };
    const parsed = JSON.parse(raw) as Partial<StudioSettings>;
    return {
      enableCompositor:
        typeof parsed.enableCompositor === "boolean"
          ? parsed.enableCompositor
          : DEFAULT_STUDIO_SETTINGS.enableCompositor,
      compositorRenderer: isRenderer(parsed.compositorRenderer)
        ? parsed.compositorRenderer
        : DEFAULT_STUDIO_SETTINGS.compositorRenderer,
    };
  } catch {
    return { ...DEFAULT_STUDIO_SETTINGS };
  }
}

export function saveStudioSettings(storage: Storage | undefined, settings: StudioSettings): void {
  try {
    storage?.setItem(STORAGE_KEY, JSON.stringify(settings));
  } catch {
    // Blocked storage keeps the choice for this page view only.
  }
}

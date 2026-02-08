import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";

import { Canvas2DRenderer } from "../src/core/renderers/Canvas2DRenderer";
import type { Scene, Layer, CompositorConfig, FilterConfig } from "../src/types";

// ---------------------------------------------------------------------------
// Mock infrastructure
// ---------------------------------------------------------------------------

function createMockCtx() {
  return {
    fillStyle: "",
    globalAlpha: 1,
    imageSmoothingEnabled: false,
    imageSmoothingQuality: "low" as ImageSmoothingQuality,
    save: vi.fn(),
    restore: vi.fn(),
    fillRect: vi.fn(),
    drawImage: vi.fn(),
    translate: vi.fn(),
    rotate: vi.fn(),
    clip: vi.fn(),
    beginPath: vi.fn(),
    moveTo: vi.fn(),
    lineTo: vi.fn(),
    quadraticCurveTo: vi.fn(),
    closePath: vi.fn(),
  };
}

function createMockCanvas(ctx: ReturnType<typeof createMockCtx> | null = null) {
  const mockCtx = ctx ?? createMockCtx();
  return {
    width: 1920,
    height: 1080,
    getContext: vi.fn(() => mockCtx),
    __ctx: mockCtx,
  } as any;
}

function makeConfig(overrides: Partial<CompositorConfig> = {}): CompositorConfig {
  return { width: 1920, height: 1080, renderer: "canvas2d", ...overrides } as CompositorConfig;
}

function makeLayer(overrides: Partial<Layer> = {}): Layer {
  return {
    id: "layer-1",
    sourceId: "src-1",
    visible: true,
    locked: false,
    zIndex: 0,
    scalingMode: "stretch",
    transform: {
      x: 0,
      y: 0,
      width: 1,
      height: 1,
      opacity: 1,
      rotation: 0,
      borderRadius: 0,
      crop: { top: 0, bottom: 0, left: 0, right: 0 },
    },
    ...overrides,
  } as Layer;
}

function makeScene(overrides: Partial<Scene> = {}): Scene {
  return {
    id: "scene-1",
    name: "Test",
    layers: [],
    backgroundColor: "#000000",
    ...overrides,
  } as Scene;
}

function makeFrame(w = 1920, h = 1080) {
  return { displayWidth: w, displayHeight: h } as unknown as VideoFrame;
}

function makeImageBitmap(w = 1920, h = 1080) {
  return { width: w, height: h } as unknown as ImageBitmap;
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("Canvas2DRenderer", () => {
  let renderer: Canvas2DRenderer;
  let canvas: ReturnType<typeof createMockCanvas>;
  let ctx: ReturnType<typeof createMockCtx>;

  beforeEach(() => {
    renderer = new Canvas2DRenderer();
    ctx = createMockCtx();
    canvas = createMockCanvas(ctx);
    vi.spyOn(console, "warn").mockImplementation(() => {});
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  // ===========================================================================
  // type / isSupported
  // ===========================================================================
  describe("type and support", () => {
    it("type is canvas2d", () => {
      expect(renderer.type).toBe("canvas2d");
    });

    it("isSupported is always true", () => {
      expect(renderer.isSupported).toBe(true);
    });
  });

  // ===========================================================================
  // init
  // ===========================================================================
  describe("init", () => {
    it("gets 2d context with correct options", async () => {
      await renderer.init(canvas, makeConfig());

      expect(canvas.getContext).toHaveBeenCalledWith("2d", {
        desynchronized: true,
        alpha: false,
        willReadFrequently: false,
      });
    });

    it("sets image smoothing", async () => {
      await renderer.init(canvas, makeConfig());

      expect(ctx.imageSmoothingEnabled).toBe(true);
      expect(ctx.imageSmoothingQuality).toBe("high");
    });

    it("throws when getContext returns null", async () => {
      const nullCanvas = { getContext: vi.fn(() => null) } as any;

      await expect(renderer.init(nullCanvas, makeConfig())).rejects.toThrow(
        "Failed to get 2D context"
      );
    });
  });

  // ===========================================================================
  // renderScene
  // ===========================================================================
  describe("renderScene", () => {
    it("clears canvas with scene background color", async () => {
      await renderer.init(canvas, makeConfig());
      const scene = makeScene({ backgroundColor: "#ff0000" });

      renderer.renderScene(scene, new Map());

      expect(ctx.fillStyle).toBe("#ff0000");
      expect(ctx.fillRect).toHaveBeenCalledWith(0, 0, 1920, 1080);
    });

    it("defaults to #000000 when no background color", async () => {
      await renderer.init(canvas, makeConfig());
      const scene = makeScene({ backgroundColor: "" });

      renderer.renderScene(scene, new Map());

      expect(ctx.fillStyle).toBe("#000000");
    });

    it("filters out invisible layers", async () => {
      await renderer.init(canvas, makeConfig());
      const scene = makeScene({
        layers: [
          makeLayer({ visible: false, sourceId: "hidden" }),
          makeLayer({ visible: true, sourceId: "visible" }),
        ],
      });
      const frames = new Map<string, VideoFrame | ImageBitmap>();
      frames.set("hidden", makeFrame());
      frames.set("visible", makeFrame());

      renderer.renderScene(scene, frames);

      // Only one drawImage call (for the visible layer)
      expect(ctx.drawImage).toHaveBeenCalledTimes(1);
    });

    it("sorts layers by zIndex ascending", async () => {
      await renderer.init(canvas, makeConfig());
      const scene = makeScene({
        layers: [
          makeLayer({ zIndex: 2, sourceId: "top" }),
          makeLayer({ zIndex: 0, sourceId: "bottom" }),
          makeLayer({ zIndex: 1, sourceId: "middle" }),
        ],
      });
      const frames = new Map<string, VideoFrame | ImageBitmap>();
      frames.set("top", makeFrame());
      frames.set("bottom", makeFrame());
      frames.set("middle", makeFrame());

      renderer.renderScene(scene, frames);

      expect(ctx.drawImage).toHaveBeenCalledTimes(3);
    });

    it("skips layers without matching frames", async () => {
      await renderer.init(canvas, makeConfig());
      const scene = makeScene({
        layers: [makeLayer({ sourceId: "missing" })],
      });

      renderer.renderScene(scene, new Map());

      expect(ctx.drawImage).not.toHaveBeenCalled();
    });
  });

  // ===========================================================================
  // renderLayer (tested via renderScene)
  // ===========================================================================
  describe("renderLayer", () => {
    it("converts relative coordinates to pixels", async () => {
      await renderer.init(canvas, makeConfig({ width: 1000, height: 500 }));
      const layer = makeLayer({
        sourceId: "src",
        transform: {
          x: 0.1,
          y: 0.2,
          width: 0.5,
          height: 0.4,
          opacity: 1,
          rotation: 0,
          borderRadius: 0,
          crop: { top: 0, bottom: 0, left: 0, right: 0 },
        },
      });
      const scene = makeScene({ layers: [layer] });
      const frames = new Map<string, VideoFrame | ImageBitmap>();
      frames.set("src", makeFrame(1000, 500));

      renderer.renderScene(scene, frames);

      // drawImage(frame, sxFinal, syFinal, swFinal, shFinal, dx, dy, dw, dh)
      // stretch mode: dx=100, dy=100, dw=500, dh=200
      expect(ctx.drawImage).toHaveBeenCalledWith(
        expect.anything(),
        0,
        0,
        1000,
        500, // source rect (no crop)
        100,
        100,
        500,
        200 // dest rect (pixels)
      );
    });

    it("clamps opacity to 0-1", async () => {
      await renderer.init(canvas, makeConfig());
      const layer = makeLayer({
        sourceId: "src",
        transform: {
          x: 0,
          y: 0,
          width: 1,
          height: 1,
          opacity: 1.5,
          rotation: 0,
          borderRadius: 0,
          crop: { top: 0, bottom: 0, left: 0, right: 0 },
        },
      });
      const scene = makeScene({ layers: [layer] });
      const frames = new Map<string, VideoFrame | ImageBitmap>();
      frames.set("src", makeFrame());

      renderer.renderScene(scene, frames);

      // globalAlpha should be clamped to 1
      expect(ctx.save).toHaveBeenCalled();
      expect(ctx.restore).toHaveBeenCalled();
    });

    it("clamps negative opacity to 0", async () => {
      await renderer.init(canvas, makeConfig());
      const layer = makeLayer({
        sourceId: "src",
        transform: {
          x: 0,
          y: 0,
          width: 1,
          height: 1,
          opacity: -0.5,
          rotation: 0,
          borderRadius: 0,
          crop: { top: 0, bottom: 0, left: 0, right: 0 },
        },
      });
      const scene = makeScene({ layers: [layer] });
      const frames = new Map<string, VideoFrame | ImageBitmap>();
      frames.set("src", makeFrame());

      renderer.renderScene(scene, frames);

      expect(ctx.save).toHaveBeenCalled();
    });

    it("applies rotation transform around center", async () => {
      await renderer.init(canvas, makeConfig({ width: 1000, height: 1000 }));
      const layer = makeLayer({
        sourceId: "src",
        transform: {
          x: 0,
          y: 0,
          width: 1,
          height: 1,
          opacity: 1,
          rotation: 90,
          borderRadius: 0,
          crop: { top: 0, bottom: 0, left: 0, right: 0 },
        },
      });
      const scene = makeScene({ layers: [layer] });
      const frames = new Map<string, VideoFrame | ImageBitmap>();
      frames.set("src", makeFrame(1000, 1000));

      renderer.renderScene(scene, frames);

      // translate to center, rotate, translate back
      expect(ctx.translate).toHaveBeenCalledTimes(2);
      expect(ctx.translate).toHaveBeenCalledWith(500, 500);
      expect(ctx.translate).toHaveBeenCalledWith(-500, -500);
      expect(ctx.rotate).toHaveBeenCalledWith((90 * Math.PI) / 180);
    });

    it("skips rotation when rotation is 0", async () => {
      await renderer.init(canvas, makeConfig());
      const layer = makeLayer({ sourceId: "src" });
      const scene = makeScene({ layers: [layer] });
      const frames = new Map<string, VideoFrame | ImageBitmap>();
      frames.set("src", makeFrame());

      renderer.renderScene(scene, frames);

      expect(ctx.rotate).not.toHaveBeenCalled();
    });

    it("applies border radius clipping", async () => {
      await renderer.init(canvas, makeConfig());
      const layer = makeLayer({
        sourceId: "src",
        transform: {
          x: 0,
          y: 0,
          width: 1,
          height: 1,
          opacity: 1,
          rotation: 0,
          borderRadius: 10,
          crop: { top: 0, bottom: 0, left: 0, right: 0 },
        },
      });
      const scene = makeScene({ layers: [layer] });
      const frames = new Map<string, VideoFrame | ImageBitmap>();
      frames.set("src", makeFrame());

      renderer.renderScene(scene, frames);

      expect(ctx.beginPath).toHaveBeenCalled();
      expect(ctx.clip).toHaveBeenCalled();
      expect(ctx.closePath).toHaveBeenCalled();
    });

    it("skips clipping when borderRadius is 0", async () => {
      await renderer.init(canvas, makeConfig());
      const layer = makeLayer({ sourceId: "src" });
      const scene = makeScene({ layers: [layer] });
      const frames = new Map<string, VideoFrame | ImageBitmap>();
      frames.set("src", makeFrame());

      renderer.renderScene(scene, frames);

      expect(ctx.clip).not.toHaveBeenCalled();
    });

    it("applies crop to source rectangle", async () => {
      await renderer.init(canvas, makeConfig({ width: 1000, height: 1000 }));
      const layer = makeLayer({
        sourceId: "src",
        transform: {
          x: 0,
          y: 0,
          width: 1,
          height: 1,
          opacity: 1,
          rotation: 0,
          borderRadius: 0,
          crop: { top: 0.1, bottom: 0.1, left: 0.2, right: 0.2 },
        },
      });
      const scene = makeScene({ layers: [layer] });
      const frames = new Map<string, VideoFrame | ImageBitmap>();
      frames.set("src", makeFrame(1000, 1000));

      renderer.renderScene(scene, frames);

      // sx = 0.2*1000=200, sy = 0.1*1000=100
      // sw = 1000*(1-0.2-0.2)=600, sh = 1000*(1-0.1-0.1)=800
      const call = ctx.drawImage.mock.calls[0];
      expect(call[1]).toBeCloseTo(200, 5);
      expect(call[2]).toBeCloseTo(100, 5);
      expect(call[3]).toBeCloseTo(600, 5);
      expect(call[4]).toBeCloseTo(800, 5);
      expect(call[5]).toBe(0);
      expect(call[6]).toBe(0);
      expect(call[7]).toBe(1000);
      expect(call[8]).toBe(1000);
    });

    it("uses ImageBitmap width/height instead of displayWidth", async () => {
      await renderer.init(canvas, makeConfig({ width: 100, height: 100 }));
      const layer = makeLayer({ sourceId: "src" });
      const scene = makeScene({ layers: [layer] });
      const bitmap = makeImageBitmap(640, 480);
      const frames = new Map<string, VideoFrame | ImageBitmap>();
      frames.set("src", bitmap);

      renderer.renderScene(scene, frames);

      // stretch: source rect = full bitmap
      expect(ctx.drawImage).toHaveBeenCalledWith(bitmap, 0, 0, 640, 480, 0, 0, 100, 100);
    });
  });

  // ===========================================================================
  // calculateScaling (via renderScene)
  // ===========================================================================
  describe("calculateScaling", () => {
    it("stretch passes through position and size", async () => {
      await renderer.init(canvas, makeConfig({ width: 1000, height: 500 }));
      const layer = makeLayer({
        sourceId: "src",
        scalingMode: "stretch",
        transform: {
          x: 0.1,
          y: 0.1,
          width: 0.8,
          height: 0.8,
          opacity: 1,
          rotation: 0,
          borderRadius: 0,
          crop: { top: 0, bottom: 0, left: 0, right: 0 },
        },
      });
      const scene = makeScene({ layers: [layer] });
      const frames = new Map<string, VideoFrame | ImageBitmap>();
      frames.set("src", makeFrame(1920, 1080));

      renderer.renderScene(scene, frames);

      expect(ctx.drawImage).toHaveBeenCalledWith(
        expect.anything(),
        0,
        0,
        1920,
        1080,
        100,
        50,
        800,
        400
      );
    });

    it("letterbox wider source: shrinks height and centers vertically", async () => {
      await renderer.init(canvas, makeConfig({ width: 1000, height: 1000 }));
      // Source is 2:1, dest is 1:1 → sourceAspect > destAspect
      // newDw = 1000, newDh = 1000/2 = 500
      // newDx = 0 + (1000-1000)/2 = 0, newDy = 0 + (1000-500)/2 = 250
      const layer = makeLayer({
        sourceId: "src",
        scalingMode: "letterbox",
        transform: {
          x: 0,
          y: 0,
          width: 1,
          height: 1,
          opacity: 1,
          rotation: 0,
          borderRadius: 0,
          crop: { top: 0, bottom: 0, left: 0, right: 0 },
        },
      });
      const scene = makeScene({ layers: [layer] });
      const frames = new Map<string, VideoFrame | ImageBitmap>();
      frames.set("src", makeFrame(2000, 1000));

      renderer.renderScene(scene, frames);

      expect(ctx.drawImage).toHaveBeenCalledWith(
        expect.anything(),
        0,
        0,
        2000,
        1000,
        0,
        250,
        1000,
        500
      );
    });

    it("letterbox taller source: shrinks width and centers horizontally", async () => {
      await renderer.init(canvas, makeConfig({ width: 1000, height: 1000 }));
      // Source is 1:2, dest is 1:1 → sourceAspect < destAspect
      // newDh = 1000, newDw = 1000*0.5 = 500
      // newDx = (1000-500)/2 = 250, newDy = 0
      const layer = makeLayer({
        sourceId: "src",
        scalingMode: "letterbox",
        transform: {
          x: 0,
          y: 0,
          width: 1,
          height: 1,
          opacity: 1,
          rotation: 0,
          borderRadius: 0,
          crop: { top: 0, bottom: 0, left: 0, right: 0 },
        },
      });
      const scene = makeScene({ layers: [layer] });
      const frames = new Map<string, VideoFrame | ImageBitmap>();
      frames.set("src", makeFrame(500, 1000));

      renderer.renderScene(scene, frames);

      expect(ctx.drawImage).toHaveBeenCalledWith(
        expect.anything(),
        0,
        0,
        500,
        1000,
        250,
        0,
        500,
        1000
      );
    });

    it("crop wider source: crops sides", async () => {
      await renderer.init(canvas, makeConfig({ width: 1000, height: 1000 }));
      // Source 2:1 (2000x1000), dest 1:1
      // sourceAspect(2) > destAspect(1) → crop sides
      // targetSw = 1000*1 = 1000, cropAmount = (2000-1000)/2 = 500
      // cropSx = 500, cropSw = 1000
      const layer = makeLayer({
        sourceId: "src",
        scalingMode: "crop",
        transform: {
          x: 0,
          y: 0,
          width: 1,
          height: 1,
          opacity: 1,
          rotation: 0,
          borderRadius: 0,
          crop: { top: 0, bottom: 0, left: 0, right: 0 },
        },
      });
      const scene = makeScene({ layers: [layer] });
      const frames = new Map<string, VideoFrame | ImageBitmap>();
      frames.set("src", makeFrame(2000, 1000));

      renderer.renderScene(scene, frames);

      expect(ctx.drawImage).toHaveBeenCalledWith(
        expect.anything(),
        500,
        0,
        1000,
        1000,
        0,
        0,
        1000,
        1000
      );
    });

    it("crop taller source: crops top/bottom", async () => {
      await renderer.init(canvas, makeConfig({ width: 1000, height: 1000 }));
      // Source 1:2 (500x1000), dest 1:1
      // sourceAspect(0.5) < destAspect(1) → crop top/bottom
      // targetSh = 500/1 = 500, cropAmount = (1000-500)/2 = 250
      // cropSy = 250, cropSh = 500
      const layer = makeLayer({
        sourceId: "src",
        scalingMode: "crop",
        transform: {
          x: 0,
          y: 0,
          width: 1,
          height: 1,
          opacity: 1,
          rotation: 0,
          borderRadius: 0,
          crop: { top: 0, bottom: 0, left: 0, right: 0 },
        },
      });
      const scene = makeScene({ layers: [layer] });
      const frames = new Map<string, VideoFrame | ImageBitmap>();
      frames.set("src", makeFrame(500, 1000));

      renderer.renderScene(scene, frames);

      expect(ctx.drawImage).toHaveBeenCalledWith(
        expect.anything(),
        0,
        250,
        500,
        500,
        0,
        0,
        1000,
        1000
      );
    });

    it("defaults to letterbox when scalingMode not set", async () => {
      await renderer.init(canvas, makeConfig({ width: 1000, height: 1000 }));
      const layer = makeLayer({
        sourceId: "src",
        scalingMode: undefined as any,
        transform: {
          x: 0,
          y: 0,
          width: 1,
          height: 1,
          opacity: 1,
          rotation: 0,
          borderRadius: 0,
          crop: { top: 0, bottom: 0, left: 0, right: 0 },
        },
      });
      const scene = makeScene({ layers: [layer] });
      const frames = new Map<string, VideoFrame | ImageBitmap>();
      frames.set("src", makeFrame(2000, 1000));

      renderer.renderScene(scene, frames);

      // letterbox: wider source in square → dy=250, dh=500
      expect(ctx.drawImage).toHaveBeenCalledWith(
        expect.anything(),
        0,
        0,
        2000,
        1000,
        0,
        250,
        1000,
        500
      );
    });
  });

  // ===========================================================================
  // renderTransition
  // ===========================================================================
  describe("renderTransition", () => {
    const from = makeImageBitmap(1920, 1080);
    const to = makeImageBitmap(1920, 1080);

    it("fade draws from at full alpha then to at progress alpha", async () => {
      await renderer.init(canvas, makeConfig());

      renderer.renderTransition(from, to, 0.5, "fade");

      expect(ctx.drawImage).toHaveBeenCalledTimes(2);
      // from drawn first at alpha 1, then to at alpha 0.5
      expect(ctx.drawImage).toHaveBeenNthCalledWith(1, from, 0, 0, 1920, 1080);
      expect(ctx.drawImage).toHaveBeenNthCalledWith(2, to, 0, 0, 1920, 1080);
    });

    it("fade resets globalAlpha to 1 after drawing", async () => {
      await renderer.init(canvas, makeConfig());

      renderer.renderTransition(from, to, 0.7, "fade");

      expect(ctx.globalAlpha).toBe(1);
    });

    it("slide-left offsets from left and to from right", async () => {
      await renderer.init(canvas, makeConfig());

      renderer.renderTransition(from, to, 0.5, "slide-left");

      // offset = 0.5 * 1920 = 960
      expect(ctx.drawImage).toHaveBeenCalledWith(from, -960, 0, 1920, 1080);
      expect(ctx.drawImage).toHaveBeenCalledWith(to, 960, 0, 1920, 1080);
    });

    it("slide-right offsets from right and to from left", async () => {
      await renderer.init(canvas, makeConfig());

      renderer.renderTransition(from, to, 0.5, "slide-right");

      // offset = 0.5 * 1920 = 960
      expect(ctx.drawImage).toHaveBeenCalledWith(from, 960, 0, 1920, 1080);
      expect(ctx.drawImage).toHaveBeenCalledWith(to, -960, 0, 1920, 1080);
    });

    it("slide-up offsets from up and to from bottom", async () => {
      await renderer.init(canvas, makeConfig());

      renderer.renderTransition(from, to, 0.5, "slide-up");

      // offset = 0.5 * 1080 = 540
      expect(ctx.drawImage).toHaveBeenCalledWith(from, 0, -540, 1920, 1080);
      expect(ctx.drawImage).toHaveBeenCalledWith(to, 0, 540, 1920, 1080);
    });

    it("slide-down offsets from down and to from top", async () => {
      await renderer.init(canvas, makeConfig());

      renderer.renderTransition(from, to, 0.5, "slide-down");

      // offset = 0.5 * 1080 = 540
      expect(ctx.drawImage).toHaveBeenCalledWith(from, 0, 540, 1920, 1080);
      expect(ctx.drawImage).toHaveBeenCalledWith(to, 0, -540, 1920, 1080);
    });

    it("cut draws only the target image", async () => {
      await renderer.init(canvas, makeConfig());

      renderer.renderTransition(from, to, 0.5, "cut");

      expect(ctx.drawImage).toHaveBeenCalledTimes(1);
      expect(ctx.drawImage).toHaveBeenCalledWith(to, 0, 0, 1920, 1080);
    });

    it("clamps progress below 0 to 0", async () => {
      await renderer.init(canvas, makeConfig());

      renderer.renderTransition(from, to, -0.5, "slide-left");

      // offset = 0 * 1920 = 0 (clamped), so from stays in place, to is offscreen
      expect(ctx.drawImage).toHaveBeenCalledTimes(2);
      const call1 = ctx.drawImage.mock.calls[0];
      const call2 = ctx.drawImage.mock.calls[1];
      expect(call1[0]).toBe(from);
      expect(Math.abs(call1[1])).toBe(0); // -0 or 0
      expect(call2[0]).toBe(to);
      expect(call2[1]).toBe(1920);
    });

    it("clamps progress above 1 to 1", async () => {
      await renderer.init(canvas, makeConfig());

      renderer.renderTransition(from, to, 1.5, "slide-left");

      // offset = 1 * 1920 = 1920
      expect(ctx.drawImage).toHaveBeenCalledWith(from, -1920, 0, 1920, 1080);
      expect(ctx.drawImage).toHaveBeenCalledWith(to, 0, 0, 1920, 1080);
    });

    it("unknown type falls back to cut", async () => {
      await renderer.init(canvas, makeConfig());

      renderer.renderTransition(from, to, 0.5, "wipe" as any);

      expect(ctx.drawImage).toHaveBeenCalledTimes(1);
      expect(ctx.drawImage).toHaveBeenCalledWith(to, 0, 0, 1920, 1080);
    });
  });

  // ===========================================================================
  // applyFilter
  // ===========================================================================
  describe("applyFilter", () => {
    it("logs a warning about unsupported filters", async () => {
      await renderer.init(canvas, makeConfig());

      renderer.applyFilter("layer-1", { type: "blur", strength: 5 } as FilterConfig);

      expect(console.warn).toHaveBeenCalledWith(expect.stringContaining("Filters not supported"));
    });
  });

  // ===========================================================================
  // captureFrame
  // ===========================================================================
  describe("captureFrame", () => {
    it("creates a VideoFrame from the canvas", async () => {
      const mockVideoFrame = { close: vi.fn() };
      (globalThis as any).VideoFrame = vi.fn(function (this: any) {
        return mockVideoFrame;
      });

      await renderer.init(canvas, makeConfig());

      vi.spyOn(performance, "now").mockReturnValue(2000);
      const frame = renderer.captureFrame();

      expect((globalThis as any).VideoFrame).toHaveBeenCalledWith(canvas, {
        timestamp: 2000 * 1000,
      });
      expect(frame).toBe(mockVideoFrame);

      delete (globalThis as any).VideoFrame;
    });
  });

  // ===========================================================================
  // resize
  // ===========================================================================
  describe("resize", () => {
    it("updates config and re-acquires context", async () => {
      await renderer.init(canvas, makeConfig({ width: 1920, height: 1080 }));

      renderer.resize(makeConfig({ width: 1280, height: 720 }));

      // getContext called once in init, once in resize
      expect(canvas.getContext).toHaveBeenCalledTimes(2);
    });

    it("sets image smoothing after resize", async () => {
      await renderer.init(canvas, makeConfig());
      ctx.imageSmoothingEnabled = false;
      ctx.imageSmoothingQuality = "low" as ImageSmoothingQuality;

      renderer.resize(makeConfig({ width: 1280, height: 720 }));

      expect(ctx.imageSmoothingEnabled).toBe(true);
      expect(ctx.imageSmoothingQuality).toBe("high");
    });

    it("throws when resize context fails", async () => {
      await renderer.init(canvas, makeConfig());
      canvas.getContext = vi.fn(() => null);

      expect(() => renderer.resize(makeConfig())).toThrow("Failed to get 2D context");
    });
  });

  // ===========================================================================
  // getStats
  // ===========================================================================
  describe("getStats", () => {
    it("returns zero fps initially", async () => {
      await renderer.init(canvas, makeConfig());

      const stats = renderer.getStats();
      expect(stats.fps).toBe(0);
      expect(stats.frameTimeMs).toBe(0);
    });

    it("calculates fps after 1 second of rendering", async () => {
      let now = 1000;
      vi.spyOn(performance, "now").mockImplementation(() => now);
      await renderer.init(canvas, makeConfig());

      const scene = makeScene();

      // Render 30 frames over 1 second
      for (let i = 0; i < 30; i++) {
        now += 33.33;
        renderer.renderScene(scene, new Map());
      }

      // Now at ~2000ms, elapsed > 1000ms
      now = 2001;
      renderer.renderScene(scene, new Map());

      const stats = renderer.getStats();
      expect(stats.fps).toBeGreaterThan(0);
    });

    it("tracks frameTimeMs from last render", async () => {
      let now = 1000;
      vi.spyOn(performance, "now").mockImplementation(() => now);
      await renderer.init(canvas, makeConfig());

      // Mock performance.now to simulate render time
      const originalNow = performance.now;
      let callCount = 0;
      vi.spyOn(performance, "now").mockImplementation(() => {
        callCount++;
        // First call in renderScene = startTime, second = end
        return callCount % 2 === 1 ? 1000 : 1005;
      });

      renderer.renderScene(makeScene(), new Map());

      const stats = renderer.getStats();
      expect(stats.frameTimeMs).toBe(5);
    });
  });

  // ===========================================================================
  // destroy
  // ===========================================================================
  describe("destroy", () => {
    it("nullifies canvas and context references", async () => {
      await renderer.init(canvas, makeConfig());
      renderer.destroy();

      // After destroy, rendering should fail (null references)
      expect(() => renderer.renderScene(makeScene(), new Map())).toThrow();
    });
  });
});

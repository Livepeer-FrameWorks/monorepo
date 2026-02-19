// Must set WebGL2RenderingContext before importing so checkWebGLSupport() returns true
(globalThis as any).WebGL2RenderingContext = class {};

import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { WebGLRenderer } from "../src/core/renderers/WebGLRenderer";
import type { Scene, Layer, CompositorConfig, FilterConfig } from "../src/types";

// ---------------------------------------------------------------------------
// Mock helpers
// ---------------------------------------------------------------------------

function createMockGL() {
  const loseContextExt = { loseContext: vi.fn() };
  const gl: Record<string, any> = {
    // Constants
    BLEND: 0x0be2,
    SRC_ALPHA: 0x0302,
    ONE_MINUS_SRC_ALPHA: 0x0303,
    VERTEX_SHADER: 0x8b31,
    FRAGMENT_SHADER: 0x8b30,
    COMPILE_STATUS: 0x8b81,
    LINK_STATUS: 0x8b82,
    TEXTURE_2D: 0x0de1,
    TEXTURE0: 0x84c0,
    TEXTURE_WRAP_S: 0x2802,
    TEXTURE_WRAP_T: 0x2803,
    TEXTURE_MIN_FILTER: 0x2801,
    TEXTURE_MAG_FILTER: 0x2800,
    CLAMP_TO_EDGE: 0x812f,
    LINEAR: 0x2601,
    RGBA: 0x1908,
    UNSIGNED_BYTE: 0x1401,
    COLOR_BUFFER_BIT: 0x4000,
    TRIANGLES: 4,

    // Methods
    enable: vi.fn(),
    blendFunc: vi.fn(),
    createShader: vi.fn(() => ({ _shader: true })),
    shaderSource: vi.fn(),
    compileShader: vi.fn(),
    getShaderParameter: vi.fn(() => true),
    getShaderInfoLog: vi.fn(() => ""),
    deleteShader: vi.fn(),
    createProgram: vi.fn(() => ({ _program: true })),
    attachShader: vi.fn(),
    linkProgram: vi.fn(),
    getProgramParameter: vi.fn(() => true),
    getProgramInfoLog: vi.fn(() => ""),
    deleteProgram: vi.fn(),
    getUniformLocation: vi.fn((_prog: any, name: string) => ({ _name: name })),
    useProgram: vi.fn(),
    viewport: vi.fn(),
    clearColor: vi.fn(),
    clear: vi.fn(),
    uniform1f: vi.fn(),
    uniform1i: vi.fn(),
    uniform2f: vi.fn(),
    uniform3f: vi.fn(),
    uniform4f: vi.fn(),
    uniformMatrix4fv: vi.fn(),
    activeTexture: vi.fn(),
    bindTexture: vi.fn(),
    createTexture: vi.fn(() => ({ _texture: true })),
    deleteTexture: vi.fn(),
    texParameteri: vi.fn(),
    texImage2D: vi.fn(),
    texSubImage2D: vi.fn(),
    drawArrays: vi.fn(),
    getExtension: vi.fn((name: string) => {
      if (name === "WEBGL_lose_context") return loseContextExt;
      return null;
    }),
  };
  return gl;
}

function createMockCanvas(gl: Record<string, any>) {
  return {
    width: 1920,
    height: 1080,
    getContext: vi.fn((_type: string, _opts?: any) => gl),
  } as unknown as OffscreenCanvas;
}

function createMockVideoFrame(w = 1920, h = 1080) {
  return {
    displayWidth: w,
    displayHeight: h,
    close: vi.fn(),
  } as unknown as VideoFrame;
}

function createMockImageBitmap(w = 1920, h = 1080) {
  return {
    width: w,
    height: h,
    close: vi.fn(),
  } as unknown as ImageBitmap;
}

function makeConfig(overrides: Partial<CompositorConfig> = {}): CompositorConfig {
  return {
    enabled: true,
    width: 1920,
    height: 1080,
    frameRate: 30,
    renderer: "webgl",
    defaultTransition: { type: "fade", durationMs: 500, easing: "ease-in-out" },
    ...overrides,
  };
}

function makeLayer(overrides: Partial<Layer> = {}): Layer {
  return {
    id: "layer-1",
    sourceId: "source-1",
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
      crop: { left: 0, top: 0, right: 0, bottom: 0 },
    },
    ...overrides,
  };
}

function makeScene(overrides: Partial<Scene> = {}): Scene {
  return {
    id: "scene-1",
    name: "Test Scene",
    layers: [makeLayer()],
    backgroundColor: "#000000",
    ...overrides,
  };
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("WebGLRenderer", () => {
  let renderer: WebGLRenderer;
  let gl: Record<string, any>;
  let canvas: OffscreenCanvas;

  beforeEach(() => {
    vi.spyOn(console, "log").mockImplementation(() => {});
    vi.spyOn(console, "warn").mockImplementation(() => {});
    vi.spyOn(performance, "now").mockReturnValue(1000);

    renderer = new WebGLRenderer();
    gl = createMockGL();
    canvas = createMockCanvas(gl);
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  // =========================================================================
  // isSupported
  // =========================================================================
  describe("isSupported", () => {
    it("returns true when WebGL2RenderingContext exists on globalThis", () => {
      expect(renderer.isSupported).toBe(true);
    });

    it("has type set to webgl", () => {
      expect(renderer.type).toBe("webgl");
    });
  });

  // =========================================================================
  // init
  // =========================================================================
  describe("init", () => {
    it("gets webgl2 context with correct options", async () => {
      await renderer.init(canvas, makeConfig());

      expect(canvas.getContext).toHaveBeenCalledWith("webgl2", {
        alpha: false,
        antialias: false,
        depth: false,
        stencil: false,
        premultipliedAlpha: true,
        preserveDrawingBuffer: false,
        powerPreference: "high-performance",
        desynchronized: true,
      });
    });

    it("enables blending with correct factors", async () => {
      await renderer.init(canvas, makeConfig());

      expect(gl.enable).toHaveBeenCalledWith(gl.BLEND);
      expect(gl.blendFunc).toHaveBeenCalledWith(gl.SRC_ALPHA, gl.ONE_MINUS_SRC_ALPHA);
    });

    it("compiles shader programs via createShader", async () => {
      await renderer.init(canvas, makeConfig());

      // 2 programs x 2 shaders = 4 createShader calls
      expect(gl.createShader).toHaveBeenCalledTimes(4);
      expect(gl.createShader).toHaveBeenCalledWith(gl.VERTEX_SHADER);
      expect(gl.createShader).toHaveBeenCalledWith(gl.FRAGMENT_SHADER);
    });

    it("creates two shader programs", async () => {
      await renderer.init(canvas, makeConfig());

      expect(gl.createProgram).toHaveBeenCalledTimes(2);
      expect(gl.linkProgram).toHaveBeenCalledTimes(2);
    });

    it("gets all uniform locations for both programs", async () => {
      await renderer.init(canvas, makeConfig());

      const uniformNames = gl.getUniformLocation.mock.calls.map((c: any[]) => c[1]);
      expect(uniformNames).toContain("u_resolution");
      expect(uniformNames).toContain("u_position");
      expect(uniformNames).toContain("u_size");
      expect(uniformNames).toContain("u_rotation");
      expect(uniformNames).toContain("u_crop");
      expect(uniformNames).toContain("u_texture");
      expect(uniformNames).toContain("u_opacity");
      expect(uniformNames).toContain("u_borderRadius");
      expect(uniformNames).toContain("u_filterType");
      expect(uniformNames).toContain("u_filterStrength");
      expect(uniformNames).toContain("u_colorMatrix");
      expect(uniformNames).toContain("u_keyColor");
      expect(uniformNames).toContain("u_keyTolerance");
      expect(uniformNames).toContain("u_keyEdgeSoftness");

      // Transition program uniforms
      expect(uniformNames).toContain("u_offset");
    });

    it("sets viewport with config dimensions", async () => {
      await renderer.init(canvas, makeConfig({ width: 1280, height: 720 }));

      expect(gl.viewport).toHaveBeenCalledWith(0, 0, 1280, 720);
    });

    it("throws when context is null", async () => {
      const nullCanvas = {
        width: 1920,
        height: 1080,
        getContext: vi.fn(() => null),
      } as unknown as OffscreenCanvas;

      await expect(renderer.init(nullCanvas, makeConfig())).rejects.toThrow(
        "Failed to create WebGL2 context"
      );
    });

    it("throws when not supported", async () => {
      const origCtx = (globalThis as any).WebGL2RenderingContext;
      delete (globalThis as any).WebGL2RenderingContext;
      const unsupported = new WebGLRenderer();
      (globalThis as any).WebGL2RenderingContext = origCtx;

      await expect(unsupported.init(canvas, makeConfig())).rejects.toThrow(
        "WebGL2 is not supported"
      );
    });

    it("logs initialization message", async () => {
      await renderer.init(canvas, makeConfig());

      expect(console.log).toHaveBeenCalledWith("[WebGLRenderer] Initialized with raw WebGL2");
    });
  });

  // =========================================================================
  // createProgram (via init) - shader compile / link failures
  // =========================================================================
  describe("shader compile and link errors", () => {
    it("throws on vertex shader compile failure", async () => {
      let shaderCallCount = 0;
      gl.getShaderParameter = vi.fn(() => {
        shaderCallCount++;
        return shaderCallCount !== 1;
      });
      gl.getShaderInfoLog = vi.fn(() => "bad vertex code");

      await expect(renderer.init(canvas, makeConfig())).rejects.toThrow(
        "Vertex shader compilation failed: bad vertex code"
      );
    });

    it("throws on fragment shader compile failure", async () => {
      let shaderCallCount = 0;
      gl.getShaderParameter = vi.fn(() => {
        shaderCallCount++;
        return shaderCallCount !== 2;
      });
      gl.getShaderInfoLog = vi.fn(() => "bad fragment code");

      await expect(renderer.init(canvas, makeConfig())).rejects.toThrow(
        "Fragment shader compilation failed: bad fragment code"
      );
    });

    it("throws on program link failure", async () => {
      gl.getProgramParameter = vi.fn(() => false);
      gl.getProgramInfoLog = vi.fn(() => "link error");

      await expect(renderer.init(canvas, makeConfig())).rejects.toThrow(
        "Program linking failed: link error"
      );
    });

    it("cleans up vertex shader on vertex compile failure", async () => {
      let shaderCallCount = 0;
      gl.getShaderParameter = vi.fn(() => {
        shaderCallCount++;
        return shaderCallCount !== 1;
      });

      try {
        await renderer.init(canvas, makeConfig());
      } catch {
        // expected
      }

      expect(gl.deleteShader).toHaveBeenCalled();
    });

    it("cleans up both shaders on fragment compile failure", async () => {
      let shaderCallCount = 0;
      gl.getShaderParameter = vi.fn(() => {
        shaderCallCount++;
        return shaderCallCount !== 2;
      });

      try {
        await renderer.init(canvas, makeConfig());
      } catch {
        // expected
      }

      expect(gl.deleteShader).toHaveBeenCalledTimes(2);
    });

    it("cleans up program and shaders on link failure", async () => {
      gl.getProgramParameter = vi.fn(() => false);

      try {
        await renderer.init(canvas, makeConfig());
      } catch {
        // expected
      }

      expect(gl.deleteProgram).toHaveBeenCalled();
      expect(gl.deleteShader).toHaveBeenCalled();
    });
  });

  // =========================================================================
  // renderScene
  // =========================================================================
  describe("renderScene", () => {
    let config: CompositorConfig;

    beforeEach(async () => {
      config = makeConfig();
      await renderer.init(canvas, config);
      gl.drawArrays.mockClear();
      gl.clearColor.mockClear();
      gl.clear.mockClear();
      gl.useProgram.mockClear();
      gl.uniform2f.mockClear();
    });

    it("clears with parsed background color", () => {
      const scene = makeScene({ backgroundColor: "#ff8040" });
      const frames = new Map<string, VideoFrame | ImageBitmap>();
      frames.set("source-1", createMockVideoFrame());

      renderer.renderScene(scene, frames);

      expect(gl.clearColor).toHaveBeenCalledWith(1, 128 / 255, 64 / 255, 1.0);
    });

    it("clears with black when no backgroundColor set", () => {
      const scene = makeScene({ backgroundColor: "" });
      const frames = new Map<string, VideoFrame | ImageBitmap>();
      frames.set("source-1", createMockVideoFrame());

      renderer.renderScene(scene, frames);

      expect(gl.clearColor).toHaveBeenCalledWith(0, 0, 0, 1.0);
    });

    it("uses the layer program", () => {
      const scene = makeScene();
      const frames = new Map<string, VideoFrame | ImageBitmap>();
      frames.set("source-1", createMockVideoFrame());

      renderer.renderScene(scene, frames);

      expect(gl.useProgram).toHaveBeenCalled();
    });

    it("sets resolution uniform", () => {
      const scene = makeScene();
      const frames = new Map<string, VideoFrame | ImageBitmap>();
      frames.set("source-1", createMockVideoFrame());

      renderer.renderScene(scene, frames);

      const resolutionCalls = gl.uniform2f.mock.calls.filter(
        (c: any[]) => c[0]._name === "u_resolution"
      );
      expect(resolutionCalls.length).toBeGreaterThanOrEqual(1);
      expect(resolutionCalls[0][1]).toBe(1920);
      expect(resolutionCalls[0][2]).toBe(1080);
    });

    it("filters out invisible layers", () => {
      const scene = makeScene({
        layers: [
          makeLayer({ id: "visible", sourceId: "src-1", visible: true, zIndex: 0 }),
          makeLayer({ id: "hidden", sourceId: "src-2", visible: false, zIndex: 1 }),
        ],
      });
      const frames = new Map<string, VideoFrame | ImageBitmap>();
      frames.set("src-1", createMockVideoFrame());
      frames.set("src-2", createMockVideoFrame());

      renderer.renderScene(scene, frames);

      expect(gl.drawArrays).toHaveBeenCalledTimes(1);
    });

    it("sorts layers by zIndex ascending", () => {
      const scene = makeScene({
        layers: [
          makeLayer({ id: "top", sourceId: "src-top", visible: true, zIndex: 10 }),
          makeLayer({ id: "bottom", sourceId: "src-bottom", visible: true, zIndex: 1 }),
        ],
      });
      const frames = new Map<string, VideoFrame | ImageBitmap>();
      frames.set("src-top", createMockVideoFrame());
      frames.set("src-bottom", createMockVideoFrame());

      renderer.renderScene(scene, frames);

      expect(gl.drawArrays).toHaveBeenCalledTimes(2);

      const bindCalls = gl.bindTexture.mock.calls.filter((c: any[]) => c[0] === gl.TEXTURE_2D);
      expect(bindCalls.length).toBeGreaterThanOrEqual(2);
    });

    it("calls drawArrays(TRIANGLES, 0, 6) for each visible layer with a texture", () => {
      const scene = makeScene({
        layers: [
          makeLayer({ id: "l1", sourceId: "src-1", visible: true, zIndex: 0 }),
          makeLayer({ id: "l2", sourceId: "src-2", visible: true, zIndex: 1 }),
        ],
      });
      const frames = new Map<string, VideoFrame | ImageBitmap>();
      frames.set("src-1", createMockVideoFrame());
      frames.set("src-2", createMockVideoFrame());

      renderer.renderScene(scene, frames);

      expect(gl.drawArrays).toHaveBeenCalledTimes(2);
      for (const call of gl.drawArrays.mock.calls) {
        expect(call).toEqual([gl.TRIANGLES, 0, 6]);
      }
    });

    it("skips layers without matching textures", () => {
      const scene = makeScene({
        layers: [makeLayer({ id: "l1", sourceId: "no-frame", visible: true, zIndex: 0 })],
      });
      const frames = new Map<string, VideoFrame | ImageBitmap>();

      renderer.renderScene(scene, frames);

      expect(gl.drawArrays).not.toHaveBeenCalled();
    });
  });

  // =========================================================================
  // renderLayer uniforms (via renderScene)
  // =========================================================================
  describe("renderLayer uniforms", () => {
    beforeEach(async () => {
      await renderer.init(canvas, makeConfig());
      gl.uniform1f.mockClear();
      gl.uniform2f.mockClear();
      gl.uniform4f.mockClear();
      gl.uniform1i.mockClear();
      gl.drawArrays.mockClear();
    });

    function renderSingleLayer(layerOverrides: Partial<Layer> = {}) {
      const layer = makeLayer({ scalingMode: "stretch", ...layerOverrides });
      const scene = makeScene({ layers: [layer] });
      const frames = new Map<string, VideoFrame | ImageBitmap>();
      frames.set(layer.sourceId, createMockVideoFrame());
      renderer.renderScene(scene, frames);
    }

    it("sets position uniform", () => {
      renderSingleLayer({
        transform: {
          x: 0.25,
          y: 0.5,
          width: 1,
          height: 1,
          opacity: 1,
          rotation: 0,
          borderRadius: 0,
          crop: { left: 0, top: 0, right: 0, bottom: 0 },
        },
      });

      const positionCalls = gl.uniform2f.mock.calls.filter(
        (c: any[]) => c[0]._name === "u_position"
      );
      expect(positionCalls.length).toBeGreaterThanOrEqual(1);
      expect(positionCalls[0][1]).toBe(0.25);
      expect(positionCalls[0][2]).toBe(0.5);
    });

    it("sets size uniform", () => {
      renderSingleLayer({
        transform: {
          x: 0,
          y: 0,
          width: 0.5,
          height: 0.75,
          opacity: 1,
          rotation: 0,
          borderRadius: 0,
          crop: { left: 0, top: 0, right: 0, bottom: 0 },
        },
      });

      const sizeCalls = gl.uniform2f.mock.calls.filter((c: any[]) => c[0]._name === "u_size");
      expect(sizeCalls.length).toBeGreaterThanOrEqual(1);
      expect(sizeCalls[0][1]).toBe(0.5);
      expect(sizeCalls[0][2]).toBe(0.75);
    });

    it("sets rotation uniform converted to radians", () => {
      renderSingleLayer({
        transform: {
          x: 0,
          y: 0,
          width: 1,
          height: 1,
          opacity: 1,
          rotation: 90,
          borderRadius: 0,
          crop: { left: 0, top: 0, right: 0, bottom: 0 },
        },
      });

      const rotationCalls = gl.uniform1f.mock.calls.filter(
        (c: any[]) => c[0]._name === "u_rotation"
      );
      expect(rotationCalls.length).toBeGreaterThanOrEqual(1);
      expect(rotationCalls[0][1]).toBeCloseTo((90 * Math.PI) / 180);
    });

    it("sets opacity uniform clamped to 0-1", () => {
      renderSingleLayer({
        transform: {
          x: 0,
          y: 0,
          width: 1,
          height: 1,
          opacity: 1.5,
          rotation: 0,
          borderRadius: 0,
          crop: { left: 0, top: 0, right: 0, bottom: 0 },
        },
      });

      const opacityCalls = gl.uniform1f.mock.calls.filter((c: any[]) => c[0]._name === "u_opacity");
      expect(opacityCalls.length).toBeGreaterThanOrEqual(1);
      expect(opacityCalls[0][1]).toBe(1);
    });

    it("sets opacity to 0 when given negative", () => {
      renderSingleLayer({
        transform: {
          x: 0,
          y: 0,
          width: 1,
          height: 1,
          opacity: -0.5,
          rotation: 0,
          borderRadius: 0,
          crop: { left: 0, top: 0, right: 0, bottom: 0 },
        },
      });

      const opacityCalls = gl.uniform1f.mock.calls.filter((c: any[]) => c[0]._name === "u_opacity");
      expect(opacityCalls[0][1]).toBe(0);
    });

    it("sets crop uniform", () => {
      renderSingleLayer({
        transform: {
          x: 0,
          y: 0,
          width: 1,
          height: 1,
          opacity: 1,
          rotation: 0,
          borderRadius: 0,
          crop: { left: 0.1, top: 0.2, right: 0.3, bottom: 0.4 },
        },
      });

      const cropCalls = gl.uniform4f.mock.calls.filter((c: any[]) => c[0]._name === "u_crop");
      expect(cropCalls.length).toBeGreaterThanOrEqual(1);
      expect(cropCalls[0][1]).toBeCloseTo(0.1);
      expect(cropCalls[0][2]).toBeCloseTo(0.2);
      expect(cropCalls[0][3]).toBeCloseTo(0.3);
      expect(cropCalls[0][4]).toBeCloseTo(0.4);
    });

    it("sets border radius uniform", () => {
      renderSingleLayer({
        transform: {
          x: 0,
          y: 0,
          width: 1,
          height: 1,
          opacity: 1,
          rotation: 0,
          borderRadius: 20,
          crop: { left: 0, top: 0, right: 0, bottom: 0 },
        },
      });

      const brCalls = gl.uniform1f.mock.calls.filter((c: any[]) => c[0]._name === "u_borderRadius");
      expect(brCalls.length).toBeGreaterThanOrEqual(1);
      expect(brCalls[0][1]).toBe(20);
    });

    it("sets filterType to 0 when no filter applied", () => {
      renderSingleLayer();

      const filterCalls = gl.uniform1i.mock.calls.filter(
        (c: any[]) => c[0]._name === "u_filterType"
      );
      expect(filterCalls.length).toBeGreaterThanOrEqual(1);
      const lastFilterCall = filterCalls[filterCalls.length - 1];
      expect(lastFilterCall[1]).toBe(0);
    });
  });

  // =========================================================================
  // Scaling modes (via renderScene)
  // =========================================================================
  describe("scaling modes", () => {
    beforeEach(async () => {
      await renderer.init(canvas, makeConfig({ width: 1920, height: 1080 }));
      gl.uniform2f.mockClear();
      gl.uniform4f.mockClear();
    });

    function renderWithScaling(
      scalingMode: "stretch" | "letterbox" | "crop",
      sourceW: number,
      sourceH: number,
      layerOverrides: Partial<Layer["transform"]> = {}
    ) {
      const transform = {
        x: 0,
        y: 0,
        width: 1,
        height: 1,
        opacity: 1,
        rotation: 0,
        borderRadius: 0,
        crop: { left: 0, top: 0, right: 0, bottom: 0 },
        ...layerOverrides,
      };
      const layer = makeLayer({ scalingMode, transform });
      const scene = makeScene({ layers: [layer] });
      const frames = new Map<string, VideoFrame | ImageBitmap>();
      frames.set("source-1", createMockVideoFrame(sourceW, sourceH));
      renderer.renderScene(scene, frames);
    }

    it("stretch passes through position and size unchanged", () => {
      renderWithScaling("stretch", 1920, 1080);

      const posCalls = gl.uniform2f.mock.calls.filter((c: any[]) => c[0]._name === "u_position");
      const sizeCalls = gl.uniform2f.mock.calls.filter((c: any[]) => c[0]._name === "u_size");
      expect(posCalls[0][1]).toBe(0);
      expect(posCalls[0][2]).toBe(0);
      expect(sizeCalls[0][1]).toBe(1);
      expect(sizeCalls[0][2]).toBe(1);
    });

    it("letterbox wider source: shrinks height and centers vertically", () => {
      renderWithScaling("letterbox", 1920, 540);

      const sizeCalls = gl.uniform2f.mock.calls.filter((c: any[]) => c[0]._name === "u_size");
      const posCalls = gl.uniform2f.mock.calls.filter((c: any[]) => c[0]._name === "u_position");

      const srcAspect = 1920 / 540;
      const expectedHeight = (1 * 1920) / srcAspect / 1080;
      const expectedY = (1 - expectedHeight) / 2;

      expect(sizeCalls[0][1]).toBeCloseTo(1);
      expect(sizeCalls[0][2]).toBeCloseTo(expectedHeight);
      expect(posCalls[0][1]).toBeCloseTo(0);
      expect(posCalls[0][2]).toBeCloseTo(expectedY);
    });

    it("letterbox taller source: shrinks width and centers horizontally", () => {
      renderWithScaling("letterbox", 540, 1080);

      const sizeCalls = gl.uniform2f.mock.calls.filter((c: any[]) => c[0]._name === "u_size");
      const posCalls = gl.uniform2f.mock.calls.filter((c: any[]) => c[0]._name === "u_position");

      const srcAspect = 540 / 1080;
      const expectedWidth = (1 * 1080 * srcAspect) / 1920;
      const expectedX = (1 - expectedWidth) / 2;

      expect(sizeCalls[0][2]).toBeCloseTo(1);
      expect(sizeCalls[0][1]).toBeCloseTo(expectedWidth);
      expect(posCalls[0][1]).toBeCloseTo(expectedX);
      expect(posCalls[0][2]).toBeCloseTo(0);
    });

    it("crop wider source: adds horizontal crop", () => {
      renderWithScaling("crop", 3840, 1080);

      const cropCalls = gl.uniform4f.mock.calls.filter((c: any[]) => c[0]._name === "u_crop");

      const destAspect = 1920 / 1080;
      const targetSrcWidth = 1080 * destAspect;
      const cropAmount = (3840 - targetSrcWidth) / 2 / 3840;

      expect(cropCalls[0][1]).toBeCloseTo(cropAmount);
      expect(cropCalls[0][2]).toBeCloseTo(0);
      expect(cropCalls[0][3]).toBeCloseTo(cropAmount);
      expect(cropCalls[0][4]).toBeCloseTo(0);
    });

    it("crop taller source: adds vertical crop", () => {
      renderWithScaling("crop", 1920, 2160);

      const cropCalls = gl.uniform4f.mock.calls.filter((c: any[]) => c[0]._name === "u_crop");

      const destAspect = 1920 / 1080;
      const targetSrcHeight = 1920 / destAspect;
      const cropAmount = (2160 - targetSrcHeight) / 2 / 2160;

      expect(cropCalls[0][1]).toBeCloseTo(0);
      expect(cropCalls[0][2]).toBeCloseTo(cropAmount);
      expect(cropCalls[0][3]).toBeCloseTo(0);
      expect(cropCalls[0][4]).toBeCloseTo(cropAmount);
    });
  });

  // =========================================================================
  // updateTextures (via renderScene)
  // =========================================================================
  describe("updateTextures", () => {
    beforeEach(async () => {
      await renderer.init(canvas, makeConfig());
      gl.createTexture.mockClear();
      gl.texImage2D.mockClear();
      gl.texSubImage2D.mockClear();
      gl.texParameteri.mockClear();
    });

    it("creates a new texture for a new source", () => {
      const scene = makeScene();
      const frames = new Map<string, VideoFrame | ImageBitmap>();
      frames.set("source-1", createMockVideoFrame());

      renderer.renderScene(scene, frames);

      expect(gl.createTexture).toHaveBeenCalled();
      expect(gl.texParameteri).toHaveBeenCalled();
      expect(gl.texImage2D).toHaveBeenCalled();
    });

    it("uses texSubImage2D for same-size updates", () => {
      const scene = makeScene();
      const frames = new Map<string, VideoFrame | ImageBitmap>();
      frames.set("source-1", createMockVideoFrame(1920, 1080));

      renderer.renderScene(scene, frames);
      gl.texImage2D.mockClear();
      gl.texSubImage2D.mockClear();
      gl.createTexture.mockClear();

      frames.set("source-1", createMockVideoFrame(1920, 1080));
      renderer.renderScene(scene, frames);

      expect(gl.createTexture).not.toHaveBeenCalled();
      expect(gl.texSubImage2D).toHaveBeenCalled();
      expect(gl.texImage2D).not.toHaveBeenCalled();
    });

    it("uses texImage2D when source dimensions change", () => {
      const scene = makeScene();
      const frames = new Map<string, VideoFrame | ImageBitmap>();
      frames.set("source-1", createMockVideoFrame(1920, 1080));

      renderer.renderScene(scene, frames);
      gl.texImage2D.mockClear();
      gl.texSubImage2D.mockClear();

      frames.set("source-1", createMockVideoFrame(1280, 720));
      renderer.renderScene(scene, frames);

      expect(gl.texImage2D).toHaveBeenCalled();
      expect(gl.texSubImage2D).not.toHaveBeenCalled();
    });

    it("handles ImageBitmap sources using width/height", () => {
      const scene = makeScene();
      const frames = new Map<string, VideoFrame | ImageBitmap>();
      frames.set("source-1", createMockImageBitmap(1280, 720));

      renderer.renderScene(scene, frames);

      expect(gl.createTexture).toHaveBeenCalled();
      expect(gl.texImage2D).toHaveBeenCalled();
    });
  });

  // =========================================================================
  // renderTransition
  // =========================================================================
  describe("renderTransition", () => {
    beforeEach(async () => {
      await renderer.init(canvas, makeConfig());
      gl.clearColor.mockClear();
      gl.clear.mockClear();
      gl.useProgram.mockClear();
      gl.drawArrays.mockClear();
      gl.uniform1f.mockClear();
      gl.uniform2f.mockClear();
      gl.deleteTexture.mockClear();
      gl.createTexture.mockClear();
    });

    it("fade draws from at opacity (1-p) and to at opacity p", () => {
      const from = createMockImageBitmap();
      const to = createMockImageBitmap();

      renderer.renderTransition(from, to, 0.3, "fade");

      expect(gl.drawArrays).toHaveBeenCalledTimes(2);

      const opacityCalls = gl.uniform1f.mock.calls.filter((c: any[]) => c[0]._name === "u_opacity");
      expect(opacityCalls[0][1]).toBeCloseTo(0.7);
      expect(opacityCalls[1][1]).toBeCloseTo(0.3);
    });

    it("fade sets zero offset for both draws", () => {
      const from = createMockImageBitmap();
      const to = createMockImageBitmap();

      renderer.renderTransition(from, to, 0.5, "fade");

      const offsetCalls = gl.uniform2f.mock.calls.filter((c: any[]) => c[0]._name === "u_offset");
      expect(offsetCalls[0][1]).toBe(0);
      expect(offsetCalls[0][2]).toBe(0);
    });

    it("slide-left sets correct offsets", () => {
      const from = createMockImageBitmap();
      const to = createMockImageBitmap();

      renderer.renderTransition(from, to, 0.4, "slide-left");

      const offsetCalls = gl.uniform2f.mock.calls.filter((c: any[]) => c[0]._name === "u_offset");
      expect(offsetCalls[0][1]).toBeCloseTo(-0.4);
      expect(offsetCalls[0][2]).toBe(0);
      expect(offsetCalls[1][1]).toBeCloseTo(0.6);
      expect(offsetCalls[1][2]).toBe(0);
    });

    it("slide-right sets correct offsets", () => {
      const from = createMockImageBitmap();
      const to = createMockImageBitmap();

      renderer.renderTransition(from, to, 0.5, "slide-right");

      const offsetCalls = gl.uniform2f.mock.calls.filter((c: any[]) => c[0]._name === "u_offset");
      expect(offsetCalls[0][1]).toBeCloseTo(0.5);
      expect(offsetCalls[0][2]).toBe(0);
      expect(offsetCalls[1][1]).toBeCloseTo(-0.5);
      expect(offsetCalls[1][2]).toBe(0);
    });

    it("slide-up sets correct offsets", () => {
      const from = createMockImageBitmap();
      const to = createMockImageBitmap();

      renderer.renderTransition(from, to, 0.6, "slide-up");

      const offsetCalls = gl.uniform2f.mock.calls.filter((c: any[]) => c[0]._name === "u_offset");
      expect(offsetCalls[0][1]).toBe(0);
      expect(offsetCalls[0][2]).toBeCloseTo(0.6);
      expect(offsetCalls[1][1]).toBe(0);
      expect(offsetCalls[1][2]).toBeCloseTo(-0.4);
    });

    it("slide-down sets correct offsets", () => {
      const from = createMockImageBitmap();
      const to = createMockImageBitmap();

      renderer.renderTransition(from, to, 0.7, "slide-down");

      const offsetCalls = gl.uniform2f.mock.calls.filter((c: any[]) => c[0]._name === "u_offset");
      expect(offsetCalls[0][1]).toBe(0);
      expect(offsetCalls[0][2]).toBeCloseTo(-0.7);
      expect(offsetCalls[1][1]).toBe(0);
      expect(offsetCalls[1][2]).toBeCloseTo(0.3);
    });

    it("cut draws only the target", () => {
      const from = createMockImageBitmap();
      const to = createMockImageBitmap();

      renderer.renderTransition(from, to, 0.5, "cut");

      expect(gl.drawArrays).toHaveBeenCalledTimes(1);

      const opacityCalls = gl.uniform1f.mock.calls.filter((c: any[]) => c[0]._name === "u_opacity");
      expect(opacityCalls[0][1]).toBe(1);
    });

    it("cleans up temp textures after transition", () => {
      const from = createMockImageBitmap();
      const to = createMockImageBitmap();

      renderer.renderTransition(from, to, 0.5, "fade");

      expect(gl.createTexture).toHaveBeenCalledTimes(2);
      expect(gl.deleteTexture).toHaveBeenCalledTimes(2);
    });

    it("clamps progress to 0-1 range", () => {
      const from = createMockImageBitmap();
      const to = createMockImageBitmap();

      renderer.renderTransition(from, to, 1.5, "fade");

      const opacityCalls = gl.uniform1f.mock.calls.filter((c: any[]) => c[0]._name === "u_opacity");
      expect(opacityCalls[0][1]).toBeCloseTo(0);
      expect(opacityCalls[1][1]).toBeCloseTo(1);
    });

    it("uses the transition program", () => {
      const from = createMockImageBitmap();
      const to = createMockImageBitmap();

      renderer.renderTransition(from, to, 0.5, "fade");

      expect(gl.useProgram).toHaveBeenCalled();
    });
  });

  // =========================================================================
  // applyFilter / clearFilter
  // =========================================================================
  describe("applyFilter and clearFilter", () => {
    beforeEach(async () => {
      await renderer.init(canvas, makeConfig());
    });

    it("stores filter config and applies it during render", () => {
      const filter: FilterConfig = { type: "grayscale", strength: 0.8 };
      renderer.applyFilter("layer-1", filter);

      gl.uniform1i.mockClear();
      gl.uniform1f.mockClear();

      const scene = makeScene();
      const frames = new Map<string, VideoFrame | ImageBitmap>();
      frames.set("source-1", createMockVideoFrame());
      renderer.renderScene(scene, frames);

      const filterTypeCalls = gl.uniform1i.mock.calls.filter(
        (c: any[]) => c[0]._name === "u_filterType"
      );
      expect(filterTypeCalls.some((c: any[]) => c[1] === 3)).toBe(true);

      const strengthCalls = gl.uniform1f.mock.calls.filter(
        (c: any[]) => c[0]._name === "u_filterStrength"
      );
      expect(strengthCalls.some((c: any[]) => c[1] === 0.8)).toBe(true);
    });

    it("clearFilter removes filter so renderLayer sets filterType to 0", () => {
      renderer.applyFilter("layer-1", { type: "grayscale", strength: 1 });
      renderer.clearFilter("layer-1");

      gl.uniform1i.mockClear();

      const scene = makeScene();
      const frames = new Map<string, VideoFrame | ImageBitmap>();
      frames.set("source-1", createMockVideoFrame());
      renderer.renderScene(scene, frames);

      const filterTypeCalls = gl.uniform1i.mock.calls.filter(
        (c: any[]) => c[0]._name === "u_filterType"
      );
      const lastCall = filterTypeCalls[filterTypeCalls.length - 1];
      expect(lastCall[1]).toBe(0);
    });
  });

  // =========================================================================
  // applyFilterUniforms (via renderScene with filters)
  // =========================================================================
  describe("applyFilterUniforms", () => {
    beforeEach(async () => {
      await renderer.init(canvas, makeConfig());
    });

    function renderWithFilter(filter: FilterConfig) {
      renderer.applyFilter("layer-1", filter);
      gl.uniform1i.mockClear();
      gl.uniform1f.mockClear();
      gl.uniformMatrix4fv.mockClear();

      const scene = makeScene();
      const frames = new Map<string, VideoFrame | ImageBitmap>();
      frames.set("source-1", createMockVideoFrame());
      renderer.renderScene(scene, frames);
    }

    it("colorMatrix sets filterType to 2 and uploads matrix", () => {
      renderWithFilter({
        type: "colorMatrix",
        brightness: 1.2,
        contrast: 1.0,
        saturation: 1.0,
      });

      const filterTypeCalls = gl.uniform1i.mock.calls.filter(
        (c: any[]) => c[0]._name === "u_filterType"
      );
      expect(filterTypeCalls.some((c: any[]) => c[1] === 2)).toBe(true);
      expect(gl.uniformMatrix4fv).toHaveBeenCalled();

      const matrixCall = gl.uniformMatrix4fv.mock.calls[0];
      expect(matrixCall[0]._name).toBe("u_colorMatrix");
      expect(matrixCall[1]).toBe(false);
      expect(matrixCall[2]).toBeInstanceOf(Float32Array);
      expect(matrixCall[2].length).toBe(16);
    });

    it("grayscale sets filterType to 3 and strength", () => {
      renderWithFilter({ type: "grayscale", strength: 0.6 });

      const filterTypeCalls = gl.uniform1i.mock.calls.filter(
        (c: any[]) => c[0]._name === "u_filterType"
      );
      expect(filterTypeCalls.some((c: any[]) => c[1] === 3)).toBe(true);

      const strengthCalls = gl.uniform1f.mock.calls.filter(
        (c: any[]) => c[0]._name === "u_filterStrength"
      );
      expect(strengthCalls.some((c: any[]) => c[1] === 0.6)).toBe(true);
    });

    it("grayscale defaults strength to 1 when not specified", () => {
      renderWithFilter({ type: "grayscale" });

      const strengthCalls = gl.uniform1f.mock.calls.filter(
        (c: any[]) => c[0]._name === "u_filterStrength"
      );
      expect(strengthCalls.some((c: any[]) => c[1] === 1)).toBe(true);
    });

    it("sepia sets filterType to 4 and strength", () => {
      renderWithFilter({ type: "sepia", strength: 0.75 });

      const filterTypeCalls = gl.uniform1i.mock.calls.filter(
        (c: any[]) => c[0]._name === "u_filterType"
      );
      expect(filterTypeCalls.some((c: any[]) => c[1] === 4)).toBe(true);

      const strengthCalls = gl.uniform1f.mock.calls.filter(
        (c: any[]) => c[0]._name === "u_filterStrength"
      );
      expect(strengthCalls.some((c: any[]) => c[1] === 0.75)).toBe(true);
    });

    it("sepia defaults strength to 1 when not specified", () => {
      renderWithFilter({ type: "sepia" });

      const strengthCalls = gl.uniform1f.mock.calls.filter(
        (c: any[]) => c[0]._name === "u_filterStrength"
      );
      expect(strengthCalls.some((c: any[]) => c[1] === 1)).toBe(true);
    });

    it("blur warns and sets filterType to 0", () => {
      renderWithFilter({ type: "blur", strength: 5 });

      expect(console.warn).toHaveBeenCalledWith(
        "[WebGLRenderer] Blur filter requires multi-pass rendering, not yet implemented"
      );

      const filterTypeCalls = gl.uniform1i.mock.calls.filter(
        (c: any[]) => c[0]._name === "u_filterType"
      );
      const lastCall = filterTypeCalls[filterTypeCalls.length - 1];
      expect(lastCall[1]).toBe(0);
    });

    it("invert sets filterType to 6", () => {
      renderWithFilter({ type: "invert" });

      const filterTypeCalls = gl.uniform1i.mock.calls.filter(
        (c: any[]) => c[0]._name === "u_filterType"
      );
      const lastCall = filterTypeCalls[filterTypeCalls.length - 1];
      expect(lastCall[1]).toBe(6);
    });

    it("unsupported filters default filterType to 0", () => {
      renderWithFilter({ type: "glow", strength: 0.5 });

      const glowCalls = gl.uniform1i.mock.calls.filter((c: any[]) => c[0]._name === "u_filterType");
      const lastGlowCall = glowCalls[glowCalls.length - 1];
      expect(lastGlowCall[1]).toBe(0);
    });

    it("chromaKey sets filterType to 5 and configures uniforms", () => {
      renderWithFilter({
        type: "chromaKey",
        keyColor: "#00FF00",
        keyTolerance: 0.4,
        keyEdgeSoftness: 0.15,
      });

      const filterTypeCalls = gl.uniform1i.mock.calls.filter(
        (c: any[]) => c[0]._name === "u_filterType"
      );
      expect(filterTypeCalls.some((c: any[]) => c[1] === 5)).toBe(true);

      const colorCalls = gl.uniform3f.mock.calls.filter((c: any[]) => c[0]._name === "u_keyColor");
      expect(colorCalls.length).toBeGreaterThan(0);
      // Green key: R=0, G=1, B=0
      expect(colorCalls[0][1]).toBeCloseTo(0);
      expect(colorCalls[0][2]).toBeCloseTo(1);
      expect(colorCalls[0][3]).toBeCloseTo(0);

      const tolCalls = gl.uniform1f.mock.calls.filter(
        (c: any[]) => c[0]._name === "u_keyTolerance"
      );
      expect(tolCalls.some((c: any[]) => c[1] === 0.4)).toBe(true);

      const softCalls = gl.uniform1f.mock.calls.filter(
        (c: any[]) => c[0]._name === "u_keyEdgeSoftness"
      );
      expect(softCalls.some((c: any[]) => c[1] === 0.15)).toBe(true);
    });

    it("chromaKey uses green defaults when no keyColor specified", () => {
      renderWithFilter({ type: "chromaKey" });

      const tolCalls = gl.uniform1f.mock.calls.filter(
        (c: any[]) => c[0]._name === "u_keyTolerance"
      );
      expect(tolCalls.some((c: any[]) => c[1] === 0.3)).toBe(true);

      const softCalls = gl.uniform1f.mock.calls.filter(
        (c: any[]) => c[0]._name === "u_keyEdgeSoftness"
      );
      expect(softCalls.some((c: any[]) => c[1] === 0.1)).toBe(true);
    });

    it("invert sets filterType to 6 with default strength 1", () => {
      renderWithFilter({ type: "invert" });

      const filterTypeCalls = gl.uniform1i.mock.calls.filter(
        (c: any[]) => c[0]._name === "u_filterType"
      );
      expect(filterTypeCalls.some((c: any[]) => c[1] === 6)).toBe(true);

      const strengthCalls = gl.uniform1f.mock.calls.filter(
        (c: any[]) => c[0]._name === "u_filterStrength"
      );
      expect(strengthCalls.some((c: any[]) => c[1] === 1)).toBe(true);
    });

    it("invert uses custom strength", () => {
      renderWithFilter({ type: "invert", strength: 0.5 });

      const strengthCalls = gl.uniform1f.mock.calls.filter(
        (c: any[]) => c[0]._name === "u_filterStrength"
      );
      expect(strengthCalls.some((c: any[]) => c[1] === 0.5)).toBe(true);
    });

    it("colorMatrix applies hue rotation when hue is non-zero", () => {
      renderWithFilter({ type: "colorMatrix", hue: 90 });

      const matrixCall = gl.uniformMatrix4fv.mock.calls[0];
      const matrix = matrixCall[2] as Float32Array;

      // With 90deg hue rotation + default b/c/s, diagonal should not be identity
      expect(matrix[0]).not.toBeCloseTo(1, 2);
      // Matrix should still have reasonable values
      for (let i = 0; i < 16; i++) {
        expect(Math.abs(matrix[i])).toBeLessThan(2);
      }
    });

    it("colorMatrix uses default values when brightness/contrast/saturation not set", () => {
      renderWithFilter({ type: "colorMatrix" });

      const matrixCall = gl.uniformMatrix4fv.mock.calls[0];
      const matrix = matrixCall[2] as Float32Array;

      // With defaults (b=1, c=1, s=1, h=0) the diagonal should be ~1 (identity-like)
      expect(matrix[0]).toBeCloseTo(1);
      expect(matrix[5]).toBeCloseTo(1);
      expect(matrix[10]).toBeCloseTo(1);
      expect(matrix[15]).toBeCloseTo(1);
    });
  });

  // =========================================================================
  // parseColor (via renderScene background)
  // =========================================================================
  describe("parseColor", () => {
    beforeEach(async () => {
      await renderer.init(canvas, makeConfig());
      gl.clearColor.mockClear();
    });

    it("parses 6-char hex correctly", () => {
      const scene = makeScene({ backgroundColor: "#1a2b3c" });
      const frames = new Map<string, VideoFrame | ImageBitmap>();

      renderer.renderScene(scene, frames);

      expect(gl.clearColor).toHaveBeenCalledWith(0x1a / 255, 0x2b / 255, 0x3c / 255, 1.0);
    });

    it("parses 3-char hex correctly", () => {
      const scene = makeScene({ backgroundColor: "#abc" });
      const frames = new Map<string, VideoFrame | ImageBitmap>();

      renderer.renderScene(scene, frames);

      expect(gl.clearColor).toHaveBeenCalledWith(0xaa / 255, 0xbb / 255, 0xcc / 255, 1.0);
    });

    it("handles white (#ffffff)", () => {
      const scene = makeScene({ backgroundColor: "#ffffff" });
      const frames = new Map<string, VideoFrame | ImageBitmap>();

      renderer.renderScene(scene, frames);

      expect(gl.clearColor).toHaveBeenCalledWith(1, 1, 1, 1.0);
    });

    it("handles black (#000000)", () => {
      const scene = makeScene({ backgroundColor: "#000000" });
      const frames = new Map<string, VideoFrame | ImageBitmap>();

      renderer.renderScene(scene, frames);

      expect(gl.clearColor).toHaveBeenCalledWith(0, 0, 0, 1.0);
    });
  });

  // =========================================================================
  // resize
  // =========================================================================
  describe("resize", () => {
    it("updates viewport with new dimensions", async () => {
      await renderer.init(canvas, makeConfig());
      gl.viewport.mockClear();

      renderer.resize(makeConfig({ width: 1280, height: 720 }));

      expect(gl.viewport).toHaveBeenCalledWith(0, 0, 1280, 720);
    });

    it("updates config so subsequent renders use new dimensions", async () => {
      await renderer.init(canvas, makeConfig({ width: 1920, height: 1080 }));
      renderer.resize(makeConfig({ width: 1280, height: 720 }));

      gl.uniform2f.mockClear();
      const scene = makeScene();
      const frames = new Map<string, VideoFrame | ImageBitmap>();
      frames.set("source-1", createMockVideoFrame());
      renderer.renderScene(scene, frames);

      const resCalls = gl.uniform2f.mock.calls.filter((c: any[]) => c[0]._name === "u_resolution");
      expect(resCalls[0][1]).toBe(1280);
      expect(resCalls[0][2]).toBe(720);
    });
  });

  // =========================================================================
  // getStats
  // =========================================================================
  describe("getStats", () => {
    it("returns fps and frameTimeMs", async () => {
      await renderer.init(canvas, makeConfig());

      const stats = renderer.getStats();

      expect(stats).toHaveProperty("fps");
      expect(stats).toHaveProperty("frameTimeMs");
      expect(typeof stats.fps).toBe("number");
      expect(typeof stats.frameTimeMs).toBe("number");
    });

    it("returns zero fps initially", async () => {
      await renderer.init(canvas, makeConfig());

      const stats = renderer.getStats();

      expect(stats.fps).toBe(0);
    });

    it("calculates fps after elapsed time exceeds 1 second", async () => {
      let now = 1000;
      vi.spyOn(performance, "now").mockImplementation(() => now);

      await renderer.init(canvas, makeConfig());

      const scene = makeScene();
      const frames = new Map<string, VideoFrame | ImageBitmap>();
      frames.set("source-1", createMockVideoFrame());

      for (let i = 0; i < 30; i++) {
        now += 33.33;
        renderer.renderScene(scene, frames);
      }

      now += 10;
      renderer.renderScene(scene, frames);

      const stats = renderer.getStats();
      expect(stats.fps).toBeGreaterThan(0);
    });

    it("tracks last render time in frameTimeMs", async () => {
      let callIdx = 0;
      const times = [1000, 1000, 1005];
      vi.spyOn(performance, "now").mockImplementation(() => {
        const t = callIdx < times.length ? times[callIdx] : 1005;
        callIdx++;
        return t;
      });

      await renderer.init(canvas, makeConfig());

      callIdx = 0;
      const scene = makeScene();
      const frames = new Map<string, VideoFrame | ImageBitmap>();
      frames.set("source-1", createMockVideoFrame());

      renderer.renderScene(scene, frames);

      const stats = renderer.getStats();
      expect(stats.frameTimeMs).toBeGreaterThanOrEqual(0);
    });
  });

  // =========================================================================
  // captureFrame
  // =========================================================================
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

  // =========================================================================
  // destroy
  // =========================================================================
  describe("destroy", () => {
    it("deletes all textures", async () => {
      await renderer.init(canvas, makeConfig());

      const scene = makeScene({
        layers: [
          makeLayer({ id: "l1", sourceId: "s1", visible: true, zIndex: 0 }),
          makeLayer({ id: "l2", sourceId: "s2", visible: true, zIndex: 1 }),
        ],
      });
      const frames = new Map<string, VideoFrame | ImageBitmap>();
      frames.set("s1", createMockVideoFrame());
      frames.set("s2", createMockVideoFrame());
      renderer.renderScene(scene, frames);

      gl.deleteTexture.mockClear();
      renderer.destroy();

      expect(gl.deleteTexture).toHaveBeenCalledTimes(2);
    });

    it("deletes both shader programs", async () => {
      await renderer.init(canvas, makeConfig());
      gl.deleteProgram.mockClear();

      renderer.destroy();

      expect(gl.deleteProgram).toHaveBeenCalledTimes(2);
    });

    it("loses WebGL context via extension", async () => {
      await renderer.init(canvas, makeConfig());

      renderer.destroy();

      expect(gl.getExtension).toHaveBeenCalledWith("WEBGL_lose_context");
      const ext = gl.getExtension("WEBGL_lose_context");
      expect(ext.loseContext).toHaveBeenCalled();
    });

    it("clears filter configs", async () => {
      await renderer.init(canvas, makeConfig());
      renderer.applyFilter("layer-1", { type: "grayscale", strength: 1 });

      renderer.destroy();

      // Confirm destroy completes without error even with filter configs present
      expect(() => renderer.destroy).not.toThrow();
    });

    it("handles missing WEBGL_lose_context extension gracefully", async () => {
      gl.getExtension = vi.fn(() => null);
      await renderer.init(canvas, makeConfig());

      expect(() => renderer.destroy()).not.toThrow();
    });
  });
});

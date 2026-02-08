import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";

// WebGPU constants must exist before import
(globalThis as any).GPUShaderStage = { VERTEX: 1, FRAGMENT: 2 };
(globalThis as any).GPUBufferUsage = {
  VERTEX: 0x0020,
  INDEX: 0x0010,
  UNIFORM: 0x0040,
  COPY_DST: 0x0008,
};
(globalThis as any).GPUTextureUsage = {
  TEXTURE_BINDING: 0x04,
  COPY_DST: 0x02,
  RENDER_ATTACHMENT: 0x10,
};

// VideoFrame constructor mock
(globalThis as any).VideoFrame = vi.fn(function (this: any, source: any, opts: any) {
  return {
    source,
    timestamp: opts.timestamp,
    close: vi.fn(),
    displayWidth: 1920,
    displayHeight: 1080,
  };
});

// ImageBitmap stub for instanceof checks
(globalThis as any).ImageBitmap = class ImageBitmap {
  width: number;
  height: number;
  constructor(w = 1920, h = 1080) {
    this.width = w;
    this.height = h;
  }
};

// ---------- Mock helpers ----------

const mockRenderPassEncoder = {
  setPipeline: vi.fn(),
  setVertexBuffer: vi.fn(),
  setIndexBuffer: vi.fn(),
  setBindGroup: vi.fn(),
  drawIndexed: vi.fn(),
  end: vi.fn(),
};

const mockCommandEncoder = {
  beginRenderPass: vi.fn(() => mockRenderPassEncoder),
  finish: vi.fn(() => ({})),
};

function createMockDevice() {
  return {
    createShaderModule: vi.fn(() => ({})),
    createBindGroupLayout: vi.fn(() => ({})),
    createPipelineLayout: vi.fn(() => ({})),
    createRenderPipeline: vi.fn(() => ({
      getBindGroupLayout: vi.fn(() => ({})),
    })),
    createBuffer: vi.fn(() => ({ destroy: vi.fn() })),
    createTexture: vi.fn(() => ({
      width: 1920,
      height: 1080,
      createView: vi.fn(() => ({})),
      destroy: vi.fn(),
    })),
    createSampler: vi.fn(() => ({})),
    createBindGroup: vi.fn(() => ({})),
    createCommandEncoder: vi.fn(() => mockCommandEncoder),
    queue: {
      writeBuffer: vi.fn(),
      copyExternalImageToTexture: vi.fn(),
      submit: vi.fn(),
      onSubmittedWorkDone: vi.fn(() => Promise.resolve()),
    },
  };
}

function createMockCanvas() {
  const mockContext = {
    configure: vi.fn(),
    getCurrentTexture: vi.fn(() => ({
      createView: vi.fn(() => ({})),
    })),
  };
  return {
    width: 1920,
    height: 1080,
    getContext: vi.fn((_type: string) => mockContext),
    _ctx: mockContext,
  } as unknown as OffscreenCanvas & { _ctx: any };
}

function createMockAdapter() {
  return {
    requestDevice: vi.fn(),
  };
}

function makeLayer(overrides: Record<string, any> = {}) {
  return {
    id: "layer-1",
    sourceId: "src-1",
    visible: true,
    locked: false,
    zIndex: 0,
    scalingMode: "stretch" as const,
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

function makeScene(layers = [makeLayer()]) {
  return {
    id: "scene-1",
    name: "Test Scene",
    layers,
    backgroundColor: "#000000",
  };
}

function makeConfig() {
  return {
    enabled: true,
    width: 1920,
    height: 1080,
    frameRate: 30,
    renderer: "webgpu" as const,
    defaultTransition: {
      type: "fade" as const,
      durationMs: 500,
      easing: "ease-in-out" as const,
    },
  };
}

// ---------- Shared state ----------

const origNavigator = Object.getOwnPropertyDescriptor(globalThis, "navigator");
let mockDevice: ReturnType<typeof createMockDevice>;
let mockAdapter: ReturnType<typeof createMockAdapter>;

function installNavigatorGpu(adapter: ReturnType<typeof createMockAdapter>) {
  Object.defineProperty(globalThis, "navigator", {
    value: {
      gpu: {
        requestAdapter: vi.fn(() => Promise.resolve(adapter)),
        getPreferredCanvasFormat: vi.fn(() => "bgra8unorm"),
      },
    },
    writable: true,
    configurable: true,
  });
}

// Install before first import so checkWebGPUSupport() sees navigator.gpu
mockAdapter = createMockAdapter();
mockDevice = createMockDevice();
mockAdapter.requestDevice.mockResolvedValue(mockDevice);
installNavigatorGpu(mockAdapter);

// Now import (isSupported evaluates at class body parse time)
import { WebGPURenderer } from "../src/core/renderers/WebGPURenderer";

// ---------- Tests ----------

describe("WebGPURenderer", () => {
  beforeEach(() => {
    vi.restoreAllMocks();

    mockRenderPassEncoder.setPipeline.mockClear();
    mockRenderPassEncoder.setVertexBuffer.mockClear();
    mockRenderPassEncoder.setIndexBuffer.mockClear();
    mockRenderPassEncoder.setBindGroup.mockClear();
    mockRenderPassEncoder.drawIndexed.mockClear();
    mockRenderPassEncoder.end.mockClear();
    mockCommandEncoder.beginRenderPass.mockClear();
    mockCommandEncoder.finish.mockClear();

    mockDevice = createMockDevice();
    mockAdapter = createMockAdapter();
    mockAdapter.requestDevice.mockResolvedValue(mockDevice);
    installNavigatorGpu(mockAdapter);

    vi.spyOn(performance, "now").mockReturnValue(1000);
  });

  afterEach(() => {
    if (origNavigator) {
      Object.defineProperty(globalThis, "navigator", origNavigator);
    } else {
      delete (globalThis as any).navigator;
    }
  });

  async function initRenderer() {
    const renderer = new WebGPURenderer();
    const canvas = createMockCanvas();
    await renderer.init(canvas, makeConfig());
    return { renderer, canvas, device: mockDevice };
  }

  // ========================================================================
  // isSupported
  // ========================================================================

  describe("isSupported", () => {
    it("returns true when navigator.gpu exists", () => {
      const renderer = new WebGPURenderer();
      expect(renderer.isSupported).toBe(true);
    });

    it("has type 'webgpu'", () => {
      const renderer = new WebGPURenderer();
      expect(renderer.type).toBe("webgpu");
    });
  });

  // ========================================================================
  // init
  // ========================================================================

  describe("init", () => {
    it("requests adapter with high-performance preference", async () => {
      await initRenderer();
      expect(navigator.gpu.requestAdapter).toHaveBeenCalledWith({
        powerPreference: "high-performance",
      });
    });

    it("throws when adapter is null", async () => {
      (navigator.gpu.requestAdapter as ReturnType<typeof vi.fn>).mockResolvedValue(null);
      const renderer = new WebGPURenderer();
      await expect(renderer.init(createMockCanvas(), makeConfig())).rejects.toThrow(
        "Failed to get WebGPU adapter"
      );
    });

    it("requests device from adapter", async () => {
      await initRenderer();
      expect(mockAdapter.requestDevice).toHaveBeenCalled();
    });

    it("configures canvas context with format and alphaMode", async () => {
      const { canvas } = await initRenderer();
      const ctx = canvas._ctx;
      expect(canvas.getContext).toHaveBeenCalledWith("webgpu");
      expect(ctx.configure).toHaveBeenCalledWith(
        expect.objectContaining({
          format: "bgra8unorm",
          alphaMode: "premultiplied",
        })
      );
    });

    it("creates render pipeline", async () => {
      await initRenderer();
      expect(mockDevice.createRenderPipeline).toHaveBeenCalled();
    });

    it("creates vertex and index geometry buffers", async () => {
      await initRenderer();
      const bufferCalls = mockDevice.createBuffer.mock.calls;
      expect(bufferCalls.length).toBeGreaterThanOrEqual(2);

      const vertexUsage = GPUBufferUsage.VERTEX | GPUBufferUsage.COPY_DST;
      expect(bufferCalls[0][0].usage).toBe(vertexUsage);

      const indexUsage = GPUBufferUsage.INDEX | GPUBufferUsage.COPY_DST;
      expect(bufferCalls[1][0].usage).toBe(indexUsage);
    });

    it("creates sampler with linear filtering", async () => {
      await initRenderer();
      expect(mockDevice.createSampler).toHaveBeenCalledWith(
        expect.objectContaining({
          magFilter: "linear",
          minFilter: "linear",
          mipmapFilter: "linear",
        })
      );
    });

    it("throws when WebGPU is not supported", async () => {
      const renderer = new WebGPURenderer();
      Object.defineProperty(renderer, "isSupported", { value: false });
      await expect(renderer.init(createMockCanvas(), makeConfig())).rejects.toThrow(
        "WebGPU is not supported"
      );
    });

    it("throws when canvas context returns null", async () => {
      const canvas = createMockCanvas();
      (canvas as any).getContext = vi.fn(() => null);
      const renderer = new WebGPURenderer();
      await expect(renderer.init(canvas, makeConfig())).rejects.toThrow(
        "Failed to get WebGPU context"
      );
    });
  });

  // ========================================================================
  // renderScene
  // ========================================================================

  describe("renderScene", () => {
    it("returns early when device is null (not initialised)", () => {
      const renderer = new WebGPURenderer();
      renderer.renderScene(makeScene(), new Map());
    });

    it("updates textures from frames", async () => {
      const { renderer, device } = await initRenderer();
      const frame = new (globalThis as any).ImageBitmap(1920, 1080);
      const frames = new Map<string, any>([["src-1", frame]]);

      renderer.renderScene(makeScene(), frames);

      expect(device.createTexture).toHaveBeenCalled();
      expect(device.queue.copyExternalImageToTexture).toHaveBeenCalledWith(
        { source: frame },
        expect.objectContaining({ texture: expect.anything() }),
        { width: 1920, height: 1080 }
      );
    });

    it("creates command encoder and render pass with clear color", async () => {
      const { renderer, device } = await initRenderer();
      const frame = new (globalThis as any).ImageBitmap(1920, 1080);
      const scene = makeScene();
      scene.backgroundColor = "#ff0000";

      renderer.renderScene(scene, new Map([["src-1", frame]]));

      expect(device.createCommandEncoder).toHaveBeenCalled();
      expect(mockCommandEncoder.beginRenderPass).toHaveBeenCalledWith(
        expect.objectContaining({
          colorAttachments: expect.arrayContaining([
            expect.objectContaining({
              clearValue: { r: 1, g: 0, b: 0, a: 1 },
              loadOp: "clear",
              storeOp: "store",
            }),
          ]),
        })
      );
    });

    it("sets pipeline and vertex/index buffers on render pass", async () => {
      const { renderer } = await initRenderer();
      const frame = new (globalThis as any).ImageBitmap();
      renderer.renderScene(makeScene(), new Map([["src-1", frame]]));

      expect(mockRenderPassEncoder.setPipeline).toHaveBeenCalled();
      expect(mockRenderPassEncoder.setVertexBuffer).toHaveBeenCalledWith(0, expect.anything());
      expect(mockRenderPassEncoder.setIndexBuffer).toHaveBeenCalledWith(
        expect.anything(),
        "uint16"
      );
    });

    it("filters visible layers and sorts by zIndex", async () => {
      const { renderer } = await initRenderer();

      const hiddenLayer = makeLayer({ id: "hidden", visible: false, zIndex: 0, sourceId: "src-1" });
      const backLayer = makeLayer({ id: "back", zIndex: 1, sourceId: "src-1" });
      const frontLayer = makeLayer({ id: "front", zIndex: 5, sourceId: "src-1" });

      const scene = makeScene([frontLayer, hiddenLayer, backLayer]);
      const frame = new (globalThis as any).ImageBitmap();

      renderer.renderScene(scene, new Map([["src-1", frame]]));

      expect(mockRenderPassEncoder.drawIndexed).toHaveBeenCalledTimes(2);
      const bindGroupCalls = mockRenderPassEncoder.setBindGroup.mock.calls;
      expect(bindGroupCalls).toHaveLength(2);
    });

    it("creates bind groups for layers with textures", async () => {
      const { renderer, device } = await initRenderer();
      const frame = new (globalThis as any).ImageBitmap();

      renderer.renderScene(makeScene(), new Map([["src-1", frame]]));

      expect(device.createBindGroup).toHaveBeenCalled();
    });

    it("calls drawIndexed(6) per visible layer", async () => {
      const { renderer } = await initRenderer();
      const frame = new (globalThis as any).ImageBitmap();

      const scene = makeScene([
        makeLayer({ id: "l1", sourceId: "src-1", zIndex: 0 }),
        makeLayer({ id: "l2", sourceId: "src-1", zIndex: 1 }),
      ]);

      renderer.renderScene(scene, new Map([["src-1", frame]]));

      expect(mockRenderPassEncoder.drawIndexed).toHaveBeenCalledTimes(2);
      expect(mockRenderPassEncoder.drawIndexed).toHaveBeenCalledWith(6);
    });

    it("submits command buffer", async () => {
      const { renderer, device } = await initRenderer();
      const frame = new (globalThis as any).ImageBitmap();

      renderer.renderScene(makeScene(), new Map([["src-1", frame]]));

      expect(device.queue.submit).toHaveBeenCalledWith([expect.anything()]);
      expect(mockRenderPassEncoder.end).toHaveBeenCalled();
    });

    it("skips layers without a matching texture", async () => {
      const { renderer } = await initRenderer();

      renderer.renderScene(makeScene(), new Map());

      expect(mockRenderPassEncoder.drawIndexed).not.toHaveBeenCalled();
    });
  });

  // ========================================================================
  // updateUniforms (exercised via renderScene)
  // ========================================================================

  describe("updateUniforms", () => {
    async function renderWithLayer(
      layerOverrides: Record<string, any>,
      textureSize?: { w: number; h: number }
    ) {
      const device = createMockDevice();
      const adapter = createMockAdapter();
      adapter.requestDevice.mockResolvedValue(device);
      installNavigatorGpu(adapter);

      if (textureSize) {
        device.createTexture.mockReturnValue({
          width: textureSize.w,
          height: textureSize.h,
          createView: vi.fn(() => ({})),
          destroy: vi.fn(),
        });
      }

      const renderer = new WebGPURenderer();
      const canvas = createMockCanvas();
      await renderer.init(canvas, makeConfig());

      const layer = makeLayer(layerOverrides);
      const scene = makeScene([layer]);

      const frame = textureSize
        ? {
            width: textureSize.w,
            height: textureSize.h,
            displayWidth: textureSize.w,
            displayHeight: textureSize.h,
          }
        : new (globalThis as any).ImageBitmap();
      const frames = new Map<string, any>([["src-1", frame]]);

      renderer.renderScene(scene, frames);

      const uniformWrite = device.queue.writeBuffer.mock.calls.find(
        (call: any[]) => call[2] instanceof ArrayBuffer && call[2].byteLength === 96
      );

      return {
        uniformWrite,
        device,
        floats: uniformWrite ? new Float32Array(uniformWrite[2] as ArrayBuffer) : null,
      };
    }

    it("writes transform matrix to uniform buffer", async () => {
      const { floats } = await renderWithLayer({});
      expect(floats).not.toBeNull();
      expect(floats![0]).toBeCloseTo(1);
      expect(floats![5]).toBeCloseTo(1);
      expect(floats![12]).toBeCloseTo(0);
      expect(floats![13]).toBeCloseTo(0);
    });

    it("sets opacity in uniform buffer", async () => {
      const { floats } = await renderWithLayer({
        transform: {
          x: 0,
          y: 0,
          width: 1,
          height: 1,
          opacity: 0.5,
          rotation: 0,
          borderRadius: 0,
          crop: { left: 0, top: 0, right: 0, bottom: 0 },
        },
      });
      expect(floats).not.toBeNull();
      expect(floats![16]).toBeCloseTo(0.5);
    });

    it("handles stretch scaling (no position/size change)", async () => {
      const { floats } = await renderWithLayer(
        {
          scalingMode: "stretch",
          transform: {
            x: 0.1,
            y: 0.1,
            width: 0.8,
            height: 0.8,
            opacity: 1,
            rotation: 0,
            borderRadius: 0,
            crop: { left: 0, top: 0, right: 0, bottom: 0 },
          },
        },
        { w: 640, h: 480 }
      );
      expect(floats).not.toBeNull();
      expect(floats![0]).toBeCloseTo(0.8);
      expect(floats![5]).toBeCloseTo(0.8);
      expect(floats![20]).toBeCloseTo(0);
      expect(floats![21]).toBeCloseTo(0);
      expect(floats![22]).toBeCloseTo(0);
      expect(floats![23]).toBeCloseTo(0);
    });

    it("handles letterbox scaling for wider source (fit to width)", async () => {
      const { floats } = await renderWithLayer(
        {
          scalingMode: "letterbox",
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
        },
        { w: 2560, h: 1080 }
      );
      expect(floats).not.toBeNull();
      expect(floats![0]).toBeCloseTo(1);
      expect(floats![5]).toBeCloseTo(0.75);
    });

    it("handles letterbox scaling for taller source (fit to height)", async () => {
      const { floats } = await renderWithLayer(
        {
          scalingMode: "letterbox",
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
        },
        { w: 1080, h: 1920 }
      );
      expect(floats).not.toBeNull();
      expect(floats![5]).toBeCloseTo(1);
      expect(floats![0]).toBeCloseTo(0.31640625);
    });

    it("handles crop scaling for wider source (crop sides via UV)", async () => {
      const { floats } = await renderWithLayer(
        {
          scalingMode: "crop",
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
        },
        { w: 2560, h: 1080 }
      );
      expect(floats).not.toBeNull();
      expect(floats![0]).toBeCloseTo(1);
      expect(floats![5]).toBeCloseTo(1);
      expect(floats![20]).toBeCloseTo(0.125);
      expect(floats![22]).toBeCloseTo(0.125);
      expect(floats![21]).toBeCloseTo(0);
      expect(floats![23]).toBeCloseTo(0);
    });

    it("handles crop scaling for taller source (crop top/bottom via UV)", async () => {
      const { floats } = await renderWithLayer(
        {
          scalingMode: "crop",
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
        },
        { w: 1080, h: 1920 }
      );
      expect(floats).not.toBeNull();
      expect(floats![20]).toBeCloseTo(0);
      expect(floats![22]).toBeCloseTo(0);
      expect(floats![21]).toBeCloseTo(0.3417, 3);
      expect(floats![23]).toBeCloseTo(0.3417, 3);
    });

    it("applies rotation to transform matrix", async () => {
      const { floats } = await renderWithLayer({
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
      expect(floats).not.toBeNull();
      expect(floats![0]).toBeCloseTo(0);
      expect(floats![1]).toBeCloseTo(1);
      expect(floats![4]).toBeCloseTo(-1);
      expect(floats![5]).toBeCloseTo(0);
    });

    it("writes crop values from layer transform", async () => {
      const { floats } = await renderWithLayer({
        scalingMode: "stretch",
        transform: {
          x: 0,
          y: 0,
          width: 1,
          height: 1,
          opacity: 1,
          rotation: 0,
          borderRadius: 0,
          crop: { left: 0.1, top: 0.2, right: 0.1, bottom: 0.2 },
        },
      });
      expect(floats).not.toBeNull();
      expect(floats![20]).toBeCloseTo(0.1);
      expect(floats![21]).toBeCloseTo(0.2);
      expect(floats![22]).toBeCloseTo(0.1);
      expect(floats![23]).toBeCloseTo(0.2);
    });
  });

  // ========================================================================
  // renderTransition
  // ========================================================================

  describe("renderTransition", () => {
    function createImageBitmap(w = 1920, h = 1080) {
      return new (globalThis as any).ImageBitmap(w, h) as ImageBitmap;
    }

    it("creates temp texture and copies image", async () => {
      const { renderer, device } = await initRenderer();
      const from = createImageBitmap();
      const to = createImageBitmap();

      renderer.renderTransition(from, to, 0.5, "fade");

      expect(device.createTexture).toHaveBeenCalled();
      expect(device.queue.copyExternalImageToTexture).toHaveBeenCalled();
    });

    it("copies target image on cut transition", async () => {
      const { renderer, device } = await initRenderer();
      const from = createImageBitmap();
      const to = createImageBitmap();

      renderer.renderTransition(from, to, 0.5, "cut");

      const copyCall = device.queue.copyExternalImageToTexture.mock.calls.find(
        (call: any[]) => call[0].source === to
      );
      expect(copyCall).toBeDefined();
    });

    it("copies target when progress >= 1", async () => {
      const { renderer, device } = await initRenderer();
      const from = createImageBitmap();
      const to = createImageBitmap();

      renderer.renderTransition(from, to, 1.0, "fade");

      const copyCall = device.queue.copyExternalImageToTexture.mock.calls.find(
        (call: any[]) => call[0].source === to
      );
      expect(copyCall).toBeDefined();
    });

    it("copies source when progress <= 0", async () => {
      const { renderer, device } = await initRenderer();
      const from = createImageBitmap();
      const to = createImageBitmap();

      renderer.renderTransition(from, to, 0.0, "fade");

      const copyCall = device.queue.copyExternalImageToTexture.mock.calls.find(
        (call: any[]) => call[0].source === from
      );
      expect(copyCall).toBeDefined();
    });

    it("copies source when progress < 0.5 for mid-transition", async () => {
      const { renderer, device } = await initRenderer();
      const from = createImageBitmap();
      const to = createImageBitmap();

      renderer.renderTransition(from, to, 0.3, "fade");

      const copyCall = device.queue.copyExternalImageToTexture.mock.calls.find(
        (call: any[]) => call[0].source === from
      );
      expect(copyCall).toBeDefined();
    });

    it("copies target when progress > 0.5 for mid-transition", async () => {
      const { renderer, device } = await initRenderer();
      const from = createImageBitmap();
      const to = createImageBitmap();

      renderer.renderTransition(from, to, 0.7, "fade");

      const copyCall = device.queue.copyExternalImageToTexture.mock.calls.find(
        (call: any[]) => call[0].source === to
      );
      expect(copyCall).toBeDefined();
    });

    it("returns early when device is null", () => {
      const renderer = new WebGPURenderer();
      const from = createImageBitmap();
      const to = createImageBitmap();
      renderer.renderTransition(from, to, 0.5, "fade");
    });

    it("submits command buffer after transition", async () => {
      const { renderer, device } = await initRenderer();
      const from = createImageBitmap();
      const to = createImageBitmap();

      renderer.renderTransition(from, to, 0.5, "fade");

      expect(device.queue.submit).toHaveBeenCalled();
    });
  });

  // ========================================================================
  // resize
  // ========================================================================

  describe("resize", () => {
    it("reconfigures context with updated config", async () => {
      const { renderer, canvas } = await initRenderer();
      const ctx = canvas._ctx;

      ctx.configure.mockClear();
      const newConfig = makeConfig();
      newConfig.width = 3840;
      newConfig.height = 2160;

      renderer.resize(newConfig);

      expect(ctx.configure).toHaveBeenCalledWith(
        expect.objectContaining({
          format: "bgra8unorm",
          alphaMode: "premultiplied",
        })
      );
    });

    it("does nothing when context is null", () => {
      const renderer = new WebGPURenderer();
      renderer.resize(makeConfig());
    });
  });

  // ========================================================================
  // captureFrame
  // ========================================================================

  describe("captureFrame", () => {
    it("creates a VideoFrame from the canvas", async () => {
      const { renderer } = await initRenderer();
      (performance.now as ReturnType<typeof vi.fn>).mockReturnValue(5000);

      const frame = renderer.captureFrame();

      expect((globalThis as any).VideoFrame).toHaveBeenCalledWith(expect.anything(), {
        timestamp: 5000 * 1000,
      });
      expect(frame).toBeDefined();
      expect(frame.timestamp).toBe(5000000);
    });
  });

  // ========================================================================
  // getStats
  // ========================================================================

  describe("getStats", () => {
    it("returns fps and frameTimeMs", async () => {
      const { renderer } = await initRenderer();
      const stats = renderer.getStats();

      expect(stats).toEqual(
        expect.objectContaining({
          fps: expect.any(Number),
          frameTimeMs: expect.any(Number),
        })
      );
    });

    it("updates fps after a second of rendering", async () => {
      const { renderer } = await initRenderer();
      const frame = new (globalThis as any).ImageBitmap();
      const scene = makeScene();

      (performance.now as ReturnType<typeof vi.fn>).mockReturnValue(1000);
      renderer.renderScene(scene, new Map([["src-1", frame]]));

      (performance.now as ReturnType<typeof vi.fn>).mockReturnValue(1100);
      renderer.renderScene(scene, new Map([["src-1", frame]]));

      (performance.now as ReturnType<typeof vi.fn>).mockReturnValue(1200);
      renderer.renderScene(scene, new Map([["src-1", frame]]));

      (performance.now as ReturnType<typeof vi.fn>).mockReturnValue(2100);
      renderer.renderScene(scene, new Map([["src-1", frame]]));

      const stats = renderer.getStats();
      expect(stats.fps).toBeGreaterThan(0);
    });

    it("reports frame render time", async () => {
      const { renderer } = await initRenderer();
      const frame = new (globalThis as any).ImageBitmap();
      const scene = makeScene();

      let callCount = 0;
      (performance.now as ReturnType<typeof vi.fn>).mockImplementation(() => {
        callCount++;
        return 1000 + (callCount % 2 === 0 ? 2 : 0);
      });

      renderer.renderScene(scene, new Map([["src-1", frame]]));

      const stats = renderer.getStats();
      expect(stats.frameTimeMs).toBeDefined();
    });
  });

  // ========================================================================
  // applyFilter
  // ========================================================================

  describe("applyFilter", () => {
    it("logs a console.warn about unimplemented filters", async () => {
      const { renderer } = await initRenderer();
      const warnSpy = vi.spyOn(console, "warn").mockImplementation(() => {});

      renderer.applyFilter("layer-1", { type: "blur", strength: 5 });

      expect(warnSpy).toHaveBeenCalledWith(
        expect.stringContaining("Filter effects not yet implemented")
      );
    });
  });

  // ========================================================================
  // destroy
  // ========================================================================

  describe("destroy", () => {
    it("destroys all textures", async () => {
      const { renderer, device } = await initRenderer();
      const frame = new (globalThis as any).ImageBitmap();

      renderer.renderScene(makeScene(), new Map([["src-1", frame]]));

      const textureMock =
        device.createTexture.mock.results[device.createTexture.mock.results.length - 1].value;

      renderer.destroy();

      expect(textureMock.destroy).toHaveBeenCalled();
    });

    it("destroys uniform buffers", async () => {
      const { renderer, device } = await initRenderer();
      const frame = new (globalThis as any).ImageBitmap();

      renderer.renderScene(makeScene(), new Map([["src-1", frame]]));

      const bufferResults = device.createBuffer.mock.results;
      const uniformBuf = bufferResults[bufferResults.length - 1].value;

      renderer.destroy();

      expect(uniformBuf.destroy).toHaveBeenCalled();
    });

    it("destroys vertex and index buffers", async () => {
      const { renderer, device } = await initRenderer();

      const vertexBuf = device.createBuffer.mock.results[0].value;
      const indexBuf = device.createBuffer.mock.results[1].value;

      renderer.destroy();

      expect(vertexBuf.destroy).toHaveBeenCalled();
      expect(indexBuf.destroy).toHaveBeenCalled();
    });

    it("nullifies device, context, pipeline, and sampler", async () => {
      const { renderer } = await initRenderer();

      renderer.destroy();

      const frame = new (globalThis as any).ImageBitmap();
      mockDevice.createCommandEncoder.mockClear();
      renderer.renderScene(makeScene(), new Map([["src-1", frame]]));
      expect(mockDevice.createCommandEncoder).not.toHaveBeenCalled();
    });

    it("clears bind groups and bind group texture maps", async () => {
      const { renderer } = await initRenderer();
      const frame = new (globalThis as any).ImageBitmap();

      renderer.renderScene(makeScene(), new Map([["src-1", frame]]));

      renderer.destroy();

      const freshDevice = createMockDevice();
      const freshAdapter = createMockAdapter();
      freshAdapter.requestDevice.mockResolvedValue(freshDevice);
      installNavigatorGpu(freshAdapter);

      const renderer2 = new WebGPURenderer();
      await renderer2.init(createMockCanvas(), makeConfig());
      renderer2.renderScene(makeScene(), new Map([["src-1", frame]]));

      expect(freshDevice.createBindGroup).toHaveBeenCalled();
    });
  });

  // ========================================================================
  // updateTextures (via renderScene)
  // ========================================================================

  describe("updateTextures", () => {
    it("creates a GPU texture from a VideoFrame", async () => {
      const { renderer, device } = await initRenderer();
      const videoFrame = {
        displayWidth: 1280,
        displayHeight: 720,
        close: vi.fn(),
      };

      device.createTexture.mockClear();
      renderer.renderScene(makeScene(), new Map([["src-1", videoFrame as any]]));

      const textureCalls = device.createTexture.mock.calls;
      const lastCall = textureCalls[textureCalls.length - 1];
      expect(lastCall[0].size).toEqual({ width: 1280, height: 720 });
    });

    it("recreates texture when size changes", async () => {
      const { renderer, device } = await initRenderer();

      const frame1 = new (globalThis as any).ImageBitmap(1920, 1080);
      renderer.renderScene(makeScene(), new Map([["src-1", frame1]]));

      const firstTextureCallCount = device.createTexture.mock.calls.length;

      const frame2 = {
        displayWidth: 1280,
        displayHeight: 720,
        close: vi.fn(),
      };
      renderer.renderScene(makeScene(), new Map([["src-1", frame2 as any]]));

      expect(device.createTexture.mock.calls.length).toBeGreaterThan(firstTextureCallCount);
    });

    it("copies ImageBitmap to texture via copyExternalImageToTexture", async () => {
      const { renderer, device } = await initRenderer();
      const bitmap = new (globalThis as any).ImageBitmap(1920, 1080);

      renderer.renderScene(makeScene(), new Map([["src-1", bitmap]]));

      expect(device.queue.copyExternalImageToTexture).toHaveBeenCalledWith(
        { source: bitmap },
        expect.objectContaining({ texture: expect.anything() }),
        { width: 1920, height: 1080 }
      );
    });
  });

  // ========================================================================
  // parseColor (exercised via renderScene background)
  // ========================================================================

  describe("parseColor", () => {
    it("parses hex color to GPU clear value", async () => {
      const { renderer } = await initRenderer();
      const frame = new (globalThis as any).ImageBitmap();

      const scene = makeScene();
      scene.backgroundColor = "#00ff80";

      renderer.renderScene(scene, new Map([["src-1", frame]]));

      expect(mockCommandEncoder.beginRenderPass).toHaveBeenCalledWith(
        expect.objectContaining({
          colorAttachments: expect.arrayContaining([
            expect.objectContaining({
              clearValue: {
                r: 0,
                g: 1,
                b: 128 / 255,
                a: 1,
              },
            }),
          ]),
        })
      );
    });

    it("defaults to black for invalid hex", async () => {
      const { renderer } = await initRenderer();
      const frame = new (globalThis as any).ImageBitmap();

      const scene = makeScene();
      scene.backgroundColor = "not-a-color";

      renderer.renderScene(scene, new Map([["src-1", frame]]));

      expect(mockCommandEncoder.beginRenderPass).toHaveBeenCalledWith(
        expect.objectContaining({
          colorAttachments: expect.arrayContaining([
            expect.objectContaining({
              clearValue: { r: 0, g: 0, b: 0, a: 1 },
            }),
          ]),
        })
      );
    });
  });
});

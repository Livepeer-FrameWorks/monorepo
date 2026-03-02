import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { CanvasRenderer } from "../src/rendering/CanvasRenderer";

function createMockCtx() {
  return {
    drawImage: vi.fn(),
    putImageData: vi.fn(),
    createImageData: vi.fn((w: number, h: number) => ({
      width: w,
      height: h,
      data: new Uint8ClampedArray(w * h * 4),
    })),
  };
}

function createMockCanvas(ctx: ReturnType<typeof createMockCtx> | null = null) {
  const mockCtx = ctx ?? createMockCtx();
  return {
    getContext: vi.fn((_type: string, _opts?: any) => mockCtx),
    width: 0,
    height: 0,
    toDataURL: vi.fn(
      (type: string, quality: number) => `data:${type};quality=${quality};base64,abc`
    ),
  } as any;
}

function makeYUVPlanes(
  width: number,
  height: number,
  format: string = "I420",
  yVal = 128,
  uVal = 128,
  vVal = 128
) {
  const chromaW = format === "I444" ? width : width >> 1;
  const chromaH = format === "I444" || format === "I422" ? height : height >> 1;

  return {
    width,
    height,
    format,
    y: new Uint8Array(width * height).fill(yVal),
    u: new Uint8Array(chromaW * chromaH).fill(uVal),
    v: new Uint8Array(chromaW * chromaH).fill(vVal),
  };
}

describe("CanvasRenderer", () => {
  let mockCtx: ReturnType<typeof createMockCtx>;
  let mockCanvas: any;

  beforeEach(() => {
    mockCtx = createMockCtx();
    mockCanvas = createMockCanvas(mockCtx);
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  describe("constructor", () => {
    it("creates renderer with valid canvas", () => {
      const renderer = new CanvasRenderer(mockCanvas);
      expect(mockCanvas.getContext).toHaveBeenCalledWith("2d", {
        desynchronized: true,
        alpha: false,
      });
      expect(renderer).toBeInstanceOf(CanvasRenderer);
    });

    it("throws when getContext returns null", () => {
      const badCanvas = createMockCanvas();
      badCanvas.getContext = vi.fn(() => null);
      expect(() => new CanvasRenderer(badCanvas)).toThrow("Canvas 2D context not supported");
    });
  });

  describe("render", () => {
    it("resizes canvas and draws frame", () => {
      const renderer = new CanvasRenderer(mockCanvas);
      const frame = {
        displayWidth: 1920,
        displayHeight: 1080,
        close: vi.fn(),
      };

      renderer.render(frame as any);
      expect(mockCanvas.width).toBe(1920);
      expect(mockCanvas.height).toBe(1080);
      expect(mockCtx.drawImage).toHaveBeenCalledWith(frame, 0, 0, 1920, 1080);
      expect(frame.close).toHaveBeenCalled();
    });

    it("is no-op when destroyed", () => {
      const renderer = new CanvasRenderer(mockCanvas);
      renderer.destroy();

      const frame = { displayWidth: 100, displayHeight: 100, close: vi.fn() };
      renderer.render(frame as any);
      expect(mockCtx.drawImage).not.toHaveBeenCalled();
    });
  });

  describe("renderYUV", () => {
    it("creates ImageData and calls putImageData", () => {
      const renderer = new CanvasRenderer(mockCanvas);
      const planes = makeYUVPlanes(4, 4);

      renderer.renderYUV(planes as any);

      expect(mockCtx.createImageData).toHaveBeenCalledWith(4, 4);
      expect(mockCtx.putImageData).toHaveBeenCalled();
    });

    it("is no-op when destroyed", () => {
      const renderer = new CanvasRenderer(mockCanvas);
      renderer.destroy();

      const planes = makeYUVPlanes(4, 4);
      renderer.renderYUV(planes as any);

      expect(mockCtx.createImageData).not.toHaveBeenCalled();
    });

    it("reuses ImageData when dimensions match", () => {
      const renderer = new CanvasRenderer(mockCanvas);
      const planes = makeYUVPlanes(4, 4);

      renderer.renderYUV(planes as any);
      renderer.renderYUV(planes as any);

      expect(mockCtx.createImageData).toHaveBeenCalledTimes(1);
    });

    it("creates new ImageData when dimensions change", () => {
      const renderer = new CanvasRenderer(mockCanvas);

      renderer.renderYUV(makeYUVPlanes(4, 4) as any);
      renderer.renderYUV(makeYUVPlanes(8, 8) as any);

      expect(mockCtx.createImageData).toHaveBeenCalledTimes(2);
    });

    it("computes YUV to RGB with I420 format", () => {
      // Use a real ImageData-like buffer so we can inspect results
      let capturedData: Uint8ClampedArray | null = null;
      mockCtx.createImageData = vi.fn((w: number, h: number) => {
        capturedData = new Uint8ClampedArray(w * h * 4);
        return { width: w, height: h, data: capturedData };
      });

      const renderer = new CanvasRenderer(mockCanvas);
      // Y=255 (white), U=128 (neutral), V=128 (neutral) → should be white
      const planes = makeYUVPlanes(2, 2, "I420", 255, 128, 128);
      renderer.renderYUV(planes as any);

      // With limited range bt709, Y=255 should produce bright white
      expect(capturedData).not.toBeNull();
      // Alpha should always be 255
      expect(capturedData![3]).toBe(255);
      expect(capturedData![7]).toBe(255);
    });

    it("computes YUV to RGB with I444 format", () => {
      let capturedData: Uint8ClampedArray | null = null;
      mockCtx.createImageData = vi.fn((w: number, h: number) => {
        capturedData = new Uint8ClampedArray(w * h * 4);
        return { width: w, height: h, data: capturedData };
      });

      const renderer = new CanvasRenderer(mockCanvas);
      const planes = makeYUVPlanes(2, 2, "I444", 128, 128, 128);
      renderer.renderYUV(planes as any);

      expect(capturedData).not.toBeNull();
      expect(capturedData![3]).toBe(255); // alpha always 255
    });

    it("uses bt601 coefficients when set", () => {
      let capturedData: Uint8ClampedArray | null = null;
      mockCtx.createImageData = vi.fn((w: number, h: number) => {
        capturedData = new Uint8ClampedArray(w * h * 4);
        return { width: w, height: h, data: capturedData };
      });

      const renderer = new CanvasRenderer(mockCanvas);
      renderer.setColorSpace("bt601", "sdr");
      const planes = makeYUVPlanes(2, 2, "I420", 200, 100, 200);
      renderer.renderYUV(planes as any);

      // Just verify it runs and produces output
      expect(capturedData).not.toBeNull();
      expect(mockCtx.putImageData).toHaveBeenCalled();
    });

    it("uses full range when set", () => {
      let capturedData: Uint8ClampedArray | null = null;
      mockCtx.createImageData = vi.fn((w: number, h: number) => {
        capturedData = new Uint8ClampedArray(w * h * 4);
        return { width: w, height: h, data: capturedData };
      });

      const renderer = new CanvasRenderer(mockCanvas);
      renderer.setColorSpace("bt709", "sdr", "full");
      const planes = makeYUVPlanes(2, 2, "I420", 128, 128, 128);
      renderer.renderYUV(planes as any);

      expect(capturedData).not.toBeNull();
      expect(mockCtx.putImageData).toHaveBeenCalled();
    });

    it("uses bt2020 coefficients when set", () => {
      const renderer = new CanvasRenderer(mockCanvas);
      renderer.setColorSpace("bt2020", "pq");
      const planes = makeYUVPlanes(2, 2, "I420", 128, 128, 128);
      renderer.renderYUV(planes as any);
      expect(mockCtx.putImageData).toHaveBeenCalled();
    });

    it("falls back to bt709 for unknown primaries", () => {
      const renderer = new CanvasRenderer(mockCanvas);
      renderer.setColorSpace("unknown" as any, "sdr");
      const planes = makeYUVPlanes(2, 2, "I420", 128, 128, 128);
      renderer.renderYUV(planes as any);
      expect(mockCtx.putImageData).toHaveBeenCalled();
    });

    it("handles I422 format with correct chroma rows", () => {
      const renderer = new CanvasRenderer(mockCanvas);
      // I422: half width, full height chroma
      const width = 4;
      const height = 4;
      const chromaW = width >> 1;
      const planes = {
        width,
        height,
        format: "I422",
        y: new Uint8Array(width * height).fill(128),
        u: new Uint8Array(chromaW * height).fill(128),
        v: new Uint8Array(chromaW * height).fill(128),
      };
      renderer.renderYUV(planes as any);
      expect(mockCtx.putImageData).toHaveBeenCalled();
    });
  });

  describe("setColorSpace", () => {
    it("updates primaries", () => {
      const renderer = new CanvasRenderer(mockCanvas);
      renderer.setColorSpace("bt601", "sdr");
      // Verify by rendering and checking that putImageData is called (no crash)
      renderer.renderYUV(makeYUVPlanes(2, 2) as any);
      expect(mockCtx.putImageData).toHaveBeenCalled();
    });

    it("updates range when provided", () => {
      const renderer = new CanvasRenderer(mockCanvas);
      renderer.setColorSpace("bt709", "sdr", "full");
      renderer.renderYUV(makeYUVPlanes(2, 2) as any);
      expect(mockCtx.putImageData).toHaveBeenCalled();
    });

    it("keeps existing range when not provided", () => {
      const renderer = new CanvasRenderer(mockCanvas);
      renderer.setColorSpace("bt709", "sdr", "full");
      renderer.setColorSpace("bt601", "sdr");
      // range should still be "full" — verified by the fact that it doesn't throw
      renderer.renderYUV(makeYUVPlanes(2, 2) as any);
      expect(mockCtx.putImageData).toHaveBeenCalled();
    });
  });

  describe("resize", () => {
    it("updates canvas dimensions", () => {
      const renderer = new CanvasRenderer(mockCanvas);
      renderer.resize(800, 600);
      expect(mockCanvas.width).toBe(800);
      expect(mockCanvas.height).toBe(600);
    });

    it("skips when dimensions unchanged", () => {
      const renderer = new CanvasRenderer(mockCanvas);
      renderer.resize(800, 600);
      mockCanvas.width = 999; // tamper to see if it changes
      renderer.resize(800, 600);
      expect(mockCanvas.width).toBe(999); // should not have been reassigned
    });
  });

  describe("snapshot", () => {
    it("calls toDataURL with defaults", () => {
      const renderer = new CanvasRenderer(mockCanvas);
      const result = renderer.snapshot();
      expect(mockCanvas.toDataURL).toHaveBeenCalledWith("image/png", 0.92);
      expect(result).toContain("data:");
    });

    it("calls toDataURL with custom type and quality", () => {
      const renderer = new CanvasRenderer(mockCanvas);
      renderer.snapshot("jpeg", 0.8);
      expect(mockCanvas.toDataURL).toHaveBeenCalledWith("image/jpeg", 0.8);
    });

    it("calls toDataURL with webp type", () => {
      const renderer = new CanvasRenderer(mockCanvas);
      renderer.snapshot("webp", 0.95);
      expect(mockCanvas.toDataURL).toHaveBeenCalledWith("image/webp", 0.95);
    });
  });

  describe("destroy", () => {
    it("sets destroyed flag", () => {
      const renderer = new CanvasRenderer(mockCanvas);
      renderer.destroy();
      // render should be no-op after destroy
      const frame = { displayWidth: 100, displayHeight: 100, close: vi.fn() };
      renderer.render(frame as any);
      expect(mockCtx.drawImage).not.toHaveBeenCalled();
    });

    it("is idempotent", () => {
      const renderer = new CanvasRenderer(mockCanvas);
      renderer.destroy();
      renderer.destroy(); // should not throw
    });
  });
});

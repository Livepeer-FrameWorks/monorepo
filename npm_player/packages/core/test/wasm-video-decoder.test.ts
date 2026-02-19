import { describe, it, expect, vi, beforeEach } from "vitest";

// Mock the loader so we don't need real WASM files
vi.mock("../src/wasm/WasmDecoderLoader", () => {
  const mockDecoder = {
    configure: vi.fn(),
    decode: vi.fn(() => null),
    flush: vi.fn(() => []),
    destroy: vi.fn(),
  };

  const mockModule = {
    createDecoder: vi.fn(() => ({ ...mockDecoder })),
  };

  return {
    loadDecoder: vi.fn(() => Promise.resolve(mockModule)),
    hasWasmDecoder: vi.fn((codec: string) => {
      const c = codec.toLowerCase();
      if (c === "hevc" || c === "h265" || c.startsWith("hvc1") || c.startsWith("hev1"))
        return "hevc";
      if (c === "av1" || c.startsWith("av01")) return "av1";
      if (c === "vp9" || c.startsWith("vp09")) return "vp9";
      return null;
    }),
  };
});

import { WasmVideoDecoder, needsWasmFallback } from "../src/wasm/WasmVideoDecoder";
import { loadDecoder } from "../src/wasm/WasmDecoderLoader";

describe("WasmVideoDecoder", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  // =========================================================================
  // Constructor
  // =========================================================================
  describe("constructor", () => {
    it("accepts valid HEVC codec strings", () => {
      expect(() => new WasmVideoDecoder("hevc")).not.toThrow();
      expect(() => new WasmVideoDecoder("hvc1.1.6.L93.B0")).not.toThrow();
      expect(() => new WasmVideoDecoder("hev1.1.6.L93.B0")).not.toThrow();
    });

    it("accepts valid AV1 codec strings", () => {
      expect(() => new WasmVideoDecoder("av1")).not.toThrow();
      expect(() => new WasmVideoDecoder("av01.0.08M.08")).not.toThrow();
    });

    it("accepts valid VP9 codec strings", () => {
      expect(() => new WasmVideoDecoder("vp9")).not.toThrow();
      expect(() => new WasmVideoDecoder("vp09.00.10.08")).not.toThrow();
    });

    it("throws for unsupported codecs", () => {
      expect(() => new WasmVideoDecoder("h264")).toThrow("No WASM decoder available");
      expect(() => new WasmVideoDecoder("opus")).toThrow("No WASM decoder available");
      expect(() => new WasmVideoDecoder("")).toThrow("No WASM decoder available");
    });
  });

  // =========================================================================
  // Initialization
  // =========================================================================
  describe("initialize", () => {
    it("loads the WASM module and creates a decoder", async () => {
      const dec = new WasmVideoDecoder("hevc");
      expect(dec.isLoading).toBe(false);
      expect(dec.isConfigured).toBe(false);

      await dec.initialize();

      expect(loadDecoder).toHaveBeenCalledWith("hevc");
      expect(dec.isLoading).toBe(false);
    });

    it("only initializes once", async () => {
      const dec = new WasmVideoDecoder("hevc");
      await dec.initialize();
      await dec.initialize();

      expect(loadDecoder).toHaveBeenCalledTimes(1);
    });

    it("does not initialize if already destroyed", async () => {
      const dec = new WasmVideoDecoder("hevc");
      dec.destroy();
      await dec.initialize();

      expect(loadDecoder).not.toHaveBeenCalled();
    });

    it("calls onError if loading fails", async () => {
      vi.mocked(loadDecoder).mockRejectedValueOnce(new Error("fetch failed"));

      const dec = new WasmVideoDecoder("hevc");
      const errorFn = vi.fn();
      dec.onError = errorFn;

      await expect(dec.initialize()).rejects.toThrow("fetch failed");
      expect(errorFn).toHaveBeenCalledWith(expect.any(Error));
    });
  });

  // =========================================================================
  // Configure
  // =========================================================================
  describe("configure", () => {
    it("throws if not initialized", () => {
      const dec = new WasmVideoDecoder("hevc");
      expect(() => dec.configure(new Uint8Array([1, 2, 3]))).toThrow("not initialized");
    });

    it("sets isConfigured after configure", async () => {
      const dec = new WasmVideoDecoder("hevc");
      await dec.initialize();

      expect(dec.isConfigured).toBe(false);
      dec.configure(new Uint8Array([0x00, 0x00, 0x01, 0x67]));
      expect(dec.isConfigured).toBe(true);
    });
  });

  // =========================================================================
  // Decode
  // =========================================================================
  describe("decode", () => {
    it("does nothing before configure", async () => {
      const dec = new WasmVideoDecoder("hevc");
      await dec.initialize();
      const frameFn = vi.fn();
      dec.onFrame = frameFn;

      dec.decode(new Uint8Array([1, 2, 3]), true, 1000);
      expect(frameFn).not.toHaveBeenCalled();
    });

    it("calls onFrame when decoder returns a frame", async () => {
      const mockFrame = {
        y: new Uint8Array(1920 * 1080),
        u: new Uint8Array(960 * 540),
        v: new Uint8Array(960 * 540),
        width: 1920,
        height: 1080,
        chromaFormat: 420 as const,
        bitDepth: 8 as const,
      };

      vi.mocked(loadDecoder).mockResolvedValueOnce({
        createDecoder: () => ({
          configure: vi.fn(),
          decode: vi.fn(() => mockFrame),
          flush: vi.fn(() => []),
          destroy: vi.fn(),
        }),
      });

      const dec = new WasmVideoDecoder("hevc");
      await dec.initialize();
      dec.configure(new Uint8Array([1]));

      const frameFn = vi.fn();
      dec.onFrame = frameFn;

      dec.decode(new Uint8Array([0, 0, 1, 0x65]), true, 33333);

      expect(frameFn).toHaveBeenCalledTimes(1);
      const output = frameFn.mock.calls[0][0];
      expect(output.timestamp).toBe(33333);
      expect(output.planes.width).toBe(1920);
      expect(output.planes.height).toBe(1080);
      expect(output.planes.format).toBe("I420");
    });

    it("does not fire onFrame when decoder returns null", async () => {
      vi.mocked(loadDecoder).mockResolvedValueOnce({
        createDecoder: () => ({
          configure: vi.fn(),
          decode: vi.fn(() => null),
          flush: vi.fn(() => []),
          destroy: vi.fn(),
        }),
      });

      const dec = new WasmVideoDecoder("hevc");
      await dec.initialize();
      dec.configure(new Uint8Array([1]));

      const frameFn = vi.fn();
      dec.onFrame = frameFn;

      dec.decode(new Uint8Array([0, 0, 1, 0x01]), false, 66666);
      expect(frameFn).not.toHaveBeenCalled();
    });

    it("silently returns after destroy", async () => {
      const dec = new WasmVideoDecoder("hevc");
      await dec.initialize();
      dec.configure(new Uint8Array([1]));
      dec.destroy();

      const frameFn = vi.fn();
      dec.onFrame = frameFn;
      dec.decode(new Uint8Array([1]), true, 0);
      expect(frameFn).not.toHaveBeenCalled();
    });
  });

  // =========================================================================
  // Format mapping (emitFrame)
  // =========================================================================
  describe("format mapping", () => {
    async function decodeWithFrame(frame: any) {
      vi.mocked(loadDecoder).mockResolvedValueOnce({
        createDecoder: () => ({
          configure: vi.fn(),
          decode: vi.fn(() => frame),
          flush: vi.fn(() => []),
          destroy: vi.fn(),
        }),
      });

      const dec = new WasmVideoDecoder("hevc");
      await dec.initialize();
      dec.configure(new Uint8Array([1]));

      const frameFn = vi.fn();
      dec.onFrame = frameFn;
      dec.decode(new Uint8Array([1]), true, 0);
      return frameFn.mock.calls[0][0];
    }

    it("maps 8-bit 4:2:0 to I420", async () => {
      const output = await decodeWithFrame({
        y: new Uint8Array(16),
        u: new Uint8Array(4),
        v: new Uint8Array(4),
        width: 4,
        height: 4,
        chromaFormat: 420,
        bitDepth: 8,
      });
      expect(output.planes.format).toBe("I420");
    });

    it("maps 10-bit to I420P10", async () => {
      const output = await decodeWithFrame({
        y: new Uint8Array(32),
        u: new Uint8Array(8),
        v: new Uint8Array(8),
        width: 4,
        height: 4,
        chromaFormat: 420,
        bitDepth: 10,
      });
      expect(output.planes.format).toBe("I420P10");
    });

    it("maps 4:4:4 to I444", async () => {
      const output = await decodeWithFrame({
        y: new Uint8Array(16),
        u: new Uint8Array(16),
        v: new Uint8Array(16),
        width: 4,
        height: 4,
        chromaFormat: 444,
        bitDepth: 8,
      });
      expect(output.planes.format).toBe("I444");
    });

    it("maps 4:2:2 to I422", async () => {
      const output = await decodeWithFrame({
        y: new Uint8Array(16),
        u: new Uint8Array(8),
        v: new Uint8Array(8),
        width: 4,
        height: 4,
        chromaFormat: 422,
        bitDepth: 8,
      });
      expect(output.planes.format).toBe("I422");
    });

    it("10-bit takes priority over chroma format", async () => {
      const output = await decodeWithFrame({
        y: new Uint8Array(32),
        u: new Uint8Array(32),
        v: new Uint8Array(32),
        width: 4,
        height: 4,
        chromaFormat: 444,
        bitDepth: 10,
      });
      expect(output.planes.format).toBe("I420P10");
    });

    it("passes through color primaries and transfer function", async () => {
      const output = await decodeWithFrame({
        y: new Uint8Array(16),
        u: new Uint8Array(4),
        v: new Uint8Array(4),
        width: 4,
        height: 4,
        chromaFormat: 420,
        bitDepth: 8,
        colorPrimaries: "bt2020",
        transferFunction: "pq",
      });
      expect(output.colorPrimaries).toBe("bt2020");
      expect(output.transferFunction).toBe("pq");
    });
  });

  // =========================================================================
  // Flush
  // =========================================================================
  describe("flush", () => {
    it("emits all buffered frames", async () => {
      const frames = [
        {
          y: new Uint8Array(4),
          u: new Uint8Array(1),
          v: new Uint8Array(1),
          width: 2,
          height: 2,
          chromaFormat: 420 as const,
          bitDepth: 8 as const,
        },
        {
          y: new Uint8Array(4),
          u: new Uint8Array(1),
          v: new Uint8Array(1),
          width: 2,
          height: 2,
          chromaFormat: 420 as const,
          bitDepth: 8 as const,
        },
      ];

      vi.mocked(loadDecoder).mockResolvedValueOnce({
        createDecoder: () => ({
          configure: vi.fn(),
          decode: vi.fn(() => null),
          flush: vi.fn(() => frames),
          destroy: vi.fn(),
        }),
      });

      const dec = new WasmVideoDecoder("hevc");
      await dec.initialize();
      dec.configure(new Uint8Array([1]));

      const frameFn = vi.fn();
      dec.onFrame = frameFn;
      dec.flush();

      expect(frameFn).toHaveBeenCalledTimes(2);
    });

    it("does nothing if not initialized", () => {
      const dec = new WasmVideoDecoder("hevc");
      expect(() => dec.flush()).not.toThrow();
    });
  });

  // =========================================================================
  // Destroy
  // =========================================================================
  describe("destroy", () => {
    it("cleans up state", async () => {
      const dec = new WasmVideoDecoder("hevc");
      await dec.initialize();
      dec.configure(new Uint8Array([1]));

      expect(dec.isConfigured).toBe(true);
      dec.destroy();
      expect(dec.isConfigured).toBe(false);
    });

    it("is idempotent", async () => {
      const dec = new WasmVideoDecoder("hevc");
      await dec.initialize();
      dec.destroy();
      dec.destroy(); // should not throw
    });
  });
});

// =========================================================================
// needsWasmFallback
// =========================================================================
describe("needsWasmFallback", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("returns true when VideoDecoder is undefined", async () => {
    // In node, VideoDecoder doesn't exist
    const result = await needsWasmFallback("hevc");
    expect(result).toBe(true);
  });

  it("returns false for codecs with no WASM decoder when VideoDecoder exists", async () => {
    const origVD = (globalThis as any).VideoDecoder;
    (globalThis as any).VideoDecoder = {
      isConfigSupported: vi.fn().mockResolvedValue({ supported: true }),
    };

    try {
      const result = await needsWasmFallback("h264");
      expect(result).toBe(false);
    } finally {
      if (origVD === undefined) {
        delete (globalThis as any).VideoDecoder;
      } else {
        (globalThis as any).VideoDecoder = origVD;
      }
    }
  });

  it("returns true when VideoDecoder.isConfigSupported rejects", async () => {
    // Temporarily define a VideoDecoder that throws
    const origVD = (globalThis as any).VideoDecoder;
    (globalThis as any).VideoDecoder = {
      isConfigSupported: vi.fn().mockRejectedValue(new Error("not supported")),
    };

    try {
      const config = { codec: "hvc1.1.6.L93.B0", codedWidth: 1920, codedHeight: 1080 };
      const result = await needsWasmFallback("hevc", config as any);
      expect(result).toBe(true);
    } finally {
      if (origVD === undefined) {
        delete (globalThis as any).VideoDecoder;
      } else {
        (globalThis as any).VideoDecoder = origVD;
      }
    }
  });

  it("returns false when VideoDecoder reports supported", async () => {
    const origVD = (globalThis as any).VideoDecoder;
    (globalThis as any).VideoDecoder = {
      isConfigSupported: vi.fn().mockResolvedValue({ supported: true }),
    };

    try {
      const config = { codec: "hvc1.1.6.L93.B0", codedWidth: 1920, codedHeight: 1080 };
      const result = await needsWasmFallback("hevc", config as any);
      expect(result).toBe(false);
    } finally {
      if (origVD === undefined) {
        delete (globalThis as any).VideoDecoder;
      } else {
        (globalThis as any).VideoDecoder = origVD;
      }
    }
  });

  it("returns true when VideoDecoder reports unsupported", async () => {
    const origVD = (globalThis as any).VideoDecoder;
    (globalThis as any).VideoDecoder = {
      isConfigSupported: vi.fn().mockResolvedValue({ supported: false }),
    };

    try {
      const config = { codec: "hvc1.1.6.L93.B0", codedWidth: 1920, codedHeight: 1080 };
      const result = await needsWasmFallback("hevc", config as any);
      expect(result).toBe(true);
    } finally {
      if (origVD === undefined) {
        delete (globalThis as any).VideoDecoder;
      } else {
        (globalThis as any).VideoDecoder = origVD;
      }
    }
  });
});

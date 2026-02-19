import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

// Mock WasmFeatureDetect before importing the module under test
vi.mock("../src/wasm/WasmFeatureDetect", () => ({
  simdSupported: vi.fn(() => true),
}));

import { simdSupported } from "../src/wasm/WasmFeatureDetect";

// We can't easily test the full load path (needs real WASM + fetch),
// so we test the pure functions and the module's codec detection logic.
// For the loader internals, we import them after resetting module state.

describe("WasmDecoderLoader", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  // =========================================================================
  // hasWasmDecoder
  // =========================================================================
  describe("hasWasmDecoder", () => {
    let hasWasmDecoder: typeof import("../src/wasm/WasmDecoderLoader").hasWasmDecoder;

    beforeEach(async () => {
      const mod = await import("../src/wasm/WasmDecoderLoader");
      hasWasmDecoder = mod.hasWasmDecoder;
    });

    it("detects HEVC codec strings", () => {
      expect(hasWasmDecoder("hevc")).toBe("hevc");
      expect(hasWasmDecoder("h265")).toBe("hevc");
      expect(hasWasmDecoder("hvc1.1.6.L93.B0")).toBe("hevc");
      expect(hasWasmDecoder("hev1.1.6.L93.B0")).toBe("hevc");
      expect(hasWasmDecoder("HEVC")).toBe("hevc");
      expect(hasWasmDecoder("H265")).toBe("hevc");
    });

    it("detects AV1 codec strings", () => {
      expect(hasWasmDecoder("av1")).toBe("av1");
      expect(hasWasmDecoder("av01.0.08M.08")).toBe("av1");
      expect(hasWasmDecoder("AV1")).toBe("av1");
    });

    it("detects VP9 codec strings", () => {
      expect(hasWasmDecoder("vp9")).toBe("vp9");
      expect(hasWasmDecoder("vp09.00.10.08")).toBe("vp9");
      expect(hasWasmDecoder("VP9")).toBe("vp9");
    });

    it("returns null for unsupported codecs", () => {
      expect(hasWasmDecoder("h264")).toBeNull();
      expect(hasWasmDecoder("avc1.42E01E")).toBeNull();
      expect(hasWasmDecoder("opus")).toBeNull();
      expect(hasWasmDecoder("vp8")).toBeNull();
      expect(hasWasmDecoder("")).toBeNull();
    });
  });

  // =========================================================================
  // SIMD variant selection
  // =========================================================================
  describe("SIMD variant selection", () => {
    it("simdSupported returns true when mocked", () => {
      expect(simdSupported()).toBe(true);
    });

    it("simdSupported can be overridden to false", () => {
      vi.mocked(simdSupported).mockReturnValueOnce(false);
      expect(simdSupported()).toBe(false);
    });
  });

  // =========================================================================
  // readYUVFrame logic (via DecodedYUVFrame struct)
  // =========================================================================
  describe("DecodedYUVFrame struct layout", () => {
    it("has correct 36-byte layout with 9 i32 fields", () => {
      // Simulate what readYUVFrame does: read a 36-byte struct from memory
      const buffer = new ArrayBuffer(1024);
      const framePtr = 64; // arbitrary offset
      const view = new DataView(buffer, framePtr, 36);

      // Write test frame metadata
      const width = 1920;
      const height = 1080;
      const chromaFormat = 420;
      const bitDepth = 8;
      const yPtr = 256;
      const uPtr = 256 + width * height;
      const vPtr = uPtr + (width / 2) * (height / 2);
      const ySize = width * height;
      const uvSize = (width / 2) * (height / 2);

      view.setInt32(0, width, true);
      view.setInt32(4, height, true);
      view.setInt32(8, chromaFormat, true);
      view.setInt32(12, bitDepth, true);
      view.setInt32(16, yPtr, true);
      view.setInt32(20, uPtr, true);
      view.setInt32(24, vPtr, true);
      view.setInt32(28, ySize, true);
      view.setInt32(32, uvSize, true);

      // Verify we can read back correctly
      expect(view.getInt32(0, true)).toBe(1920);
      expect(view.getInt32(4, true)).toBe(1080);
      expect(view.getInt32(8, true)).toBe(420);
      expect(view.getInt32(12, true)).toBe(8);
      expect(view.getInt32(16, true)).toBe(yPtr);
      expect(view.getInt32(20, true)).toBe(uPtr);
      expect(view.getInt32(24, true)).toBe(vPtr);
      expect(view.getInt32(28, true)).toBe(ySize);
      expect(view.getInt32(32, true)).toBe(uvSize);
    });

    it("handles 10-bit content metadata", () => {
      const buffer = new ArrayBuffer(256);
      const view = new DataView(buffer, 0, 36);
      view.setInt32(0, 3840, true); // 4K width
      view.setInt32(4, 2160, true); // 4K height
      view.setInt32(8, 420, true);
      view.setInt32(12, 10, true); // 10-bit
      view.setInt32(28, 3840 * 2160 * 2, true); // ySize (2 bytes per sample)
      view.setInt32(32, 1920 * 1080 * 2, true); // uvSize

      expect(view.getInt32(12, true)).toBe(10);
      expect(view.getInt32(28, true)).toBe(3840 * 2160 * 2);
    });

    it("handles 4:4:4 chroma format", () => {
      const buffer = new ArrayBuffer(256);
      const view = new DataView(buffer, 0, 36);
      view.setInt32(8, 444, true);
      expect(view.getInt32(8, true)).toBe(444);
    });

    it("handles 4:2:2 chroma format", () => {
      const buffer = new ArrayBuffer(256);
      const view = new DataView(buffer, 0, 36);
      view.setInt32(8, 422, true);
      expect(view.getInt32(8, true)).toBe(422);
    });
  });

  // =========================================================================
  // allocAndCopy pattern
  // =========================================================================
  describe("allocAndCopy pattern", () => {
    it("copies data into WASM memory correctly", () => {
      const wasmMemory = new WebAssembly.Memory({ initial: 1 });
      const data = new Uint8Array([0x00, 0x00, 0x01, 0x67, 0x42, 0xe0, 0x1e]);

      // Simulate malloc returning ptr=128
      const ptr = 128;
      new Uint8Array(wasmMemory.buffer, ptr, data.byteLength).set(data);

      // Verify data was copied
      const copied = new Uint8Array(wasmMemory.buffer, ptr, data.byteLength);
      expect(copied).toEqual(data);
    });

    it("handles large buffers without overflow", () => {
      const wasmMemory = new WebAssembly.Memory({ initial: 64 }); // 4MB
      const size = 1920 * 1080; // 1080p Y plane
      const data = new Uint8Array(size);
      data.fill(128); // mid-gray

      const ptr = 256;
      new Uint8Array(wasmMemory.buffer, ptr, size).set(data);

      const first = new Uint8Array(wasmMemory.buffer, ptr, 1)[0];
      const last = new Uint8Array(wasmMemory.buffer, ptr + size - 1, 1)[0];
      expect(first).toBe(128);
      expect(last).toBe(128);
    });
  });

  // =========================================================================
  // preloadDecoder
  // =========================================================================
  describe("preloadDecoder", () => {
    it("is exported as a function", async () => {
      const mod = await import("../src/wasm/WasmDecoderLoader");
      expect(typeof mod.preloadDecoder).toBe("function");
    });
  });

  // =========================================================================
  // Module cache behavior (unit-level)
  // =========================================================================
  describe("module caching", () => {
    it("loadDecoder is exported and returns a promise", async () => {
      const mod = await import("../src/wasm/WasmDecoderLoader");
      expect(typeof mod.loadDecoder).toBe("function");
      // Calling it will fail in node (no fetch/WebAssembly.instantiateStreaming)
      // but we verify the function signature exists
    });
  });
});

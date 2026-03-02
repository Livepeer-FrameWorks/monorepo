import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

describe("WasmFeatureDetect", () => {
  let origWebAssembly: typeof WebAssembly;
  let origSharedArrayBuffer: typeof SharedArrayBuffer;

  beforeEach(() => {
    origWebAssembly = globalThis.WebAssembly;
    origSharedArrayBuffer = globalThis.SharedArrayBuffer;
    vi.resetModules();
  });

  afterEach(() => {
    (globalThis as any).WebAssembly = origWebAssembly;
    (globalThis as any).SharedArrayBuffer = origSharedArrayBuffer;
    vi.restoreAllMocks();
  });

  describe("simdSupported", () => {
    it("returns true when WebAssembly.validate returns true", async () => {
      (globalThis as any).WebAssembly = {
        validate: vi.fn().mockReturnValue(true),
      };
      const { simdSupported } = await import("../src/wasm/WasmFeatureDetect");
      expect(simdSupported()).toBe(true);
    });

    it("returns false when WebAssembly.validate returns false", async () => {
      (globalThis as any).WebAssembly = {
        validate: vi.fn().mockReturnValue(false),
      };
      const { simdSupported } = await import("../src/wasm/WasmFeatureDetect");
      expect(simdSupported()).toBe(false);
    });

    it("caches result on subsequent calls", async () => {
      const validateFn = vi.fn().mockReturnValue(true);
      (globalThis as any).WebAssembly = { validate: validateFn };
      const { simdSupported } = await import("../src/wasm/WasmFeatureDetect");
      simdSupported();
      simdSupported();
      simdSupported();
      expect(validateFn).toHaveBeenCalledTimes(1);
    });

    it("returns false when validate throws", async () => {
      (globalThis as any).WebAssembly = {
        validate: vi.fn().mockImplementation(() => {
          throw new Error("not supported");
        }),
      };
      const { simdSupported } = await import("../src/wasm/WasmFeatureDetect");
      expect(simdSupported()).toBe(false);
    });

    it("passes SIMD test bytes to validate", async () => {
      const validateFn = vi.fn().mockReturnValue(true);
      (globalThis as any).WebAssembly = { validate: validateFn };
      const { simdSupported } = await import("../src/wasm/WasmFeatureDetect");
      simdSupported();
      expect(validateFn).toHaveBeenCalledWith(expect.any(Uint8Array));
      const bytes = validateFn.mock.calls[0][0] as Uint8Array;
      // WASM magic number
      expect(bytes[0]).toBe(0x00);
      expect(bytes[1]).toBe(0x61);
      expect(bytes[2]).toBe(0x73);
      expect(bytes[3]).toBe(0x6d);
    });
  });

  describe("atomicsSupported", () => {
    it("returns true when SharedArrayBuffer exists and validate passes", async () => {
      (globalThis as any).SharedArrayBuffer = class {};
      (globalThis as any).WebAssembly = {
        validate: vi.fn().mockReturnValue(true),
      };
      const { atomicsSupported } = await import("../src/wasm/WasmFeatureDetect");
      expect(atomicsSupported()).toBe(true);
    });

    it("returns false when SharedArrayBuffer is undefined", async () => {
      delete (globalThis as any).SharedArrayBuffer;
      (globalThis as any).WebAssembly = {
        validate: vi.fn().mockReturnValue(true),
      };
      const { atomicsSupported } = await import("../src/wasm/WasmFeatureDetect");
      expect(atomicsSupported()).toBe(false);
    });

    it("returns false when validate returns false", async () => {
      (globalThis as any).SharedArrayBuffer = class {};
      (globalThis as any).WebAssembly = {
        validate: vi.fn().mockReturnValue(false),
      };
      const { atomicsSupported } = await import("../src/wasm/WasmFeatureDetect");
      expect(atomicsSupported()).toBe(false);
    });

    it("returns false when validate throws", async () => {
      (globalThis as any).SharedArrayBuffer = class {};
      (globalThis as any).WebAssembly = {
        validate: vi.fn().mockImplementation(() => {
          throw new Error("nope");
        }),
      };
      const { atomicsSupported } = await import("../src/wasm/WasmFeatureDetect");
      expect(atomicsSupported()).toBe(false);
    });

    it("caches result on subsequent calls", async () => {
      (globalThis as any).SharedArrayBuffer = class {};
      const validateFn = vi.fn().mockReturnValue(true);
      (globalThis as any).WebAssembly = { validate: validateFn };
      const { atomicsSupported } = await import("../src/wasm/WasmFeatureDetect");
      atomicsSupported();
      atomicsSupported();
      // validate called once for atomics (SIMD not called yet)
      expect(validateFn).toHaveBeenCalledTimes(1);
    });
  });

  describe("getWasmFeatures", () => {
    it("returns both simd and atomics results", async () => {
      (globalThis as any).SharedArrayBuffer = class {};
      (globalThis as any).WebAssembly = {
        validate: vi.fn().mockReturnValue(true),
      };
      const { getWasmFeatures } = await import("../src/wasm/WasmFeatureDetect");
      const features = getWasmFeatures();
      expect(features).toEqual({ simd: true, atomics: true });
    });

    it("returns false for both when WebAssembly not available", async () => {
      delete (globalThis as any).SharedArrayBuffer;
      (globalThis as any).WebAssembly = {
        validate: vi.fn().mockReturnValue(false),
      };
      const { getWasmFeatures } = await import("../src/wasm/WasmFeatureDetect");
      const features = getWasmFeatures();
      expect(features).toEqual({ simd: false, atomics: false });
    });
  });
});

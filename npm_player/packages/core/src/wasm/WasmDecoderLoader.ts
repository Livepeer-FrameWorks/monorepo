/**
 * WASM Decoder Loader — On-demand codec module loading
 *
 * Loads WASM decoder modules for codecs not supported by the browser's WebCodecs.
 * Modules are loaded on-demand (not at registration time) to avoid bundle bloat.
 *
 * Each codec has two variants:
 * - `baseline`: works on all WASM-capable browsers
 * - `simd`: uses 128-bit SIMD for ~2-4x speedup on supported browsers
 *
 * WASM files are self-hosted alongside the player (no CDN dependency).
 */

import { simdSupported } from "./WasmFeatureDetect";

export type WasmCodec = "hevc" | "av1" | "vp9";

export interface WasmDecoder {
  /** Configure the decoder with codec-specific data (SPS/PPS for HEVC, etc.) */
  configure(config: Uint8Array): void;
  /**
   * Decode a single NAL unit / OBU.
   * Returns decoded YUV planes or null if the frame is not yet available
   * (B-frame reordering, etc.)
   */
  decode(data: Uint8Array, isKeyframe: boolean): DecodedYUVFrame | null;
  /** Flush any remaining buffered frames */
  flush(): DecodedYUVFrame[];
  /** Release all resources */
  destroy(): void;
}

export interface DecodedYUVFrame {
  y: Uint8Array;
  u: Uint8Array;
  v: Uint8Array;
  width: number;
  height: number;
  /** Chroma subsampling: 420 (most common), 422, 444 */
  chromaFormat: 420 | 422 | 444;
  /** Bits per sample: 8 or 10 */
  bitDepth: 8 | 10;
  /** Color primaries if signaled */
  colorPrimaries?: "bt601" | "bt709" | "bt2020";
  /** Transfer function if signaled */
  transferFunction?: "srgb" | "pq" | "hlg";
}

export interface WasmCodecModule {
  createDecoder(): WasmDecoder;
}

// Cache loaded modules
const moduleCache = new Map<string, WasmCodecModule>();
const loadingPromises = new Map<string, Promise<WasmCodecModule>>();

/**
 * Resolve the base URL for WASM files.
 * Tries import.meta.url first (module environments), falls back to known paths.
 */
function resolveWasmBaseUrl(): string {
  try {
    // In bundled environment, WASM files are copied to dist/wasm/
    return new URL("../wasm", import.meta.url).href;
  } catch {
    // Fallback: assume wasm/ is served from the same origin
    return "/wasm";
  }
}

/**
 * Load a WASM decoder module for a specific codec.
 * Returns cached module if already loaded. Loading is deduplicated.
 */
export async function loadDecoder(codec: WasmCodec): Promise<WasmCodecModule> {
  const variant = simdSupported() ? "simd" : "baseline";
  const key = `${codec}-${variant}`;

  // Return cached
  const cached = moduleCache.get(key);
  if (cached) return cached;

  // Deduplicate concurrent loads
  const existing = loadingPromises.get(key);
  if (existing) return existing;

  const promise = doLoadDecoder(codec, variant, key);
  loadingPromises.set(key, promise);

  try {
    const mod = await promise;
    moduleCache.set(key, mod);
    return mod;
  } finally {
    loadingPromises.delete(key);
  }
}

async function doLoadDecoder(
  codec: WasmCodec,
  variant: string,
  _key: string
): Promise<WasmCodecModule> {
  const baseUrl = resolveWasmBaseUrl();
  const wasmUrl = `${baseUrl}/${codec}-${variant}.wasm`;

  // Minimal import object — Emscripten standalone WASM exports its own memory,
  // so we don't provide env.memory by default. If the module requires imported
  // memory (wasi-sdk builds), we retry with it.
  const minimalImports: WebAssembly.Imports = {
    env: {
      __stack_pointer: new WebAssembly.Global({ value: "i32", mutable: true }, 0),
      abort: () => {
        throw new Error("WASM abort");
      },
    },
    wasi_snapshot_preview1: {
      proc_exit: () => {},
      fd_write: () => 0,
      fd_seek: () => 0,
      fd_close: () => 0,
    },
  };

  // Full import object with imported memory (fallback for modules that need it)
  const fullImports: WebAssembly.Imports = {
    ...minimalImports,
    env: {
      ...minimalImports.env,
      memory: new WebAssembly.Memory({ initial: 256, maximum: 4096 }),
    },
  };

  let instance: WebAssembly.Instance;

  try {
    instance = await instantiateWasm(wasmUrl, minimalImports);
  } catch {
    // Retry with imported memory for modules that expect it
    try {
      instance = await instantiateWasm(wasmUrl, fullImports);
    } catch (err) {
      throw new Error(`Failed to load WASM decoder ${codec}-${variant}: ${err}`);
    }
  }

  return wrapWasmModule(instance, codec);
}

async function instantiateWasm(
  url: string,
  imports: WebAssembly.Imports
): Promise<WebAssembly.Instance> {
  if (typeof WebAssembly.instantiateStreaming === "function") {
    const result = await WebAssembly.instantiateStreaming(fetch(url), imports);
    return result.instance;
  }
  const response = await fetch(url);
  const bytes = await response.arrayBuffer();
  const result = await WebAssembly.instantiate(bytes, imports);
  return result.instance;
}

/**
 * Wrap a raw WASM instance into our WasmCodecModule interface.
 * The exported functions follow a standard naming convention per codec.
 */
function wrapWasmModule(instance: WebAssembly.Instance, codec: WasmCodec): WasmCodecModule {
  const exports = instance.exports as Record<string, Function>;
  const memory = (instance.exports.memory ??
    (instance.exports.env as any)?.memory) as WebAssembly.Memory;

  return {
    createDecoder(): WasmDecoder {
      // Call the WASM module's create function
      const ctx = exports[`${codec}_create_decoder`]?.() ?? exports["create_decoder"]?.() ?? 0;

      return {
        configure(config: Uint8Array): void {
          const configPtr = allocAndCopy(memory, exports, config);
          const configureFn = exports[`${codec}_configure`] ?? exports["configure"];
          configureFn?.(ctx, configPtr, config.byteLength);
          freeMem(exports, configPtr);
        },

        decode(data: Uint8Array, isKeyframe: boolean): DecodedYUVFrame | null {
          const dataPtr = allocAndCopy(memory, exports, data);
          const decodeFn = exports[`${codec}_decode`] ?? exports["decode"];
          const resultPtr = decodeFn?.(ctx, dataPtr, data.byteLength, isKeyframe ? 1 : 0) ?? 0;
          freeMem(exports, dataPtr);

          if (resultPtr === 0) return null;
          return readYUVFrame(memory, exports, resultPtr);
        },

        flush(): DecodedYUVFrame[] {
          const flushFn = exports[`${codec}_flush`] ?? exports["flush"];
          const frames: DecodedYUVFrame[] = [];
          if (!flushFn) return frames;

          let resultPtr = flushFn(ctx);
          while (resultPtr !== 0) {
            frames.push(readYUVFrame(memory, exports, resultPtr));
            resultPtr = flushFn(ctx);
          }
          return frames;
        },

        destroy(): void {
          const destroyFn = exports[`${codec}_destroy`] ?? exports["destroy"];
          destroyFn?.(ctx);
        },
      };
    },
  };
}

/** Allocate WASM memory and copy data into it */
function allocAndCopy(
  memory: WebAssembly.Memory,
  exports: Record<string, Function>,
  data: Uint8Array
): number {
  const mallocFn = exports["malloc"] ?? exports["__wasm_alloc"];
  if (!mallocFn) throw new Error("WASM module missing malloc");
  const ptr = mallocFn(data.byteLength) as number;
  new Uint8Array(memory.buffer, ptr, data.byteLength).set(data);
  return ptr;
}

/** Free WASM memory */
function freeMem(exports: Record<string, Function>, ptr: number): void {
  const freeFn = exports["free"] ?? exports["__wasm_free"];
  freeFn?.(ptr);
}

/** Read a decoded YUV frame from WASM memory */
function readYUVFrame(
  memory: WebAssembly.Memory,
  exports: Record<string, Function>,
  resultPtr: number
): DecodedYUVFrame {
  // Frame layout in WASM memory: [width:i32, height:i32, chromaFormat:i32, bitDepth:i32, yPtr:i32, uPtr:i32, vPtr:i32, ySize:i32, uvSize:i32]
  const view = new DataView(memory.buffer, resultPtr, 36);
  const width = view.getInt32(0, true);
  const height = view.getInt32(4, true);
  const chromaFormat = view.getInt32(8, true) as 420 | 422 | 444;
  const bitDepth = view.getInt32(12, true) as 8 | 10;
  const yPtr = view.getInt32(16, true);
  const uPtr = view.getInt32(20, true);
  const vPtr = view.getInt32(24, true);
  const ySize = view.getInt32(28, true);
  const uvSize = view.getInt32(32, true);

  // Copy planes out of WASM memory (they may be overwritten on next decode)
  const y = new Uint8Array(ySize);
  y.set(new Uint8Array(memory.buffer, yPtr, ySize));
  const u = new Uint8Array(uvSize);
  u.set(new Uint8Array(memory.buffer, uPtr, uvSize));
  const v = new Uint8Array(uvSize);
  v.set(new Uint8Array(memory.buffer, vPtr, uvSize));

  // Free the result struct
  const freeFn = exports["free_frame"] ?? exports["free"];
  freeFn?.(resultPtr);

  return { y, u, v, width, height, chromaFormat, bitDepth };
}

/**
 * Check if a WASM decoder is available for a given codec string.
 */
export function hasWasmDecoder(codec: string): WasmCodec | null {
  const c = codec.toLowerCase();
  if (c === "hevc" || c === "h265" || c.startsWith("hvc1") || c.startsWith("hev1")) return "hevc";
  if (c === "av1" || c.startsWith("av01")) return "av1";
  if (c === "vp9" || c.startsWith("vp09")) return "vp9";
  return null;
}

/**
 * Preload a decoder module (e.g., during idle time).
 * Does not block — returns immediately.
 */
export function preloadDecoder(codec: WasmCodec): void {
  loadDecoder(codec).catch(() => {
    // Preload failure is non-fatal
  });
}

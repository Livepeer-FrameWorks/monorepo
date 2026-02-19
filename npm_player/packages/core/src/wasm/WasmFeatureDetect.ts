/**
 * WASM Feature Detection
 *
 * Detects SIMD and Atomics/SharedArrayBuffer support at the WebAssembly level
 * using tiny validation snippets. Results are cached on first call.
 */

// Minimal WASM module that uses v128 SIMD instruction (i32x4.splat)
// Compiled from: (module (func (result v128) (i32x4.splat (i32.const 0))))
const SIMD_TEST = new Uint8Array([
  0x00,
  0x61,
  0x73,
  0x6d, // magic
  0x01,
  0x00,
  0x00,
  0x00, // version
  0x01,
  0x05,
  0x01,
  0x60,
  0x00,
  0x01,
  0x7b, // type section: () -> v128
  0x03,
  0x02,
  0x01,
  0x00, // function section
  0x0a,
  0x0a,
  0x01,
  0x08,
  0x00,
  0x41,
  0x00,
  0xfd,
  0x0c,
  0x00,
  0x00,
  0x00,
  0x00,
  0x0b, // code: i32x4.splat(0)
]);

// Minimal WASM module that uses atomic.wait instruction
// Compiled from: (module (memory 1 1 shared) (func (result i32) (memory.atomic.wait32 (i32.const 0) (i32.const 0) (i64.const 0))))
const ATOMICS_TEST = new Uint8Array([
  0x00,
  0x61,
  0x73,
  0x6d, // magic
  0x01,
  0x00,
  0x00,
  0x00, // version
  0x01,
  0x05,
  0x01,
  0x60,
  0x00,
  0x01,
  0x7f, // type section: () -> i32
  0x03,
  0x02,
  0x01,
  0x00, // function section
  0x05,
  0x04,
  0x01,
  0x03,
  0x01,
  0x01, // memory section: shared 1 1
  0x0a,
  0x12,
  0x01,
  0x10,
  0x00,
  0x41,
  0x00,
  0x41,
  0x00,
  0x42,
  0x00,
  0xfe,
  0x01,
  0x02,
  0x00,
  0x0b, // code
]);

let _simd: boolean | null = null;
let _atomics: boolean | null = null;

/** Whether the browser supports WASM SIMD (128-bit packed operations) */
export function simdSupported(): boolean {
  if (_simd === null) {
    try {
      _simd = WebAssembly.validate(SIMD_TEST);
    } catch {
      _simd = false;
    }
  }
  return _simd;
}

/** Whether the browser supports WASM Atomics + SharedArrayBuffer */
export function atomicsSupported(): boolean {
  if (_atomics === null) {
    try {
      // SharedArrayBuffer must also be available
      if (typeof SharedArrayBuffer === "undefined") {
        _atomics = false;
      } else {
        _atomics = WebAssembly.validate(ATOMICS_TEST);
      }
    } catch {
      _atomics = false;
    }
  }
  return _atomics;
}

/** Summary of detected features */
export function getWasmFeatures(): { simd: boolean; atomics: boolean } {
  return {
    simd: simdSupported(),
    atomics: atomicsSupported(),
  };
}

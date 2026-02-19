/**
 * WASM Video Decoder — WebCodecs-compatible wrapper
 *
 * When VideoDecoder.isConfigSupported() returns false for a codec (e.g., HEVC on Firefox),
 * this wrapper loads the appropriate WASM module and provides a compatible decode interface
 * that outputs raw YUV planes for WebGLRenderer.renderYUV().
 *
 * Usage flow:
 * 1. Try WebCodecs native → if unsupported:
 * 2. Create WasmVideoDecoder(codec)
 * 3. configure(description)
 * 4. decode(data, isKeyframe) → YUV planes
 * 5. Feed planes to WebGLRenderer.renderYUV()
 */

import { loadDecoder, hasWasmDecoder } from "./WasmDecoderLoader";
import type { WasmDecoder, DecodedYUVFrame, WasmCodec } from "./WasmDecoderLoader";
import type {
  YUVPlanes,
  PixelFormat,
  ColorPrimaries,
  TransferFunction,
} from "../rendering/WebGLRenderer";

export interface WasmDecodedOutput {
  planes: YUVPlanes;
  timestamp: number; // microseconds, passed through from input
  colorPrimaries?: ColorPrimaries;
  transferFunction?: TransferFunction;
}

export class WasmVideoDecoder {
  private codec: WasmCodec;
  private decoder: WasmDecoder | null = null;
  private loading = false;
  private configured = false;
  private destroyed = false;

  /** Callback when a frame is decoded */
  onFrame?: (output: WasmDecodedOutput) => void;
  /** Callback on error */
  onError?: (err: Error) => void;

  constructor(codec: string) {
    const wasmCodec = hasWasmDecoder(codec);
    if (!wasmCodec) throw new Error(`No WASM decoder available for codec: ${codec}`);
    this.codec = wasmCodec;
  }

  /**
   * Load the WASM module and create a decoder instance.
   * Must be called before configure/decode.
   */
  async initialize(): Promise<void> {
    if (this.decoder || this.loading || this.destroyed) return;
    this.loading = true;
    try {
      const module = await loadDecoder(this.codec);
      if (this.destroyed) return;
      this.decoder = module.createDecoder();
    } catch (err) {
      this.onError?.(err instanceof Error ? err : new Error(String(err)));
      throw err;
    } finally {
      this.loading = false;
    }
  }

  /**
   * Configure the decoder with codec-specific init data.
   * For HEVC: SPS/PPS NAL units. For AV1: OBU sequence header.
   */
  configure(description: Uint8Array): void {
    if (!this.decoder) throw new Error("Decoder not initialized");
    this.decoder.configure(description);
    this.configured = true;
  }

  /**
   * Decode a single encoded chunk.
   * Fires onFrame callback when a decoded frame is available.
   */
  decode(data: Uint8Array, isKeyframe: boolean, timestamp: number): void {
    if (!this.decoder || !this.configured || this.destroyed) return;

    try {
      const frame = this.decoder.decode(data, isKeyframe);
      if (frame) {
        this.emitFrame(frame, timestamp);
      }
    } catch (err) {
      this.onError?.(err instanceof Error ? err : new Error(String(err)));
    }
  }

  /**
   * Flush any buffered frames (call on seek or stream end).
   */
  flush(): void {
    if (!this.decoder || this.destroyed) return;
    const frames = this.decoder.flush();
    for (const frame of frames) {
      this.emitFrame(frame, 0);
    }
  }

  private emitFrame(frame: DecodedYUVFrame, timestamp: number): void {
    const format: PixelFormat =
      frame.bitDepth === 10
        ? "I420P10"
        : frame.chromaFormat === 444
          ? "I444"
          : frame.chromaFormat === 422
            ? "I422"
            : "I420";

    const planes: YUVPlanes = {
      y: frame.y,
      u: frame.u,
      v: frame.v,
      width: frame.width,
      height: frame.height,
      format,
    };

    this.onFrame?.({
      planes,
      timestamp,
      colorPrimaries: frame.colorPrimaries,
      transferFunction: frame.transferFunction,
    });
  }

  /** Whether the WASM decoder is ready for decode() calls */
  get isConfigured(): boolean {
    return this.configured;
  }

  /** Whether the WASM module is still loading */
  get isLoading(): boolean {
    return this.loading;
  }

  destroy(): void {
    if (this.destroyed) return;
    this.destroyed = true;
    this.decoder?.destroy();
    this.decoder = null;
    this.configured = false;
  }
}

/**
 * Check if a codec needs WASM fallback (not natively supported by WebCodecs).
 */
export async function needsWasmFallback(
  codec: string,
  config?: VideoDecoderConfig
): Promise<boolean> {
  if (typeof VideoDecoder === "undefined") return true;

  const wasmCodec = hasWasmDecoder(codec);
  if (!wasmCodec) return false; // No WASM decoder available, can't fallback

  if (config) {
    try {
      const result = await VideoDecoder.isConfigSupported(config);
      return !result.supported;
    } catch {
      return true;
    }
  }

  // No config provided — check if the codec family is generally supported
  // HEVC is the main one that Firefox doesn't support
  if (wasmCodec === "hevc") {
    try {
      const result = await VideoDecoder.isConfigSupported({
        codec: "hvc1.1.6.L93.B0",
        codedWidth: 1920,
        codedHeight: 1080,
      });
      return !result.supported;
    } catch {
      return true;
    }
  }

  return false;
}

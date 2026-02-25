/**
 * WebCodecs Decoder Worker
 *
 * Handles VideoDecoder and AudioDecoder in a dedicated worker thread.
 * This keeps decoding off the main thread for better performance.
 *
 * Features:
 * - Video/Audio pipeline management per track
 * - Frame scheduling based on timestamps and playback speed
 * - Stats collection and reporting
 * - Seek handling with queue flush
 */

import type {
  MainToWorkerMessage,
  WorkerToMainMessage,
  PipelineState,
  FrameTiming,
  DecodedFrame,
  VideoDecoderInit,
  AudioDecoderInit,
  TransferFrameMessage,
  TransferYUVMessage,
} from "./types";
import type { PipelineStats, FrameTrackerStats } from "../types";
import { WasmVideoDecoder, needsWasmFallback } from "../../../wasm/WasmVideoDecoder";
import type { WasmDecodedOutput } from "../../../wasm/WasmVideoDecoder";

// ============================================================================
// Global State
// ============================================================================

const pipelines = new Map<number, PipelineState>();
let debugging: boolean | "verbose" = false;
let uidCounter = 0;

// Frame timing state (shared across all pipelines)
const frameTiming: FrameTiming = {
  in: 0,
  decoded: 0,
  out: 0,
  speed: {
    main: 1,
    tweak: 1,
    combined: 1,
  },
  seeking: false,
  paused: false,
  serverTime: 0,
};

// Per-track wall-clock reference points for frame scheduling
// Each track gets its own baseTime to handle different timestamp bases for A/V
const trackBaseTimes = new Map<number, number>();
// During seek, gate completion on a stable primary track (prefer video).
let seekGateTrackIdx: number | null = null;

// Buffer warmup state - prevents initial jitter by waiting for buffer to build
// Before warmup, frames are queued but not output
let warmupComplete = false;
let warmupStartTime: number | null = null;
const WARMUP_BUFFER_MS = 100; // Wait for ~100ms of frames before starting output
const WARMUP_TIMEOUT_MS = 300; // Reduced from 500ms - start faster to reduce latency

/**
 * Get or initialize baseTime for a specific track
 */
function getTrackBaseTime(idx: number, frameTimeMs: number, now: number): number {
  if (!trackBaseTimes.has(idx)) {
    trackBaseTimes.set(idx, now - frameTimeMs / frameTiming.speed.combined);
    log(
      `Track ${idx} baseTime: ${trackBaseTimes.get(idx)!.toFixed(0)} (first frame @ ${frameTimeMs.toFixed(0)}ms)`
    );
  }
  return trackBaseTimes.get(idx)!;
}

/**
 * Reset all track baseTimes (used during seek or reset)
 */
function resetBaseTime(): void {
  trackBaseTimes.clear();
  log(`Reset all track baseTimes`);
}

function selectSeekGateTrack(): number | null {
  let fallbackIdx: number | null = null;
  for (const pipeline of pipelines.values()) {
    if (pipeline.closed) continue;
    if (fallbackIdx === null || pipeline.idx < fallbackIdx) {
      fallbackIdx = pipeline.idx;
    }
    if (pipeline.track.type === "video") {
      return pipeline.idx;
    }
  }
  return fallbackIdx;
}

function cloneVideoFrame(frame: VideoFrame): VideoFrame | null {
  try {
    if ("clone" in frame) {
      return (frame as VideoFrame).clone();
    }
    return new VideoFrame(frame);
  } catch {
    return null;
  }
}

function pushFrameHistory(pipeline: PipelineState, frame: VideoFrame, timestamp: number): void {
  if (pipeline.track.type !== "video") return;
  if (!pipeline.frameHistory) pipeline.frameHistory = [];

  const cloned = cloneVideoFrame(frame);
  if (!cloned) return;

  pipeline.frameHistory.push({ frame: cloned, timestamp });

  // Trim history
  while (pipeline.frameHistory.length > MAX_FRAME_HISTORY) {
    const entry = pipeline.frameHistory.shift();
    if (entry) {
      try {
        entry.frame.close();
      } catch {}
    }
  }

  pipeline.historyCursor = pipeline.frameHistory.length - 1;
}

function alignHistoryCursorToLastOutput(pipeline: PipelineState): void {
  if (!pipeline.frameHistory || pipeline.frameHistory.length === 0) return;
  const lastTs = pipeline.stats.lastOutputTimestamp;
  if (!Number.isFinite(lastTs)) {
    pipeline.historyCursor = pipeline.frameHistory.length - 1;
    return;
  }
  // Find first history entry greater than last output, then step back one
  const idx = pipeline.frameHistory.findIndex((entry) => entry.timestamp > lastTs);
  if (idx === -1) {
    pipeline.historyCursor = pipeline.frameHistory.length - 1;
    return;
  }
  pipeline.historyCursor = Math.max(0, idx - 1);
}

function getPrimaryVideoPipeline(): PipelineState | null {
  let selected: PipelineState | null = null;
  for (const pipeline of pipelines.values()) {
    if (pipeline.track.type === "video") {
      if (!selected || pipeline.idx < selected.idx) {
        selected = pipeline;
      }
    }
  }
  return selected;
}

// Stats update interval
let statsTimer: ReturnType<typeof setInterval> | null = null;
const STATS_INTERVAL_MS = 250;

// Frame dropping stats (Phase 2B)
let _totalFramesDropped = 0;

// Chrome-recommended decoder queue threshold.
// When decodeQueueSize exceeds this, new chunks are held in inputQueue (backpressure)
// rather than submitted to the hardware decoder. The decoder output callback drains
// the inputQueue as capacity frees up. This prevents the huge decoder queue buildup
// (80-148 frames) that caused post-warmup pressure drops and multi-second freezes.
const MAX_DECODER_QUEUE_SIZE = 2;
// Safety valve: if inputQueue grows beyond this, the decoder genuinely can't keep up.
// Drop to next keyframe rather than buffering unboundedly.
const MAX_INPUT_QUEUE_BEFORE_DROP = 300;
const MAX_FRAME_HISTORY = 60;
const MAX_OUTPUT_QUEUE = 60; // ~2s at 30fps; backpressure halts decoding above this
const MAX_PAUSED_OUTPUT_QUEUE = 120;
const MAX_PAUSED_INPUT_QUEUE = 600;

// ============================================================================
// Annex B <-> AVCC Frame Conversion (inlined to avoid worker import issues)
// ============================================================================

/** Check if frame data starts with an Annex B start code (0x000001 or 0x00000001) */
function hasAnnexBStartCode(data: Uint8Array): boolean {
  if (data.length < 4) return false;
  if (data[0] === 0 && data[1] === 0 && data[2] === 0 && data[3] === 1) return true;
  if (data[0] === 0 && data[1] === 0 && data[2] === 1) return true;
  return false;
}

/**
 * Convert Annex B frame data to AVCC format (replace start codes with 4-byte NAL lengths).
 * Handles both 3-byte (0x000001) and 4-byte (0x00000001) start codes.
 */
function annexBFrameToAvcc(data: Uint8Array): Uint8Array {
  // Find all NAL unit boundaries
  const nalUnits: Array<{ start: number; end: number }> = [];
  let i = 0;
  while (i < data.length - 2) {
    // Look for start code
    if (data[i] === 0 && data[i + 1] === 0) {
      let startCodeLen = 0;
      if (i + 3 < data.length && data[i + 2] === 0 && data[i + 3] === 1) {
        startCodeLen = 4;
      } else if (data[i + 2] === 1) {
        startCodeLen = 3;
      }
      if (startCodeLen > 0) {
        // End previous NAL unit
        if (nalUnits.length > 0) {
          nalUnits[nalUnits.length - 1].end = i;
        }
        nalUnits.push({ start: i + startCodeLen, end: data.length });
        i += startCodeLen;
        continue;
      }
    }
    i++;
  }

  if (nalUnits.length === 0) return data;

  // Calculate total output size: 4 bytes length prefix per NAL + NAL data
  let totalSize = 0;
  for (const nal of nalUnits) {
    totalSize += 4 + (nal.end - nal.start);
  }

  const out = new Uint8Array(totalSize);
  let offset = 0;
  for (const nal of nalUnits) {
    const nalLen = nal.end - nal.start;
    // Write 4-byte big-endian length
    out[offset] = (nalLen >>> 24) & 0xff;
    out[offset + 1] = (nalLen >>> 16) & 0xff;
    out[offset + 2] = (nalLen >>> 8) & 0xff;
    out[offset + 3] = nalLen & 0xff;
    out.set(data.subarray(nal.start, nal.end), offset + 4);
    offset += 4 + nalLen;
  }

  return out;
}

// ============================================================================
// Logging
// ============================================================================

function log(msg: string, level: "info" | "warn" | "error" = "info"): void {
  if (!debugging) return;

  const message: WorkerToMainMessage = {
    type: "log",
    msg,
    level,
    uid: uidCounter++,
  };
  self.postMessage(message);
}

function logVerbose(msg: string): void {
  if (debugging !== "verbose") return;
  log(msg);
}

// ============================================================================
// Message Handling
// ============================================================================

self.onmessage = (event: MessageEvent<MainToWorkerMessage>) => {
  const msg = event.data;

  switch (msg.type) {
    case "create":
      handleCreate(msg);
      break;

    case "configure":
      handleConfigure(msg);
      break;

    case "receive":
      handleReceive(msg);
      break;

    case "setwritable":
      handleSetWritable(msg);
      break;

    case "creategenerator":
      handleCreateGenerator(msg);
      break;

    case "close":
      handleClose(msg);
      break;

    case "frametiming":
      handleFrameTiming(msg);
      break;

    case "seek":
      handleSeek(msg);
      break;

    case "framestep":
      handleFrameStep(msg);
      break;

    case "setrendermode":
      handleSetRenderMode(msg);
      break;

    case "debugging":
      debugging = msg.value;
      log(`Debugging set to: ${msg.value}`);
      break;

    default:
      log(`Unknown message type: ${(msg as any).type}`, "warn");
  }
};

// ============================================================================
// Pipeline Management
// ============================================================================

function handleSetRenderMode(msg: MainToWorkerMessage & { type: "setrendermode" }): void {
  const { idx, directTransfer, uid } = msg;
  const pipeline = pipelines.get(idx);
  if (!pipeline) {
    sendError(uid, idx, "Pipeline not found");
    return;
  }
  pipeline.directTransfer = directTransfer;
  log(`Pipeline ${idx} directTransfer=${directTransfer}`);
  sendAck(uid, idx);
}

function handleCreate(msg: MainToWorkerMessage & { type: "create" }): void {
  const { idx, track, opts, uid } = msg;

  log(`Creating pipeline for track ${idx} (${track.type} ${track.codec})`);

  const pipeline: PipelineState = {
    idx,
    track,
    configured: false,
    closed: false,
    decoder: null,
    writable: null,
    writer: null,
    inputQueue: [],
    outputQueue: [],
    frameHistory: track.type === "video" ? [] : undefined,
    historyCursor: track.type === "video" ? null : undefined,
    stats: {
      framesIn: 0,
      framesDecoded: 0,
      framesOut: 0,
      framesDropped: 0,
      lastInputTimestamp: 0,
      lastOutputTimestamp: 0,
      decoderQueueSize: 0,
      lastChunkType: "" as string,
      lastChunkSize: 0,
      lastChunkBytes: "" as string,
    },
    optimizeForLatency: opts.optimizeForLatency,
    payloadFormat: opts.payloadFormat || "avcc",
    directTransfer: false,
    wasmDecoder: null,
    lastDecoderConfig: null,
    lastResetTime: 0,
  };

  pipelines.set(idx, pipeline);

  // Start stats reporting if not already running
  if (!statsTimer) {
    statsTimer = setInterval(sendStats, STATS_INTERVAL_MS);
  }

  sendAck(uid, idx);
}

function handleConfigure(msg: MainToWorkerMessage & { type: "configure" }): void {
  const { idx, header, uid } = msg;

  log(`Received configure for track ${idx}, header length=${header?.byteLength ?? "null"}`);

  const pipeline = pipelines.get(idx);

  if (!pipeline) {
    log(`Cannot configure: pipeline ${idx} not found`, "error");
    sendError(uid, idx, "Pipeline not found");
    return;
  }

  // Skip if already configured and decoder is ready
  // This prevents duplicate configuration when both WS INIT and HTTP fallback fire
  if (pipeline.configured && pipeline.decoder && pipeline.decoder.state === "configured") {
    log(`Track ${idx} already configured, skipping duplicate configure`);
    sendAck(uid, idx);
    return;
  }

  try {
    if (pipeline.track.type === "video") {
      log(`Configuring video decoder for track ${idx}...`);
      configureVideoDecoder(pipeline, header);
    } else if (pipeline.track.type === "audio") {
      log(`Configuring audio decoder for track ${idx}...`);
      configureAudioDecoder(pipeline, header);
    }

    pipeline.configured = true;
    log(`Successfully configured decoder for track ${idx}`);
    sendAck(uid, idx);
  } catch (err) {
    log(`Failed to configure decoder for track ${idx}: ${err}`, "error");
    sendError(uid, idx, String(err));
  }
}

async function configureVideoDecoder(
  pipeline: PipelineState,
  description?: Uint8Array
): Promise<void> {
  const track = pipeline.track;

  // Handle JPEG codec separately via ImageDecoder (Phase 2C)
  if (track.codec === "JPEG" || track.codec.toLowerCase() === "jpeg") {
    log("JPEG codec detected - will use ImageDecoder");
    pipeline.configured = true;
    return;
  }

  // Close existing decoder if any (per rawws.js reconfiguration pattern)
  if (pipeline.decoder) {
    if (pipeline.decoder.state === "configured") {
      try {
        pipeline.decoder.reset();
      } catch {
        // Ignore reset errors
      }
    }
    if (pipeline.decoder.state !== "closed") {
      try {
        pipeline.decoder.close();
      } catch {
        // Ignore close errors
      }
    }
    pipeline.decoder = null;
  }

  // Destroy existing WASM decoder if any
  if (pipeline.wasmDecoder) {
    (pipeline.wasmDecoder as WasmVideoDecoder).destroy();
    pipeline.wasmDecoder = null;
  }

  const codecString = track.codecstring || track.codec.toLowerCase();

  // Match reference rawws.js configOpts pattern:
  // codec, optimizeForLatency, description + hw acceleration hint
  const config: VideoDecoderInit = {
    codec: codecString,
    optimizeForLatency: pipeline.optimizeForLatency,
    hardwareAcceleration: "prefer-hardware",
  };

  // Pass description directly (matches reference rawws.js: pipeline.decoderConfigOpts.description = header)
  // For ws/video/h264 (payloadFormat=annexb), no INIT frame arrives so description will be empty —
  // that's fine because SPS/PPS comes inline in the Annex B bitstream.
  if (description && description.byteLength > 0) {
    config.description = description;
    log(`Configuring with description (${description.byteLength} bytes)`);
  } else {
    log(`No description provided - SPS/PPS expected inline in bitstream`);
  }

  // Check if native WebCodecs supports this codec — fall back to WASM if not
  let useWasm = false;
  try {
    useWasm = await needsWasmFallback(codecString, config as VideoDecoderConfig);
  } catch {
    useWasm = false;
  }

  if (useWasm && pipeline.directTransfer) {
    log(`Native VideoDecoder does not support ${codecString} — using WASM fallback`);
    try {
      const wasmDec = new WasmVideoDecoder(codecString);
      await wasmDec.initialize();

      if (description && description.byteLength > 0) {
        wasmDec.configure(description);
      }

      wasmDec.onFrame = (output: WasmDecodedOutput) => {
        if (pipeline.closed) return;
        emitWasmFrame(pipeline, output);
      };

      wasmDec.onError = (err: Error) => {
        log(`WASM decoder error on track ${pipeline.idx}: ${err.message}`, "error");
        const message: WorkerToMainMessage = {
          type: "sendevent",
          kind: "error",
          message: `WASM decoder error: ${err.message}`,
          idx: pipeline.idx,
          uid: uidCounter++,
        };
        self.postMessage(message);
      };

      pipeline.wasmDecoder = wasmDec;
      pipeline.configured = true;
      log(`WASM video decoder configured: ${codecString}`);
      drainInputQueue(pipeline);
      return;
    } catch (err) {
      log(`WASM fallback failed for ${codecString}: ${err} — trying native anyway`, "warn");
    }
  }

  // Native WebCodecs path
  log(`Configuring video decoder: ${config.codec}`);

  const decoder = new VideoDecoder({
    output: (frame: VideoFrame) => handleDecodedFrame(pipeline, frame),
    error: (err: DOMException) => handleDecoderError(pipeline, err),
  });

  decoder.configure(config as VideoDecoderConfig);
  pipeline.decoder = decoder;
  pipeline.lastDecoderConfig = config as VideoDecoderConfig;

  log(`Video decoder configured: ${config.codec}`);

  drainInputQueue(pipeline);
}

/**
 * Emit a WASM-decoded YUV frame to the main thread for WebGL rendering.
 * Transfers Uint8Array buffers for zero-copy.
 */
function emitWasmFrame(pipeline: PipelineState, output: WasmDecodedOutput): void {
  pipeline.stats.framesDecoded++;
  pipeline.stats.framesOut++;
  pipeline.stats.lastOutputTimestamp = output.timestamp;
  frameTiming.decoded = performance.now() * 1000;
  frameTiming.out = frameTiming.decoded;

  const msg: TransferYUVMessage = {
    type: "transferyuv",
    idx: pipeline.idx,
    timestamp: output.timestamp,
    y: output.planes.y,
    u: output.planes.u,
    v: output.planes.v,
    width: output.planes.width,
    height: output.planes.height,
    format: output.planes.format,
    colorPrimaries: output.colorPrimaries,
    transferFunction: output.transferFunction,
    uid: uidCounter++,
  };

  // Transfer the ArrayBuffer backing each plane for zero-copy
  self.postMessage(msg, {
    transfer: [output.planes.y.buffer, output.planes.u.buffer, output.planes.v.buffer],
  });

  // Send timeupdate
  const timeMsg: WorkerToMainMessage = {
    type: "sendevent",
    kind: "timeupdate",
    idx: pipeline.idx,
    time: output.timestamp / 1e6,
    uid: uidCounter++,
  };
  self.postMessage(timeMsg);
}

/**
 * Map MistServer audio codec names to WebCodecs-compatible codec strings
 * Per W3C AAC WebCodecs Registration: https://www.w3.org/TR/webcodecs-aac-codec-registration/
 */
function mapAudioCodec(codec: string, codecstring?: string): string {
  // If we have a full codec string like "mp4a.40.2", use it
  if (codecstring && codecstring.startsWith("mp4a.")) {
    return codecstring;
  }

  // Map common MistServer codec names to WebCodecs codec strings
  const normalized = codec.toLowerCase();
  switch (normalized) {
    case "aac":
    case "mp4a":
      return "mp4a.40.2"; // AAC-LC
    case "mp3":
      return "mp3";
    case "opus":
      return "opus";
    case "flac":
      return "flac";
    case "vorbis":
      return "vorbis";
    case "ac3":
    case "ac-3":
      return "ac-3";
    case "pcm_s16le":
    case "pcm_s32le":
    case "pcm_f32le":
      return "pcm-" + normalized.replace("pcm_", "").replace("le", "-le");
    default:
      log(`Unknown audio codec: ${codec}, trying as-is`);
      return codecstring || codec;
  }
}

function configureAudioDecoder(pipeline: PipelineState, description?: Uint8Array): void {
  const track = pipeline.track;

  const codec = mapAudioCodec(track.codec, track.codecstring);
  log(`Audio codec mapping: ${track.codec} -> ${codec}`);

  const config: AudioDecoderInit = {
    codec,
    sampleRate: track.rate || 48000,
    numberOfChannels: track.channels || 2,
  };

  if (description && description.byteLength > 0) {
    config.description = description;
  }

  const decoder = new AudioDecoder({
    output: (data: AudioData) => handleDecodedFrame(pipeline, data),
    error: (err: DOMException) => handleDecoderError(pipeline, err),
  });

  decoder.configure(config as AudioDecoderConfig);
  pipeline.decoder = decoder;
  pipeline.lastDecoderConfig = config as AudioDecoderConfig;

  log(
    `Audio decoder configured: ${config.codec} ${config.sampleRate}Hz ${config.numberOfChannels}ch`
  );
}

function handleDecodedFrame(pipeline: PipelineState, frame: VideoFrame | AudioData): void {
  if (pipeline.closed) {
    frame.close();
    return;
  }

  const now = performance.now() * 1000; // Convert to microseconds
  const timestamp = frame.timestamp ?? 0;

  pipeline.stats.framesDecoded++;
  frameTiming.decoded = now;

  // Log first few decoded frames
  if (pipeline.stats.framesDecoded <= 3) {
    const frameType = pipeline.track.type;
    const extraInfo =
      frameType === "audio"
        ? ` (${(frame as AudioData).numberOfFrames} samples, ${(frame as AudioData).sampleRate}Hz)`
        : ` (${(frame as VideoFrame).displayWidth}x${(frame as VideoFrame).displayHeight})`;
    log(
      `Decoded ${frameType} frame ${pipeline.stats.framesDecoded} for track ${pipeline.idx}: ts=${timestamp}μs${extraInfo}`
    );
  }

  // Add to output queue for scheduled release
  pipeline.outputQueue.push({
    frame,
    timestamp,
    decodedAt: performance.now(),
  });

  // Decoder just freed capacity — drain any backpressured chunks from inputQueue
  drainInputQueue(pipeline);

  // Try to output frames
  processOutputQueue(pipeline);
}

function handleDecoderError(pipeline: PipelineState, err: DOMException): void {
  log(`Decoder error on track ${pipeline.idx}: ${err.name}: ${err.message}`, "error");
  log(
    `  Last chunk info: type=${pipeline.stats.lastChunkType}, size=${pipeline.stats.lastChunkSize}, first bytes=[${pipeline.stats.lastChunkBytes}]`,
    "error"
  );

  // Per rawws.js: reset the pipeline after decoder error
  // This clears queues and recreates the decoder if needed
  resetPipelineAfterError(pipeline);

  const message: WorkerToMainMessage = {
    type: "sendevent",
    kind: "error",
    message: `Decoder error: ${err.message}`,
    idx: pipeline.idx,
    uid: uidCounter++,
  };
  self.postMessage(message);
}

/**
 * Reset pipeline after a decoder error.
 * Matches upstream webcodecsworker.js pipeline.reset():
 * - Closed decoder → recreate from lastDecoderConfig (rate-limited to 1s)
 * - Open decoder → reset() + reconfigure()
 * - Set droppingUntilKeyframe so we wait for a clean reference frame
 */
function resetPipelineAfterError(pipeline: PipelineState): void {
  // Clear queues
  pipeline.inputQueue = [];
  for (const entry of pipeline.outputQueue) {
    entry.frame.close();
  }
  pipeline.outputQueue = [];

  const now = performance.now();

  // Close/null the old decoder
  if (pipeline.decoder) {
    if (pipeline.decoder.state !== "closed") {
      try {
        pipeline.decoder.close();
      } catch {
        // ignore
      }
    }
    pipeline.decoder = null;
  }

  // Auto-recover: recreate decoder from stored config (upstream pipeline.reset() pattern)
  if (pipeline.lastDecoderConfig) {
    // Rate limit: upstream throttles hard resets to 1s minimum
    if (now - pipeline.lastResetTime < 1000) {
      log(
        `Rate-limiting reset for track ${pipeline.idx} (last reset ${Math.round(now - pipeline.lastResetTime)}ms ago)`
      );
      pipeline.configured = false;
      pipeline.droppingUntilKeyframe = false;
      // Schedule a delayed retry
      setTimeout(() => {
        if (pipeline.closed) return;
        resetPipelineAfterError(pipeline);
      }, 1000);
      return;
    }

    pipeline.lastResetTime = now;

    try {
      if (pipeline.track.type === "video") {
        const decoder = new VideoDecoder({
          output: (frame: VideoFrame) => handleDecodedFrame(pipeline, frame),
          error: (err: DOMException) => handleDecoderError(pipeline, err),
        });
        decoder.configure(pipeline.lastDecoderConfig as VideoDecoderConfig);
        pipeline.decoder = decoder;
      } else {
        const decoder = new AudioDecoder({
          output: (data: AudioData) => handleDecodedFrame(pipeline, data),
          error: (err: DOMException) => handleDecoderError(pipeline, err),
        });
        decoder.configure(pipeline.lastDecoderConfig as AudioDecoderConfig);
        pipeline.decoder = decoder;
      }

      pipeline.configured = true;
      pipeline.droppingUntilKeyframe = true;
      log(`Auto-recovered decoder for track ${pipeline.idx} (waiting for keyframe)`);
      return;
    } catch (e) {
      log(`Failed to auto-recover decoder for track ${pipeline.idx}: ${e}`, "warn");
      pipeline.decoder = null;
    }
  }

  // Fallback: no stored config or recovery failed
  pipeline.configured = false;
  pipeline.droppingUntilKeyframe = false;
}

// ============================================================================
// Frame Input/Output
// ============================================================================

/**
 * Drain chunks from inputQueue into the decoder, respecting backpressure.
 *
 * Feeds queued chunks to the decoder while decodeQueueSize <= threshold.
 * Called after decoder setup (flush all queued during async config) and
 * after each decoded frame output (decoder freed a slot).
 *
 * This replaces the old "drop when pressured" model with flow control:
 * chunks are buffered locally instead of dropped, preserving the H.264
 * reference chain and avoiding moshing artifacts.
 */
function drainInputQueue(pipeline: PipelineState): void {
  if (pipeline.inputQueue.length === 0) return;
  if (pipeline.closed) return;
  // Don't drain into a full output queue — wait for scheduler to catch up
  if (pipeline.outputQueue.length >= MAX_OUTPUT_QUEUE) return;

  const decoder = pipeline.decoder;
  const isWasm = !!pipeline.wasmDecoder;

  let fed = 0;
  while (pipeline.inputQueue.length > 0) {
    // WASM decoder: no queue pressure concept, feed all
    // Native decoder: respect queue threshold
    if (!isWasm && decoder && decoder.decodeQueueSize > MAX_DECODER_QUEUE_SIZE) {
      break;
    }
    // Stop draining if output queue hit capacity
    if (pipeline.outputQueue.length >= MAX_OUTPUT_QUEUE) break;
    const chunk = pipeline.inputQueue.shift()!;
    decodeChunk(pipeline, chunk);
    fed++;
  }

  if (fed > 0) {
    logVerbose(
      `Drained ${fed} chunks from inputQueue for track ${pipeline.idx} (${pipeline.inputQueue.length} remaining)`
    );
  }
}

function handleReceive(msg: MainToWorkerMessage & { type: "receive" }): void {
  const { idx, chunk } = msg;
  const pipeline = pipelines.get(idx);

  if (!pipeline) {
    logVerbose(`Received chunk for unknown pipeline ${idx}`);
    return;
  }

  if (!pipeline.configured || (!pipeline.decoder && !pipeline.wasmDecoder)) {
    // Queue for later
    pipeline.inputQueue.push(chunk);
    logVerbose(
      `Queued chunk for track ${idx} (configured=${pipeline.configured}, decoder=${!!pipeline.decoder}, wasm=${!!pipeline.wasmDecoder})`
    );
    return;
  }

  // If paused and output queue is saturated, queue input to preserve per-frame stepping
  if (frameTiming.paused && pipeline.outputQueue.length >= MAX_PAUSED_OUTPUT_QUEUE) {
    pipeline.inputQueue.push(chunk);
    if (pipeline.inputQueue.length > MAX_PAUSED_INPUT_QUEUE) {
      pipeline.inputQueue.splice(0, pipeline.inputQueue.length - MAX_PAUSED_INPUT_QUEUE);
      logVerbose(`Trimmed paused input queue for track ${idx} to ${MAX_PAUSED_INPUT_QUEUE}`);
    }
    return;
  }

  if (pipeline.stats.framesIn < 3) {
    log(
      `Received chunk ${pipeline.stats.framesIn} for track ${idx}: type=${chunk.type}, ts=${chunk.timestamp / 1000}ms, size=${chunk.data.byteLength}`
    );
  }

  // Output queue backpressure: hold chunks in inputQueue when decoded frames
  // are piling up (e.g. post-seek fast-forward). Encoded chunks are tiny vs
  // decoded VideoFrame objects, so this caps memory without dropping data.
  if (pipeline.outputQueue.length >= MAX_OUTPUT_QUEUE) {
    pipeline.inputQueue.push(chunk);
    return;
  }

  // Sustained overload: drop until keyframe resets reference chain
  if (pipeline.droppingUntilKeyframe) {
    if (chunk.type === "key") {
      pipeline.droppingUntilKeyframe = false;
      log(`Keyframe received — resuming decode for track ${idx}`);
      pipeline.inputQueue = [];
      decodeChunk(pipeline, chunk);
    } else {
      pipeline.stats.framesDropped++;
      _totalFramesDropped++;
    }
    return;
  }

  // Backpressure: queue locally when decoder is busy, drain via output callback
  if (isDecoderBusy(pipeline)) {
    pipeline.inputQueue.push(chunk);
    if (pipeline.inputQueue.length > MAX_INPUT_QUEUE_BEFORE_DROP) {
      pipeline.droppingUntilKeyframe = true;
      const dropped = _dropToNextKeyframe(pipeline);
      log(
        `Input queue overflow on track ${idx} (${pipeline.inputQueue.length + dropped}) — dropped ${dropped}`,
        "warn"
      );
    }
    return;
  }

  decodeChunk(pipeline, chunk);
}

/**
 * Check if decoder's internal queue is at capacity.
 * When true, callers should buffer chunks in inputQueue instead of submitting.
 */
function isDecoderBusy(pipeline: PipelineState): boolean {
  if (pipeline.wasmDecoder) return false;
  if (!pipeline.decoder) return false;
  // Audio decodes fast and has no inter-frame dependencies — don't throttle
  if (pipeline.track.type === "audio") return false;
  const queueSize = pipeline.decoder.decodeQueueSize;
  pipeline.stats.decoderQueueSize = queueSize;
  return queueSize > MAX_DECODER_QUEUE_SIZE;
}

/**
 * Drop all frames up to the next keyframe in the input queue
 * Called when decoder is severely backed up
 */
function _dropToNextKeyframe(pipeline: PipelineState): number {
  if (pipeline.inputQueue.length === 0) return 0;

  // Find next keyframe in queue
  const keyframeIdx = pipeline.inputQueue.findIndex((c) => c.type === "key");

  if (keyframeIdx <= 0) {
    // No keyframe or keyframe is first - nothing to drop
    return 0;
  }

  // Drop all frames before keyframe
  const dropped = pipeline.inputQueue.splice(0, keyframeIdx);
  pipeline.stats.framesDropped += dropped.length;
  _totalFramesDropped += dropped.length;

  log(`Dropped ${dropped.length} frames to next keyframe`, "warn");

  return dropped.length;
}

function decodeChunk(
  pipeline: PipelineState,
  chunk: { type: "key" | "delta"; timestamp: number; data: Uint8Array }
): void {
  if (pipeline.closed) return;

  const now = performance.now() * 1000;
  frameTiming.in = now;
  pipeline.stats.framesIn++;
  pipeline.stats.lastInputTimestamp = chunk.timestamp;

  try {
    // Handle JPEG via ImageDecoder (Phase 2C)
    const codec = pipeline.track.codec;
    if (codec === "JPEG" || codec.toLowerCase() === "jpeg") {
      decodeJpegFrame(pipeline, chunk);
      return;
    }

    // WASM fallback path: route to WasmVideoDecoder if active
    if (pipeline.wasmDecoder && pipeline.track.type === "video") {
      const wasmDec = pipeline.wasmDecoder as WasmVideoDecoder;
      pipeline.stats.lastChunkType = chunk.type;
      pipeline.stats.lastChunkSize = chunk.data.byteLength;
      wasmDec.decode(chunk.data, chunk.type === "key", chunk.timestamp);
      return;
    }

    if (!pipeline.decoder) return;

    // chunk.timestamp is ALREADY in microseconds (converted by main thread via getPresentationTimestamp)
    const timestampUs = chunk.timestamp;

    // Record debug info before decode (for error diagnosis)
    pipeline.stats.lastChunkType = chunk.type;
    pipeline.stats.lastChunkSize = chunk.data.byteLength;
    // Show first 8 bytes to identify format (Annex B starts 0x00 0x00 0x00 0x01, AVCC starts with length)
    const firstBytes = Array.from(chunk.data.slice(0, 8))
      .map((b) => "0x" + b.toString(16).padStart(2, "0"))
      .join(" ");
    pipeline.stats.lastChunkBytes = firstBytes;

    if (pipeline.track.type === "video") {
      let frameData = chunk.data;

      // Runtime Annex B → AVCC conversion: MistServer ws/video/raw sends AVCC init
      // (AVCDecoderConfigurationRecord) but Annex B frame data. The AVCC description
      // makes the decoder expect AVCC frames, so we convert at decode time.
      if (pipeline.annexBConvert === undefined && chunk.type === "key") {
        pipeline.annexBConvert = hasAnnexBStartCode(frameData);
        if (pipeline.annexBConvert) {
          log(`Detected Annex B frame data on track ${pipeline.idx} — enabling AVCC conversion`);
        }
      }
      if (pipeline.annexBConvert) {
        frameData = annexBFrameToAvcc(frameData);
      }

      const encodedChunk = new EncodedVideoChunk({
        type: chunk.type,
        timestamp: timestampUs,
        data: frameData,
      });

      const decoder = pipeline.decoder as VideoDecoder;
      if (pipeline.stats.framesIn <= 3) {
        const first16 = Array.from(chunk.data.slice(0, 16))
          .map((b) => "0x" + b.toString(16).padStart(2, "0"))
          .join(" ");
        log(
          `Calling decode() for track ${pipeline.idx}: state=${decoder.state}, queueSize=${decoder.decodeQueueSize}, chunk type=${chunk.type}, ts=${timestampUs}μs`
        );
        log(
          `  First 16 bytes (raw): ${first16}${pipeline.annexBConvert ? " [converted to AVCC]" : ""}`
        );
      }

      decoder.decode(encodedChunk);

      if (pipeline.stats.framesIn <= 3) {
        log(`After decode() for track ${pipeline.idx}: queueSize=${decoder.decodeQueueSize}`);
      }
    } else if (pipeline.track.type === "audio") {
      // Audio chunks are always treated as "key" frames - per MistServer rawws.js line 1127
      // Audio codecs don't use inter-frame dependencies like video does
      const encodedChunk = new EncodedAudioChunk({
        type: "key",
        timestamp: timestampUs,
        data: chunk.data,
      });
      (pipeline.decoder as AudioDecoder).decode(encodedChunk);
    }

    // Update decoder queue size (decoder may have been nullified by error callback)
    if (pipeline.decoder) {
      pipeline.stats.decoderQueueSize = pipeline.decoder.decodeQueueSize;
    }

    logVerbose(
      `Decoded chunk ${chunk.type} @ ${chunk.timestamp / 1000}ms for track ${pipeline.idx}`
    );
  } catch (err) {
    log(`Decode error on track ${pipeline.idx}: ${err}`, "error");
  }
}

/**
 * Decode JPEG frame using ImageDecoder API (Phase 2C)
 * ImageDecoder is simpler than VideoDecoder for still images
 */
async function decodeJpegFrame(
  pipeline: PipelineState,
  chunk: { type: "key" | "delta"; timestamp: number; data: Uint8Array }
): Promise<void> {
  if (pipeline.closed) return;

  // Check if ImageDecoder is available
  if (typeof ImageDecoder === "undefined") {
    log("ImageDecoder not available - JPEG streams not supported", "error");
    return;
  }

  try {
    // Create ImageDecoder for this frame
    const decoder = new ImageDecoder({
      type: "image/jpeg",
      data: chunk.data,
    });

    // Decode the frame - single decode call per MistServer rawws.js line 1069
    const result = await decoder.decode({ frameIndex: 0 });

    // Create VideoFrame from ImageBitmap with the correct timestamp
    const frame = new VideoFrame(result.image, {
      timestamp: chunk.timestamp, // Preserve original timestamp
    });

    // Clean up ImageDecoder resources
    result.image.close();
    decoder.close();

    // Pass frame through normal output handling
    handleDecodedFrame(pipeline, frame);

    logVerbose(`Decoded JPEG frame @ ${chunk.timestamp / 1000}ms for track ${pipeline.idx}`);
  } catch (err) {
    log(`JPEG decode error on track ${pipeline.idx}: ${err}`, "error");
  }
}

function processOutputQueue(pipeline: PipelineState): void {
  if (frameTiming.paused) {
    return;
  }
  // Check if pipeline is closed (e.g., player destroyed) - clean up queued frames
  if (pipeline.closed) {
    while (pipeline.outputQueue.length > 0) {
      const entry = pipeline.outputQueue.shift()!;
      entry.frame.close();
    }
    return;
  }

  if (pipeline.outputQueue.length === 0) return;

  // Need either a writer (stream mode) or directTransfer enabled
  if (!pipeline.writer && !pipeline.directTransfer) {
    log(
      `Cannot output: no writer and no directTransfer for track ${pipeline.idx} (queue has ${pipeline.outputQueue.length} frames)`,
      "warn"
    );
    return;
  }

  const now = performance.now();

  // Sort output queue by timestamp - MistServer can send frames out of order
  // This is more robust than just swapping adjacent frames
  if (pipeline.outputQueue.length > 1) {
    const wasSorted = pipeline.outputQueue.every(
      (entry, i, arr) => i === 0 || arr[i - 1].timestamp <= entry.timestamp
    );
    if (!wasSorted) {
      pipeline.outputQueue.sort((a, b) => a.timestamp - b.timestamp);
      logVerbose(
        `Sorted ${pipeline.outputQueue.length} frames in output queue for track ${pipeline.idx}`
      );
    }
  }

  // Buffer warmup - wait for buffer to build before starting output to prevent initial jitter
  // With per-track baseTime, each track can start independently once it has enough buffer
  if (!warmupComplete) {
    // Track when warmup started
    if (warmupStartTime === null) {
      warmupStartTime = now;
      log(`Starting buffer warmup (target: ${WARMUP_BUFFER_MS}ms)`);
    }

    const elapsed = now - warmupStartTime;

    // Calculate buffer from timestamp range in queue
    if (pipeline.outputQueue.length >= 2) {
      const oldest = pipeline.outputQueue[0].timestamp / 1000; // Convert to ms
      const newest = pipeline.outputQueue[pipeline.outputQueue.length - 1].timestamp / 1000;
      const bufferMs = newest - oldest;

      // Complete warmup when we have enough buffer OR timeout
      if (bufferMs >= WARMUP_BUFFER_MS || elapsed >= WARMUP_TIMEOUT_MS) {
        warmupComplete = true;
        log(
          `Buffer warmup complete: ${bufferMs.toFixed(0)}ms buffer, ${pipeline.outputQueue.length} frames queued (track ${pipeline.idx})`
        );
      } else {
        // Not ready yet - schedule another check
        setTimeout(() => processOutputQueue(pipeline), 10);
        return;
      }
    } else {
      // Not enough frames yet - schedule another check
      if (elapsed >= WARMUP_TIMEOUT_MS) {
        warmupComplete = true;
        log(
          `Buffer warmup timeout - starting with ${pipeline.outputQueue.length} frame(s) (track ${pipeline.idx})`
        );
      } else {
        setTimeout(() => processOutputQueue(pipeline), 10);
        return;
      }
    }
  }

  // Skip pre-seek frames (upstream webcodecsworker.js lines 470-497)
  // Server sends data starting from the keyframe BEFORE the seek target,
  // so we discard frames until we reach the target timestamp.
  if (frameTiming.seeking !== false) {
    while (pipeline.outputQueue.length > 0) {
      const entry = pipeline.outputQueue[0];
      if (entry.timestamp < frameTiming.seeking) {
        pipeline.outputQueue.shift();
        entry.frame.close();
        pipeline.stats.framesDropped++;
        logVerbose(
          `Skipped pre-seek frame ${entry.timestamp} < ${frameTiming.seeking} (track ${pipeline.idx})`
        );
      } else {
        if (seekGateTrackIdx === null) {
          seekGateTrackIdx = selectSeekGateTrack();
        }

        if (seekGateTrackIdx !== null && pipeline.idx !== seekGateTrackIdx) {
          // Keep this frame queued and wait for the gate track (prefer video)
          // to reach target so we don't complete seek on audio first.
          setTimeout(() => processOutputQueue(pipeline), 10);
          return;
        }

        log(
          `Reached seek target [${(frameTiming.seeking / 1e6).toFixed(3)}s] on track ${pipeline.idx}: resuming normal playback`
        );
        frameTiming.seeking = false;
        seekGateTrackIdx = null;
        self.postMessage({ type: "sendevent", kind: "seeked", uid: uidCounter++ });
        break;
      }
    }
    if (frameTiming.seeking !== false) return; // still waiting for target frame
  }

  // Process all frames that are ready
  while (pipeline.outputQueue.length > 0) {
    const entry = pipeline.outputQueue[0];

    // Frame timing (per-track baseTime for A/V with different timestamp bases)
    const schedule = shouldOutputFrame(pipeline, entry, now);

    if (!schedule.shouldOutput) {
      // Schedule next check
      if (schedule.checkDelayMs > 0) {
        setTimeout(() => processOutputQueue(pipeline), schedule.checkDelayMs);
      }
      break;
    }

    // Output this frame
    pipeline.outputQueue.shift();
    outputFrame(pipeline, entry);
  }
}

function shouldOutputFrame(
  pipeline: PipelineState,
  entry: DecodedFrame,
  now: number
): { shouldOutput: boolean; earliness: number; checkDelayMs: number } {
  const trackIdx = pipeline.idx;

  if (frameTiming.seeking !== false) {
    // Reached seek target — resume normal playback
    trackBaseTimes.delete(trackIdx);
    return { shouldOutput: true, earliness: 0, checkDelayMs: 0 };
  }

  // Frame timestamp in milliseconds (entry.timestamp is in microseconds)
  const frameTimeMs = entry.timestamp / 1000;
  const speed = frameTiming.speed.combined;

  // Per-track baseTime to handle different timestamp bases for audio/video
  const baseTime = getTrackBaseTime(trackIdx, frameTimeMs, now);

  // Calculate target wall-clock time for this frame (per rawws.js line 872)
  // targetTime = baseTime + frameTimeMs / speed
  const targetTime = baseTime + frameTimeMs / speed;

  // How early/late is this frame? Positive = too early, negative = late
  const delay = targetTime - now;

  logVerbose(
    `Frame timing: track=${trackIdx} frame=${frameTimeMs.toFixed(0)}ms, target=${targetTime.toFixed(0)}, now=${now.toFixed(0)}, delay=${delay.toFixed(1)}ms`
  );

  // Output immediately if ready or late (per rawws.js line 889: delay <= 2)
  if (delay <= 2) {
    return { shouldOutput: true, earliness: -delay, checkDelayMs: 0 };
  }

  // Schedule check for when frame should be ready
  return { shouldOutput: false, earliness: -delay, checkDelayMs: Math.max(1, Math.floor(delay)) };
}

function outputFrame(
  pipeline: PipelineState,
  entry: DecodedFrame,
  options?: { skipHistory?: boolean }
): void {
  if (pipeline.closed) {
    entry.frame.close();
    return;
  }

  const now = performance.now() * 1000;
  frameTiming.out = now;
  pipeline.stats.framesOut++;
  pipeline.stats.lastOutputTimestamp = entry.timestamp;

  if (pipeline.stats.framesOut <= 3) {
    log(
      `Output frame ${pipeline.stats.framesOut} for track ${pipeline.idx}: ts=${entry.timestamp}μs`
    );
  }

  // Store history for frame stepping (video only)
  if (pipeline.track.type === "video" && !options?.skipHistory) {
    pushFrameHistory(pipeline, entry.frame as VideoFrame, entry.timestamp);
  }

  // Direct transfer mode: send frame to main thread via postMessage for WebGL/AudioWorklet rendering
  if (pipeline.directTransfer) {
    const msg: TransferFrameMessage = {
      type: "transferframe",
      idx: pipeline.idx,
      trackType: pipeline.track.type as "video" | "audio",
      frame: entry.frame,
      timestamp: entry.timestamp,
      uid: uidCounter++,
    };
    try {
      // Transfer ownership of the frame to main thread (zero-copy)
      self.postMessage(msg, { transfer: [entry.frame as unknown as Transferable] });
    } catch (err) {
      // Firefox may not support AudioData/VideoFrame as Transferable — fall back to structured clone
      log(`Transfer failed for ${pipeline.track.type} frame, using clone: ${err}`, "warn");
      try {
        self.postMessage(msg);
        entry.frame.close();
      } catch {
        entry.frame.close();
      }
    }

    // Send timeupdate
    const timeMsg: WorkerToMainMessage = {
      type: "sendevent",
      kind: "timeupdate",
      idx: pipeline.idx,
      time: entry.timestamp / 1e6,
      uid: uidCounter++,
    };
    self.postMessage(timeMsg);
    return;
  }

  // Stream mode: write to WritableStream (MediaStreamTrackGenerator)
  if (!pipeline.writer) {
    entry.frame.close();
    return;
  }

  pipeline.writer
    .write(entry.frame)
    .then(() => {
      const message: WorkerToMainMessage = {
        type: "sendevent",
        kind: "timeupdate",
        idx: pipeline.idx,
        time: entry.timestamp / 1e6,
        uid: uidCounter++,
      };
      self.postMessage(message);
    })
    .catch((err: Error) => {
      const errStr = String(err);
      if (errStr.includes("Stream closed") || errStr.includes("InvalidStateError")) {
        pipeline.closed = true;
      } else {
        log(`Failed to write frame: ${err}`, "error");
      }
      try {
        entry.frame.close();
      } catch {
        // Frame may already be detached/closed
      }
    });
}

// ============================================================================
// Track Generator / Writable Stream
// ============================================================================

function handleSetWritable(msg: MainToWorkerMessage & { type: "setwritable" }): void {
  const { idx, writable, uid } = msg;
  const pipeline = pipelines.get(idx);

  if (!pipeline) {
    log(`Cannot set writable: pipeline ${idx} not found`, "error");
    sendError(uid, idx, "Pipeline not found");
    return;
  }

  pipeline.writable = writable;
  pipeline.writer = writable.getWriter();

  log(`Writable stream set for track ${idx}`);

  // Process any queued frames
  processOutputQueue(pipeline);

  // Notify main thread track is ready
  const message: WorkerToMainMessage = {
    type: "addtrack",
    idx,
    uid,
    status: "ok",
  };
  self.postMessage(message);
}

function handleCreateGenerator(msg: MainToWorkerMessage & { type: "creategenerator" }): void {
  const { idx, uid } = msg;
  const pipeline = pipelines.get(idx);

  if (!pipeline) {
    log(`Cannot create generator: pipeline ${idx} not found`, "error");
    sendError(uid, idx, "Pipeline not found");
    return;
  }

  // Safari: VideoTrackGenerator is available in worker (not MediaStreamTrackGenerator)
  // Reference: webcodecsworker.js line 852-863
  // @ts-ignore - VideoTrackGenerator may not be in types
  if (typeof VideoTrackGenerator !== "undefined") {
    if (pipeline.track.type === "video") {
      // Safari video: use VideoTrackGenerator
      // @ts-ignore
      const generator = new VideoTrackGenerator();
      pipeline.writable = generator.writable;
      pipeline.writer = generator.writable.getWriter();

      // Send track back to main thread
      const message: WorkerToMainMessage = {
        type: "addtrack",
        idx,
        track: generator.track,
        uid,
        status: "ok",
      };
      // @ts-ignore - transferring MediaStreamTrack
      self.postMessage(message, [generator.track]);
      log(`Created VideoTrackGenerator for track ${idx} (Safari video)`);
    } else if (pipeline.track.type === "audio") {
      // Safari audio: relay frames to main thread via postMessage
      // Reference: webcodecsworker.js line 773-800
      // Main thread creates the audio generator, we just send frames
      pipeline.writer = {
        write: (frame: AudioData): Promise<void> => {
          return new Promise((resolve, reject) => {
            const frameUid = uidCounter++;
            // Set up listener for response
            const timeoutId = setTimeout(() => {
              reject(new Error("writeframe timeout"));
            }, 5000);

            const handler = (e: MessageEvent) => {
              const msg = e.data;
              if (msg.type === "writeframe" && msg.idx === idx && msg.uid === frameUid) {
                clearTimeout(timeoutId);
                self.removeEventListener("message", handler);
                if (msg.status === "ok") {
                  resolve();
                } else {
                  reject(new Error(msg.error || "writeframe failed"));
                }
              }
            };
            self.addEventListener("message", handler);

            // Send frame to main thread (transfer AudioData)
            const msg = {
              type: "writeframe",
              idx,
              frame,
              uid: frameUid,
            };
            self.postMessage(msg, { transfer: [frame] });
          });
        },
        close: () => Promise.resolve(),
      } as WritableStreamDefaultWriter<AudioData>;

      // Notify main thread to set up audio generator
      const message: WorkerToMainMessage = {
        type: "addtrack",
        idx,
        uid,
        status: "ok",
      };
      self.postMessage(message);
      log(`Set up frame relay for track ${idx} (Safari audio)`);
    }
    // @ts-ignore - MediaStreamTrackGenerator may not be in standard types
  } else if (typeof MediaStreamTrackGenerator !== "undefined") {
    // Chrome/Edge: use MediaStreamTrackGenerator in worker
    // @ts-ignore
    const generator = new MediaStreamTrackGenerator({ kind: pipeline.track.type });
    pipeline.writable = generator.writable;
    pipeline.writer = generator.writable.getWriter();

    // Send track back to main thread
    const message: WorkerToMainMessage = {
      type: "addtrack",
      idx,
      track: generator,
      uid,
      status: "ok",
    };
    // @ts-ignore - transferring MediaStreamTrack
    self.postMessage(message, [generator]);
    log(`Created MediaStreamTrackGenerator for track ${idx}`);
  } else {
    log("Neither VideoTrackGenerator nor MediaStreamTrackGenerator available in worker", "warn");
    sendError(uid, idx, "No track generator available");
  }
}

// ============================================================================
// Seeking & Timing
// ============================================================================

function handleSeek(msg: MainToWorkerMessage & { type: "seek" }): void {
  const { seekTime, uid } = msg;

  log(`Seek to ${seekTime}ms`);
  // Store seek target in microseconds — frames before this are skipped during catchup.
  // Matches upstream: frameTiming.seeking = seekTo * 1e3
  frameTiming.seeking = seekTime * 1000;
  frameTiming.out = seekTime * 1000;
  seekGateTrackIdx = selectSeekGateTrack();
  resetBaseTime();

  // Reset warmup state - need to rebuild buffer after seek
  warmupComplete = false;
  warmupStartTime = null;

  // Flush all pipeline queues
  for (const pipeline of pipelines.values()) {
    flushPipeline(pipeline);
  }

  sendAck(uid);
}

function flushPipeline(pipeline: PipelineState): void {
  pipeline.inputQueue = [];

  for (const entry of pipeline.outputQueue) {
    entry.frame.close();
  }
  pipeline.outputQueue = [];

  // WASM decoder: no reset/configure cycle needed
  if (pipeline.wasmDecoder) {
    return;
  }

  if (pipeline.decoder && pipeline.decoder.state !== "closed") {
    try {
      pipeline.decoder.reset();
      // Reconfigure immediately — reset() puts decoder in "unconfigured" state.
      // Matches upstream rawws.js: pipeline.reset() → decoder.reset() + configureDecoder()
      if (pipeline.lastDecoderConfig) {
        if (pipeline.track.type === "video") {
          (pipeline.decoder as VideoDecoder).configure(
            pipeline.lastDecoderConfig as VideoDecoderConfig
          );
        } else {
          (pipeline.decoder as AudioDecoder).configure(
            pipeline.lastDecoderConfig as AudioDecoderConfig
          );
        }
        // Require keyframe before accepting delta frames after reconfigure.
        // In-flight delta frames from the old position arrive after flush;
        // feeding them to the decoder causes "A key frame is required" error.
        // Matches upstream VideoPipeline wantKey pattern.
        pipeline.droppingUntilKeyframe = true;
        log(`Pipeline ${pipeline.idx} reconfigured after flush (waiting for keyframe)`);
        return;
      }
    } catch (e) {
      log(`Pipeline ${pipeline.idx} flush error: ${e}`, "warn");
    }
  }

  // Only mark unconfigured if reconfigure failed or no stored config
  pipeline.configured = false;
}

function handleFrameTiming(msg: MainToWorkerMessage & { type: "frametiming" }): void {
  const { action, speed, tweak, uid } = msg;

  if (action === "setSpeed") {
    if (speed !== undefined) frameTiming.speed.main = speed;
    if (tweak !== undefined) frameTiming.speed.tweak = tweak;
    frameTiming.speed.combined = frameTiming.speed.main * frameTiming.speed.tweak;
    log(
      `Speed set to ${frameTiming.speed.combined} (main: ${frameTiming.speed.main}, tweak: ${frameTiming.speed.tweak})`
    );
  } else if (action === "setPaused") {
    frameTiming.paused = msg.paused === true;
    log(`Frame timing paused=${frameTiming.paused}`);
  } else if (action === "reset") {
    frameTiming.seeking = false;
    seekGateTrackIdx = null;
    log("Frame timing reset (seek complete)");
  }

  sendAck(uid);
}

function handleFrameStep(msg: MainToWorkerMessage & { type: "framestep" }): void {
  const { direction, uid } = msg;

  log(`FrameStep request dir=${direction} paused=${frameTiming.paused}`);

  if (!frameTiming.paused) {
    log(`FrameStep ignored (not paused)`);
    sendAck(uid);
    return;
  }

  const pipeline = getPrimaryVideoPipeline();
  if (!pipeline || !pipeline.writer || pipeline.closed) {
    log(`FrameStep ignored (pipeline missing or closed)`);
    sendAck(uid);
    return;
  }

  pipeline.frameHistory = pipeline.frameHistory ?? [];
  if (pipeline.historyCursor === null || pipeline.historyCursor === undefined) {
    alignHistoryCursorToLastOutput(pipeline);
  }
  log(
    `FrameStep pipeline idx=${pipeline.idx} outQueue=${pipeline.outputQueue.length} history=${pipeline.frameHistory.length} cursor=${pipeline.historyCursor}`
  );

  if (direction < 0) {
    const nextIndex = (pipeline.historyCursor ?? 0) - 1;
    if (nextIndex < 0 || pipeline.frameHistory.length === 0) {
      log(`FrameStep back: no history`);
      sendAck(uid);
      return;
    }
    pipeline.historyCursor = nextIndex;
    const entry = pipeline.frameHistory[nextIndex];
    const clone = entry ? cloneVideoFrame(entry.frame) : null;
    if (!clone) {
      log(`FrameStep back: failed to clone frame`);
      sendAck(uid);
      return;
    }
    log(`FrameStep back: output ts=${entry.timestamp}`);
    outputFrame(
      pipeline,
      { frame: clone, timestamp: entry.timestamp, decodedAt: performance.now() },
      { skipHistory: true }
    );
    sendAck(uid);
    return;
  }

  if (direction > 0) {
    // If we're stepping forward within history (after stepping back), use history
    const cursor = pipeline.historyCursor;
    if (cursor !== null && cursor !== undefined && cursor < pipeline.frameHistory.length - 1) {
      pipeline.historyCursor = cursor + 1;
      const entry = pipeline.frameHistory[pipeline.historyCursor!];
      const clone = entry ? cloneVideoFrame(entry.frame) : null;
      if (!clone) {
        log(`FrameStep forward: failed to clone frame`);
        sendAck(uid);
        return;
      }
      log(`FrameStep forward (history): output ts=${entry.timestamp}`);
      outputFrame(
        pipeline,
        { frame: clone, timestamp: entry.timestamp, decodedAt: performance.now() },
        { skipHistory: true }
      );
      sendAck(uid);
      return;
    }

    // Otherwise, output the next queued frame
    if (pipeline.outputQueue.length > 1) {
      const wasSorted = pipeline.outputQueue.every(
        (entry, i, arr) => i === 0 || arr[i - 1].timestamp <= entry.timestamp
      );
      if (!wasSorted) {
        pipeline.outputQueue.sort((a, b) => a.timestamp - b.timestamp);
      }
    }

    const lastTs = pipeline.stats.lastOutputTimestamp;
    let idx = pipeline.outputQueue.findIndex((e) => e.timestamp > lastTs);
    if (idx === -1 && pipeline.outputQueue.length > 0) idx = 0;
    if (idx === -1) {
      log(`FrameStep forward: no queued frame available`);
      sendAck(uid);
      return;
    }

    const entry = pipeline.outputQueue.splice(idx, 1)[0];
    log(`FrameStep forward (queue): output ts=${entry.timestamp}`);
    outputFrame(pipeline, entry);
    sendAck(uid);
    return;
  }

  sendAck(uid);
}

// ============================================================================
// Cleanup
// ============================================================================

function handleClose(msg: MainToWorkerMessage & { type: "close" }): void {
  const { idx, waitEmpty, uid } = msg;
  const pipeline = pipelines.get(idx);

  if (!pipeline) {
    sendAck(uid, idx);
    return;
  }

  if (waitEmpty && pipeline.outputQueue.length > 0) {
    // Wait for queue to drain
    const checkDrain = () => {
      if (pipeline.outputQueue.length === 0) {
        closePipeline(pipeline, uid);
      } else {
        setTimeout(checkDrain, 10);
      }
    };
    checkDrain();
  } else {
    closePipeline(pipeline, uid);
  }
}

function closePipeline(pipeline: PipelineState, uid: number): void {
  pipeline.closed = true;

  // Close decoder
  if (pipeline.decoder && pipeline.decoder.state !== "closed") {
    try {
      pipeline.decoder.close();
    } catch {
      // Ignore close errors
    }
  }

  // Destroy WASM decoder
  if (pipeline.wasmDecoder) {
    (pipeline.wasmDecoder as WasmVideoDecoder).destroy();
    pipeline.wasmDecoder = null;
  }

  // Close writer
  if (pipeline.writer) {
    try {
      pipeline.writer.close();
    } catch {
      // Ignore close errors
    }
  }

  // Clear queues
  for (const entry of pipeline.outputQueue) {
    entry.frame.close();
  }
  pipeline.outputQueue = [];
  pipeline.inputQueue = [];

  // Clean up per-track timing
  trackBaseTimes.delete(pipeline.idx);

  pipelines.delete(pipeline.idx);

  log(`Closed pipeline ${pipeline.idx}`);

  // Stop stats if no more pipelines
  if (pipelines.size === 0 && statsTimer) {
    clearInterval(statsTimer);
    statsTimer = null;
  }

  const message: WorkerToMainMessage = {
    type: "closed",
    idx: pipeline.idx,
    uid,
    status: "ok",
  };
  self.postMessage(message);
}

// ============================================================================
// Stats Reporting
// ============================================================================

function sendStats(): void {
  const pipelineStats: Record<number, PipelineStats> = {};

  for (const [idx, pipeline] of pipelines) {
    pipelineStats[idx] = {
      early: null, // Would need frame timing to calculate
      frameDuration: null,
      frames: {
        in: pipeline.stats.framesIn,
        decoded: pipeline.stats.framesDecoded,
        out: pipeline.stats.framesOut,
      },
      queues: {
        in: pipeline.inputQueue.length,
        decoder: pipeline.stats.decoderQueueSize,
        out: pipeline.outputQueue.length,
      },
      timing: {
        decoder: createFrameTrackerStats(),
        writable: createFrameTrackerStats(),
      },
    };
  }

  const message: WorkerToMainMessage = {
    type: "stats",
    stats: {
      frameTiming: {
        in: frameTiming.in,
        decoded: frameTiming.decoded,
        out: frameTiming.out,
        speed: { ...frameTiming.speed },
        seeking: frameTiming.seeking !== false,
        paused: frameTiming.paused,
      },
      pipelines: pipelineStats,
    },
    uid: uidCounter++,
  };

  self.postMessage(message);
}

function createFrameTrackerStats(): FrameTrackerStats {
  return {
    lastIn: undefined,
    lastOut: undefined,
    delay: undefined,
    delta: undefined,
    shift: undefined,
  };
}

// ============================================================================
// Response Helpers
// ============================================================================

function sendAck(uid: number, idx?: number): void {
  const message: WorkerToMainMessage = {
    type: "ack",
    uid,
    idx,
    status: "ok",
  };
  self.postMessage(message);
}

function sendError(uid: number, idx: number | undefined, error: string): void {
  const message: WorkerToMainMessage = {
    type: "ack",
    uid,
    idx,
    status: "error",
    error,
  };
  self.postMessage(message);
}

// ============================================================================
// Worker Initialization
// ============================================================================

log("WebCodecs decoder worker initialized");

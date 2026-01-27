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
} from "./types";
import type { PipelineStats, FrameTrackerStats } from "../types";

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

// Chrome-recommended decoder queue threshold
// Per Chrome WebCodecs best practices: drop when decodeQueueSize > 2
// This ensures decoder doesn't fall too far behind before corrective action
const MAX_DECODER_QUEUE_SIZE = 2;
const MAX_FRAME_HISTORY = 60;
const MAX_PAUSED_OUTPUT_QUEUE = 120;
const MAX_PAUSED_INPUT_QUEUE = 600;

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
      // Debug info for error diagnosis
      lastChunkType: "" as string,
      lastChunkSize: 0,
      lastChunkBytes: "" as string,
    },
    optimizeForLatency: opts.optimizeForLatency,
    payloadFormat: opts.payloadFormat || "avcc",
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

function configureVideoDecoder(pipeline: PipelineState, description?: Uint8Array): void {
  const track = pipeline.track;

  // Handle JPEG codec separately via ImageDecoder (Phase 2C)
  if (track.codec === "JPEG" || track.codec.toLowerCase() === "jpeg") {
    log("JPEG codec detected - will use ImageDecoder");
    pipeline.configured = true;
    // JPEG doesn't need a persistent decoder - each frame is decoded individually
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

  // Match reference rawws.js configOpts pattern:
  // codec, optimizeForLatency, description + hw acceleration hint
  const config: VideoDecoderInit = {
    codec: track.codecstring || track.codec.toLowerCase(),
    optimizeForLatency: pipeline.optimizeForLatency,
    hardwareAcceleration: "prefer-hardware",
  };

  // Pass description directly from WebSocket INIT data (per reference rawws.js line 1052)
  // For Annex B format (ws/video/h264), SPS/PPS comes inline in the bitstream - skip description
  if (pipeline.payloadFormat === "annexb") {
    log(`Annex B mode - SPS/PPS inline in bitstream, no description needed`);
  } else if (description && description.byteLength > 0) {
    config.description = description;
    log(`Configuring with description (${description.byteLength} bytes)`);
  } else {
    log(`No description provided - decoder may fail on H.264/HEVC`, "warn");
  }

  log(`Configuring video decoder: ${config.codec}`);

  const decoder = new VideoDecoder({
    output: (frame: VideoFrame) => handleDecodedFrame(pipeline, frame),
    error: (err: DOMException) => handleDecoderError(pipeline, err),
  });

  decoder.configure(config as VideoDecoderConfig);
  pipeline.decoder = decoder;

  log(`Video decoder configured: ${config.codec}`);
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
 * Reset pipeline after a decoder error
 * Per rawws.js: recreate decoder if closed, otherwise just reset
 */
function resetPipelineAfterError(pipeline: PipelineState): void {
  // Clear queues
  pipeline.inputQueue = [];
  for (const entry of pipeline.outputQueue) {
    entry.frame.close();
  }
  pipeline.outputQueue = [];

  // Mark as needing reconfiguration - we'll wait for next keyframe
  pipeline.configured = false;

  // If decoder is closed, we need to recreate it (can't reset a closed decoder)
  if (pipeline.decoder && pipeline.decoder.state === "closed") {
    log(`Decoder closed for track ${pipeline.idx}, will recreate on next configure`);
    pipeline.decoder = null;
  } else if (pipeline.decoder && pipeline.decoder.state !== "closed") {
    // Try to reset if not closed
    try {
      pipeline.decoder.reset();
      log(`Reset decoder for track ${pipeline.idx}`);
    } catch (e) {
      log(`Failed to reset decoder for track ${pipeline.idx}: ${e}`, "warn");
      pipeline.decoder = null;
    }
  }
}

// ============================================================================
// Frame Input/Output
// ============================================================================

function handleReceive(msg: MainToWorkerMessage & { type: "receive" }): void {
  const { idx, chunk } = msg;
  const pipeline = pipelines.get(idx);

  if (!pipeline) {
    logVerbose(`Received chunk for unknown pipeline ${idx}`);
    return;
  }

  if (!pipeline.configured || !pipeline.decoder) {
    // Queue for later
    pipeline.inputQueue.push(chunk);
    logVerbose(
      `Queued chunk for track ${idx} (configured=${pipeline.configured}, decoder=${!!pipeline.decoder})`
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

  // Log only first 3 chunks per track to confirm receiving
  if (pipeline.stats.framesIn < 3) {
    log(
      `Received chunk ${pipeline.stats.framesIn} for track ${idx}: type=${chunk.type}, ts=${chunk.timestamp / 1000}ms, size=${chunk.data.byteLength}`
    );
  }

  // Check if we need to drop frames due to decoder pressure (Phase 2B)
  if (shouldDropFramesDueToDecoderPressure(pipeline)) {
    if (chunk.type === "key") {
      // Always accept keyframes - they're needed to resume
      decodeChunk(pipeline, chunk);
    } else {
      // Drop delta frames when decoder is overwhelmed
      pipeline.stats.framesDropped++;
      _totalFramesDropped++;
      logVerbose(
        `Dropped delta frame @ ${chunk.timestamp / 1000}ms (decoder queue: ${pipeline.decoder.decodeQueueSize})`
      );
    }
    return;
  }

  decodeChunk(pipeline, chunk);
}

/**
 * Check if decoder is under pressure and frames should be dropped
 * Based on Chrome WebCodecs best practices: drop when decodeQueueSize > 2
 */
function shouldDropFramesDueToDecoderPressure(pipeline: PipelineState): boolean {
  if (frameTiming.paused) return false;
  if (!pipeline.decoder) return false;

  const queueSize = pipeline.decoder.decodeQueueSize;
  pipeline.stats.decoderQueueSize = queueSize;

  // Chrome recommendation: drop frames when queue > 2
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
      // AVCC mode: frames pass through unchanged (decoder has SPS/PPS from description)
      const encodedChunk = new EncodedVideoChunk({
        type: chunk.type,
        timestamp: timestampUs,
        data: chunk.data,
      });

      const decoder = pipeline.decoder as VideoDecoder;
      if (pipeline.stats.framesIn <= 3) {
        const firstBytes = Array.from(chunk.data.slice(0, 16))
          .map((b) => "0x" + b.toString(16).padStart(2, "0"))
          .join(" ");
        log(
          `Calling decode() for track ${pipeline.idx}: state=${decoder.state}, queueSize=${decoder.decodeQueueSize}, chunk type=${chunk.type}, ts=${timestampUs}μs`
        );
        log(`  First 16 bytes: ${firstBytes}`);
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

  if (!pipeline.writer || pipeline.outputQueue.length === 0) {
    if (pipeline.outputQueue.length > 0 && !pipeline.writer) {
      log(
        `Cannot output: no writer for track ${pipeline.idx} (queue has ${pipeline.outputQueue.length} frames)`,
        "warn"
      );
    }
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
      log(`Sorted ${pipeline.outputQueue.length} frames in output queue for track ${pipeline.idx}`);
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

  if (frameTiming.seeking) {
    // During seek, reset baseTime and output first keyframe immediately
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
  if (!pipeline.writer || pipeline.closed) {
    entry.frame.close();
    return;
  }

  const now = performance.now() * 1000;
  frameTiming.out = now;
  pipeline.stats.framesOut++;
  pipeline.stats.lastOutputTimestamp = entry.timestamp;

  // Log first few output frames
  if (pipeline.stats.framesOut <= 3) {
    log(
      `Output frame ${pipeline.stats.framesOut} for track ${pipeline.idx}: ts=${entry.timestamp}μs`
    );
  }

  // Store history for frame stepping (video only)
  if (pipeline.track.type === "video" && !options?.skipHistory) {
    pushFrameHistory(pipeline, entry.frame as VideoFrame, entry.timestamp);
  }

  // Write returns a Promise - handle rejection to avoid unhandled promise errors
  // Frame ownership is transferred to the stream, so we don't need to close() on success
  pipeline.writer
    .write(entry.frame)
    .then(() => {
      // Send timeupdate event on successful write
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
      // Check for "stream closed" errors - these are expected during cleanup
      const errStr = String(err);
      if (errStr.includes("Stream closed") || errStr.includes("InvalidStateError")) {
        // Expected during player cleanup - silently mark pipeline as closed
        pipeline.closed = true;
      } else {
        log(`Failed to write frame: ${err}`, "error");
      }
      // Frame may not have been consumed by the stream - try to close it
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
  frameTiming.seeking = true;
  resetBaseTime(); // Reset timing reference for new position

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
  // Clear input queue
  pipeline.inputQueue = [];

  // Close and clear output queue frames
  for (const entry of pipeline.outputQueue) {
    entry.frame.close();
  }
  pipeline.outputQueue = [];

  // Reset decoder if possible
  if (pipeline.decoder && pipeline.decoder.state !== "closed") {
    try {
      pipeline.decoder.reset();
    } catch {
      // Ignore reset errors
    }
  }
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
        seeking: frameTiming.seeking,
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

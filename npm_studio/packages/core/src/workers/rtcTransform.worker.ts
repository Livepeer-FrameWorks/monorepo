/**
 * RTC Transform Worker
 * Handles RTCRtpScriptTransform for injecting WebCodecs-encoded chunks into WebRTC stream
 *
 * This worker receives encoded video/audio chunks from our WebCodecs encoder
 * and uses them to replace the browser's default encoded frames in the WebRTC pipeline.
 *
 * Architecture:
 * 1. Main thread creates RTCRtpScriptTransform with this worker
 * 2. Worker receives 'rtctransform' event with readable/writable streams
 * 3. Main thread sends encoded chunks via postMessage
 * 4. Worker replaces browser frames with our chunks in the transform stream
 *
 * Safety features:
 * - Keyframe sync gating: Don't inject delta frames until we've successfully injected a keyframe
 * - Drift detection: Don't replace frames if timestamp drift exceeds threshold
 * - Consecutive miss tracking: Reset sync state if too many frames pass without replacement
 */
export {};

// Type declarations for WebRTC Encoded Transform APIs (not in standard lib.dom.d.ts yet)
declare global {
  interface RTCEncodedVideoFrame {
    readonly type: "key" | "delta" | "empty";
    readonly timestamp: number;
    data: ArrayBuffer;
    getMetadata(): RTCEncodedVideoFrameMetadata;
  }

  interface RTCEncodedVideoFrameMetadata {
    frameId?: number;
    dependencies?: number[];
    width?: number;
    height?: number;
    spatialIndex?: number;
    temporalIndex?: number;
    synchronizationSource?: number;
    payloadType?: number;
    contributingSources?: number[];
  }

  interface RTCEncodedAudioFrame {
    readonly timestamp: number;
    data: ArrayBuffer;
    getMetadata(): RTCEncodedAudioFrameMetadata;
  }

  interface RTCEncodedAudioFrameMetadata {
    synchronizationSource?: number;
    payloadType?: number;
    contributingSources?: number[];
  }

  interface RTCTransformEvent extends Event {
    readonly transformer: RTCRtpScriptTransformer;
  }

  interface RTCRtpScriptTransformer {
    readonly readable: ReadableStream;
    readonly writable: WritableStream;
    readonly options?: unknown;
  }
}

// ============================================================================
// Types
// ============================================================================

interface EncodedChunkData {
  timestamp: number;
  duration: number | null;
  type: "key" | "delta";
  data: ArrayBuffer;
}

interface TransformWorkerMessage {
  type: "videoChunk" | "audioChunk" | "configure" | "flush" | "stop";
  data?: EncodedChunkData;
  config?: {
    debug?: boolean;
    maxQueueSize?: number;
    maxTimestampDriftUs?: number;
    maxConsecutiveMisses?: number;
  };
}

interface TransformStats {
  video: {
    chunksReceived: number;
    chunksUsed: number;
    chunksDropped: number;
    framesProcessed: number;
    framesPassedThrough: number;
    keyframesInjected: number;
    syncResets: number;
  };
  audio: {
    chunksReceived: number;
    chunksUsed: number;
    chunksDropped: number;
    framesProcessed: number;
    framesPassedThrough: number;
  };
}

// ============================================================================
// Constants
// ============================================================================

const DEFAULT_MAX_QUEUE_SIZE = 30; // ~1 second at 30fps
const DEFAULT_MAX_TIMESTAMP_DRIFT_US = 100_000; // 100ms in microseconds
const DEFAULT_MAX_CONSECUTIVE_MISSES = 30; // ~1s at 30fps

// ============================================================================
// State
// ============================================================================

// Configuration
let debug = false;
let maxQueueSize = DEFAULT_MAX_QUEUE_SIZE;
let maxTimestampDriftUs = DEFAULT_MAX_TIMESTAMP_DRIFT_US;
let maxConsecutiveMisses = DEFAULT_MAX_CONSECUTIVE_MISSES;

// Chunk queues
const videoChunkQueue: EncodedChunkData[] = [];
const audioChunkQueue: EncodedChunkData[] = [];

// Transform state
let videoTransformActive = false;
let audioTransformActive = false;
let isRunning = true;

// Keyframe sync gating state (video only)
let hasInjectedKeyframe = false;
let consecutiveVideoMisses = 0;

// Stats
const stats: TransformStats = {
  video: {
    chunksReceived: 0,
    chunksUsed: 0,
    chunksDropped: 0,
    framesProcessed: 0,
    framesPassedThrough: 0,
    keyframesInjected: 0,
    syncResets: 0,
  },
  audio: {
    chunksReceived: 0,
    chunksUsed: 0,
    chunksDropped: 0,
    framesProcessed: 0,
    framesPassedThrough: 0,
  },
};

// ============================================================================
// Helpers
// ============================================================================

function log(message: string, data?: unknown): void {
  if (debug) {
    console.log(`[RTCTransformWorker] ${message}`, data ?? "");
  }
}

/**
 * Handle incoming encoded chunk from main thread.
 * Maintains bounded queue with FIFO drop policy.
 */
function handleChunk(type: "video" | "audio", chunk: EncodedChunkData): void {
  const queue = type === "video" ? videoChunkQueue : audioChunkQueue;
  const statsObj = type === "video" ? stats.video : stats.audio;

  statsObj.chunksReceived++;

  // Drop old chunks if queue is full
  while (queue.length >= maxQueueSize) {
    queue.shift();
    statsObj.chunksDropped++;
  }

  queue.push(chunk);
}

/**
 * Find a chunk matching the target timestamp within drift tolerance.
 * Returns the chunk and removes it from the queue.
 */
function findMatchingChunk(
  type: "video" | "audio",
  targetTimestamp: number
): EncodedChunkData | undefined {
  const queue = type === "video" ? videoChunkQueue : audioChunkQueue;
  const statsObj = type === "video" ? stats.video : stats.audio;

  if (queue.length === 0) {
    return undefined;
  }

  // Find chunk within drift tolerance
  const matchIndex = queue.findIndex(
    (c) => Math.abs(c.timestamp - targetTimestamp) < maxTimestampDriftUs
  );

  if (matchIndex !== -1) {
    const chunk = queue.splice(matchIndex, 1)[0];
    statsObj.chunksUsed++;
    return chunk;
  }

  // No match within tolerance - check if queue is stale (all timestamps too old)
  const oldestChunk = queue[0];
  if (oldestChunk && targetTimestamp - oldestChunk.timestamp > maxTimestampDriftUs * 2) {
    // Queue is stale, drop oldest
    queue.shift();
    statsObj.chunksDropped++;
  }

  return undefined;
}

/**
 * Reset video sync state.
 * Called when we've had too many consecutive misses.
 */
function resetVideoSync(): void {
  hasInjectedKeyframe = false;
  consecutiveVideoMisses = 0;
  stats.video.syncResets++;
  log("Video sync reset - waiting for next keyframe");
}

// ============================================================================
// Transform Streams
// ============================================================================

/**
 * Create transform stream for video frames.
 * Implements keyframe sync gating and drift detection.
 */
function createVideoTransform(): TransformStream<RTCEncodedVideoFrame, RTCEncodedVideoFrame> {
  return new TransformStream({
    transform(frame: RTCEncodedVideoFrame, controller) {
      stats.video.framesProcessed++;

      if (!isRunning) {
        controller.enqueue(frame);
        return;
      }

      // Try to find a matching replacement chunk
      const replacement = findMatchingChunk("video", frame.timestamp);

      if (!replacement) {
        // No replacement available
        consecutiveVideoMisses++;

        // Too many misses - reset sync state
        if (consecutiveVideoMisses > maxConsecutiveMisses) {
          resetVideoSync();
        }

        stats.video.framesPassedThrough++;
        log("Passed through browser video frame", {
          ts: frame.timestamp,
          queueSize: videoChunkQueue.length,
          misses: consecutiveVideoMisses,
          hasSynced: hasInjectedKeyframe,
        });

        controller.enqueue(frame);
        return;
      }

      // Reset miss counter on successful match
      consecutiveVideoMisses = 0;

      // Keyframe sync gating: Don't inject deltas until we've injected a keyframe
      if (!hasInjectedKeyframe) {
        if (replacement.type !== "key") {
          // Can't inject delta before keyframe - pass through browser frame
          // (but we already consumed the chunk, so we lose it)
          stats.video.framesPassedThrough++;
          log("Skipped delta chunk (no keyframe yet)", {
            ts: frame.timestamp,
            chunkType: replacement.type,
          });
          controller.enqueue(frame);
          return;
        }

        // First keyframe - enable injection
        hasInjectedKeyframe = true;
        stats.video.keyframesInjected++;
        log("Keyframe sync established");
      }

      // Track keyframes for stats
      if (replacement.type === "key") {
        stats.video.keyframesInjected++;
      }

      // Replace frame data with our encoded chunk
      // Buffer is already transferred and single-use - no need to copy
      frame.data = replacement.data;

      log("Replaced video frame", {
        originalTs: frame.timestamp,
        chunkTs: replacement.timestamp,
        size: replacement.data.byteLength,
        type: replacement.type,
      });

      controller.enqueue(frame);
    },
  });
}

/**
 * Create transform stream for audio frames.
 * Audio doesn't need keyframe gating (each packet is independent for Opus).
 */
function createAudioTransform(): TransformStream<RTCEncodedAudioFrame, RTCEncodedAudioFrame> {
  return new TransformStream({
    transform(frame: RTCEncodedAudioFrame, controller) {
      stats.audio.framesProcessed++;

      if (!isRunning) {
        controller.enqueue(frame);
        return;
      }

      const replacement = findMatchingChunk("audio", frame.timestamp);

      if (replacement) {
        // Replace frame data - no copy needed
        frame.data = replacement.data;

        log("Replaced audio frame", {
          originalTs: frame.timestamp,
          chunkTs: replacement.timestamp,
          size: replacement.data.byteLength,
        });
      } else {
        stats.audio.framesPassedThrough++;
      }

      controller.enqueue(frame);
    },
  });
}

// ============================================================================
// RTCTransform Event Handler
// ============================================================================

/**
 * Handle RTCRtpScriptTransform initialization.
 * Called when the transform is attached to an RTCRtpSender.
 */
function handleRTCTransform(event: RTCTransformEvent): void {
  const { readable, writable } = event.transformer;
  const options = event.transformer.options as { kind?: "video" | "audio" } | undefined;
  const kind = options?.kind ?? "video";

  log(`RTCTransform initialized for ${kind}`, { options });

  if (kind === "video") {
    if (videoTransformActive) {
      console.warn("[RTCTransformWorker] Video transform already active");
      return;
    }
    videoTransformActive = true;

    // Reset sync state for new transform
    hasInjectedKeyframe = false;
    consecutiveVideoMisses = 0;

    const transform = createVideoTransform();
    readable
      .pipeThrough(transform)
      .pipeTo(writable)
      .catch((error: unknown) => {
        console.error("[RTCTransformWorker] Video transform pipeline error:", error);
        videoTransformActive = false;
      });
  } else {
    if (audioTransformActive) {
      console.warn("[RTCTransformWorker] Audio transform already active");
      return;
    }
    audioTransformActive = true;

    const transform = createAudioTransform();
    readable
      .pipeThrough(transform)
      .pipeTo(writable)
      .catch((error: unknown) => {
        console.error("[RTCTransformWorker] Audio transform pipeline error:", error);
        audioTransformActive = false;
      });
  }
}

// ============================================================================
// Message Handler
// ============================================================================

self.addEventListener("message", (event: MessageEvent<TransformWorkerMessage>) => {
  const message = event.data;

  switch (message.type) {
    case "videoChunk":
      if (message.data) {
        handleChunk("video", message.data);
      }
      break;

    case "audioChunk":
      if (message.data) {
        handleChunk("audio", message.data);
      }
      break;

    case "configure":
      if (message.config) {
        if (message.config.debug !== undefined) {
          debug = message.config.debug;
        }
        if (message.config.maxQueueSize !== undefined) {
          maxQueueSize = message.config.maxQueueSize;
        }
        if (message.config.maxTimestampDriftUs !== undefined) {
          maxTimestampDriftUs = message.config.maxTimestampDriftUs;
        }
        if (message.config.maxConsecutiveMisses !== undefined) {
          maxConsecutiveMisses = message.config.maxConsecutiveMisses;
        }
        log("Configuration updated", message.config);
      }
      break;

    case "flush":
      // Clear queues and reset sync
      videoChunkQueue.length = 0;
      audioChunkQueue.length = 0;
      hasInjectedKeyframe = false;
      consecutiveVideoMisses = 0;
      log("Queues flushed, sync reset");
      break;

    case "stop":
      isRunning = false;
      videoChunkQueue.length = 0;
      audioChunkQueue.length = 0;
      hasInjectedKeyframe = false;
      consecutiveVideoMisses = 0;
      log("Transform stopped", stats);

      self.postMessage({ type: "stopped", stats });
      break;

    default:
      log("Unknown message type", message);
  }
});

// ============================================================================
// Event Listeners
// ============================================================================

self.addEventListener("rtctransform", handleRTCTransform as EventListener);

// Periodic stats reporting (every 5 seconds in debug mode)
setInterval(() => {
  if (debug && isRunning) {
    self.postMessage({
      type: "stats",
      stats: { ...stats },
      syncState: {
        hasInjectedKeyframe,
        consecutiveVideoMisses,
        videoQueueSize: videoChunkQueue.length,
        audioQueueSize: audioChunkQueue.length,
      },
    });
  }
}, 5000);

log("Worker loaded");

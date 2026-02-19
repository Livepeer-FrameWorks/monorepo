/**
 * Encoder Worker
 * Background-safe video/audio encoding using WebCodecs
 * Runs in a Web Worker to avoid main thread blocking and background tab throttling
 *
 * Output: Encoded chunks sent via postMessage (for RTCRtpScriptTransform injection)
 */
export {};

// ============================================================================
// Types
// ============================================================================

interface VideoEncoderSettings {
  codec: string;
  width: number;
  height: number;
  bitrate: number;
  framerate: number;
}

interface AudioEncoderSettings {
  codec: string;
  sampleRate: number;
  numberOfChannels: number;
  bitrate: number;
}

interface EncoderConfig {
  video: VideoEncoderSettings;
  audio: AudioEncoderSettings;
  keyframeInterval?: number;
}

interface EncoderStats {
  video: {
    framesEncoded: number;
    framesDropped: number;
    framesSubmitted: number;
    framesPending: number;
    bytesEncoded: number;
    lastFrameTime: number;
  };
  audio: {
    samplesEncoded: number;
    samplesDropped: number;
    samplesSubmitted: number;
    samplesPending: number;
    bytesEncoded: number;
    lastSampleTime: number;
  };
  timestamp: number;
}

// Worker messages (main → worker)
type WorkerInMessage =
  | { type: "initialize"; requestId: string; data: { config: EncoderConfig } }
  | { type: "start"; requestId: string }
  | { type: "stop"; requestId: string }
  | { type: "flush"; requestId: string }
  | { type: "updateConfig"; requestId: string; data: Partial<EncoderConfig> }
  | { type: "videoFrame"; data: VideoFrame }
  | { type: "audioData"; data: AudioData };

// Worker messages (worker → main)
type WorkerOutMessage =
  | { type: "ready"; requestId: string }
  | { type: "started"; requestId: string }
  | { type: "stopped"; requestId: string }
  | { type: "flushed"; requestId: string }
  | { type: "error"; requestId?: string; data: { message: string; fatal: boolean } }
  | { type: "stats"; data: EncoderStats }
  | { type: "encodedVideoChunk"; data: EncodedVideoChunkData }
  | { type: "encodedAudioChunk"; data: EncodedAudioChunkData };

// Serializable encoded chunk data (for postMessage transfer)
interface EncodedVideoChunkData {
  timestamp: number;
  duration: number | null;
  type: "key" | "delta";
  data: ArrayBuffer;
}

interface EncodedAudioChunkData {
  timestamp: number;
  duration: number | null;
  type: "key" | "delta";
  data: ArrayBuffer;
}

// ============================================================================
// Constants (backpressure thresholds)
// ============================================================================

const MAX_VIDEO_QUEUE_SIZE = 3; // Low-latency: only buffer ~3 frames
const MAX_AUDIO_QUEUE_SIZE = 10; // Audio is smaller, buffer a bit more
const ENCODE_QUEUE_THRESHOLD = 5; // If encoder queue > 5, start dropping
const DEFAULT_KEYFRAME_INTERVAL = 60; // Every 60 frames (~2s at 30fps)
const STATS_INTERVAL_MS = 1000;

// ============================================================================
// Worker state
// ============================================================================

let videoEncoder: VideoEncoder | null = null;
let audioEncoder: AudioEncoder | null = null;
let config: EncoderConfig | null = null;
let isRunning = false;
let isInitialized = false;
let keyframeInterval = DEFAULT_KEYFRAME_INTERVAL;

// Queue management for frame processing
let videoWriteQueue: VideoFrame[] = [];
let audioWriteQueue: AudioData[] = [];
let isProcessingVideoQueue = false;
let isProcessingAudioQueue = false;

// Keyframe scheduling state (input-driven)
let videoFramesSubmitted = 0;
let justRecoveredFromBackpressure = false;
let justReconfigured = false;
let wasInBackpressure = false;

// Audio frame counter
let audioFramesSubmitted = 0;

// Stats tracking
const stats: EncoderStats = {
  video: {
    framesEncoded: 0,
    framesDropped: 0,
    framesSubmitted: 0,
    framesPending: 0,
    bytesEncoded: 0,
    lastFrameTime: 0,
  },
  audio: {
    samplesEncoded: 0,
    samplesDropped: 0,
    samplesSubmitted: 0,
    samplesPending: 0,
    bytesEncoded: 0,
    lastSampleTime: 0,
  },
  timestamp: 0,
};

// Stats reporting interval
let statsInterval: ReturnType<typeof setInterval> | null = null;

// ============================================================================
// Messaging helpers
// ============================================================================

function postMessage(message: WorkerOutMessage, transfer?: Transferable[]): void {
  if (transfer && transfer.length > 0) {
    // Worker context uses options object for transferables
    self.postMessage(message, { transfer });
  } else {
    self.postMessage(message);
  }
}

function postError(message: string, fatal: boolean, requestId?: string): void {
  postMessage({
    type: "error",
    requestId,
    data: { message, fatal },
  });
}

// ============================================================================
// Encoder output handlers
// ============================================================================

function handleVideoOutput(chunk: EncodedVideoChunk, _metadata?: EncodedVideoChunkMetadata): void {
  stats.video.framesEncoded++;
  stats.video.bytesEncoded += chunk.byteLength;
  stats.video.lastFrameTime = performance.now();

  // Copy chunk data to ArrayBuffer for transfer to main thread
  const buffer = new ArrayBuffer(chunk.byteLength);
  chunk.copyTo(buffer);

  const chunkData: EncodedVideoChunkData = {
    timestamp: chunk.timestamp,
    duration: chunk.duration,
    type: chunk.type,
    data: buffer,
  };

  // Transfer buffer ownership for zero-copy performance
  postMessage({ type: "encodedVideoChunk", data: chunkData }, [buffer]);
}

function handleAudioOutput(chunk: EncodedAudioChunk, _metadata?: EncodedAudioChunkMetadata): void {
  stats.audio.samplesEncoded++;
  stats.audio.bytesEncoded += chunk.byteLength;
  stats.audio.lastSampleTime = performance.now();

  const buffer = new ArrayBuffer(chunk.byteLength);
  chunk.copyTo(buffer);

  const chunkData: EncodedAudioChunkData = {
    timestamp: chunk.timestamp,
    duration: chunk.duration,
    type: chunk.type,
    data: buffer,
  };

  postMessage({ type: "encodedAudioChunk", data: chunkData }, [buffer]);
}

function handleEncoderError(type: "video" | "audio", error: Error): void {
  console.error(`[EncoderWorker] ${type} encoder error:`, error);
  postError(`${type} encoder error: ${error.message}`, true);
}

// ============================================================================
// Encoder initialization
// ============================================================================

async function initVideoEncoder(): Promise<boolean> {
  if (!config) return false;

  const { video } = config;

  const codecConfig: VideoEncoderConfig = {
    codec: video.codec,
    width: video.width,
    height: video.height,
    bitrate: video.bitrate,
    framerate: video.framerate,
    latencyMode: "realtime",
    bitrateMode: "variable",
  };

  console.log("[EncoderWorker] Video encoder config:", codecConfig);

  try {
    const support = await VideoEncoder.isConfigSupported(codecConfig);
    if (!support.supported) {
      throw new Error(`Video codec ${video.codec} not supported`);
    }

    // Close existing encoder if any
    if (videoEncoder && videoEncoder.state !== "closed") {
      try {
        videoEncoder.close();
      } catch {
        // Ignore
      }
    }

    videoEncoder = new VideoEncoder({
      output: handleVideoOutput,
      error: (e) => handleEncoderError("video", e),
    });

    videoEncoder.configure(codecConfig);
    console.log("[EncoderWorker] Video encoder initialized");
    return true;
  } catch (error) {
    console.error("[EncoderWorker] Failed to initialize video encoder:", error);
    return false;
  }
}

async function initAudioEncoder(): Promise<boolean> {
  if (!config) return false;

  const { audio } = config;

  const codecConfig: AudioEncoderConfig = {
    codec: audio.codec,
    sampleRate: audio.sampleRate,
    numberOfChannels: audio.numberOfChannels,
    bitrate: audio.bitrate,
  };

  try {
    const support = await AudioEncoder.isConfigSupported(codecConfig);
    if (!support.supported) {
      throw new Error(`Audio codec ${audio.codec} not supported`);
    }

    // Close existing encoder if any
    if (audioEncoder && audioEncoder.state !== "closed") {
      try {
        audioEncoder.close();
      } catch {
        // Ignore
      }
    }

    audioEncoder = new AudioEncoder({
      output: handleAudioOutput,
      error: (e) => handleEncoderError("audio", e),
    });

    audioEncoder.configure(codecConfig);
    console.log("[EncoderWorker] Audio encoder initialized");
    return true;
  } catch (error) {
    console.error("[EncoderWorker] Failed to initialize audio encoder:", error);
    return false;
  }
}

// ============================================================================
// Queue processing with backpressure
// ============================================================================

async function processVideoQueue(): Promise<void> {
  if (isProcessingVideoQueue || videoWriteQueue.length === 0) {
    return;
  }

  isProcessingVideoQueue = true;

  try {
    while (videoWriteQueue.length > 0 && isRunning) {
      const frame = videoWriteQueue.shift();
      if (!frame) continue;

      try {
        if (videoEncoder && videoEncoder.state === "configured") {
          // Backpressure check: if encoder queue is too deep, drop frame
          if (videoEncoder.encodeQueueSize > ENCODE_QUEUE_THRESHOLD) {
            stats.video.framesDropped++;
            wasInBackpressure = true;
            frame.close();
            continue;
          }

          // Check if we just recovered from backpressure
          if (wasInBackpressure && videoEncoder.encodeQueueSize <= 2) {
            justRecoveredFromBackpressure = true;
            wasInBackpressure = false;
          }

          // Input-driven keyframe scheduling
          const forceKeyframe =
            videoFramesSubmitted === 0 || // First frame
            videoFramesSubmitted % keyframeInterval === 0 || // Periodic
            justRecoveredFromBackpressure || // After recovery
            justReconfigured; // After config change

          videoEncoder.encode(frame, { keyFrame: forceKeyframe });
          videoFramesSubmitted++;
          stats.video.framesSubmitted = videoFramesSubmitted;
          stats.video.framesPending = videoEncoder.encodeQueueSize;

          // Clear flags after use
          justRecoveredFromBackpressure = false;
          justReconfigured = false;
        }
      } catch (error) {
        console.error("[EncoderWorker] Error encoding video frame:", error);
        stats.video.framesDropped++;
      } finally {
        // Always close the frame to prevent memory leaks
        try {
          frame.close();
        } catch {
          // Ignore close errors
        }
      }
    }
  } finally {
    isProcessingVideoQueue = false;

    // Check if more frames arrived while processing
    if (videoWriteQueue.length > 0 && isRunning) {
      processVideoQueue();
    }
  }
}

async function processAudioQueue(): Promise<void> {
  if (isProcessingAudioQueue || audioWriteQueue.length === 0) {
    return;
  }

  isProcessingAudioQueue = true;

  try {
    while (audioWriteQueue.length > 0 && isRunning) {
      const audioData = audioWriteQueue.shift();
      if (!audioData) continue;

      try {
        if (audioEncoder && audioEncoder.state === "configured") {
          // Backpressure check for audio
          if (audioEncoder.encodeQueueSize > ENCODE_QUEUE_THRESHOLD) {
            stats.audio.samplesDropped++;
            audioData.close();
            continue;
          }

          audioEncoder.encode(audioData);
          audioFramesSubmitted++;
          stats.audio.samplesSubmitted = audioFramesSubmitted;
          stats.audio.samplesPending = audioEncoder.encodeQueueSize;
        }
      } catch (error) {
        console.error("[EncoderWorker] Error encoding audio data:", error);
        stats.audio.samplesDropped++;
      } finally {
        try {
          audioData.close();
        } catch {
          // Ignore close errors
        }
      }
    }
  } finally {
    isProcessingAudioQueue = false;

    if (audioWriteQueue.length > 0 && isRunning) {
      processAudioQueue();
    }
  }
}

// ============================================================================
// Worker lifecycle commands
// ============================================================================

async function initialize(encoderConfig: EncoderConfig, requestId: string): Promise<void> {
  console.log("[EncoderWorker] Initializing with config:", encoderConfig);
  config = encoderConfig;
  keyframeInterval = encoderConfig.keyframeInterval ?? DEFAULT_KEYFRAME_INTERVAL;

  // Reset state
  videoFramesSubmitted = 0;
  audioFramesSubmitted = 0;
  justRecoveredFromBackpressure = false;
  justReconfigured = false;
  wasInBackpressure = false;

  // Reset stats
  stats.video = {
    framesEncoded: 0,
    framesDropped: 0,
    framesSubmitted: 0,
    framesPending: 0,
    bytesEncoded: 0,
    lastFrameTime: 0,
  };
  stats.audio = {
    samplesEncoded: 0,
    samplesDropped: 0,
    samplesSubmitted: 0,
    samplesPending: 0,
    bytesEncoded: 0,
    lastSampleTime: 0,
  };

  // Initialize encoders
  const videoOk = await initVideoEncoder();
  const audioOk = await initAudioEncoder();

  if (!videoOk && !audioOk) {
    postError("Failed to initialize any encoders", true, requestId);
    return;
  }

  isInitialized = true;
  postMessage({ type: "ready", requestId });
}

function start(requestId: string): void {
  if (!isInitialized) {
    postError("Worker not initialized", true, requestId);
    return;
  }

  if (isRunning) {
    postMessage({ type: "started", requestId });
    return;
  }

  isRunning = true;

  // Start stats reporting
  statsInterval = setInterval(() => {
    stats.timestamp = performance.now();
    postMessage({ type: "stats", data: { ...stats } });
  }, STATS_INTERVAL_MS);

  postMessage({ type: "started", requestId });
}

async function stop(requestId: string): Promise<void> {
  isRunning = false;

  // Stop stats reporting
  if (statsInterval) {
    clearInterval(statsInterval);
    statsInterval = null;
  }

  // Clear queues and close pending frames
  for (const frame of videoWriteQueue) {
    try {
      frame.close();
    } catch {
      // Ignore
    }
  }
  videoWriteQueue = [];

  for (const data of audioWriteQueue) {
    try {
      data.close();
    } catch {
      // Ignore
    }
  }
  audioWriteQueue = [];

  postMessage({ type: "stopped", requestId });
}

async function flush(requestId: string): Promise<void> {
  try {
    if (videoEncoder && videoEncoder.state === "configured") {
      await videoEncoder.flush();
    }
    if (audioEncoder && audioEncoder.state === "configured") {
      await audioEncoder.flush();
    }
    postMessage({ type: "flushed", requestId });
  } catch (error) {
    console.error("[EncoderWorker] Flush error:", error);
    postError(`Flush failed: ${error}`, false, requestId);
  }
}

async function updateConfig(newConfig: Partial<EncoderConfig>, requestId: string): Promise<void> {
  if (!config) {
    postError("Worker not initialized", true, requestId);
    return;
  }

  justReconfigured = true;

  if (newConfig.keyframeInterval !== undefined) {
    keyframeInterval = newConfig.keyframeInterval;
  }

  if (newConfig.video) {
    config.video = { ...config.video, ...newConfig.video };
    await initVideoEncoder();
  }

  if (newConfig.audio) {
    config.audio = { ...config.audio, ...newConfig.audio };
    await initAudioEncoder();
  }

  // Ack config update (reuse ready message type for simplicity)
  postMessage({ type: "ready", requestId });
}

// ============================================================================
// Cleanup
// ============================================================================

function cleanup(): void {
  isRunning = false;
  isInitialized = false;

  if (statsInterval) {
    clearInterval(statsInterval);
    statsInterval = null;
  }

  // Close encoders
  if (videoEncoder) {
    try {
      if (videoEncoder.state !== "closed") {
        videoEncoder.close();
      }
    } catch {
      // Ignore
    }
    videoEncoder = null;
  }

  if (audioEncoder) {
    try {
      if (audioEncoder.state !== "closed") {
        audioEncoder.close();
      }
    } catch {
      // Ignore
    }
    audioEncoder = null;
  }

  // Clear queues
  for (const frame of videoWriteQueue) {
    try {
      frame.close();
    } catch {
      // Ignore
    }
  }
  videoWriteQueue = [];

  for (const data of audioWriteQueue) {
    try {
      data.close();
    } catch {
      // Ignore
    }
  }
  audioWriteQueue = [];
}

// ============================================================================
// Message handler
// ============================================================================

self.onmessage = async (event: MessageEvent<WorkerInMessage>) => {
  const message = event.data;

  switch (message.type) {
    case "initialize":
      await initialize(message.data.config, message.requestId);
      break;

    case "start":
      start(message.requestId);
      break;

    case "stop":
      await stop(message.requestId);
      break;

    case "flush":
      await flush(message.requestId);
      break;

    case "updateConfig":
      await updateConfig(message.data, message.requestId);
      break;

    case "videoFrame":
      if (!isRunning || !message.data) {
        // Drop frame if not running
        if (message.data) {
          try {
            message.data.close();
          } catch {
            // Ignore
          }
        }
        break;
      }

      // Bounded queue: drop oldest if full
      if (videoWriteQueue.length >= MAX_VIDEO_QUEUE_SIZE) {
        const dropped = videoWriteQueue.shift();
        if (dropped) {
          try {
            dropped.close();
          } catch {
            // Ignore
          }
          stats.video.framesDropped++;
        }
      }

      videoWriteQueue.push(message.data);
      processVideoQueue();
      break;

    case "audioData":
      if (!isRunning || !message.data) {
        if (message.data) {
          try {
            message.data.close();
          } catch {
            // Ignore
          }
        }
        break;
      }

      // Bounded queue: drop oldest if full
      if (audioWriteQueue.length >= MAX_AUDIO_QUEUE_SIZE) {
        const dropped = audioWriteQueue.shift();
        if (dropped) {
          try {
            dropped.close();
          } catch {
            // Ignore
          }
          stats.audio.samplesDropped++;
        }
      }

      audioWriteQueue.push(message.data);
      processAudioQueue();
      break;

    default:
      console.warn("[EncoderWorker] Unknown message type:", message);
  }
};

// Handle worker termination
self.onclose = () => {
  cleanup();
};

console.log("[EncoderWorker] Worker loaded");

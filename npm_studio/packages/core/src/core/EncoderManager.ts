/**
 * Encoder Manager
 * Manages WebCodecs encoder worker from main thread
 * Handles frame transfer via MediaStreamTrackProcessor
 *
 * Output: Encoded chunks emitted via 'videoChunk' and 'audioChunk' events
 * (for RTCRtpScriptTransform injection)
 */

import { TypedEventEmitter } from "./EventEmitter";
import type {
  EncoderConfig,
  VideoEncoderSettings,
  AudioEncoderSettings,
  EncoderOverrides,
} from "../types";

// ============================================================================
// Types
// ============================================================================

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

// Serializable encoded chunk data (matches worker output)
export interface EncodedVideoChunkData {
  timestamp: number;
  duration: number | null;
  type: "key" | "delta";
  data: ArrayBuffer;
}

export interface EncodedAudioChunkData {
  timestamp: number;
  duration: number | null;
  type: "key" | "delta";
  data: ArrayBuffer;
}

interface EncoderManagerEvents {
  ready: undefined;
  started: undefined;
  stopped: undefined;
  stats: EncoderStats;
  error: { message: string; fatal: boolean };
  videoChunk: EncodedVideoChunkData;
  audioChunk: EncodedAudioChunkData;
}

export interface EncoderManagerOptions {
  /** URL to the encoder worker script */
  workerUrl?: string;
  /** Pre-constructed Worker instance (takes precedence over workerUrl) */
  worker?: Worker;
  debug?: boolean;
  /** Timeout for worker responses (ms) */
  timeout?: number;
}

// Worker message types (must match encoder.worker.ts)
type WorkerInMessage =
  | { type: "initialize"; requestId: string; data: { config: EncoderConfig } }
  | { type: "start"; requestId: string }
  | { type: "stop"; requestId: string }
  | { type: "flush"; requestId: string }
  | { type: "updateConfig"; requestId: string; data: Partial<EncoderConfig> }
  | { type: "videoFrame"; data: VideoFrame }
  | { type: "audioData"; data: AudioData };

interface PendingRequest {
  resolve: (value?: unknown) => void;
  reject: (reason?: unknown) => void;
  timer: ReturnType<typeof setTimeout>;
}

// ============================================================================
// Default encoder settings
// ============================================================================

export const DEFAULT_VIDEO_SETTINGS: Record<string, VideoEncoderSettings> = {
  professional: {
    codec: "avc1.4d0032", // H.264 High Profile Level 5.0
    width: 1920,
    height: 1080,
    bitrate: 8_000_000,
    framerate: 30,
  },
  broadcast: {
    codec: "avc1.4d0028", // H.264 High Profile Level 4.0
    width: 1920,
    height: 1080,
    bitrate: 4_500_000,
    framerate: 30,
  },
  conference: {
    codec: "avc1.4d001f", // H.264 High Profile Level 3.1
    width: 1280,
    height: 720,
    bitrate: 2_500_000,
    framerate: 30,
  },
  low: {
    codec: "avc1.42001e", // H.264 Baseline Profile Level 3.0
    width: 640,
    height: 480,
    bitrate: 1_000_000,
    framerate: 24,
  },
};

export const DEFAULT_AUDIO_SETTINGS: AudioEncoderSettings = {
  codec: "opus",
  sampleRate: 48000,
  numberOfChannels: 2,
  bitrate: 128_000,
};

// ============================================================================
// EncoderManager
// ============================================================================

export class EncoderManager extends TypedEventEmitter<EncoderManagerEvents> {
  private worker: Worker | null = null;
  private videoProcessor: MediaStreamTrackProcessor<VideoFrame> | null = null;
  private audioProcessor: MediaStreamTrackProcessor<AudioData> | null = null;
  private videoReader: ReadableStreamDefaultReader<VideoFrame> | null = null;
  private audioReader: ReadableStreamDefaultReader<AudioData> | null = null;
  private isRunning = false;
  private isInitialized = false;
  private options: Required<Omit<EncoderManagerOptions, "worker">>;
  private stats: EncoderStats | null = null;
  private config: EncoderConfig | null = null;

  // Pending request tracking for async handshake
  private pendingRequests = new Map<string, PendingRequest>();
  private requestCounter = 0;

  private providedWorker: Worker | null = null;

  constructor(options: EncoderManagerOptions = {}) {
    super();
    this.options = {
      workerUrl: options.workerUrl ?? "",
      debug: options.debug ?? false,
      timeout: options.timeout ?? 10000,
    } as Required<Omit<EncoderManagerOptions, "worker">>;
    // Store pre-constructed worker if provided
    this.providedWorker = options.worker ?? null;
  }

  // ==========================================================================
  // Debug logging
  // ==========================================================================

  private log(message: string, data?: unknown): void {
    if (this.options.debug) {
      console.log(`[EncoderManager] ${message}`, data ?? "");
    }
  }

  // ==========================================================================
  // Request/response helpers
  // ==========================================================================

  private generateRequestId(): string {
    return `req_${++this.requestCounter}_${Date.now()}`;
  }

  private sendRequest<T = void>(message: WorkerInMessage): Promise<T> {
    return new Promise((resolve, reject) => {
      if (!this.worker) {
        reject(new Error("Worker not created"));
        return;
      }

      const requestId = (message as { requestId?: string }).requestId;
      if (!requestId) {
        // Fire-and-forget message (videoFrame, audioData)
        this.worker.postMessage(message);
        resolve(undefined as T);
        return;
      }

      const timer = setTimeout(() => {
        this.pendingRequests.delete(requestId);
        reject(new Error(`Request ${requestId} timed out`));
      }, this.options.timeout);

      this.pendingRequests.set(requestId, {
        resolve: resolve as (v?: unknown) => void,
        reject,
        timer,
      });
      this.worker.postMessage(message);
    });
  }

  private handleResponse(requestId: string, success: boolean, error?: string): void {
    const pending = this.pendingRequests.get(requestId);
    if (!pending) return;

    clearTimeout(pending.timer);
    this.pendingRequests.delete(requestId);

    if (success) {
      pending.resolve();
    } else {
      pending.reject(new Error(error ?? "Unknown error"));
    }
  }

  // ==========================================================================
  // Worker creation
  // ==========================================================================

  /**
   * Try to create a worker from a URL and wait for it to be ready
   * Returns the worker if successful, null if it fails to load
   */
  private tryCreateWorker(url: string | URL, useModule = true): Promise<Worker | null> {
    return new Promise((resolve) => {
      try {
        const worker = new Worker(url, useModule ? { type: "module" } : undefined);
        this.validateCreatedWorker(worker, url.toString()).then(resolve);
      } catch (e) {
        this.log("Failed to create worker: " + url.toString(), e);
        resolve(null);
      }
    });
  }

  private validateCreatedWorker(worker: Worker, label: string): Promise<Worker | null> {
    return new Promise((resolve) => {
      let resolved = false;

      const cleanup = () => {
        worker.removeEventListener("message", onMessage);
        worker.removeEventListener("error", onError);
      };

      const onMessage = (_e: MessageEvent) => {
        if (!resolved) {
          resolved = true;
          cleanup();
          resolve(worker);
        }
      };

      const onError = (e: ErrorEvent) => {
        if (!resolved) {
          resolved = true;
          cleanup();
          this.log("Worker failed to load from: " + label, e.message);
          worker.terminate();
          resolve(null);
        }
      };

      worker.addEventListener("message", onMessage);
      worker.addEventListener("error", onError);

      // If startup is silent, keep worker and let initialize() handshake determine readiness.
      setTimeout(() => {
        if (!resolved) {
          resolved = true;
          cleanup();
          resolve(worker);
        }
      }, 2000);
    });
  }

  private tryCreateWorkerFromFactory(
    description: string,
    createWorker: () => Worker
  ): Promise<Worker | null> {
    return new Promise((resolve) => {
      try {
        const worker = createWorker();
        this.validateCreatedWorker(worker, description).then(resolve);
      } catch (e) {
        this.log("Failed to create worker from factory: " + description, e);
        resolve(null);
      }
    });
  }

  private async createWorkerAsync(): Promise<Worker> {
    // Priority 1: Use pre-constructed worker if provided
    if (this.providedWorker) {
      this.log("Using provided worker instance");
      return this.providedWorker;
    }

    // Priority 2: Use explicit worker URL if provided
    if (this.options.workerUrl) {
      this.log("Creating worker from URL:", this.options.workerUrl);
      const worker = await this.tryCreateWorker(this.options.workerUrl);
      if (worker) return worker;
    }

    // Bundler-managed worker URL patterns. Keep new URL(...) inline in new Worker(...)
    // so app bundlers can emit/rehydrate worker assets.
    const bundlerFactories = [
      {
        description: "bundler ../../workers/encoder.worker.js",
        create: () =>
          new Worker(new URL("../../workers/encoder.worker.js", import.meta.url), {
            type: "module",
          }),
      },
    ] as const;

    for (const factory of bundlerFactories) {
      this.log("Trying worker path: " + factory.description);
      const worker = await this.tryCreateWorkerFromFactory(factory.description, factory.create);
      if (worker) {
        this.log("Worker loaded from:", factory.description);
        return worker;
      }
    }

    // Build list of URLs to try
    const urlsToTry: Array<{ url: string | URL; description: string }> = [];

    // Priority 3: Try loading relative to source module (bundler/dev)
    try {
      const workerUrl = new URL("../workers/encoder.worker.mod.js", import.meta.url);
      urlsToTry.push({ url: workerUrl, description: "import.meta.url source (.mod.js)" });
    } catch {
      // URL construction failed, skip
    }

    // Priority 4: Try loading relative to source module (.ts fallback)
    try {
      const workerUrl = new URL("../workers/encoder.worker.ts", import.meta.url);
      urlsToTry.push({ url: workerUrl, description: "import.meta.url source (.ts fallback)" });
    } catch {
      // URL construction failed, skip
    }

    // Priority 5: Try loading relative to published ESM output
    try {
      const workerUrl = new URL("../../workers/encoder.worker.js", import.meta.url);
      urlsToTry.push({ url: workerUrl, description: "import.meta.url dist (.js)" });
    } catch {
      // URL construction failed, skip
    }

    // Priority 6: Fallback paths for various environments
    urlsToTry.push(
      {
        url: "/node_modules/@livepeer-frameworks/streamcrafter-wc/dist/workers/encoder.worker.js",
        description: "wc package dist",
      },
      { url: "/workers/encoder.worker.js", description: "Vite dev server" },
      {
        url: "/node_modules/@livepeer-frameworks/streamcrafter-core/src/workers/encoder.worker.mod.js",
        description: "node_modules source (.mod.js)",
      },
      {
        url: "/node_modules/@livepeer-frameworks/streamcrafter-core/src/workers/encoder.worker.ts",
        description: "node_modules source",
      },
      {
        url: "/node_modules/@livepeer-frameworks/streamcrafter-core/dist/workers/encoder.worker.js",
        description: "node_modules",
      },
      { url: "./workers/encoder.worker.js", description: "relative path" }
    );

    // Try each URL in sequence
    for (const { url, description } of urlsToTry) {
      this.log(`Trying worker path: ${url.toString()} (${description})`);
      const worker = await this.tryCreateWorker(url);
      if (worker) {
        this.log("Worker loaded from:", url.toString());
        return worker;
      }
    }

    throw new Error(
      "Failed to create encoder worker. " +
        "Provide a worker URL via options.workerUrl or ensure workers are served correctly."
    );
  }

  // Synchronous wrapper for backwards compatibility - creates worker optimistically
  private createWorker(): Worker {
    // For sync creation, just try the most likely path
    // The async initialization will handle failures
    if (this.providedWorker) {
      return this.providedWorker;
    }

    if (this.options.workerUrl) {
      return new Worker(this.options.workerUrl, { type: "module" });
    }

    // Try source-relative worker path first (bundlers can rewrite .ts worker URLs)
    try {
      const workerUrl = new URL("../../workers/encoder.worker.js", import.meta.url);
      return new Worker(workerUrl, { type: "module" });
    } catch {
      // Fall through
    }

    // Source .ts fallback
    try {
      const workerUrl = new URL("../workers/encoder.worker.ts", import.meta.url);
      return new Worker(workerUrl, { type: "module" });
    } catch {
      // Fall through
    }

    // Try dist-relative worker path for published ESM builds
    try {
      const workerUrl = new URL("../../workers/encoder.worker.js", import.meta.url).href;
      return new Worker(workerUrl, { type: "module" });
    } catch {
      // Fall through
    }

    // Try common fallback path
    return new Worker("/workers/encoder.worker.js", { type: "module" });
  }

  // ==========================================================================
  // Worker message handling
  // ==========================================================================

  private handleWorkerMessage(message: { type: string; requestId?: string; data?: unknown }): void {
    switch (message.type) {
      case "ready":
        this.log("Worker ready");
        if (message.requestId) {
          this.handleResponse(message.requestId, true);
        }
        this.emit("ready", undefined);
        break;

      case "started":
        this.log("Encoding started");
        if (message.requestId) {
          this.handleResponse(message.requestId, true);
        }
        this.emit("started", undefined);
        break;

      case "stopped":
        this.log("Encoding stopped");
        if (message.requestId) {
          this.handleResponse(message.requestId, true);
        }
        this.emit("stopped", undefined);
        break;

      case "flushed":
        this.log("Encoder flushed");
        if (message.requestId) {
          this.handleResponse(message.requestId, true);
        }
        break;

      case "stats":
        this.stats = message.data as EncoderStats;
        this.emit("stats", this.stats);
        break;

      case "error": {
        const error = message.data as { message: string; fatal: boolean };
        console.error("[EncoderManager] Worker error:", error);
        if (message.requestId) {
          this.handleResponse(message.requestId, false, error.message);
        }
        this.emit("error", error);
        break;
      }

      case "encodedVideoChunk":
        this.emit("videoChunk", message.data as EncodedVideoChunkData);
        break;

      case "encodedAudioChunk":
        this.emit("audioChunk", message.data as EncodedAudioChunkData);
        break;

      default:
        this.log("Unknown message from worker", message);
    }
  }

  // ==========================================================================
  // Public API
  // ==========================================================================

  /**
   * Initialize the encoder with a media stream and config.
   * Creates the worker, sends initialize message, and waits for ready ack.
   */
  async initialize(stream: MediaStream, config: EncoderConfig): Promise<void> {
    this.log("Initializing encoder", config);
    this.config = config;

    // Check for WebCodecs support
    if (typeof VideoEncoder === "undefined" || typeof AudioEncoder === "undefined") {
      throw new Error("WebCodecs not supported in this browser");
    }

    // Check for MediaStreamTrackProcessor support
    if (typeof MediaStreamTrackProcessor === "undefined") {
      throw new Error("MediaStreamTrackProcessor not supported in this browser");
    }

    // Get input tracks
    const videoTrack = stream.getVideoTracks()[0];
    const audioTrack = stream.getAudioTracks()[0];

    // Create track processors for input
    if (videoTrack) {
      this.videoProcessor = new MediaStreamTrackProcessor({ track: videoTrack });
    }

    if (audioTrack) {
      this.audioProcessor = new MediaStreamTrackProcessor({ track: audioTrack });
    }

    // Create worker using async method that tries multiple paths
    this.worker = await this.createWorkerAsync();

    this.worker.onmessage = (event) => {
      this.handleWorkerMessage(event.data);
    };

    this.worker.onerror = (error) => {
      console.error("[EncoderManager] Worker error:", error);
      this.emit("error", { message: error.message, fatal: true });
    };

    // Send initialize message and wait for ready
    const requestId = this.generateRequestId();
    this.log("Sending initialize to worker", { requestId });

    await this.sendRequest({
      type: "initialize",
      requestId,
      data: { config },
    });

    this.isInitialized = true;
    this.log("Worker initialized and ready");
  }

  /**
   * Start encoding.
   * Sends start message, waits for started ack, then begins frame processing.
   */
  async start(): Promise<void> {
    if (!this.isInitialized) {
      throw new Error("EncoderManager not initialized");
    }

    if (this.isRunning) {
      return;
    }

    this.log("Starting encoder");

    // Send start message and wait for started ack
    const requestId = this.generateRequestId();
    await this.sendRequest({
      type: "start",
      requestId,
    });

    this.isRunning = true;

    // Now start frame processing (only after worker is ready)
    if (this.videoProcessor) {
      this.startVideoProcessing();
    }

    if (this.audioProcessor) {
      this.startAudioProcessing();
    }

    this.log("Encoder started, frame processing active");
  }

  /**
   * Start video frame processing loop.
   * Reads frames from MediaStreamTrackProcessor and sends to worker.
   */
  private async startVideoProcessing(): Promise<void> {
    if (!this.videoProcessor || !this.worker) return;

    try {
      this.videoReader = this.videoProcessor.readable.getReader();
      const reader = this.videoReader;

      while (this.isRunning) {
        const { value: frame, done } = await reader.read();

        if (done || !frame) {
          break;
        }

        try {
          // Transfer frame to worker (zero-copy)
          this.worker.postMessage({ type: "videoFrame", data: frame }, [frame]);
        } catch (error) {
          console.error("[EncoderManager] Error sending video frame:", error);
          try {
            frame.close();
          } catch {
            // Ignore
          }
        }
      }
    } catch (error) {
      if (this.isRunning) {
        console.error("[EncoderManager] Video processing error:", error);
      }
    }
  }

  /**
   * Start audio data processing loop.
   */
  private async startAudioProcessing(): Promise<void> {
    if (!this.audioProcessor || !this.worker) return;

    try {
      this.audioReader = this.audioProcessor.readable.getReader();
      const reader = this.audioReader;

      while (this.isRunning) {
        const { value: audioData, done } = await reader.read();

        if (done || !audioData) {
          break;
        }

        try {
          this.worker.postMessage({ type: "audioData", data: audioData }, [audioData]);
        } catch (error) {
          console.error("[EncoderManager] Error sending audio data:", error);
          try {
            audioData.close();
          } catch {
            // Ignore
          }
        }
      }
    } catch (error) {
      if (this.isRunning) {
        console.error("[EncoderManager] Audio processing error:", error);
      }
    }
  }

  /**
   * Stop encoding.
   */
  async stop(): Promise<void> {
    if (!this.isRunning) {
      return;
    }

    this.isRunning = false;
    this.log("Stopping encoder");

    // Cancel readers
    if (this.videoReader) {
      try {
        await this.videoReader.cancel();
        this.videoReader.releaseLock();
      } catch {
        // Ignore
      }
      this.videoReader = null;
    }

    if (this.audioReader) {
      try {
        await this.audioReader.cancel();
        this.audioReader.releaseLock();
      } catch {
        // Ignore
      }
      this.audioReader = null;
    }

    // Tell worker to stop
    if (this.worker && this.isInitialized) {
      const requestId = this.generateRequestId();
      try {
        await this.sendRequest({
          type: "stop",
          requestId,
        });
      } catch (error) {
        this.log("Stop request failed (may be expected)", error);
      }
    }

    this.emit("stopped", undefined);
  }

  /**
   * Update encoder configuration (e.g., bitrate).
   */
  async updateConfig(config: Partial<EncoderConfig>): Promise<void> {
    if (!this.worker || !this.isInitialized) {
      throw new Error("EncoderManager not initialized");
    }

    const requestId = this.generateRequestId();
    await this.sendRequest({
      type: "updateConfig",
      requestId,
      data: config,
    });

    // Update local config
    if (this.config) {
      if (config.video) {
        this.config.video = { ...this.config.video, ...config.video };
      }
      if (config.audio) {
        this.config.audio = { ...this.config.audio, ...config.audio };
      }
    }
  }

  /**
   * Update the input stream (hot-swap tracks).
   * Swaps to new tracks without reinitializing the encoder.
   * Used when sources change while streaming.
   */
  async updateInputStream(stream: MediaStream): Promise<void> {
    if (!this.isInitialized || !this.worker) {
      throw new Error("EncoderManager not initialized");
    }

    const wasRunning = this.isRunning;
    this.log("Updating input stream (hot-swap)", { wasRunning });

    // Cancel existing readers to stop read loops
    if (this.videoReader) {
      try {
        await this.videoReader.cancel();
        this.videoReader.releaseLock();
      } catch {
        // Ignore - reader may already be closed
      }
      this.videoReader = null;
    }

    if (this.audioReader) {
      try {
        await this.audioReader.cancel();
        this.audioReader.releaseLock();
      } catch {
        // Ignore - reader may already be closed
      }
      this.audioReader = null;
    }

    // Create new processors from new tracks
    const videoTrack = stream.getVideoTracks()[0];
    const audioTrack = stream.getAudioTracks()[0];

    if (videoTrack) {
      this.videoProcessor = new MediaStreamTrackProcessor({ track: videoTrack });
    } else {
      this.videoProcessor = null;
    }

    if (audioTrack) {
      this.audioProcessor = new MediaStreamTrackProcessor({ track: audioTrack });
    } else {
      this.audioProcessor = null;
    }

    // Restart processing if we were running
    if (wasRunning) {
      if (this.videoProcessor) {
        this.startVideoProcessing();
      }
      if (this.audioProcessor) {
        this.startAudioProcessing();
      }
      this.log("Input stream updated, processing restarted");
    }
  }

  /**
   * Flush encoder buffers.
   */
  async flush(): Promise<void> {
    if (!this.worker || !this.isInitialized) {
      return;
    }

    const requestId = this.generateRequestId();
    await this.sendRequest({
      type: "flush",
      requestId,
    });
  }

  /**
   * Get current stats.
   */
  getStats(): EncoderStats | null {
    return this.stats;
  }

  /**
   * Get current config.
   */
  getConfig(): EncoderConfig | null {
    return this.config;
  }

  /**
   * Check if encoder is initialized.
   */
  getIsInitialized(): boolean {
    return this.isInitialized;
  }

  /**
   * Check if encoder is running.
   */
  getIsRunning(): boolean {
    return this.isRunning;
  }

  /**
   * Destroy the encoder manager.
   */
  destroy(): void {
    this.stop();
    this.isInitialized = false;

    // Clear processors
    this.videoProcessor = null;
    this.audioProcessor = null;

    // Clear pending requests
    for (const [, pending] of this.pendingRequests) {
      clearTimeout(pending.timer);
      pending.reject(new Error("EncoderManager destroyed"));
    }
    this.pendingRequests.clear();

    // Terminate worker
    if (this.worker) {
      this.worker.terminate();
      this.worker = null;
    }

    this.removeAllListeners();
  }
}

/**
 * Select appropriate H.264 codec profile/level based on resolution and framerate.
 * Uses High Profile with the minimum level that supports the given parameters.
 *
 * H.264 Level capabilities (High Profile):
 * - Level 3.1 (4d001f): 1280x720 @ 30fps, 14 Mbps
 * - Level 4.0 (4d0028): 1920x1080 @ 30fps or 1280x720 @ 60fps, 20 Mbps
 * - Level 5.0 (4d0032): 1920x1080 @ 60fps or 2560x1440 @ 30fps, 25 Mbps
 * - Level 5.1 (4d0033): 2560x1440 @ 60fps or 3840x2160 @ 30fps, 40 Mbps
 * - Level 5.2 (4d0034): 3840x2160 @ 60fps, 60 Mbps
 */
function selectH264Codec(width: number, height: number, framerate: number): string {
  const pixels = width * height;

  // 4K (3840x2160)
  if (pixels >= 3840 * 2160) {
    return framerate > 30 ? "avc1.640034" : "avc1.640033"; // Level 5.2 or 5.1
  }
  // 1440p (2560x1440)
  if (pixels >= 2560 * 1440) {
    return framerate > 30 ? "avc1.640033" : "avc1.640032"; // Level 5.1 or 5.0
  }
  // 1080p (1920x1080)
  if (pixels >= 1920 * 1080) {
    return framerate > 30 ? "avc1.640032" : "avc1.64002a"; // Level 5.0 or 4.2
  }
  // 720p (1280x720)
  if (pixels >= 1280 * 720) {
    return framerate > 30 ? "avc1.64002a" : "avc1.640028"; // Level 4.2 or 4.0
  }
  // Lower resolutions
  return "avc1.64001f"; // Level 3.1 (High Profile)
}

/**
 * Create default encoder config for a quality profile.
 * Optionally merge with encoder overrides from UI.
 * Automatically selects an appropriate H.264 codec based on final resolution/framerate.
 */
export function createEncoderConfig(
  profile: "professional" | "broadcast" | "conference" | "low" = "broadcast",
  overrides?: EncoderOverrides
): EncoderConfig {
  const baseVideo = DEFAULT_VIDEO_SETTINGS[profile];
  const baseAudio = DEFAULT_AUDIO_SETTINGS;

  // Calculate final video settings
  const finalWidth = overrides?.video?.width ?? baseVideo.width;
  const finalHeight = overrides?.video?.height ?? baseVideo.height;
  const finalFramerate = overrides?.video?.framerate ?? baseVideo.framerate;

  // Select appropriate codec for final resolution/framerate
  const codec = selectH264Codec(finalWidth, finalHeight, finalFramerate);

  return {
    video: {
      ...baseVideo,
      codec, // Use dynamically selected codec
      width: finalWidth,
      height: finalHeight,
      framerate: finalFramerate,
      ...(overrides?.video?.bitrate !== undefined && { bitrate: overrides.video.bitrate }),
    },
    audio: {
      ...baseAudio,
      ...(overrides?.audio?.bitrate !== undefined && { bitrate: overrides.audio.bitrate }),
      ...(overrides?.audio?.sampleRate !== undefined && { sampleRate: overrides.audio.sampleRate }),
      ...(overrides?.audio?.numberOfChannels !== undefined && {
        numberOfChannels: overrides.audio.numberOfChannels,
      }),
    },
  };
}

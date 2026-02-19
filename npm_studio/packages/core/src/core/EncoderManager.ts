/**
 * Encoder Manager
 * Manages WebCodecs encoder worker from main thread
 * Handles frame transfer via MediaStreamTrackProcessor
 *
 * Output: Encoded chunks emitted via 'videoChunk' and 'audioChunk' events
 * (for RTCRtpScriptTransform injection)
 */

import { TypedEventEmitter } from "./EventEmitter";
import {
  type VideoCodecFamily,
  getDefaultVideoSettings,
  getDefaultAudioSettings,
  getKeyframeInterval,
  createEncoderConfig as createMultiCodecConfig,
} from "./CodecProfiles";
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
// Default encoder settings (H.264, backward-compatible)
// ============================================================================

export const DEFAULT_VIDEO_SETTINGS: Record<string, VideoEncoderSettings> = {
  professional: getDefaultVideoSettings("professional", "h264"),
  broadcast: getDefaultVideoSettings("broadcast", "h264"),
  conference: getDefaultVideoSettings("conference", "h264"),
  low: getDefaultVideoSettings("low", "h264"),
};

export const DEFAULT_AUDIO_SETTINGS: AudioEncoderSettings = getDefaultAudioSettings("broadcast");

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
   * Feed a VideoFrame directly to the encoder worker, bypassing
   * MediaStreamTrackProcessor. Used by the compositor direct-frame path.
   * Caller transfers ownership — the frame must not be used after this call.
   */
  feedVideoFrame(frame: VideoFrame): void {
    if (!this.worker || !this.isRunning) {
      frame.close();
      return;
    }

    try {
      this.worker.postMessage({ type: "videoFrame", data: frame }, [frame]);
    } catch {
      try {
        frame.close();
      } catch {
        /* already closed */
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
    this.isRunning = false;
    this.isInitialized = false;

    // Cancel readers synchronously (best-effort, worker termination handles the rest)
    if (this.videoReader) {
      try {
        this.videoReader.cancel();
      } catch {}
      this.videoReader = null;
    }
    if (this.audioReader) {
      try {
        this.audioReader.cancel();
      } catch {}
      this.audioReader = null;
    }

    // Clear processors
    this.videoProcessor = null;
    this.audioProcessor = null;

    // Clear pending requests
    for (const [, pending] of this.pendingRequests) {
      clearTimeout(pending.timer);
      pending.reject(new Error("EncoderManager destroyed"));
    }
    this.pendingRequests.clear();

    // Terminate worker (forcefully stops all encoding)
    if (this.worker) {
      this.worker.terminate();
      this.worker = null;
    }

    this.removeAllListeners();
  }
}

/**
 * Create encoder config for a quality profile (defaults to H.264).
 * For multi-codec support, use createMultiCodecEncoderConfig or import from CodecProfiles.
 */
export function createEncoderConfig(
  profile: "professional" | "broadcast" | "conference" | "low" = "broadcast",
  overrides?: EncoderOverrides
): EncoderConfig {
  return createMultiCodecConfig(profile, "h264", overrides);
}

/**
 * Create encoder config with explicit codec family selection.
 * Supports H.264, VP9, and AV1 with codec-appropriate bitrate targets.
 */
export function createMultiCodecEncoderConfig(
  profile: "professional" | "broadcast" | "conference" | "low" = "broadcast",
  codecFamily: VideoCodecFamily = "h264",
  overrides?: EncoderOverrides
): EncoderConfig {
  const config = createMultiCodecConfig(profile, codecFamily, overrides);
  config.keyframeInterval = getKeyframeInterval(codecFamily);
  return config;
}

export { type VideoCodecFamily } from "./CodecProfiles";

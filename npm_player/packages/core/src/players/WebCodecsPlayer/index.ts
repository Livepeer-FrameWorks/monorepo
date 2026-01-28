/**
 * WebCodecs Player Implementation
 *
 * Low-latency WebSocket streaming using WebCodecs API for video/audio decoding.
 * Decoding runs in a Web Worker for optimal performance.
 *
 * Features:
 * - Ultra-low latency streaming (configurable via profiles)
 * - Worker-based VideoDecoder/AudioDecoder
 * - Adaptive playback speed for live catchup/slowdown
 * - Jitter compensation
 * - Firefox polyfill for MediaStreamTrackGenerator
 *
 * Protocol: MistServer raw WebSocket frames (12-byte header + data)
 */

import { BasePlayer } from "../../core/PlayerInterface";
import type {
  StreamSource,
  StreamInfo,
  PlayerOptions,
  PlayerCapability,
} from "../../core/PlayerInterface";
import type {
  TrackInfo,
  CodecDataMessage,
  InfoMessage,
  OnTimeMessage,
  RawChunk,
  WebCodecsPlayerOptions,
  WebCodecsStats,
  MainToWorkerMessage,
  WorkerToMainMessage,
} from "./types";
import { WebSocketController } from "./WebSocketController";
import { SyncController } from "./SyncController";
import { getPresentationTimestamp, isInitData } from "./RawChunkParser";
import { mergeLatencyProfile, selectDefaultProfile } from "./LatencyProfiles";
import {
  createTrackGenerator,
  hasNativeMediaStreamTrackGenerator,
} from "./polyfills/MediaStreamTrackGenerator";

/**
 * Detect if running on Safari (which has VideoTrackGenerator in worker but not MediaStreamTrackGenerator on main thread)
 */
function isSafari(): boolean {
  if (typeof navigator === "undefined") return false;
  const ua = navigator.userAgent;
  return /^((?!chrome|android).)*safari/i.test(ua);
}

// Import inline worker (bundled via rollup-plugin-web-worker-loader)

/**
 * Convert string (ASCII with escaped chars) to Uint8Array
 * Reference: rawws.js:76-84 - init data is raw ASCII from stream info JSON
 */
function str2bin(str: string): Uint8Array {
  const out = new Uint8Array(str.length);
  for (let i = 0; i < str.length; i++) {
    out[i] = str.charCodeAt(i);
  }
  return out;
}

/**
 * Create a TimeRanges-like object from an array of [start, end] pairs
 */
function createTimeRanges(ranges: [number, number][]): TimeRanges {
  return {
    length: ranges.length,
    start(index: number): number {
      if (index < 0 || index >= ranges.length) throw new DOMException("Index out of bounds");
      return ranges[index][0];
    },
    end(index: number): number {
      if (index < 0 || index >= ranges.length) throw new DOMException("Index out of bounds");
      return ranges[index][1];
    },
  };
}

/**
 * Type for requestVideoFrameCallback metadata
 */
interface VideoFrameCallbackMetadata {
  presentationTime: DOMHighResTimeStamp;
  expectedDisplayTime: DOMHighResTimeStamp;
  width: number;
  height: number;
  mediaTime: number;
  presentedFrames: number;
  processingDuration?: number;
}

/**
 * Pipeline state for tracking per-track resources
 */
interface PipelineInfo {
  idx: number;
  track: TrackInfo;
  generator: ReturnType<typeof createTrackGenerator> | null;
  configured: boolean;
  /** Safari audio: writer for audio frames relayed from worker */
  safariAudioWriter?: WritableStreamDefaultWriter<AudioData>;
  /** Safari audio: the audio generator created on main thread */
  safariAudioGenerator?: MediaStreamTrack;
}

/**
 * WebCodecsPlayerImpl - WebCodecs-based low-latency player
 */
export class WebCodecsPlayerImpl extends BasePlayer {
  readonly capability: PlayerCapability = {
    name: "WebCodecs Player",
    shortname: "webcodecs",
    priority: 0, // Highest priority - lowest latency option
    // Raw WebSocket (12-byte header + codec frames) - NOT MP4-muxed
    // MistServer's output_wsraw.cpp provides full codec negotiation (audio + video)
    // MistServer's output_h264.cpp uses same 12-byte header but Annex B payload (video-only)
    // NOTE: ws/video/mp4 is MP4-fragmented which needs MEWS player (uses MSE)
    mimes: [
      "ws/video/raw",
      "wss/video/raw", // Raw codec frames - AVCC format (audio + video)
      "ws/video/h264",
      "wss/video/h264", // Annex B H264/HEVC (video-only, same 12-byte header)
    ],
  };

  private wsController: WebSocketController | null = null;
  private syncController: SyncController | null = null;
  private worker: Worker | null = null;
  private mediaStream: MediaStream | null = null;
  private container: HTMLElement | null = null;
  private pipelines = new Map<number, PipelineInfo>();
  private tracks: TrackInfo[] = [];
  private tracksByIndex = new Map<number, TrackInfo>(); // Track metadata indexed by track idx
  private queuedInitData = new Map<number, Uint8Array>(); // Queued INIT data waiting for track info
  private queuedChunks = new Map<number, RawChunk[]>(); // Queued chunks waiting for decoder config
  private isDestroyed = false;
  private debugging = false;
  private verboseDebugging = false;
  private streamType: "live" | "vod" = "live";
  /** Payload format: 'avcc' for ws/video/raw, 'annexb' for ws/video/h264 */
  private payloadFormat: "avcc" | "annexb" = "avcc";
  private workerUidCounter = 0;
  private workerListeners = new Map<number, (msg: WorkerToMainMessage) => void>();

  // Playback state
  private _duration = Infinity;
  private _currentTime = 0;
  private _bufferMs = 0;
  private _avDrift = 0;
  private _frameCallbackId: number | null = null;
  private _statsInterval: ReturnType<typeof setInterval> | null = null;
  private _framesDropped = 0;
  private _framesDecoded = 0;
  private _bytesReceived = 0;
  private _messagesReceived = 0;
  private _isPaused = true;
  private _suppressPlayPauseSync = false;
  private _onVideoPlay?: () => void;
  private _onVideoPause?: () => void;
  private _pendingStepPause = false;
  private _stepPauseTimeout: ReturnType<typeof setTimeout> | null = null;

  // Codec support cache - keyed by "codec|init_hash"
  private static codecCache = new Map<string, boolean>();

  /**
   * Get cache key for a track's codec configuration
   */
  private static getCodecCacheKey(track: {
    codec: string;
    codecstring?: string;
    init?: string;
  }): string {
    const codecStr = track.codecstring ?? track.codec?.toLowerCase() ?? "";
    // Simple hash of init data for cache key (just first/last bytes + length)
    const init = track.init ?? "";
    const initHash =
      init.length > 0
        ? `${init.length}_${init.charCodeAt(0)}_${init.charCodeAt(init.length - 1)}`
        : "";
    return `${codecStr}|${initHash}`;
  }

  /**
   * Test if a track's codec is supported by WebCodecs
   * Reference: rawws.js:75-137 - isTrackSupported()
   */
  static async isTrackSupported(track: TrackInfo): Promise<{ supported: boolean; config: any }> {
    const cacheKey = WebCodecsPlayerImpl.getCodecCacheKey(track);

    // Check cache first
    if (WebCodecsPlayerImpl.codecCache.has(cacheKey)) {
      const cached = WebCodecsPlayerImpl.codecCache.get(cacheKey)!;
      return { supported: cached, config: { codec: track.codecstring ?? track.codec } };
    }

    // Build codec config
    const codecStr = track.codecstring ?? (track.codec ?? "").toLowerCase();
    const config: any = { codec: codecStr };

    // Add description (init data) if present
    if (track.init && track.init !== "") {
      config.description = str2bin(track.init);
    }

    let result: { supported: boolean; config: any };

    try {
      switch (track.type) {
        case "video": {
          // Special handling for JPEG - uses ImageDecoder
          if (track.codec === "JPEG") {
            if (!("ImageDecoder" in window)) {
              result = { supported: false, config: { codec: "image/jpeg" } };
            } else {
              // @ts-ignore - ImageDecoder may not have types
              const isSupported = await (window as any).ImageDecoder.isTypeSupported("image/jpeg");
              result = { supported: isSupported, config: { codec: "image/jpeg" } };
            }
          } else {
            // Use VideoDecoder.isConfigSupported()
            const videoResult = await VideoDecoder.isConfigSupported(config as VideoDecoderConfig);
            result = { supported: videoResult.supported === true, config: videoResult.config };
          }
          break;
        }
        case "audio": {
          // Audio requires numberOfChannels and sampleRate
          config.numberOfChannels = track.channels ?? 2;
          config.sampleRate = track.rate ?? 48000;
          const audioResult = await AudioDecoder.isConfigSupported(config as AudioDecoderConfig);
          result = { supported: audioResult.supported === true, config: audioResult.config };
          break;
        }
        default:
          result = { supported: false, config };
      }
    } catch (err) {
      console.warn(`[WebCodecs] isConfigSupported failed for ${track.codec}:`, err);
      result = { supported: false, config };
    }

    // Cache the result
    WebCodecsPlayerImpl.codecCache.set(cacheKey, result.supported);
    return result;
  }

  /**
   * Validate all tracks and return which are supported
   * Returns array of supported track types ('video', 'audio')
   */
  static async validateTracks(tracks: TrackInfo[]): Promise<string[]> {
    const supportedTypes: Set<string> = new Set();

    const validationPromises = tracks
      .filter((t) => t.type === "video" || t.type === "audio")
      .map(async (track) => {
        const result = await WebCodecsPlayerImpl.isTrackSupported(track);
        if (result.supported) {
          supportedTypes.add(track.type);
        }
        return { track, supported: result.supported };
      });

    const results = await Promise.all(validationPromises);

    // Log validation results for debugging
    for (const { track, supported } of results) {
      console.debug(
        `[WebCodecs] Track ${track.idx} (${track.type} ${track.codec}): ${supported ? "supported" : "UNSUPPORTED"}`
      );
    }

    return Array.from(supportedTypes);
  }

  isMimeSupported(mimetype: string): boolean {
    return this.capability.mimes.includes(mimetype);
  }

  isBrowserSupported(
    mimetype: string,
    source: StreamSource,
    streamInfo: StreamInfo
  ): boolean | string[] {
    // Basic requirements
    if (!("WebSocket" in window)) {
      return false;
    }
    if (!("Worker" in window)) {
      return false;
    }
    if (!("VideoDecoder" in window) || !("AudioDecoder" in window)) {
      // WebCodecs not available (requires HTTPS)
      return false;
    }

    // Check for HTTP/HTTPS mismatch
    const sourceUrl = new URL(source.url.replace(/^ws/, "http"), location.href);
    if (location.protocol === "https:" && sourceUrl.protocol === "http:") {
      return false;
    }

    // Check track codec support using cache when available
    // Reference: rawws.js tests codecs via isConfigSupported() before selection
    const playableTracks: Record<string, boolean> = {};

    for (const track of streamInfo.meta.tracks) {
      if (track.type === "video" || track.type === "audio") {
        // Check cache for this track's codec
        const cacheKey = WebCodecsPlayerImpl.getCodecCacheKey(track as any);
        if (WebCodecsPlayerImpl.codecCache.has(cacheKey)) {
          // Use cached result
          if (WebCodecsPlayerImpl.codecCache.get(cacheKey)) {
            playableTracks[track.type] = true;
          }
        } else {
          // Not in cache - assume supported for now, validate in initialize()
          // This is necessary because isBrowserSupported is synchronous
          playableTracks[track.type] = true;
        }
      } else if (track.type === "meta" && track.codec === "subtitle") {
        // Subtitles supported via text track
        playableTracks["subtitle"] = true;
      }
    }

    // Annex B H264 WebSocket is video-only (no audio payloads)
    if (mimetype.includes("video/h264")) {
      delete playableTracks.audio;
    }

    if (Object.keys(playableTracks).length === 0) {
      return false;
    }

    return Object.keys(playableTracks);
  }

  async initialize(
    container: HTMLElement,
    source: StreamSource,
    options: PlayerOptions,
    streamInfo?: StreamInfo
  ): Promise<HTMLVideoElement> {
    // Clear any leftover state from previous initialization FIRST
    // This fixes race condition where async destroy() clears state after new initialize()
    this.tracksByIndex.clear();
    this.pipelines.clear();
    this.tracks = [];
    this.queuedInitData.clear();
    this.queuedChunks.clear();
    this.isDestroyed = false;
    this._duration = Infinity;
    this._currentTime = 0;
    this._bufferMs = 0;
    this._avDrift = 0;
    this._framesDropped = 0;
    this._framesDecoded = 0;
    this._bytesReceived = 0;
    this._messagesReceived = 0;

    // Detect payload format from source MIME type
    // ws/video/h264 uses Annex B (start code delimited NALs), ws/video/raw uses AVCC (length-prefixed)
    this.payloadFormat = source.type?.includes("h264") ? "annexb" : "avcc";
    if (this.payloadFormat === "annexb") {
      this.log("Using Annex B payload format (ws/video/h264)");
    }

    this.container = container;
    container.classList.add("fw-player-container");

    // Pre-populate track metadata from streamInfo (fetched via HTTP before WebSocket)
    // This is how the reference player (rawws.js) gets track info - from MistVideo.info.meta.tracks
    if (streamInfo?.meta?.tracks) {
      this.log(`Pre-populating ${streamInfo.meta.tracks.length} tracks from streamInfo`);
      for (const track of streamInfo.meta.tracks) {
        if (track.idx !== undefined) {
          // Convert StreamTrack to TrackInfo (WebCodecs format)
          const trackInfo: TrackInfo = {
            idx: track.idx,
            type: track.type as TrackInfo["type"],
            codec: track.codec,
            codecstring: track.codecstring,
            init: track.init,
            width: track.width,
            height: track.height,
            fpks: track.fpks,
            channels: track.channels,
            rate: track.rate,
            size: track.size,
          };
          this.tracksByIndex.set(track.idx, trackInfo);
          this.log(`Pre-registered track ${track.idx}: ${track.type} ${track.codec}`);
        }
      }
    }

    // Parse WebCodecs-specific options
    const wcOptions = options as PlayerOptions & WebCodecsPlayerOptions;
    this.debugging = wcOptions.debug ?? wcOptions.devMode ?? false;
    this.verboseDebugging = wcOptions.verboseDebug ?? false;

    // Determine stream type
    this.streamType = (source as any).type === "live" ? "live" : "vod";

    // Select latency profile
    const profileName =
      wcOptions.latencyProfile ?? selectDefaultProfile(this.streamType === "live");
    const profile = mergeLatencyProfile(profileName, wcOptions.customLatencyProfile);

    this.log(`Initializing WebCodecs player with ${profile.name} profile`);

    // Create video element
    const video = document.createElement("video");
    video.classList.add("fw-player-video");
    video.setAttribute("playsinline", "");
    video.setAttribute("crossorigin", "anonymous");

    if (options.autoplay) video.autoplay = true;
    if (options.muted) video.muted = true;
    video.controls = options.controls === true;
    if (options.loop && this.streamType !== "live") video.loop = true;
    if (options.poster) video.poster = options.poster;

    this.videoElement = video;
    container.appendChild(video);

    // Keep paused state in sync with actual element state
    this._onVideoPlay = () => {
      if (this._suppressPlayPauseSync) return;
      this._isPaused = false;
      this.sendToWorker({
        type: "frametiming",
        action: "setPaused",
        paused: false,
        uid: this.workerUidCounter++,
      }).catch(() => {});
    };
    this._onVideoPause = () => {
      if (this._suppressPlayPauseSync) return;
      this._isPaused = true;
      this.sendToWorker({
        type: "frametiming",
        action: "setPaused",
        paused: true,
        uid: this.workerUidCounter++,
      }).catch(() => {});
    };
    video.addEventListener("play", this._onVideoPlay);
    video.addEventListener("pause", this._onVideoPause);

    // Create MediaStream for output
    this.mediaStream = new MediaStream();
    video.srcObject = this.mediaStream;

    // Initialize worker
    await this.initializeWorker();

    // Initialize sync controller
    this.syncController = new SyncController({
      profile,
      isLive: this.streamType === "live",
      onSpeedChange: (main, tweak) => {
        this.sendToWorker({
          type: "frametiming",
          action: "setSpeed",
          speed: main,
          tweak,
          uid: this.workerUidCounter++,
        });
        if (this.videoElement) {
          this.videoElement.playbackRate = main * tweak;
        }
      },
      onFastForwardRequest: (ms) => {
        this.wsController?.fastForward(ms);
      },
    });

    // Initialize WebSocket - URL should already be .raw from source selection
    this.wsController = new WebSocketController(source.url, {
      debug: this.debugging,
    });

    this.setupWebSocketHandlers();

    // Validate track codecs using isConfigSupported() BEFORE connecting
    // Reference: rawws.js:75-137 tests each track's codec support
    // This fixes "codec unsupported" errors by only sending verified codecs
    const supportedAudioCodecs: Set<string> = new Set();
    const supportedVideoCodecs: Set<string> = new Set();

    if (streamInfo?.meta?.tracks) {
      this.log("Validating track codecs with isConfigSupported()...");

      for (const track of streamInfo.meta.tracks) {
        if (track.type === "video" || track.type === "audio") {
          const trackInfo: TrackInfo = {
            idx: track.idx ?? 0,
            type: track.type as "video" | "audio",
            codec: track.codec,
            codecstring: track.codecstring,
            init: track.init,
            width: track.width,
            height: track.height,
            channels: track.channels,
            rate: track.rate,
          };

          const result = await WebCodecsPlayerImpl.isTrackSupported(trackInfo);
          if (result.supported) {
            if (track.type === "audio") {
              supportedAudioCodecs.add(track.codec);
            } else {
              supportedVideoCodecs.add(track.codec);
            }
            this.log(`Track ${track.idx} (${track.type} ${track.codec}): SUPPORTED`);
          } else {
            this.log(`Track ${track.idx} (${track.type} ${track.codec}): NOT SUPPORTED`, "warn");
          }
        }
      }
    }

    // If no codecs validated, check if we have any tracks at all
    if (supportedAudioCodecs.size === 0 && supportedVideoCodecs.size === 0) {
      // Fallback: Use default codec list if no tracks provided or all failed
      // This handles streams where track info isn't available until WebSocket connects
      this.log("No validated codecs, using default codec list");
      ["AAC", "MP3", "opus", "FLAC", "AC3"].forEach((c) => supportedAudioCodecs.add(c));
      ["H264", "HEVC", "VP8", "VP9", "AV1", "JPEG"].forEach((c) => supportedVideoCodecs.add(c));
    }

    // Connect and request codec data
    // Per MistServer rawws.js line 1544, we need to tell the server what codecs we support
    // Format: [[ [audio codecs], [video codecs] ]] - audio FIRST per Object.values({audio:[], video:[]}) order
    const supportedCombinations: string[][][] = [
      [
        Array.from(supportedAudioCodecs), // Audio codecs (position 0)
        Array.from(supportedVideoCodecs), // Video codecs (position 1)
      ],
    ];

    this.log(
      `Requesting codecs: audio=[${supportedCombinations[0][0].join(", ")}], video=[${supportedCombinations[0][1].join(", ")}]`
    );

    try {
      await this.wsController.connect();
      this.wsController.requestCodecData(supportedCombinations);
    } catch (err) {
      this.log(`Failed to connect: ${err}`, "error");
      this.emit("error", err instanceof Error ? err : new Error(String(err)));
      throw err;
    }

    // Proactively create pipelines for pre-populated tracks
    // This ensures pipelines exist when first chunks arrive, they just need init data
    for (const [idx, track] of this.tracksByIndex) {
      if (track.type === "video" || track.type === "audio") {
        this.log(`Creating pipeline proactively for track ${idx} (${track.type} ${track.codec})`);
        await this.createPipeline(track);
      }
    }

    // Set up video event listeners
    this.setupVideoEventListeners(video, options);

    // Set up requestVideoFrameCallback for accurate frame timing
    this.setupFrameCallback();

    this.isDestroyed = false;
    return video;
  }

  async destroy(): Promise<void> {
    if (this.isDestroyed) return;
    this.isDestroyed = true;

    this.log("Destroying WebCodecs player");

    // Cancel frame callback
    this.cancelFrameCallback();

    // Stop stats interval
    if (this._statsInterval) {
      clearInterval(this._statsInterval);
      this._statsInterval = null;
    }

    // Stop WebSocket
    this.wsController?.disconnect();
    this.wsController = null;

    // Close all pipelines
    for (const pipeline of this.pipelines.values()) {
      await this.closePipeline(pipeline.idx, false);
    }
    this.pipelines.clear();

    // Terminate worker
    this.worker?.terminate();
    this.worker = null;
    this.workerListeners.clear();

    // Clean up MediaStream
    if (this.mediaStream) {
      for (const track of this.mediaStream.getTracks()) {
        track.stop();
        this.mediaStream.removeTrack(track);
      }
      this.mediaStream = null;
    }

    // Clean up video element
    if (this.videoElement) {
      if (this._onVideoPlay) {
        this.videoElement.removeEventListener("play", this._onVideoPlay);
        this._onVideoPlay = undefined;
      }
      if (this._onVideoPause) {
        this.videoElement.removeEventListener("pause", this._onVideoPause);
        this._onVideoPause = undefined;
      }
      if (this._stepPauseTimeout) {
        clearTimeout(this._stepPauseTimeout);
        this._stepPauseTimeout = null;
      }
      this._pendingStepPause = false;
      this.videoElement.srcObject = null;
      this.videoElement.remove();
      this.videoElement = null;
    }

    this.syncController = null;
    // NOTE: Don't clear tracks/tracksByIndex/queues here!
    // Since PlayerManager reuses instances, a concurrent initialize() may have
    // already pre-populated these. Clearing happens at the START of initialize().
  }

  // ============================================================================
  // Worker Management
  // ============================================================================

  /**
   * Try to load a worker from a URL with proper async error detection.
   * new Worker() doesn't throw on invalid URLs - it fires error events async.
   */
  private tryLoadWorker(url: string): Promise<Worker> {
    return new Promise((resolve, reject) => {
      let worker: Worker;
      try {
        worker = new Worker(url, { type: "module" });
      } catch (e) {
        reject(e);
        return;
      }

      const cleanup = () => {
        clearTimeout(timeout);
        worker.removeEventListener("error", onError);
        worker.removeEventListener("message", onMessage);
      };

      const onError = (e: ErrorEvent) => {
        cleanup();
        worker.terminate();
        reject(new Error(e.message || "Worker failed to load"));
      };

      const onMessage = () => {
        cleanup();
        resolve(worker);
      };

      // Timeout: if no error after 500ms, assume loaded (worker may not send immediate message)
      const timeout = setTimeout(() => {
        cleanup();
        resolve(worker);
      }, 500);

      worker.addEventListener("error", onError);
      worker.addEventListener("message", onMessage);
    });
  }

  private async initializeWorker(): Promise<void> {
    // Worker paths to try in order:
    // 1. Dev server path (Vite plugin serves /workers/* from source)
    // 2. Production npm package path (relative to built module)
    const paths = ["/workers/decoder.worker.js"];

    // Add production path (may fail in dev but that's ok)
    try {
      paths.push(new URL("../workers/decoder.worker.js", import.meta.url).href);
    } catch {
      // import.meta.url may not work in all environments
    }

    let lastError: Error | null = null;
    for (const path of paths) {
      try {
        this.log(`Trying worker path: ${path}`);
        this.worker = await this.tryLoadWorker(path);
        this.log(`Worker loaded from: ${path}`);
        break;
      } catch (e) {
        lastError = e instanceof Error ? e : new Error(String(e));
        this.log(`Worker path failed: ${path} - ${lastError.message}`, "warn");
      }
    }

    if (!this.worker) {
      throw new Error(
        "Failed to initialize WebCodecs worker. " + `Last error: ${lastError?.message ?? "unknown"}`
      );
    }

    // Set up worker event handlers (replace the ones from tryLoadWorker)
    this.worker.onmessage = (event: MessageEvent<WorkerToMainMessage>) => {
      this.handleWorkerMessage(event.data);
    };

    this.worker.onerror = (err) => {
      this.log(`Worker error: ${err?.message ?? "unknown error"}`, "error");
      this.emit("error", new Error(`Worker error: ${err?.message ?? "unknown"}`));
    };

    // Configure debugging mode in worker
    this.sendToWorker({
      type: "debugging",
      value: this.verboseDebugging ? "verbose" : this.debugging,
      uid: this.workerUidCounter++,
    });
  }

  private sendToWorker(
    msg: MainToWorkerMessage & { uid: number },
    transfer?: Transferable[]
  ): Promise<WorkerToMainMessage> {
    return new Promise((resolve, reject) => {
      // Reject with proper error if destroyed or no worker
      // This prevents silent failures and allows callers to handle errors appropriately
      if (this.isDestroyed) {
        reject(new Error("Player destroyed"));
        return;
      }
      if (!this.worker) {
        reject(new Error("Worker not initialized"));
        return;
      }

      const uid = msg.uid;

      // Register listener for response
      this.workerListeners.set(uid, (response) => {
        this.workerListeners.delete(uid);
        if (response.type === "ack" && response.status === "error") {
          reject(new Error(response.error));
        } else {
          resolve(response);
        }
      });

      if (transfer) {
        this.worker.postMessage(msg, transfer);
      } else {
        this.worker.postMessage(msg);
      }
    });
  }

  private handleWorkerMessage(msg: WorkerToMainMessage): void {
    // Check for specific listener
    if (msg.uid !== undefined && this.workerListeners.has(msg.uid)) {
      this.workerListeners.get(msg.uid)!(msg);
    }

    // Handle message by type
    switch (msg.type) {
      case "addtrack": {
        const pipeline = this.pipelines.get(msg.idx);
        if (pipeline && this.mediaStream) {
          // If track was created in worker (Safari), use it directly
          if (msg.track) {
            this.mediaStream.addTrack(msg.track);
          } else if (pipeline.generator) {
            // Otherwise use generator's track
            this.mediaStream.addTrack(pipeline.generator.getTrack());
          }
        }
        break;
      }

      case "removetrack": {
        const pipeline = this.pipelines.get(msg.idx);
        if (pipeline?.generator && this.mediaStream) {
          const track = pipeline.generator.getTrack();
          this.mediaStream.removeTrack(track);
        }
        break;
      }

      case "setplaybackrate": {
        if (this.videoElement) {
          this.videoElement.playbackRate = msg.speed;
        }
        break;
      }

      case "sendevent": {
        if (msg.kind === "timeupdate") {
          if (this._pendingStepPause) {
            this.finishStepPause();
          }
          if (typeof msg.time === "number" && Number.isFinite(msg.time)) {
            this._currentTime = msg.time;
            this.emit("timeupdate", this._currentTime);
          } else if (this.videoElement) {
            this.emit("timeupdate", this.videoElement.currentTime);
          }
        } else if (msg.kind === "error") {
          this.emit("error", new Error(msg.message ?? "Unknown error"));
        }
        break;
      }

      case "writeframe": {
        // Safari audio: worker sends frames via postMessage, we write them here
        // Reference: rawws.js line 897-918
        const pipeline = this.pipelines.get(msg.idx);
        if (pipeline?.safariAudioWriter) {
          const frame = msg.frame;
          const frameUid = msg.uid;
          pipeline.safariAudioWriter
            .write(frame)
            .then(() => {
              this.worker?.postMessage({
                type: "writeframe",
                idx: msg.idx,
                uid: frameUid,
                status: "ok",
              });
            })
            .catch((err: Error) => {
              this.worker?.postMessage({
                type: "writeframe",
                idx: msg.idx,
                uid: frameUid,
                status: "error",
                error: err.message,
              });
            });
        } else {
          this.worker?.postMessage({
            type: "writeframe",
            idx: msg.idx,
            uid: msg.uid,
            status: "error",
            error: "Pipeline not active or no audio writer",
          });
        }
        break;
      }

      case "log": {
        if (this.debugging) {
          const level = (msg as any).level ?? "info";
          const logFn =
            level === "error" ? console.error : level === "warn" ? console.warn : console.log;
          logFn(`[WebCodecs Worker] ${msg.msg}`);
        }
        break;
      }

      case "stats": {
        // Could emit stats for monitoring
        break;
      }

      case "closed": {
        this.pipelines.delete(msg.idx);
        break;
      }
    }
  }

  // ============================================================================
  // WebSocket Handlers
  // ============================================================================

  private setupWebSocketHandlers(): void {
    if (!this.wsController) return;

    this.wsController.on("codecdata", (msg) => this.handleCodecData(msg));
    this.wsController.on("info", (msg) => this.handleInfo(msg));
    this.wsController.on("ontime", (msg) => this.handleOnTime(msg));
    this.wsController.on("tracks", (tracks) => this.handleTracksChange(tracks));
    this.wsController.on("chunk", (chunk) => this.handleChunk(chunk));
    this.wsController.on("stop", () => this.handleStop());
    this.wsController.on("error", (err) => this.handleError(err));
    this.wsController.on("statechange", (state) => {
      this.log(`Connection state: ${state}`);
      if (state === "error") {
        this.emit("error", new Error("WebSocket connection failed"));
      }
    });
  }

  private async handleCodecData(msg: CodecDataMessage): Promise<void> {
    const codecs = msg.codecs ?? [];
    const trackIndices = msg.tracks ?? []; // Array of track indices (numbers), NOT TrackInfo
    this.log(
      `Received codec data: codecs=[${codecs.join(", ") || "none"}], tracks=[${trackIndices.join(", ") || "none"}]`
    );

    if (codecs.length === 0 || trackIndices.length === 0) {
      this.log("No playable codecs/tracks selected by server", "warn");
      // Still start playback - info message may populate tracks later
      this.wsController?.play();
      return;
    }

    // Store codec strings by track index for later lookup
    // Per rawws.js: codecs[i] corresponds to tracks[i]
    for (let i = 0; i < trackIndices.length; i++) {
      const trackIdx = trackIndices[i];
      const codec = codecs[i];
      if (codec) {
        // If we have track metadata from info message, update it with codec
        const existingTrack = this.tracksByIndex.get(trackIdx);
        if (existingTrack) {
          existingTrack.codec = codec;
        } else {
          // Create minimal track info - will be filled in by info message
          this.tracksByIndex.set(trackIdx, {
            idx: trackIdx,
            type: codec.match(/^(H264|HEVC|VP[89]|AV1|JPEG)/i)
              ? "video"
              : codec.match(/^(AAC|MP3|opus|FLAC|AC3|pcm)/i)
                ? "audio"
                : "meta",
            codec,
          });
        }
        this.log(`Track ${trackIdx}: codec=${codec}`);
      }
    }

    // Create pipelines for selected tracks that have metadata
    for (const trackIdx of trackIndices) {
      const track = this.tracksByIndex.get(trackIdx);
      if (track && (track.type === "video" || track.type === "audio")) {
        await this.createPipeline(track);
      }
    }

    // Start playback
    this.wsController?.play();
  }

  /**
   * Handle stream info message containing track metadata
   * This is sent by MistServer with full track information
   */
  private async handleInfo(msg: InfoMessage): Promise<void> {
    this.log("Received stream info");

    // Extract tracks from meta.tracks object
    if (msg.meta?.tracks) {
      const tracksObj = msg.meta.tracks;
      this.log(`Info contains ${Object.keys(tracksObj).length} tracks`);

      for (const [_name, track] of Object.entries(tracksObj)) {
        // Store track by its index for lookup when chunks arrive
        if (track.idx !== undefined) {
          this.tracksByIndex.set(track.idx, track);
          this.log(`Registered track ${track.idx}: ${track.type} ${track.codec}`);

          // Process any queued init data for this track
          if (this.queuedInitData.has(track.idx)) {
            if (track.type === "video" || track.type === "audio") {
              this.log(`Processing queued INIT data for track ${track.idx}`);
              await this.createPipeline(track);
              const initData = this.queuedInitData.get(track.idx)!;
              this.configurePipeline(track.idx, initData);
              this.queuedInitData.delete(track.idx);
            }
          }
        }
      }

      // Also update tracks array
      this.tracks = Object.values(tracksObj);
    }
  }

  private handleOnTime(msg: OnTimeMessage): void {
    // Update sync controller with server time
    this.syncController?.updateServerTime(msg.current);

    // Update current time if no frame callback available
    if (this._frameCallbackId === null) {
      this._currentTime = msg.current;
    }

    // Record server delay
    const delay = this.wsController?.getServerDelay() ?? 0;
    if (delay > 0) {
      this.syncController?.recordServerDelay(delay);
    }

    // Update duration from server (VOD streams have finite duration)
    if (msg.total !== undefined && isFinite(msg.total) && msg.total > 0) {
      this._duration = msg.total;
    }

    // Update buffer level
    const syncState = this.syncController?.getState();
    if (syncState) {
      this._bufferMs = syncState.buffer.current;
    }

    // Create pipelines for tracks mentioned in on_time.tracks (like reference player)
    if (msg.tracks && msg.tracks.length > 0) {
      for (const trackIdx of msg.tracks) {
        if (!this.pipelines.has(trackIdx)) {
          const track = this.tracksByIndex.get(trackIdx);
          if (track && (track.type === "video" || track.type === "audio")) {
            this.log(
              `Creating pipeline from on_time for track ${track.idx} (${track.type} ${track.codec})`
            );
            this.createPipeline(track).then(() => {
              // Process any queued init data
              const queuedInit = this.queuedInitData.get(track.idx);
              if (queuedInit) {
                this.configurePipeline(track.idx, queuedInit);
                this.queuedInitData.delete(track.idx);
              }
            });
          }
        }
      }
    }
  }

  private async handleTracksChange(tracks: TrackInfo[]): Promise<void> {
    this.log(`Tracks changed: ${tracks.map((t) => `${t.idx}:${t.type}`).join(", ")}`);

    // Check if codecs changed
    const newTrackIds = new Set(tracks.map((t) => t.idx));
    const oldTrackIds = new Set(this.pipelines.keys());

    // Remove old pipelines
    for (const idx of oldTrackIds) {
      if (!newTrackIds.has(idx)) {
        await this.closePipeline(idx, true);
      }
    }

    // Update tracksByIndex and create new pipelines
    for (const track of tracks) {
      this.tracksByIndex.set(track.idx, track);

      if (track.type === "video" || track.type === "audio") {
        if (!this.pipelines.has(track.idx)) {
          await this.createPipeline(track);
        }
      }
    }

    this.tracks = tracks;
  }

  private handleChunk(chunk: RawChunk): void {
    if (this.isDestroyed) return;

    const pipeline = this.pipelines.get(chunk.trackIndex);

    // Create pipeline if missing - look up track from tracksByIndex (populated by info message)
    if (!pipeline) {
      const track = this.tracksByIndex.get(chunk.trackIndex);

      // If track info not available, try to infer from chunk type
      // MistServer track indices: video typically 1, audio typically 2, meta typically 9
      if (!track) {
        // INIT data for an unknown track - we need to infer the track type
        // For now, create a placeholder track entry based on common MistServer patterns
        if (isInitData(chunk)) {
          this.log(`Received INIT for unknown track ${chunk.trackIndex}, queuing for later`);
          // Queue the init data - it will be processed when track info becomes available
          this.queuedInitData.set(chunk.trackIndex, chunk.data);
          return;
        }

        // For regular chunks without track info, we can't decode without codec config
        this.log(`Received chunk for unknown track ${chunk.trackIndex} without track info`, "warn");
        return;
      }

      if (track.type === "video" || track.type === "audio") {
        this.log(
          `Creating pipeline for discovered track ${track.idx} (${track.type} ${track.codec})`
        );
        this.createPipeline(track).then(() => {
          if (this.isDestroyed) return; // Guard against async completion after destroy
          // Process any queued init data for this track
          const queuedInit = this.queuedInitData.get(track!.idx);
          if (queuedInit) {
            this.configurePipeline(track!.idx, queuedInit);
            this.queuedInitData.delete(track!.idx);
          }
          // Re-process this chunk now that pipeline exists
          this.handleChunk(chunk);
        });
      }
      return;
    }

    // Handle init data
    if (isInitData(chunk)) {
      this.configurePipeline(pipeline.idx, chunk.data);
      return;
    }

    // Queue chunks until pipeline is configured (decoder needs init data first)
    // Per rawws.js: frames are queued when decoder is "unconfigured" (line 1408-1410)
    if (!pipeline.configured) {
      // For AUDIO tracks: configure on FIRST frame (audio doesn't have key/delta distinction)
      // Audio chunks are sent as type 0 (delta) by the server even though they're independent
      // Reference: rawws.js line 768-769 forces audio type to 'key'
      const isAudioTrack = pipeline.track.type === "audio";

      // For VIDEO tracks: wait for KEY frame before configuring
      // This handles Annex B streams where SPS/PPS is inline with keyframes
      const shouldConfigure = isAudioTrack || chunk.type === "key";

      if (shouldConfigure) {
        this.log(
          `Received ${chunk.type.toUpperCase()} frame for unconfigured ${pipeline.track.type} track ${chunk.trackIndex}, configuring`
        );

        // Queue this frame at the FRONT so it's sent before any DELTAs
        if (!this.queuedChunks.has(chunk.trackIndex)) {
          this.queuedChunks.set(chunk.trackIndex, []);
        }
        this.queuedChunks.get(chunk.trackIndex)!.unshift(chunk);

        // Configure without description (or with description from track.init if available)
        // For audio codecs like opus/mp3 that don't need init data, this works fine
        // For AAC, the description should come from track.init or the server will send INIT
        const initData = pipeline.track.init ? str2bin(pipeline.track.init) : new Uint8Array(0);
        this.configurePipeline(chunk.trackIndex, initData).catch((err) => {
          this.log(`Failed to configure track ${chunk.trackIndex}: ${err}`, "error");
        });
        return;
      }

      // Otherwise queue the chunk (video delta before first keyframe)
      if (!this.queuedChunks.has(chunk.trackIndex)) {
        this.queuedChunks.set(chunk.trackIndex, []);
      }
      this.queuedChunks.get(chunk.trackIndex)!.push(chunk);
      if (this.verboseDebugging) {
        this.log(`Queued chunk for track ${chunk.trackIndex} (waiting for decoder config)`);
      }
      return;
    }

    // Track jitter
    this.syncController?.recordChunkArrival(chunk.trackIndex, chunk.timestamp);

    // Send to worker for decoding
    this.sendChunkToWorker(chunk);
  }

  private sendChunkToWorker(chunk: RawChunk): void {
    const msg: MainToWorkerMessage = {
      type: "receive",
      idx: chunk.trackIndex,
      chunk: {
        type: chunk.type === "key" ? "key" : "delta",
        timestamp: getPresentationTimestamp(chunk),
        data: chunk.data,
      },
      uid: this.workerUidCounter++,
    };

    this.worker?.postMessage(msg, [chunk.data.buffer]);
  }

  private handleStop(): void {
    this.log("Stream stopped");
    this.emit("ended", undefined);
  }

  private handleError(err: Error): void {
    this.log(`WebSocket error: ${err.message}`, "error");
    this.emit("error", err);
  }

  // ============================================================================
  // Pipeline Management
  // ============================================================================

  private async createPipeline(track: TrackInfo): Promise<void> {
    if (this.pipelines.has(track.idx)) return;

    this.log(`Creating pipeline for track ${track.idx} (${track.type} ${track.codec})`);

    const pipeline: PipelineInfo = {
      idx: track.idx,
      track,
      generator: null,
      configured: false,
    };

    this.pipelines.set(track.idx, pipeline);
    this.syncController?.addTrack(track.idx, track);

    // Create worker pipeline
    await this.sendToWorker({
      type: "create",
      idx: track.idx,
      track,
      opts: {
        optimizeForLatency: this.streamType === "live",
        payloadFormat: this.payloadFormat, // 'avcc' for ws/video/raw, 'annexb' for ws/video/h264
      },
      uid: this.workerUidCounter++,
    });

    // Create track generator - three paths:
    // 1. Chrome/Edge: MediaStreamTrackGenerator on main thread, transfer writable to worker
    // 2. Safari: VideoTrackGenerator in worker (video) or frame relay (audio)
    // 3. Firefox: Use canvas/AudioWorklet polyfill
    if (hasNativeMediaStreamTrackGenerator()) {
      // Chrome/Edge: Create generator and transfer writable to worker
      // @ts-ignore
      const generator = new MediaStreamTrackGenerator({ kind: track.type });
      pipeline.generator = {
        writable: generator.writable,
        getTrack: () => generator,
        close: () => generator.stop?.(),
      };

      await this.sendToWorker(
        {
          type: "setwritable",
          idx: track.idx,
          writable: generator.writable,
          uid: this.workerUidCounter++,
        },
        [generator.writable]
      );
    } else if (isSafari()) {
      // Safari: Worker uses VideoTrackGenerator (video) or frame relay (audio)
      // Reference: rawws.js line 1012-1037
      this.log(`Safari detected - using worker-based track generator for ${track.type}`);

      if (track.type === "audio") {
        // Safari audio: create generator on main thread, frames relayed from worker
        // @ts-ignore - Safari has MediaStreamTrackGenerator for audio
        if (typeof MediaStreamTrackGenerator !== "undefined") {
          // @ts-ignore
          const audioGen = new MediaStreamTrackGenerator({ kind: "audio" });
          pipeline.safariAudioGenerator = audioGen;
          pipeline.safariAudioWriter = audioGen.writable.getWriter();

          // Add track to stream
          if (this.mediaStream) {
            this.mediaStream.addTrack(audioGen);
          }
        }
      }

      // Ask worker to create generator (video uses VideoTrackGenerator, audio sets up relay)
      await this.sendToWorker({
        type: "creategenerator",
        idx: track.idx,
        uid: this.workerUidCounter++,
      });
    } else {
      // Firefox/other: Use canvas/AudioWorklet polyfill
      pipeline.generator = createTrackGenerator(track.type as "video" | "audio");

      if (pipeline.generator.waitForInit) {
        await pipeline.generator.waitForInit();
      }

      // For polyfill, writable stays on main thread
      // Worker would need different architecture - for now, fall back to main thread decode
      this.log("Using MediaStreamTrackGenerator polyfill - main thread decode");

      // Add track to stream directly
      if (this.mediaStream && pipeline.generator) {
        this.mediaStream.addTrack(pipeline.generator.getTrack());
      }
    }

    // Per rawws.js: Do NOT configure from HTTP info automatically.
    // Wait for WebSocket binary INIT frames to configure decoders.
    // This ensures we use the exact init data the server sends for this session.
    //
    // However, if track.init is empty/undefined, the codec doesn't need init data
    // and we can configure immediately (per rawws.js line 1239-1241).
    // This applies to codecs like opus, mp3, vp8, vp9 that don't need init data.
    if (!track.init || track.init === "") {
      this.log(
        `Track ${track.idx} (${track.codec}) doesn't need init data, configuring immediately`
      );
      await this.configurePipeline(track.idx, new Uint8Array(0));
    } else {
      // For codecs that need init data (H264, HEVC, AAC), we have two paths:
      // 1. WebSocket sends INIT frame -> handleChunk triggers configurePipeline
      // 2. First frame arrives without prior INIT -> handleChunk uses track.init
      this.log(
        `Track ${track.idx} (${track.codec}) has init data (${track.init.length} bytes), waiting for first frame`
      );
    }
  }

  private async configurePipeline(idx: number, header: Uint8Array): Promise<void> {
    const pipeline = this.pipelines.get(idx);
    if (!pipeline || pipeline.configured) return;

    this.log(`Configuring decoder for track ${idx}`);

    // Copy the header to avoid transfer issues (neutered buffers)
    // The structured clone will copy this automatically
    const headerCopy = new Uint8Array(header);

    await this.sendToWorker({
      type: "configure",
      idx,
      header: headerCopy,
      uid: this.workerUidCounter++,
    });

    pipeline.configured = true;

    // Flush any queued chunks now that decoder is configured
    const queued = this.queuedChunks.get(idx);
    if (queued && queued.length > 0) {
      this.log(`Flushing ${queued.length} queued chunks for track ${idx}`);
      // Find first keyframe to start from (can't decode deltas without reference)
      let startIdx = 0;
      for (let i = 0; i < queued.length; i++) {
        if (queued[i].type === "key") {
          startIdx = i;
          break;
        }
      }
      if (startIdx > 0) {
        this.log(`Skipping ${startIdx} delta frames, starting from keyframe`);
      }
      for (let i = startIdx; i < queued.length; i++) {
        this.sendChunkToWorker(queued[i]);
      }
      this.queuedChunks.delete(idx);
    }
  }

  private async closePipeline(idx: number, waitEmpty: boolean): Promise<void> {
    const pipeline = this.pipelines.get(idx);
    if (!pipeline) return;

    this.log(`Closing pipeline ${idx}`);

    // Close worker pipeline
    await this.sendToWorker({
      type: "close",
      idx,
      waitEmpty,
      uid: this.workerUidCounter++,
    });

    // Close generator
    pipeline.generator?.close();

    // Remove from sync controller
    this.syncController?.removeTrack(idx);

    this.pipelines.delete(idx);
  }

  // ============================================================================
  // Playback Control
  // ============================================================================

  async play(): Promise<void> {
    this._isPaused = false;
    this.wsController?.play();
    this.sendToWorker({
      type: "frametiming",
      action: "setPaused",
      paused: false,
      uid: this.workerUidCounter++,
    });
    await this.videoElement?.play();
  }

  pause(): void {
    this._isPaused = true;
    this.wsController?.hold();
    this.sendToWorker({
      type: "frametiming",
      action: "setPaused",
      paused: true,
      uid: this.workerUidCounter++,
    });
    this.videoElement?.pause();
  }

  private finishStepPause(): void {
    if (!this.videoElement) {
      this._pendingStepPause = false;
      this._suppressPlayPauseSync = false;
      if (this._stepPauseTimeout) {
        clearTimeout(this._stepPauseTimeout);
        this._stepPauseTimeout = null;
      }
      return;
    }

    if (this._stepPauseTimeout) {
      clearTimeout(this._stepPauseTimeout);
      this._stepPauseTimeout = null;
    }

    this._pendingStepPause = false;
    this._suppressPlayPauseSync = false;
    try {
      this.videoElement.pause();
    } catch {}
  }

  frameStep(direction: -1 | 1, _seconds?: number): void {
    if (!this._isPaused) return;
    if (!this.videoElement) return;
    this.log(
      `Frame step requested dir=${direction} paused=${this._isPaused} videoPaused=${this.videoElement.paused}`
    );
    // Ensure worker is paused (in case pause didn't flow through)
    this.sendToWorker({
      type: "frametiming",
      action: "setPaused",
      paused: true,
      uid: this.workerUidCounter++,
    }).catch(() => {});

    // MediaStream-backed video elements don't present new frames while paused.
    // Pulse playback briefly so the stepped frame can render, then pause again.
    if (this.videoElement.paused) {
      const video = this.videoElement;
      this._suppressPlayPauseSync = true;
      this._pendingStepPause = true;
      try {
        const maybePromise = video.play();
        if (maybePromise && typeof (maybePromise as Promise<void>).catch === "function") {
          (maybePromise as Promise<void>).catch(() => {});
        }
      } catch {}

      if ("requestVideoFrameCallback" in video) {
        (video as any).requestVideoFrameCallback(() => this.finishStepPause());
      }
      // Failsafe: avoid staying in suppressed state if no frame is delivered
      this._stepPauseTimeout = setTimeout(() => this.finishStepPause(), 200);
    }
    this.sendToWorker({
      type: "framestep",
      direction,
      uid: this.workerUidCounter++,
    });
  }

  seek(time: number): void {
    if (!this.wsController || !this.syncController) return;

    const timeMs = time * 1000;
    const seekId = this.syncController.startSeek(timeMs);

    // Optimistically update current time for immediate UI feedback
    this._currentTime = time;
    this.emit("timeupdate", this._currentTime);

    // Flush worker queues
    this.sendToWorker({
      type: "seek",
      seekTime: timeMs,
      uid: this.workerUidCounter++,
    });

    // Send seek to server
    const desiredBuffer = this.syncController.getDesiredBuffer();
    this.wsController.seek(timeMs, desiredBuffer);

    // Mark seek complete after first frame (handled by worker)
    // In practice, we'd wait for first frame callback
    setTimeout(() => {
      if (this.syncController?.isSeekActive(seekId)) {
        this.syncController.completeSeek(seekId);
        this.sendToWorker({
          type: "frametiming",
          action: "reset",
          uid: this.workerUidCounter++,
        });
      }
    }, 100);
  }

  setPlaybackRate(rate: number): void {
    this.syncController?.setMainSpeed(rate);
  }

  isPaused(): boolean {
    return this._isPaused;
  }

  isLive(): boolean {
    return this.streamType === "live";
  }

  jumpToLive(): void {
    if (this.streamType === "live" && this.wsController) {
      // For WebCodecs live, request fresh data from live edge
      // Send fast_forward to request 5 seconds of new data
      // Reference: rawws.js live catchup sends fast_forward
      const desiredBuffer = this.syncController?.getDesiredBuffer() ?? 2000;
      this.wsController.send({
        type: "fast_forward",
        ff_add: 5000, // Request 5 seconds ahead
      });

      // Also request buffer from current time to rebuild
      const serverTime = this.syncController?.getEstimatedServerTime() ?? 0;
      if (serverTime > 0) {
        this.wsController.seek(serverTime * 1000, desiredBuffer);
      }

      this.log("Jump to live: requested fresh data from server");
    }
  }

  /**
   * Check if seeking is supported.
   * WebCodecs can seek via server commands when connected.
   * Reference: rawws.js line 1294-1304 implements seeking via control channel
   */
  canSeek(): boolean {
    // WebCodecs CAN seek via server commands when WebSocket is connected
    // This overrides the default MediaStream check in SeekingUtils
    return this.wsController !== null && !this.isDestroyed;
  }

  // ============================================================================
  // Media Properties (Phase 2A)
  // ============================================================================

  /**
   * Get stream duration (Infinity for live streams)
   */
  get duration(): number {
    return this._duration;
  }

  getDuration(): number {
    return this._duration;
  }

  /**
   * Get current playback time (seconds)
   * Uses requestVideoFrameCallback for accurate timing when available
   */
  get currentTime(): number {
    return this._currentTime;
  }

  getCurrentTime(): number {
    return this._currentTime;
  }

  /**
   * Get buffered time ranges
   * Returns single range from current time to current + buffer
   */
  get buffered(): TimeRanges {
    if (this._bufferMs <= 0) {
      return createTimeRanges([]);
    }
    const start = this._currentTime;
    const end = start + this._bufferMs / 1000;
    return createTimeRanges([[start, end]]);
  }

  /**
   * Get comprehensive player statistics
   */
  async getStats(): Promise<WebCodecsStats> {
    const syncState = this.syncController?.getState();
    return {
      latency: {
        buffer: syncState?.buffer.current ?? 0,
        target: syncState?.buffer.desired ?? 0,
        jitter: syncState?.jitter.weighted ?? 0,
      },
      sync: {
        avDrift: this._avDrift,
        playbackSpeed: syncState?.playbackSpeed ?? 1,
      },
      decoder: {
        videoQueueSize: 0, // Will be populated from worker stats
        audioQueueSize: 0,
        framesDropped: this._framesDropped,
        framesDecoded: this._framesDecoded,
      },
      network: {
        bytesReceived: this._bytesReceived,
        messagesReceived: this._messagesReceived,
      },
    };
  }

  // ============================================================================
  // Frame Timing (requestVideoFrameCallback)
  // ============================================================================

  /**
   * Set up requestVideoFrameCallback for accurate frame timing
   * This provides vsync-aligned frame metadata for A/V sync
   */
  private setupFrameCallback(): void {
    if (!this.videoElement) return;

    // Check if requestVideoFrameCallback is available
    if ("requestVideoFrameCallback" in HTMLVideoElement.prototype) {
      const callback = (_now: DOMHighResTimeStamp, metadata: VideoFrameCallbackMetadata) => {
        if (this.isDestroyed || !this.videoElement) return;

        this.onVideoFrame(metadata);

        // Schedule next callback
        this._frameCallbackId = (this.videoElement as any).requestVideoFrameCallback(callback);
      };

      this._frameCallbackId = (this.videoElement as any).requestVideoFrameCallback(callback);
      this.log("requestVideoFrameCallback enabled for accurate frame timing");
    } else {
      // Fallback: Use video element's currentTime directly
      this.log("requestVideoFrameCallback not available, using fallback timing");
    }
  }

  /**
   * Handle video frame presentation callback
   * Updates current time
   */
  private onVideoFrame(metadata: VideoFrameCallbackMetadata): void {
    // Update current time from actual frame presentation
    this._currentTime = metadata.mediaTime;

    // Update buffer level from sync controller
    const syncState = this.syncController?.getState();
    if (syncState) {
      this._bufferMs = syncState.buffer.current;
    }

    // Emit timeupdate event
    this.emit("timeupdate", this._currentTime);

    // Update frame stats
    this._framesDecoded = metadata.presentedFrames;
  }

  /**
   * Cancel frame callback on cleanup
   */
  private cancelFrameCallback(): void {
    if (this._frameCallbackId !== null && this.videoElement) {
      if ("cancelVideoFrameCallback" in HTMLVideoElement.prototype) {
        (this.videoElement as any).cancelVideoFrameCallback(this._frameCallbackId);
      }
      this._frameCallbackId = null;
    }
  }

  // ============================================================================
  // Logging
  // ============================================================================

  private log(message: string, level: "info" | "warn" | "error" = "info"): void {
    if (!this.debugging && level === "info") return;
    console[level](`[WebCodecs] ${message}`);
  }
}

// Export for direct use
export { WebSocketController } from "./WebSocketController";
export { SyncController } from "./SyncController";
export { JitterTracker, MultiTrackJitterTracker } from "./JitterBuffer";
export { getLatencyProfile, mergeLatencyProfile, LATENCY_PROFILES } from "./LatencyProfiles";
export { parseRawChunk, RawChunkParser } from "./RawChunkParser";
export * from "./types";

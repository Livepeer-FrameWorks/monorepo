/**
 * MEWS WebSocket Player Implementation
 *
 * Low-latency WebSocket MP4 streaming using MediaSource Extensions.
 * Protocol: Custom MEWS (MistServer Extended WebSocket)
 *
 */

import { BasePlayer } from "../../core/PlayerInterface";
import { isLiveStreamType } from "../../core/PlayerInterface";
import type {
  StreamSource,
  StreamInfo,
  PlayerOptions,
  PlayerCapability,
} from "../../core/PlayerInterface";
import { WebSocketManager } from "./WebSocketManager";
import { SourceBufferManager } from "./SourceBufferManager";
import { translateCodec } from "../../core/CodecUtils";
import { getBrowserInfo, isFileProtocol, isIPadWithBrokenHEVC } from "../../core/detector";
import { buildQualityLevelsFromStreamTracks } from "../../core/QualityLevels";
import { DeliveryPolicy } from "../../core/delivery/delivery-policy";
import { DesiredBufferModel } from "../../core/delivery/desired-buffer";
import { normalizeLiveCatchupConfig } from "../../core/delivery/live-catchup";
import { decideDeadPointRecovery } from "../../core/mist/dead-point-recovery";
import type { MistPlayRate } from "../../core/mist/protocol";
import { ServerDelayTracker } from "../../core/mist/server-delay";
import { CallbackMistTransport } from "../../core/mist/transports/callback-transport";
import { MewsBufferedProbe } from "./BufferProbe.MewsBuffered";
import type { MewsMessage, AnalyticsConfig, OnTimeMessage, MewsMessageListener } from "./types";

export class MewsWsPlayerImpl extends BasePlayer {
  readonly capability: PlayerCapability = {
    name: "MEWS WebSocket Player",
    shortname: "mews",
    priority: 2, // High priority - low latency protocol
    mimes: ["ws/video/mp4", "wss/video/mp4", "ws/video/webm", "wss/video/webm"],
  };

  private wsManager: WebSocketManager | null = null;
  private sbManager: SourceBufferManager | null = null;
  private mediaSource: MediaSource | null = null;
  private objectUrl: string | null = null;
  private container: HTMLElement | null = null;
  private isDestroyed = false;
  private debugging = false;

  // Server delay estimation
  private serverDelays = new ServerDelayTracker(5, () => Date.now());

  // Supported codecs (short names for MistServer protocol)
  private supportedCodecs: string[] = [];

  // Ready state - true after codec_data received and SourceBuffer initialized
  private isReady = false;
  private readyResolvers: Array<() => void> = [];

  // Duration tracking
  private lastDuration = Infinity;

  // Live vs VoD detection
  private streamType: "live" | "vod" | "unknown" = "unknown";

  // Current tracks for change detection
  private currentTracks: string[] = [];
  private selectedTrack: string = "auto";
  private streamInfoRef: StreamInfo | null = null;

  // Last codecs for track switch comparison
  private lastCodecs: string[] | null = null;

  // Playback rate tuning
  // "direct" prevents compounding; "multiplicative" preserves server-authored scaling.
  private rateAdjustmentMode: "direct" | "multiplicative" = "direct";
  private requestedRate = 1;
  private desiredBuffer: DesiredBufferModel | null = null;
  private requestBuffer: DesiredBufferModel | null = null;
  private bufferProbe: MewsBufferedProbe | null = null;
  private deliveryPolicy: DeliveryPolicy | null = null;

  // ABR state
  private bitCounter: number[] = [];
  private bitsSince: number[] = [];
  private currentBps: number | null = null;
  private nWaiting = 0;
  private nWaitingThreshold = 3;

  // Seeking state
  private seeking = false;

  // Autoplay option
  private autoplay = false;

  // Seekable range from on_time messages (begin/end in ms)
  private seekableBeginMs: number | null = null;
  private seekableEndMs: number | null = null;

  // Analytics
  private analyticsConfig: AnalyticsConfig = { enabled: false, endpoint: null };
  private analyticsTimer: ReturnType<typeof setInterval> | null = null;

  isMimeSupported(mimetype: string): boolean {
    return this.capability.mimes.includes(mimetype);
  }

  isBrowserSupported(
    mimetype: string,
    source: StreamSource,
    streamInfo: StreamInfo
  ): boolean | string[] {
    // Basic requirements check
    if (!("WebSocket" in window) || !("MediaSource" in window) || !("Promise" in window)) {
      return false;
    }

    // file:// protocol cannot use MSE (CORS blocks it)
    if (isFileProtocol()) {
      return false;
    }

    // Safari cannot play WebM via MSE
    const browser = getBrowserInfo();
    if (mimetype.includes("webm") && browser.isSafari) {
      return false;
    }

    // macOS MSE breaks MEWS playback
    if (navigator.platform.toUpperCase().indexOf("MAC") >= 0) {
      return false;
    }

    // Check codec compatibility against the stream's advertised tracks.
    const container = mimetype.split("/")[2] || "mp4";
    const playableTracks: Record<string, number> = {};
    let hasSubtitles = false;

    // iPad claims HEVC MSE support but fails in practice (iPadOS < 17)
    // Skip HEVC tracks so MEWS falls through to HLS which handles HEVC natively
    const skipHEVC = isIPadWithBrokenHEVC();

    // Test actual stream codecs against MediaSource
    this.supportedCodecs = [];
    for (const track of streamInfo.meta.tracks) {
      if (track.type === "meta") {
        if (track.codec === "subtitle") hasSubtitles = true;
        continue;
      }

      if (skipHEVC && (track.codec === "HEVC" || track.codec === "H265")) {
        continue;
      }

      const codecString = translateCodec(track as any);
      const testMime = `video/${container};codecs="${codecString}"`;

      if (MediaSource.isTypeSupported(testMime)) {
        this.supportedCodecs.push(track.codec);
        playableTracks[track.type] = 1;
      }
    }

    // Check for subtitle source
    if (hasSubtitles) {
      const hasVttSource = streamInfo.source?.some((s) => s.type === "html5/text/vtt");
      if (hasVttSource) {
        playableTracks["subtitle"] = 1;
      }
    }

    if (Object.keys(playableTracks).length === 0) return false;
    return Object.keys(playableTracks);
  }

  async initialize(
    container: HTMLElement,
    source: StreamSource,
    options: PlayerOptions,
    streamInfo?: StreamInfo
  ): Promise<HTMLVideoElement> {
    this.container = container;
    this.streamInfoRef = streamInfo ?? null;
    container.classList.add("fw-player-container");

    const video = document.createElement("video");
    video.classList.add("fw-player-video");
    video.setAttribute("playsinline", "");
    video.setAttribute("crossorigin", "anonymous");

    // Apply options
    this.autoplay = !!options.autoplay;
    if (options.autoplay) video.autoplay = true;
    if (options.muted) video.muted = true;
    video.controls = options.controls === true;
    if (options.loop) video.loop = true;
    if (options.poster) video.poster = options.poster;

    // Live streams don't loop
    if (this.streamType === "live") {
      video.loop = false;
    }

    this.videoElement = video;
    container.appendChild(video);
    this.setupVideoEventListeners(video, options);
    this.setupDeliveryPolicy(video, options);

    // Analytics configuration
    const anyOpts = options as any;
    this.analyticsConfig = {
      enabled: !!anyOpts.analytics?.enabled,
      endpoint: anyOpts.analytics?.endpoint || null,
    };

    // Get stream type from streamInfo if available
    // Note: source.type is a MIME string (e.g., 'ws/video/mp4'), not 'live'/'vod'
    if (isLiveStreamType(streamInfo?.type)) {
      this.streamType = "live";
    } else if (streamInfo?.type === "vod") {
      this.streamType = "vod";
    }
    // Fallback: will be determined by server on_time messages (end === 0 means live)

    try {
      // Initialize MediaSource
      this.mediaSource = new MediaSource();

      // Set up MediaSource event handlers
      this.mediaSource.addEventListener("sourceopen", () => this.handleSourceOpen(source));
      this.mediaSource.addEventListener("sourceclose", () => this.handleSourceClose());
      this.mediaSource.addEventListener("sourceended", () => this.handleSourceEnded());

      this.objectUrl = URL.createObjectURL(this.mediaSource);
      video.src = this.objectUrl;
      this.isDestroyed = false;
      this.startTelemetry();
      return video;
    } catch (error: any) {
      this.emit("error", error.message || String(error));
      throw error;
    }
  }

  /**
   * Handle MediaSource sourceopen event.
   */
  private handleSourceOpen(source: StreamSource): void {
    if (!this.mediaSource || !this.videoElement) return;

    // Parse container from source MIME (e.g. "ws/video/mp4" → "mp4", "ws/video/webm" → "webm")
    const container = (source.type?.split("/")[2] === "webm" ? "webm" : "mp4") as "mp4" | "webm";

    // Create SourceBufferManager
    this.sbManager = new SourceBufferManager({
      mediaSource: this.mediaSource,
      videoElement: this.videoElement,
      container,
      onError: (msg) => this.emit("error", msg),
    });

    // Install browser event handlers
    this.installWaitingHandler();
    this.installSeekingHandler();
    this.installPauseHandler();
    this.installLoopHandler();

    // Create WebSocketManager with listener support
    this.wsManager = new WebSocketManager({
      url: source.url,
      maxReconnectAttempts: 5,
      onMessage: (data) => this.handleMessage(data),
      onOpen: () => this.handleWsOpen(),
      onClose: () => this.handleWsClose(),
      onError: (msg) => this.emit("error", msg),
      shouldReconnect: () => !this.sbManager?.paused && !this.videoElement?.error,
    });
    this.wsManager.addSendDecorator((cmd) => {
      if ((cmd.type === "play" || cmd.type === "seek") && cmd.ff_add === undefined) {
        return { ...cmd, ff_add: this.getForwardBufferMs() };
      }
      return cmd;
    });

    this.wsManager.connect();
  }

  private setupDeliveryPolicy(video: HTMLVideoElement, options: PlayerOptions): void {
    this.bufferProbe = new MewsBufferedProbe(video);
    this.desiredBuffer = new DesiredBufferModel({
      baseMs: () => (this.streamType === "live" ? 600 : 3000),
    });
    this.desiredBuffer.setFactor("serverDelay", () => this.getServerDelay());
    this.desiredBuffer.setFactor("liveJitter", () =>
      this.streamType === "live" ? (this.bufferProbe?.sample().jitterMs ?? 0) : 0
    );
    this.requestBuffer = new DesiredBufferModel({
      baseMs: () => (this.streamType === "live" ? 600 : 1000),
    });
    this.requestBuffer.setFactor("serverDelay", () => this.getServerDelay());

    this.deliveryPolicy = new DeliveryPolicy({
      transport: this.createDeliveryTransport(),
      probe: this.bufferProbe,
      desired: this.desiredBuffer,
      liveCatchup: normalizeLiveCatchupConfig(options.liveCatchup, {
        undefinedMeans: "off",
      }),
      isLive: () => this.streamType === "live",
      speedDownThreshold: 0.6,
      speedUpThreshold: 2,
      maxSpeedUp: 1.08,
      minSpeedDown: 0.98,
      serverRateMode: "vod-only",
      localRateMode: "always",
      liveSetSpeedToggle: true,
      bucketHysteresis: true,
      pendingFastForward: true,
      applyLocalRate: (rate) => {
        if (this.streamType === "live") {
          this.applyLocalPlaybackRate(rate);
        }
      },
      tickSource: "on_time",
      now: () => performance.now(),
    });

    this.deliveryPolicy.on("bufferlow", (data) => this.emit("bufferlow", data));
    this.deliveryPolicy.on("serverratesuggest", ({ rate }) => {
      if (this.streamType === "live") return;
      this.requestedRate = rate === "auto" ? 1 : rate;
      this.send({ type: "set_speed", play_rate: rate });
    });
  }

  private createDeliveryTransport(): CallbackMistTransport {
    return new CallbackMistTransport((cmd) => {
      this.send(cmd);
      return true;
    });
  }

  /**
   * Handle MediaSource sourceclose event.
   */
  private handleSourceClose(): void {
    if (this.debugging) console.log("MEWS: MediaSource closed");
    this.send({ type: "stop" });
  }

  /**
   * Handle MediaSource sourceended event.
   */
  private handleSourceEnded(): void {
    if (this.debugging) console.log("MEWS: MediaSource ended");
    this.send({ type: "stop" });
  }

  /**
   * Handle WebSocket open event.
   */
  private handleWsOpen(): void {
    // Request codec data
    const listener: MewsMessageListener = (msg) => {
      // Got codec data, set up source buffer
      if (this.mediaSource?.readyState === "open") {
        const codecs = msg.data?.codecs || [];
        const initialized = this.sbManager?.initWithCodecs(codecs);

        if (initialized && !this.isReady) {
          this.isReady = true;
          // Resolve any waiting play() calls
          for (const resolve of this.readyResolvers) {
            resolve();
          }
          this.readyResolvers = [];

          // checkReady: auto-play once WS + MS + SB are all ready
          if (this.autoplay) {
            this.play().catch(() => {});
          }
        }
      }
      this.wsManager?.removeListener("codec_data", listener);
    };

    this.wsManager?.addListener("codec_data", listener);
    this.logDelay("codec_data");

    // Send request with SHORT codec names
    // CRITICAL: MistServer expects short names like "H264", not browser codec strings
    this.send({ type: "request_codec_data", supported_codecs: this.supportedCodecs });
  }

  /**
   * Handle WebSocket close event with reconnection logic.
   */
  private handleWsClose(): void {
    if (this.debugging) console.log("MEWS: WebSocket closed");
    // Reconnection is handled by WebSocketManager
  }

  /**
   * Handle incoming WebSocket message.
   * Routes to binary append or JSON control message handler.
   */
  private handleMessage(data: ArrayBuffer | string): void {
    if (typeof data === "string") {
      try {
        const msg = JSON.parse(data) as MewsMessage;
        this.handleControlMessage(msg);
        // Notify listeners
        this.wsManager?.notifyListeners(msg);
      } catch (e) {
        if (this.debugging) console.error("MEWS: Failed to parse message", e);
      }
      return;
    }

    // Binary data - MP4 segment
    const bytes = new Uint8Array(data);
    this.sbManager?.append(bytes);
    this.trackBits(data);
  }

  /**
   * Handle JSON control messages.
   */
  private handleControlMessage(msg: MewsMessage): void {
    if (this.debugging && msg.type !== "on_time") {
      console.log("MEWS: message", msg);
    }

    switch (msg.type) {
      case "on_stop":
        this.handleOnStop();
        break;

      case "on_time":
        this.handleOnTime(msg as OnTimeMessage);
        break;

      case "tracks":
        this.handleTracks(msg);
        break;

      case "pause":
        this.handlePause(msg);
        break;

      case "codec_data":
        this.resolveDelay("codec_data");
        break;

      case "seek":
        this.resolveDelay("seek");
        break;

      case "set_speed":
        this.resolveDelay("set_speed");
        this.handleSetSpeedAck(msg);
        break;
    }
  }

  /**
   * Handle on_stop message - stream ended (VoD).
   */
  private handleOnStop(): void {
    // Mark as VoD (stream ended)
    this.streamType = "vod";

    // Prevent reconnection after server closes the WS
    this.wsManager?.disableReconnection();

    // Wait for buffer to finish playing
    const onWaiting = () => {
      if (this.sbManager) {
        this.sbManager.paused = true;
      }
      this.emit("ended", undefined);
      this.videoElement?.removeEventListener("waiting", onWaiting);
    };
    this.videoElement?.addEventListener("waiting", onWaiting);
  }

  /**
   * Handle on_time message - playback time sync.
   */
  private handleOnTime(msg: OnTimeMessage): void {
    const data = msg.data;
    if (!data || !this.videoElement) return;

    const currentMs = data.current;
    const endMs = data.end;
    const jitter = data.jitter || 0;

    // Store seekable range for controller (begin/end in ms)
    if (data.begin !== undefined) this.seekableBeginMs = data.begin;
    if (endMs !== undefined) this.seekableEndMs = endMs;

    // Buffer calculation
    const buffer = currentMs - this.videoElement.currentTime * 1000;
    this.bufferProbe?.updateServerState({
      currentMs,
      endMs,
      jitterMs: jitter,
      playRateCurr: data.play_rate_curr,
    });
    const desiredBuffer = this.getForwardBufferMs();

    if (this.debugging) {
      console.log(
        "MEWS: on_time",
        "current:",
        currentMs / 1000,
        "video:",
        this.videoElement.currentTime,
        "rate:",
        this.requestedRate + "x",
        "buffer:",
        Math.round(buffer),
        "/",
        Math.round(desiredBuffer),
        this.streamType === "live"
          ? "latency:" + Math.round((endMs || 0) - this.videoElement.currentTime * 1000) + "ms"
          : ""
      );
    }

    if (!this.sbManager) {
      if (this.debugging) console.log("MEWS: on_time but no sourceBuffer");
      return;
    }

    // Update duration
    if (endMs !== undefined && this.lastDuration !== endMs / 1000) {
      this.lastDuration = endMs / 1000;
      // Duration is updated via native video element durationchange event
    }

    // Mark source buffer as not paused
    this.sbManager.paused = false;

    this.deliveryPolicy?.ingestOnTime({
      current: currentMs,
      end: endMs ?? currentMs,
      begin: data.begin ?? 0,
      jitter,
      play_rate_curr: data.play_rate_curr,
    });

    // Track change detection
    if (data.tracks && this.currentTracks.join(",") !== data.tracks.join(",")) {
      if (this.debugging) {
        for (const trackId of data.tracks) {
          if (!this.currentTracks.includes(trackId)) {
            console.log("MEWS: track changed", trackId);
          }
        }
      }
      this.currentTracks = data.tracks;
    }
  }

  private getForwardBufferMs(): number {
    return Math.max(
      0,
      this.requestBuffer?.getDesiredMs() ?? this.desiredBuffer?.getDesiredMs() ?? 0
    );
  }

  private applyLocalPlaybackRate(rate: number): void {
    if (!this.videoElement) return;
    if (this.rateAdjustmentMode === "multiplicative") {
      this.videoElement.playbackRate *= rate / this.requestedRate;
    } else {
      this.videoElement.playbackRate = rate;
    }
    this.requestedRate = rate;
  }

  private handleSetSpeedAck(msg: MewsMessage): void {
    const data = msg.data || {};
    const playRateCurr = data.play_rate_curr as "auto" | "fast-forward" | number | undefined;
    const playRatePrev = data.play_rate_prev as "auto" | "fast-forward" | number | undefined;

    this.deliveryPolicy?.ingestSetSpeedAck({
      type: "set_speed",
      play_rate_curr: playRateCurr,
      play_rate_prev: playRatePrev,
    });

    if (typeof playRateCurr === "number" && this.streamType !== "live") {
      this.requestedRate = playRateCurr;
    }
  }

  /**
   * Handle tracks message - codec switch.
   */
  private handleTracks(msg: MewsMessage): void {
    const codecs: string[] = msg.data?.codecs || [];
    const switchPointMs = msg.data?.current;

    if (!codecs.length) {
      this.emit("error", "Track switch contains no codecs");
      return;
    }

    // Check if codecs are same as before
    const prevCodecs = this.lastCodecs || this.sbManager?.getCodecs() || [];
    if (this.codecsEqual(prevCodecs, codecs)) {
      if (this.debugging) console.log("MEWS: keeping buffer, codecs same");
      // If at position 0 and switch point is not 0, seek to switch point
      if (this.videoElement?.currentTime === 0 && switchPointMs && switchPointMs !== 0) {
        this.setSeekingPosition(switchPointMs / 1000);
      }
      return;
    }

    // Different codecs, save for next comparison
    this.lastCodecs = codecs;

    // Change codecs (will handle msgqueue internally)
    this.sbManager?.changeCodecs(codecs, switchPointMs);
  }

  /**
   * Handle pause message.
   */
  private handlePause(msg: MewsMessage): void {
    const data = msg.data || {};
    const recovery = decideDeadPointRecovery(data, this.requestedRate);
    if (recovery.kind === "seek_recover") {
      if (recovery.resetSpeedToAuto) {
        this.requestedRate = 1;
        if (this.videoElement) this.videoElement.playbackRate = 1;
        this.send({ type: "set_speed", play_rate: "auto" });
      }
      this.send({ type: "seek", seek_time: recovery.seekToMs });
      return;
    }
    if (this.sbManager) {
      this.sbManager.paused = true;
    }
  }

  /**
   * Set video currentTime with retry logic.
   */
  private setSeekingPosition(tSec: number, retries = 10): void {
    if (!this.videoElement || !this.sbManager || retries <= 0) return;

    const currPos = this.videoElement.currentTime;
    if (currPos > tSec) {
      // Don't seek backwards
      tSec = currPos;
    }

    const buffered = this.videoElement.buffered;
    if (!buffered.length || buffered.end(buffered.length - 1) < tSec) {
      // Desired position not in buffer yet, wait for more data
      this.sbManager.scheduleAfterUpdate(() => this.setSeekingPosition(tSec, retries - 1));
      return;
    }

    this.videoElement.currentTime = tSec;

    if (this.videoElement.currentTime < tSec - 0.001) {
      // Didn't reach target, retry
      this.sbManager.scheduleAfterUpdate(() => this.setSeekingPosition(tSec, retries - 1));
    }
  }

  /**
   * Check if two codec arrays are equivalent (order-independent)
   */
  private codecsEqual(arr1: string[], arr2: string[]): boolean {
    if (arr1.length !== arr2.length) return false;
    for (const codec of arr1) {
      if (!arr2.includes(codec)) return false;
    }
    return true;
  }

  // ========== PUBLIC API ==========

  /**
   * Play with optional skip to live edge.
   */
  async play(): Promise<void> {
    const v = this.videoElement;
    if (!v) return;

    // If already playing, nothing to do
    if (!v.paused) return;

    // Wait for ready state (codec_data received) with timeout
    if (!this.isReady) {
      await new Promise<void>((resolve, reject) => {
        const timeout = setTimeout(() => {
          reject(new Error("MEWS: Timeout waiting for codec data"));
        }, 5000);
        this.readyResolvers.push(() => {
          clearTimeout(timeout);
          resolve();
        });
      });
    }

    // Use listener to wait for on_time before playing
    return new Promise((resolve, reject) => {
      // Flag to prevent race condition where multiple on_time messages
      // could trigger seek before the first completes
      let handled = false;

      const onTime: MewsMessageListener = (msg) => {
        // Remove listener immediately to prevent race condition (single-use pattern)
        if (handled) return;
        handled = true;
        this.wsManager?.removeListener("on_time", onTime);

        if (!this.sbManager) {
          if (this.debugging) console.log("MEWS: play waiting for sourceBuffer");
          handled = false; // Allow retry
          this.wsManager?.addListener("on_time", onTime);
          return;
        }

        const data = (msg as OnTimeMessage).data;

        if (this.streamType === "live") {
          // Live stream - wait for buffer then seek to live edge
          const waitForBuffer = () => {
            if (!v.buffered.length) return;

            const bufferIdx = this.sbManager?.findBufferIndex(data.current / 1000);
            if (typeof bufferIdx === "number") {
              // Check if current position is in buffer
              if (
                v.buffered.start(bufferIdx) > v.currentTime ||
                v.buffered.end(bufferIdx) < v.currentTime
              ) {
                v.currentTime = data.current / 1000;
                if (this.debugging) console.log("MEWS: seeking to live position", v.currentTime);
              }

              v.play()
                .then(resolve)
                .catch((err) => {
                  this.pause();
                  reject(err);
                });

              this.sbManager!.paused = false;
            }
          };

          // Wait for buffer via updateend
          this.sbManager?.scheduleAfterUpdate(waitForBuffer);
        } else {
          // VoD - just play when we have data
          this.sbManager!.paused = false;
          if (v.buffered.length && v.buffered.start(0) > v.currentTime) {
            v.currentTime = v.buffered.start(0);
          }
          v.play().then(resolve).catch(reject);
        }
      };

      this.wsManager?.addListener("on_time", onTime);

      // Send play command
      const skipToLive = this.streamType === "live" && v.currentTime === 0;
      if (skipToLive) {
        this.send({ type: "play", seek_time: "live" });
      } else {
        this.send({ type: "play" });
      }
    });
  }

  /**
   * Pause playback and server delivery.
   */
  pause(): void {
    this.videoElement?.pause();
    this.send({ type: "hold" });
    if (this.sbManager) {
      this.sbManager.paused = true;
    }
  }

  /**
   * Seek to position with server sync.
   */
  seek(timeMs: number): void {
    if (!this.videoElement || isNaN(timeMs) || timeMs < 0) return;

    // Calculate seek time with server delay compensation
    const seekMs = Math.round(Math.max(0, timeMs - (250 + this.getServerDelay())));

    this.logDelay("seek");
    this.send({ type: "seek", seek_time: seekMs });

    // Wait for seek acknowledgment then on_time
    const onSeek: MewsMessageListener = () => {
      this.wsManager?.removeListener("seek", onSeek);

      const onTime: MewsMessageListener = (msg) => {
        this.wsManager?.removeListener("on_time", onTime);

        // Use server's actual position — server sends ms
        const actualTimeSec = (msg as OnTimeMessage).data.current / 1000;
        this.trySetCurrentTime(actualTimeSec);
      };

      this.wsManager?.addListener("on_time", onTime);
    };

    this.wsManager?.addListener("seek", onSeek);

    // Also set directly as fallback (convert ms → seconds for video element)
    this.videoElement.currentTime = timeMs / 1000;
    if (this.debugging) console.log("MEWS: seeking to", timeMs, "ms");
  }

  /**
   * Try to set currentTime with retry logic.
   */
  private trySetCurrentTime(tSec: number, retries = 10): void {
    const v = this.videoElement;
    if (!v) return;

    v.currentTime = tSec;

    if (v.currentTime < tSec - 0.001 && retries > 0) {
      // Failed to seek, retry
      this.sbManager?.scheduleAfterUpdate(() => this.trySetCurrentTime(tSec, retries - 1));
    }
  }

  getCurrentTime(): number {
    return (this.videoElement?.currentTime ?? 0) * 1000;
  }

  getDuration(): number {
    const sec = isFinite(this.lastDuration)
      ? this.lastDuration
      : (this.videoElement?.duration ?? 0);
    if (!Number.isFinite(sec)) return sec; // preserve Infinity
    return sec * 1000;
  }

  getSeekableRange(): { start: number; end: number } | null {
    if (this.seekableBeginMs !== null && this.seekableEndMs !== null) {
      return { start: this.seekableBeginMs, end: this.seekableEndMs };
    }
    return null;
  }

  /**
   * Set playback rate.
   */
  setPlaybackRate(rate: number): void {
    super.setPlaybackRate(rate);
    const playRate = rate === 1 ? "auto" : rate;
    this.logDelay("set_speed");
    this.send({ type: "set_speed", play_rate: playRate });
  }

  getQualities(): Array<{ id: string; label: string; isAuto?: boolean; active?: boolean }> {
    const qualities: Array<{ id: string; label: string; isAuto?: boolean; active?: boolean }> = [
      { id: "auto", label: "Auto", isAuto: true, active: this.selectedTrack === "auto" },
    ];
    for (const level of buildQualityLevelsFromStreamTracks(this.streamInfoRef?.meta?.tracks)) {
      qualities.push({
        ...level,
        isAuto: false,
        active: this.selectedTrack === level.id,
      });
    }
    return qualities;
  }

  selectQuality(id: string): void {
    if (id === "auto") {
      // Reset to automatic track selection (not set_speed which controls delivery rate)
      this.send({ type: "tracks" });
      this.selectedTrack = "auto";
    } else {
      this.send({ type: "tracks", video: id });
      this.selectedTrack = id;
    }
  }

  /**
   * Set tracks for ABR or quality selection.
   */
  setTracks(obj: { video?: string; audio?: string; subtitle?: string }): void {
    if (!Object.keys(obj).length) return;
    this.send({ type: "tracks", ...obj });
  }

  /**
   * Select a subtitle track.
   */
  selectTextTrack(id: string | null): void {
    if (id === null) {
      this.send({ type: "tracks", subtitle: "none" });
    } else {
      this.send({ type: "tracks", subtitle: id });
    }
  }

  isLive(): boolean {
    return this.streamType === "live";
  }

  /**
   * Jump to live edge.
   */
  jumpToLive(): void {
    if (this.streamType !== "live" || !this.wsManager) return;
    this.send({ type: "play", seek_time: "live" });
    this.videoElement?.play().catch(() => {});
  }

  async getStats(): Promise<any> {
    return {
      currentBps: this.currentBps,
      waitingEvents: this.nWaiting,
      isLive: this.streamType === "live",
      serverDelay: this.getServerDelay(),
    };
  }

  // ========== EVENT HANDLERS ==========

  /**
   * Install waiting event handler.
   * Handles buffer gaps and ABR.
   */
  private installWaitingHandler(): void {
    if (!this.videoElement) return;

    this.videoElement.addEventListener("waiting", () => {
      if (this.seeking) return;

      const v = this.videoElement!;
      if (!v.buffered || !v.buffered.length) return;

      // Check for buffer gap and jump it
      const bufferIdx = this.sbManager?.findBufferIndex(v.currentTime);
      if (bufferIdx !== false && typeof bufferIdx === "number") {
        // currentTime is in a range — check for gap to next range
        if (bufferIdx + 1 < v.buffered.length) {
          const nextStart = v.buffered.start(bufferIdx + 1);
          if (nextStart - v.currentTime < 10) {
            if (this.debugging) console.log("MEWS: skipping buffer gap to", nextStart);
            v.currentTime = nextStart;
          }
        }
      } else {
        // currentTime is in a gap between ranges — find the next range and jump
        for (let i = 0; i < v.buffered.length; i++) {
          if (v.buffered.start(i) > v.currentTime) {
            if (v.buffered.start(i) - v.currentTime < 10) {
              if (this.debugging) console.log("MEWS: jumping out of gap to", v.buffered.start(i));
              v.currentTime = v.buffered.start(i);
            }
            break;
          }
        }
      }

      // ABR trigger
      this.nWaiting++;
      if (this.nWaiting >= this.nWaitingThreshold && this.currentBps) {
        this.nWaiting = 0;
        if (this.debugging) console.log("MEWS: ABR triggered, requesting lower bitrate");
        this.setTracks({ video: `<${Math.round(this.currentBps)}bps,minbps` });
      }
    });
  }

  /**
   * Install seeking event handlers.
   */
  private installSeekingHandler(): void {
    if (!this.videoElement) return;

    this.videoElement.addEventListener("seeking", () => {
      this.seeking = true;
    });

    this.videoElement.addEventListener("seeked", () => {
      this.seeking = false;
    });
  }

  /**
   * Install pause event handler for browser pause detection.
   */
  private installPauseHandler(): void {
    if (!this.videoElement) return;

    this.videoElement.addEventListener("pause", () => {
      if (this.sbManager && !this.sbManager.paused) {
        // Browser paused (probably tab hidden) - pause download
        if (this.debugging) console.log("MEWS: browser paused, pausing download");
        this.send({ type: "hold" });
        this.sbManager.paused = true;

        // Resume on play
        const onPlay = () => {
          if (this.sbManager?.paused) {
            this.send({ type: "play" });
          }
          this.videoElement?.removeEventListener("play", onPlay);
        };
        this.videoElement?.addEventListener("play", onPlay);
      }
    });
  }

  /**
   * Install loop handler for VoD content.
   */
  private installLoopHandler(): void {
    if (!this.videoElement) return;

    this.videoElement.addEventListener("ended", () => {
      const v = this.videoElement;
      if (!v) return;

      if (v.loop && this.streamType !== "live") {
        // Loop VoD content
        this.seek(0);
        this.sbManager?.clearBuffer();
      }
    });
  }

  // ========== UTILITIES ==========

  /**
   * Send command to server with retry.
   */
  private send(cmd: object): void {
    if (this.wsManager) {
      this.wsManager.send(cmd);
    }
  }

  /**
   * Log delay for server RTT estimation.
   */
  private logDelay(type: string): void {
    this.serverDelays.beginRequest(type);
  }

  /**
   * Resolve delay measurement.
   */
  private resolveDelay(type: string): void {
    this.serverDelays.resolveRequest(type);
  }

  /**
   * Get average server delay.
   */
  private getServerDelay(): number {
    return this.serverDelays.getAverageDelay(500);
  }

  /**
   * Track bandwidth for ABR.
   */
  private trackBits(buf: ArrayBuffer): void {
    this.bitCounter.push(buf.byteLength * 8);
    this.bitsSince.push(Date.now());

    // Keep window size of 5 samples
    if (this.bitCounter.length > 5) {
      this.bitCounter.shift();
      this.bitsSince.shift();
    }

    // Calculate current bitrate (sum all samples over the window)
    if (this.bitCounter.length >= 2) {
      const totalBits = this.bitCounter.reduce((sum, b) => sum + b, 0);
      const dt = (this.bitsSince[this.bitsSince.length - 1] - this.bitsSince[0]) / 1000;
      if (dt > 0) {
        this.currentBps = Math.round(totalBits / dt);
      }
    }
  }

  private startTelemetry(): void {
    if (!this.analyticsConfig.enabled || !this.analyticsConfig.endpoint) return;

    const endpoint = this.analyticsConfig.endpoint;

    this.analyticsTimer = setInterval(async () => {
      if (!this.videoElement) return;

      const stats = await this.getStats();
      const payload = {
        t: Date.now(),
        bps: stats.currentBps || 0,
        waiting: stats.waitingEvents || 0,
      };

      try {
        await fetch(endpoint, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(payload),
        });
      } catch {}
    }, 5000);
  }

  async destroy(): Promise<void> {
    console.debug("[MEWS] destroy() called");
    this.isDestroyed = true;
    this.isReady = false;
    this.readyResolvers = [];
    this.seekableBeginMs = null;
    this.seekableEndMs = null;

    if (this.analyticsTimer) {
      clearInterval(this.analyticsTimer);
      this.analyticsTimer = null;
    }
    this.deliveryPolicy?.destroy();
    this.deliveryPolicy = null;
    this.bufferProbe = null;
    this.desiredBuffer = null;

    // Tell server to stop encoding before closing the WS.
    // Use sendDirect to avoid retry logic — fire-and-forget.
    this.wsManager?.sendDirect({ type: "stop" });
    this.wsManager?.destroy();
    this.wsManager = null;

    this.sbManager?.destroy();
    this.sbManager = null;

    if (this.mediaSource?.readyState === "open") {
      try {
        this.mediaSource.endOfStream();
      } catch {}
    }

    if (this.objectUrl) {
      URL.revokeObjectURL(this.objectUrl);
      this.objectUrl = null;
    }

    if (this.videoElement && this.container) {
      try {
        this.container.removeChild(this.videoElement);
      } catch {}
    }

    this.videoElement = null;
    this.container = null;
    this.mediaSource = null;
    this.listeners.clear();
    console.debug("[MEWS] destroy() completed");
  }
}

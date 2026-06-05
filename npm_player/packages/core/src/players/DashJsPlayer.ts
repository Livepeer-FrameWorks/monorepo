import { BasePlayer } from "../core/PlayerInterface";
import { checkProtocolMismatch, getBrowserInfo, isFileProtocol } from "../core/detector";
import { translateCodec } from "../core/CodecUtils";
import { formatQualityLabel } from "../core/TimeFormat";
import type {
  StreamSource,
  StreamInfo,
  PlayerOptions,
  PlayerCapability,
} from "../core/PlayerInterface";
import { isLiveStreamType } from "../core/PlayerInterface";

// Player implementation class
export class DashJsPlayerImpl extends BasePlayer {
  readonly capability: PlayerCapability = {
    name: "Dash.js Player",
    shortname: "dashjs",
    priority: 100, // Below legacy (99) - DASH support is experimental
    mimes: ["dash/video/mp4"],
  };

  private dashPlayer: any = null;
  private container: HTMLElement | null = null;
  private destroyed = false;
  private debugging = false;

  // Live duration proxy state (ported from reference dashjs.js:81-122)
  private lastProgress = Date.now();
  private videoProxy: HTMLVideoElement | null = null;
  private streamType: "live" | "vod" | "unknown" = "unknown";

  // Subtitle deferred loading (ported from reference dashjs.js:173-197)
  private subsLoaded = false;
  private pendingSubtitleId: string | null = null;

  // Catch unhandled promise rejections from dash.js internals
  private _rejectionHandler: ((e: PromiseRejectionEvent) => void) | null = null;

  isMimeSupported(mimetype: string): boolean {
    return this.capability.mimes.includes(mimetype);
  }

  isBrowserSupported(
    mimetype: string,
    source: StreamSource,
    streamInfo: StreamInfo
  ): boolean | string[] {
    // Check protocol mismatch
    if (checkProtocolMismatch(source.url)) {
      return false;
    }

    // Don't use DASH.js if loaded via file://
    if (isFileProtocol()) {
      return false;
    }

    const browser = getBrowserInfo();

    // Check MediaSource support (required for DASH.js)
    if (!browser.supportsMediaSource) {
      return false;
    }

    // Check codec compatibility
    const playableTracks: string[] = [];
    const tracksByType: Record<string, typeof streamInfo.meta.tracks> = {};

    // Group tracks by type
    for (const track of streamInfo.meta.tracks) {
      if (track.type === "meta") {
        if (track.codec === "subtitle") {
          // Check for WebVTT subtitle support
          for (const src of streamInfo.source) {
            if (src.type === "html5/text/vtt") {
              playableTracks.push("subtitle");
              break;
            }
          }
        }
        continue;
      }

      if (!tracksByType[track.type]) {
        tracksByType[track.type] = [];
      }
      tracksByType[track.type].push(track);
    }

    // DASH-incompatible audio codecs for fMP4 segments (even if browser MSE supports them)
    // Standard DASH audio: AAC, MP3, AC-3/E-AC-3. OPUS only works in WebM DASH (not fMP4)
    const DASH_INCOMPATIBLE_AUDIO = ["OPUS", "Opus", "opus", "VORBIS", "Vorbis"];

    // Test codec support for video/audio tracks
    for (const [trackType, tracks] of Object.entries(tracksByType)) {
      let hasPlayableTrack = false;

      for (const track of tracks) {
        // Explicit DASH codec filtering - OPUS in fMP4 DASH doesn't work reliably
        if (trackType === "audio" && DASH_INCOMPATIBLE_AUDIO.includes(track.codec)) {
          if (this.debugging) {
            console.debug(`[DashJS] Codec incompatible with DASH fMP4: ${track.codec}`);
          }
          continue;
        }

        const codecString = translateCodec(track);
        // Use correct container type for audio vs video tracks
        const container = trackType === "audio" ? "audio/mp4" : "video/mp4";
        const mimeType = `${container};codecs="${codecString}"`;

        if (MediaSource.isTypeSupported && MediaSource.isTypeSupported(mimeType)) {
          hasPlayableTrack = true;
          break;
        } else if (this.debugging) {
          console.debug(`[DashJS] Codec not supported: ${mimeType}`);
        }
      }

      if (hasPlayableTrack) {
        playableTracks.push(trackType);
      }
    }

    return playableTracks.length > 0 ? playableTracks : false;
  }

  /**
   * Check if current stream is live.
   * Ported from reference dashjs.js live detection.
   */
  private isLiveStream(): boolean {
    if (this.streamType === "live") return true;
    if (this.streamType === "vod") return false;
    // Fallback: check video duration
    const v = this.videoElement;
    if (v && (v.duration === Infinity || !isFinite(v.duration))) {
      return true;
    }
    return false;
  }

  /**
   * Create a Proxy wrapper for the video element that intercepts duration for live streams.
   * Ported from reference dashjs.js:81-122.
   *
   * For live streams, returns synthetic duration = buffer_end + time_since_last_progress
   * This makes the seek bar usable for live content.
   */
  private createVideoProxy(video: HTMLVideoElement): HTMLVideoElement {
    if (!("Proxy" in window)) {
      // Fallback for older browsers
      return video;
    }

    // Track buffer progress for duration extrapolation
    video.addEventListener("progress", () => {
      this.lastProgress = Date.now();
    });

    const self = this;
    return new Proxy(video, {
      get(target, key) {
        // Override duration for live streams (reference dashjs.js:108-116)
        if (key === "duration" && self.isLiveStream()) {
          const buffered = target.buffered;
          if (buffered.length > 0) {
            const bufferEnd = buffered.end(buffered.length - 1);
            const timeSinceBuffer = (Date.now() - self.lastProgress) / 1000;
            return bufferEnd + timeSinceBuffer;
          }
        }
        const value = Reflect.get(target, key, target);
        // Bind functions to the original target
        if (typeof value === "function") {
          return value.bind(target);
        }
        return value;
      },
      set(target, key, value) {
        return Reflect.set(target, key, value);
      },
    }) as HTMLVideoElement;
  }

  /**
   * Set up comprehensive event logging.
   * Ported from reference dashjs.js:152-160.
   */
  private setupEventLogging(dashjs: any): void {
    const skipEvents = [
      "METRIC_ADDED",
      "METRIC_UPDATED",
      "METRIC_CHANGED",
      "METRICS_CHANGED",
      "FRAGMENT_LOADING_STARTED",
      "FRAGMENT_LOADING_COMPLETED",
      "LOG",
      "PLAYBACK_TIME_UPDATED",
      "PLAYBACK_PROGRESS",
    ];

    const events = dashjs.MediaPlayer?.events || {};
    for (const eventKey of Object.keys(events)) {
      if (!skipEvents.includes(eventKey)) {
        this.dashPlayer.on(events[eventKey], (e: any) => {
          if (this.destroyed) return;
          this.debugLog("DASH event:", e.type);
        });
      }
    }
  }

  /**
   * Set up subtitle deferred loading.
   * Ported from reference dashjs.js:173-197.
   */
  private setupSubtitleHandling(): void {
    this.dashPlayer.on("allTextTracksAdded", () => {
      if (this.destroyed) return;
      this.subsLoaded = true;
      if (this.pendingSubtitleId !== null) {
        this.selectTextTrack(this.pendingSubtitleId);
        this.pendingSubtitleId = null;
      }
    });
  }

  /**
   * Set up stalled indicator handling.
   * Ported from reference dashjs.js:207-211.
   */
  private setupStalledHandling(): void {
    this.videoElement?.addEventListener("progress", () => {
      // Clear any stalled state when buffer advances
      // This integrates with the loading indicator system
    });
  }

  private reportDashFailure(error: string | Error, options?: PlayerOptions): void {
    const message = typeof error === "string" ? error : error.message || String(error);
    options?.onError?.(message);
    this.emit("error", message);
  }

  private waitForDashStartup(): Promise<void> {
    return new Promise((resolve, reject) => {
      if (!this.dashPlayer) {
        resolve();
        return;
      }

      let settled = false;
      let timeoutId: ReturnType<typeof setTimeout> | null = null;

      const cleanup = () => {
        if (timeoutId !== null) clearTimeout(timeoutId);
        this.dashPlayer?.off?.("streamInitialized", succeed);
        this.dashPlayer?.off?.("canPlay", succeed);
        this.dashPlayer?.off?.("error", fail);
      };

      const succeed = () => {
        if (settled) return;
        settled = true;
        cleanup();
        resolve();
      };

      const fail = (event?: any) => {
        if (settled) return;
        settled = true;
        cleanup();
        const message =
          event?.event?.message ||
          event?.message ||
          "DASH startup timed out before stream initialization";
        reject(new Error(`DASH startup failed: ${message}`));
      };

      this.dashPlayer.on("streamInitialized", succeed);
      this.dashPlayer.on("canPlay", succeed);
      this.dashPlayer.on("error", fail);
      timeoutId = setTimeout(fail, 3000);
    });
  }

  private debugLog(...args: unknown[]): void {
    if (this.debugging) {
      console.debug(...args);
    }
  }

  private createInternalRejectionHandler(
    options: PlayerOptions
  ): (e: PromiseRejectionEvent) => void {
    return (e: PromiseRejectionEvent) => {
      if (this.destroyed) return;
      const reason = e.reason;
      const msg = reason?.message || String(reason);
      const stack = typeof reason?.stack === "string" ? reason.stack : "";

      // Only claim rejections that are actually dash.js's. The generic null /
      // "range" symptoms below are the known DVR/SIDX crash signature, but on
      // their own they also match unrelated app rejections fired while this
      // player is alive — so gate them on a dash.js stack frame. Specific
      // dash.js API signatures are claimed regardless.
      const fromDashjs = stack.includes("dashjs") || stack.includes("dash.all");
      const knownDashSignature =
        msg.includes("getCurrentDVRInfo") || // dash.js DVR-window race
        /\bsidx\b|segmentbase/i.test(msg); // SegmentBase SIDX loader crash on live CMAF
      const genericDashSymptom =
        fromDashjs &&
        (msg.includes("range") ||
          msg.includes("Cannot read properties of null") ||
          msg.includes("can't access property"));

      if (!knownDashSignature && !genericDashSymptom) return;

      e.preventDefault();
      e.stopImmediatePropagation?.();
      console.warn("[DashJS] Caught internal dash.js rejection:", msg);
      this.reportDashFailure(`DASH fatal internal error: ${msg}`, options);
    };
  }

  async initialize(
    container: HTMLElement,
    source: StreamSource,
    options: PlayerOptions,
    streamInfo?: StreamInfo
  ): Promise<HTMLVideoElement> {
    this.destroyed = false;
    this.debugging = options.debug === true;
    this.container = container;
    this.subsLoaded = false;
    this.pendingSubtitleId = null;
    container.classList.add("fw-player-container");

    // Detect stream type from gateway/Mist metadata. source.type is the MIME
    // protocol, not live/VOD state.
    if (isLiveStreamType(streamInfo?.type)) {
      this.streamType = "live";
    } else if (streamInfo?.type === "vod") {
      this.streamType = "vod";
    } else {
      this.streamType = "unknown";
    }

    // Create video element
    const video = document.createElement("video");
    video.classList.add("fw-player-video");
    video.setAttribute("playsinline", "");
    video.setAttribute("crossorigin", "anonymous");

    // Apply options (ported from reference dashjs.js:129-142)
    if (options.autoplay) video.autoplay = true;
    if (options.muted) video.muted = true;
    video.controls = options.controls === true;
    // Loop only for VoD (reference dashjs.js: live streams don't loop)
    if (options.loop && this.streamType !== "live") video.loop = true;
    if (options.poster) video.poster = options.poster;

    // Create proxy for live duration handling (reference dashjs.js:81-122)
    this.videoProxy = this.createVideoProxy(video);
    this.videoElement = video;
    container.appendChild(video);

    try {
      // Dynamic import of DASH.js
      this.debugLog("[DashJS] Importing dashjs module...");
      const mod = await import("dashjs");
      const dashjs = (mod as any).default || (mod as any);
      this.debugLog("[DashJS] Module imported:", dashjs);

      this.dashPlayer = dashjs.MediaPlayer().create();
      this.debugLog("[DashJS] MediaPlayer created");
      if (options.playbackHeaders && typeof this.dashPlayer.addRequestInterceptor === "function") {
        const playbackHeaders = options.playbackHeaders;
        this.dashPlayer.addRequestInterceptor((request: any) => {
          request.headers = { ...(request.headers ?? {}), ...playbackHeaders };
          return request;
        });
      }

      // Set up event logging (reference dashjs.js:152-160)
      this.setupEventLogging(dashjs);

      // Set up subtitle handling (reference dashjs.js:173-197)
      this.setupSubtitleHandling();

      this.dashPlayer.on("error", (e: any) => {
        if (this.destroyed) return;
        const error = `DASH error: ${e?.event?.message || e?.message || "unknown"}`;
        console.error("[DashJS] Error event:", e);
        this.reportDashFailure(error, options);
      });

      // dash.js has internal unhandled promise rejections (e.g. SegmentBase SIDX
      // loader crashes on live CMAF streams). Catch these and surface as errors
      // so PlayerController can fall back to another player/protocol.
      this._rejectionHandler = this.createInternalRejectionHandler(options);
      window.addEventListener("unhandledrejection", this._rejectionHandler);

      // Log key dashjs events for debugging
      this.dashPlayer.on("manifestLoaded", (e: any) => {
        this.debugLog("[DashJS] manifestLoaded:", e);
      });
      this.dashPlayer.on("canPlay", () => {
        this.debugLog("[DashJS] canPlay event");
      });

      // Log stream initialization for debugging
      this.dashPlayer.on("streamInitialized", () => {
        if (this.destroyed) return;
        const isDynamic = this.dashPlayer.isDynamic?.() ?? false;
        this.debugLog("[DashJS v5] streamInitialized - isDynamic:", isDynamic);
      });

      // Configure dashjs v5 streaming settings BEFORE initialization
      this.dashPlayer.updateSettings({
        streaming: {
          buffer: {
            fastSwitchEnabled: true,
            bufferTimeDefault: 4,
            bufferTimeAtTopQuality: 8,
            bufferTimeAtTopQualityLongForm: 15,
            bufferToKeep: 10,
            bufferPruningInterval: 10,
          },
          gaps: {
            jumpGaps: true,
            jumpLargeGaps: true,
            smallGapLimit: 1.5,
            threshold: 0.3,
          },
          abr: {
            autoSwitchBitrate: { video: true, audio: true },
            limitBitrateByPortal: false,
            initialBitrate: { video: 5_000_000, audio: 128_000 },
          },
          liveCatchup: {
            enabled: true,
            maxDrift: 1.5,
            playbackRate: { max: 0.15, min: -0.15 },
            playbackBufferMin: 0.3,
            mode: "liveCatchupModeDefault",
          },
          retryAttempts: {
            MPD: 5,
            MediaSegment: 5,
            InitializationSegment: 5,
            BitstreamSwitchingSegment: 5,
            IndexSegment: 5,
            XLinkExpansion: 3,
            other: 3,
          },
          retryIntervals: {
            MPD: 1000,
            MediaSegment: 1000,
            InitializationSegment: 1000,
            BitstreamSwitchingSegment: 1000,
            IndexSegment: 1000,
            XLinkExpansion: 1000,
            other: 1000,
          },
          abandonLoadTimeout: 5000,
          text: { defaultEnabled: false },
          delay: {
            liveDelay: 8,
            liveDelayFragmentCount: null,
            useSuggestedPresentationDelay: false,
          },
        },
        debug: {
          logLevel: 2,
        },
      });

      // Caller overrides on top of the defaults above. dash.js's updateSettings
      // deep-merges, so a second call lets options.dashConfig win on overlapping
      // leaf keys while leaving the rest of the defaults intact.
      if (options.dashConfig) {
        this.dashPlayer.updateSettings(options.dashConfig as Record<string, unknown>);
      }

      if (this.debugging) {
        this.dashPlayer.on("fragmentLoadingStarted", (e: any) => {
          this.debugLog("[DashJS] Fragment loading started:", e.request?.url?.split("/").pop());
        });
        this.dashPlayer.on("fragmentLoadingCompleted", (e: any) => {
          this.debugLog("[DashJS] Fragment loading completed:", e.request?.url?.split("/").pop());
        });
      }
      this.dashPlayer.on("fragmentLoadingAbandoned", (e: any) => {
        console.warn("[DashJS] Fragment loading ABANDONED:", e.request?.url?.split("/").pop(), e);
      });
      this.dashPlayer.on("fragmentLoadingFailed", (e: any) => {
        console.error("[DashJS] Fragment loading FAILED:", e.request?.url?.split("/").pop(), e);
      });

      // dashjs v5: Initialize with URL
      this.debugLog("[DashJS v5] Initializing with URL:", source.url);
      const dashStartup = this.waitForDashStartup();
      this.dashPlayer.initialize(video, source.url, false);
      this.debugLog("[DashJS v5] Initialize called");
      await dashStartup;
      if (this.destroyed) {
        throw new Error("DASH player destroyed during initialization");
      }

      // Emit ready only after dash.js has initialized the dynamic stream.
      // PlayerController owns autoplay; dash.js autoplay races with MSE attachment
      // and can leave the video element paused=false but still buffering.
      this.setupVideoEventListeners(video, options);
      this.setupStalledHandling();

      // Optional subtitle tracks helper from source extras (external tracks)
      try {
        const subs = (source as any).subtitles as Array<{
          label: string;
          lang: string;
          src: string;
        }>;
        if (Array.isArray(subs)) {
          subs.forEach((s, idx) => {
            const track = document.createElement("track");
            track.kind = "subtitles";
            track.label = s.label;
            track.srclang = s.lang;
            track.src = s.src;
            if (idx === 0) track.default = true;
            video.appendChild(track);
          });
        }
      } catch {}

      return video;
    } catch (error: any) {
      this.reportDashFailure(error.message || String(error), options);
      throw error;
    }
  }

  /**
   * Get DASH.js-specific stats for ABR and playback monitoring
   * Updated for dashjs v5 API
   */
  async getStats(): Promise<
    | {
        type: "dash";
        currentQuality: number;
        bufferLevel: number;
        bitrateInfoList: Array<{ bitrate: number; width: number; height: number }>;
        currentBitrate: number;
        playbackRate: number;
      }
    | undefined
  > {
    if (!this.dashPlayer || !this.videoElement) return undefined;

    try {
      // dashjs v5: getCurrentRepresentationForType returns Representation object
      const currentRep = this.dashPlayer.getCurrentRepresentationForType?.("video");
      // dashjs v5: getRepresentationsByType returns Representation[] (bandwidth instead of bitrate)
      const representations = this.dashPlayer.getRepresentationsByType?.("video") || [];
      const bufferLevel = this.dashPlayer.getBufferLength("video") || 0;

      // Find current quality index
      const currentIndex = currentRep
        ? representations.findIndex((r: any) => r.id === currentRep.id)
        : 0;

      return {
        type: "dash",
        currentQuality: currentIndex >= 0 ? currentIndex : 0,
        bufferLevel,
        bitrateInfoList: representations.map((r: any) => ({
          bitrate: r.bandwidth || 0, // v5 uses 'bandwidth' not 'bitrate'
          width: r.width || 0,
          height: r.height || 0,
        })),
        currentBitrate: currentRep?.bandwidth || 0,
        playbackRate: this.videoElement.playbackRate,
      };
    } catch {
      return undefined;
    }
  }

  /**
   * Set playback rate
   */
  setPlaybackRate(rate: number): void {
    if (this.videoElement) {
      this.videoElement.playbackRate = rate;
    }
  }

  /**
   * Set source URL for seamless source switching.
   * Ported from reference dashjs.js:166-168.
   */
  setSource(url: string): void {
    if (this.dashPlayer) {
      this.dashPlayer.attachSource(url);
    }
  }

  /**
   * Get duration using proxy for live streams.
   * Returns synthetic growing duration for live content.
   */
  getDuration(): number {
    let sec: number;
    // Use proxy if available for live duration handling
    if (this.videoProxy && this.isLiveStream()) {
      sec = (this.videoProxy as any).duration ?? 0;
    } else {
      sec = this.videoElement?.duration ?? 0;
    }
    if (!Number.isFinite(sec)) return sec;
    return sec * 1000;
  }

  /**
   * Jump to live edge for live streams.
   * Uses DASH.js seekToLive API when available.
   */
  jumpToLive(): void {
    const video = this.videoElement;
    if (!video || !this.isLiveStream()) return;

    // DASH.js has a seekToLive method for live streams
    if (this.dashPlayer && typeof this.dashPlayer.seekToLive === "function") {
      this.debugLog("[DashJS] jumpToLive using seekToLive()");
      this.dashPlayer.seekToLive();
      return;
    }

    // Fallback: seek to end of seekable range
    if (video.seekable && video.seekable.length > 0) {
      const liveEdge = video.seekable.end(video.seekable.length - 1);
      if (isFinite(liveEdge) && liveEdge > 0) {
        this.debugLog("[DashJS] jumpToLive using seekable.end:", liveEdge);
        video.currentTime = liveEdge;
      }
    }
  }

  /**
   * Get latency from live edge (for live streams)
   */
  getLiveLatency(): number {
    const video = this.videoElement;
    if (!video || !this.isLiveStream()) return 0;

    // DASH.js provides live delay metrics
    if (this.dashPlayer && typeof this.dashPlayer.getCurrentLiveLatency === "function") {
      return this.dashPlayer.getCurrentLiveLatency() * 1000;
    }

    // Fallback: calculate from seekable end
    if (video.seekable && video.seekable.length > 0) {
      const liveEdge = video.seekable.end(video.seekable.length - 1);
      if (isFinite(liveEdge)) {
        return Math.max(0, (liveEdge - video.currentTime) * 1000);
      }
    }

    return 0;
  }

  async destroy(): Promise<void> {
    this.destroyed = true;
    this.subsLoaded = false;
    this.pendingSubtitleId = null;
    this.videoProxy = null;

    if (this._rejectionHandler) {
      window.removeEventListener("unhandledrejection", this._rejectionHandler);
      this._rejectionHandler = null;
    }

    if (this.dashPlayer) {
      try {
        this.dashPlayer.reset();
      } catch (e) {
        console.warn("Error destroying DASH.js:", e);
      }
      this.dashPlayer = null;
    }

    if (this.videoElement && this.container) {
      try {
        this.container.removeChild(this.videoElement);
      } catch {}
    }

    this.videoElement = null;
    this.container = null;
    this.listeners.clear();
  }

  getQualities(): Array<{
    id: string;
    label: string;
    bitrate?: number;
    width?: number;
    height?: number;
    isAuto?: boolean;
    active?: boolean;
  }> {
    const out: any[] = [];
    const v = this.videoElement;
    if (!this.dashPlayer || !v) return out;
    try {
      // dashjs v5: getRepresentationsByType returns Representation[] (bandwidth instead of bitrate)
      const representations = this.dashPlayer.getRepresentationsByType?.("video") || [];
      const settings = this.dashPlayer.getSettings?.();
      const isAutoEnabled = settings?.streaming?.abr?.autoSwitchBitrate?.video !== false;

      out.push({ id: "auto", label: "Auto", isAuto: true, active: isAutoEnabled });
      representations.forEach((rep: any, i: number) => {
        out.push({
          id: String(i),
          label: formatQualityLabel(rep.width, rep.height, rep.bandwidth),
          bitrate: rep.bandwidth, // v5 uses 'bandwidth'
          width: rep.width,
          height: rep.height,
        });
      });
    } catch {}
    return out;
  }

  selectQuality(id: string): void {
    if (!this.dashPlayer) return;
    if (id === "auto") {
      this.dashPlayer.updateSettings({
        streaming: { abr: { autoSwitchBitrate: { video: true } } },
      });
      return;
    }
    const idx = parseInt(id, 10);
    if (!isNaN(idx)) {
      this.dashPlayer.updateSettings({
        streaming: { abr: { autoSwitchBitrate: { video: false } } },
      });
      // dashjs v5: setRepresentationForTypeByIndex instead of setQualityFor
      try {
        this.dashPlayer.setRepresentationForTypeByIndex?.("video", idx);
      } catch {}
    }
  }

  // Captions via native text tracks or dash.js API
  getTextTracks(): Array<{ id: string; label: string; lang?: string; active: boolean }> {
    const v = this.videoElement;
    if (!this.dashPlayer || !v) return [];
    const out: any[] = [];
    try {
      const textTracks = (v.textTracks || []) as any;
      for (let i = 0; i < textTracks.length; i++) {
        const tt = textTracks[i];
        out.push({
          id: String(i),
          label: tt.label || `CC ${i + 1}`,
          lang: (tt as any).language,
          active: tt.mode === "showing",
        });
      }
    } catch {}
    return out;
  }

  selectTextTrack(id: string | null): void {
    const v = this.videoElement;
    if (!this.dashPlayer || !v) return;

    // Deferred loading: wait for allTextTracksAdded (reference dashjs.js:180-186)
    if (!this.subsLoaded) {
      this.pendingSubtitleId = id;
      return;
    }

    // Try dash.js API first (reference dashjs.js:193-197)
    try {
      const dashTracks = this.dashPlayer.getTracksFor("text");
      if (dashTracks && dashTracks.length > 0) {
        const idx = id === null ? -1 : parseInt(id, 10);
        if (idx >= 0 && idx < dashTracks.length) {
          this.dashPlayer.setTextTrack(idx);
          return;
        } else if (id === null || idx < 0) {
          // Disable all dash.js text tracks
          this.dashPlayer.setTextTrack(-1);
          return;
        }
      }
    } catch {}

    // Fallback to native text tracks
    const list = v.textTracks as TextTrackList;
    for (let i = 0; i < list.length; i++) {
      const tt = list[i];
      if (id !== null && String(i) === id) tt.mode = "showing";
      else tt.mode = "disabled";
    }
  }
}

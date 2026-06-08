import { BasePlayer } from "../core/PlayerInterface";
import { isFileProtocol } from "../core/detector";
import { translateCodec } from "../core/CodecUtils";
import type {
  StreamSource,
  StreamInfo,
  PlayerOptions,
  PlayerCapability,
} from "../core/PlayerInterface";

const DEFAULT_VIDEOJS_STARTUP_TIMEOUT_MS = 10_000;

function positiveNumber(value: unknown): number | null {
  return typeof value === "number" && Number.isFinite(value) && value > 0 ? value : null;
}

function getVhsStartupTimeoutMs(vhsConfig: Record<string, unknown> | undefined): number {
  const flatTimeout = positiveNumber(vhsConfig?.timeout);
  if (flatTimeout !== null) return flatTimeout;

  const xhrConfig = vhsConfig?.xhr;
  if (xhrConfig && typeof xhrConfig === "object") {
    const xhrTimeout = positiveNumber((xhrConfig as Record<string, unknown>).timeout);
    if (xhrTimeout !== null) return xhrTimeout;
  }

  return DEFAULT_VIDEOJS_STARTUP_TIMEOUT_MS;
}

export class VideoJsPlayerImpl extends BasePlayer {
  readonly capability: PlayerCapability = {
    name: "Video.js Player",
    shortname: "videojs",
    priority: 2,
    // VideoJS only has built-in HLS support via VHS (videojs-http-streaming)
    // DASH requires videojs-contrib-dash plugin which wraps dash.js - we use DashJsPlayer directly instead
    mimes: ["html5/application/vnd.apple.mpegurl", "html5/application/vnd.apple.mpegurl;version=7"],
  };

  private videojsPlayer: any = null;
  private container: HTMLElement | null = null;
  private destroyed = false;
  private cleanupStartupWatchdog: (() => void) | null = null;

  isMimeSupported(mimetype: string): boolean {
    return this.capability.mimes.includes(mimetype);
  }

  /**
   * Classify a Video.js MediaError into a recovery action. Pure (no player
   * state) so it can be unit-tested directly.
   * - Firefox `NS_ERROR_DOM_MEDIA_OVERFLOW_ERR` → reload (segment glitch).
   * - `MediaError.code === 3` (MEDIA_ERR_DECODE), e.g. Firefox CMAF → a hard
   *   decode failure surfaced as a string carrying "decode" + the code, so
   *   ErrorClassifier maps it to CODEC_DECODE_ERROR and the controller falls back.
   */
  static classifyError(
    err: { code?: number; message?: string } | null | undefined
  ):
    | { kind: "reload"; reason: string; log: string }
    | { kind: "error"; message: string; log?: string } {
    const errorMsg = err?.message || "";
    const code = typeof err?.code === "number" ? err.code : undefined;

    if (errorMsg.includes("NS_ERROR_DOM_MEDIA_OVERFLOW_ERR")) {
      return {
        kind: "reload",
        reason: "NS_ERROR_DOM_MEDIA_OVERFLOW_ERR",
        log: "Firefox segment error, requesting reload",
      };
    }
    if (code === 3) {
      const message = `VideoJS decode error (MEDIA_ERR_DECODE code=3): ${errorMsg || "media decode failure"}`;
      return { kind: "error", message, log: message };
    }
    return { kind: "error", message: errorMsg || "VideoJS playback error" };
  }

  isBrowserSupported(
    mimetype: string,
    source: StreamSource,
    streamInfo: StreamInfo
  ): boolean | string[] {
    if (source.headers) {
      return false;
    }

    // VideoJS uses MSE (VHS) which requires http/https (not file://)
    if (isFileProtocol()) {
      return false;
    }

    // Check for HTTP/HTTPS protocol mismatch
    try {
      const sourceProtocol = new URL(source.url).protocol;
      if (typeof location !== "undefined" && location.protocol !== sourceProtocol) {
        console.debug("[VideoJS] HTTP/HTTPS mismatch - skipping");
        return false;
      }
    } catch {
      // URL parsing failed, continue with other checks
    }

    // Test codec support properly - don't just assume compatibility
    const playableTracks: string[] = [];
    const tracksByType: Record<string, typeof streamInfo.meta.tracks> = {};

    // Group tracks by type
    for (const track of streamInfo.meta.tracks) {
      if (track.type === "meta") {
        if (track.codec === "subtitle") {
          playableTracks.push("subtitle");
        }
        continue;
      }

      if (!tracksByType[track.type]) {
        tracksByType[track.type] = [];
      }
      tracksByType[track.type].push(track);
    }

    // HLS-incompatible audio codecs (VideoJS uses VHS for HLS)
    // HLS standard only supports: AAC, MP3, AC-3/E-AC-3
    const HLS_INCOMPATIBLE_AUDIO = ["OPUS", "Opus", "opus", "VORBIS", "Vorbis", "FLAC"];

    // Test codec support for video/audio tracks using canPlayType
    const testVideo = document.createElement("video");
    for (const [trackType, tracks] of Object.entries(tracksByType)) {
      let hasPlayableTrack = false;

      for (const track of tracks) {
        // Explicit HLS codec filtering - OPUS doesn't work in HLS even if canPlayType says yes
        if (trackType === "audio" && HLS_INCOMPATIBLE_AUDIO.includes(track.codec)) {
          console.debug(`[VideoJS] Codec incompatible with HLS: ${track.codec}`);
          continue;
        }

        const codecString = translateCodec(track);

        // Test with video element canPlayType
        const mimeToTest =
          trackType === "audio"
            ? `audio/mp4;codecs="${codecString}"`
            : `video/mp4;codecs="${codecString}"`;

        if (testVideo.canPlayType(mimeToTest) !== "") {
          hasPlayableTrack = true;
          break;
        } else {
          console.debug(`[VideoJS] Codec not supported: ${mimeToTest}`);
        }
      }

      if (hasPlayableTrack) {
        playableTracks.push(trackType);
      }
    }

    // If no tracks to test, assume basic support (fallback behavior)
    if (Object.keys(tracksByType).length === 0) {
      return ["video", "audio"];
    }

    return playableTracks.length > 0 ? playableTracks : false;
  }

  private clearStartupWatchdog(): void {
    this.cleanupStartupWatchdog?.();
    this.cleanupStartupWatchdog = null;
  }

  private armStartupWatchdog(
    video: HTMLVideoElement,
    timeoutMs: number,
    reportFailure: (error: Error) => void
  ): void {
    this.clearStartupWatchdog();

    let settled = false;
    let timeoutId: ReturnType<typeof setTimeout> | null = null;
    const mediaReadyEvents = ["loadeddata", "canplay", "playing"] as const;

    const cleanup = () => {
      if (settled) return;
      settled = true;
      if (timeoutId !== null) clearTimeout(timeoutId);
      for (const event of mediaReadyEvents) {
        this.videojsPlayer?.off?.(event, cleanup);
        video.removeEventListener(event, cleanup);
      }
      video.removeEventListener("error", failFromVideo);
      if (this.cleanupStartupWatchdog === cleanup) {
        this.cleanupStartupWatchdog = null;
      }
    };

    const fail = (error: Error) => {
      if (settled) return;
      cleanup();
      if (!this.destroyed) reportFailure(error);
    };

    const failFromVideo = () => {
      const message = video.error?.message || "media error before first playable frame";
      fail(new Error(`VideoJS media startup failed: ${message}`));
    };

    for (const event of mediaReadyEvents) {
      this.videojsPlayer?.on?.(event, cleanup);
      video.addEventListener(event, cleanup, { once: true });
    }
    video.addEventListener("error", failFromVideo, { once: true });
    timeoutId = setTimeout(() => {
      fail(new Error("VideoJS fatal startup timed out before media became playable"));
    }, timeoutMs);
    this.cleanupStartupWatchdog = cleanup;
  }

  async initialize(
    container: HTMLElement,
    source: StreamSource,
    options: PlayerOptions,
    _streamInfo?: StreamInfo
  ): Promise<HTMLVideoElement> {
    this.destroyed = false;
    this.container = container;
    container.classList.add("fw-player-container");

    const video = document.createElement("video");
    video.classList.add("fw-player-video");
    video.setAttribute("playsinline", "");
    video.setAttribute("crossorigin", "anonymous");
    video.className = "video-js vjs-default-skin fw-player-video";

    video.autoplay = false;
    if (options.muted) video.muted = true;
    video.controls = options.controls === true; // Explicit false to hide native controls
    if (options.loop) video.loop = true;
    if (options.poster) video.poster = options.poster;

    this.videoElement = video;
    container.appendChild(video);

    try {
      const mod = await import("video.js");
      const videojs = (mod as any).default || (mod as any);

      // When using custom controls (controls: false), disable ALL VideoJS UI elements
      const useVideoJsControls = options.controls === true;

      // Android < 7 workaround: enable overrideNative for HLS
      const androidMatch = navigator.userAgent.match(/android\s([\d.]*)/i);
      const androidVersion = androidMatch ? parseFloat(androidMatch[1]) : null;

      // Build VideoJS options
      // NOTE: We disable UI components but NOT children array - that breaks playback
      const vjsOptions: Record<string, any> = {
        autoplay: false,
        controls: useVideoJsControls,
        muted: options.muted,
        sources: [{ src: source.url, type: this.getVideoJsType(source.type) }],
        // Disable VideoJS UI components when using custom controls
        loadingSpinner: useVideoJsControls,
        bigPlayButton: useVideoJsControls,
        textTrackDisplay: useVideoJsControls, // We handle subtitles ourselves
        errorDisplay: useVideoJsControls,
        controlBar: useVideoJsControls,
        // Defaults (trackingThreshold=20, liveTolerance=15) are calibrated to not
        // fight VHS's holdback (3x target duration). Must stay enabled — we use
        // seekToLiveEdge()/liveCurrentTime() for jumpToLive and getLiveLatency.
        liveTracker: true,
        // Don't set children: [] - that can break internal VideoJS components

        // VHS (http-streaming) configuration. Keep live-edge policy library/manifest-driven by
        // default; only force the fMP4 partial-append behavior that affects CMAF parsing.
        html5: {
          vhs: {
            // CMAF/fMP4 must be appended on MP4 box boundaries. VHS partial response appends can split
            // boxes and make Firefox reject the segment as an invalid top-level box.
            handlePartialData: false,

            // Allow user overrides via options.vhsConfig
            ...options.vhsConfig,
          },
          // Android < 7 workaround
          ...(androidVersion && androidVersion < 7
            ? {
                hls: { overrideNative: true },
              }
            : {}),
        },
        nativeAudioTracks: androidVersion && androidVersion < 7 ? false : undefined,
        nativeVideoTracks: androidVersion && androidVersion < 7 ? false : undefined,
      };

      console.debug("[VideoJS] Creating player with options:", vjsOptions);
      this.videojsPlayer = videojs(video, vjsOptions);
      console.debug("[VideoJS] Player created");

      const startupTimeoutMs = getVhsStartupTimeoutMs(options.vhsConfig);
      this.armStartupWatchdog(video, startupTimeoutMs, (error) => {
        this.emit("error", error.message);
      });

      // Hide VideoJS UI completely when using custom controls
      if (!useVideoJsControls) {
        // Add class to hide all VideoJS chrome
        const wrapper = this.videojsPlayer.el();
        if (wrapper) {
          wrapper.classList.add("vjs-fw-custom-controls");
        }
      }

      // Error handling (Firefox segment-glitch reload + decode classification).
      this.videojsPlayer.on("error", () => {
        if (this.destroyed) return; // Guard against zombie callbacks
        const action = VideoJsPlayerImpl.classifyError(this.videojsPlayer?.error());
        if (action.log) console.warn("[VideoJS]", action.log);
        if (action.kind === "reload") {
          this.clearStartupWatchdog();
          this.emit("reloadrequested", { reason: action.reason });
          return;
        }
        this.clearStartupWatchdog();
        this.emit("error", action.message);
      });

      // FIX: Explicitly trigger play after VideoJS is ready
      // VideoJS autoplay option alone doesn't always work (browser policies)
      this.videojsPlayer.ready(() => {
        if (this.destroyed) return; // Guard against zombie callbacks

        // Debug: Log VideoJS tech info
        const tech = this.videojsPlayer.tech?.({ IWillNotUseThisInPlugins: true });
        console.debug(
          "[VideoJS] ready - tech:",
          tech?.name || "unknown",
          "videoWidth:",
          video.videoWidth,
          "videoHeight:",
          video.videoHeight,
          "readyState:",
          video.readyState,
          "networkState:",
          video.networkState
        );

        // DVR seeking is handled natively by VHS through the HLS playlist —
        // no startunix URL rewriting needed (that's only for progressive formats).
        const duration = this.videojsPlayer.duration();
        const isLive = !isFinite(duration);

        // Live streams may not emit canplay; synthesize it after a short delay
        if (isLive) {
          let canplayReceived = false;
          const onCanPlay = () => {
            canplayReceived = true;
          };
          video.addEventListener("canplay", onCanPlay, { once: true });
          setTimeout(() => {
            video.removeEventListener("canplay", onCanPlay);
            if (!canplayReceived && !this.destroyed && video.readyState >= 2) {
              console.debug("[VideoJS] Synthetic canplay for live stream");
              video.dispatchEvent(new Event("canplay"));
            }
          }, 500);
        }
      });

      // Listen for VideoJS loadedmetadata to track loading progress
      this.videojsPlayer.on("loadedmetadata", () => {
        console.debug(
          "[VideoJS] loadedmetadata - duration:",
          this.videojsPlayer.duration(),
          "videoWidth:",
          video.videoWidth,
          "videoHeight:",
          video.videoHeight
        );
      });

      // Debug: Track VHS (video.js http-streaming) state
      this.videojsPlayer.on("loadeddata", () => {
        const tech = this.videojsPlayer.tech?.({ IWillNotUseThisInPlugins: true });
        const vhs = tech?.vhs || tech?.hls;
        if (vhs) {
          console.debug(
            "[VideoJS] VHS state -",
            "bandwidth:",
            vhs.bandwidth,
            "seekable:",
            vhs.seekable?.()?.length > 0
              ? `${vhs.seekable().start(0)}-${vhs.seekable().end(0)}`
              : "none",
            "buffered:",
            video.buffered.length > 0 ? `${video.buffered.end(0)}s` : "none"
          );
        }
      });

      // Listen for canplay from VideoJS to ensure we transition out of buffering
      this.videojsPlayer.on("canplay", () => {
        console.debug("[VideoJS] canplay");
      });

      // Additional debug events
      this.videojsPlayer.on("playing", () => {
        console.debug("[VideoJS] playing - currentTime:", this.videojsPlayer.currentTime());
      });

      this.videojsPlayer.on("waiting", () => {
        console.debug("[VideoJS] waiting/buffering");
      });

      this.videojsPlayer.on("stalled", () => {
        console.debug("[VideoJS] stalled");
      });

      // Log video element state
      video.addEventListener("loadeddata", () => {
        console.debug(
          "[VideoJS] video loadeddata - readyState:",
          video.readyState,
          "videoWidth:",
          video.videoWidth,
          "videoHeight:",
          video.videoHeight
        );
      });

      if (this.destroyed) {
        throw new Error("VideoJS player destroyed during initialization");
      }
      this.setupVideoEventListeners(video, options, { readyEvent: "immediate" });

      return video;
    } catch (error: any) {
      this.emit("error", error.message || String(error));
      throw error;
    }
  }

  private getVideoJsType(mimeType?: string): string {
    if (!mimeType) return "application/x-mpegURL";

    // Convert our mime types to VideoJS types
    if (mimeType.includes("mpegurl")) return "application/x-mpegURL";
    if (mimeType.includes("dash")) return "application/dash+xml";
    if (mimeType.includes("mp4")) return "video/mp4";
    if (mimeType.includes("webm")) return "video/webm";

    return mimeType.replace("html5/", "");
  }

  setPlaybackRate(rate: number): void {
    super.setPlaybackRate(rate);
    try {
      if (this.videojsPlayer) this.videojsPlayer.playbackRate(rate);
    } catch {}
  }

  protected seekInBuffer(timeSec: number): void {
    if (this.videojsPlayer) {
      this.videojsPlayer.currentTime(timeSec);
    } else if (this.videoElement) {
      this.videoElement.currentTime = timeSec;
    }
  }

  /**
   * Get VideoJS-specific stats for playback monitoring
   */
  async getStats(): Promise<
    | {
        type: "videojs";
        buffered: number;
        currentTime: number;
        duration: number;
        readyState: number;
        networkState: number;
        playbackRate: number;
      }
    | undefined
  > {
    const video = this.videoElement;
    if (!video) return undefined;

    // Calculate buffered ahead of current position
    let buffered = 0;
    if (video.buffered.length > 0) {
      for (let i = 0; i < video.buffered.length; i++) {
        if (
          video.buffered.start(i) <= video.currentTime &&
          video.buffered.end(i) > video.currentTime
        ) {
          buffered = video.buffered.end(i) - video.currentTime;
          break;
        }
      }
    }

    return {
      type: "videojs",
      buffered,
      currentTime: video.currentTime,
      duration: video.duration,
      readyState: video.readyState,
      networkState: video.networkState,
      playbackRate: video.playbackRate,
    };
  }

  // ============================================================================
  // Live Stream Support
  // ============================================================================

  getDuration(): number {
    const sec = this.videoElement?.duration ?? 0;
    if (!Number.isFinite(sec)) {
      const nativeRange = this.getNativeSeekableRange();
      return nativeRange?.end ?? sec;
    }
    if (!Number.isFinite(sec)) return sec;
    return sec * 1000;
  }

  getSeekableRange(): { start: number; end: number } | null {
    return this.getNativeSeekableRange();
  }

  setSeekableRangeHint(_range: { start: number; end: number } | null): void {
    // VHS owns its MSE timeline and playlist seek window.
  }

  jumpToLive(): void {
    if (this.videojsPlayer?.liveTracker) {
      const tracker = this.videojsPlayer.liveTracker;
      if (tracker.isLive?.() && typeof tracker.seekToLiveEdge === "function") {
        tracker.seekToLiveEdge();
        // seekToLiveEdge doesn't auto-resume since VideoJS 7.18.0
        this.videojsPlayer.play();
        return;
      }
    }
    super.jumpToLive();
  }

  getLiveLatency(): number {
    const video = this.videoElement;
    if (!video) return 0;

    if (this.videojsPlayer?.liveTracker) {
      const tracker = this.videojsPlayer.liveTracker;
      if (tracker.isLive?.() && typeof tracker.liveCurrentTime === "function") {
        const liveTime = tracker.liveCurrentTime();
        if (typeof liveTime === "number" && isFinite(liveTime)) {
          return Math.max(0, (liveTime - video.currentTime) * 1000);
        }
      }
    }

    const nativeRange = this.getNativeSeekableRange();
    if (nativeRange) {
      return Math.max(0, nativeRange.end - video.currentTime * 1000);
    }

    return 0;
  }

  async destroy(): Promise<void> {
    this.destroyed = true;
    this.clearStartupWatchdog();

    if (this.videojsPlayer) {
      try {
        this.videojsPlayer.dispose();
      } catch (e) {
        console.warn("Error disposing VideoJS:", e);
      }
      this.videojsPlayer = null;
    }

    if (this.videoElement && this.container) {
      try {
        this.container.removeChild(this.videoElement);
      } catch {}
    }

    this.videoElement = null;
    this.container = null;
    this.cleanupLiveSeek();
    this.listeners.clear();
  }
}

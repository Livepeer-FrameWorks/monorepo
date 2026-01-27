import { BasePlayer } from "../core/PlayerInterface";
import { LiveDurationProxy } from "../core/LiveDurationProxy";
import type {
  StreamSource,
  StreamInfo,
  PlayerOptions,
  PlayerCapability,
} from "../core/PlayerInterface";

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
  private timeCorrection: number = 0;
  private proxyElement: HTMLVideoElement | null = null;
  private currentStreamInfo: StreamInfo | null = null;
  private liveDurationProxy: LiveDurationProxy | null = null;

  isMimeSupported(mimetype: string): boolean {
    return this.capability.mimes.includes(mimetype);
  }

  isBrowserSupported(
    mimetype: string,
    source: StreamSource,
    streamInfo: StreamInfo
  ): boolean | string[] {
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

        // Build codec string
        let codecString = track.codec;
        if (track.init) {
          // Use init data for accurate codec string like HLS.js does
          const bin2hex = (idx: number) => {
            if (!track.init || idx >= track.init.length) return "00";
            return ("0" + track.init.charCodeAt(idx).toString(16)).slice(-2);
          };
          switch (track.codec) {
            case "H264":
              codecString = `avc1.${bin2hex(1)}${bin2hex(2)}${bin2hex(3)}`;
              break;
            case "AAC":
              codecString = "mp4a.40.2";
              break;
            case "MP3":
              codecString = "mp4a.40.34";
              break;
            case "HEVC":
              codecString = "hev1.1.6.L93.B0";
              break;
          }
        }

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

  async initialize(
    container: HTMLElement,
    source: StreamSource,
    options: PlayerOptions,
    streamInfo?: StreamInfo
  ): Promise<HTMLVideoElement> {
    this.destroyed = false;
    this.container = container;
    this.currentStreamInfo = streamInfo || null;
    container.classList.add("fw-player-container");

    const video = document.createElement("video");
    video.classList.add("fw-player-video");
    video.setAttribute("playsinline", "");
    video.setAttribute("crossorigin", "anonymous");
    video.className = "video-js vjs-default-skin fw-player-video";

    if (options.autoplay) video.autoplay = true;
    if (options.muted) video.muted = true;
    video.controls = options.controls === true; // Explicit false to hide native controls
    if (options.loop) video.loop = true;
    if (options.poster) video.poster = options.poster;

    this.videoElement = video;
    container.appendChild(video);

    this.setupVideoEventListeners(video, options);

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
        autoplay: options.autoplay,
        controls: useVideoJsControls,
        muted: options.muted,
        sources: [{ src: source.url, type: this.getVideoJsType(source.type) }],
        // Disable VideoJS UI components when using custom controls
        loadingSpinner: useVideoJsControls,
        bigPlayButton: useVideoJsControls,
        textTrackDisplay: useVideoJsControls, // We handle subtitles ourselves
        errorDisplay: useVideoJsControls,
        controlBar: useVideoJsControls,
        liveTracker: useVideoJsControls,
        // Don't set children: [] - that can break internal VideoJS components

        // VHS (http-streaming) configuration - AGGRESSIVE for fastest startup
        html5: {
          vhs: {
            // AGGRESSIVE: Start with lower quality for instant playback
            enableLowInitialPlaylist: true,

            // AGGRESSIVE: Assume 5 Mbps initially
            bandwidth: 5_000_000,

            // Persist bandwidth across sessions for returning users
            useBandwidthFromLocalStorage: true,

            // Enable partial segment processing for lower latency
            handlePartialData: true,

            // AGGRESSIVE: Very tight live range
            liveRangeSafeTimeDelta: 0.3,

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

      // Hide VideoJS UI completely when using custom controls
      if (!useVideoJsControls) {
        // Add class to hide all VideoJS chrome
        const wrapper = this.videojsPlayer.el();
        if (wrapper) {
          wrapper.classList.add("vjs-fw-custom-controls");
        }
      }

      // Error handling with Firefox NS_ERROR detection
      this.videojsPlayer.on("error", () => {
        if (this.destroyed) return; // Guard against zombie callbacks
        const err = this.videojsPlayer?.error();
        const errorMsg = err?.message || "";

        // Firefox-specific segment error - trigger reload
        if (errorMsg.includes("NS_ERROR_DOM_MEDIA_OVERFLOW_ERR")) {
          console.warn("[VideoJS] Firefox segment error, requesting reload");
          this.emit("reloadrequested", { reason: "NS_ERROR_DOM_MEDIA_OVERFLOW_ERR" });
          return;
        }

        this.emit("error", errorMsg || "VideoJS playback error");
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

        // Create time-corrected proxy for external consumers
        if (this.currentStreamInfo) {
          this.proxyElement = this.createTimeCorrectedProxy(video, this.currentStreamInfo);
        }

        // Check if live stream and set up LiveDurationProxy
        const duration = this.videojsPlayer.duration();
        if (!isFinite(duration) && !this.liveDurationProxy) {
          this.liveDurationProxy = new LiveDurationProxy(video, {
            constrainSeek: true,
            liveOffset: 0,
          });
          console.debug("[VideoJS] LiveDurationProxy initialized for live stream");
        }

        if (options.autoplay) {
          // Ensure muted for autoplay - browsers block unmuted autoplay
          if (!video.muted) {
            video.muted = true;
          }
          this.videojsPlayer.play().catch((e: any) => {
            console.warn("VideoJS autoplay failed:", e);
            // Emit a warning but don't fail - user can click play
          });
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

      // Emit seekable range updates for live streams (DVR support)
      this.videojsPlayer.on("progress", () => {
        if (this.destroyed) return;
        try {
          const seekable = this.videojsPlayer.seekable();
          if (seekable && seekable.length > 0) {
            const start = seekable.start(0);
            const end = seekable.end(seekable.length - 1);
            const bufferWindow = (end - start) * 1000; // Convert to ms
            this.emit("seekablechange", {
              start: start + this.timeCorrection,
              end: end + this.timeCorrection,
              bufferWindow,
            });
          }
        } catch {
          // Seekable not available yet
        }
      });

      return this.proxyElement || video;
    } catch (error: any) {
      this.emit("error", error.message || String(error));
      throw error;
    }
  }

  /**
   * Creates a Proxy wrapper around the video element that corrects
   * currentTime/duration/buffered using the firstms offset from MistServer.
   * This ensures timestamps align with MistServer's track metadata.
   */
  private createTimeCorrectedProxy(
    video: HTMLVideoElement,
    streamInfo: StreamInfo
  ): HTMLVideoElement {
    // Calculate correction from minimum firstms across all tracks
    let firstms = Infinity;
    for (const track of streamInfo.meta.tracks) {
      if ((track as any).firstms !== undefined && (track as any).firstms < firstms) {
        firstms = (track as any).firstms;
      }
    }
    this.timeCorrection = firstms !== Infinity ? firstms / 1000 : 0;

    // No correction needed or Proxy not supported
    if (this.timeCorrection === 0 || typeof Proxy === "undefined") {
      return video;
    }

    console.debug(
      `[VideoJS] Applying timestamp correction: ${this.timeCorrection}s (firstms=${firstms})`
    );

    const correction = this.timeCorrection;
    const vjsPlayer = this.videojsPlayer;

    return new Proxy(video, {
      get: (target, prop) => {
        if (prop === "currentTime") {
          const time = vjsPlayer ? vjsPlayer.currentTime() : target.currentTime;
          return isNaN(time) ? 0 : time + correction;
        }
        if (prop === "duration") {
          const duration = target.duration;
          return isNaN(duration) ? 0 : duration + correction;
        }
        if (prop === "buffered") {
          const buffered = target.buffered;
          return {
            length: buffered.length,
            start: (i: number) => buffered.start(i) + correction,
            end: (i: number) => buffered.end(i) + correction,
          };
        }
        const value = target[prop as keyof HTMLVideoElement];
        if (typeof value === "function") {
          return value.bind(target);
        }
        return value;
      },
      set: (target, prop, value) => {
        if (prop === "currentTime") {
          // Use VideoJS API for seeking (fixes backwards seeking in HLS)
          const correctedValue = value - correction;
          if (vjsPlayer) {
            vjsPlayer.currentTime(correctedValue);
          } else {
            target.currentTime = correctedValue;
          }
          return true;
        }
        (target as any)[prop] = value;
        return true;
      },
    }) as HTMLVideoElement;
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

  getCurrentTime(): number {
    const v = this.proxyElement || this.videoElement;
    return v?.currentTime ?? 0;
  }

  /**
   * Seek to time using VideoJS API (fixes backwards seeking in HLS).
   * Time should be in the corrected coordinate space (with firstms offset applied).
   */
  seek(time: number): void {
    const correctedTime = time - this.timeCorrection;
    if (this.videojsPlayer) {
      this.videojsPlayer.currentTime(correctedTime);
    } else if (this.videoElement) {
      this.videoElement.currentTime = correctedTime;
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

  /**
   * Check if the stream is live
   */
  isLiveStream(): boolean {
    if (this.liveDurationProxy) {
      return this.liveDurationProxy.isLive();
    }
    const video = this.videoElement;
    if (!video) return false;
    return !isFinite(video.duration);
  }

  /**
   * Get the calculated duration for live streams
   */
  getDuration(): number {
    if (this.liveDurationProxy) {
      return this.liveDurationProxy.getDuration();
    }
    return this.videoElement?.duration ?? 0;
  }

  /**
   * Jump to live edge
   * Uses VideoJS liveTracker when available, otherwise LiveDurationProxy
   */
  jumpToLive(): void {
    const video = this.videoElement;
    if (!video) return;

    // VideoJS has a liveTracker module for live streams
    if (this.videojsPlayer && this.videojsPlayer.liveTracker) {
      const tracker = this.videojsPlayer.liveTracker;
      if (tracker.isLive && tracker.isLive()) {
        const liveCurrentTime = tracker.liveCurrentTime?.();
        if (typeof liveCurrentTime === "number" && liveCurrentTime > 0) {
          console.debug("[VideoJS] jumpToLive using liveTracker:", liveCurrentTime);
          this.videojsPlayer.currentTime(liveCurrentTime);
          return;
        }
      }
    }

    // Fall back to LiveDurationProxy
    if (this.liveDurationProxy && this.liveDurationProxy.isLive()) {
      console.debug("[VideoJS] jumpToLive using LiveDurationProxy");
      this.liveDurationProxy.jumpToLive();
      return;
    }

    // VideoJS seekable fallback
    if (this.videojsPlayer) {
      try {
        const seekable = this.videojsPlayer.seekable();
        if (seekable && seekable.length > 0) {
          const liveEdge = seekable.end(seekable.length - 1);
          if (isFinite(liveEdge) && liveEdge > 0) {
            console.debug("[VideoJS] jumpToLive using seekable.end:", liveEdge);
            this.videojsPlayer.currentTime(liveEdge);
            return;
          }
        }
      } catch {}
    }

    // Native video seekable fallback
    if (video.seekable && video.seekable.length > 0) {
      const liveEdge = video.seekable.end(video.seekable.length - 1);
      if (isFinite(liveEdge) && liveEdge > 0) {
        console.debug("[VideoJS] jumpToLive using video.seekable.end:", liveEdge);
        video.currentTime = liveEdge;
      }
    }
  }

  /**
   * Provide a seekable range override for live streams.
   * Uses VideoJS liveTracker seekableEnd as the live edge when available.
   */
  getSeekableRange(): { start: number; end: number } | null {
    const video = this.videoElement;
    if (!video?.seekable || video.seekable.length === 0) return null;
    let start = video.seekable.start(0);
    let end = video.seekable.end(video.seekable.length - 1);

    if (this.videojsPlayer?.liveTracker) {
      const tracker = this.videojsPlayer.liveTracker;
      const trackerEnd = tracker.seekableEnd?.();
      const trackerStart = tracker.seekableStart?.();
      if (typeof trackerStart === "number" && Number.isFinite(trackerStart)) {
        start = trackerStart;
      }
      if (typeof trackerEnd === "number" && Number.isFinite(trackerEnd) && trackerEnd > 0) {
        end = Math.min(end, trackerEnd);
      }
    }

    if (!Number.isFinite(start) || !Number.isFinite(end) || end <= start) return null;
    return { start, end };
  }

  /**
   * Get latency from live edge (for live streams)
   */
  getLiveLatency(): number {
    const video = this.videoElement;
    if (!video) return 0;

    // VideoJS liveTracker provides seekableEnd
    if (this.videojsPlayer && this.videojsPlayer.liveTracker) {
      const tracker = this.videojsPlayer.liveTracker;
      if (tracker.isLive?.() && typeof tracker.seekableEnd === "function") {
        const liveEdge = tracker.seekableEnd();
        if (typeof liveEdge === "number" && isFinite(liveEdge)) {
          return Math.max(0, (liveEdge - video.currentTime) * 1000);
        }
      }
    }

    // Fall back to proxy
    if (this.liveDurationProxy) {
      return this.liveDurationProxy.getLatency() * 1000;
    }

    return 0;
  }

  async destroy(): Promise<void> {
    this.destroyed = true;

    if (this.liveDurationProxy) {
      this.liveDurationProxy.destroy();
      this.liveDurationProxy = null;
    }

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
    this.proxyElement = null;
    this.currentStreamInfo = null;
    this.timeCorrection = 0;
    this.listeners.clear();
  }
}

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
import type { HlsJsConfig } from "../types";

const DEFAULT_HLS_STARTUP_TIMEOUT_MS = 10_000;

function positiveNumber(value: unknown): number | null {
  return typeof value === "number" && Number.isFinite(value) && value > 0 ? value : null;
}

// Player implementation class
export class HlsJsPlayerImpl extends BasePlayer {
  readonly capability: PlayerCapability = {
    name: "HLS.js Player",
    shortname: "hlsjs",
    priority: 3,
    mimes: ["html5/application/vnd.apple.mpegurl", "html5/application/vnd.apple.mpegurl;version=7"],
  };

  private hls: any = null;
  private container: HTMLElement | null = null;
  private failureCount = 0;
  private destroyed = false;

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

    // HLS.js uses MSE which requires http/https (not file://)
    if (isFileProtocol()) {
      return false;
    }

    // Check if HLS.js is supported or native HLS is available
    const browser = getBrowserInfo();

    // If native HLS is supported (Safari/iOS), prefer that for older Android
    if (browser.isAndroid && browser.isMobile) {
      // Let VideoJS handle older Android instead
      return false;
    }

    // Check MediaSource support (required for HLS.js)
    if (!browser.supportsMediaSource) {
      if (source.headers) {
        return false;
      }
      // Fall back to native if available
      const testVideo = document.createElement("video");
      if (testVideo.canPlayType("application/vnd.apple.mpegurl")) {
        return ["video", "audio"];
      }
      return false;
    }

    // Check codec compatibility
    const playableTracks: string[] = [];
    const tracksByType: Record<string, typeof streamInfo.meta.tracks> = {};

    // If no track info available yet, assume compatible (like upstream does)
    // Track info comes async from MistServer - don't block on it
    if (!streamInfo.meta.tracks || streamInfo.meta.tracks.length === 0) {
      return ["video", "audio"]; // Assume standard tracks until we know better
    }

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

    // HLS-incompatible audio codecs (even if browser MSE supports them in fMP4)
    // HLS standard only supports: AAC, MP3, AC-3/E-AC-3
    const HLS_INCOMPATIBLE_AUDIO = ["OPUS", "Opus", "opus", "VORBIS", "Vorbis", "FLAC"];

    // Test codec support for video/audio tracks
    for (const [trackType, tracks] of Object.entries(tracksByType)) {
      let hasPlayableTrack = false;

      for (const track of tracks) {
        // Explicit HLS codec filtering - OPUS doesn't work in HLS even if MSE supports it
        if (trackType === "audio" && HLS_INCOMPATIBLE_AUDIO.includes(track.codec)) {
          console.debug(`[HLS.js] Codec incompatible with HLS: ${track.codec}`);
          continue;
        }

        const codecString = translateCodec(track);
        // Use correct container type for audio vs video tracks
        const container = trackType === "audio" ? "audio/mp4" : "video/mp4";
        const mimeType = `${container};codecs="${codecString}"`;

        if (MediaSource.isTypeSupported && MediaSource.isTypeSupported(mimeType)) {
          hasPlayableTrack = true;
          break;
        } else {
          console.debug(`[HLS.js] Codec not supported: ${mimeType}`);
        }
      }

      if (hasPlayableTrack) {
        playableTracks.push(trackType);
      }
    }

    return playableTracks.length > 0 ? playableTracks : false;
  }

  async initialize(
    container: HTMLElement,
    source: StreamSource,
    options: PlayerOptions
  ): Promise<HTMLVideoElement> {
    console.log("[HLS.js] initialize() starting for", source.url.slice(0, 60) + "...");
    this.destroyed = false;
    this.container = container;
    container.classList.add("fw-player-container");

    // Create video element
    const video = document.createElement("video");
    video.classList.add("fw-player-video");
    video.setAttribute("playsinline", "");
    video.setAttribute("crossorigin", "anonymous");

    // Apply options
    video.autoplay = false;
    if (options.muted) video.muted = true;
    video.controls = options.controls === true; // Explicit false to hide native controls
    if (options.loop) video.loop = true;
    if (options.poster) video.poster = options.poster;

    this.videoElement = video;
    container.appendChild(video);

    try {
      // Dynamic import of HLS.js
      console.log("[HLS.js] Dynamically importing hls.js module...");
      const mod = await import("hls.js");
      const Hls = (mod as any).default || (mod as any);
      console.log("[HLS.js] hls.js module imported, Hls.isSupported():", Hls.isSupported?.());

      if (Hls.isSupported()) {
        const playbackHeaders = options.playbackHeaders;
        const userXhrSetup = options.hlsConfig?.xhrSetup as
          | ((xhr: XMLHttpRequest, url: string) => void | Promise<void>)
          | undefined;
        const userFetchSetup = options.hlsConfig?.fetchSetup as
          | ((context: unknown, initParams: RequestInit) => Request | Promise<Request>)
          | undefined;

        // Keep live-edge policy library/manifest-driven by default. Mist's CMAF
        // playlists advertise HOLD-BACK/PART-HOLD-BACK; overriding those here can
        // make hls.js chase partials the server has not made retrievable yet.
        const hlsConfig: HlsJsConfig = {
          // LL-HLS support
          lowLatencyMode: true,

          // Use a realistic initial estimate without overriding live sync, live
          // max latency, or buffer lengths. Those should follow the manifest and
          // hls.js defaults unless the caller explicitly supplies hlsConfig.
          abrEwmaDefaultEstimate: 5_000_000,

          // Allow user overrides
          ...options.hlsConfig,
        };
        if (playbackHeaders) {
          hlsConfig.xhrSetup = (xhr: XMLHttpRequest, url: string) => {
            for (const [key, value] of Object.entries(playbackHeaders)) {
              xhr.setRequestHeader(key, value);
            }
            return userXhrSetup?.(xhr, url);
          };
          hlsConfig.fetchSetup = async (context: unknown, initParams: RequestInit) => {
            const headers = new Headers(initParams.headers);
            for (const [key, value] of Object.entries(playbackHeaders)) {
              headers.set(key, value);
            }
            const nextInit = { ...initParams, headers };
            return userFetchSetup
              ? userFetchSetup(context, nextInit)
              : new Request((context as any).url, nextInit);
          };
        }

        this.hls = new Hls(hlsConfig);
        const startupTimeoutMs =
          positiveNumber(hlsConfig.manifestLoadingTimeOut) ?? DEFAULT_HLS_STARTUP_TIMEOUT_MS;
        let startupSettled = false;
        let startupTimer: ReturnType<typeof setTimeout> | null = null;
        let resolveStartup: (() => void) | null = null;
        let rejectStartup: ((error: Error) => void) | null = null;
        const hlsStartup = new Promise<void>((resolve, reject) => {
          resolveStartup = resolve;
          rejectStartup = reject;
          startupTimer = setTimeout(() => {
            if (startupSettled) return;
            startupSettled = true;
            reject(new Error("HLS startup timed out before media became playable"));
          }, startupTimeoutMs);
        });
        const mediaReadyEvents = ["loadeddata", "canplay", "playing"] as const;
        let removeStartupListeners = () => {};
        const finishStartup = () => {
          if (startupSettled) return;
          startupSettled = true;
          if (startupTimer !== null) clearTimeout(startupTimer);
          removeStartupListeners();
          resolveStartup?.();
        };
        const failStartup = (error: Error) => {
          if (startupSettled) return;
          startupSettled = true;
          if (startupTimer !== null) clearTimeout(startupTimer);
          removeStartupListeners();
          rejectStartup?.(error);
        };
        const failStartupFromVideo = () => {
          const message = video.error?.message || "media error before first playable frame";
          failStartup(new Error(`HLS media startup failed: ${message}`));
        };
        removeStartupListeners = () => {
          for (const event of mediaReadyEvents) {
            video.removeEventListener(event, finishStartup);
          }
          video.removeEventListener("error", failStartupFromVideo);
        };
        for (const event of mediaReadyEvents) {
          video.addEventListener(event, finishStartup, { once: true });
        }
        video.addEventListener("error", failStartupFromVideo, { once: true });

        this.hls.attachMedia(video);

        this.hls.on(Hls.Events.MEDIA_ATTACHED, () => {
          this.hls.loadSource(source.url);
        });

        this.hls.on(Hls.Events.ERROR, (_: any, data: any) => {
          if (this.destroyed) return; // Guard against zombie callbacks
          if (data?.fatal) {
            const error = `HLS fatal error: ${data?.type || "unknown"}${
              data?.details ? `:${data.details}` : ""
            }`;
            const isMediaError =
              data?.type === Hls.ErrorTypes?.MEDIA_ERROR || data?.type === "mediaError";
            if (isMediaError && video.error && this.failureCount < 1) {
              this.failureCount++;
              try {
                this.hls.recoverMediaError();
              } catch {}
              return;
            }
            failStartup(new Error(error));
            this.emit("error", error);
          }
        });

        this.hls.on(Hls.Events.MANIFEST_PARSED, () => {
          if (this.destroyed) return; // Guard against zombie callbacks

          // DVR seeking is handled natively by HLS.js through the playlist —
          // no startunix URL rewriting needed (that's only for progressive formats).
        });
        await hlsStartup;
        if (this.destroyed) {
          throw new Error("HLS player destroyed during initialization");
        }
        this.setupVideoEventListeners(video, options, { readyEvent: "immediate" });
      } else if (video.canPlayType("application/vnd.apple.mpegurl")) {
        if (options.playbackHeaders) {
          throw new Error("Native HLS cannot attach playback Authorization headers");
        }
        // Use native HLS support
        video.src = source.url;
        this.setupVideoEventListeners(video, options);
      } else {
        throw new Error("HLS not supported in this browser");
      }

      // Optional subtitle tracks helper from source extras
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

      console.log("[HLS.js] initialize() complete, returning video element");
      return video;
    } catch (error: any) {
      this.emit("error", error.message || String(error));
      throw error;
    }
  }

  async destroy(): Promise<void> {
    console.debug("[HLS.js] destroy() called");
    this.destroyed = true;

    if (this.hls) {
      try {
        this.hls.destroy();
        console.debug("[HLS.js] hls.destroy() completed");
      } catch (e) {
        console.warn("[HLS.js] Error destroying:", e);
      }
      this.hls = null;
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
    // HLS.js owns its MSE timeline and playlist seek window.
  }

  jumpToLive(): void {
    const video = this.videoElement;
    const liveSyncPosition = this.hls?.liveSyncPosition;
    if (video && typeof liveSyncPosition === "number" && Number.isFinite(liveSyncPosition)) {
      video.currentTime = liveSyncPosition;
      return;
    }
    super.jumpToLive();
  }

  getLiveLatency(): number {
    const video = this.videoElement;
    if (!video) return 0;

    // HLS.js provides liveSyncPosition
    if (this.hls && typeof this.hls.liveSyncPosition === "number") {
      return Math.max(0, (this.hls.liveSyncPosition - video.currentTime) * 1000);
    }

    const nativeRange = this.getNativeSeekableRange();
    if (nativeRange) {
      return Math.max(0, nativeRange.end - video.currentTime * 1000);
    }

    return 0;
  }

  // ============================================================================
  // Quality API (Auto + levels)
  // ============================================================================
  getQualities(): Array<{
    id: string;
    label: string;
    bitrate?: number;
    width?: number;
    height?: number;
    isAuto?: boolean;
    active?: boolean;
  }> {
    const qualities: any[] = [];
    const video = this.videoElement;
    if (!this.hls || !video) return qualities;
    const levels = this.hls.levels || [];
    const auto = { id: "auto", label: "Auto", isAuto: true, active: this.hls.autoLevelEnabled };
    qualities.push(auto);
    levels.forEach((lvl: any, idx: number) => {
      qualities.push({
        id: String(idx),
        label: formatQualityLabel(lvl.width, lvl.height, lvl.bitrate),
        bitrate: lvl.bitrate,
        width: lvl.width,
        height: lvl.height,
        active: this.hls.currentLevel === idx,
      });
    });
    return qualities;
  }

  selectQuality(id: string): void {
    if (!this.hls) return;
    if (id === "auto") {
      this.hls.currentLevel = -1;
      return;
    }
    const idx = parseInt(id, 10);
    if (!isNaN(idx)) {
      this.hls.currentLevel = idx;
    }
  }

  // Captions via native textTracks if rendered; hls.js can also manage subtitles tracks
  getTextTracks(): Array<{ id: string; label: string; lang?: string; active: boolean }> {
    const v = this.videoElement;
    if (!v) return [];
    const list = v.textTracks;
    const out: any[] = [];
    for (let i = 0; i < list.length; i++) {
      const tt = list[i];
      out.push({
        id: String(i),
        label: tt.label || `CC ${i + 1}`,
        lang: (tt as any).language,
        active: tt.mode === "showing",
      });
    }
    return out;
  }

  selectTextTrack(id: string | null): void {
    const v = this.videoElement as any;
    if (!v) return;
    const list = v.textTracks as TextTrackList;
    for (let i = 0; i < list.length; i++) {
      const tt = list[i];
      if (id !== null && String(i) === id) tt.mode = "showing";
      else tt.mode = "disabled";
    }
  }

  // Audio track selection via HLS.js audioTracks API
  getAudioTracks(): Array<{ id: string; label: string; lang?: string; active: boolean }> {
    if (!this.hls) return [];
    const tracks = this.hls.audioTracks || [];
    const currentId = this.hls.audioTrack;
    return tracks.map((t: any, i: number) => ({
      id: String(i),
      label: t.name || t.lang || `Audio ${i + 1}`,
      lang: t.lang,
      active: i === currentId,
    }));
  }

  selectAudioTrack(id: string): void {
    if (!this.hls) return;
    const idx = parseInt(id, 10);
    if (!isNaN(idx) && idx >= 0 && idx < (this.hls.audioTracks?.length ?? 0)) {
      this.hls.audioTrack = idx;
    }
  }

  /**
   * Get HLS.js-specific stats for accurate bitrate and bandwidth
   */
  async getStats(): Promise<
    | {
        type: "hls";
        bandwidthEstimate: number;
        currentLevel: number;
        currentBitrate: number;
        loadLevel: number;
        levels: Array<{ bitrate: number; width: number; height: number }>;
        buffered: number;
        latency?: number;
      }
    | undefined
  > {
    if (!this.hls) return undefined;

    const levels = (this.hls.levels || []).map((lvl: any) => ({
      bitrate: lvl.bitrate || 0,
      width: lvl.width || 0,
      height: lvl.height || 0,
    }));

    const currentLevel = this.hls.currentLevel;
    const currentLevelData = levels[currentLevel];

    // Calculate buffered ahead
    let buffered = 0;
    const video = this.videoElement;
    if (video && video.buffered.length > 0) {
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

    // Latency for live streams
    let latency: number | undefined;
    if (video && this.hls.liveSyncPosition !== undefined && !isFinite(video.duration)) {
      latency = (this.hls.liveSyncPosition - video.currentTime) * 1000;
    }

    return {
      type: "hls",
      bandwidthEstimate: this.hls.bandwidthEstimate || 0,
      currentLevel,
      currentBitrate: currentLevelData?.bitrate || 0,
      loadLevel: this.hls.loadLevel || 0,
      levels,
      buffered,
      latency,
    };
  }
}

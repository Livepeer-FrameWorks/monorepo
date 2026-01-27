import { BasePlayer } from "../core/PlayerInterface";
import { checkProtocolMismatch, getBrowserInfo } from "../core/detector";
import { translateCodec } from "../core/CodecUtils";
import { LiveDurationProxy } from "../core/LiveDurationProxy";
import type {
  StreamSource,
  StreamInfo,
  PlayerOptions,
  PlayerCapability,
} from "../core/PlayerInterface";
import type { HlsJsConfig } from "../types";

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
  private liveDurationProxy: LiveDurationProxy | null = null;

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

    // Check if HLS.js is supported or native HLS is available
    const browser = getBrowserInfo();

    // If native HLS is supported (Safari/iOS), prefer that for older Android
    if (browser.isAndroid && browser.isMobile) {
      // Let VideoJS handle older Android instead
      return false;
    }

    // Check MediaSource support (required for HLS.js)
    if (!browser.supportsMediaSource) {
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
    if (options.autoplay) video.autoplay = true;
    if (options.muted) video.muted = true;
    video.controls = options.controls === true; // Explicit false to hide native controls
    if (options.loop) video.loop = true;
    if (options.poster) video.poster = options.poster;

    this.videoElement = video;
    container.appendChild(video);

    // Set up event listeners
    this.setupVideoEventListeners(video, options);

    try {
      // Dynamic import of HLS.js
      console.log("[HLS.js] Dynamically importing hls.js module...");
      const mod = await import("hls.js");
      const Hls = (mod as any).default || (mod as any);
      console.log("[HLS.js] hls.js module imported, Hls.isSupported():", Hls.isSupported?.());

      if (Hls.isSupported()) {
        // Build optimized HLS.js config with user overrides
        const hlsConfig: HlsJsConfig = {
          // Worker disabled for lower latency (per HLS.js maintainer recommendation)
          enableWorker: false,

          // LL-HLS support
          lowLatencyMode: true,

          // AGGRESSIVE: Assume 5 Mbps initially (not 500kbps default)
          // This dramatically improves startup time by selecting appropriate quality faster
          abrEwmaDefaultEstimate: 5_000_000,

          // AGGRESSIVE: Minimal buffers for fastest startup
          maxBufferLength: 6, // Reduced from 15 (just 2 segments @ 3s)
          maxMaxBufferLength: 15, // Reduced from 60
          backBufferLength: Infinity, // Let browser manage (per maintainer advice)

          // Stay close to live edge but not too aggressive
          liveSyncDuration: 4, // Target 4 seconds behind live edge
          liveMaxLatencyDuration: 8, // Max 8 seconds before seeking to live

          // Faster ABR adaptation for live
          abrEwmaFastLive: 2.0, // Faster than default 3.0
          abrEwmaSlowLive: 6.0, // Faster than default 9.0

          // Allow user overrides
          ...options.hlsConfig,
        };

        this.hls = new Hls(hlsConfig);

        this.hls.attachMedia(video);

        this.hls.on(Hls.Events.MEDIA_ATTACHED, () => {
          this.hls.loadSource(source.url);
        });

        this.hls.on(Hls.Events.ERROR, (_: any, data: any) => {
          if (this.destroyed) return; // Guard against zombie callbacks
          if (data?.fatal) {
            if (this.failureCount < 3) {
              this.failureCount++;
              try {
                this.hls.recoverMediaError();
              } catch {}
            } else {
              const error = `HLS fatal error: ${data?.type || "unknown"}`;
              this.emit("error", error);
            }
          }
        });

        this.hls.on(Hls.Events.MANIFEST_PARSED, () => {
          if (this.destroyed) return; // Guard against zombie callbacks

          // Set up LiveDurationProxy for live streams
          // HLS.js sets video.duration to Infinity for live streams
          const isLive = !isFinite(video.duration) || this.hls.levels?.[0]?.details?.live;
          if (isLive && !this.liveDurationProxy) {
            this.liveDurationProxy = new LiveDurationProxy(video, {
              constrainSeek: true,
              liveOffset: 0,
            });
            console.debug("[HLS.js] LiveDurationProxy initialized for live stream");
          }

          if (options.autoplay) {
            video.play().catch((e) => console.warn("HLS autoplay failed:", e));
          }
        });
      } else if (video.canPlayType("application/vnd.apple.mpegurl")) {
        // Use native HLS support
        video.src = source.url;
        if (options.autoplay) {
          video.play().catch((e) => console.warn("Native HLS autoplay failed:", e));
        }
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

    if (this.liveDurationProxy) {
      this.liveDurationProxy.destroy();
      this.liveDurationProxy = null;
    }

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
    this.listeners.clear();
  }

  // ============================================================================
  // Live Stream Support
  // ============================================================================

  /**
   * Get the calculated duration for live streams
   * Falls back to native duration for VOD
   */
  getDuration(): number {
    if (this.liveDurationProxy) {
      return this.liveDurationProxy.getDuration();
    }
    return this.videoElement?.duration ?? 0;
  }

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
   * Seek to a position with live-aware constraints
   */
  seek(time: number): void {
    const video = this.videoElement;
    if (!video) return;

    // For live streams, use the proxy which constrains to buffer
    if (this.liveDurationProxy && this.liveDurationProxy.isLive()) {
      this.liveDurationProxy.seek(time);
      return;
    }

    // For VOD, seek directly
    video.currentTime = time;
  }

  /**
   * Jump to live edge
   * Uses HLS.js liveSyncPosition when available (more accurate)
   */
  jumpToLive(): void {
    const video = this.videoElement;
    if (!video) return;

    // HLS.js provides liveSyncPosition for live streams - use that first
    if (
      this.hls &&
      typeof this.hls.liveSyncPosition === "number" &&
      this.hls.liveSyncPosition > 0
    ) {
      console.debug("[HLS.js] jumpToLive using liveSyncPosition:", this.hls.liveSyncPosition);
      video.currentTime = this.hls.liveSyncPosition;
      return;
    }

    // Fall back to LiveDurationProxy
    if (this.liveDurationProxy && this.liveDurationProxy.isLive()) {
      console.debug("[HLS.js] jumpToLive using LiveDurationProxy");
      this.liveDurationProxy.jumpToLive();
      return;
    }

    // Last resort: use seekable end
    if (video.seekable && video.seekable.length > 0) {
      const liveEdge = video.seekable.end(video.seekable.length - 1);
      if (isFinite(liveEdge) && liveEdge > 0) {
        console.debug("[HLS.js] jumpToLive using seekable.end:", liveEdge);
        video.currentTime = liveEdge;
      }
    }
  }

  /**
   * Provide a seekable range override for live streams.
   * Uses liveSyncPosition as the live edge to avoid waiting for the absolute end.
   */
  getSeekableRange(): { start: number; end: number } | null {
    const video = this.videoElement;
    if (!video?.seekable || video.seekable.length === 0) return null;
    const start = video.seekable.start(0);
    let end = video.seekable.end(video.seekable.length - 1);

    if (
      this.liveDurationProxy?.isLive() &&
      this.hls &&
      typeof this.hls.liveSyncPosition === "number"
    ) {
      const sync = this.hls.liveSyncPosition;
      if (Number.isFinite(sync) && sync > 0) {
        end = Math.min(end, sync);
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

    // HLS.js provides liveSyncPosition
    if (this.hls && typeof this.hls.liveSyncPosition === "number") {
      return Math.max(0, (this.hls.liveSyncPosition - video.currentTime) * 1000);
    }

    // Fall back to proxy
    if (this.liveDurationProxy) {
      return this.liveDurationProxy.getLatency() * 1000;
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
        label: lvl.height ? `${lvl.height}p` : `${Math.round((lvl.bitrate || 0) / 1000)}kbps`,
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
      this.hls.autoLevelEnabled = true;
      return;
    }
    const idx = parseInt(id, 10);
    if (!isNaN(idx)) {
      this.hls.autoLevelEnabled = false;
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

import { BasePlayer } from "../core/PlayerInterface";
import { LiveDurationProxy } from "../core/LiveDurationProxy";
import {
  checkProtocolMismatch,
  getBrowserInfo,
  checkWebRTCCodecCompatibility,
} from "../core/detector";
import type {
  StreamSource,
  StreamInfo,
  PlayerOptions,
  PlayerCapability,
} from "../core/PlayerInterface";

/**
 * Native Player Implementation
 *
 * Handles direct playback using native browser APIs:
 * - HTML5 video element for direct media
 * - WHEP (WebRTC HTTP Egress Protocol) for WebRTC streams
 *
 * Ported from reference html5.js with:
 * - Live duration proxy for meaningful seek bar
 * - Auto-recovery on long pause (reload after 5s)
 * - MP3 seeking restriction
 * - Dynamic source switching via setSource()
 */
export class NativePlayerImpl extends BasePlayer {
  readonly capability: PlayerCapability = {
    name: "Native Player",
    shortname: "native",
    priority: 1, // Highest priority as it's most compatible
    mimes: [
      "html5/video/mp4",
      "html5/video/webm",
      "html5/video/ogg",
      "html5/audio/mp3",
      "html5/audio/webm",
      "html5/audio/ogg",
      "html5/audio/wav",
      "html5/application/vnd.apple.mpegurl", // Native HLS on Safari/iOS
      "html5/application/vnd.apple.mpegurl;version=7",
      "whep",
    ],
  };

  private peerConnection: RTCPeerConnection | null = null;
  private sessionUrl: string | null = null;
  private lastInboundStats: any = null;
  private reconnectEnabled = false;
  private reconnectAttempts = 0;
  private maxReconnectAttempts = 3;
  private reconnectTimer: any = null;
  private currentWhepUrl: string | null = null;
  private currentHeaders: Record<string, string> | null = null;
  private currentIceServers: RTCIceServer[] | null = null;
  private container: HTMLElement | null = null;
  private destroyed = false;

  // Reference html5.js features
  private liveDurationProxy: LiveDurationProxy | null = null;
  private pausedAt: number | null = null;
  private currentSourceUrl: string | null = null;
  private currentMimeType: string | null = null;
  private sourceElement: HTMLSourceElement | null = null; // legacy, always null now
  private isMP3Source = false;

  // Auto-recovery threshold (reference: 5 seconds)
  private static readonly PAUSE_RECOVERY_THRESHOLD = 5000;

  isMimeSupported(mimetype: string): boolean {
    return this.capability.mimes.indexOf(mimetype) !== -1;
  }

  isBrowserSupported(
    mimetype: string,
    source: StreamSource,
    streamInfo: StreamInfo
  ): boolean | string[] {
    if (mimetype === "whep") {
      // Check basic WebRTC support
      if (!("RTCPeerConnection" in window) || !("fetch" in window)) return false;

      // Check codec compatibility - WebRTC can only carry certain codecs
      const codecCompat = checkWebRTCCodecCompatibility(streamInfo.meta.tracks);
      if (!codecCompat.compatible) {
        if (codecCompat.incompatibleCodecs.length > 0) {
          console.debug(
            "[WHEP] Skipping - incompatible codecs:",
            codecCompat.incompatibleCodecs.join(", ")
          );
        }
        return false;
      }

      // Return which track types we can play
      const playable: string[] = [];
      if (codecCompat.details.compatibleVideoCodecs.length > 0) {
        playable.push("video");
      }
      if (codecCompat.details.compatibleAudioCodecs.length > 0) {
        playable.push("audio");
      }

      return playable.length > 0 ? playable : false;
    }
    // Check protocol mismatch
    if (checkProtocolMismatch(source.url)) {
      // Allow file:// -> http:// but warn
      if (!(window.location.protocol === "file:" && source.url.startsWith("http:"))) {
        return false;
      }
    }

    const browser = getBrowserInfo();

    // Safari cannot play WebM - skip entirely
    // Reference: html5.js:28-29
    if (mimetype === "html5/video/webm" && browser.isSafari) {
      return false;
    }

    // Special handling for HLS
    if (mimetype === "html5/application/vnd.apple.mpegurl") {
      // Check for native HLS support
      const testVideo = document.createElement("video");
      if (testVideo.canPlayType("application/vnd.apple.mpegurl")) {
        // Prefer VideoJS for older Android
        const androidVersion = this.getAndroidVersion();
        if (androidVersion && androidVersion < 7) {
          return false; // Let VideoJS handle it
        }
        return ["video", "audio"];
      }
      return false;
    }

    // Test codec support for regular media types
    const supportedTracks: string[] = [];
    const testVideo = document.createElement("video");

    // Extract the actual mime type from the format
    const shortMime = mimetype.replace("html5/", "");

    // For codec testing, we need to check against stream info
    const tracksByType: Record<string, typeof streamInfo.meta.tracks> = {};
    for (const track of streamInfo.meta.tracks) {
      if (track.type === "meta") {
        if (track.codec === "subtitle") {
          supportedTracks.push("subtitle");
        }
        continue;
      }

      if (!tracksByType[track.type]) {
        tracksByType[track.type] = [];
      }
      tracksByType[track.type].push(track);
    }

    // Test each track type
    for (const [trackType, tracks] of Object.entries(tracksByType)) {
      let hasPlayableTrack = false;

      for (const track of tracks) {
        // Build codec string for testing
        let codecString = "";
        if (track.codecstring) {
          codecString = track.codecstring;
        } else {
          codecString = this.translateCodecForHtml5(track);
        }

        const testMimeType = `${shortMime};codecs="${codecString}"`;

        // Special handling for WebM - Chrome reports issues with codec strings
        if (shortMime === "video/webm") {
          if (testVideo.canPlayType(shortMime) !== "") {
            hasPlayableTrack = true;
            break;
          }
        } else {
          if (testVideo.canPlayType(testMimeType) !== "") {
            hasPlayableTrack = true;
            break;
          }
        }
      }

      if (hasPlayableTrack) {
        supportedTracks.push(trackType);
      }
    }

    return supportedTracks.length > 0 ? supportedTracks : false;
  }

  private translateCodecForHtml5(track: {
    codec: string;
    codecstring?: string;
    init?: string;
  }): string {
    if (track.codecstring) return track.codecstring;

    const bin2hex = (index: number) => {
      if (!track.init || index >= track.init.length) return "00";
      return ("0" + track.init.charCodeAt(index).toString(16)).slice(-2);
    };

    switch (track.codec) {
      case "AAC":
        return "mp4a.40.2";
      case "MP3":
        return "mp4a.40.34";
      case "AC3":
        return "ec-3";
      case "H264":
        return `avc1.${bin2hex(1)}${bin2hex(2)}${bin2hex(3)}`;
      case "HEVC":
        return `hev1.${bin2hex(1)}${bin2hex(6)}${bin2hex(7)}${bin2hex(8)}${bin2hex(9)}${bin2hex(10)}${bin2hex(11)}${bin2hex(12)}`;
      case "VP8":
        return "vp8";
      case "VP9":
        return "vp09.00.10.08";
      case "AV1":
        return "av01.0.04M.08";
      case "Opus":
        return "opus";
      default:
        return track.codec.toLowerCase();
    }
  }

  private getAndroidVersion(): number | null {
    const match = navigator.userAgent.match(/Android (\d+)(?:\.(\d+))?(?:\.(\d+))*/i);
    if (!match) return null;

    const major = parseInt(match[1], 10);
    const minor = match[2] ? parseInt(match[2], 10) : 0;

    return major + minor / 10;
  }

  async initialize(
    container: HTMLElement,
    source: StreamSource,
    options: PlayerOptions,
    streamInfo?: StreamInfo
  ): Promise<HTMLVideoElement> {
    // Reset destroyed flag for reuse
    this.destroyed = false;
    this.container = container;
    this.currentSourceUrl = source.url;
    this.currentMimeType = source.type;
    this.isMP3Source = source.type === "html5/audio/mp3";
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

    // Setup reference features for HTML5 playback
    // Use LiveDurationProxy for all live streams (non-WHEP)
    // WHEP handles its own live edge via signaling
    // This enables seeking and jump-to-live for native MP4/WebM/HLS live streams
    const isLiveStream = streamInfo?.type === "live";
    if (source.type !== "whep" && isLiveStream) {
      // Upstream html5.js:158-160: force loop=false for live
      video.loop = false;
      this.setupAutoRecovery(video);
      this.setupLiveDurationProxy(video);
      // startunix URL rewriting only works for progressive formats (MP4/MPEG-TS/WebM).
      // For HLS, the browser's native HLS stack handles DVR seeking via the playlist —
      // startunix rewrites cause 404s ("Fragment out of range").
      const isHLS = source.type?.includes("mpegurl");
      if (!isHLS) {
        this.initLiveSeek(source.url);
      }
    }

    // Optional subtitle tracks helper from source extras
    try {
      const subs = (source as any).subtitles as Array<{ label: string; lang: string; src: string }>;
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

    try {
      if (source.type === "whep") {
        // Read optional settings from source
        const s: any = source as any;
        const headers = s && s.headers ? (s.headers as Record<string, string>) : {};
        const iceServers = s && s.iceServers ? (s.iceServers as RTCIceServer[]) : [];
        this.reconnectEnabled = !!(s && s.reconnect);
        this.currentWhepUrl = source.url;
        this.currentHeaders = headers;
        this.currentIceServers = iceServers;
        await this.startWhep(video, source.url, headers, iceServers);
        return video;
      } else {
        video.src = source.url;
        return video;
      }
    } catch (error: any) {
      this.emit("error", error.message || String(error));
      throw error;
    }
  }

  /**
   * Setup live duration proxy for meaningful seek bar on live streams
   * Ported from reference html5.js:194-202
   */
  private setupLiveDurationProxy(video: HTMLVideoElement): void {
    this.liveDurationProxy = new LiveDurationProxy(video, {
      constrainSeek: true,
      // Duration changes are handled by UI polling getDuration()
    });
  }

  /**
   * Setup auto-recovery on long pause
   * Ported from reference html5.js:227-239
   *
   * If the stream has been paused for more than 5 seconds,
   * reload the stream on play to recover from stale buffer.
   */
  private setupAutoRecovery(video: HTMLVideoElement): void {
    video.addEventListener("pause", () => {
      if (this.destroyed) return;
      this.pausedAt = Date.now();
    });

    video.addEventListener("play", () => {
      if (this.destroyed) return;

      // Check if we need to recover from long pause
      if (this.pausedAt && this.liveSeekEnabled) {
        const pauseDuration = Date.now() - this.pausedAt;
        if (pauseDuration > NativePlayerImpl.PAUSE_RECOVERY_THRESHOLD) {
          console.debug(
            "[NativePlayer] Auto-recovery: reloading stream after",
            pauseDuration,
            "ms pause"
          );
          video.load();
        }
      }
      this.pausedAt = null;
    });
  }

  /**
   * Set a new source URL dynamically
   * Ported from reference html5.js:276-281
   */
  setSource(url: string): void {
    if (!this.videoElement) return;
    this.currentSourceUrl = url;
    if (this.liveSeekEnabled) {
      this.initLiveSeek(url);
    }
    this.videoElement.src = url;
    this.videoElement.load();
  }

  /**
   * Override seek for MP3 files (seeking not supported)
   * Ported from reference html5.js:185-191
   */
  seek(timeMs: number): void {
    if (this.isMP3Source) return;
    super.seek(timeMs);
  }

  canSeek(): boolean {
    const v = this.videoElement;
    if (!v) return false;
    // MediaStream sources (WHEP/WebRTC) are real-time — nothing to seek through
    if (v.srcObject instanceof MediaStream) return false;
    return true;
  }

  getLiveLatency(): number {
    return this.liveDurationProxy?.getLatency() ?? 0;
  }

  protected reloadSource(url: string): void {
    if (!this.videoElement) return;
    if (url === this.currentSourceUrl) return;
    const wasPlaying = !this.videoElement.paused;
    this.currentSourceUrl = url;
    this.videoElement.src = url;
    this.videoElement.load();
    this._anchorRaw = 0;
    if (wasPlaying) this.videoElement.play().catch(() => {});
  }

  async destroy(): Promise<void> {
    // Set destroyed flag immediately to guard against async callbacks
    this.destroyed = true;

    // Cleanup live duration proxy
    if (this.liveDurationProxy) {
      this.liveDurationProxy.destroy();
      this.liveDurationProxy = null;
    }

    if (this.reconnectTimer) {
      try {
        clearTimeout(this.reconnectTimer);
      } catch {}
      this.reconnectTimer = null;
    }

    // Best-effort WHEP session DELETE (CORS may block this)
    if (this.sessionUrl) {
      const url = this.sessionUrl;
      this.sessionUrl = null;
      fetch(url, { method: "DELETE" }).catch(() => {
        // Silently ignore - CORS often blocks DELETE, session will timeout on server
      });
    }

    if (this.peerConnection) {
      try {
        this.peerConnection.close();
      } catch {}
      this.peerConnection = null;
    }

    if (this.videoElement) {
      try {
        (this.videoElement as any).srcObject = null;
      } catch {}
      this.videoElement.pause();
      this.videoElement.removeAttribute("src");
      // Note: Don't call load() - it triggers "Empty src attribute" error event

      if (this.container) {
        try {
          this.container.removeChild(this.videoElement);
        } catch {}
      }
    }

    this.videoElement = null;
    this.sourceElement = null;
    this.container = null;
    this.pausedAt = null;
    this.currentSourceUrl = null;
    this.currentMimeType = null;
    this.cleanupLiveSeek();
    this.listeners.clear();
  }

  /**
   * Get WebRTC-specific stats including RTT, packet loss, jitter, bitrate
   */
  async getStats(): Promise<
    | {
        type: "webrtc";
        video?: {
          bytesReceived: number;
          packetsReceived: number;
          packetsLost: number;
          packetLossRate: number;
          jitter: number;
          framesDecoded: number;
          framesDropped: number;
          frameDropRate: number;
          frameWidth: number;
          frameHeight: number;
          framesPerSecond: number;
          bitrate: number;
          jitterBufferDelay: number;
        };
        audio?: {
          bytesReceived: number;
          packetsReceived: number;
          packetsLost: number;
          packetLossRate: number;
          jitter: number;
          bitrate: number;
        };
        network?: {
          rtt: number;
          availableOutgoingBitrate: number;
          availableIncomingBitrate: number;
          bytesSent: number;
          bytesReceived: number;
        };
        timestamp: number;
      }
    | undefined
  > {
    if (!this.peerConnection) return undefined;
    try {
      const stats = await this.peerConnection.getStats();
      const now = Date.now();
      const result: any = { type: "webrtc", timestamp: now };

      stats.forEach((report: any) => {
        if (report.type === "inbound-rtp") {
          const packetLossRate =
            report.packetsReceived > 0
              ? (report.packetsLost / (report.packetsReceived + report.packetsLost)) * 100
              : 0;

          // Calculate bitrate from previous sample
          let bitrate = 0;
          if (this.lastInboundStats && this.lastInboundStats[report.kind]) {
            const prev = this.lastInboundStats[report.kind];
            const timeDelta = (now - (this.lastInboundStats.timestamp || 0)) / 1000;
            if (timeDelta > 0) {
              const bytesDelta = report.bytesReceived - (prev.bytesReceived || 0);
              bitrate = Math.round((bytesDelta * 8) / timeDelta); // bits per second
            }
          }

          if (report.kind === "video") {
            const frameDropRate =
              report.framesDecoded > 0
                ? (report.framesDropped / (report.framesDecoded + report.framesDropped)) * 100
                : 0;

            result.video = {
              bytesReceived: report.bytesReceived || 0,
              packetsReceived: report.packetsReceived || 0,
              packetsLost: report.packetsLost || 0,
              packetLossRate,
              jitter: (report.jitter || 0) * 1000, // Convert to ms
              framesDecoded: report.framesDecoded || 0,
              framesDropped: report.framesDropped || 0,
              frameDropRate,
              frameWidth: report.frameWidth || 0,
              frameHeight: report.frameHeight || 0,
              framesPerSecond: report.framesPerSecond || 0,
              bitrate,
              jitterBufferDelay:
                report.jitterBufferDelay && report.jitterBufferEmittedCount
                  ? (report.jitterBufferDelay / report.jitterBufferEmittedCount) * 1000 // ms
                  : 0,
            };
          }
          if (report.kind === "audio") {
            result.audio = {
              bytesReceived: report.bytesReceived || 0,
              packetsReceived: report.packetsReceived || 0,
              packetsLost: report.packetsLost || 0,
              packetLossRate,
              jitter: (report.jitter || 0) * 1000, // Convert to ms
              bitrate,
            };
          }
        }
        if (report.type === "candidate-pair" && report.nominated) {
          result.network = {
            rtt: report.currentRoundTripTime ? report.currentRoundTripTime * 1000 : 0, // ms
            availableOutgoingBitrate: report.availableOutgoingBitrate || 0,
            availableIncomingBitrate: report.availableIncomingBitrate || 0,
            bytesSent: report.bytesSent || 0,
            bytesReceived: report.bytesReceived || 0,
          };
        }
      });

      // Store for next sample's bitrate calculation
      this.lastInboundStats = {
        video: result.video ? { bytesReceived: result.video.bytesReceived } : undefined,
        audio: result.audio ? { bytesReceived: result.audio.bytesReceived } : undefined,
        timestamp: now,
      };

      return result;
    } catch {
      return undefined;
    }
  }

  async getLatency(): Promise<
    { estimatedMs: number; jitterBufferMs: number; rttMs: number } | undefined
  > {
    const s = await this.getStats();
    if (!s) return undefined;

    return {
      estimatedMs: s.video?.jitterBufferDelay || 0,
      jitterBufferMs: s.video?.jitterBufferDelay || 0,
      rttMs: s.network?.rtt || 0,
    };
  }

  private async startWhep(
    video: HTMLVideoElement,
    url: string,
    headers: Record<string, string>,
    iceServers: RTCIceServer[]
  ) {
    // Clean previous sessionUrl
    if (this.sessionUrl) {
      try {
        fetch(this.sessionUrl, { method: "DELETE" }).catch(() => {});
      } catch {}
      this.sessionUrl = null;
    }

    // Create peer connection
    const pc = new RTCPeerConnection({ iceServers });
    this.peerConnection = pc;

    pc.ontrack = (event: RTCTrackEvent) => {
      if (this.destroyed) return; // Guard against zombie callbacks
      if (video && event.streams[0]) {
        video.srcObject = event.streams[0];
      }
    };

    pc.oniceconnectionstatechange = () => {
      if (this.destroyed) return; // Guard against zombie callbacks
      const state = pc.iceConnectionState;
      if (state === "failed" || state === "disconnected") {
        this.emit("error", "WHEP connection failed");
        if (
          this.reconnectEnabled &&
          this.reconnectAttempts < this.maxReconnectAttempts &&
          this.currentWhepUrl
        ) {
          const backoff = Math.min(5000, 500 * Math.pow(2, this.reconnectAttempts));
          this.reconnectAttempts++;
          this.reconnectTimer = setTimeout(() => {
            if (this.destroyed) return; // Guard inside timer callback too
            this.startWhep(
              video,
              this.currentWhepUrl!,
              this.currentHeaders || {},
              this.currentIceServers || []
            );
          }, backoff);
        }
      }
      if (state === "connected") {
        this.reconnectAttempts = 0;
      }
    };

    pc.addTransceiver("video", { direction: "recvonly" });
    pc.addTransceiver("audio", { direction: "recvonly" });

    const offer = await pc.createOffer();
    await pc.setLocalDescription(offer);

    const requestHeaders: Record<string, string> = { "Content-Type": "application/sdp" };
    for (const k in headers) requestHeaders[k] = headers[k];

    const response = await fetch(url, {
      method: "POST",
      headers: requestHeaders,
      body: offer.sdp || "",
    });
    if (!response.ok) {
      throw new Error(`WHEP request failed: ${response.status}`);
    }
    const answerSdp = await response.text();
    await pc.setRemoteDescription(new RTCSessionDescription({ type: "answer", sdp: answerSdp }));

    // Resolve sessionUrl against the WHEP endpoint URL (Location header may be relative)
    const locationHeader = response.headers.get("Location");
    if (locationHeader) {
      try {
        // Use URL constructor to resolve relative path against the WHEP endpoint
        this.sessionUrl = new URL(locationHeader, url).href;
      } catch {
        this.sessionUrl = locationHeader;
      }
    } else {
      this.sessionUrl = null;
    }
  }
}

// Backwards compatibility alias
export { NativePlayerImpl as DirectPlaybackPlayerImpl };

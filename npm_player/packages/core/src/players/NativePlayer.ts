import { BasePlayer } from "../core/PlayerInterface";
import { isLiveStreamType } from "../core/PlayerInterface";
import {
  decideKeepAwayRate,
  keepAwareTargetMs,
  KEEP_AWAY_DEFAULTS,
} from "../core/delivery/keep-away-rate-controller";
import { MistControlChannel } from "../core/MistControlChannel";
import { buildQualityLevelsFromStreamTracks } from "../core/QualityLevels";
import { translateCodec } from "../core/CodecUtils";
import { normalizeLiveCatchupConfig } from "../core/delivery/live-catchup";
import { decideDeadPointRecovery } from "../core/mist/dead-point-recovery";
import { LiveEdgeRateController } from "../core/mist/live-edge-rate-controller";
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
 * - Single-timebase live seek bar + keep-away playback-rate control (BasePlayer)
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
  private incomingMediaStream: MediaStream | null = null;
  private lastInboundStats: any = null;
  private reconnectEnabled = false;
  private reconnectAttempts = 0;
  private maxReconnectAttempts = 3;
  private reconnectTimer: any = null;
  private controlOpenTimer: ReturnType<typeof setTimeout> | null = null;
  private currentWhepUrl: string | null = null;
  private currentHeaders: Record<string, string> | null = null;
  private currentIceServers: RTCIceServer[] | null = null;
  private container: HTMLElement | null = null;
  private destroyed = false;

  // MistControl data channel for WHEP seeking
  private controlChannel: MistControlChannel | null = null;
  private whepLiveEdge: LiveEdgeRateController | null = null;
  private whepSeekOffset = 0;
  private whepDurationMs = 0;
  private whepIsLive = true;
  private whepBufferWindow = 0;
  private whepBeginMs = 0;
  private whepEndMs = 0;
  private whepPlayRate: number | "auto" | "fast-forward" = "auto";
  private whepPlayRequested = false;
  private whepHoldRequested = false;
  private currentOptions: PlayerOptions | null = null;
  private streamInfoRef: StreamInfo | null = null;
  private selectedTrack = "auto";

  // Read-ahead (ms) below which we must not speed up (protect the buffer), and below
  // which we actively slow down to rebuild. Decouples the low-latency goal (latency
  // control) from the don't-stall guarantee (real buffer floor).
  private static readonly READ_AHEAD_SAFE_MS = 1500;
  private static readonly READ_AHEAD_CRITICAL_MS = 750;

  // Reference html5.js features
  private pausedAt: number | null = null;
  // Target latency behind the live edge for progressive live (keep-away setpoint).
  // Live target = how much read-ahead (≈ latency behind live) to hold. Derived from
  // MistServer's reported keepaway; refreshed via setLiveKeepAwayMs as jitter changes.
  private targetLatencyMs = KEEP_AWAY_DEFAULTS.targetLatencyMs;
  private currentSourceUrl: string | null = null;
  private currentMimeType: string | null = null;
  private sourceElement: HTMLSourceElement | null = null; // legacy, always null now
  private isMP3Source = false;

  // Auto-recovery threshold for live playback that stalls in a paused state.
  private static readonly PAUSE_RECOVERY_THRESHOLD = 5000;

  isMimeSupported(mimetype: string): boolean {
    return this.capability.mimes.indexOf(mimetype) !== -1;
  }

  isBrowserSupported(
    mimetype: string,
    source: StreamSource,
    streamInfo: StreamInfo
  ): boolean | string[] {
    if (source.headers && mimetype !== "whep") {
      return false;
    }
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
        const codecString = translateCodec(track);
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
    this.currentOptions = options;
    this.streamInfoRef = streamInfo ?? null;
    // The initial startunix offset below uses the default target; the controller refines
    // targetLatencyMs to the jitter-aware keepaway via setLiveKeepAwayMs on the first hint.
    this.isMP3Source = source.type === "html5/audio/mp3";
    this.whepPlayRequested = false;
    this.whepHoldRequested = false;
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

    // Live edge is handled by the single-timebase model in BasePlayer (seek/duration
    // anchored to MistServer stream time). WHEP uses Mist control signaling instead.
    const isLiveStream = isLiveStreamType(streamInfo?.type);
    if (source.type !== "whep" && isLiveStream) {
      // Upstream html5.js:158-160: force loop=false for live
      video.loop = false;
      this.setupAutoRecovery(video);
      // startunix URL rewriting only works for progressive formats (MP4/MPEG-TS/WebM).
      // For HLS, the browser's native HLS stack handles DVR seeking via the playlist —
      // startunix rewrites cause 404s ("Fragment out of range").
      const isHLS = source.type?.includes("mpegurl");
      if (!isHLS) {
        this.initLiveSeek(source.url);
        // Start a chosen margin behind the live edge (startunix) so Mist bursts a
        // backlog and the browser builds read-ahead, instead of starving at the
        // realtime-delivered edge. The keep-away controller holds it there.
        const offsetSec = -this.targetLatencyMs / 1000;
        const initialUrl = this.buildLiveSeekUrl(offsetSec);
        if (initialUrl) {
          this.currentSourceUrl = initialUrl;
          this.liveSeekOffsetSec = offsetSec;
        }
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
        this.setupVideoEventListeners(video, options);
        return video;
      } else {
        this.setupVideoEventListeners(video, options);
        // currentSourceUrl carries the initial startunix offset for progressive live.
        video.src = this.currentSourceUrl ?? source.url;
        return video;
      }
    } catch (error: any) {
      if (source.type === "whep") {
        this.cleanupFailedWhepStartup(video);
      }
      this.emit("error", error.message || String(error));
      throw error;
    }
  }

  /** Keep-away setpoint: how far behind the live edge we hold progressive live. */
  protected getTargetLatencyMs(): number {
    return this.targetLatencyMs;
  }

  /**
   * Refresh the live target from MistServer's reported keepaway as it changes
   * (controller pushes per stream-state update). Keeps the target jitter-aware.
   */
  setLiveKeepAwayMs(keepAwayMs: number): void {
    this.targetLatencyMs = keepAwareTargetMs(keepAwayMs);
  }

  /**
   * On each authoritative live-edge update, nudge playbackRate (±1%, imperceptible) to
   * hold a target LATENCY behind the live edge — the catch-up MistServer's embed leaves
   * as a TODO. WHEP never reaches here (it doesn't enable live seek).
   *
   * Two signals, two jobs:
   *  - Latency (anchor-derived `getLiveLatencyMs`) is the control target — it's the thing
   *    we want low and is independent of how much the browser happens to buffer ahead.
   *  - Read-ahead (`buffered.end − currentTime`) is the MEASURED safety floor: never speed
   *    up into a thin buffer, and slow down if it gets critically low. This is what keeps
   *    the latency controller from ever catching up into a stall.
   */
  protected onLiveEdgeUpdated(): void {
    const video = this.videoElement;
    if (!video || video.paused || this.currentMimeType === "whep") return;
    const buffered = video.buffered;
    if (!buffered || buffered.length === 0) return;

    const readAheadMs = Math.max(0, (buffered.end(buffered.length - 1) - video.currentTime) * 1000);
    const latencyMs = this.getLiveLatencyMs();
    const decision = decideKeepAwayRate(
      { currentLatencyMs: latencyMs, currentRate: video.playbackRate },
      { ...KEEP_AWAY_DEFAULTS, targetLatencyMs: this.targetLatencyMs }
    );
    let rate = decision.kind === "set_rate" ? decision.rate : video.playbackRate;

    // Buffer safety overrides the latency target: protect against starvation.
    if (readAheadMs < NativePlayerImpl.READ_AHEAD_CRITICAL_MS) {
      rate = Math.min(rate, KEEP_AWAY_DEFAULTS.rebuildRate); // force slow-down to rebuild
    } else if (rate > 1 && readAheadMs < NativePlayerImpl.READ_AHEAD_SAFE_MS) {
      rate = 1.0; // don't catch up (speed up) into a thin buffer
    }

    if (Math.abs(rate - video.playbackRate) > 0.0001) {
      video.playbackRate = rate;
    }
  }

  private cleanupFailedWhepStartup(video: HTMLVideoElement): void {
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
    if (this.controlOpenTimer) {
      clearTimeout(this.controlOpenTimer);
      this.controlOpenTimer = null;
    }
    if (this.controlChannel) {
      try {
        this.controlChannel.close();
      } catch {}
      this.controlChannel = null;
      this.whepLiveEdge = null;
    }
    if (this.sessionUrl) {
      const url = this.sessionUrl;
      this.sessionUrl = null;
      fetch(url, { method: "DELETE" }).catch(() => {});
    }
    if (this.peerConnection) {
      try {
        this.peerConnection.close();
      } catch {}
      this.peerConnection = null;
    }
    try {
      (video as any).srcObject = null;
    } catch {}
    this.incomingMediaStream = null;
  }

  /**
   * Setup auto-recovery on long pause
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
   * Override seek for MP3 files (seeking not supported) and WHEP data channel seeking.
   */
  seek(timeMs: number): void {
    if (this.isMP3Source) return;
    // WHEP: seek via MistControl data channel
    if (this.controlChannel?.isOpen && this.videoElement) {
      this.videoElement.pause();
      this.whepSeekOffset = timeMs / 1000 - this.videoElement.currentTime;
      this.controlChannel.seek(timeMs);
      return;
    }
    super.seek(timeMs);
  }

  async play(): Promise<void> {
    if (this.currentMimeType === "whep") {
      this.whepPlayRequested = true;
      this.whepHoldRequested = false;
      this.controlChannel?.play();
    }
    return super.play();
  }

  pause(): void {
    super.pause();
    if (this.currentMimeType === "whep") {
      this.whepHoldRequested = true;
      this.whepPlayRequested = false;
      this.controlChannel?.hold();
    }
  }

  jumpToLive(): void {
    if (this.controlChannel?.isOpen && this.videoElement) {
      this.videoElement.pause();
      this.whepSeekOffset = 0;
      this.controlChannel.seek("live");
      return;
    }
    super.jumpToLive();
  }

  canSeek(): boolean {
    const v = this.videoElement;
    if (!v) return false;
    // WHEP seeking is only reliable when MistControl channel is actually open.
    if (this.controlChannel) {
      return this.controlChannel.isOpen;
    }
    // MediaStream sources without data channel can't seek
    if (v.srcObject instanceof MediaStream) return false;
    return true;
  }

  getQualities(): Array<{ id: string; label: string; isAuto?: boolean; active?: boolean }> {
    if (this.currentMimeType !== "whep") return [];
    const qualities: Array<{ id: string; label: string; isAuto?: boolean; active?: boolean }> = [
      { id: "auto", label: "Auto", isAuto: true, active: this.selectedTrack === "auto" },
    ];
    for (const level of buildQualityLevelsFromStreamTracks(this.streamInfoRef?.meta?.tracks)) {
      qualities.push({ ...level, isAuto: false, active: this.selectedTrack === level.id });
    }
    return qualities;
  }

  selectQuality(id: string): void {
    if (this.currentMimeType !== "whep" || !this.controlChannel) return;
    if (id === "auto") {
      this.controlChannel.setTracks({});
      this.selectedTrack = "auto";
      return;
    }
    this.controlChannel.setTracks({ video: id });
    this.selectedTrack = id;
  }

  getCurrentTime(): number {
    if (this.controlChannel?.isOpen && this.videoElement) {
      return (this.whepSeekOffset + this.videoElement.currentTime) * 1000;
    }
    return super.getCurrentTime();
  }

  getDuration(): number {
    if (this.controlChannel?.isOpen && this.whepDurationMs > 0) {
      if (!Number.isFinite(this.whepDurationMs)) return this.whepDurationMs;
      return this.whepDurationMs;
    }
    return super.getDuration();
  }

  getSeekableRange(): { start: number; end: number } | null {
    if (this.controlChannel?.isOpen && this.whepBufferWindow > 0) {
      return { start: this.whepBeginMs, end: this.whepEndMs };
    }
    return super.getSeekableRange();
  }

  getBufferWindow(): number {
    return this.whepBufferWindow;
  }

  getLiveLatency(): number {
    return this.getLiveLatencyMs();
  }

  protected reloadSource(url: string): void {
    if (!this.videoElement) return;
    if (url === this.currentSourceUrl) return;
    const wasPlaying = !this.videoElement.paused;
    this.currentSourceUrl = url;
    this.videoElement.src = url;
    this.videoElement.load();
    // New connection → element timeline resets; re-capture the anchor on the next hint.
    this._hasConnAnchor = false;
    if (wasPlaying) this.videoElement.play().catch(() => {});
  }

  async destroy(): Promise<void> {
    // Set destroyed flag immediately to guard against async callbacks
    this.destroyed = true;

    if (this.reconnectTimer) {
      try {
        clearTimeout(this.reconnectTimer);
      } catch {}
      this.reconnectTimer = null;
    }
    if (this.controlOpenTimer) {
      try {
        clearTimeout(this.controlOpenTimer);
      } catch {}
      this.controlOpenTimer = null;
    }

    // Clean up MistControl data channel
    if (this.controlChannel) {
      try {
        this.controlChannel.close();
      } catch {}
      this.controlChannel = null;
      this.whepLiveEdge = null;
    }
    this.whepSeekOffset = 0;
    this.whepDurationMs = 0;
    this.whepBufferWindow = 0;
    this.whepBeginMs = 0;
    this.whepEndMs = 0;
    this.whepPlayRequested = false;
    this.whepHoldRequested = false;

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
    this.incomingMediaStream = null;
    this.sourceElement = null;
    this.container = null;
    this.pausedAt = null;
    this.currentSourceUrl = null;
    this.currentMimeType = null;
    this.streamInfoRef = null;
    this.selectedTrack = "auto";
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

  /**
   * Set up MistControl data channel event handlers for WHEP seeking.
   */
  private setupMistControl(control: MistControlChannel, video: HTMLVideoElement): void {
    this.whepLiveEdge = new LiveEdgeRateController({
      transport: control.transport,
      config: normalizeLiveCatchupConfig(this.currentOptions?.liveCatchup, {
        undefinedMeans: "off",
      }),
      isLive: () => this.whepIsLive,
    });

    control.on("open", () => {
      if (this.destroyed) return;
      if (this.controlOpenTimer) {
        clearTimeout(this.controlOpenTimer);
        this.controlOpenTimer = null;
      }
      if (this.whepPlayRequested && !video.paused) {
        control.play();
      } else if (this.whepHoldRequested || video.paused) {
        control.hold();
      }
      this.emit("seekablechange", {
        start: this.whepBeginMs,
        end: this.whepEndMs,
        bufferWindow: this.whepBufferWindow,
      });
    });

    control.on("close", () => {
      if (this.destroyed) return;
      this.emit("seekablechange", {
        start: this.whepBeginMs,
        end: this.whepEndMs,
        bufferWindow: this.whepBufferWindow,
      });
    });

    control.on("time_update", (update) => {
      if (this.destroyed) return;
      this.whepSeekOffset = update.current / 1000 - video.currentTime;
      this.whepDurationMs = update.end === 0 ? Infinity : update.end;
      this.whepIsLive = !isFinite(this.whepDurationMs) || this.whepDurationMs === 0;
      this.whepBufferWindow = update.end - update.begin;
      this.whepBeginMs = update.begin;
      this.whepEndMs = update.end === 0 ? update.current : update.end;
      this.whepLiveEdge?.ingestOnTime(update);
      this.emit("seekablechange", {
        start: update.begin,
        end: this.whepEndMs,
        bufferWindow: this.whepBufferWindow,
      });
      if (!update.paused && video.paused) {
        video.play().catch(() => {});
      }
    });

    control.on("seeked", ({ live_point }) => {
      if (this.destroyed) return;
      video.dispatchEvent(
        new CustomEvent("seeked", { detail: { seekOffset: this.whepSeekOffset } })
      );
      if (live_point) {
        control.setSpeed("auto");
      }
      video.play().catch(() => {});
    });

    control.on("speed_changed", ({ play_rate_curr }) => {
      if (this.destroyed) return;
      this.whepPlayRate = play_rate_curr;
    });

    // Handle at_dead_point recovery near the start of the available buffer.
    control.on("pause", (msg) => {
      if (this.destroyed) return;
      const recovery = decideDeadPointRecovery(msg, this.whepPlayRate);
      if (recovery.kind === "seek_recover") {
        if (recovery.resetSpeedToAuto) {
          control.setSpeed("auto");
        }
        control.seek(recovery.seekToMs);
        return;
      }
      if (msg.paused) video.pause();
    });

    control.on("stopped", () => {
      if (this.destroyed) return;
      this.whepIsLive = false;
      video.pause();
      this.emit("ended", undefined);
    });

    control.on("control_error", ({ message }) => {
      if (this.destroyed) return;
      this.emit("error", message);
    });
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
    this.incomingMediaStream = null;
    let mediaReadySettled = false;
    let mediaReadyTimer: ReturnType<typeof setTimeout> | null = null;
    let resolveMediaReady: () => void = () => {};
    let rejectMediaReady: (error: Error) => void = () => {};
    const finishMediaReady = () => {
      if (mediaReadySettled) return;
      mediaReadySettled = true;
      if (mediaReadyTimer !== null) clearTimeout(mediaReadyTimer);
      resolveMediaReady();
    };
    const failMediaReady = (error: Error) => {
      if (mediaReadySettled) return;
      mediaReadySettled = true;
      if (mediaReadyTimer !== null) clearTimeout(mediaReadyTimer);
      rejectMediaReady(error);
    };
    const cancelMediaReady = () => {
      if (mediaReadySettled) return;
      mediaReadySettled = true;
      if (mediaReadyTimer !== null) clearTimeout(mediaReadyTimer);
    };
    const mediaReady = new Promise<void>((resolve, reject) => {
      resolveMediaReady = resolve;
      rejectMediaReady = reject;
    });
    mediaReadyTimer = setTimeout(() => {
      failMediaReady(new Error("WHEP media did not arrive"));
    }, 10000);

    // Create MistControl data channel for seeking/DVR.
    // Must be created before createOffer() so it's included in the SDP exchange.
    const mistControlDC = pc.createDataChannel("MistControl");
    this.controlChannel = new MistControlChannel(mistControlDC);
    this.setupMistControl(this.controlChannel, video);

    pc.ontrack = (event: RTCTrackEvent) => {
      if (this.destroyed) return; // Guard against zombie callbacks
      if (!video) return;
      if (!this.incomingMediaStream) {
        this.incomingMediaStream = new MediaStream();
        video.srcObject = this.incomingMediaStream;
      }
      const aggregate = this.incomingMediaStream;
      const incomingTracks =
        event.streams && event.streams.length > 0
          ? event.streams.flatMap((s) => s.getTracks())
          : [event.track];
      for (const track of incomingTracks) {
        if (!aggregate.getTracks().some((t) => t.id === track.id)) {
          aggregate.addTrack(track);
        }
      }
      finishMediaReady();
    };

    pc.oniceconnectionstatechange = () => {
      if (this.destroyed) return; // Guard against zombie callbacks
      const state = pc.iceConnectionState;
      if (state === "failed" || state === "disconnected") {
        this.emit("error", "WHEP connection failed");
        failMediaReady(new Error("WHEP connection failed before media arrived"));
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

    let response: Response | null = null;
    try {
      const offer = await pc.createOffer();
      await pc.setLocalDescription(offer);

      const requestHeaders: Record<string, string> = { "Content-Type": "application/sdp" };
      for (const k in headers) requestHeaders[k] = headers[k];

      response = await fetch(url, {
        method: "POST",
        headers: requestHeaders,
        body: offer.sdp || "",
      });
      if (!response.ok) {
        throw new Error(`WHEP request failed: ${response.status}`);
      }
      const answerSdp = await response.text();
      await pc.setRemoteDescription(new RTCSessionDescription({ type: "answer", sdp: answerSdp }));
      const locationHeader = response.headers.get("Location");
      if (locationHeader) {
        try {
          this.sessionUrl = new URL(locationHeader, url).href;
        } catch {
          this.sessionUrl = locationHeader;
        }
      } else {
        this.sessionUrl = null;
      }
      await mediaReady;
    } catch (error) {
      cancelMediaReady();
      throw error;
    }

    // WHEP can carry media without negotiating SCTP/DataChannel.
    // When SCTP is absent, MistControl will never open and seeking must stay disabled.
    if (!pc.sctp) {
      console.warn("[NativePlayer] WHEP negotiated without SCTP; continuing without seek/control");
      this.controlChannel = null;
      this.whepLiveEdge = null;
    } else if (this.controlChannel && !this.controlChannel.isOpen) {
      if (this.controlOpenTimer) clearTimeout(this.controlOpenTimer);
      this.controlOpenTimer = setTimeout(() => {
        if (!this.destroyed && this.controlChannel && !this.controlChannel.isOpen) {
          console.warn(
            "[NativePlayer] WHEP MistControl datachannel did not open; seeking disabled"
          );
        }
      }, 5000);
    }
  }
}

// Backwards compatibility alias
export { NativePlayerImpl as DirectPlaybackPlayerImpl };

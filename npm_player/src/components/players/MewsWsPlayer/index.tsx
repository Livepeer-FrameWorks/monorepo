/**
 * MEWS WebSocket Player
 *
 * Low-latency WebSocket MP4 streaming using MediaSource Extensions.
 * Protocol: Custom MEWS (MistServer Extended WebSocket)
 */

import React, { useEffect, useRef } from 'react';
import { BasePlayer, StreamSource, StreamInfo, PlayerOptions, PlayerCapability } from '../../../core/PlayerInterface';
import { WebSocketManager } from './WebSocketManager';
import { SourceBufferManager } from './SourceBufferManager';
import type { MewsMessage, AnalyticsConfig } from './types';

type Props = {
  wsUrl: string;
  muted?: boolean;
  autoPlay?: boolean;
  controls?: boolean;
  onError?: (e: Error) => void;
};

export class MewsWsPlayerImpl extends BasePlayer {
  readonly capability: PlayerCapability = {
    name: "MEWS WebSocket Player",
    shortname: "mews",
    priority: 6,
    mimes: ["ws/video/mp4", "ws/video/webm"]
  };

  private wsManager: WebSocketManager | null = null;
  private sbManager: SourceBufferManager | null = null;
  private mediaSource: MediaSource | null = null;
  private objectUrl: string | null = null;
  private container: HTMLElement | null = null;
  private lastDuration = Infinity;
  private pausedByHold = false;
  private isDestroyed = false;

  // Server delay estimation
  private serverDelaySamples: number[] = [];
  private pendingDelayTypes: Record<string, number> = {};

  // Seek state
  private pendingSeekMs: number | null = null;
  private seekRetries = 0;

  // ABR state
  private bitCounter: number[] = [];
  private bitsSince: number[] = [];
  private currentBps: number | null = null;
  private waitingCount = 0;
  private abrEnabled = true;

  // VoD vs Live state
  private isLiveStream = true; // Assume live until proven otherwise
  private lastEnd: number | null = null;

  // Jitter tracking for catch-up (3.3.9)
  private jitterSamples: number[] = [];

  // Browser pause detection (3.3.10)
  private lastOnTimeMs = 0;
  private visibilityHandler: (() => void) | null = null;

  // Subtitle state (3.3.11)
  private currentSubtitleTrack: number | null = null;

  // Analytics
  private analyticsConfig: AnalyticsConfig = { enabled: false, endpoint: null };
  private analyticsTimer: ReturnType<typeof setInterval> | null = null;

  isMimeSupported(mimetype: string): boolean {
    return this.capability.mimes.includes(mimetype);
  }

  isBrowserSupported(mimetype: string, source: StreamSource, streamInfo: StreamInfo): boolean | string[] {
    try {
      const extras = source as any;
      if (extras?.disableOnMacOS) {
        const isMac = navigator.platform.toUpperCase().indexOf('MAC') >= 0;
        if (isMac) return false;
      }
    } catch {}

    if (!window.MediaSource || !window.WebSocket) return false;

    const mp4Ok = MediaSource.isTypeSupported('video/mp4; codecs="avc1.42E01E, mp4a.40.2"');
    const webmOk = MediaSource.isTypeSupported('video/webm; codecs="vp9, opus"') ||
                   MediaSource.isTypeSupported('video/webm; codecs="vp8, vorbis"');

    if (!mp4Ok && !webmOk) return false;
    return ['video', 'audio'];
  }

  async initialize(container: HTMLElement, source: StreamSource, options: PlayerOptions): Promise<HTMLVideoElement> {
    this.container = container;
    container.classList.add('fw-player-container');

    const video = document.createElement('video');
    video.classList.add('fw-player-video');
    video.setAttribute('playsinline', '');
    video.setAttribute('crossorigin', 'anonymous');

    if (options.autoplay) video.autoplay = true;
    if (options.muted) video.muted = true;
    if (options.controls) video.controls = true;
    if (options.loop) video.loop = true;
    if (options.poster) video.poster = options.poster;

    this.videoElement = video;
    container.appendChild(video);
    this.setupVideoEventListeners(video, options);

    // Analytics configuration
    const anyOpts = options as any;
    this.analyticsConfig = {
      enabled: !!anyOpts.analytics?.enabled,
      endpoint: anyOpts.analytics?.endpoint || null
    };

    try {
      this.mediaSource = new MediaSource();
      this.mediaSource.addEventListener('sourceopen', () => this.handleSourceOpen(source));
      this.objectUrl = URL.createObjectURL(this.mediaSource);
      video.src = this.objectUrl;
      this.isDestroyed = false;
      this.startTelemetry();
      return video;
    } catch (error: any) {
      this.emit('error', error.message || String(error));
      throw error;
    }
  }

  destroy(): void {
    this.isDestroyed = true;

    if (this.analyticsTimer) {
      clearInterval(this.analyticsTimer);
      this.analyticsTimer = null;
    }

    // Clean up visibility handler (3.3.10)
    if (this.visibilityHandler) {
      document.removeEventListener('visibilitychange', this.visibilityHandler);
      this.visibilityHandler = null;
    }

    this.wsManager?.destroy();
    this.wsManager = null;

    this.sbManager?.destroy();
    this.sbManager = null;

    if (this.mediaSource?.readyState === 'open') {
      try {
        this.mediaSource.endOfStream();
      } catch {}
    }

    if (this.objectUrl) {
      URL.revokeObjectURL(this.objectUrl);
      this.objectUrl = null;
    }

    if (this.videoElement && this.container) {
      this.container.removeChild(this.videoElement);
    }

    this.videoElement = null;
    this.container = null;
    this.mediaSource = null;
    this.listeners.clear();
  }

  /**
   * Play with optional skip to live edge (3.3.7).
   * For live streams, if at position 0, seeks to live edge.
   */
  async play(): Promise<void> {
    const v = this.videoElement;
    const skipToLive = this.isLiveStream && v && v.currentTime === 0;

    if (skipToLive) {
      this.wsManager?.send({ type: 'play', seek_time: 'live' });
    } else {
      this.wsManager?.send({ type: 'play' });
    }

    this.pausedByHold = false;
    await v?.play();
  }

  pause(): void {
    this.videoElement?.pause();
    this.wsManager?.send({ type: 'hold' });
    this.pausedByHold = true;
  }

  seek(time: number): void {
    if (!this.videoElement) return;
    const seekMs = Math.max(0, Math.round(time * 1000 - (250 + this.getServerDelay())));
    this.pendingSeekMs = seekMs;
    this.wsManager?.send({ type: 'seek', seek_time: seekMs });
  }

  getCurrentTime(): number {
    return this.videoElement?.currentTime ?? 0;
  }

  getDuration(): number {
    return isFinite(this.lastDuration) ? this.lastDuration : super.getDuration();
  }

  setPlaybackRate(rate: number): void {
    super.setPlaybackRate(rate);
    const play_rate = rate === 1 ? 'auto' : rate;
    this.wsManager?.send({ type: 'set_speed', play_rate });
  }

  getQualities(): Array<{ id: string; label: string; isAuto?: boolean; active?: boolean }> {
    return [{ id: 'auto', label: 'Auto', isAuto: true, active: true }];
  }

  selectQuality(id: string): void {
    if (id === 'auto') {
      this.wsManager?.send({ type: 'set_speed', play_rate: 'auto' });
    }
  }

  /**
   * Select a subtitle track (3.3.11).
   * Sends track selection to server via 'tracks' command.
   * @param id - Track id (string index), or null to disable subtitles
   */
  selectTextTrack(id: string | null): void {
    if (id === null) {
      this.currentSubtitleTrack = null;
      this.wsManager?.send({ type: 'tracks', subtitle: 'none' });
    } else {
      this.currentSubtitleTrack = parseInt(id, 10) || 0;
      this.wsManager?.send({ type: 'tracks', subtitle: id });
    }
  }

  /**
   * Check if this is a live stream (3.3.8).
   */
  isLive(): boolean {
    return this.isLiveStream;
  }

  async getStats(): Promise<any> {
    return {
      currentBps: this.currentBps,
      waitingEvents: this.waitingCount,
      isLive: this.isLiveStream,
      serverDelay: this.getServerDelay()
    };
  }

  // Private methods

  private handleSourceOpen(source: StreamSource): void {
    if (!this.mediaSource || !this.videoElement) return;

    this.sbManager = new SourceBufferManager({
      mediaSource: this.mediaSource,
      videoElement: this.videoElement,
      onError: (msg) => this.emit('error', msg)
    });

    this.installWaitingHandler();
    this.installVisibilityHandler(); // 3.3.10
    this.installLoopHandler(); // 3.3.12

    this.wsManager = new WebSocketManager({
      url: source.url,
      maxReconnectAttempts: 5,
      onMessage: (data) => this.handleMessage(data),
      onOpen: () => this.requestCodecData(),
      onClose: () => {},
      onError: (msg) => this.emit('error', msg)
    });

    this.wsManager.connect();
  }

  private handleMessage(data: ArrayBuffer | string): void {
    if (typeof data === 'string') {
      try {
        const msg = JSON.parse(data) as MewsMessage;
        this.handleControlMessage(msg);
      } catch {}
      return;
    }

    const bytes = new Uint8Array(data);
    this.sbManager?.append(bytes);
    this.trackBits(data);
  }

  private handleControlMessage(msg: MewsMessage): void {
    switch (msg.type) {
      case 'codec_data': {
        const codecs: string[] = msg.data?.codecs || [];
        this.sbManager?.initWithCodecs(codecs);
        this.resolveDelay('codec_data');
        break;
      }

      case 'on_time': {
        const currentMs = msg.data?.current;
        const end = msg.data?.end;

        // Track last on_time for browser pause detection (3.3.10)
        this.lastOnTimeMs = Date.now();

        if (typeof end === 'number') {
          this.lastDuration = end / 1000;

          // VoD vs Live detection (3.3.8)
          // If end keeps changing, it's likely live. If stable, it's VoD.
          if (this.lastEnd !== null && Math.abs(this.lastEnd - end) < 100) {
            // End stable for this message - might be VoD
            // Need multiple stable readings to confirm
          } else if (this.lastEnd !== null && end > this.lastEnd) {
            // End is growing - definitely live
            this.isLiveStream = true;
          }
          this.lastEnd = end;
        }

        // Jitter-aware catch-up tuning (3.3.9)
        if (this.videoElement && typeof currentMs === 'number') {
          const buffer = currentMs - this.videoElement.currentTime * 1000;

          // Track jitter
          if (this.jitterSamples.length > 0) {
            const lastBuffer = this.jitterSamples[this.jitterSamples.length - 1];
            const jitter = Math.abs(buffer - lastBuffer);
            this.jitterSamples.push(jitter);
            if (this.jitterSamples.length > 10) {
              this.jitterSamples.shift();
            }
          } else {
            this.jitterSamples.push(buffer);
          }

          // Calculate desired buffer considering jitter
          const avgJitter = this.jitterSamples.length > 1
            ? this.jitterSamples.reduce((a, b) => a + b, 0) / this.jitterSamples.length
            : 0;
          const desired = 2 * this.getServerDelay() + avgJitter;

          this.tunePlaybackRate(buffer, desired);
        }

        // Seek acknowledgment with retry logic (3.3.4)
        if (this.pendingSeekMs !== null && typeof currentMs === 'number' && currentMs >= this.pendingSeekMs) {
          const tSec = currentMs / 1000;
          const i = this.sbManager?.findBufferIndex(tSec);
          if (i !== false && this.videoElement) {
            this.trySetCurrentTime(tSec);
          }
        }
        break;
      }

      case 'on_stop': {
        // Confirmed VoD (stream ended)
        this.isLiveStream = false;

        if (this.mediaSource?.readyState === 'open') {
          try {
            this.mediaSource.endOfStream();
          } catch {}
        }
        break;
      }

      case 'tracks': {
        const codecs: string[] = msg.data?.codecs || [];
        const switchPointMs = msg.data?.current;
        if (codecs.length) {
          this.sbManager?.changeCodecs(codecs, switchPointMs);
        }
        break;
      }

      case 'seek':
      case 'set_speed': {
        this.resolveDelay(msg.type);
        break;
      }
    }
  }

  /**
   * Try to set currentTime with retry logic (3.3.4).
   * Some browsers need multiple attempts for accurate seeking.
   */
  private trySetCurrentTime(tSec: number): void {
    const v = this.videoElement;
    if (!v) return;

    const MAX_RETRIES = 10;

    const attempt = () => {
      v.currentTime = tSec;
      // Check if seek succeeded (within 0.1s tolerance)
      if (Math.abs(v.currentTime - tSec) > 0.1 && this.seekRetries < MAX_RETRIES) {
        this.seekRetries++;
        this.sbManager?.scheduleAfterUpdate(attempt);
      } else {
        // Seek succeeded or max retries reached
        this.pendingSeekMs = null;
        this.seekRetries = 0;
      }
    };

    attempt();
  }

  private installWaitingHandler(): void {
    if (!this.videoElement) return;

    this.videoElement.addEventListener('waiting', () => {
      const v = this.videoElement!;
      if (!v.buffered || v.buffered.length === 0) return;

      // Jump to next buffer range if gap is small
      const i = this.sbManager?.findBufferIndex(v.currentTime);
      if (i !== false && typeof i === 'number' && i + 1 < v.buffered.length) {
        const nextStart = v.buffered.start(i + 1);
        if (nextStart - v.currentTime < 10) {
          v.currentTime = nextStart;
        }
      }

      // ABR trigger
      if (this.abrEnabled) {
        this.waitingCount++;
        if (this.waitingCount >= 3) {
          this.waitingCount = 0;
          if (this.currentBps) {
            this.requestLowerBitrate(this.currentBps);
          }
        }
      }
    });
  }

  private requestCodecData(): void {
    const supported: string[] = [];
    if (MediaSource.isTypeSupported('video/mp4; codecs="avc1.42E01E"')) {
      supported.push('avc1.42E01E');
    }
    if (MediaSource.isTypeSupported('audio/mp4; codecs="mp4a.40.2"')) {
      supported.push('mp4a.40.2');
    }

    this.logDelay('codec_data');
    this.wsManager?.send({ type: 'request_codec_data', supported_codecs: supported });
  }

  private requestLowerBitrate(currentBps: number): void {
    const hint = `<${currentBps}bps,minbps`;
    this.wsManager?.send({ type: 'tracks', video: hint });
  }

  private logDelay(type: string): void {
    this.pendingDelayTypes[type] = Date.now();
  }

  private resolveDelay(type: string): void {
    const start = this.pendingDelayTypes[type];
    if (start) {
      const dt = Date.now() - start;
      this.serverDelaySamples.unshift(dt);
      if (this.serverDelaySamples.length > 5) {
        this.serverDelaySamples.pop();
      }
      delete this.pendingDelayTypes[type];
    }
  }

  private getServerDelay(): number {
    if (!this.serverDelaySamples.length) return 500;
    const n = Math.min(3, this.serverDelaySamples.length);
    const sum = this.serverDelaySamples.slice(0, n).reduce((a, b) => a + b, 0);
    return Math.round(sum / n);
  }

  private tunePlaybackRate(bufferMs: number, desiredBufferMs: number): void {
    const v = this.videoElement;
    if (!v) return;

    if (bufferMs > desiredBufferMs * 2) {
      v.playbackRate = Math.min(2, v.playbackRate * 1.05);
    } else if (bufferMs < desiredBufferMs / 2) {
      v.playbackRate = Math.max(0.8, v.playbackRate * 0.95);
    } else if (Math.abs(v.playbackRate - 1) > 0.01) {
      v.playbackRate = 1;
    }
  }

  private trackBits(buf: ArrayBuffer): void {
    this.bitCounter.push(buf.byteLength * 8);
    this.bitsSince.push(Date.now());

    if (this.bitCounter.length > 10) {
      this.bitCounter.shift();
      this.bitsSince.shift();
    }

    const len = this.bitCounter.length;
    if (len >= 2) {
      const bits = this.bitCounter[len - 1] - this.bitCounter[0];
      const dt = (this.bitsSince[len - 1] - this.bitsSince[0]) / 1000;
      if (dt > 0) {
        this.currentBps = Math.max(1, Math.round(bits / dt));
      }
    }
  }

  /**
   * Browser pause detection (3.3.10).
   * When tab becomes hidden, browser may pause WebSocket/rendering.
   * On visibility restore, if we haven't received on_time for >2s,
   * skip to buffer position to resync.
   */
  private installVisibilityHandler(): void {
    this.visibilityHandler = () => {
      if (document.visibilityState === 'visible') {
        const elapsed = Date.now() - this.lastOnTimeMs;
        if (elapsed > 2000 && this.videoElement) {
          // Haven't received data in >2s, likely paused while hidden
          // Try to skip to available buffer position
          const v = this.videoElement;
          if (v.buffered.length > 0) {
            const lastEnd = v.buffered.end(v.buffered.length - 1);
            if (lastEnd > v.currentTime + 1) {
              // Skip forward to near end of buffer
              v.currentTime = Math.max(v.currentTime, lastEnd - 0.5);
            }
          }
        }
      }
    };
    document.addEventListener('visibilitychange', this.visibilityHandler);
  }

  /**
   * Loop handling (3.3.12).
   * When video ends, if loop is enabled, seek back to start.
   */
  private installLoopHandler(): void {
    if (!this.videoElement) return;

    this.videoElement.addEventListener('ended', () => {
      const v = this.videoElement;
      if (v?.loop && !this.isLiveStream) {
        // Seek back to start for VoD content
        this.seek(0);
        v.play().catch(() => {});
      }
    });
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
        waiting: stats.waitingEvents || 0
      };

      try {
        await fetch(endpoint, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(payload)
        });
      } catch {}
    }, 5000);
  }
}

// React component wrapper
const MewsWsPlayer: React.FC<Props> = ({
  wsUrl,
  muted = true,
  autoPlay = true,
  controls = true,
  onError
}) => {
  const videoRef = useRef<HTMLVideoElement>(null);
  const wsRef = useRef<WebSocket | null>(null);
  const msRef = useRef<MediaSource | null>(null);
  const sbRef = useRef<SourceBuffer | null>(null);

  useEffect(() => {
    const el = videoRef.current;
    if (!el) return;

    el.muted = muted;
    const ms = new MediaSource();
    msRef.current = ms;
    ms.addEventListener('sourceopen', () => {});
    const url = URL.createObjectURL(ms);
    el.src = url;

    return () => {
      try {
        wsRef.current?.close();
      } catch {}
      try {
        sbRef.current?.abort();
      } catch {}
      try {
        URL.revokeObjectURL(url);
      } catch {}
    };
  }, [wsUrl, muted, onError]);

  return (
    <video
      ref={videoRef}
      autoPlay={autoPlay}
      muted={muted}
      controls={controls}
      playsInline
      className="fw-player-video"
    />
  );
};

export default MewsWsPlayer;

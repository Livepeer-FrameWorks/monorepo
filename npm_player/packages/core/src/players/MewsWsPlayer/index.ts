/**
 * MEWS WebSocket Player Implementation
 *
 * Low-latency WebSocket MP4 streaming using MediaSource Extensions.
 * Protocol: Custom MEWS (MistServer Extended WebSocket)
 *
 * Ported from reference: mews.js (MistMetaPlayer)
 */

import { BasePlayer } from '../../core/PlayerInterface';
import type { StreamSource, StreamInfo, PlayerOptions, PlayerCapability } from '../../core/PlayerInterface';
import { WebSocketManager } from './WebSocketManager';
import { SourceBufferManager } from './SourceBufferManager';
import { translateCodec } from '../../core/CodecUtils';
import type { MewsMessage, AnalyticsConfig, OnTimeMessage, MewsMessageListener } from './types';

export class MewsWsPlayerImpl extends BasePlayer {
  readonly capability: PlayerCapability = {
    name: "MEWS WebSocket Player",
    shortname: "mews",
    priority: 2, // High priority - low latency protocol
    mimes: ["ws/video/mp4", "wss/video/mp4", "ws/video/webm", "wss/video/webm"]
  };

  private wsManager: WebSocketManager | null = null;
  private sbManager: SourceBufferManager | null = null;
  private mediaSource: MediaSource | null = null;
  private objectUrl: string | null = null;
  private container: HTMLElement | null = null;
  private isDestroyed = false;
  private debugging = false;

  // Server delay estimation (ported from mews.js:833-882)
  private serverDelays: number[] = [];
  private pendingDelayTypes: Record<string, number> = {};

  // Supported codecs (short names for MistServer protocol)
  private supportedCodecs: string[] = [];

  // Ready state - true after codec_data received and SourceBuffer initialized
  private isReady = false;
  private readyResolvers: Array<() => void> = [];

  // Duration tracking (ported from mews.js:1113)
  private lastDuration = Infinity;

  // Live vs VoD detection (ported from mews.js:105-107, 508)
  private streamType: 'live' | 'vod' | 'unknown' = 'unknown';

  // Current tracks for change detection (ported from mews.js:455, 593-619)
  private currentTracks: string[] = [];

  // Last codecs for track switch comparison (ported from mews.js:687)
  private lastCodecs: string[] | null = null;

  // Playback rate tuning (ported from mews.js:453, 509-545)
  private requestedRate = 1;

  // ABR state (ported from mews.js:1266-1314)
  private bitCounter: number[] = [];
  private bitsSince: number[] = [];
  private currentBps: number | null = null;
  private nWaiting = 0;
  private nWaitingThreshold = 3;

  // Seeking state (ported from mews.js:1169-1175)
  private seeking = false;

  // Analytics
  private analyticsConfig: AnalyticsConfig = { enabled: false, endpoint: null };
  private analyticsTimer: ReturnType<typeof setInterval> | null = null;

  isMimeSupported(mimetype: string): boolean {
    return this.capability.mimes.includes(mimetype);
  }

  isBrowserSupported(mimetype: string, source: StreamSource, streamInfo: StreamInfo): boolean | string[] {
    // Basic requirements check (mews.js:10)
    if (!('WebSocket' in window) || !('MediaSource' in window) || !('Promise' in window)) {
      return false;
    }

    // MacOS exemption (reference mews.js behavior)
    // MediaSource has bugs on Safari/MacOS - prefer HLS
    const isMac = /Mac OS X/.test(navigator.userAgent);
    if (isMac) {
      return false;
    }

    // Check codec compatibility using ACTUAL stream codecs (mews.js:45-83)
    const container = mimetype.split('/')[2] || 'mp4';
    const playableTracks: Record<string, number> = {};
    let hasSubtitles = false;

    // Test actual stream codecs against MediaSource
    this.supportedCodecs = [];
    for (const track of streamInfo.meta.tracks) {
      if (track.type === 'meta') {
        if (track.codec === 'subtitle') hasSubtitles = true;
        continue;
      }

      const codecString = translateCodec(track as any);
      const testMime = `video/${container};codecs="${codecString}"`;

      if (MediaSource.isTypeSupported(testMime)) {
        this.supportedCodecs.push(track.codec);
        playableTracks[track.type] = 1;
      }
    }

    // Check for subtitle source (mews.js:73-80)
    if (hasSubtitles) {
      const hasVttSource = streamInfo.source?.some(s => s.type === 'html5/text/vtt');
      if (hasVttSource) {
        playableTracks['subtitle'] = 1;
      }
    }

    if (Object.keys(playableTracks).length === 0) return false;
    return Object.keys(playableTracks);
  }

  async initialize(container: HTMLElement, source: StreamSource, options: PlayerOptions, streamInfo?: StreamInfo): Promise<HTMLVideoElement> {
    this.container = container;
    container.classList.add('fw-player-container');

    const video = document.createElement('video');
    video.classList.add('fw-player-video');
    video.setAttribute('playsinline', ''); // iphones (mews.js:92)
    video.setAttribute('crossorigin', 'anonymous'); // mews.js:111

    // Apply options (mews.js:95-110)
    if (options.autoplay) video.autoplay = true;
    if (options.muted) video.muted = true;
    video.controls = options.controls === true;
    if (options.loop) video.loop = true;
    if (options.poster) video.poster = options.poster;

    // Live streams don't loop (mews.js:105-107)
    if (this.streamType === 'live') {
      video.loop = false;
    }

    this.videoElement = video;
    container.appendChild(video);
    this.setupVideoEventListeners(video, options);

    // Analytics configuration
    const anyOpts = options as any;
    this.analyticsConfig = {
      enabled: !!anyOpts.analytics?.enabled,
      endpoint: anyOpts.analytics?.endpoint || null
    };

    // Get stream type from streamInfo if available
    // Note: source.type is a MIME string (e.g., 'ws/video/mp4'), not 'live'/'vod'
    if (streamInfo?.type === 'live') {
      this.streamType = 'live';
    } else if (streamInfo?.type === 'vod') {
      this.streamType = 'vod';
    }
    // Fallback: will be determined by server on_time messages (end === 0 means live)

    try {
      // Initialize MediaSource (mews.js:138-196)
      this.mediaSource = new MediaSource();

      // Set up MediaSource event handlers (mews.js:143-195)
      this.mediaSource.addEventListener('sourceopen', () => this.handleSourceOpen(source));
      this.mediaSource.addEventListener('sourceclose', () => this.handleSourceClose());
      this.mediaSource.addEventListener('sourceended', () => this.handleSourceEnded());

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

  /**
   * Handle MediaSource sourceopen event.
   * Ported from mews.js:143-148, 198-204, 885-902
   */
  private handleSourceOpen(source: StreamSource): void {
    if (!this.mediaSource || !this.videoElement) return;

    // Create SourceBufferManager
    this.sbManager = new SourceBufferManager({
      mediaSource: this.mediaSource,
      videoElement: this.videoElement,
      onError: (msg) => this.emit('error', msg)
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
      onError: (msg) => this.emit('error', msg)
    });

    this.wsManager.connect();
  }

  /**
   * Handle MediaSource sourceclose event.
   * Ported from mews.js:150-153
   */
  private handleSourceClose(): void {
    if (this.debugging) console.log('MEWS: MediaSource closed');
    this.send({ type: 'stop' });
  }

  /**
   * Handle MediaSource sourceended event.
   * Ported from mews.js:154-194
   */
  private handleSourceEnded(): void {
    if (this.debugging) console.log('MEWS: MediaSource ended');
    this.send({ type: 'stop' });
  }

  /**
   * Handle WebSocket open event.
   * Ported from mews.js:401-403, 885-902
   */
  private handleWsOpen(): void {
    // Request codec data (mews.js:885-902)
    const listener: MewsMessageListener = (msg) => {
      // Got codec data, set up source buffer
      if (this.mediaSource?.readyState === 'open') {
        const codecs = msg.data?.codecs || [];
        const initialized = this.sbManager?.initWithCodecs(codecs);

        if (initialized && !this.isReady) {
          this.isReady = true;
          // Resolve any waiting play() calls
          for (const resolve of this.readyResolvers) {
            resolve();
          }
          this.readyResolvers = [];
        }
      }
      this.wsManager?.removeListener('codec_data', listener);
    };

    this.wsManager?.addListener('codec_data', listener);
    this.logDelay('codec_data');

    // Send request with SHORT codec names (mews.js:901)
    // CRITICAL: MistServer expects short names like "H264", not browser codec strings
    this.send({ type: 'request_codec_data', supported_codecs: this.supportedCodecs });
  }

  /**
   * Handle WebSocket close event with reconnection logic.
   * Ported from mews.js:408-431
   */
  private handleWsClose(): void {
    if (this.debugging) console.log('MEWS: WebSocket closed');
    // Reconnection is handled by WebSocketManager
  }

  /**
   * Handle incoming WebSocket message.
   * Routes to binary append or JSON control message handler.
   * Ported from mews.js:456-830
   */
  private handleMessage(data: ArrayBuffer | string): void {
    if (typeof data === 'string') {
      try {
        const msg = JSON.parse(data) as MewsMessage;
        this.handleControlMessage(msg);
        // Notify listeners (mews.js:795-799)
        this.wsManager?.notifyListeners(msg);
      } catch (e) {
        if (this.debugging) console.error('MEWS: Failed to parse message', e);
      }
      return;
    }

    // Binary data - MP4 segment (mews.js:802-829)
    const bytes = new Uint8Array(data);
    this.sbManager?.append(bytes);
    this.trackBits(data);
  }

  /**
   * Handle JSON control messages.
   * Ported from mews.js:461-799
   */
  private handleControlMessage(msg: MewsMessage): void {
    if (this.debugging && msg.type !== 'on_time') {
      console.log('MEWS: message', msg);
    }

    switch (msg.type) {
      case 'on_stop':
        this.handleOnStop();
        break;

      case 'on_time':
        this.handleOnTime(msg as OnTimeMessage);
        break;

      case 'tracks':
        this.handleTracks(msg);
        break;

      case 'pause':
        this.handlePause();
        break;

      case 'codec_data':
        this.resolveDelay('codec_data');
        break;

      case 'seek':
        this.resolveDelay('seek');
        break;

      case 'set_speed':
        this.resolveDelay('set_speed');
        break;
    }
  }

  /**
   * Handle on_stop message - stream ended (VoD).
   * Ported from mews.js:462-471
   */
  private handleOnStop(): void {
    // Mark as VoD (stream ended)
    this.streamType = 'vod';

    // Wait for buffer to finish playing (mews.js:465-469)
    const onWaiting = () => {
      if (this.sbManager) {
        this.sbManager.paused = true;
      }
      this.emit('ended', undefined);
      this.videoElement?.removeEventListener('waiting', onWaiting);
    };
    this.videoElement?.addEventListener('waiting', onWaiting);
  }

  /**
   * Handle on_time message - playback time sync.
   * Ported from mews.js:473-621
   */
  private handleOnTime(msg: OnTimeMessage): void {
    const data = msg.data;
    if (!data || !this.videoElement) return;

    const currentMs = data.current;
    const endMs = data.end;
    const jitter = data.jitter || 0;

    // Buffer calculation (mews.js:474)
    const buffer = currentMs - this.videoElement.currentTime * 1000;
    const serverDelay = this.getServerDelay();
    // Chrome needs larger base buffer (mews.js:482)
    const isChrome = /Chrome/.test(navigator.userAgent) && !/Edge|Edg/.test(navigator.userAgent);
    const baseBuffer = isChrome ? 1000 : 100;
    const desiredBuffer = Math.max(baseBuffer + serverDelay, serverDelay * 2);
    const desiredBufferWithJitter = desiredBuffer + jitter;

    // VoD gets extra buffer (mews.js:480)
    const actualDesiredBuffer = this.streamType !== 'live' ? desiredBuffer + 2000 : desiredBuffer;

    if (this.debugging) {
      console.log(
        'MEWS: on_time',
        'current:', currentMs / 1000,
        'video:', this.videoElement.currentTime,
        'rate:', this.requestedRate + 'x',
        'buffer:', Math.round(buffer), '/', Math.round(desiredBuffer),
        this.streamType === 'live' ? 'latency:' + Math.round((endMs || 0) - this.videoElement.currentTime * 1000) + 'ms' : ''
      );
    }

    if (!this.sbManager) {
      if (this.debugging) console.log('MEWS: on_time but no sourceBuffer');
      return;
    }

    // Update duration (mews.js:501-504)
    if (endMs !== undefined && this.lastDuration !== endMs / 1000) {
      this.lastDuration = endMs / 1000;
      // Duration is updated via native video element durationchange event
    }

    // Mark source buffer as not paused
    this.sbManager.paused = false;

    // Playback rate tuning for LIVE streams (mews.js:508-545)
    if (this.streamType === 'live') {
      this.tuneLivePlaybackRate(buffer, desiredBufferWithJitter, data.play_rate_curr);
    } else {
      // VoD - adjust server delivery speed (mews.js:547-586)
      this.tuneVodDeliverySpeed(buffer, actualDesiredBuffer, data.play_rate_curr);
    }

    // Track change detection (mews.js:593-619)
    if (data.tracks && this.currentTracks.join(',') !== data.tracks.join(',')) {
      if (this.debugging) {
        for (const trackId of data.tracks) {
          if (!this.currentTracks.includes(trackId)) {
            console.log('MEWS: track changed', trackId);
          }
        }
      }
      this.currentTracks = data.tracks;
    }
  }

  /**
   * Tune playback rate for live streams.
   * Ported from mews.js:508-545
   *
   * Fixed: Use direct assignment instead of multiplication to prevent
   * compounding rate adjustments on each on_time message.
   */
  private tuneLivePlaybackRate(buffer: number, desiredBuffer: number, playRateCurr?: 'auto' | number): void {
    if (!this.videoElement) return;

    if (this.requestedRate === 1) {
      if (playRateCurr === 'auto' && this.videoElement.currentTime > 0) {
        // Assume we want to be as live as possible
        if (buffer > desiredBuffer * 2) {
          // Buffer too big, speed up (mews.js:513-516)
          this.requestedRate = 1 + Math.min(1, (buffer - desiredBuffer) / desiredBuffer) * 0.08;
          this.videoElement.playbackRate = this.requestedRate;
          if (this.debugging) console.log('MEWS: speeding up to', this.requestedRate);
        } else if (buffer < 0) {
          // Negative buffer, slow down (mews.js:518-521)
          this.requestedRate = 0.8;
          this.videoElement.playbackRate = this.requestedRate;
          if (this.debugging) console.log('MEWS: slowing down to', this.requestedRate);
        } else if (buffer < desiredBuffer / 2) {
          // Buffer too small, slow down (mews.js:523-526)
          this.requestedRate = 1 + Math.min(1, (buffer - desiredBuffer) / desiredBuffer) * 0.08;
          this.videoElement.playbackRate = this.requestedRate;
          if (this.debugging) console.log('MEWS: adjusting to', this.requestedRate);
        }
      }
    } else if (this.requestedRate > 1) {
      // Return to normal when buffer is small enough (mews.js:531-536)
      if (buffer < desiredBuffer) {
        this.videoElement.playbackRate = 1;
        this.requestedRate = 1;
        if (this.debugging) console.log('MEWS: returning to normal rate');
      }
    } else {
      // requestedRate < 1, return to normal when buffer is big enough (mews.js:538-544)
      if (buffer > desiredBuffer) {
        this.videoElement.playbackRate = 1;
        this.requestedRate = 1;
        if (this.debugging) console.log('MEWS: returning to normal rate');
      }
    }
  }

  /**
   * Tune server delivery speed for VoD.
   * Ported from mews.js:547-586
   */
  private tuneVodDeliverySpeed(buffer: number, desiredBuffer: number, playRateCurr?: 'auto' | number): void {
    if (this.requestedRate === 1) {
      if (playRateCurr === 'auto') {
        if (buffer < desiredBuffer / 2) {
          if (buffer < -10000) {
            // Way behind, seek to current position (mews.js:553-554)
            this.send({ type: 'seek', seek_time: Math.round((this.videoElement?.currentTime || 0) * 1000) });
          } else {
            // Request faster delivery (mews.js:557-560)
            this.requestedRate = 2;
            this.send({ type: 'set_speed', play_rate: this.requestedRate });
            if (this.debugging) console.log('MEWS: requesting faster delivery');
          }
        } else if (buffer - desiredBuffer > desiredBuffer) {
          // Too much buffer, slow down (mews.js:563-566)
          this.requestedRate = 0.5;
          this.send({ type: 'set_speed', play_rate: this.requestedRate });
          if (this.debugging) console.log('MEWS: requesting slower delivery');
        }
      }
    } else if (this.requestedRate > 1) {
      if (buffer > desiredBuffer) {
        // Enough buffer, return to realtime (mews.js:571-575)
        this.send({ type: 'set_speed', play_rate: 'auto' });
        this.requestedRate = 1;
        if (this.debugging) console.log('MEWS: returning to realtime delivery');
      }
    } else {
      if (buffer < desiredBuffer) {
        // Buffer small enough, return to realtime (mews.js:579-583)
        this.send({ type: 'set_speed', play_rate: 'auto' });
        this.requestedRate = 1;
        if (this.debugging) console.log('MEWS: returning to realtime delivery');
      }
    }
  }

  /**
   * Handle tracks message - codec switch.
   * Ported from mews.js:623-788
   */
  private handleTracks(msg: MewsMessage): void {
    const codecs: string[] = msg.data?.codecs || [];
    const switchPointMs = msg.data?.current;

    if (!codecs.length) {
      this.emit('error', 'Track switch contains no codecs');
      return;
    }

    // Check if codecs are same as before (mews.js:676)
    const prevCodecs = this.lastCodecs || this.sbManager?.getCodecs() || [];
    if (this.codecsEqual(prevCodecs, codecs)) {
      if (this.debugging) console.log('MEWS: keeping buffer, codecs same');
      // If at position 0 and switch point is not 0, seek to switch point (mews.js:678-679)
      if (this.videoElement?.currentTime === 0 && switchPointMs && switchPointMs !== 0) {
        this.setSeekingPosition(switchPointMs / 1000);
      }
      return;
    }

    // Different codecs, save for next comparison (mews.js:687)
    this.lastCodecs = codecs;

    // Change codecs (will handle msgqueue internally)
    this.sbManager?.changeCodecs(codecs, switchPointMs);
  }

  /**
   * Handle pause message.
   * Ported from mews.js:790-792
   */
  private handlePause(): void {
    if (this.sbManager) {
      this.sbManager.paused = true;
    }
  }

  /**
   * Set video currentTime with retry logic.
   * Ported from mews.js:635-672
   */
  private setSeekingPosition(tSec: number): void {
    if (!this.videoElement || !this.sbManager) return;

    const currPos = this.videoElement.currentTime;
    if (currPos > tSec) {
      // Don't seek backwards (mews.js:637-639)
      tSec = currPos;
    }

    const buffered = this.videoElement.buffered;
    if (!buffered.length || buffered.end(buffered.length - 1) < tSec) {
      // Desired position not in buffer yet, wait for more data (mews.js:641-644)
      this.sbManager.scheduleAfterUpdate(() => this.setSeekingPosition(tSec));
      return;
    }

    this.videoElement.currentTime = tSec;

    if (this.videoElement.currentTime < tSec - 0.001) {
      // Didn't reach target, retry (mews.js:648-651)
      this.sbManager.scheduleAfterUpdate(() => this.setSeekingPosition(tSec));
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
   * Ported from mews.js:959-1023
   */
  async play(): Promise<void> {
    const v = this.videoElement;
    if (!v) return;

    // If already playing, nothing to do (mews.js:961-964)
    if (!v.paused) return;

    // Wait for ready state (codec_data received) with timeout
    if (!this.isReady) {
      await new Promise<void>((resolve, reject) => {
        const timeout = setTimeout(() => {
          reject(new Error('MEWS: Timeout waiting for codec data'));
        }, 5000);
        this.readyResolvers.push(() => {
          clearTimeout(timeout);
          resolve();
        });
      });
    }

    // Use listener to wait for on_time before playing (mews.js:973-1017)
    return new Promise((resolve, reject) => {
      // Flag to prevent race condition where multiple on_time messages
      // could trigger seek before the first completes
      let handled = false;

      const onTime: MewsMessageListener = (msg) => {
        // Remove listener immediately to prevent race condition (single-use pattern)
        if (handled) return;
        handled = true;
        this.wsManager?.removeListener('on_time', onTime);

        if (!this.sbManager) {
          if (this.debugging) console.log('MEWS: play waiting for sourceBuffer');
          handled = false; // Allow retry
          this.wsManager?.addListener('on_time', onTime);
          return;
        }

        const data = (msg as OnTimeMessage).data;

        if (this.streamType === 'live') {
          // Live stream - wait for buffer then seek to live edge (mews.js:978-998)
          const waitForBuffer = () => {
            if (!v.buffered.length) return;

            const bufferIdx = this.sbManager?.findBufferIndex(data.current / 1000);
            if (typeof bufferIdx === 'number') {
              // Check if current position is in buffer
              if (v.buffered.start(bufferIdx) > v.currentTime || v.buffered.end(bufferIdx) < v.currentTime) {
                v.currentTime = data.current / 1000;
                if (this.debugging) console.log('MEWS: seeking to live position', v.currentTime);
              }

              v.play().then(resolve).catch((err) => {
                this.pause();
                reject(err);
              });

              this.sbManager!.paused = false;
            }
          };

          // Wait for buffer via updateend
          this.sbManager?.scheduleAfterUpdate(waitForBuffer);
        } else {
          // VoD - just play when we have data (mews.js:1010-1016)
          this.sbManager!.paused = false;
          if (v.buffered.length && v.buffered.start(0) > v.currentTime) {
            v.currentTime = v.buffered.start(0);
          }
          v.play().then(resolve).catch(reject);
        }
      };

      this.wsManager?.addListener('on_time', onTime);

      // Send play command (mews.js:1020-1022)
      const skipToLive = this.streamType === 'live' && v.currentTime === 0;
      if (skipToLive) {
        this.send({ type: 'play', seek_time: 'live' });
      } else {
        this.send({ type: 'play' });
      }
    });
  }

  /**
   * Pause playback and server delivery.
   * Ported from mews.js:1025-1029
   */
  pause(): void {
    this.videoElement?.pause();
    this.send({ type: 'hold' });
    if (this.sbManager) {
      this.sbManager.paused = true;
    }
  }

  /**
   * Seek to position with server sync.
   * Ported from mews.js:1071-1111
   */
  seek(time: number): void {
    if (!this.videoElement || isNaN(time) || time < 0) return;

    // Calculate seek time with server delay compensation (mews.js:1082)
    const seekMs = Math.round(Math.max(0, time * 1000 - (250 + this.getServerDelay())));

    this.logDelay('seek');
    this.send({ type: 'seek', seek_time: seekMs });

    // Wait for seek acknowledgment then on_time (mews.js:1084-1108)
    const onSeek: MewsMessageListener = () => {
      this.wsManager?.removeListener('seek', onSeek);

      const onTime: MewsMessageListener = (msg) => {
        this.wsManager?.removeListener('on_time', onTime);

        // Use server's actual position (mews.js:1089)
        const actualTime = (msg as OnTimeMessage).data.current / 1000;
        this.trySetCurrentTime(actualTime);
      };

      this.wsManager?.addListener('on_time', onTime);
    };

    this.wsManager?.addListener('seek', onSeek);

    // Also set directly as fallback
    this.videoElement.currentTime = time;
    if (this.debugging) console.log('MEWS: seeking to', time);
  }

  /**
   * Try to set currentTime with retry logic.
   * Ported from mews.js:1092-1103
   */
  private trySetCurrentTime(tSec: number, retries = 10): void {
    const v = this.videoElement;
    if (!v) return;

    v.currentTime = tSec;

    if (v.currentTime < tSec - 0.001 && retries > 0) {
      // Failed to seek, retry (mews.js:1095-1100)
      this.sbManager?.scheduleAfterUpdate(() => this.trySetCurrentTime(tSec, retries - 1));
    }
  }

  getCurrentTime(): number {
    return this.videoElement?.currentTime ?? 0;
  }

  getDuration(): number {
    return isFinite(this.lastDuration) ? this.lastDuration : super.getDuration();
  }

  /**
   * Set playback rate.
   * Ported from mews.js:1119-1129
   */
  setPlaybackRate(rate: number): void {
    super.setPlaybackRate(rate);
    const playRate = rate === 1 ? 'auto' : rate;
    this.logDelay('set_speed');
    this.send({ type: 'set_speed', play_rate: playRate });
  }

  getQualities(): Array<{ id: string; label: string; isAuto?: boolean; active?: boolean }> {
    return [{ id: 'auto', label: 'Auto', isAuto: true, active: true }];
  }

  selectQuality(id: string): void {
    if (id === 'auto') {
      this.send({ type: 'set_speed', play_rate: 'auto' });
    }
  }

  /**
   * Set tracks for ABR or quality selection.
   * Ported from mews.js:1030-1037
   */
  setTracks(obj: { video?: string; audio?: string; subtitle?: string }): void {
    if (!Object.keys(obj).length) return;
    this.send({ type: 'tracks', ...obj });
  }

  /**
   * Select a subtitle track.
   */
  selectTextTrack(id: string | null): void {
    if (id === null) {
      this.send({ type: 'tracks', subtitle: 'none' });
    } else {
      this.send({ type: 'tracks', subtitle: id });
    }
  }

  isLive(): boolean {
    return this.streamType === 'live';
  }

  /**
   * Jump to live edge.
   */
  jumpToLive(): void {
    if (this.streamType !== 'live' || !this.wsManager) return;
    this.send({ type: 'play', seek_time: 'live' });
    this.videoElement?.play().catch(() => {});
  }

  async getStats(): Promise<any> {
    return {
      currentBps: this.currentBps,
      waitingEvents: this.nWaiting,
      isLive: this.streamType === 'live',
      serverDelay: this.getServerDelay()
    };
  }

  // ========== EVENT HANDLERS ==========

  /**
   * Install waiting event handler.
   * Handles buffer gaps and ABR.
   * Ported from mews.js:1177-1186, 1272-1278
   */
  private installWaitingHandler(): void {
    if (!this.videoElement) return;

    this.videoElement.addEventListener('waiting', () => {
      if (this.seeking) return;

      const v = this.videoElement!;
      if (!v.buffered || !v.buffered.length) return;

      // Check for buffer gap and jump it (mews.js:1180-1186)
      const bufferIdx = this.sbManager?.findBufferIndex(v.currentTime);
      if (bufferIdx !== false && typeof bufferIdx === 'number') {
        if (bufferIdx + 1 < v.buffered.length) {
          const nextStart = v.buffered.start(bufferIdx + 1);
          if (nextStart - v.currentTime < 10) {
            if (this.debugging) console.log('MEWS: skipping buffer gap to', nextStart);
            v.currentTime = nextStart;
          }
        }
      }

      // ABR trigger (mews.js:1272-1278)
      this.nWaiting++;
      if (this.nWaiting >= this.nWaitingThreshold && this.currentBps) {
        this.nWaiting = 0;
        if (this.debugging) console.log('MEWS: ABR triggered, requesting lower bitrate');
        this.setTracks({ video: `<${Math.round(this.currentBps)}bps,minbps` });
      }
    });
  }

  /**
   * Install seeking event handlers.
   * Ported from mews.js:1169-1175
   */
  private installSeekingHandler(): void {
    if (!this.videoElement) return;

    this.videoElement.addEventListener('seeking', () => {
      this.seeking = true;
    });

    this.videoElement.addEventListener('seeked', () => {
      this.seeking = false;
    });
  }

  /**
   * Install pause event handler for browser pause detection.
   * Ported from mews.js:1188-1200
   */
  private installPauseHandler(): void {
    if (!this.videoElement) return;

    this.videoElement.addEventListener('pause', () => {
      if (this.sbManager && !this.sbManager.paused) {
        // Browser paused (probably tab hidden) - pause download (mews.js:1189-1192)
        if (this.debugging) console.log('MEWS: browser paused, pausing download');
        this.send({ type: 'hold' });
        this.sbManager.paused = true;

        // Resume on play (mews.js:1193-1197)
        const onPlay = () => {
          if (this.sbManager?.paused) {
            this.send({ type: 'play' });
          }
          this.videoElement?.removeEventListener('play', onPlay);
        };
        this.videoElement?.addEventListener('play', onPlay);
      }
    });
  }

  /**
   * Install loop handler for VoD content.
   * Ported from mews.js:1157-1167
   */
  private installLoopHandler(): void {
    if (!this.videoElement) return;

    this.videoElement.addEventListener('ended', () => {
      const v = this.videoElement;
      if (!v) return;

      if (v.loop && this.streamType !== 'live') {
        // Loop VoD content (mews.js:1159-1166)
        this.seek(0);
        this.sbManager?._do(() => {
          try {
            // Clear buffer for clean loop
          } catch {}
        });
      }
    });
  }

  // ========== UTILITIES ==========

  /**
   * Send command to server with retry.
   * Ported from mews.js:904-944
   */
  private send(cmd: object): void {
    if (this.wsManager) {
      this.wsManager.send(cmd);
    }
  }

  /**
   * Log delay for server RTT estimation.
   * Ported from mews.js:835-862
   */
  private logDelay(type: string): void {
    this.pendingDelayTypes[type] = Date.now();
  }

  /**
   * Resolve delay measurement.
   * Ported from mews.js:855-861, 863-867
   */
  private resolveDelay(type: string): void {
    const start = this.pendingDelayTypes[type];
    if (start) {
      const delay = Date.now() - start;
      this.serverDelays.unshift(delay);
      if (this.serverDelays.length > 5) {
        this.serverDelays.pop();
      }
      delete this.pendingDelayTypes[type];
    }
  }

  /**
   * Get average server delay.
   * Ported from mews.js:869-881
   */
  private getServerDelay(): number {
    if (!this.serverDelays.length) return 500;
    const n = Math.min(3, this.serverDelays.length);
    let sum = 0;
    for (let i = 0; i < n; i++) {
      sum += this.serverDelays[i];
    }
    return sum / n;
  }

  /**
   * Track bandwidth for ABR.
   * Ported from mews.js:1280-1303
   */
  private trackBits(buf: ArrayBuffer): void {
    this.bitCounter.push(buf.byteLength * 8);
    this.bitsSince.push(Date.now());

    // Keep window size of 5 samples
    if (this.bitCounter.length > 5) {
      this.bitCounter.shift();
      this.bitsSince.shift();
    }

    // Calculate current bitrate
    if (this.bitCounter.length >= 2) {
      const bits = this.bitCounter[this.bitCounter.length - 1];
      const dt = (this.bitsSince[this.bitsSince.length - 1] - this.bitsSince[0]) / 1000;
      if (dt > 0) {
        this.currentBps = Math.round(bits / dt);
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

  async destroy(): Promise<void> {
    console.debug('[MEWS] destroy() called');
    this.isDestroyed = true;
    this.isReady = false;
    this.readyResolvers = [];

    if (this.analyticsTimer) {
      clearInterval(this.analyticsTimer);
      this.analyticsTimer = null;
    }

    // CRITICAL: Close WebSocket FIRST to stop all network activity immediately
    // Don't send 'stop' message - it can trigger retry logic and delay cleanup
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
      try { this.container.removeChild(this.videoElement); } catch {}
    }

    this.videoElement = null;
    this.container = null;
    this.mediaSource = null;
    this.listeners.clear();
    console.debug('[MEWS] destroy() completed');
  }
}

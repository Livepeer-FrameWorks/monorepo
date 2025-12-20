/**
 * MistWebRTCPlayerImpl - IPlayer implementation for MistServer native WebRTC
 *
 * Uses MistServer's WebSocket signaling protocol instead of WHEP.
 * Key advantages over WHEP:
 * - Server-side track selection via signaling
 * - Playback speed control (including "auto" for live catch-up)
 * - Seeking via signaling (DVR support)
 * - Real-time buffer_window updates
 * - DataChannel for timed metadata
 */

import { BasePlayer } from '../../core/PlayerInterface';
import type { StreamSource, StreamInfo, PlayerOptions, PlayerCapability } from '../../core/PlayerInterface';
import { MistSignaling, type MistTimeUpdate } from '../../core/MistSignaling';
import { checkWebRTCCodecCompatibility } from '../../core/detector';

export class MistWebRTCPlayerImpl extends BasePlayer {
  readonly capability: PlayerCapability = {
    name: "MistServer WebRTC",
    shortname: "mist-webrtc",
    priority: 2, // After direct (WHEP=1), before HLS.js (3)
    mimes: ["webrtc", "mist/webrtc"]
  };

  private signaling: MistSignaling | null = null;
  private peerConnection: RTCPeerConnection | null = null;
  private dataChannel: RTCDataChannel | null = null;
  private container: HTMLElement | null = null;
  private destroyed = false;

  // Time tracking
  private seekOffset = 0;
  private durationMs = 0;
  private isLiveStream = true;
  private playRate: number | 'auto' = 'auto';

  // Buffer window tracking (P2)
  private bufferWindow = 0;

  // Track change detection (P1)
  private currentTracks: string[] = [];

  // Store source/options for loop reconnect (P1)
  private currentSource: StreamSource | null = null;
  private currentOptions: PlayerOptions | null = null;

  // Stats tracking
  private lastInboundStats: { video?: { bytesReceived: number }; audio?: { bytesReceived: number }; timestamp: number } | null = null;

  /**
   * Chrome on Android has a bug where H264 is not available immediately
   * after the tab is opened. Retry up to 5 times with 100ms intervals.
   * https://bugs.chromium.org/p/webrtc/issues/detail?id=11620
   */
  private async checkH264Available(retries = 5): Promise<boolean> {
    for (let i = 0; i < retries; i++) {
      try {
        const caps = RTCRtpReceiver.getCapabilities?.('video');
        if (caps?.codecs.some(c => c.mimeType === 'video/H264')) {
          return true;
        }
      } catch {}
      if (i < retries - 1) {
        await new Promise(r => setTimeout(r, 100));
      }
    }
    console.warn('[MistWebRTC] H264 not available after retries');
    return false;
  }

  /**
   * Load MistServer's WebRTC browser equalizer script for browser-specific fixes.
   * This is non-fatal if it fails to load.
   */
  private async loadBrowserEqualizer(host: string): Promise<void> {
    if ((window as any).WebRTCBrowserEqualizerLoaded) return;

    return new Promise((resolve) => {
      const script = document.createElement('script');
      script.src = `${host}/webrtc.js`;
      script.onload = () => {
        console.debug('[MistWebRTC] Browser equalizer loaded');
        resolve();
      };
      script.onerror = () => {
        console.warn('[MistWebRTC] Failed to load browser equalizer');
        resolve(); // Non-fatal
      };
      document.head.appendChild(script);
    });
  }

  /**
   * Compare two arrays for equality (order-independent)
   */
  private arraysEqual(a: string[], b: string[]): boolean {
    if (a.length !== b.length) return false;
    const sortedA = [...a].sort();
    const sortedB = [...b].sort();
    return sortedA.every((v, i) => v === sortedB[i]);
  }

  isMimeSupported(mimetype: string): boolean {
    return this.capability.mimes.includes(mimetype);
  }

  isBrowserSupported(mimetype: string, source: StreamSource, streamInfo: StreamInfo): boolean | string[] {
    // Check basic WebRTC support
    if (!('RTCPeerConnection' in window) || !('WebSocket' in window)) return false;

    // Check codec compatibility
    const codecCompat = checkWebRTCCodecCompatibility(streamInfo.meta.tracks);
    if (!codecCompat.compatible) {
      console.debug('[MistWebRTC] Skipping - incompatible codecs:', codecCompat.incompatibleCodecs.join(', '));
      return false;
    }

    // Return which track types we can play
    const playable: string[] = [];
    if (codecCompat.details.compatibleVideoCodecs.length > 0) {
      playable.push('video');
    }
    if (codecCompat.details.compatibleAudioCodecs.length > 0) {
      playable.push('audio');
    }

    return playable.length > 0 ? playable : false;
  }

  async initialize(container: HTMLElement, source: StreamSource, options: PlayerOptions): Promise<HTMLVideoElement> {
    this.destroyed = false;
    this.container = container;
    this.currentSource = source;
    this.currentOptions = options;
    container.classList.add('fw-player-container');

    // Load browser equalizer script (P0) - extract host from source URL
    try {
      const url = new URL(source.url, window.location.href);
      const host = `${url.protocol}//${url.host}`;
      await this.loadBrowserEqualizer(host);
    } catch {}

    // Check H264 availability with retry for Chrome Android bug (P0)
    await this.checkH264Available();

    // Create video element
    const video = document.createElement('video');
    video.classList.add('fw-player-video');
    video.setAttribute('playsinline', '');
    video.setAttribute('crossorigin', 'anonymous');

    if (options.autoplay) video.autoplay = true;
    if (options.muted) video.muted = true;
    video.controls = options.controls === true; // Explicit false to hide native controls
    if (options.loop) video.loop = true;
    if (options.poster) video.poster = options.poster;

    this.videoElement = video;
    container.appendChild(video);
    this.setupVideoEventListeners(video, options);

    try {
      await this.setupWebRTC(video, source, options);
      return video;
    } catch (error: any) {
      this.emit('error', error.message || String(error));
      throw error;
    }
  }

  async destroy(): Promise<void> {
    this.destroyed = true;

    // Close signaling
    if (this.signaling) {
      try {
        this.signaling.stop();
        this.signaling.close();
      } catch {}
      this.signaling = null;
    }

    // Close data channel
    if (this.dataChannel) {
      try { this.dataChannel.close(); } catch {}
      this.dataChannel = null;
    }

    // Close peer connection
    if (this.peerConnection) {
      try { this.peerConnection.close(); } catch {}
      this.peerConnection = null;
    }

    // Clean up video element
    if (this.videoElement) {
      try { (this.videoElement as any).srcObject = null; } catch {}
      this.videoElement.pause();

      if (this.container) {
        try { this.container.removeChild(this.videoElement); } catch {}
      }
    }

    this.videoElement = null;
    this.container = null;
    this.listeners.clear();
  }

  // Override seek to use signaling
  seek(time: number): void {
    if (!this.signaling?.isConnected || !this.videoElement) return;

    this.videoElement.pause();
    this.seekOffset = time - this.videoElement.currentTime;
    this.signaling.seek(time).catch((e) => {
      console.warn('[MistWebRTC] Seek failed:', e);
    });
  }

  // Override setPlaybackRate to use signaling
  setPlaybackRate(rate: number): void {
    this.signaling?.setSpeed(rate);
    this.playRate = rate;
  }

  // Implement jumpToLive via signaling
  jumpToLive(): void {
    if (!this.signaling?.isConnected || !this.videoElement) return;

    this.videoElement.pause();
    this.seekOffset = 0;
    this.signaling.seek('live').catch((e) => {
      console.warn('[MistWebRTC] Jump to live failed:', e);
    });
  }

  // Override isLive
  isLive(): boolean {
    return this.isLiveStream;
  }

  // Override getDuration to use signaling data
  getDuration(): number {
    return this.durationMs > 0 ? this.durationMs / 1000 : super.getDuration();
  }

  // Override getCurrentTime to include seek offset
  getCurrentTime(): number {
    const v = this.videoElement;
    if (!v) return 0;
    return this.seekOffset + v.currentTime;
  }

  /**
   * Get available quality levels from signaling
   */
  getQualities(): Array<{ id: string; label: string; isAuto?: boolean; active?: boolean }> {
    // Always offer auto as first option
    const qualities: Array<{ id: string; label: string; isAuto?: boolean; active?: boolean }> = [
      { id: 'auto', label: 'Auto', isAuto: true, active: this.playRate === 'auto' }
    ];

    // If we have track info from signaling, add quality options
    // MistServer provides track selection via ~widthxheight or |bitrate patterns
    // For now, we expose auto mode - full track enumeration would require
    // parsing the signaling track info which varies by stream
    return qualities;
  }

  // Track selection via signaling
  selectQuality(id: string): void {
    if (!this.signaling?.isConnected) return;

    if (id === 'auto') {
      this.signaling.setSpeed('auto');
    } else {
      // Track selection: ~widthxheight or |bitrate
      this.signaling.setTracks({ video: id });
    }
  }

  // Text track selection via signaling
  selectTextTrack(id: string | null): void {
    if (!this.signaling?.isConnected) return;

    if (id === null) {
      this.signaling.setTracks({ video: 'none' });
    } else {
      this.signaling.setTracks({ video: id });
    }
  }

  async getStats(): Promise<{
    type: 'webrtc';
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
  } | undefined> {
    if (!this.peerConnection) return undefined;

    try {
      const stats = await this.peerConnection.getStats();
      const now = Date.now();
      const result: any = { type: 'webrtc', timestamp: now };

      stats.forEach((report: any) => {
        if (report.type === 'inbound-rtp') {
          const packetLossRate = report.packetsReceived > 0
            ? (report.packetsLost / (report.packetsReceived + report.packetsLost)) * 100
            : 0;

          // Calculate bitrate from previous sample
          let bitrate = 0;
          if (this.lastInboundStats && this.lastInboundStats[report.kind as 'video' | 'audio']) {
            const prev = this.lastInboundStats[report.kind as 'video' | 'audio'];
            const timeDelta = (now - this.lastInboundStats.timestamp) / 1000;
            if (timeDelta > 0 && prev) {
              const bytesDelta = report.bytesReceived - prev.bytesReceived;
              bitrate = Math.round((bytesDelta * 8) / timeDelta);
            }
          }

          if (report.kind === 'video') {
            const frameDropRate = report.framesDecoded > 0
              ? (report.framesDropped / (report.framesDecoded + report.framesDropped)) * 100
              : 0;

            result.video = {
              bytesReceived: report.bytesReceived || 0,
              packetsReceived: report.packetsReceived || 0,
              packetsLost: report.packetsLost || 0,
              packetLossRate,
              jitter: (report.jitter || 0) * 1000,
              framesDecoded: report.framesDecoded || 0,
              framesDropped: report.framesDropped || 0,
              frameDropRate,
              frameWidth: report.frameWidth || 0,
              frameHeight: report.frameHeight || 0,
              framesPerSecond: report.framesPerSecond || 0,
              bitrate,
              jitterBufferDelay: report.jitterBufferDelay && report.jitterBufferEmittedCount
                ? (report.jitterBufferDelay / report.jitterBufferEmittedCount) * 1000
                : 0,
            };
          }
          if (report.kind === 'audio') {
            result.audio = {
              bytesReceived: report.bytesReceived || 0,
              packetsReceived: report.packetsReceived || 0,
              packetsLost: report.packetsLost || 0,
              packetLossRate,
              jitter: (report.jitter || 0) * 1000,
              bitrate,
            };
          }
        }
        if (report.type === 'candidate-pair' && report.nominated) {
          result.network = {
            rtt: report.currentRoundTripTime ? report.currentRoundTripTime * 1000 : 0,
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

  async getLatency(): Promise<{ estimatedMs: number; jitterBufferMs: number; rttMs: number } | undefined> {
    const s = await this.getStats();
    if (!s) return undefined;

    return {
      estimatedMs: s.video?.jitterBufferDelay || 0,
      jitterBufferMs: s.video?.jitterBufferDelay || 0,
      rttMs: s.network?.rtt || 0,
    };
  }

  /**
   * Get the current buffer window in milliseconds (P2)
   * This is the difference between buffer end and begin from on_time messages.
   */
  getBufferWindow(): number {
    return this.bufferWindow;
  }

  /**
   * Request video track matching the given player size (P2 - ABR_resize)
   * Uses MistServer's ~widthxheight track selection syntax.
   */
  setQualityForSize(size: { width: number; height: number }): void {
    if (!this.signaling?.isConnected) return;
    this.signaling.setTracks({ video: `~${size.width}x${size.height}` });
  }

  /**
   * Get the metadata DataChannel for timed metadata (P2)
   * Returns the RTCDataChannel or null if not available.
   */
  getMetaDataChannel(): RTCDataChannel | null {
    return this.dataChannel;
  }

  /**
   * Override to add WebRTC-specific event handling:
   * - Loop reconnect for VoD (P1)
   * - Proper autoplay disable (P2)
   */
  protected setupVideoEventListeners(video: HTMLVideoElement, options: PlayerOptions): void {
    // Call parent implementation first
    super.setupVideoEventListeners(video, options);

    // Proper autoplay disable handling (P2)
    // WebRTC may auto-start even with autoplay=false
    if (!options.autoplay) {
      const pauseOnFirstPlay = () => {
        video.pause();
        this.signaling?.pause();
        video.removeEventListener('play', pauseOnFirstPlay);
      };
      video.addEventListener('play', pauseOnFirstPlay);
    }

    // Loop reconnect for VoD content (P1)
    video.addEventListener('ended', async () => {
      if (video.loop && !this.isLiveStream && this.currentSource && this.currentOptions) {
        console.debug('[MistWebRTC] VoD ended with loop enabled, reconnecting...');
        try {
          // Partial cleanup - keep container and video element
          if (this.signaling) {
            try {
              this.signaling.stop();
              this.signaling.close();
            } catch {}
            this.signaling = null;
          }
          if (this.dataChannel) {
            try { this.dataChannel.close(); } catch {}
            this.dataChannel = null;
          }
          if (this.peerConnection) {
            try { this.peerConnection.close(); } catch {}
            this.peerConnection = null;
          }

          // Reconnect WebRTC
          await this.setupWebRTC(video, this.currentSource, this.currentOptions);
        } catch (e) {
          console.error('[MistWebRTC] Failed to reconnect for loop:', e);
          this.emit('error', 'Failed to reconnect for loop');
        }
      }
    });
  }

  // Private methods

  private async setupWebRTC(video: HTMLVideoElement, source: StreamSource, options: PlayerOptions): Promise<void> {
    const sourceAny = source as any;
    const iceServers: RTCIceServer[] = sourceAny?.iceServers || [];

    // Create signaling
    this.signaling = new MistSignaling({
      url: source.url,
      timeout: 5000,
      onLog: (msg) => console.debug(`[MistWebRTC] ${msg}`),
    });

    // Create peer connection
    const pc = new RTCPeerConnection({ iceServers });
    this.peerConnection = pc;

    // Create data channel for metadata
    this.dataChannel = pc.createDataChannel('*', { protocol: 'JSON' });
    this.dataChannel.onmessage = (event) => {
      if (this.destroyed) return;
      console.debug('[MistWebRTC] DataChannel message:', event.data);
      // Handle timed metadata here if needed
    };

    // Handle incoming tracks
    pc.ontrack = (event) => {
      if (this.destroyed) return;
      if (video && event.streams[0]) {
        video.srcObject = event.streams[0];
      }
    };

    // Connection state changes
    pc.onconnectionstatechange = () => {
      if (this.destroyed) return;
      const state = pc.connectionState;
      console.debug(`[MistWebRTC] Connection state: ${state}`);

      if (state === 'failed') {
        this.emit('error', 'WebRTC connection failed (firewall?)');
      }
    };

    // ICE connection state
    pc.oniceconnectionstatechange = () => {
      if (this.destroyed) return;
      const state = pc.iceConnectionState;
      console.debug(`[MistWebRTC] ICE state: ${state}`);

      if (state === 'failed') {
        this.emit('error', 'ICE connection failed');
      }
    };

    // Set up signaling event handlers
    this.setupSignalingHandlers(pc, video);

    // Connect signaling
    this.signaling.connect();

    // Wait for signaling to connect
    await new Promise<void>((resolve, reject) => {
      const timeout = setTimeout(() => {
        reject(new Error('Signaling connection timeout'));
      }, 10000);

      this.signaling!.once('connected', () => {
        clearTimeout(timeout);
        resolve();
      });

      this.signaling!.once('error', ({ message }) => {
        clearTimeout(timeout);
        reject(new Error(message));
      });
    });

    // Create and send offer
    await this.createAndSendOffer(pc);

    // Wait for answer
    await new Promise<void>((resolve, reject) => {
      const timeout = setTimeout(() => {
        reject(new Error('SDP answer timeout'));
      }, 10000);

      this.signaling!.once('answer_sdp', async ({ result, answer_sdp }) => {
        clearTimeout(timeout);
        if (!result) {
          reject(new Error('Failed to get SDP answer'));
          return;
        }

        try {
          await pc.setRemoteDescription({ type: 'answer', sdp: answer_sdp });
          resolve();
        } catch (err) {
          reject(err);
        }
      });
    });
  }

  private setupSignalingHandlers(pc: RTCPeerConnection, video: HTMLVideoElement): void {
    if (!this.signaling) return;

    // Dispatch webrtc_connected event (P2)
    this.signaling.on('connected', () => {
      if (this.destroyed) return;
      video.dispatchEvent(new Event('webrtc_connected'));
    });

    this.signaling.on('time_update', (update: MistTimeUpdate) => {
      if (this.destroyed) return;
      this.handleTimeUpdate(update, video);
    });

    this.signaling.on('seeked', ({ live_point }) => {
      if (this.destroyed) return;
      // Dispatch seeked event
      video.dispatchEvent(new CustomEvent('seeked', { detail: { seekOffset: this.seekOffset } }));
      // Set playback rate to auto if seeked to live point
      if (live_point && this.signaling) {
        this.signaling.setSpeed('auto');
      }
      video.play().catch(() => {});
    });

    this.signaling.on('speed_changed', ({ play_rate_curr }) => {
      if (this.destroyed) return;
      this.playRate = play_rate_curr;
      video.dispatchEvent(new CustomEvent('ratechange', { detail: { play_rate_curr } }));
    });

    this.signaling.on('stopped', () => {
      if (this.destroyed) return;
      this.isLiveStream = false;
      video.pause();
      this.emit('ended', undefined);
    });

    this.signaling.on('error', ({ message }) => {
      if (this.destroyed) return;
      this.emit('error', message);
    });

    // Dispatch webrtc_disconnected event (P2)
    this.signaling.on('disconnected', () => {
      if (this.destroyed) return;
      video.dispatchEvent(new Event('webrtc_disconnected'));
      video.pause();
    });
  }

  private handleTimeUpdate(update: MistTimeUpdate, video: HTMLVideoElement): void {
    // Update seek offset
    this.seekOffset = update.current / 1000 - video.currentTime;

    // Update duration
    const newDuration = update.end === 0 ? Infinity : update.end;
    this.durationMs = newDuration;
    this.isLiveStream = !isFinite(newDuration) || newDuration === 0;

    // Track buffer window (P2)
    this.bufferWindow = update.end - update.begin;

    // Fire track changed events (P1)
    if (update.tracks && !this.arraysEqual(update.tracks, this.currentTracks)) {
      for (const trackId of update.tracks) {
        if (!this.currentTracks.includes(trackId)) {
          video.dispatchEvent(new CustomEvent('playerUpdate_trackChanged', {
            detail: { trackId }
          }));
        }
      }
      this.currentTracks = [...update.tracks];
    }

    // Resume playback if not paused on server
    if (!update.paused && video.paused) {
      video.play().catch(() => {});
    }
  }

  private async createAndSendOffer(pc: RTCPeerConnection): Promise<void> {
    if (!this.signaling) return;

    // Add transceivers for receiving
    pc.addTransceiver('video', { direction: 'recvonly' });
    pc.addTransceiver('audio', { direction: 'recvonly' });

    const offer = await pc.createOffer({
      offerToReceiveAudio: true,
      offerToReceiveVideo: true,
    });

    await pc.setLocalDescription(offer);

    if (offer.sdp) {
      this.signaling.sendOfferSDP(offer.sdp);
    }
  }
}

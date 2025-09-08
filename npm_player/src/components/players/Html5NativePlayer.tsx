import React, { useEffect, useRef } from 'react';
import { BasePlayer, StreamSource, StreamInfo, PlayerOptions, PlayerCapability } from '../../core/PlayerInterface';
import { checkProtocolMismatch, getBrowserInfo } from '../../core/detector';

// React component interface
type Props = {
  src: string;
  muted?: boolean;
  autoPlay?: boolean;
  controls?: boolean;
  poster?: string;
  onError?: (e: Error) => void;
};

// Player implementation class
export class Html5NativePlayerImpl extends BasePlayer {
  readonly capability: PlayerCapability = {
    name: "HTML5 Native Player",
    shortname: "html5",
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
      "whep"
    ]
  };

  private peerConnection: RTCPeerConnection | null = null;
  private sessionUrl: string | null = null;
  private lastInboundStats: any = null;
  private reconnectEnabled = false;
  private reconnectAttempts = 0;
  private maxReconnectAttempts = 3;
  private reconnectTimer: any = null;
  private currentWhepUrl: string | null = null;
  private currentHeaders: Record<string,string> | null = null;
  private currentIceServers: RTCIceServer[] | null = null;
  private container: HTMLElement | null = null;

  isMimeSupported(mimetype: string): boolean {
    return this.capability.mimes.indexOf(mimetype) !== -1;
  }

  isBrowserSupported(mimetype: string, source: StreamSource, streamInfo: StreamInfo): boolean | string[] {
    if (mimetype === 'whep') {
      if (!('RTCPeerConnection' in window) || !('fetch' in window)) return false;
      const playable: string[] = [];
      for (const track of streamInfo.meta.tracks) {
        if (track.type === 'video' || track.type === 'audio') playable.push(track.type);
      }
      return playable.length ? playable : ['video', 'audio'];
    }
    // Check protocol mismatch
    if (checkProtocolMismatch(source.url)) {
      const browser = getBrowserInfo();
      // Allow file:// -> http:// but warn
      if (!(window.location.protocol === 'file:' && source.url.startsWith('http:'))) {
        return false;
      }
    }

    const browser = getBrowserInfo();
    
    // Special handling for HLS
    if (mimetype === "html5/application/vnd.apple.mpegurl") {
      // Check for native HLS support
      const testVideo = document.createElement('video');
      if (testVideo.canPlayType('application/vnd.apple.mpegurl')) {
        // Prefer VideoJS for older Android
        const androidVersion = this.getAndroidVersion();
        if (androidVersion && androidVersion < 7) {
          return false; // Let VideoJS handle it
        }
        return ['video', 'audio'];
      }
      return false;
    }

    // Test codec support for regular media types
    const supportedTracks: string[] = [];
    const testVideo = document.createElement('video');
    
    // Extract the actual mime type from the format
    const shortMime = mimetype.replace('html5/', '');
    
    // For codec testing, we need to check against stream info
    const tracksByType: Record<string, typeof streamInfo.meta.tracks> = {};
    for (const track of streamInfo.meta.tracks) {
      if (track.type === 'meta') {
        if (track.codec === 'subtitle') {
          supportedTracks.push('subtitle');
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
        let codecString = '';
        if (track.codecstring) {
          codecString = track.codecstring;
        } else {
          codecString = this.translateCodecForHtml5(track);
        }
        
        const testMimeType = `${shortMime};codecs="${codecString}"`;
        
        // Special handling for WebM - Chrome reports issues with codec strings
        if (shortMime === 'video/webm') {
          if (testVideo.canPlayType(shortMime) !== '') {
            hasPlayableTrack = true;
            break;
          }
        } else {
          if (testVideo.canPlayType(testMimeType) !== '') {
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

  private translateCodecForHtml5(track: { codec: string; codecstring?: string; init?: string }): string {
    if (track.codecstring) return track.codecstring;
    
    const bin2hex = (index: number) => {
      if (!track.init || index >= track.init.length) return '00';
      return ('0' + track.init.charCodeAt(index).toString(16)).slice(-2);
    };

    switch (track.codec) {
      case 'AAC':
        return 'mp4a.40.2';
      case 'MP3':
        return 'mp4a.40.34';
      case 'AC3':
        return 'ec-3';
      case 'H264':
        return `avc1.${bin2hex(1)}${bin2hex(2)}${bin2hex(3)}`;
      case 'HEVC':
        return `hev1.${bin2hex(1)}${bin2hex(6)}${bin2hex(7)}${bin2hex(8)}${bin2hex(9)}${bin2hex(10)}${bin2hex(11)}${bin2hex(12)}`;
      case 'VP8':
        return 'vp8';
      case 'VP9':
        return 'vp09.00.10.08';
      case 'AV1':
        return 'av01.0.04M.08';
      case 'Opus':
        return 'opus';
      default:
        return track.codec.toLowerCase();
    }
  }

  private getAndroidVersion(): number | null {
    const match = navigator.userAgent.match(/Android (\d+)(?:\.(\d+))?(?:\.(\d+))*/i);
    if (!match) return null;
    
    const major = parseInt(match[1], 10);
    const minor = match[2] ? parseInt(match[2], 10) : 0;
    
    return major + (minor / 10);
  }

  async initialize(container: HTMLElement, source: StreamSource, options: PlayerOptions): Promise<HTMLVideoElement> {
    this.container = container;
    
    // Create video element
    const video = document.createElement('video');
    video.style.width = '100%';
    video.style.height = '100%';
    video.setAttribute('playsinline', '');
    video.setAttribute('crossorigin', 'anonymous');
    
    // Apply options
    if (options.autoplay) video.autoplay = true;
    if (options.muted) video.muted = true;
    if (options.controls) video.controls = true;
    if (options.loop) video.loop = true;
    if (options.poster) video.poster = options.poster;

    this.videoElement = video;
    container.appendChild(video);

    // Set up event listeners
    this.setupVideoEventListeners(video, options);

    // Optional subtitle tracks helper from source extras
    try {
      const subs = (source as any).subtitles as Array<{ label: string; lang: string; src: string }>;
      if (Array.isArray(subs)) {
        subs.forEach((s, idx) => {
          const track = document.createElement('track');
          track.kind = 'subtitles';
          track.label = s.label;
          track.srclang = s.lang;
          track.src = s.src;
          if (idx === 0) track.default = true;
          video.appendChild(track);
        });
      }
    } catch {}

    try {
      if (source.type === 'whep') {
        // Read optional settings from source
        const s: any = source as any;
        const headers = (s && s.headers) ? (s.headers as Record<string,string>) : {};
        const iceServers = (s && s.iceServers) ? (s.iceServers as RTCIceServer[]) : [];
        this.reconnectEnabled = !!(s && s.reconnect);
        this.currentWhepUrl = source.url;
        this.currentHeaders = headers;
        this.currentIceServers = iceServers;
        await this.startWhep(video, source.url, headers, iceServers);
        return video;
      } else {
        // Set the source for direct HTML5 playback
        video.src = source.url;
        if (options.autoplay) {
          video.play().catch(e => console.warn('HTML5 autoplay failed:', e));
        }
        return video;
      }
      
    } catch (error: any) {
      this.emit('error', error.message || String(error));
      throw error;
    }
  }

  destroy(): void {
    if (this.reconnectTimer) {
      try { clearTimeout(this.reconnectTimer); } catch {}
      this.reconnectTimer = null;
    }
    if (this.sessionUrl) {
      try { fetch(this.sessionUrl, { method: 'DELETE' }).catch(() => {}); } catch {}
      this.sessionUrl = null;
    }
    if (this.peerConnection) {
      try { this.peerConnection.close(); } catch {}
      this.peerConnection = null;
    }
    if (this.videoElement) {
      try { (this.videoElement as any).srcObject = null; } catch {}
      this.videoElement.pause();
      this.videoElement.src = '';
      this.videoElement.load(); // Reset the video element
      
      if (this.container) {
        this.container.removeChild(this.videoElement);
      }
    }

    this.videoElement = null;
    this.container = null;
    this.listeners.clear();
  }

  async getStats(): Promise<any> {
    if (!this.peerConnection) return undefined;
    try {
      const stats = await this.peerConnection.getStats();
      const out: any = {};
      stats.forEach((report) => {
        if (report.type === 'inbound-rtp') {
          if (report.kind === 'video') out.video = report;
          if (report.kind === 'audio') out.audio = report;
        }
        if (report.type === 'candidate-pair' && report.nominated) {
          out.network = report;
        }
      });
      this.lastInboundStats = out;
      return out;
    } catch {
      return undefined;
    }
  }

  async getLatency(): Promise<any> {
    // Rough estimate from RTCP timestamps if available
    const s = await this.getStats();
    if (!s || !s.video) return undefined;
    const est = s.video.jitterBufferDelay && s.video.jitterBufferEmittedCount
      ? (s.video.jitterBufferDelay / Math.max(1, s.video.jitterBufferEmittedCount))
      : undefined;
    return { estimatedSeconds: est };
  }

  private async startWhep(video: HTMLVideoElement, url: string, headers: Record<string,string>, iceServers: RTCIceServer[]) {
    // Clean previous sessionUrl
    if (this.sessionUrl) {
      try { fetch(this.sessionUrl, { method: 'DELETE' }).catch(() => {}); } catch {}
      this.sessionUrl = null;
    }

    // Create peer connection
    const pc = new RTCPeerConnection({ iceServers });
    this.peerConnection = pc;

    pc.ontrack = (event: RTCTrackEvent) => {
      if (video && event.streams[0]) {
        video.srcObject = event.streams[0];
      }
    };

    pc.oniceconnectionstatechange = () => {
      const state = pc.iceConnectionState;
      if (state === 'failed' || state === 'disconnected') {
        this.emit('error', 'WHEP connection failed');
        if (this.reconnectEnabled && this.reconnectAttempts < this.maxReconnectAttempts && this.currentWhepUrl) {
          const backoff = Math.min(5000, 500 * Math.pow(2, this.reconnectAttempts));
          this.reconnectAttempts++;
          this.reconnectTimer = setTimeout(() => {
            this.startWhep(video, this.currentWhepUrl!, this.currentHeaders || {}, this.currentIceServers || []);
          }, backoff);
        }
      }
      if (state === 'connected') {
        this.reconnectAttempts = 0;
      }
    };

    pc.addTransceiver('video', { direction: 'recvonly' });
    pc.addTransceiver('audio', { direction: 'recvonly' });

    const offer = await pc.createOffer();
    await pc.setLocalDescription(offer);

    const requestHeaders: Record<string,string> = { 'Content-Type': 'application/sdp' };
    for (const k in headers) requestHeaders[k] = headers[k];

    const response = await fetch(url, {
      method: 'POST',
      headers: requestHeaders,
      body: offer.sdp || ''
    });
    if (!response.ok) {
      throw new Error(`WHEP request failed: ${response.status}`);
    }
    const answerSdp = await response.text();
    await pc.setRemoteDescription(new RTCSessionDescription({ type: 'answer', sdp: answerSdp }));
    this.sessionUrl = response.headers.get('Location');
  }
}

// React component wrapper
const Html5NativePlayer: React.FC<Props> = ({ 
  src, 
  muted = true, 
  autoPlay = true, 
  controls = true, 
  poster,
  onError 
}) => {
  const videoRef = useRef<HTMLVideoElement>(null);

  useEffect(() => {
    const video = videoRef.current;
    if (!video) return;
    
    video.muted = muted;
    video.src = src;
    if (poster) video.poster = poster;
    
    const handleError = () => {
      if (onError && video.error) {
        onError(new Error(`HTML5 video error: ${video.error.message}`));
      }
    };
    
    video.addEventListener('error', handleError);
    
    return () => {
      video.removeEventListener('error', handleError);
    };
  }, [src, muted, poster, onError]);

  return (
    <video
      ref={videoRef}
      autoPlay={autoPlay}
      muted={muted}
      controls={controls}
      playsInline
      crossOrigin="anonymous"
      style={{ width: '100%', height: '100%' }}
    />
  );
};

export default Html5NativePlayer;
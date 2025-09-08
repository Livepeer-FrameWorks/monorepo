import React, { useEffect, useRef } from 'react';
import { BasePlayer, StreamSource, StreamInfo, PlayerOptions, PlayerCapability } from '../../core/PlayerInterface';
import { translateCodec, checkProtocolMismatch, getBrowserInfo } from '../../core/detector';

// React component interface
type Props = {
  src: string;
  muted?: boolean;
  autoPlay?: boolean;
  controls?: boolean;
  onError?: (e: Error) => void;
};

// Player implementation class
export class HlsJsPlayerImpl extends BasePlayer {
  readonly capability: PlayerCapability = {
    name: "HLS.js Player",
    shortname: "hlsjs",
    priority: 3,
    mimes: ["html5/application/vnd.apple.mpegurl", "html5/application/vnd.apple.mpegurl;version=7"]
  };

  private hls: any = null;
  private container: HTMLElement | null = null;
  private failureCount = 0;

  isMimeSupported(mimetype: string): boolean {
    return this.capability.mimes.includes(mimetype);
  }

  isBrowserSupported(mimetype: string, source: StreamSource, streamInfo: StreamInfo): boolean | string[] {
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
      const testVideo = document.createElement('video');
      if (testVideo.canPlayType('application/vnd.apple.mpegurl')) {
        return ['video', 'audio'];
      }
      return false;
    }

    // Check codec compatibility
    const playableTracks: string[] = [];
    const tracksByType: Record<string, typeof streamInfo.meta.tracks> = {};

    // Group tracks by type
    for (const track of streamInfo.meta.tracks) {
      if (track.type === 'meta') {
        if (track.codec === 'subtitle') {
          // Check for WebVTT subtitle support
          for (const src of streamInfo.source) {
            if (src.type === 'html5/text/vtt') {
              playableTracks.push('subtitle');
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

    // Test codec support for video/audio tracks
    for (const [trackType, tracks] of Object.entries(tracksByType)) {
      let hasPlayableTrack = false;
      
      for (const track of tracks) {
        const codecString = translateCodec(track);
        const mimeType = `video/mp4;codecs="${codecString}"`;
        
        if (MediaSource.isTypeSupported && MediaSource.isTypeSupported(mimeType)) {
          hasPlayableTrack = true;
          break;
        }
      }
      
      if (hasPlayableTrack) {
        playableTracks.push(trackType);
      }
    }

    return playableTracks.length > 0 ? playableTracks : false;
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

    try {
      // Dynamic import of HLS.js
      const mod = await import('hls.js');
      const Hls = (mod as any).default || (mod as any);
      
      if (Hls.isSupported()) {
        this.hls = new Hls({
          enableWorker: false,
          lowLatencyMode: true,
          maxBufferLength: 15,
          maxMaxBufferLength: 60,
          backBufferLength: 90
        });
        
        this.hls.attachMedia(video);
        
        this.hls.on(Hls.Events.MEDIA_ATTACHED, () => {
          this.hls.loadSource(source.url);
        });
        
        this.hls.on(Hls.Events.ERROR, (_: any, data: any) => {
          if (data?.fatal) {
            if (this.failureCount < 3) {
              this.failureCount++;
              try { this.hls.recoverMediaError(); } catch {}
            } else {
              const error = `HLS fatal error: ${data?.type || 'unknown'}`;
              this.emit('error', error);
            }
          }
        });
        
        this.hls.on(Hls.Events.MANIFEST_PARSED, () => {
          if (options.autoplay) {
            video.play().catch(e => console.warn('HLS autoplay failed:', e));
          }
        });
        
      } else if (video.canPlayType('application/vnd.apple.mpegurl')) {
        // Use native HLS support
        video.src = source.url;
        if (options.autoplay) {
          video.play().catch(e => console.warn('Native HLS autoplay failed:', e));
        }
      } else {
        throw new Error('HLS not supported in this browser');
      }

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

      return video;
      
    } catch (error: any) {
      this.emit('error', error.message || String(error));
      throw error;
    }
  }

  destroy(): void {
    if (this.hls) {
      try {
        this.hls.destroy();
      } catch (e) {
        console.warn('Error destroying HLS.js:', e);
      }
      this.hls = null;
    }

    if (this.videoElement && this.container) {
      this.container.removeChild(this.videoElement);
    }

    this.videoElement = null;
    this.container = null;
    this.listeners.clear();
  }

  // Quality API (Auto + levels)
  getQualities(): Array<{ id: string; label: string; bitrate?: number; width?: number; height?: number; isAuto?: boolean; active?: boolean }> {
    const qualities: any[] = [];
    const video = this.videoElement;
    if (!this.hls || !video) return qualities;
    const levels = this.hls.levels || [];
    const auto = { id: 'auto', label: 'Auto', isAuto: true, active: this.hls.autoLevelEnabled };
    qualities.push(auto);
    levels.forEach((lvl: any, idx: number) => {
      qualities.push({ id: String(idx), label: lvl.height ? `${lvl.height}p` : `${Math.round((lvl.bitrate||0)/1000)}kbps`, bitrate: lvl.bitrate, width: lvl.width, height: lvl.height, active: this.hls.currentLevel === idx });
    });
    return qualities;
  }

  selectQuality(id: string): void {
    if (!this.hls) return;
    if (id === 'auto') {
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
      out.push({ id: String(i), label: tt.label || `CC ${i+1}`, lang: (tt as any).language, active: tt.mode === 'showing' });
    }
    return out;
  }

  selectTextTrack(id: string | null): void {
    const v = this.videoElement as any;
    if (!v) return;
    const list = v.textTracks as TextTrackList;
    for (let i = 0; i < list.length; i++) {
      const tt = list[i];
      if (id !== null && String(i) === id) tt.mode = 'showing'; else tt.mode = 'disabled';
    }
  }
}

// React component wrapper
const HlsJsPlayer: React.FC<Props> = ({ src, muted = true, autoPlay = true, controls = true, onError }) => {
  const videoRef = useRef<HTMLVideoElement>(null);

  useEffect(() => {
    let hls: any;
    const el = videoRef.current;
    if (!el) return;
    el.muted = muted;

    const setup = async () => {
      try {
        const mod = await import('hls.js');
        const Hls = (mod as any).default || (mod as any);
        if (Hls.isSupported()) {
          hls = new Hls();
          hls.attachMedia(el);
          hls.on(Hls.Events.MEDIA_ATTACHED, () => {
            hls.loadSource(src);
          });
          hls.on(Hls.Events.ERROR, (_: any, data: any) => {
            if (data?.fatal && onError) onError(new Error(`HLS fatal error: ${data?.type || 'unknown'}`));
          });
        } else if (el.canPlayType('application/vnd.apple.mpegurl')) {
          el.src = src;
        } else {
          if (onError) onError(new Error('HLS not supported'));
        }
      } catch (e: any) {
        if (onError) onError(e);
      }
    };
    setup();

    return () => {
      try { if (hls && hls.destroy) hls.destroy(); } catch {}
    };
  }, [src, muted, onError]);

  return (
    <video ref={videoRef} autoPlay={autoPlay} muted={muted} controls={controls} playsInline style={{ width: '100%', height: '100%' }} />
  );
};

export default HlsJsPlayer;



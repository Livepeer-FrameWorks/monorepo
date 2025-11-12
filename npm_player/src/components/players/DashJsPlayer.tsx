import React, { useEffect, useRef } from 'react';
import { BasePlayer, StreamSource, StreamInfo, PlayerOptions, PlayerCapability } from '../../core/PlayerInterface';
import { translateCodec, checkProtocolMismatch, getBrowserInfo, isFileProtocol } from '../../core/detector';

// React component interface
type Props = {
  src: string;
  muted?: boolean;
  autoPlay?: boolean;
  controls?: boolean;
  onError?: (e: Error) => void;
};

// Player implementation class
export class DashJsPlayerImpl extends BasePlayer {
  readonly capability: PlayerCapability = {
    name: "Dash.js Player",
    shortname: "dashjs",
    priority: 4,
    mimes: ["dash/video/mp4"]
  };

  private dashPlayer: any = null;
  private container: HTMLElement | null = null;

  isMimeSupported(mimetype: string): boolean {
    return this.capability.mimes.includes(mimetype);
  }

  isBrowserSupported(mimetype: string, source: StreamSource, streamInfo: StreamInfo): boolean | string[] {
    // Check protocol mismatch
    if (checkProtocolMismatch(source.url)) {
      return false;
    }

    // Don't use DASH.js if loaded via file://
    if (isFileProtocol()) {
      return false;
    }

    const browser = getBrowserInfo();
    
    // Check MediaSource support (required for DASH.js)
    if (!browser.supportsMediaSource) {
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
    container.classList.add('fw-player-container');
    
    // Create video element
    const video = document.createElement('video');
    video.classList.add('fw-player-video');
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
      // Dynamic import of DASH.js
      const mod = await import('dashjs');
      const dashjs = (mod as any).default || (mod as any);
      
      this.dashPlayer = dashjs.MediaPlayer().create();
      
      this.dashPlayer.on('error', (e: any) => {
        const error = `DASH error: ${e?.event?.message || e?.message || 'unknown'}`;
        this.emit('error', error);
      });

      this.dashPlayer.on('playbackInitialized', () => {
        if (options.autoplay) {
          video.play().catch(e => console.warn('DASH autoplay failed:', e));
        }
      });
      
      // Configure DASH.js settings
      this.dashPlayer.updateSettings({
        streaming: {
          enableLowLatency: true,
          liveDelayFragmentCount: 2,
          bufferTimeAtTopQuality: 30,
          bufferTimeAtTopQualityLongForm: 60,
          abr: {
            autoSwitchBitrate: { video: true }
          }
        }
      });

      this.dashPlayer.initialize(video, source.url, options.autoplay);

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
    if (this.dashPlayer) {
      try {
        this.dashPlayer.reset();
      } catch (e) {
        console.warn('Error destroying DASH.js:', e);
      }
      this.dashPlayer = null;
    }

    if (this.videoElement && this.container) {
      this.container.removeChild(this.videoElement);
    }

    this.videoElement = null;
    this.container = null;
    this.listeners.clear();
  }

  getQualities(): Array<{ id: string; label: string; bitrate?: number; width?: number; height?: number; isAuto?: boolean; active?: boolean }> {
    const out: any[] = [];
    const v = this.videoElement;
    if (!this.dashPlayer || !v) return out;
    // dashjs reports bitrates via getBitrateInfoListFor('video')
    try {
      const infos = this.dashPlayer.getBitrateInfoListFor('video') || [];
      out.push({ id: 'auto', label: 'Auto', isAuto: true, active: this.dashPlayer.getSettings().streaming.abr.autoSwitchBitrate.video === true });
      infos.forEach((info: any, i: number) => {
        out.push({ id: String(i), label: info.height ? `${info.height}p` : `${Math.round((info.bitrate||0))}kbps`, bitrate: info.bitrate, width: info.width, height: info.height });
      });
    } catch {}
    return out;
  }

  selectQuality(id: string): void {
    if (!this.dashPlayer) return;
    if (id === 'auto') {
      this.dashPlayer.updateSettings({ streaming: { abr: { autoSwitchBitrate: { video: true } } } });
      return;
    }
    const idx = parseInt(id, 10);
    if (!isNaN(idx)) {
      this.dashPlayer.updateSettings({ streaming: { abr: { autoSwitchBitrate: { video: false } } } });
      try { this.dashPlayer.setQualityFor('video', idx); } catch {}
    }
  }

  // Captions via native text tracks or dash.js API
  getTextTracks(): Array<{ id: string; label: string; lang?: string; active: boolean }> {
    const v = this.videoElement;
    if (!this.dashPlayer || !v) return [];
    const out: any[] = [];
    try {
      const textTracks = (v.textTracks || []) as any;
      for (let i = 0; i < textTracks.length; i++) {
        const tt = textTracks[i];
        out.push({ id: String(i), label: tt.label || `CC ${i+1}`, lang: (tt as any).language, active: tt.mode === 'showing' });
      }
    } catch {}
    return out;
  }

  selectTextTrack(id: string | null): void {
    const v = this.videoElement;
    if (!this.dashPlayer || !v) return;
    const list = v.textTracks as TextTrackList;
    for (let i = 0; i < list.length; i++) {
      const tt = list[i];
      if (id !== null && String(i) === id) tt.mode = 'showing'; else tt.mode = 'disabled';
    }
  }
}

// React component wrapper
const DashJsPlayer: React.FC<Props> = ({ src, muted = true, autoPlay = true, controls = true, onError }) => {
  const videoRef = useRef<HTMLVideoElement>(null);

  useEffect(() => {
    let player: any;
    const el = videoRef.current;
    if (!el) return;
    el.muted = muted;

    const setup = async () => {
      try {
        const mod = await import('dashjs');
        const dashjs = (mod as any).default || (mod as any);
        player = dashjs.MediaPlayer().create();
        player.initialize(el, src, autoPlay);
        player.on('error', (e: any) => {
          if (onError) onError(new Error(`DASH error: ${e?.event?.message || 'unknown'}`));
        });
      } catch (e: any) {
        if (onError) onError(e);
      }
    };
    setup();

    return () => {
      try { if (player && player.reset) player.reset(); } catch {}
    };
  }, [src, muted, autoPlay, onError]);

  return (
    <video ref={videoRef} autoPlay={autoPlay} muted={muted} controls={controls} playsInline className="fw-player-video" />
  );
};

export default DashJsPlayer;

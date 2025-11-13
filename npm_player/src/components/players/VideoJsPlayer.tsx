import React, { useEffect, useRef } from 'react';
import { BasePlayer, StreamSource, StreamInfo, PlayerOptions, PlayerCapability } from '../../core/PlayerInterface';

type Props = {
  src: string;
  type?: string; // e.g., 'application/x-mpegURL' for HLS
  muted?: boolean;
  autoPlay?: boolean;
  controls?: boolean;
  onError?: (e: Error) => void;
};

const VideoJsPlayer: React.FC<Props> = ({ src, type = 'application/x-mpegURL', muted = true, autoPlay = true, controls = true, onError }) => {
  const videoRef = useRef<HTMLVideoElement>(null);
  const playerRef = useRef<any>(null);

  useEffect(() => {
    const el = videoRef.current as any;
    if (!el) return;
    el.muted = muted;

    const setup = async () => {
      try {
        const mod = await import('video.js');
        const videojs = (mod as any).default || (mod as any);
        playerRef.current = videojs(el, { autoplay: autoPlay, controls, muted, sources: [{ src, type }] });
        playerRef.current.on('error', () => {
          const err = playerRef.current?.error();
          if (onError) onError(new Error(err?.message || 'video.js error'));
        });
      } catch (e: any) {
        if (onError) onError(e);
      }
    };
    setup();

    return () => {
      try { playerRef.current && playerRef.current.dispose && playerRef.current.dispose(); } catch {}
    };
  }, [src, type, muted, autoPlay, controls, onError]);

  return (
    <video ref={videoRef} className="fw-video-js vjs-default-skin fw-player-video" playsInline />
  );
};

// Player implementation class
export class VideoJsPlayerImpl extends BasePlayer {
  readonly capability: PlayerCapability = {
    name: "Video.js Player",
    shortname: "videojs",
    priority: 2,
    mimes: [
      "html5/application/vnd.apple.mpegurl",
      "html5/application/vnd.apple.mpegurl;version=7",
      "dash/video/mp4",
      "html5/video/mp4",
      "html5/video/webm"
    ]
  };

  private videojsPlayer: any = null;
  private container: HTMLElement | null = null;

  isMimeSupported(mimetype: string): boolean {
    return this.capability.mimes.includes(mimetype);
  }

  isBrowserSupported(mimetype: string, source: StreamSource, streamInfo: StreamInfo): boolean | string[] {
    // VideoJS is very compatible and handles most formats well
    const playableTracks: string[] = [];
    
    for (const track of streamInfo.meta.tracks) {
      if (track.type === 'meta') {
        if (track.codec === 'subtitle') {
          playableTracks.push('subtitle');
        }
        continue;
      }
      playableTracks.push(track.type);
    }

    return playableTracks.length > 0 ? playableTracks : ['video', 'audio'];
  }

  async initialize(container: HTMLElement, source: StreamSource, options: PlayerOptions): Promise<HTMLVideoElement> {
    this.container = container;
    container.classList.add('fw-player-container');
    
    const video = document.createElement('video');
    video.classList.add('fw-player-video');
    video.setAttribute('playsinline', '');
    video.setAttribute('crossorigin', 'anonymous');
    video.className = 'video-js vjs-default-skin fw-player-video';
    
    if (options.autoplay) video.autoplay = true;
    if (options.muted) video.muted = true;
    if (options.controls) video.controls = true;
    if (options.loop) video.loop = true;
    if (options.poster) video.poster = options.poster;

    this.videoElement = video;
    container.appendChild(video);

    this.setupVideoEventListeners(video, options);

    try {
      const mod = await import('video.js');
      const videojs = (mod as any).default || (mod as any);
      
      this.videojsPlayer = videojs(video, {
        autoplay: options.autoplay,
        controls: options.controls,
        muted: options.muted,
        sources: [{ src: source.url, type: this.getVideoJsType(source.type) }]
      });

      this.videojsPlayer.on('error', () => {
        const err = this.videojsPlayer?.error();
        this.emit('error', err?.message || 'VideoJS playback error');
      });

      return video;
      
    } catch (error: any) {
      this.emit('error', error.message || String(error));
      throw error;
    }
  }

  private getVideoJsType(mimeType?: string): string {
    if (!mimeType) return 'application/x-mpegURL';
    
    // Convert our mime types to VideoJS types
    if (mimeType.includes('mpegurl')) return 'application/x-mpegURL';
    if (mimeType.includes('dash')) return 'application/dash+xml';
    if (mimeType.includes('mp4')) return 'video/mp4';
    if (mimeType.includes('webm')) return 'video/webm';
    
    return mimeType.replace('html5/', '');
  }

  // Optional helper to inject default skin CSS when controls are enabled
  private ensureSkinCss() {
    if (!document.getElementById('videojs-css')) {
      const link = document.createElement('link');
      link.rel = 'stylesheet';
      link.href = 'https://vjs.zencdn.net/8.10.0/video-js.css';
      link.id = 'videojs-css';
      document.head.appendChild(link);
    }
  }

  setPlaybackRate(rate: number): void {
    super.setPlaybackRate(rate);
    try {
      if (this.videojsPlayer) this.videojsPlayer.playbackRate(rate);
    } catch {}
  }

  destroy(): void {
    if (this.videojsPlayer) {
      try {
        this.videojsPlayer.dispose();
      } catch (e) {
        console.warn('Error disposing VideoJS:', e);
      }
      this.videojsPlayer = null;
    }

    if (this.videoElement && this.container) {
      this.container.removeChild(this.videoElement);
    }

    this.videoElement = null;
    this.container = null;
    this.listeners.clear();
  }
}

export default VideoJsPlayer;

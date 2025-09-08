/**
 * Common Player Interface
 * 
 * All player implementations must implement this interface to ensure
 * consistent behavior and enable the PlayerManager selection system
 */

export interface StreamSource {
  url: string;
  type: string;
  index?: number;
  streamName?: string;
  mistPlayerUrl?: string;
}

export interface StreamTrack {
  type: 'video' | 'audio' | 'meta';
  codec: string;
  codecstring?: string;
  init?: string;
}

export interface StreamInfo {
  source: StreamSource[];
  meta: {
    tracks: StreamTrack[];
  };
  type?: 'live' | 'vod';
}

export interface PlayerOptions {
  autoplay?: boolean;
  muted?: boolean;
  controls?: boolean;
  loop?: boolean;
  poster?: string;
  width?: number;
  height?: number;
  onReady?: (element: HTMLVideoElement) => void;
  onError?: (error: string | Error) => void;
  onPlay?: () => void;
  onPause?: () => void;
  onEnded?: () => void;
  onTimeUpdate?: (currentTime: number) => void;
}

export interface PlayerCapability {
  /** Player name for display */
  name: string;
  /** Unique identifier */
  shortname: string;
  /** Priority (lower number = higher priority) */
  priority: number;
  /** MIME types this player can handle */
  mimes: string[];
}

export interface PlayerEvents {
  ready: HTMLVideoElement;
  error: string | Error;
  play: void;
  pause: void;
  ended: void;
  timeupdate: number;
}

/**
 * Base interface all players must implement
 */
export interface IPlayer {
  /** Player metadata */
  readonly capability: PlayerCapability;
  
  /**
   * Check if this player supports the given MIME type
   */
  isMimeSupported(mimetype: string): boolean;
  
  /**
   * Check if this player can play in the current browser environment
   * @param mimetype - MIME type to test
   * @param source - Source information
   * @param streamInfo - Stream metadata
   * @returns false if not supported, true if supported (no track info), 
   *          or array of supported track types
   */
  isBrowserSupported(
    mimetype: string, 
    source: StreamSource, 
    streamInfo: StreamInfo
  ): boolean | string[];
  
  /**
   * Initialize the player with given source and options
   * @param container - Container element to render in
   * @param source - Source to play
   * @param options - Player options
   * @returns Promise resolving to video element
   */
  initialize(
    container: HTMLElement,
    source: StreamSource,
    options: PlayerOptions
  ): Promise<HTMLVideoElement>;
  
  /**
   * Clean up and destroy the player
   */
  destroy(): void;
  
  /**
   * Get the underlying video element (if available)
   */
  getVideoElement(): HTMLVideoElement | null;
  
  /**
   * Set video size
   */
  setSize?(width: number, height: number): void;
  
  /**
   * Add event listener
   */
  on<K extends keyof PlayerEvents>(event: K, listener: (data: PlayerEvents[K]) => void): void;
  
  /**
   * Remove event listener
   */
  off<K extends keyof PlayerEvents>(event: K, listener: (data: PlayerEvents[K]) => void): void;
  
  /**
   * Get current playback state
   */
  getCurrentTime?(): number;
  getDuration?(): number;
  isPaused?(): boolean;
  isMuted?(): boolean;
  
  /**
   * Control playback
   */
  play?(): Promise<void>;
  pause?(): void;
  seek?(time: number): void;
  setVolume?(volume: number): void;
  setMuted?(muted: boolean): void;
  setPlaybackRate?(rate: number): void;

  // Optional: captions/text tracks
  getTextTracks?(): Array<{ id: string; label: string; lang?: string; active: boolean }>;
  selectTextTrack?(id: string | null): void;

  // Optional: quality/level selection
  getQualities?(): Array<{ id: string; label: string; bitrate?: number; width?: number; height?: number; isAuto?: boolean; active?: boolean }>;
  selectQuality?(id: string): void; // use 'auto' to enable ABR

  // Optional: live edge helpers
  isLive?(): boolean;
  jumpToLive?(): void;

  // Optional: PiP
  requestPiP?(): Promise<void>;

  /**
   * Optional: Retrieve player-specific stats (e.g., WebRTC inbound-rtp)
   */
  getStats?(): Promise<any>;

  /**
   * Optional: Retrieve approximate playback latency stats
   */
  getLatency?(): Promise<any>;
}

/**
 * Base class providing common functionality
 */
export abstract class BasePlayer implements IPlayer {
  abstract readonly capability: PlayerCapability;
  
  protected listeners: Map<string, Set<Function>> = new Map();
  protected videoElement: HTMLVideoElement | null = null;
  
  abstract isMimeSupported(mimetype: string): boolean;
  abstract isBrowserSupported(mimetype: string, source: StreamSource, streamInfo: StreamInfo): boolean | string[];
  abstract initialize(container: HTMLElement, source: StreamSource, options: PlayerOptions): Promise<HTMLVideoElement>;
  abstract destroy(): void;
  
  getVideoElement(): HTMLVideoElement | null {
    return this.videoElement;
  }
  
  on<K extends keyof PlayerEvents>(event: K, listener: (data: PlayerEvents[K]) => void): void {
    if (!this.listeners.has(event)) {
      this.listeners.set(event, new Set());
    }
    this.listeners.get(event)!.add(listener);
  }
  
  off<K extends keyof PlayerEvents>(event: K, listener: (data: PlayerEvents[K]) => void): void {
    const eventListeners = this.listeners.get(event);
    if (eventListeners) {
      eventListeners.delete(listener);
    }
  }
  
  protected emit<K extends keyof PlayerEvents>(event: K, data: PlayerEvents[K]): void {
    const eventListeners = this.listeners.get(event);
    if (eventListeners) {
      eventListeners.forEach(listener => {
        try {
          listener(data);
        } catch (e) {
          console.error(`Error in ${event} listener:`, e);
        }
      });
    }
  }
  
  protected setupVideoEventListeners(video: HTMLVideoElement, options: PlayerOptions): void {
    const handleEvent = (eventName: keyof PlayerEvents, handler: () => void) => {
      const listener = () => {
        handler();
        this.emit(eventName as any, undefined as any);
      };
      video.addEventListener(eventName, listener);
    };
    
    if (options.onReady) {
      options.onReady(video);
    }
    this.emit('ready', video);
    
    handleEvent('play', () => options.onPlay?.());
    handleEvent('pause', () => options.onPause?.());
    handleEvent('ended', () => options.onEnded?.());
    
    video.addEventListener('timeupdate', () => {
      const currentTime = video.currentTime;
      options.onTimeUpdate?.(currentTime);
      this.emit('timeupdate', currentTime);
    });
    
    video.addEventListener('error', () => {
      const error = video.error ? 
        `Video error: ${video.error.message}` : 
        'Unknown video error';
      options.onError?.(error);
      this.emit('error', error);
    });
  }
  
  // Default implementations for optional methods
  getCurrentTime(): number {
    return this.videoElement?.currentTime || 0;
  }
  
  getDuration(): number {
    return this.videoElement?.duration || 0;
  }
  
  isPaused(): boolean {
    return this.videoElement?.paused ?? true;
  }
  
  isMuted(): boolean {
    return this.videoElement?.muted ?? false;
  }
  
  async play(): Promise<void> {
    if (this.videoElement) {
      return this.videoElement.play();
    }
  }
  
  pause(): void {
    this.videoElement?.pause();
  }
  
  seek(time: number): void {
    if (this.videoElement) {
      this.videoElement.currentTime = time;
    }
  }
  
  setVolume(volume: number): void {
    if (this.videoElement) {
      this.videoElement.volume = Math.max(0, Math.min(1, volume));
    }
  }
  
  setMuted(muted: boolean): void {
    if (this.videoElement) {
      this.videoElement.muted = muted;
    }
  }
  setPlaybackRate(rate: number): void {
    if (this.videoElement) {
      this.videoElement.playbackRate = rate;
    }
  }
  
  // Default captions/text tracks using native TextTrack API
  getTextTracks(): Array<{ id: string; label: string; lang?: string; active: boolean }> {
    const video = this.videoElement;
    if (!video || !video.textTracks) return [];
    const out: Array<{ id: string; label: string; lang?: string; active: boolean }> = [];
    const list = video.textTracks as any as TextTrackList;
    for (let i = 0; i < list.length; i++) {
      const tt = list[i];
      out.push({ id: String(i), label: tt.label || `CC ${i+1}`, lang: (tt as any).language, active: tt.mode === 'showing' });
    }
    return out;
  }

  selectTextTrack(id: string | null): void {
    const video = this.videoElement;
    if (!video || !video.textTracks) return;
    const list = video.textTracks as any as TextTrackList;
    for (let i = 0; i < list.length; i++) {
      const tt = list[i];
      if (id !== null && String(i) === id) {
        tt.mode = 'showing';
      } else {
        tt.mode = 'disabled';
      }
    }
  }

  // Default live helpers
  isLive(): boolean {
    const v = this.videoElement;
    if (!v) return false;
    return !isFinite(v.duration) || v.duration === Infinity;
  }

  jumpToLive(): void {
    const v = this.videoElement;
    if (!v) return;
    const seekable = v.seekable;
    if (seekable && seekable.length > 0) {
      try { v.currentTime = seekable.end(seekable.length - 1); } catch {}
    }
  }

  // Default PiP helper
  async requestPiP(): Promise<void> {
    const v: any = this.videoElement as any;
    if (!v) return;
    // Exit if already in PiP
    if (document.pictureInPictureElement === v) {
      try { await (document as any).exitPictureInPicture?.(); } catch {}
      return;
    }
    try {
      if (v.requestPictureInPicture) {
        await v.requestPictureInPicture();
      } else if (v.webkitSetPresentationMode) {
        v.webkitSetPresentationMode('picture-in-picture');
      }
    } catch {}
  }
  
  setSize(width: number, height: number): void {
    if (this.videoElement) {
      this.videoElement.style.width = `${width}px`;
      this.videoElement.style.height = `${height}px`;
    }
  }

  // Default optional stats methods
  async getStats(): Promise<any> {
    return undefined;
  }

  async getLatency(): Promise<any> {
    return undefined;
  }
}
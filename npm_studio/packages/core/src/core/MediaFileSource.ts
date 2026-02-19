/**
 * MediaFileSource
 *
 * Play a video/audio file as a source in the streaming pipeline.
 * Uses HTMLVideoElement.captureStream() to produce a MediaStream
 * that plugs into the existing compositor/mixer pipeline.
 */

import { TypedEventEmitter } from "./EventEmitter";

export interface MediaFileSourceEvents {
  loaded: { duration: number; hasVideo: boolean; hasAudio: boolean };
  playing: undefined;
  paused: undefined;
  ended: undefined;
  seeked: { time: number };
  timeupdate: { currentTime: number; duration: number };
  error: { message: string; error?: Error };
}

export class MediaFileSource extends TypedEventEmitter<MediaFileSourceEvents> {
  private video: HTMLVideoElement;
  private stream: MediaStream | null = null;
  private objectUrl: string | null = null;
  private _loop = false;
  private timeupdateInterval: ReturnType<typeof setInterval> | null = null;

  constructor() {
    super();
    this.video = document.createElement("video");
    this.video.muted = true; // Audio routes through mixer, not local speakers
    this.video.playsInline = true;
    this.video.preload = "auto";

    this.video.addEventListener("playing", () => {
      this.emit("playing", undefined as any);
      this.startTimeupdates();
    });

    this.video.addEventListener("pause", () => {
      this.emit("paused", undefined as any);
      this.stopTimeupdates();
    });

    this.video.addEventListener("ended", () => {
      this.emit("ended", undefined as any);
      this.stopTimeupdates();
    });

    this.video.addEventListener("seeked", () => {
      this.emit("seeked", { time: this.video.currentTime });
    });

    this.video.addEventListener("error", () => {
      const err = this.video.error;
      this.emit("error", {
        message: err?.message ?? "Media playback error",
      });
    });
  }

  /**
   * Load a local File object (video or audio).
   */
  async loadFile(file: File): Promise<void> {
    this.cleanup();
    this.objectUrl = URL.createObjectURL(file);
    this.video.src = this.objectUrl;
    await this.waitForLoaded();
  }

  /**
   * Load a remote URL (video or audio).
   */
  async loadUrl(url: string): Promise<void> {
    this.cleanup();
    this.video.src = url;
    this.video.crossOrigin = "anonymous";
    await this.waitForLoaded();
  }

  private waitForLoaded(): Promise<void> {
    return new Promise((resolve, reject) => {
      const onLoaded = () => {
        this.video.removeEventListener("loadedmetadata", onLoaded);
        this.video.removeEventListener("error", onError);

        // Create capture stream
        this.stream = (this.video as any).captureStream();

        this.emit("loaded", {
          duration: this.video.duration,
          hasVideo: this.video.videoWidth > 0,
          hasAudio: this.stream!.getAudioTracks().length > 0,
        });
        resolve();
      };

      const onError = () => {
        this.video.removeEventListener("loadedmetadata", onLoaded);
        this.video.removeEventListener("error", onError);
        reject(new Error(this.video.error?.message ?? "Failed to load media"));
      };

      this.video.addEventListener("loadedmetadata", onLoaded);
      this.video.addEventListener("error", onError);
    });
  }

  play(): void {
    this.video.play().catch((e) => {
      this.emit("error", { message: e.message, error: e });
    });
  }

  pause(): void {
    this.video.pause();
  }

  seek(timeSeconds: number): void {
    this.video.currentTime = Math.max(0, Math.min(timeSeconds, this.video.duration || 0));
  }

  stop(): void {
    this.video.pause();
    this.video.currentTime = 0;
    this.stopTimeupdates();
  }

  get isPlaying(): boolean {
    return !this.video.paused && !this.video.ended;
  }

  get currentTime(): number {
    return this.video.currentTime;
  }

  get duration(): number {
    return this.video.duration || 0;
  }

  get hasVideo(): boolean {
    return this.video.videoWidth > 0;
  }

  get hasAudio(): boolean {
    return this.stream ? this.stream.getAudioTracks().length > 0 : false;
  }

  get loop(): boolean {
    return this._loop;
  }

  set loop(value: boolean) {
    this._loop = value;
    this.video.loop = value;
  }

  getStream(): MediaStream | null {
    return this.stream;
  }

  private startTimeupdates(): void {
    this.stopTimeupdates();
    this.timeupdateInterval = setInterval(() => {
      if (this.isPlaying) {
        this.emit("timeupdate", {
          currentTime: this.video.currentTime,
          duration: this.video.duration,
        });
      }
    }, 250);
  }

  private stopTimeupdates(): void {
    if (this.timeupdateInterval !== null) {
      clearInterval(this.timeupdateInterval);
      this.timeupdateInterval = null;
    }
  }

  private cleanup(): void {
    this.stopTimeupdates();
    if (this.objectUrl) {
      URL.revokeObjectURL(this.objectUrl);
      this.objectUrl = null;
    }
    this.stream = null;
    this.video.removeAttribute("src");
    this.video.load(); // Reset element
  }

  destroy(): void {
    this.cleanup();
    this.video.pause();
    this.removeAllListeners();
  }
}

/**
 * AudioGainController - Web Audio API volume gain above 100%
 *
 * Uses GainNode to amplify audio beyond the native HTMLMediaElement volume range,
 * up to a configurable maximum (default 300%).
 *
 * Lazily initializes AudioContext on first use to comply with browser autoplay policies.
 */

export interface AudioGainConfig {
  maxGain?: number;
}

const DEFAULT_MAX_GAIN = 3.0;

// createMediaElementSource can only be called once per element — reuse across instances
const sourceNodeCache = new WeakMap<HTMLVideoElement, MediaElementAudioSourceNode>();

export class AudioGainController {
  private maxGain: number;
  private context: AudioContext | null = null;
  private gainNode: GainNode | null = null;
  private currentGain = 1.0;
  private attachedVideo: HTMLVideoElement | null = null;
  private isDestroyed = false;

  constructor(config: AudioGainConfig = {}) {
    this.maxGain = config.maxGain ?? DEFAULT_MAX_GAIN;
  }

  isSupported(): boolean {
    return (
      typeof AudioContext !== "undefined" ||
      typeof (window as any).webkitAudioContext !== "undefined"
    );
  }

  attach(video: HTMLVideoElement): void {
    if (this.isDestroyed) return;
    if (this.attachedVideo === video) return;

    this.attachedVideo = video;
    this.ensureContext();

    if (!this.context || !this.gainNode) return;

    let source = sourceNodeCache.get(video);
    if (!source) {
      source = this.context.createMediaElementSource(video);
      sourceNodeCache.set(video, source);
    }

    source.connect(this.gainNode);
    this.gainNode.connect(this.context.destination);
  }

  setGain(value: number): void {
    if (this.isDestroyed) return;

    this.currentGain = Math.max(0, Math.min(value, this.maxGain));

    if (this.gainNode) {
      this.gainNode.gain.value = this.currentGain;
    }
  }

  getGain(): number {
    return this.currentGain;
  }

  destroy(): void {
    if (this.isDestroyed) return;
    this.isDestroyed = true;

    if (this.gainNode) {
      this.gainNode.disconnect();
      this.gainNode = null;
    }

    if (this.context) {
      this.context.close().catch(() => {});
      this.context = null;
    }

    this.attachedVideo = null;
  }

  private ensureContext(): void {
    if (this.context) return;
    if (!this.isSupported()) return;

    const Ctor =
      typeof AudioContext !== "undefined" ? AudioContext : (window as any).webkitAudioContext;

    this.context = new Ctor();
    this.gainNode = this.context!.createGain();
    this.gainNode.gain.value = this.currentGain;
  }
}

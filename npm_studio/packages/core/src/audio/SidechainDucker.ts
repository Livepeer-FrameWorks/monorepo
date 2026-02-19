/**
 * SidechainDucker
 *
 * When the key source (e.g. microphone) has signal above a threshold,
 * automatically reduce the volume of target sources (e.g. background music).
 * Uses an AnalyserNode + requestAnimationFrame loop to monitor RMS level.
 */

export interface SidechainDuckerOptions {
  threshold?: number; // RMS threshold (0-1) to trigger ducking, default 0.02
  ratio?: number; // Target gain when ducking (0-1), default 0.2 (-14dB)
  attack?: number; // Attack time in seconds, default 0.01
  release?: number; // Release time in seconds, default 0.3
}

export class SidechainDucker {
  private context: AudioContext;
  private analyser: AnalyserNode;
  private _keyInput: GainNode;
  private _keyOutput: GainNode;
  private targets: Map<string, GainNode> = new Map();
  private rafId: number | null = null;
  private isDucking = false;
  private destroyed = false;

  private threshold: number;
  private ratio: number;
  private attack: number;
  private release: number;

  constructor(context: AudioContext, opts: SidechainDuckerOptions = {}) {
    this.context = context;
    this.threshold = opts.threshold ?? 0.02;
    this.ratio = opts.ratio ?? 0.2;
    this.attack = opts.attack ?? 0.01;
    this.release = opts.release ?? 0.3;

    // Key input is a passthrough — audio flows through to keyOutput unchanged
    this._keyInput = context.createGain();
    this._keyOutput = context.createGain();
    this._keyInput.connect(this._keyOutput);

    // Analyser taps the key signal for level detection
    this.analyser = context.createAnalyser();
    this.analyser.fftSize = 256;
    this._keyInput.connect(this.analyser);

    this.startMonitoring();
  }

  get keyInput(): AudioNode {
    return this._keyInput;
  }

  get keyOutput(): AudioNode {
    return this._keyOutput;
  }

  addTarget(id: string, gainNode: GainNode): void {
    this.targets.set(id, gainNode);
  }

  removeTarget(id: string): void {
    this.targets.delete(id);
  }

  setThreshold(value: number): void {
    this.threshold = Math.max(0, Math.min(1, value));
  }

  setRatio(value: number): void {
    this.ratio = Math.max(0, Math.min(1, value));
  }

  private startMonitoring(): void {
    const data = new Float32Array(this.analyser.fftSize);

    const tick = () => {
      if (this.destroyed) return;

      this.analyser.getFloatTimeDomainData(data);

      // Calculate RMS
      let sum = 0;
      for (let i = 0; i < data.length; i++) {
        sum += data[i] * data[i];
      }
      const rms = Math.sqrt(sum / data.length);

      const now = this.context.currentTime;
      const shouldDuck = rms > this.threshold;

      if (shouldDuck && !this.isDucking) {
        this.isDucking = true;
        for (const gain of this.targets.values()) {
          gain.gain.setTargetAtTime(this.ratio, now, this.attack);
        }
      } else if (!shouldDuck && this.isDucking) {
        this.isDucking = false;
        for (const gain of this.targets.values()) {
          gain.gain.setTargetAtTime(1, now, this.release);
        }
      }

      this.rafId = requestAnimationFrame(tick);
    };

    this.rafId = requestAnimationFrame(tick);
  }

  destroy(): void {
    this.destroyed = true;
    if (this.rafId !== null) {
      cancelAnimationFrame(this.rafId);
      this.rafId = null;
    }
    this._keyInput.disconnect();
    this._keyOutput.disconnect();
    this.analyser.disconnect();
    this.targets.clear();
  }
}

/**
 * SidechainDucker
 *
 * When the key source (e.g. microphone) has signal above a threshold,
 * automatically reduce the volume of target sources (e.g. background music).
 * Uses LevelTap to monitor RMS level.
 */

import { LevelTap } from "./LevelTap";

export interface SidechainDuckerOptions {
  threshold?: number; // RMS threshold (0-1) to trigger ducking, default 0.02
  ratio?: number; // Target gain when ducking (0-1), default 0.2 (-14dB)
  attack?: number; // Attack time in seconds, default 0.01
  release?: number; // Release time in seconds, default 0.3
}

export class SidechainDucker {
  private context: AudioContext;
  private levelTap: LevelTap;
  private _keyInput: GainNode;
  private _keyOutput: GainNode;
  private targets: Map<string, GainNode> = new Map();
  private isDucking = false;

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

    // Level tap on the key signal
    this.levelTap = new LevelTap(context, (rms) => this.processLevel(rms));
    this._keyInput.connect(this.levelTap.input);
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

  private processLevel(rms: number): void {
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
  }

  destroy(): void {
    this.levelTap.destroy();
    this._keyInput.disconnect();
    this._keyOutput.disconnect();
    this.targets.clear();
  }
}

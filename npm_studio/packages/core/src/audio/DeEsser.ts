/**
 * DeEsser
 *
 * Sibilance control in the 4-8 kHz range.
 * Detection: BiquadFilterNode (bandpass) → AnalyserNode for sibilant energy.
 * Reduction: BiquadFilterNode (highshelf) with dynamically modulated gain.
 * Uses the same LevelTap level detection as NoiseGate.
 */

import { LevelTap } from "./LevelTap";

export interface DeEsserOptions {
  frequency?: number; // Center frequency in Hz, default 6000
  threshold?: number; // Detection threshold in dB, default -20
  reduction?: number; // Max reduction in dB, default -10
  q?: number; // Bandpass Q, default 2
}

export class DeEsser {
  private context: AudioContext;
  private _input: GainNode;
  private _output: GainNode;

  // Detection chain
  private detector: BiquadFilterNode;
  private levelTap: LevelTap;

  // Reduction
  private shelf: BiquadFilterNode;

  private frequency: number;
  private threshold: number;
  private reduction: number;

  constructor(context: AudioContext, opts: DeEsserOptions = {}) {
    this.context = context;
    this.frequency = opts.frequency ?? 6000;
    this.threshold = opts.threshold ?? -20;
    this.reduction = opts.reduction ?? -10;

    this._input = context.createGain();
    this._output = context.createGain();

    // Detection: bandpass at sibilance frequency → level tap
    this.detector = context.createBiquadFilter();
    this.detector.type = "bandpass";
    this.detector.frequency.value = this.frequency;
    this.detector.Q.value = opts.q ?? 2;

    this.levelTap = new LevelTap(context, (rms) => this.processLevel(rms));

    // Reduction: highshelf at slightly below sibilance frequency
    this.shelf = context.createBiquadFilter();
    this.shelf.type = "highshelf";
    this.shelf.frequency.value = this.frequency * 0.8; // Start shelf below peak
    this.shelf.gain.value = 0; // No reduction by default

    // Signal path: input → shelf → output
    this._input.connect(this.shelf);
    this.shelf.connect(this._output);

    // Detection path (parallel): input → detector → level tap (no output)
    this._input.connect(this.detector);
    this.detector.connect(this.levelTap.input);
  }

  get input(): AudioNode {
    return this._input;
  }

  get output(): AudioNode {
    return this._output;
  }

  setFrequency(hz: number): void {
    this.frequency = Math.max(2000, Math.min(12000, hz));
    this.detector.frequency.value = this.frequency;
    this.shelf.frequency.value = this.frequency * 0.8;
  }

  setThreshold(dB: number): void {
    this.threshold = Math.max(-60, Math.min(0, dB));
  }

  private processLevel(rms: number): void {
    const dB = rms > 0.0001 ? 20 * Math.log10(rms) : -100;

    // Modulate shelf gain based on sibilant energy
    const now = this.context.currentTime;
    if (dB > this.threshold) {
      // Sibilance detected — apply reduction proportional to excess
      const excess = dB - this.threshold;
      const targetGain = Math.max(this.reduction, -excess);
      this.shelf.gain.setTargetAtTime(targetGain, now, 0.005);
    } else {
      // No sibilance — release
      this.shelf.gain.setTargetAtTime(0, now, 0.05);
    }
  }

  destroy(): void {
    this.levelTap.destroy();
    this._input.disconnect();
    this._output.disconnect();
    this.detector.disconnect();
    this.shelf.disconnect();
  }
}

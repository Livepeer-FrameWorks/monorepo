/**
 * Three-Band Equalizer
 * Low/Mid/High tone control using native BiquadFilterNodes.
 * Three bands: lowshelf (200 Hz), peaking (1 kHz), highshelf (4 kHz).
 * Each band adjustable from -12 dB to +12 dB.
 */

export interface ThreeBandEQOptions {
  lowFreq?: number; // Low shelf frequency (default: 200 Hz)
  midFreq?: number; // Mid peak frequency (default: 1000 Hz)
  highFreq?: number; // High shelf frequency (default: 4000 Hz)
  midQ?: number; // Mid band Q/width (default: 1.0)
}

export class ThreeBandEQ {
  private lowShelf: BiquadFilterNode;
  private midPeak: BiquadFilterNode;
  private highShelf: BiquadFilterNode;

  constructor(context: AudioContext, opts?: ThreeBandEQOptions) {
    // Low shelf: boosts/cuts frequencies below cutoff
    this.lowShelf = context.createBiquadFilter();
    this.lowShelf.type = "lowshelf";
    this.lowShelf.frequency.value = opts?.lowFreq ?? 200;
    this.lowShelf.gain.value = 0;

    // Mid peak: boosts/cuts around center frequency
    this.midPeak = context.createBiquadFilter();
    this.midPeak.type = "peaking";
    this.midPeak.frequency.value = opts?.midFreq ?? 1000;
    this.midPeak.Q.value = opts?.midQ ?? 1.0;
    this.midPeak.gain.value = 0;

    // High shelf: boosts/cuts frequencies above cutoff
    this.highShelf = context.createBiquadFilter();
    this.highShelf.type = "highshelf";
    this.highShelf.frequency.value = opts?.highFreq ?? 4000;
    this.highShelf.gain.value = 0;

    // Chain: low → mid → high
    this.lowShelf.connect(this.midPeak);
    this.midPeak.connect(this.highShelf);
  }

  get input(): AudioNode {
    return this.lowShelf;
  }

  get output(): AudioNode {
    return this.highShelf;
  }

  /**
   * Set low band gain (-12 to +12 dB)
   */
  setLow(dB: number): void {
    const clamped = Math.max(-12, Math.min(12, dB));
    this.lowShelf.gain.setTargetAtTime(clamped, this.lowShelf.context.currentTime, 0.01);
  }

  /**
   * Set mid band gain (-12 to +12 dB)
   */
  setMid(dB: number): void {
    const clamped = Math.max(-12, Math.min(12, dB));
    this.midPeak.gain.setTargetAtTime(clamped, this.midPeak.context.currentTime, 0.01);
  }

  /**
   * Set high band gain (-12 to +12 dB)
   */
  setHigh(dB: number): void {
    const clamped = Math.max(-12, Math.min(12, dB));
    this.highShelf.gain.setTargetAtTime(clamped, this.highShelf.context.currentTime, 0.01);
  }

  getLow(): number {
    return this.lowShelf.gain.value;
  }

  getMid(): number {
    return this.midPeak.gain.value;
  }

  getHigh(): number {
    return this.highShelf.gain.value;
  }

  /**
   * Reset all bands to 0 dB (flat)
   */
  reset(): void {
    this.setLow(0);
    this.setMid(0);
    this.setHigh(0);
  }

  destroy(): void {
    this.lowShelf.disconnect();
    this.midPeak.disconnect();
    this.highShelf.disconnect();
  }
}

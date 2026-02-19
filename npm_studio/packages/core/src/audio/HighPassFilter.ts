/**
 * High-Pass Filter
 * Removes low-frequency rumble (air conditioning, desk vibration, proximity effect).
 * Uses native BiquadFilterNode — zero overhead.
 */

export interface HighPassFilterOptions {
  frequency?: number; // Cutoff frequency in Hz (default: 80)
  q?: number; // Resonance / Q factor (default: 0.707 = Butterworth flat response)
}

export class HighPassFilter {
  private filter: BiquadFilterNode;

  constructor(context: AudioContext, opts?: HighPassFilterOptions) {
    this.filter = context.createBiquadFilter();
    this.filter.type = "highpass";
    this.filter.frequency.value = opts?.frequency ?? 80;
    this.filter.Q.value = opts?.q ?? 0.707;
  }

  get input(): AudioNode {
    return this.filter;
  }

  get output(): AudioNode {
    return this.filter;
  }

  setFrequency(hz: number): void {
    this.filter.frequency.setTargetAtTime(hz, this.filter.context.currentTime, 0.01);
  }

  setQ(q: number): void {
    this.filter.Q.setTargetAtTime(q, this.filter.context.currentTime, 0.01);
  }

  getFrequency(): number {
    return this.filter.frequency.value;
  }

  destroy(): void {
    this.filter.disconnect();
  }
}

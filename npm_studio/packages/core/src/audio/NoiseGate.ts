/**
 * Noise Gate
 * Attenuates audio below a threshold — silences background noise when not speaking.
 * Uses AnalyserNode for level detection and GainNode with envelope for gating.
 *
 * States: OPEN (signal above threshold) → HOLD → CLOSE (attenuate)
 * The hold period prevents the gate from chattering on speech pauses.
 */

export interface NoiseGateOptions {
  threshold?: number; // dB below which gate closes (default: -40)
  attack?: number; // ms to fully open (default: 1)
  hold?: number; // ms to stay open after signal drops (default: 50)
  release?: number; // ms to fully close (default: 100)
  range?: number; // dB of attenuation when closed (default: -80)
}

type GateState = "open" | "hold" | "closed";

export class NoiseGate {
  private inputNode: GainNode;
  private outputGain: GainNode;
  private analyser: AnalyserNode;
  private context: AudioContext;

  private thresholdDb: number;
  private attackMs: number;
  private holdMs: number;
  private releaseMs: number;
  private rangeDb: number;

  private state: GateState = "closed";
  private holdTimer: ReturnType<typeof setTimeout> | null = null;
  private rafId: number | null = null;
  private active = true;

  // Level detection buffer
  private analyserData: Float32Array<ArrayBuffer>;

  constructor(context: AudioContext, opts?: NoiseGateOptions) {
    this.context = context;
    this.thresholdDb = opts?.threshold ?? -40;
    this.attackMs = opts?.attack ?? 1;
    this.holdMs = opts?.hold ?? 50;
    this.releaseMs = opts?.release ?? 100;
    this.rangeDb = opts?.range ?? -80;

    // Input passthrough (so we can tap the signal for analysis)
    this.inputNode = context.createGain();
    this.inputNode.gain.value = 1;

    // Analyser reads the input signal level
    this.analyser = context.createAnalyser();
    this.analyser.fftSize = 256;
    this.analyser.smoothingTimeConstant = 0.5;
    this.analyserData = new Float32Array(new ArrayBuffer(this.analyser.fftSize * 4));

    // Output gain controls attenuation
    this.outputGain = context.createGain();
    this.outputGain.gain.value = this.closedGain; // Start closed

    // Wire: input → analyser (tap) + input → outputGain
    this.inputNode.connect(this.analyser);
    this.inputNode.connect(this.outputGain);

    // Start the gate processing loop
    this.startProcessing();
  }

  get input(): AudioNode {
    return this.inputNode;
  }

  get output(): AudioNode {
    return this.outputGain;
  }

  get isOpen(): boolean {
    return this.state === "open" || this.state === "hold";
  }

  get currentState(): GateState {
    return this.state;
  }

  // Attenuation gain when gate is closed (convert dB range to linear)
  private get closedGain(): number {
    return Math.pow(10, this.rangeDb / 20);
  }

  // ==========================================================================
  // Parameter control
  // ==========================================================================

  setThreshold(dB: number): void {
    this.thresholdDb = dB;
  }

  setAttack(ms: number): void {
    this.attackMs = Math.max(0.1, ms);
  }

  setHold(ms: number): void {
    this.holdMs = Math.max(0, ms);
  }

  setRelease(ms: number): void {
    this.releaseMs = Math.max(1, ms);
  }

  setRange(dB: number): void {
    this.rangeDb = dB;
  }

  getThreshold(): number {
    return this.thresholdDb;
  }

  // ==========================================================================
  // Processing
  // ==========================================================================

  private startProcessing(): void {
    const process = () => {
      if (!this.active) return;
      this.processFrame();
      this.rafId = requestAnimationFrame(process);
    };
    this.rafId = requestAnimationFrame(process);
  }

  private processFrame(): void {
    // Read RMS level from analyser
    this.analyser.getFloatTimeDomainData(this.analyserData);

    let sumSquares = 0;
    for (let i = 0; i < this.analyserData.length; i++) {
      sumSquares += this.analyserData[i] * this.analyserData[i];
    }
    const rms = Math.sqrt(sumSquares / this.analyserData.length);
    const levelDb = rms > 0.00001 ? 20 * Math.log10(rms) : -100;

    const now = this.context.currentTime;
    const aboveThreshold = levelDb >= this.thresholdDb;

    switch (this.state) {
      case "closed":
        if (aboveThreshold) {
          this.state = "open";
          this.clearHoldTimer();
          // Open with attack time
          this.outputGain.gain.setTargetAtTime(1, now, this.attackMs / 1000 / 3);
        }
        break;

      case "open":
        if (!aboveThreshold) {
          this.state = "hold";
          this.startHoldTimer();
        }
        break;

      case "hold":
        if (aboveThreshold) {
          // Signal came back — return to open
          this.state = "open";
          this.clearHoldTimer();
        }
        // Hold timer will transition to closed
        break;
    }
  }

  private startHoldTimer(): void {
    this.clearHoldTimer();
    this.holdTimer = setTimeout(() => {
      if (this.state === "hold") {
        this.state = "closed";
        // Close with release time
        this.outputGain.gain.setTargetAtTime(
          this.closedGain,
          this.context.currentTime,
          this.releaseMs / 1000 / 3
        );
      }
    }, this.holdMs);
  }

  private clearHoldTimer(): void {
    if (this.holdTimer !== null) {
      clearTimeout(this.holdTimer);
      this.holdTimer = null;
    }
  }

  // ==========================================================================
  // Cleanup
  // ==========================================================================

  destroy(): void {
    this.active = false;
    this.clearHoldTimer();
    if (this.rafId !== null) {
      cancelAnimationFrame(this.rafId);
      this.rafId = null;
    }
    this.inputNode.disconnect();
    this.analyser.disconnect();
    this.outputGain.disconnect();
  }
}

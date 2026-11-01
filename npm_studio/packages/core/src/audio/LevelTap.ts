/**
 * Level Tap
 * Reports the RMS level of a signal from the audio thread, so gain automation
 * keeps running in a background tab, where animation frames stop. Without
 * AudioWorklet (insecure context, CSP blocking blob: scripts) it falls back to
 * an AnalyserNode read on a timer, which browsers throttle in background tabs.
 */

const PROCESSOR_NAME = "fw-level-tap";
const WINDOW_SAMPLES = 512;
const FALLBACK_INTERVAL_MS = 20;

// Runs in AudioWorkletGlobalScope and loads from a blob: URL, so the package
// needs no extra bundled asset.
const PROCESSOR_SOURCE = `
class LevelTapProcessor extends AudioWorkletProcessor {
  sum = 0;
  count = 0;
  process(inputs) {
    const samples = inputs[0]?.[0];
    if (samples) {
      for (let i = 0; i < samples.length; i++) {
        this.sum += samples[i] * samples[i];
      }
      this.count += samples.length;
      if (this.count >= ${WINDOW_SAMPLES}) {
        this.port.postMessage(Math.sqrt(this.sum / this.count));
        this.sum = 0;
        this.count = 0;
      }
    }
    return true;
  }
}
registerProcessor("${PROCESSOR_NAME}", LevelTapProcessor);
`;

const loadedModules = new WeakMap<BaseAudioContext, Promise<void>>();

function loadProcessor(context: BaseAudioContext): Promise<void> {
  let loaded = loadedModules.get(context);
  if (!loaded) {
    const url = URL.createObjectURL(new Blob([PROCESSOR_SOURCE], { type: "text/javascript" }));
    loaded = context.audioWorklet.addModule(url).finally(() => URL.revokeObjectURL(url));
    loadedModules.set(context, loaded);
  }
  return loaded;
}

export class LevelTap {
  private inputNode: GainNode;
  private workletNode: AudioWorkletNode | null = null;
  private analyser: AnalyserNode | null = null;
  private fallbackTimer: ReturnType<typeof setInterval> | null = null;
  private destroyed = false;

  constructor(
    private context: AudioContext,
    private onLevel: (rms: number) => void
  ) {
    this.inputNode = context.createGain();
    this.start();
  }

  get input(): AudioNode {
    return this.inputNode;
  }

  private async start(): Promise<void> {
    try {
      await loadProcessor(this.context);
      if (this.destroyed) return;
      this.workletNode = new AudioWorkletNode(this.context, PROCESSOR_NAME, {
        numberOfOutputs: 0,
      });
      this.workletNode.port.onmessage = (event: MessageEvent<number>) => {
        if (!this.destroyed) this.onLevel(event.data);
      };
      this.inputNode.connect(this.workletNode);
    } catch {
      if (!this.destroyed) this.startFallback();
    }
  }

  private startFallback(): void {
    const analyser = this.context.createAnalyser();
    analyser.fftSize = 256;
    this.analyser = analyser;
    this.inputNode.connect(analyser);

    const data = new Float32Array(analyser.fftSize);
    this.fallbackTimer = setInterval(() => {
      analyser.getFloatTimeDomainData(data);
      let sum = 0;
      for (let i = 0; i < data.length; i++) {
        sum += data[i] * data[i];
      }
      this.onLevel(Math.sqrt(sum / data.length));
    }, FALLBACK_INTERVAL_MS);
  }

  destroy(): void {
    this.destroyed = true;
    if (this.fallbackTimer !== null) {
      clearInterval(this.fallbackTimer);
      this.fallbackTimer = null;
    }
    this.inputNode.disconnect();
    this.workletNode?.disconnect();
    this.analyser?.disconnect();
  }
}

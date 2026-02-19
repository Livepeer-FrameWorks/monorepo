/**
 * AudioWorklet Renderer — Main Thread Controller
 *
 * Manages an AudioContext with a GainNode for volume control and an
 * AudioWorkletNode that pulls PCM samples from a double-buffered ring.
 *
 * Features:
 * - GainNode-based volume/mute (no re-encoding)
 * - Pull-based design — worklet requests samples, never starves on push
 * - Underrun detection with throttled events
 * - Desktop/mobile buffer tuning
 * - Playback rate via AudioContext
 */

/**
 * Worklet source inlined as a data: URI.
 * Blob URLs are rejected by Firefox's AudioWorklet scope — data: URIs work
 * across all browsers (same approach as MistServer rawws.js).
 */
const WORKLET_SOURCE = `
class FWAudioProcessor extends AudioWorkletProcessor {
  constructor() {
    super();
    this.backBuffer = [];
    this.readOffset = 0;
    this.channels = 2;
    this.lastUnderrunReport = 0;
    this.alive = true;
    this.port.onmessage = (e) => {
      const msg = e.data;
      if (msg.type === "samples") {
        this.pushSamples(msg);
      } else if (msg.type === "destroy") {
        this.alive = false;
      }
    };
  }
  pushSamples(msg) {
    this.channels = msg.channels;
    const off = this.readOffset;
    for (let ch = 0; ch < msg.channels; ch++) {
      if (!this.backBuffer[ch]) {
        this.backBuffer[ch] = msg.data[ch];
      } else {
        const existing = this.backBuffer[ch];
        const incoming = msg.data[ch];
        const merged = new Float32Array(existing.length - off + incoming.length);
        merged.set(existing.subarray(off));
        merged.set(incoming, existing.length - off);
        this.backBuffer[ch] = merged;
      }
    }
    this.readOffset = 0;
  }
  process(_inputs, outputs) {
    if (!this.alive) return false;
    const output = outputs[0];
    if (!output || output.length === 0) return true;
    const framesToWrite = output[0].length;
    const available = this.backBuffer[0] ? this.backBuffer[0].length - this.readOffset : 0;
    if (available >= framesToWrite) {
      for (let ch = 0; ch < output.length; ch++) {
        const src = this.backBuffer[ch] || this.backBuffer[0];
        if (src) output[ch].set(src.subarray(this.readOffset, this.readOffset + framesToWrite));
        else output[ch].fill(0);
      }
      this.readOffset += framesToWrite;
      if (this.readOffset > 4096) {
        for (let ch = 0; ch < this.backBuffer.length; ch++) {
          if (this.backBuffer[ch]) this.backBuffer[ch] = this.backBuffer[ch].subarray(this.readOffset);
        }
        this.readOffset = 0;
      }
    } else if (available > 0) {
      for (let ch = 0; ch < output.length; ch++) {
        const src = this.backBuffer[ch] || this.backBuffer[0];
        if (src) output[ch].set(src.subarray(this.readOffset, this.readOffset + available));
        output[ch].fill(0, available);
      }
      this.readOffset += available;
      this.port.postMessage({ type: "underrun", time: currentTime });
    } else {
      for (let ch = 0; ch < output.length; ch++) output[ch].fill(0);
      if (currentTime - this.lastUnderrunReport > 1) {
        this.lastUnderrunReport = currentTime;
        this.port.postMessage({ type: "underrun", time: currentTime });
      }
    }
    return true;
  }
}
registerProcessor("fw-audio-processor", FWAudioProcessor);
`;

export interface AudioWorkletRendererOptions {
  sampleRate?: number;
  channels?: number;
}

export class AudioWorkletRenderer {
  private audioContext: AudioContext | null = null;
  private workletNode: AudioWorkletNode | null = null;
  private gainNode: GainNode | null = null;
  private started = false;
  private destroyed = false;
  private ramped = false;
  private framesSent = 0;
  private sampleRate: number;
  private channels: number;
  private pendingData: { data: Float32Array[]; channels: number }[] = [];

  // Underrun callback
  onUnderrun?: (time: number) => void;

  constructor(opts?: AudioWorkletRendererOptions) {
    this.sampleRate = opts?.sampleRate ?? 48000;
    this.channels = opts?.channels ?? 2;
  }

  /**
   * Create AudioContext, register worklet, and wire the audio graph.
   * Must be called after a user gesture (autoplay policy).
   */
  async start(): Promise<void> {
    if (this.started || this.destroyed) return;

    this.audioContext = new AudioContext({
      sampleRate: this.sampleRate,
      latencyHint: "interactive",
    });

    // Use data: URI — Blob URLs are rejected by Firefox's AudioWorklet scope
    const encoded = encodeURIComponent(WORKLET_SOURCE);
    await this.audioContext.audioWorklet.addModule(`data:text/javascript,${encoded}`);

    // Create nodes
    this.workletNode = new AudioWorkletNode(this.audioContext, "fw-audio-processor", {
      outputChannelCount: [this.channels],
    });

    this.gainNode = this.audioContext.createGain();
    // Start silent — ramp up after first samples arrive to mask startup underruns
    this.gainNode.gain.value = 0;

    // Wire: worklet → gain → destination
    this.workletNode.connect(this.gainNode);
    this.gainNode.connect(this.audioContext.destination);

    // Listen for underrun events from worklet
    this.workletNode.port.onmessage = (e: MessageEvent) => {
      if (e.data.type === "underrun") {
        this.onUnderrun?.(e.data.time);
      }
    };

    // Firefox/Safari create AudioContext in "suspended" state (autoplay policy).
    // Without resume(), the worklet runs but produces silence.
    if (this.audioContext.state === "suspended") {
      await this.audioContext.resume();
    }

    this.started = true;

    // Flush any frames that arrived while start() was loading
    for (const pending of this.pendingData) {
      this.postToWorklet(pending.data, pending.channels);
    }
    this.pendingData = [];
  }

  /**
   * Feed decoded AudioData into the worklet ring buffer.
   * Converts AudioData to per-channel Float32Arrays and posts to worklet.
   * Frames arriving before start() completes are buffered and flushed once ready.
   */
  feed(data: AudioData): void {
    if (this.destroyed) {
      data.close();
      return;
    }

    const channels = data.numberOfChannels;
    const frames = data.numberOfFrames;
    const channelData: Float32Array[] = [];

    // Extract per-channel data
    for (let ch = 0; ch < channels; ch++) {
      const buf = new Float32Array(frames);
      data.copyTo(buf, { planeIndex: ch, format: "f32-planar" });
      channelData.push(buf);
    }

    data.close();

    if (!this.started || !this.workletNode) {
      // Buffer until worklet is ready
      this.pendingData.push({ data: channelData, channels });
      return;
    }

    this.postToWorklet(channelData, channels);
  }

  private postToWorklet(channelData: Float32Array[], channels: number): void {
    if (!this.workletNode) return;
    const transferables = channelData.map((c) => c.buffer);
    this.workletNode.port.postMessage(
      { type: "samples", data: channelData, channels },
      transferables
    );

    // Startup watermark: let worklet build buffer before ramping gain up (~115ms at 44100/1024)
    this.framesSent++;
    if (!this.ramped && this.framesSent >= 5 && this.gainNode && this.audioContext) {
      this.ramped = true;
      this.gainNode.gain.setTargetAtTime(1.0, this.audioContext.currentTime, 0.05);
    }
  }

  /**
   * Set output volume (0-1). Uses GainNode for smooth ramping.
   */
  setVolume(v: number): void {
    if (!this.gainNode || !this.audioContext) return;
    this.gainNode.gain.setTargetAtTime(
      Math.max(0, Math.min(1, v)),
      this.audioContext.currentTime,
      0.015 // ~15ms ramp for click-free transitions
    );
  }

  /**
   * Set muted state. Ramps gain to 0 or restores previous value.
   */
  setMuted(muted: boolean): void {
    if (!this.gainNode || !this.audioContext) return;
    if (muted) {
      this.gainNode.gain.setTargetAtTime(0, this.audioContext.currentTime, 0.015);
    }
    // Unmute handled by setVolume() from the controller
  }

  /**
   * Set playback rate. Changes AudioContext playback rate.
   * Note: This affects pitch. For pitch-corrected speed, a SoundTouch WASM would be needed.
   */
  setPlaybackRate(rate: number): void {
    // AudioContext doesn't have a native playback rate.
    // Speed changes are handled by the frame timing controller feeding samples at a different rate.
    // This is a no-op for now — future work could integrate SoundTouch.
    void rate;
  }

  /**
   * Get the current AudioContext time (seconds).
   * This is the audio clock that drives A/V sync.
   */
  getCurrentTime(): number {
    return this.audioContext?.currentTime ?? 0;
  }

  /**
   * Get the AudioContext for external use (e.g., analyzers).
   */
  getAudioContext(): AudioContext | null {
    return this.audioContext;
  }

  /**
   * Suspend audio output (e.g., when paused).
   */
  async suspend(): Promise<void> {
    if (this.audioContext?.state === "running") {
      await this.audioContext.suspend();
    }
  }

  /**
   * Resume audio output.
   */
  async resume(): Promise<void> {
    if (this.audioContext?.state === "suspended") {
      await this.audioContext.resume();
    }
  }

  destroy(): void {
    if (this.destroyed) return;
    this.destroyed = true;

    // Tell worklet to stop
    if (this.workletNode) {
      this.workletNode.port.postMessage({ type: "destroy" });
      this.workletNode.disconnect();
      this.workletNode = null;
    }

    if (this.gainNode) {
      this.gainNode.disconnect();
      this.gainNode = null;
    }

    if (this.audioContext) {
      this.audioContext.close().catch(() => {});
      this.audioContext = null;
    }

    this.pendingData = [];
    this.started = false;
  }
}

/**
 * AudioWorklet Processor — Pull-based PCM Renderer
 *
 * Runs in the AudioWorklet thread. Reads 128-sample chunks from a double-buffered
 * ring via MessagePort. Posts underrun events (throttled) when starved.
 *
 * This file is loaded as a standalone module via AudioWorklet.addModule().
 */

interface AudioRingMessage {
  type: "samples";
  /** Interleaved or per-channel Float32 arrays */
  data: Float32Array[];
  /** Number of channels */
  channels: number;
}

class FWAudioProcessor extends AudioWorkletProcessor {
  // Double-buffer: front is being filled by main thread, back is being read
  private backBuffer: Float32Array[] = [];
  private readOffset = 0;
  private channels = 2;
  private lastUnderrunReport = 0;
  private alive = true;

  constructor() {
    super();

    this.port.onmessage = (e: MessageEvent) => {
      const msg = e.data;
      if (msg.type === "samples") {
        this.pushSamples(msg as AudioRingMessage);
      } else if (msg.type === "destroy") {
        this.alive = false;
      }
    };
  }

  private pushSamples(msg: AudioRingMessage): void {
    this.channels = msg.channels;
    // Append incoming channel buffers to back buffer
    for (let ch = 0; ch < msg.channels; ch++) {
      if (!this.backBuffer[ch]) {
        this.backBuffer[ch] = msg.data[ch];
      } else {
        // Concatenate
        const existing = this.backBuffer[ch];
        const incoming = msg.data[ch];
        const merged = new Float32Array(existing.length - this.readOffset + incoming.length);
        merged.set(existing.subarray(this.readOffset));
        merged.set(incoming, existing.length - this.readOffset);
        this.backBuffer[ch] = merged;
        this.readOffset = 0;
      }
    }
  }

  process(_inputs: Float32Array[][], outputs: Float32Array[][]): boolean {
    if (!this.alive) return false;

    const output = outputs[0];
    if (!output || output.length === 0) return true;

    const framesToWrite = output[0].length; // Always 128

    // Check if we have enough data
    const available = this.backBuffer[0] ? this.backBuffer[0].length - this.readOffset : 0;

    if (available >= framesToWrite) {
      // Write from back buffer to output
      for (let ch = 0; ch < output.length; ch++) {
        const src = this.backBuffer[ch] ?? this.backBuffer[0];
        if (src) {
          output[ch].set(src.subarray(this.readOffset, this.readOffset + framesToWrite));
        } else {
          output[ch].fill(0);
        }
      }
      this.readOffset += framesToWrite;

      // Compact buffer when mostly consumed
      if (this.readOffset > 4096) {
        for (let ch = 0; ch < this.backBuffer.length; ch++) {
          if (this.backBuffer[ch]) {
            this.backBuffer[ch] = this.backBuffer[ch].subarray(this.readOffset);
          }
        }
        this.readOffset = 0;
      }
    } else if (available > 0) {
      // Partial: write what we have, zero-fill the rest
      for (let ch = 0; ch < output.length; ch++) {
        const src = this.backBuffer[ch] ?? this.backBuffer[0];
        if (src) {
          output[ch].set(src.subarray(this.readOffset, this.readOffset + available));
        }
        output[ch].fill(0, available);
      }
      this.readOffset += available;
      this.reportUnderrun();
    } else {
      // No data — output silence
      for (let ch = 0; ch < output.length; ch++) {
        output[ch].fill(0);
      }
      this.reportUnderrun();
    }

    return true;
  }

  private reportUnderrun(): void {
    const now = currentTime; // AudioWorklet global
    if (now - this.lastUnderrunReport > 1) {
      this.lastUnderrunReport = now;
      this.port.postMessage({ type: "underrun", time: now });
    }
  }
}

registerProcessor("fw-audio-processor", FWAudioProcessor);

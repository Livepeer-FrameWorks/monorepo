/**
 * MediaStreamTrackGenerator Polyfill
 *
 * Provides fallback for browsers without native MediaStreamTrackGenerator (Firefox).
 *
 * Video: Uses Canvas2D + captureStream()
 * Audio: Uses AudioWorklet + createMediaStreamDestination()
 *
 * Based on legacy rawws.js polyfill with improvements:
 * - TypeScript types
 * - Pull-based audio to prevent tin-can distortion
 * - Better resource cleanup
 */

/**
 * Check if native MediaStreamTrackGenerator is available
 */
export function hasNativeMediaStreamTrackGenerator(): boolean {
  return typeof (globalThis as any).MediaStreamTrackGenerator !== "undefined";
}

/**
 * Polyfill for MediaStreamTrackGenerator (video)
 *
 * Uses an offscreen canvas and captureStream() to create a MediaStreamTrack
 * that can be fed VideoFrames via a WritableStream.
 */
export class VideoTrackGeneratorPolyfill {
  private canvas: HTMLCanvasElement;
  private ctx: CanvasRenderingContext2D;
  private track: MediaStreamTrack;
  private _writable: WritableStream<VideoFrame>;
  private closed = false;

  constructor() {
    // Create offscreen canvas
    this.canvas = document.createElement("canvas");
    this.canvas.width = 1920; // Will be resized on first frame
    this.canvas.height = 1080;

    const ctx = this.canvas.getContext("2d", { desynchronized: true });
    if (!ctx) {
      throw new Error("Failed to create canvas 2D context");
    }
    this.ctx = ctx;

    // Capture stream from canvas
    const stream = this.canvas.captureStream();
    const tracks = stream.getVideoTracks();
    if (tracks.length === 0) {
      throw new Error("Failed to capture stream from canvas");
    }
    this.track = tracks[0];

    // Create writable stream that draws frames to canvas
    this._writable = new WritableStream<VideoFrame>({
      write: (frame: VideoFrame) => {
        if (this.closed) {
          frame.close();
          return;
        }

        // Resize canvas to match frame if needed
        if (
          this.canvas.width !== frame.displayWidth ||
          this.canvas.height !== frame.displayHeight
        ) {
          this.canvas.width = frame.displayWidth;
          this.canvas.height = frame.displayHeight;
        }

        // Draw frame to canvas
        this.ctx.drawImage(
          frame as unknown as CanvasImageSource,
          0,
          0,
          this.canvas.width,
          this.canvas.height
        );

        // Close the frame to release resources
        frame.close();
      },
      close: () => {
        this.close();
      },
      abort: () => {
        this.close();
      },
    });
  }

  /**
   * Get the writable stream for writing VideoFrames
   */
  get writable(): WritableStream<VideoFrame> {
    return this._writable;
  }

  /**
   * Get the MediaStreamTrack for adding to MediaStream
   */
  getTrack(): MediaStreamTrack {
    return this.track;
  }

  /**
   * Close and cleanup resources
   */
  close(): void {
    if (this.closed) return;
    this.closed = true;

    this.track.stop();
  }
}

/**
 * Polyfill for MediaStreamTrackGenerator (audio)
 *
 * Uses AudioWorklet to create a pull-based audio output that prevents
 * the "tin-can" distortion issue from the legacy implementation.
 *
 * Key improvement: Audio samples are pulled by the worklet at a fixed rate,
 * and we feed samples proactively rather than pushing them as they arrive.
 */
export class AudioTrackGeneratorPolyfill {
  private audioContext: AudioContext;
  private destination: MediaStreamAudioDestinationNode;
  private workletNode: AudioWorkletNode | null = null;
  private track: MediaStreamTrack;
  private _writable: WritableStream<AudioData>;
  private closed = false;
  private initialized = false;
  private initPromise: Promise<void>;

  // Ring buffer for samples (main thread side)
  private sampleBuffer: Float32Array[] = [];
  private maxBufferChunks = 20; // ~400ms at 48kHz with 1024 samples/chunk

  constructor() {
    // Create audio context
    this.audioContext = new AudioContext({ latencyHint: "interactive" });

    // Create destination for MediaStreamTrack
    this.destination = this.audioContext.createMediaStreamDestination();
    const tracks = this.destination.stream.getAudioTracks();
    if (tracks.length === 0) {
      throw new Error("Failed to create audio destination");
    }
    this.track = tracks[0];

    // Initialize worklet
    this.initPromise = this.initializeWorklet();

    // Create writable stream for AudioData
    this._writable = new WritableStream<AudioData>({
      write: (data: AudioData) => {
        if (this.closed) {
          data.close();
          return;
        }

        // Convert AudioData to Float32Array
        const samples = this.audioDataToSamples(data);
        data.close();

        // Add to ring buffer
        this.sampleBuffer.push(samples);
        if (this.sampleBuffer.length > this.maxBufferChunks) {
          this.sampleBuffer.shift(); // Drop oldest
        }

        // Feed samples to worklet
        this.feedSamples();
      },
      close: () => {
        this.close();
      },
      abort: () => {
        this.close();
      },
    });
  }

  /**
   * Initialize the AudioWorklet
   */
  private async initializeWorklet(): Promise<void> {
    // Create worklet code as blob
    const workletCode = `
      class SyncedAudioProcessor extends AudioWorkletProcessor {
        constructor() {
          super();
          this.ringBuffer = [];
          this.maxBufferSize = 10;

          this.port.onmessage = (e) => {
            if (e.data.type === 'samples') {
              // Add samples to ring buffer
              this.ringBuffer.push(e.data.samples);
              if (this.ringBuffer.length > this.maxBufferSize) {
                this.ringBuffer.shift(); // Drop oldest
              }
            }
          };
        }

        process(inputs, outputs, parameters) {
          const output = outputs[0];
          if (!output || output.length === 0) return true;

          const channel = output[0];

          if (this.ringBuffer.length > 0) {
            const samples = this.ringBuffer.shift();
            const len = Math.min(samples.length, channel.length);
            for (let i = 0; i < len; i++) {
              channel[i] = samples[i];
            }
            // Fill remainder with last sample (smooth transition)
            for (let i = len; i < channel.length; i++) {
              channel[i] = samples[len - 1] || 0;
            }
          } else {
            // No samples - output silence
            channel.fill(0);
          }

          return true;
        }
      }

      registerProcessor('synced-audio-processor', SyncedAudioProcessor);
    `;

    const blob = new Blob([workletCode], { type: "application/javascript" });
    const url = URL.createObjectURL(blob);

    try {
      await this.audioContext.audioWorklet.addModule(url);

      this.workletNode = new AudioWorkletNode(this.audioContext, "synced-audio-processor");
      this.workletNode.connect(this.destination);

      this.initialized = true;
    } finally {
      URL.revokeObjectURL(url);
    }
  }

  /**
   * Convert AudioData to mono Float32Array
   */
  private audioDataToSamples(data: AudioData): Float32Array {
    const channels = data.numberOfChannels;
    const frames = data.numberOfFrames;

    // Get all samples
    const allSamples = new Float32Array(frames * channels);
    data.copyTo(allSamples, { planeIndex: 0 });

    // Convert to mono if needed
    if (channels === 1) {
      return allSamples;
    }

    // Mix down to mono
    const mono = new Float32Array(frames);
    for (let i = 0; i < frames; i++) {
      let sum = 0;
      for (let ch = 0; ch < channels; ch++) {
        sum += allSamples[i * channels + ch];
      }
      mono[i] = sum / channels;
    }

    return mono;
  }

  /**
   * Feed samples to the worklet
   */
  private feedSamples(): void {
    if (!this.initialized || !this.workletNode || this.sampleBuffer.length === 0) {
      return;
    }

    // Send oldest chunk to worklet
    const samples = this.sampleBuffer.shift();
    if (samples) {
      this.workletNode.port.postMessage(
        { type: "samples", samples },
        { transfer: [samples.buffer] }
      );
    }
  }

  /**
   * Get the writable stream for writing AudioData
   */
  get writable(): WritableStream<AudioData> {
    return this._writable;
  }

  /**
   * Get the MediaStreamTrack for adding to MediaStream
   */
  getTrack(): MediaStreamTrack {
    return this.track;
  }

  /**
   * Wait for initialization to complete
   */
  async waitForInit(): Promise<void> {
    await this.initPromise;
  }

  /**
   * Close and cleanup resources
   */
  close(): void {
    if (this.closed) return;
    this.closed = true;

    this.track.stop();
    this.workletNode?.disconnect();
    this.audioContext.close();
    this.sampleBuffer = [];
  }
}

/**
 * Create appropriate track generator based on browser support
 *
 * @param kind - 'video' or 'audio'
 * @returns Native generator or polyfill
 */
export function createTrackGenerator(kind: "video" | "audio"): {
  writable: WritableStream<VideoFrame | AudioData>;
  getTrack: () => MediaStreamTrack;
  close: () => void;
  waitForInit?: () => Promise<void>;
} {
  // Try native first
  if (hasNativeMediaStreamTrackGenerator()) {
    const Generator = (globalThis as any).MediaStreamTrackGenerator;
    const generator = new Generator({ kind });
    return {
      writable: generator.writable,
      getTrack: () => generator,
      close: () => generator.stop?.(),
    };
  }

  // Fall back to polyfill
  if (kind === "video") {
    const polyfill = new VideoTrackGeneratorPolyfill();
    return {
      writable: polyfill.writable as WritableStream<VideoFrame | AudioData>,
      getTrack: () => polyfill.getTrack(),
      close: () => polyfill.close(),
    };
  } else {
    const polyfill = new AudioTrackGeneratorPolyfill();
    return {
      writable: polyfill.writable as WritableStream<VideoFrame | AudioData>,
      getTrack: () => polyfill.getTrack(),
      close: () => polyfill.close(),
      waitForInit: () => polyfill.waitForInit(),
    };
  }
}

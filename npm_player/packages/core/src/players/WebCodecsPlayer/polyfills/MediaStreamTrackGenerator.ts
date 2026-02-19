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
 * Uses AudioWorklet + createMediaStreamDestination() to shim audio output.
 *
 * Firefox rejects Blob URL modules in AudioWorklet scope, so the worklet code
 * is loaded via data: URI (matching MistServer rawws.js approach). The worklet
 * uses a sample-offset pattern for gapless playback: incoming Float32Arrays are
 * queued and consumed sample-by-sample across process() calls.
 */
export class AudioTrackGeneratorPolyfill {
  private audioContext: AudioContext;
  private destination: MediaStreamAudioDestinationNode;
  private gainNode: GainNode;
  private workletNode: AudioWorkletNode | null = null;
  private track: MediaStreamTrack;
  private _writable: WritableStream<AudioData>;
  private closed = false;
  private initialized = false;
  private ramped = false;
  private framesSent = 0;
  private initPromise: Promise<void>;

  constructor() {
    this.audioContext = new AudioContext({ latencyHint: "interactive" });

    // Start silent — ramp up after first samples arrive to mask startup underruns
    this.gainNode = this.audioContext.createGain();
    this.gainNode.gain.value = 0;

    this.destination = this.audioContext.createMediaStreamDestination();
    const tracks = this.destination.stream.getAudioTracks();
    if (tracks.length === 0) {
      throw new Error("Failed to create audio destination");
    }
    this.track = tracks[0];

    this.initPromise = this.initializeWorklet();

    this._writable = new WritableStream<AudioData>({
      write: (data: AudioData) => {
        if (this.closed) {
          data.close();
          return;
        }

        const planes = this.extractPlanes(data);
        data.close();

        // Forward per-channel planes to worklet
        if (this.initialized && this.workletNode) {
          const buffers = planes.map((p) => p.buffer);
          this.workletNode.port.postMessage(planes, buffers);

          // Startup watermark: let worklet build buffer before ramping gain up
          this.framesSent++;
          if (!this.ramped && this.framesSent >= 5) {
            this.ramped = true;
            this.gainNode.gain.setTargetAtTime(1.0, this.audioContext.currentTime, 0.05);
          }
        }
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
   * Initialize AudioWorklet via data: URI (Firefox-compatible).
   *
   * Blob URLs are rejected by Firefox's AudioWorklet scope. The OG MistServer
   * embed uses `data:text/javascript,(function(){...})()` which works across
   * all browsers that support AudioWorklet.
   */
  private async initializeWorklet(): Promise<void> {
    // Per-channel worklet: q[ch] = queue of Float32Array, a[ch] = current array,
    // o[ch] = offset. Process 1:1 source→output channels, then .set() to
    // duplicate the last source channel to any extra outputs (mono→stereo).
    const workletFn =
      "function worklet(){" +
      'registerProcessor("mstg-shim",class extends AudioWorkletProcessor{' +
      "constructor(){super();this.q=[];this.a=[];this.o=[];this.e=new Float32Array(0);" +
      "this.port.onmessage=({data})=>{" +
      "for(let c=0;c<data.length;c++){" +
      "if(!this.q[c]){this.q[c]=[];this.a[c]=this.e;this.o[c]=0}" +
      "this.q[c].push(data[c])}}}" +
      "process(i,outputs){" +
      "const out=outputs[0];" +
      "if(!out||!out[0])return true;" +
      "if(!this.q.length)return true;" +
      "const len=out[0].length;" +
      "const sc=this.q.length;" +
      // Process each source channel that maps 1:1 to an output channel
      "for(let s=0;s<sc&&s<out.length;s++){" +
      "for(let j=0;j<len;j++){" +
      "if(this.o[s]>=this.a[s].length){this.a[s]=this.q[s]&&this.q[s].shift()||this.e;this.o[s]=0}" +
      "out[s][j]=this.a[s][this.o[s]++]||0}}" +
      // Duplicate last source channel to extra output channels (mono→stereo)
      "for(let c=sc;c<out.length;c++)out[c].set(out[sc-1]);" +
      "return true}" +
      "})" +
      "}";

    await this.audioContext.audioWorklet.addModule("data:text/javascript,(" + workletFn + ")()");

    this.workletNode = new AudioWorkletNode(this.audioContext, "mstg-shim", {
      outputChannelCount: [2],
    });
    // Wire: worklet → gain → destination (gain starts at 0 for ramp-in)
    this.workletNode.connect(this.gainNode);
    this.gainNode.connect(this.destination);

    // Firefox/Safari create AudioContext in "suspended" state (autoplay policy).
    // Without resume(), the worklet runs but produces silence.
    if (this.audioContext.state === "suspended") {
      await this.audioContext.resume();
    }

    this.initialized = true;
  }

  /**
   * Extract per-channel planes from AudioData.
   *
   * Returns one Float32Array per channel. The worklet maintains independent
   * queues per channel and writes to however many output channels it has.
   * If Firefox only provides mono output despite outputChannelCount:[2],
   * the worklet maps all outputs to source channel 0 (graceful fallback).
   */
  private extractPlanes(data: AudioData): Float32Array[] {
    const frames = data.numberOfFrames;
    const channels = data.numberOfChannels;
    const isPlanar = data.format ? data.format.includes("planar") : true;

    if (isPlanar) {
      // Each planeIndex = one channel
      const planes: Float32Array[] = [];
      for (let ch = 0; ch < channels; ch++) {
        const plane = new Float32Array(frames);
        data.copyTo(plane, { planeIndex: ch });
        planes.push(plane);
      }
      return planes;
    }

    // Interleaved: single plane [L0, R0, L1, R1, ...] — de-interleave
    const all = new Float32Array(frames * channels);
    data.copyTo(all, { planeIndex: 0 });
    const planes: Float32Array[] = [];
    for (let ch = 0; ch < channels; ch++) {
      const plane = new Float32Array(frames);
      for (let i = 0; i < frames; i++) {
        plane[i] = all[i * channels + ch];
      }
      planes.push(plane);
    }
    return planes;
  }

  get writable(): WritableStream<AudioData> {
    return this._writable;
  }

  getTrack(): MediaStreamTrack {
    return this.track;
  }

  async waitForInit(): Promise<void> {
    await this.initPromise;
  }

  close(): void {
    if (this.closed) return;
    this.closed = true;

    this.track.stop();
    this.workletNode?.disconnect();
    this.gainNode.disconnect();
    this.audioContext.close();
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

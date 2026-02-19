/**
 * Recording Manager
 * Subscribes to EncoderManager chunk events and muxes into a WebM container.
 * Zero re-encoding — same encoded data goes to both streaming and recording.
 *
 * Architecture:
 *   EncoderManager
 *     ├── videoChunk → RTCRtpScriptTransform (streaming)
 *     └── videoChunk → RecordingManager → WebMWriter (recording)
 *     ├── audioChunk → RTCRtpScriptTransform (streaming)
 *     └── audioChunk → RecordingManager → WebMWriter (recording)
 */

import { TypedEventEmitter } from "../core/EventEmitter";
import { WebMWriter, type WebMWriterOptions } from "./WebMWriter";
import type {
  EncoderManager,
  EncodedVideoChunkData,
  EncodedAudioChunkData,
} from "../core/EncoderManager";

// ============================================================================
// Types
// ============================================================================

interface RecordingEvents {
  started: undefined;
  stopped: { blob: Blob; duration: number; fileSize: number };
  paused: undefined;
  resumed: undefined;
  progress: { duration: number; fileSize: number };
  error: { message: string };
}

export interface RecordingManagerOptions {
  /** WebM muxer options (codec, resolution, audio config) */
  muxerOptions: WebMWriterOptions;
  /** Progress event interval in ms (default: 1000) */
  progressInterval?: number;
}

type RecordingState = "idle" | "recording" | "paused";

// ============================================================================
// RecordingManager
// ============================================================================

export class RecordingManager extends TypedEventEmitter<RecordingEvents> {
  private writer: WebMWriter | null = null;
  private state: RecordingState = "idle";
  private options: RecordingManagerOptions;
  private progressTimer: ReturnType<typeof setInterval> | null = null;

  // Encoder event listeners (stored for cleanup)
  private videoChunkHandler: ((chunk: EncodedVideoChunkData) => void) | null = null;
  private audioChunkHandler: ((chunk: EncodedAudioChunkData) => void) | null = null;
  private encoder: EncoderManager | null = null;

  constructor(options: RecordingManagerOptions) {
    super();
    this.options = options;
  }

  // ==========================================================================
  // State
  // ==========================================================================

  get isRecording(): boolean {
    return this.state === "recording";
  }

  get isPaused(): boolean {
    return this.state === "paused";
  }

  get duration(): number {
    return this.writer?.duration ?? 0;
  }

  get fileSize(): number {
    return this.writer?.size ?? 0;
  }

  getState(): RecordingState {
    return this.state;
  }

  // ==========================================================================
  // Public API
  // ==========================================================================

  /**
   * Start recording. Subscribes to encoder chunk events and begins muxing.
   */
  start(encoder: EncoderManager): void {
    if (this.state !== "idle") {
      this.emit("error", { message: "Recording already in progress" });
      return;
    }

    this.encoder = encoder;
    this.writer = new WebMWriter(this.options.muxerOptions);
    this.state = "recording";

    // Subscribe to encoded chunks
    this.videoChunkHandler = (chunk: EncodedVideoChunkData) => {
      if (this.state !== "recording" || !this.writer) return;
      const timestampMs = chunk.timestamp / 1000; // WebCodecs timestamps are in microseconds
      this.writer.addVideoChunk(chunk.data, timestampMs, chunk.type === "key");
    };

    this.audioChunkHandler = (chunk: EncodedAudioChunkData) => {
      if (this.state !== "recording" || !this.writer) return;
      const timestampMs = chunk.timestamp / 1000;
      this.writer.addAudioChunk(chunk.data, timestampMs);
    };

    encoder.on("videoChunk", this.videoChunkHandler);
    encoder.on("audioChunk", this.audioChunkHandler);

    // Progress reporting
    const interval = this.options.progressInterval ?? 1000;
    this.progressTimer = setInterval(() => {
      if (this.state === "recording") {
        this.emit("progress", { duration: this.duration, fileSize: this.fileSize });
      }
    }, interval);

    this.emit("started", undefined);
  }

  /**
   * Stop recording and return the finalized WebM blob.
   */
  stop(): Blob | null {
    if (this.state === "idle" || !this.writer) return null;

    // Unsubscribe from encoder
    this.cleanupListeners();

    // Stop progress reporting
    if (this.progressTimer) {
      clearInterval(this.progressTimer);
      this.progressTimer = null;
    }

    // Finalize and collect
    const blob = this.writer.finalize();
    const duration = this.duration;
    const fileSize = this.fileSize;

    this.state = "idle";
    this.writer = null;

    this.emit("stopped", { blob, duration, fileSize });
    return blob;
  }

  /**
   * Pause recording. Chunks received while paused are discarded.
   */
  pause(): void {
    if (this.state !== "recording") return;
    this.state = "paused";
    this.emit("paused", undefined);
  }

  /**
   * Resume recording after pause.
   */
  resume(): void {
    if (this.state !== "paused") return;
    this.state = "recording";
    this.emit("resumed", undefined);
  }

  /**
   * Destroy the recording manager and clean up resources.
   */
  destroy(): void {
    if (this.state !== "idle") {
      this.stop();
    }
    this.removeAllListeners();
  }

  // ==========================================================================
  // Internal
  // ==========================================================================

  private cleanupListeners(): void {
    if (this.encoder) {
      if (this.videoChunkHandler) {
        this.encoder.off("videoChunk", this.videoChunkHandler);
      }
      if (this.audioChunkHandler) {
        this.encoder.off("audioChunk", this.audioChunkHandler);
      }
    }
    this.videoChunkHandler = null;
    this.audioChunkHandler = null;
    this.encoder = null;
  }
}

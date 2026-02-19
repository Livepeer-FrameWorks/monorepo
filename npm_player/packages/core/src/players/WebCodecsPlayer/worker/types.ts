/**
 * Worker-specific Types for WebCodecs Player
 *
 * These types define the message protocol between main thread and worker.
 */

import type { TrackInfo, FrameTimingStats, PipelineStats } from "../types";

// ============================================================================
// Main Thread -> Worker Messages
// ============================================================================

export interface CreateMessage {
  type: "create";
  idx: number;
  track: TrackInfo;
  opts: {
    optimizeForLatency: boolean;
    /** Payload format: 'avcc' (length-prefixed) or 'annexb' (start-code delimited) */
    payloadFormat?: "avcc" | "annexb";
  };
  uid: number;
}

export interface ConfigureMessage {
  type: "configure";
  idx: number;
  header: Uint8Array;
  uid: number;
}

export interface ReceiveMessage {
  type: "receive";
  idx: number;
  chunk: {
    type: "key" | "delta";
    timestamp: number; // microseconds
    data: Uint8Array;
  };
  uid: number;
}

export interface SetWritableMessage {
  type: "setwritable";
  idx: number;
  writable: WritableStream<VideoFrame | AudioData>;
  uid: number;
}

export interface CreateGeneratorMessage {
  type: "creategenerator";
  idx: number;
  uid: number;
}

export interface CloseMessage {
  type: "close";
  idx: number;
  waitEmpty?: boolean;
  uid: number;
}

export interface FrameTimingMessage {
  type: "frametiming";
  action: "setSpeed" | "reset" | "setPaused";
  speed?: number;
  tweak?: number;
  paused?: boolean;
  uid: number;
}

export interface SeekMessage {
  type: "seek";
  seekTime: number; // milliseconds
  uid: number;
}

export interface FrameStepMessage {
  type: "framestep";
  direction: -1 | 1;
  uid: number;
}

export interface DebuggingMessage {
  type: "debugging";
  value: boolean | "verbose";
  uid: number;
}

export interface SetRenderModeMessage {
  type: "setrendermode";
  idx: number;
  /** When true, worker transfers frames via postMessage instead of writing to stream */
  directTransfer: boolean;
  uid: number;
}

export type MainToWorkerMessage =
  | CreateMessage
  | ConfigureMessage
  | ReceiveMessage
  | SetWritableMessage
  | CreateGeneratorMessage
  | CloseMessage
  | FrameTimingMessage
  | SeekMessage
  | FrameStepMessage
  | DebuggingMessage
  | SetRenderModeMessage;

// ============================================================================
// Worker -> Main Thread Messages
// ============================================================================

export interface AddTrackMessage {
  type: "addtrack";
  idx: number;
  track?: MediaStreamTrack; // Only for Safari's MediaStreamTrackGenerator in Worker
  uid: number;
  status: "ok";
}

export interface RemoveTrackMessage {
  type: "removetrack";
  idx: number;
  uid: number;
}

export interface SetPlaybackRateMessage {
  type: "setplaybackrate";
  speed: number;
  uid: number;
}

export interface ClosedMessage {
  type: "closed";
  idx: number;
  uid: number;
  status: "ok";
}

export interface LogMessage {
  type: "log";
  msg: string;
  level?: "info" | "warn" | "error";
  uid: number;
}

export interface SendEventMessage {
  type: "sendevent";
  kind: string;
  message?: string;
  time?: number;
  idx?: number;
  uid: number;
}

export interface StatsUpdateMessage {
  type: "stats";
  stats: {
    frameTiming: FrameTimingStats;
    pipelines: Record<number, PipelineStats>;
  };
  uid: number;
}

export interface AckMessage {
  type: "ack";
  idx?: number;
  uid: number;
  status: "ok" | "error";
  error?: string;
}

/** Safari audio: worker sends frames to main thread for writing */
export interface WriteFrameMessage {
  type: "writeframe";
  idx: number;
  frame: AudioData;
  uid: number;
}

/** Direct frame transfer: worker sends decoded frame for WebGL/AudioWorklet rendering */
export interface TransferFrameMessage {
  type: "transferframe";
  idx: number;
  trackType: "video" | "audio";
  frame: VideoFrame | AudioData;
  timestamp: number; // microseconds
  uid: number;
}

/** WASM-decoded YUV planes: worker sends raw YUV data for WebGL renderYUV() */
export interface TransferYUVMessage {
  type: "transferyuv";
  idx: number;
  timestamp: number; // microseconds
  y: Uint8Array | Uint16Array;
  u: Uint8Array | Uint16Array;
  v: Uint8Array | Uint16Array;
  width: number;
  height: number;
  format: string; // PixelFormat: "I420" | "I422" | "I444" | "I420P10"
  colorPrimaries?: string;
  transferFunction?: string;
  uid: number;
}

export type WorkerToMainMessage =
  | AddTrackMessage
  | RemoveTrackMessage
  | SetPlaybackRateMessage
  | ClosedMessage
  | LogMessage
  | SendEventMessage
  | StatsUpdateMessage
  | AckMessage
  | WriteFrameMessage
  | TransferFrameMessage
  | TransferYUVMessage;

// ============================================================================
// Internal Worker Types
// ============================================================================

export interface FrameTiming {
  /** When frame entered decoder (microseconds, performance.now() * 1000) */
  in: number;
  /** When frame exited decoder (microseconds) */
  decoded: number;
  /** When frame was written to output (microseconds) */
  out: number;
  speed: {
    main: number;
    tweak: number;
    combined: number;
  };
  seeking: boolean;
  paused: boolean;
  /** Server-sent current time */
  serverTime: number;
}

export interface DecodedFrame {
  frame: VideoFrame | AudioData;
  timestamp: number; // microseconds
  decodedAt: number; // performance.now() when decoded
}

export interface PipelineState {
  idx: number;
  track: TrackInfo;
  configured: boolean;
  closed: boolean;
  decoder: VideoDecoder | AudioDecoder | null;
  writable: WritableStream<VideoFrame | AudioData> | null;
  writer: WritableStreamDefaultWriter<VideoFrame | AudioData> | null;
  inputQueue: Array<{
    type: "key" | "delta";
    timestamp: number;
    data: Uint8Array;
  }>;
  outputQueue: DecodedFrame[];
  /** Recent video frames for backward/forward stepping (video only) */
  frameHistory?: Array<{ frame: VideoFrame; timestamp: number }>;
  /** Cursor into frameHistory for step navigation */
  historyCursor?: number | null;
  stats: {
    framesIn: number;
    framesDecoded: number;
    framesOut: number;
    framesDropped: number; // Phase 2B: Track dropped frames
    lastInputTimestamp: number;
    lastOutputTimestamp: number;
    decoderQueueSize: number;
    // Debug info for error diagnosis
    lastChunkType: string;
    lastChunkSize: number;
    lastChunkBytes: string;
  };
  optimizeForLatency: boolean;
  /** Payload format: 'avcc' (length-prefixed) or 'annexb' (start-code delimited) */
  payloadFormat: "avcc" | "annexb";
  /** When true, transfer frames to main thread via postMessage instead of writing to stream */
  directTransfer: boolean;
  /** WASM fallback decoder — set when native VideoDecoder doesn't support the codec */
  wasmDecoder: unknown | null;
  /** Set at runtime when first keyframe has Annex B start codes despite AVCC description */
  annexBConvert?: boolean;
  /** Pressure drop: dropping all frames until next keyframe to avoid moshing */
  droppingUntilKeyframe?: boolean;
}

// ============================================================================
// Frame Scheduling Types
// ============================================================================

export interface ScheduleResult {
  /** Whether frame should be output now */
  shouldOutput: boolean;
  /** How early/late frame is (negative = late) in ms */
  earliness: number;
  /** Suggested delay before next check (ms) */
  checkDelayMs: number;
}

// ============================================================================
// Codec Configuration Types
// ============================================================================

export interface VideoDecoderInit {
  codec: string;
  codedWidth?: number;
  codedHeight?: number;
  description?: Uint8Array;
  hardwareAcceleration?: "no-preference" | "prefer-hardware" | "prefer-software";
  optimizeForLatency?: boolean;
}

export interface AudioDecoderInit {
  codec: string;
  sampleRate: number;
  numberOfChannels: number;
  description?: Uint8Array;
}

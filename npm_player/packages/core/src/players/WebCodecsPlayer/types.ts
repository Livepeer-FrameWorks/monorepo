/**
 * WebCodecs Player Types
 *
 * Type definitions for the WebCodecs-based low-latency player.
 */

// ============================================================================
// Latency Profile Types
// ============================================================================

export type LatencyProfileName = 'ultra-low' | 'low' | 'balanced' | 'quality';

export interface LatencyProfile {
  name: string;
  /** Base buffer before decoding (ms) */
  keepAway: number;
  /** Multiplier for jitter in buffer calculation */
  jitterMultiplier: number;
  /** Buffer ratio threshold to trigger speed up (e.g., 2.0 = 200%) */
  speedUpThreshold: number;
  /** Buffer ratio threshold to trigger slow down (e.g., 0.6 = 60%) */
  speedDownThreshold: number;
  /** Maximum playback speed for catchup */
  maxSpeedUp: number;
  /** Minimum playback speed for slowdown */
  minSpeedDown: number;
  /** AudioWorklet ring buffer duration (ms) */
  audioBufferMs: number;
  /** Whether to use optimizeForLatency hint for decoders */
  optimizeForLatency: boolean;
}

// ============================================================================
// Raw Chunk Types (12-byte binary header protocol)
// ============================================================================

export type ChunkType = 'key' | 'delta' | 'init';

export interface RawChunk {
  /** Track index from server */
  trackIndex: number;
  /** Frame type: key (keyframe), delta (P/B frame), init (codec init data) */
  type: ChunkType;
  /** Presentation timestamp in milliseconds */
  timestamp: number;
  /** Server-calculated offset in milliseconds (for A/V sync) */
  offset: number;
  /** Actual frame data */
  data: Uint8Array;
}

// ============================================================================
// Track Types
// ============================================================================

export type TrackType = 'video' | 'audio' | 'meta';

export interface TrackInfo {
  idx: number;
  type: TrackType;
  codec: string;
  codecstring?: string;
  init?: string;
  // Video-specific
  width?: number;
  height?: number;
  fpks?: number; // frames per kilosecond
  // Audio-specific
  channels?: number;
  rate?: number; // sample rate
  size?: number; // bits per sample
}

// ============================================================================
// WebSocket Control Messages
// ============================================================================

export type ControlMessageType =
  | 'codec_data'
  | 'info'
  | 'on_time'
  | 'tracks'
  | 'set_speed'
  | 'pause'
  | 'on_stop'
  | 'error';

export interface CodecDataMessage {
  type: 'codec_data';
  /** Current stream position in milliseconds */
  current?: number;
  /** List of WebCodecs-compatible codec strings (e.g., "avc1.42001f", "mp4a.40.2") */
  codecs?: string[];
  /** Track indices selected by server based on supported_combinations */
  tracks?: number[];
}

/**
 * Stream info message with track metadata
 * Sent by MistServer with full stream information
 */
export interface InfoMessage {
  type: 'info';
  /** Stream metadata including tracks */
  meta?: {
    tracks?: Record<string, TrackInfo>;
  };
  /** Stream type ('live' or 'vod') */
  type_?: string;
}

export interface OnTimeMessage {
  type: 'on_time';
  /** Current playback time (seconds) */
  current: number;
  /** Total duration (seconds, Infinity for live) */
  total: number;
  /** Available buffer end time (seconds) */
  available?: number;
  /** End time of available content (seconds) */
  end?: number;
  /** Server-reported jitter (ms) */
  jitter?: number;
  /** Current play rate */
  play_rate?: number | 'auto';
  /** Current play rate (alias) */
  play_rate_curr?: number | 'auto' | 'fast-forward';
  /** Active track indices */
  tracks?: number[];
}

export interface TracksMessage {
  type: 'tracks';
  tracks: TrackInfo[];
  codecs?: string[];
}

export interface SetSpeedMessage {
  type: 'set_speed';
  play_rate: number | 'auto';
}

export interface PauseMessage {
  type: 'pause';
}

export interface OnStopMessage {
  type: 'on_stop';
}

export interface ErrorMessage {
  type: 'error';
  message: string;
}

export type ControlMessage =
  | CodecDataMessage
  | InfoMessage
  | OnTimeMessage
  | TracksMessage
  | SetSpeedMessage
  | PauseMessage
  | OnStopMessage
  | ErrorMessage;

// Outbound control commands
export interface PlayCommand {
  type: 'play';
}

export interface HoldCommand {
  type: 'hold';
}

export interface SeekCommand {
  type: 'seek';
  seek_time: number; // milliseconds
  ff_add?: number; // fast-forward buffer request (ms)
}

export interface SetSpeedCommand {
  type: 'set_speed';
  play_rate: number | 'auto';
}

export interface RequestCodecDataCommand {
  type: 'request_codec_data';
  /** Supported codec combinations - array of [video codecs[], audio codecs[]] */
  supported_combinations?: string[][][];
}

export interface FastForwardCommand {
  type: 'fast_forward';
  ff_add: number; // milliseconds of additional data to request
}

export type ControlCommand =
  | PlayCommand
  | HoldCommand
  | SeekCommand
  | SetSpeedCommand
  | RequestCodecDataCommand
  | FastForwardCommand;

// ============================================================================
// Worker Message Types
// ============================================================================

// Main thread -> Worker messages
export interface CreatePipelineMessage {
  type: 'create';
  idx: number;
  track: TrackInfo;
  opts: {
    optimizeForLatency: boolean;
  };
  uid?: number;
}

export interface ConfigurePipelineMessage {
  type: 'configure';
  idx: number;
  header: Uint8Array;
  uid?: number;
}

export interface ReceiveChunkMessage {
  type: 'receive';
  idx: number;
  chunk: {
    type: 'key' | 'delta';
    timestamp: number; // microseconds
    data: Uint8Array;
  };
  uid?: number;
}

export interface SetWritableMessage {
  type: 'setwritable';
  idx: number;
  writable: WritableStream;
  uid?: number;
}

export interface CreateGeneratorMessage {
  type: 'creategenerator';
  idx: number;
  uid?: number;
}

export interface ClosePipelineMessage {
  type: 'close';
  idx: number;
  waitEmpty?: boolean;
  uid?: number;
}

export interface FrameTimingMessage {
  type: 'frametiming';
  action: 'setSpeed' | 'reset';
  speed?: number;
  tweak?: number;
  uid?: number;
}

export interface SeekWorkerMessage {
  type: 'seek';
  seekTime: number; // milliseconds
  uid?: number;
}

export interface DebuggingMessage {
  type: 'debugging';
  value: boolean | 'verbose';
  uid?: number;
}

export type MainToWorkerMessage =
  | CreatePipelineMessage
  | ConfigurePipelineMessage
  | ReceiveChunkMessage
  | SetWritableMessage
  | CreateGeneratorMessage
  | ClosePipelineMessage
  | FrameTimingMessage
  | SeekWorkerMessage
  | DebuggingMessage;

// Worker -> Main thread messages
export interface AddTrackMessage {
  type: 'addtrack';
  idx: number;
  track?: MediaStreamTrack; // Safari only
  uid?: number;
}

export interface RemoveTrackMessage {
  type: 'removetrack';
  idx: number;
  uid?: number;
}

export interface SetPlaybackRateMessage {
  type: 'setplaybackrate';
  speed: number;
  uid?: number;
}

export interface ClosedMessage {
  type: 'closed';
  idx: number;
  uid?: number;
}

export interface LogMessage {
  type: 'log';
  msg: string;
  level?: 'info' | 'warn' | 'error';
  uid?: number;
}

export interface SendEventMessage {
  type: 'sendevent';
  kind: string;
  message?: string;
  idx?: number;
  uid?: number;
}

export interface StatsMessage {
  type: 'stats';
  stats: WorkerStats;
  uid?: number;
}

export interface AckMessage {
  type: 'ack';
  idx?: number;
  uid?: number;
  status?: 'ok' | 'error';
  error?: string;
}

export type WorkerToMainMessage =
  | AddTrackMessage
  | RemoveTrackMessage
  | SetPlaybackRateMessage
  | ClosedMessage
  | LogMessage
  | SendEventMessage
  | StatsMessage
  | AckMessage;

// ============================================================================
// Stats Types
// ============================================================================

export interface FrameTimingStats {
  /** Timestamp when frame entered decoder (microseconds) */
  in: number;
  /** Timestamp when frame exited decoder (microseconds) */
  decoded: number;
  /** Timestamp when frame reached trackwriter (microseconds) */
  out: number;
  speed: {
    main: number;
    tweak: number;
    combined: number;
  };
  seeking: boolean;
  paused: boolean;
}

export interface PipelineStats {
  /** How early frames arrive at trackwriter (negative = late) */
  early: number | null;
  /** Duration between frames (ms) */
  frameDuration: number | null;
  frames: {
    in: number;
    decoded: number;
    out: number;
  };
  queues: {
    in: number;
    decoder: number;
    out: number;
  };
  timing: {
    decoder: FrameTrackerStats;
    writable: FrameTrackerStats;
  };
}

export interface FrameTrackerStats {
  lastIn: number | undefined;
  lastOut: number | undefined;
  delay: number | undefined;
  delta: number | undefined;
  shift: number | undefined;
}

export interface WorkerStats {
  frameTiming: FrameTimingStats;
  pipelines: Record<number, PipelineStats>;
}

// ============================================================================
// Sync Controller Types
// ============================================================================

export interface BufferState {
  /** Current buffer level (ms) */
  current: number;
  /** Desired buffer level (ms) */
  desired: number;
  /** Ratio of current/desired */
  ratio: number;
}

export interface JitterState {
  /** Current jitter estimate (ms) */
  current: number;
  /** Peak jitter over sliding window (ms) */
  peak: number;
  /** Weighted average jitter (ms) */
  weighted: number;
}

export interface SyncState {
  buffer: BufferState;
  jitter: JitterState;
  /** Current playback speed (main * tweak) */
  playbackSpeed: number;
  /** Server-reported current time (seconds) */
  serverTime: number;
  /** Server delay estimate (ms) */
  serverDelay: number;
  /** A/V sync offset (ms, positive = audio ahead) */
  avOffset: number;
}

// ============================================================================
// Event Types
// ============================================================================

export interface WebCodecsPlayerEvents {
  ready: HTMLVideoElement;
  error: string | Error;
  play: void;
  pause: void;
  ended: void;
  timeupdate: number;
  waiting: void;
  playing: void;
  seeking: void;
  seeked: void;
  progress: void;
  trackchange: { trackIndex: number; type: TrackType };
  statsupdate: SyncState;
}

// ============================================================================
// Player Options Extensions
// ============================================================================

export interface WebCodecsPlayerOptions {
  /** Latency profile preset */
  latencyProfile?: LatencyProfileName;
  /** Custom latency profile (overrides preset) */
  customLatencyProfile?: Partial<LatencyProfile>;
  /** Enable debug logging */
  debug?: boolean;
  /** Enable verbose frame logging */
  verboseDebug?: boolean;
  /** Stats update interval (ms), 0 to disable */
  statsInterval?: number;
}

// ============================================================================
// Public Stats API (Phase 2A)
// ============================================================================

/**
 * Comprehensive player statistics for monitoring and debugging
 */
export interface WebCodecsStats {
  /** Latency/buffer statistics */
  latency: {
    /** Current buffer level (ms) */
    buffer: number;
    /** Target buffer level (ms) */
    target: number;
    /** Estimated network jitter (ms) */
    jitter: number;
  };
  /** Audio/Video sync statistics */
  sync: {
    /** A/V drift (ms, positive = video ahead of audio) */
    avDrift: number;
    /** Current playback speed (including adjustments) */
    playbackSpeed: number;
  };
  /** Decoder statistics */
  decoder: {
    /** Video decoder queue size */
    videoQueueSize: number;
    /** Audio decoder queue size */
    audioQueueSize: number;
    /** Total frames dropped */
    framesDropped: number;
    /** Total frames decoded */
    framesDecoded: number;
  };
  /** Network statistics */
  network: {
    /** Total bytes received */
    bytesReceived: number;
    /** Total messages received */
    messagesReceived: number;
  };
}

// ============================================================================
// Utility Types
// ============================================================================

export interface DeferredPromise<T> {
  promise: Promise<T>;
  resolve: (value: T) => void;
  reject: (reason?: any) => void;
}

export function createDeferredPromise<T>(): DeferredPromise<T> {
  let resolve!: (value: T) => void;
  let reject!: (reason?: any) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

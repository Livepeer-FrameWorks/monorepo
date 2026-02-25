/**
 * WebSocketController - Control Channel & Binary Frame Routing
 *
 * Manages WebSocket connection to MistServer for raw frame streaming.
 * Handles:
 * - JSON control messages (play, hold, seek, etc.)
 * - Binary frame routing to chunk parser
 * - Reconnection with exponential backoff
 * - Server delay estimation
 *
 * Based on MewsWsPlayer WebSocketManager with raw frame protocol support.
 */

import type {
  ControlMessage,
  ControlCommand,
  CodecDataMessage,
  InfoMessage,
  OnTimeMessage,
  SetSpeedMessage,
  RawChunk,
  TrackInfo,
} from "./types";
import { parseRawChunk, formatChunkForLog } from "./RawChunkParser";

/** Connection states */
export type ConnectionState =
  | "disconnected"
  | "connecting"
  | "connected"
  | "reconnecting"
  | "error";

/** Event types emitted by WebSocketController */
export interface WebSocketControllerEvents {
  statechange: ConnectionState;
  codecdata: CodecDataMessage;
  info: InfoMessage;
  ontime: OnTimeMessage;
  setspeed: SetSpeedMessage;
  tracks: TrackInfo[];
  chunk: RawChunk;
  pause: { paused: boolean; reason?: string; begin?: number; end?: number };
  stop: void;
  error: Error;
}

type EventListener<K extends keyof WebSocketControllerEvents> = (
  data: WebSocketControllerEvents[K]
) => void;

/** Options for WebSocketController */
export interface WebSocketControllerOptions {
  /** Enable debug logging */
  debug?: boolean;
  /** Maximum reconnection attempts (0 = unlimited) */
  maxReconnectAttempts?: number;
  /** Initial reconnection delay (ms) */
  reconnectDelayMs?: number;
  /** Maximum reconnection delay (ms) */
  maxReconnectDelayMs?: number;
  /** Connection timeout (ms) */
  connectionTimeoutMs?: number;
}

/** Default options */
const DEFAULTS: Required<WebSocketControllerOptions> = {
  debug: false,
  maxReconnectAttempts: 5,
  reconnectDelayMs: 1000,
  maxReconnectDelayMs: 30000,
  connectionTimeoutMs: 5000,
};

/**
 * Server delay tracker for estimating round-trip time
 */
class ServerDelayTracker {
  private delays: number[] = [];
  private pending = new Map<string, number>();
  private maxSamples = 3;

  /**
   * Start timing a request
   */
  startTiming(requestType: string): void {
    this.pending.set(requestType, performance.now());
  }

  /**
   * Complete timing and record delay
   */
  completeTiming(requestType: string): number | null {
    const startTime = this.pending.get(requestType);
    if (startTime === undefined) {
      return null;
    }

    this.pending.delete(requestType);
    const delay = performance.now() - startTime;

    this.delays.push(delay);
    if (this.delays.length > this.maxSamples) {
      this.delays.shift();
    }

    return delay;
  }

  /**
   * Get average server delay
   */
  getAverageDelay(): number {
    if (this.delays.length === 0) {
      return 0;
    }
    return this.delays.reduce((sum, d) => sum + d, 0) / this.delays.length;
  }

  /**
   * Clear all pending timings
   */
  clear(): void {
    this.pending.clear();
    this.delays = [];
  }
}

/**
 * WebSocketController - Manages raw frame WebSocket connection
 */
export class WebSocketController {
  private ws: WebSocket | null = null;
  private url: string;
  private options: Required<WebSocketControllerOptions>;
  private state: ConnectionState = "disconnected";
  private reconnectAttempts = 0;
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private connectionTimer: ReturnType<typeof setTimeout> | null = null;
  private listeners = new Map<keyof WebSocketControllerEvents, Set<Function>>();
  private serverDelay = new ServerDelayTracker();
  private intentionalClose = false;

  constructor(url: string, options: WebSocketControllerOptions = {}) {
    this.url = url;
    this.options = { ...DEFAULTS, ...options };
  }

  /**
   * Connect to WebSocket server
   */
  connect(): Promise<void> {
    return new Promise((resolve, reject) => {
      if (this.ws && this.state === "connected") {
        resolve();
        return;
      }

      this.intentionalClose = false;
      this.setState("connecting");

      try {
        this.ws = new WebSocket(this.url);
        this.ws.binaryType = "arraybuffer";

        // Connection timeout
        this.connectionTimer = setTimeout(() => {
          if (this.state === "connecting") {
            this.log("Connection timeout");
            this.ws?.close();
            reject(new Error("Connection timeout"));
          }
        }, this.options.connectionTimeoutMs);

        this.ws.onopen = () => {
          this.clearConnectionTimer();
          this.setState("connected");
          this.reconnectAttempts = 0;
          this.log("Connected");
          resolve();
        };

        this.ws.onclose = (event) => {
          this.clearConnectionTimer();
          this.log(`Disconnected: ${event.code} ${event.reason}`);

          if (!this.intentionalClose && this.shouldReconnect()) {
            this.scheduleReconnect();
          } else {
            this.setState("disconnected");
          }
        };

        this.ws.onerror = (_event) => {
          this.log("WebSocket error");
          this.emit("error", new Error("WebSocket error"));
        };

        this.ws.onmessage = (event) => {
          this.handleMessage(event);
        };
      } catch (err) {
        this.setState("error");
        reject(err);
      }
    });
  }

  /**
   * Disconnect from WebSocket server
   */
  disconnect(): void {
    this.intentionalClose = true;
    this.clearReconnectTimer();
    this.clearConnectionTimer();

    if (this.ws) {
      // Send hold command before closing
      try {
        this.send({ type: "hold" });
      } catch {
        // Ignore send errors during close
      }

      this.ws.close();
      this.ws = null;
    }

    this.serverDelay.clear();
    this.setState("disconnected");
  }

  /**
   * Send a control command
   */
  send(command: ControlCommand): boolean {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) {
      this.log(`Cannot send ${command.type}: not connected`);
      return false;
    }

    // Track timing for certain commands
    const timedCommands = ["seek", "set_speed", "request_codec_data"];
    if (timedCommands.includes(command.type)) {
      this.serverDelay.startTiming(command.type);
    }

    const message = JSON.stringify(command);
    this.log(`Sending: ${message}`);
    this.ws.send(message);
    return true;
  }

  /**
   * Request playback start
   */
  play(): boolean {
    return this.send({ type: "play" });
  }

  /**
   * Request playback pause
   */
  hold(): boolean {
    return this.send({ type: "hold" });
  }

  /**
   * Seek to position
   * @param timeMs - Target time in milliseconds
   * @param fastForwardMs - Additional buffer to request (ms)
   */
  seek(timeMs: number, fastForwardMs?: number): boolean {
    const cmd: ControlCommand = {
      type: "seek",
      seek_time: Math.round(timeMs),
    };
    if (fastForwardMs !== undefined) {
      cmd.ff_add = Math.round(fastForwardMs);
    }
    return this.send(cmd);
  }

  /**
   * Set playback speed
   */
  setSpeed(rate: number | "auto"): boolean {
    return this.send({ type: "set_speed", play_rate: rate });
  }

  /**
   * Request codec initialization data
   * @param supportedCombinations - Array of codec combinations we can play
   *   Format: [[ ["H264"], ["AAC"] ]] means "H264 video AND AAC audio"
   *   Per MistServer rawws.js line 1544
   */
  requestCodecData(supportedCombinations?: string[][][]): boolean {
    if (supportedCombinations && supportedCombinations.length > 0) {
      return this.send({
        type: "request_codec_data",
        supported_combinations: supportedCombinations,
      });
    }
    return this.send({ type: "request_codec_data" });
  }

  /**
   * Request additional data (fast-forward for buffer recovery)
   */
  fastForward(ms: number): boolean {
    return this.send({ type: "fast_forward", ff_add: Math.round(ms) });
  }

  /**
   * Get current connection state
   */
  getState(): ConnectionState {
    return this.state;
  }

  /**
   * Get estimated server delay
   */
  getServerDelay(): number {
    return this.serverDelay.getAverageDelay();
  }

  /**
   * Check if connected
   */
  isConnected(): boolean {
    return this.state === "connected" && this.ws?.readyState === WebSocket.OPEN;
  }

  /**
   * Add event listener
   */
  on<K extends keyof WebSocketControllerEvents>(event: K, listener: EventListener<K>): void {
    if (!this.listeners.has(event)) {
      this.listeners.set(event, new Set());
    }
    this.listeners.get(event)!.add(listener);
  }

  /**
   * Remove event listener
   */
  off<K extends keyof WebSocketControllerEvents>(event: K, listener: EventListener<K>): void {
    this.listeners.get(event)?.delete(listener);
  }

  /**
   * Emit event to listeners
   */
  private emit<K extends keyof WebSocketControllerEvents>(
    event: K,
    data: WebSocketControllerEvents[K]
  ): void {
    const eventListeners = this.listeners.get(event);
    if (eventListeners) {
      for (const listener of eventListeners) {
        try {
          listener(data);
        } catch (err) {
          console.error(`Error in ${event} listener:`, err);
        }
      }
    }
  }

  /**
   * Handle incoming WebSocket message
   */
  private handleMessage(event: MessageEvent): void {
    if (event.data instanceof ArrayBuffer) {
      // Binary data - parse as raw chunk
      this.handleBinaryMessage(event.data);
    } else if (typeof event.data === "string") {
      // JSON control message
      this.handleControlMessage(event.data);
    }
  }

  // Rate limit delta frame logging (log at most once per 5 seconds per track)
  private lastDeltaLogTime: Record<number, number> = {};
  private deltaLogInterval = 5000; // ms

  /**
   * Handle binary frame data
   */
  private handleBinaryMessage(data: ArrayBuffer): void {
    try {
      const chunk = parseRawChunk(data);
      if (this.options.debug) {
        // Only log KEY/INIT frames, rate-limit DELTA logs
        if (chunk.type !== "delta") {
          this.log(formatChunkForLog(chunk));
        } else {
          const now = performance.now();
          const lastLog = this.lastDeltaLogTime[chunk.trackIndex] || 0;
          if (now - lastLog > this.deltaLogInterval) {
            this.lastDeltaLogTime[chunk.trackIndex] = now;
            // Don't log delta frames at all - too spammy
          }
        }
      }
      this.emit("chunk", chunk);
    } catch (err) {
      this.log(`Failed to parse binary chunk: ${err}`);
    }
  }

  /**
   * Handle JSON control message
   *
   * Per MistServer util.js line 1301, we need to unwrap the `data` field:
   * MistServer sends: { type: "codec_data", data: { codecs: [...], tracks: [...] } }
   * We need to emit: { type: "codec_data", codecs: [...], tracks: [...] }
   */
  private handleControlMessage(data: string): void {
    try {
      const raw = JSON.parse(data);

      // Match MistServer util.js line 1301: unwrap data field if present
      // Some messages (like on_answer_sdp) don't have the data key
      const payload = "data" in raw ? raw.data : raw;
      const message: ControlMessage = { type: raw.type, ...payload };

      this.log(`Received: ${message.type}`);

      // Complete timing for responses
      if (message.type === "codec_data") {
        this.serverDelay.completeTiming("request_codec_data");
      } else if (message.type === "seek") {
        this.serverDelay.completeTiming("seek");
      } else if (message.type === "set_speed") {
        this.serverDelay.completeTiming("set_speed");
      }

      // Route to appropriate handler
      switch (message.type) {
        case "codec_data":
          this.emit("codecdata", message as CodecDataMessage);
          break;

        case "info":
          this.log(
            `Info message tracks: ${JSON.stringify(Object.keys((message as InfoMessage).meta?.tracks ?? {}))}`
          );
          this.emit("info", message as InfoMessage);
          break;

        case "on_time": {
          const otMsg = message as OnTimeMessage;
          if (otMsg.tracks?.length) {
            this.log(`on_time tracks: ${JSON.stringify(otMsg.tracks)}`);
          }
          // TEMP: log seekable range from on_time
          if (otMsg.begin !== undefined || otMsg.end !== undefined) {
            this.log(`on_time begin=${otMsg.begin} end=${otMsg.end} current=${otMsg.current}`);
          }
          this.emit("ontime", otMsg);
          break;
        }

        case "tracks":
          this.emit("tracks", (message as any).tracks);
          break;

        case "on_stop":
          this.emit("stop", undefined);
          break;

        case "error":
          this.emit("error", new Error((message as any).message));
          break;

        case "pause":
          // Server-initiated pause (e.g., buffer underrun on server side)
          // Preserve reason/begin/end for dead-point recovery (OG util.js:1697)
          this.emit("pause", {
            paused: !!(message as any).paused,
            reason: (message as any).reason,
            begin: (message as any).begin,
            end: (message as any).end,
          });
          break;

        case "set_speed":
          this.emit("setspeed", message as SetSpeedMessage);
          break;

        case "seek":
          // Seek acknowledgment from server (expected after send({type:"seek"}).
          // OG rawws.js does not treat this as unknown/noise.
          break;

        default:
          this.log(`Unknown message type: ${(message as any).type}`);
      }
    } catch (err) {
      this.log(`Failed to parse control message: ${err}`);
    }
  }

  /**
   * Set connection state and emit event
   */
  private setState(state: ConnectionState): void {
    if (this.state !== state) {
      this.state = state;
      this.emit("statechange", state);
    }
  }

  /**
   * Check if should attempt reconnection
   */
  private shouldReconnect(): boolean {
    if (this.intentionalClose) {
      return false;
    }

    if (this.options.maxReconnectAttempts === 0) {
      return true; // Unlimited retries
    }

    return this.reconnectAttempts < this.options.maxReconnectAttempts;
  }

  /**
   * Schedule reconnection with exponential backoff
   */
  private scheduleReconnect(): void {
    this.setState("reconnecting");
    this.reconnectAttempts++;

    // Exponential backoff with jitter
    const baseDelay = this.options.reconnectDelayMs;
    const maxDelay = this.options.maxReconnectDelayMs;
    const delay = Math.min(baseDelay * Math.pow(2, this.reconnectAttempts - 1), maxDelay);
    const jitter = delay * 0.2 * Math.random(); // +/- 20% jitter

    this.log(`Reconnecting in ${Math.round(delay + jitter)}ms (attempt ${this.reconnectAttempts})`);

    this.reconnectTimer = setTimeout(() => {
      this.connect().catch((err) => {
        this.log(`Reconnect failed: ${err.message}`);
        if (this.shouldReconnect()) {
          this.scheduleReconnect();
        } else {
          this.setState("error");
          this.emit("error", new Error("Max reconnection attempts exceeded"));
        }
      });
    }, delay + jitter);
  }

  /**
   * Clear reconnect timer
   */
  private clearReconnectTimer(): void {
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
  }

  /**
   * Clear connection timer
   */
  private clearConnectionTimer(): void {
    if (this.connectionTimer) {
      clearTimeout(this.connectionTimer);
      this.connectionTimer = null;
    }
  }

  /**
   * Log message (if debug enabled)
   */
  private log(message: string): void {
    if (this.options.debug) {
      console.log(`[WebCodecsWS] ${message}`);
    }
  }
}

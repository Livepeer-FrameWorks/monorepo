/**
 * MistSignaling - WebSocket signaling for MistServer's native WebRTC protocol
 *
 * Protocol messages (from MistServer):
 * - on_connected: WebSocket opened successfully
 * - on_disconnected: WebSocket closed
 * - on_answer_sdp: SDP answer received, { result: boolean, answer_sdp: string }
 * - on_time: Time update { current: ms, end: ms, begin: ms, tracks: string[] }
 * - on_stop: Stream ended
 * - on_error: Error occurred { message: string }
 *
 * Protocol messages (to MistServer):
 * - offer_sdp: SDP offer { offer_sdp: string }
 * - seek: Seek to position { seek_time: number | "live" }
 * - play: Resume playback
 * - hold: Pause playback
 * - stop: Stop playback
 * - tracks: Select tracks { video: string, audio: string }
 * - set_speed: Set playback rate { play_rate: number | "auto" }
 */

import { TypedEventEmitter } from "./EventEmitter";

export interface MistSignalingConfig {
  /** WebSocket URL (will convert http to ws) */
  url: string;
  /** Connection timeout in ms (default: 5000) */
  timeout?: number;
  /** Callback for debug logging */
  onLog?: (message: string) => void;
}

export interface MistTimeUpdate {
  /** Current playback position in ms */
  current: number;
  /** End position in ms (0 for live) */
  end: number;
  /** Begin position in ms (buffer start) */
  begin: number;
  /** Currently active track IDs */
  tracks?: string[];
  /** Whether playback is paused */
  paused?: boolean;
  /** Whether at live point */
  live_point?: boolean;
}

export interface MistSignalingEvents {
  /** Connection established */
  connected: void;
  /** Connection closed */
  disconnected: { code: number };
  /** SDP answer received */
  answer_sdp: { result: boolean; answer_sdp: string };
  /** Time/track update */
  time_update: MistTimeUpdate;
  /** Seek completed */
  seeked: { live_point?: boolean };
  /** Playback speed changed */
  speed_changed: {
    play_rate: number | "auto" | "fast-forward";
    play_rate_curr: number | "auto" | "fast-forward";
  };
  /** Server-initiated pause (e.g., buffer underrun at_dead_point) */
  pause_request: { paused: boolean; reason?: string; begin?: number; end?: number };
  /** Stream ended */
  stopped: void;
  /** Error occurred */
  error: { message: string };
}

export type MistSignalingState = "connecting" | "connected" | "disconnected" | "closed";

/**
 * MistSignaling handles WebSocket communication with MistServer for WebRTC
 */
export class MistSignaling extends TypedEventEmitter<MistSignalingEvents> {
  private ws: WebSocket | null = null;
  private url: string;
  private timeout: number;
  private timeoutId: ReturnType<typeof setTimeout> | null = null;
  private onLog: (message: string) => void;
  private _state: MistSignalingState = "disconnected";

  // Promise for seek operation
  public seekPromise: {
    resolve: (msg: string) => void;
    reject: (msg: string) => void;
  } | null = null;

  constructor(config: MistSignalingConfig) {
    super();
    // Convert http(s) to ws(s)
    this.url = config.url.replace(/^http/, "ws");
    this.timeout = config.timeout ?? 5000;
    this.onLog = config.onLog ?? (() => {});
  }

  /**
   * Get current connection state
   */
  get state(): MistSignalingState {
    return this._state;
  }

  /**
   * Check if connected
   */
  get isConnected(): boolean {
    return this._state === "connected";
  }

  /**
   * Connect to MistServer WebSocket
   */
  connect(): void {
    if (
      this.ws &&
      (this.ws.readyState === WebSocket.OPEN || this.ws.readyState === WebSocket.CONNECTING)
    ) {
      this.onLog("Already connected or connecting");
      return;
    }

    this._state = "connecting";
    this.onLog(`Connecting to ${this.url}`);

    try {
      this.ws = new WebSocket(this.url);
    } catch (e) {
      this.onLog(`Failed to create WebSocket: ${e}`);
      this._state = "disconnected";
      return;
    }

    // Connection timeout
    this.timeoutId = setTimeout(() => {
      if (this.ws && this.ws.readyState === WebSocket.CONNECTING) {
        this.onLog("WebSocket connection timeout");
        this.ws.close();
        this._state = "disconnected";
        this.emit("error", { message: "Connection timeout" });
      }
    }, this.timeout);

    this.ws.onopen = () => {
      if (this.timeoutId) {
        clearTimeout(this.timeoutId);
        this.timeoutId = null;
      }
      this._state = "connected";
      this.onLog("WebSocket connected");
      this.emit("connected", undefined);
    };

    this.ws.onmessage = (event) => {
      try {
        const cmd = JSON.parse(event.data);
        this.handleMessage(cmd);
      } catch (err) {
        this.onLog(`Failed to parse message: ${err}`);
      }
    };

    this.ws.onclose = (event) => {
      if (this.timeoutId) {
        clearTimeout(this.timeoutId);
        this.timeoutId = null;
      }
      this._state = "closed";
      this.onLog(`WebSocket closed (code: ${event.code})`);
      this.emit("disconnected", { code: event.code });
    };

    this.ws.onerror = (event) => {
      this.onLog(`WebSocket error: ${event}`);
    };
  }

  /**
   * Handle incoming message from MistServer
   */
  private handleMessage(cmd: Record<string, unknown>): void {
    const type = cmd.type as string;
    const payload =
      cmd.data && typeof cmd.data === "object" ? (cmd.data as Record<string, unknown>) : cmd;

    switch (type) {
      case "on_connected":
        // Already handled by onopen
        break;

      case "on_disconnected":
        this._state = "disconnected";
        this.emit("disconnected", { code: (payload.code as number) || 0 });
        break;

      case "on_answer_sdp":
        this.emit("answer_sdp", {
          result: payload.result as boolean,
          answer_sdp: payload.answer_sdp as string,
        });
        break;

      case "on_time":
        this.emit("time_update", {
          current: payload.current as number,
          end: payload.end as number,
          begin: payload.begin as number,
          tracks: payload.tracks as string[] | undefined,
          paused: payload.paused as boolean | undefined,
          live_point: payload.live_point as boolean | undefined,
        });
        break;

      case "seek":
        this.emit("seeked", {
          live_point: payload.live_point as boolean | undefined,
        });
        // Resolve seek promise if pending
        if (this.seekPromise) {
          this.seekPromise.resolve("Seeked");
          this.seekPromise = null;
        }
        break;

      case "set_speed":
        this.emit("speed_changed", {
          play_rate: payload.play_rate as number | "auto" | "fast-forward",
          play_rate_curr: payload.play_rate_curr as number | "auto" | "fast-forward",
        });
        break;

      case "pause":
        this.emit("pause_request", {
          paused: payload.paused as boolean,
          reason: payload.reason as string | undefined,
          begin: payload.begin as number | undefined,
          end: payload.end as number | undefined,
        });
        break;

      case "on_stop":
        this.emit("stopped", undefined);
        break;

      case "on_error":
        this.emit("error", { message: payload.message as string });
        break;

      default:
        this.onLog(`Unhandled message type: ${type}`);
    }
  }

  /**
   * Send a message to MistServer
   */
  send(cmd: Record<string, unknown>): boolean {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) {
      this.onLog("Cannot send: WebSocket not connected");
      return false;
    }

    try {
      this.ws.send(JSON.stringify(cmd));
      return true;
    } catch (e) {
      this.onLog(`Failed to send message: ${e}`);
      return false;
    }
  }

  /**
   * Send SDP offer to MistServer
   */
  sendOfferSDP(sdp: string): boolean {
    return this.send({ type: "offer_sdp", offer_sdp: sdp });
  }

  /**
   * Seek to position (in seconds or "live")
   */
  seek(time: number | "live"): Promise<string> {
    return new Promise((resolve, reject) => {
      if (!this.isConnected) {
        reject("Not connected");
        return;
      }

      // Cancel previous seek if pending
      if (this.seekPromise) {
        this.seekPromise.reject("New seek requested");
      }

      // Send seek command (time in ms for MistServer)
      const seekTime = time === "live" ? "live" : time * 1000;
      this.send({ type: "seek", seek_time: seekTime });

      // Store promise handlers
      this.seekPromise = { resolve, reject };
    });
  }

  /**
   * Resume playback
   */
  play(): boolean {
    return this.send({ type: "play" });
  }

  /**
   * Pause playback (hold)
   */
  pause(): boolean {
    return this.send({ type: "hold" });
  }

  /**
   * Stop playback
   */
  stop(): boolean {
    return this.send({ type: "stop" });
  }

  /**
   * Set track selection
   * @param video - Video track selection (e.g., "~1080x720", "|500000", "none")
   * @param audio - Audio track selection (e.g., "eng", "none")
   */
  setTracks(options: { video?: string; audio?: string }): boolean {
    return this.send({ type: "tracks", ...options });
  }

  /**
   * Set playback speed
   * @param rate - Playback rate (1.0 normal, "auto" for live catch-up)
   */
  setSpeed(rate: number | "auto"): boolean {
    return this.send({ type: "set_speed", play_rate: rate });
  }

  /**
   * Close the connection
   */
  close(): void {
    if (this.timeoutId) {
      clearTimeout(this.timeoutId);
      this.timeoutId = null;
    }

    if (this.seekPromise) {
      this.seekPromise.reject("Connection closed");
      this.seekPromise = null;
    }

    if (this.ws) {
      this._state = "closed";
      this.ws.close();
      this.ws = null;
    }
  }

  /**
   * Destroy and cleanup
   */
  destroy(): void {
    this.close();
    this.removeAllListeners();
  }
}

export default MistSignaling;

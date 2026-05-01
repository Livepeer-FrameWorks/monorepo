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
import type { MistCommand, MistEvent, MistPlayRate } from "./mist/protocol";
import type { MistMediaTransport, MistOnceHandle } from "./mist/transport";
import { MistSignalingTransport } from "./mist/transports/signaling-transport";

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
  /** Current server-side playback rate */
  play_rate_curr?: MistPlayRate;
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
    play_rate: MistPlayRate;
    play_rate_curr: MistPlayRate;
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
  private readonly _transport: MistSignalingTransport;
  private readonly url: string;
  private readonly timeout: number;
  private timeoutId: ReturnType<typeof setTimeout> | null = null;
  private readonly onLog: (message: string) => void;
  private _state: MistSignalingState = "disconnected";
  private seekHandle: MistOnceHandle<Extract<MistEvent, { type: "seek" }>> | null = null;
  private suppressNextTransportDisconnect = false;

  // Promise for seek operation
  public seekPromise: {
    resolve: (msg: string) => void;
    reject: (msg: string) => void;
  } | null = null;

  constructor(config: MistSignalingConfig) {
    super();
    this.url = config.url.replace(/^http/, "ws");
    this.timeout = config.timeout ?? 5000;
    this.onLog = config.onLog ?? (() => {});
    this._transport = new MistSignalingTransport(this.url, { maxReconnectAttempts: -1 });
    this.bindTransport();
  }

  /**
   * Underlying typed Mist media transport.
   */
  get transport(): MistMediaTransport {
    return this._transport;
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
    if (this._transport.state === "connected" || this._transport.state === "connecting") {
      this.onLog("Already connected or connecting");
      return;
    }

    this._state = "connecting";
    this.onLog(`Connecting to ${this.url}`);

    this.timeoutId = setTimeout(() => {
      if (this._state === "connecting") {
        this.onLog("WebSocket connection timeout");
        this.suppressNextTransportDisconnect = true;
        this._transport.disconnect("Connection timeout");
        this._state = "disconnected";
        this.emit("error", { message: "Connection timeout" });
      }
    }, this.timeout);

    void this._transport.connect().catch((e) => {
      this.clearTimeout();
      if (this.suppressNextTransportDisconnect) {
        this.suppressNextTransportDisconnect = false;
        return;
      }
      this.onLog(`Failed to create WebSocket: ${e}`);
      this._state = "disconnected";
    });
  }

  /**
   * Send a message to MistServer
   */
  send(cmd: Record<string, unknown>): boolean {
    if (!this.isConnected) {
      this.onLog("Cannot send: WebSocket not connected");
      return false;
    }

    const sent = this._transport.send(cmd as MistCommand);
    if (!sent) {
      this.onLog("Failed to send message");
    }
    return sent;
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

      if (this.seekPromise) {
        this.seekPromise.reject("New seek requested");
        this.seekHandle?.cancel("New seek requested");
      }

      const seekTime = time === "live" ? "live" : time * 1000;
      this.seekHandle = this._transport.once("seek");
      this.seekHandle.promise
        .then(() => {
          resolve("Seeked");
          if (this.seekPromise?.resolve === resolve) {
            this.seekPromise = null;
            this.seekHandle = null;
          }
        })
        .catch((e) => {
          reject(e instanceof Error ? e.message : String(e));
          if (this.seekPromise?.reject === reject) {
            this.seekPromise = null;
            this.seekHandle = null;
          }
        });

      this.seekPromise = { resolve, reject };

      if (!this.send({ type: "seek", seek_time: seekTime })) {
        this.seekHandle.cancel("Send failed");
        this.seekPromise = null;
        this.seekHandle = null;
        reject("Send failed");
      }
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
    this.clearTimeout();

    if (this.seekPromise) {
      this.seekPromise.reject("Connection closed");
      this.seekPromise = null;
    }
    this.seekHandle?.cancel("Connection closed");
    this.seekHandle = null;

    if (this._state !== "disconnected") {
      this._state = "closed";
    }
    this._transport.disconnect("Connection closed");
  }

  /**
   * Destroy and cleanup
   */
  destroy(): void {
    this.close();
    this._transport.destroy();
    this.removeAllListeners();
  }

  private bindTransport(): void {
    this._transport.on("statechange", ({ state, code }) => {
      switch (state) {
        case "connecting":
        case "reconnecting":
          this._state = "connecting";
          break;
        case "connected":
          this.clearTimeout();
          this._state = "connected";
          this.onLog("WebSocket connected");
          this.emit("connected", undefined);
          break;
        case "disconnected":
          this.clearTimeout();
          if (this.suppressNextTransportDisconnect) {
            this.suppressNextTransportDisconnect = false;
            break;
          }
          if (this._state === "connecting" && code === undefined) {
            this._state = "disconnected";
            break;
          }
          this._state = "closed";
          this.onLog(`WebSocket closed (code: ${code ?? 0})`);
          this.emit("disconnected", { code: code ?? 0 });
          break;
        case "closed":
          this._state = "closed";
          break;
      }
    });

    this._transport.on("event", ({ event }) => this.handleEvent(event));
    this._transport.on("error", ({ message }) => {
      this.onLog(`WebSocket error: ${message}`);
    });
  }

  /**
   * Handle incoming message from MistServer
   */
  private handleEvent(event: MistEvent): void {
    switch (event.type) {
      case "on_connected":
        break;

      case "on_disconnected":
        this._state = "disconnected";
        this.emit("disconnected", { code: event.code ?? 0 });
        break;

      case "on_answer_sdp":
        this.emit("answer_sdp", {
          result: event.result ?? false,
          answer_sdp: event.answer_sdp ?? "",
        });
        break;

      case "on_time":
        this.emit("time_update", {
          current: event.current,
          end: event.end,
          begin: event.begin,
          tracks: event.tracks as string[] | undefined,
          paused: event.paused,
          live_point: event.live_point,
          play_rate_curr: event.play_rate_curr,
        });
        break;

      case "seek":
        this.emit("seeked", {
          live_point: event.live_point,
        });
        break;

      case "set_speed":
        this.emit("speed_changed", {
          play_rate: event.play_rate ?? "auto",
          play_rate_curr: event.play_rate_curr ?? event.play_rate ?? "auto",
        });
        break;

      case "pause":
        this.emit("pause_request", {
          paused: event.paused ?? false,
          reason: event.reason,
          begin: event.begin,
          end: event.end,
        });
        break;

      case "on_stop":
        this.emit("stopped", undefined);
        break;

      case "on_error":
      case "error":
        this.emit("error", { message: event.message ?? "Unknown Mist signaling error" });
        break;

      default:
        this.onLog(`Unhandled message type: ${event.type}`);
    }
  }

  private clearTimeout(): void {
    if (this.timeoutId) {
      clearTimeout(this.timeoutId);
      this.timeoutId = null;
    }
  }
}

export default MistSignaling;

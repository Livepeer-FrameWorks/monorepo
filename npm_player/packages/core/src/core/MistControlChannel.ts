/**
 * MistControlChannel - RTCDataChannel wrapper for MistServer's JSON control protocol
 *
 * Mirrors upstream embed/util.js ControlChannel. Used by NativePlayer (WHEP) to enable
 * seeking, time tracking, and playback control over the data channel.
 *
 * Protocol messages (from MistServer):
 * - on_time: { current, end, begin, tracks, play_rate_curr, paused, live_point }
 * - seek: Seek confirmation { live_point? }
 * - pause: { paused, reason?, begin?, end? }  (reason: "at_dead_point" for buffer underrun)
 * - set_speed: { play_rate, play_rate_curr }
 * - on_stop: Stream ended
 * - on_error: { message }
 *
 * Protocol messages (to MistServer):
 * - seek: { seek_time: ms | "live" }
 * - hold: Pause playback
 * - play: Resume playback
 * - set_speed: { play_rate: number | "auto" }
 */

import { TypedEventEmitter } from "./EventEmitter";

export interface MistControlTimeUpdate {
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
  /** Current playback rate */
  play_rate_curr?: number | "auto" | "fast-forward";
}

export interface MistControlChannelEvents {
  open: void;
  close: void;
  time_update: MistControlTimeUpdate;
  seeked: { live_point?: boolean };
  speed_changed: {
    play_rate: number | "auto" | "fast-forward";
    play_rate_curr: number | "auto" | "fast-forward";
  };
  stopped: void;
  pause: { paused: boolean; reason?: string; begin?: number; end?: number };
  control_error: { message: string };
}

export class MistControlChannel extends TypedEventEmitter<MistControlChannelEvents> {
  private channel: RTCDataChannel;
  private queue: Record<string, unknown>[] = [];
  private _isOpen = false;

  constructor(channel: RTCDataChannel) {
    super();
    this.channel = channel;

    channel.addEventListener("open", () => {
      this._isOpen = true;
      for (const msg of this.queue) {
        this.send(msg);
      }
      this.queue = [];
      this.emit("open", undefined);
    });

    channel.addEventListener("close", () => {
      this._isOpen = false;
      this.emit("close", undefined);
    });

    channel.addEventListener("message", (e) => {
      if (typeof e.data !== "string") return;
      try {
        const msg = JSON.parse(e.data);
        this.handleMessage(msg);
      } catch {}
    });

    if (channel.readyState === "open") {
      this._isOpen = true;
    }
  }

  get isOpen(): boolean {
    return this._isOpen;
  }

  private handleMessage(msg: Record<string, unknown>): void {
    const payload =
      msg.data && typeof msg.data === "object" ? (msg.data as Record<string, unknown>) : msg;

    switch (msg.type) {
      case "on_time":
        this.emit("time_update", {
          current: payload.current as number,
          end: payload.end as number,
          begin: payload.begin as number,
          tracks: payload.tracks as string[] | undefined,
          paused: payload.paused as boolean | undefined,
          live_point: payload.live_point as boolean | undefined,
          play_rate_curr: payload.play_rate_curr as number | "auto" | "fast-forward" | undefined,
        });
        break;
      case "seek":
        this.emit("seeked", {
          live_point: payload.live_point as boolean | undefined,
        });
        break;
      case "set_speed":
        this.emit("speed_changed", {
          play_rate: payload.play_rate as number | "auto" | "fast-forward",
          play_rate_curr: payload.play_rate_curr as number | "auto" | "fast-forward",
        });
        break;
      case "on_stop":
        this.emit("stopped", undefined);
        break;
      case "pause":
        this.emit("pause", {
          paused: payload.paused as boolean,
          reason: payload.reason as string | undefined,
          begin: payload.begin as number | undefined,
          end: payload.end as number | undefined,
        });
        break;
      case "on_error":
        this.emit("control_error", { message: payload.message as string });
        break;
    }
  }

  send(cmd: Record<string, unknown>): boolean {
    if (!this._isOpen) {
      this.queue.push(cmd);
      return false;
    }
    try {
      this.channel.send(JSON.stringify(cmd));
      return true;
    } catch {
      return false;
    }
  }

  /** Seek to position in ms or "live" */
  seek(timeMs: number | "live"): void {
    this.send({
      type: "seek",
      seek_time: timeMs === "live" ? "live" : timeMs,
    });
  }

  /** Pause playback (hold) */
  hold(): void {
    this.send({ type: "hold" });
  }

  /** Resume playback */
  play(): void {
    this.send({ type: "play" });
  }

  /** Set playback speed */
  setSpeed(rate: number | "auto"): void {
    this.send({ type: "set_speed", play_rate: rate });
  }

  close(): void {
    try {
      this.channel.close();
    } catch {}
  }
}

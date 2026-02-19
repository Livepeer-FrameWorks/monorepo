/**
 * MetadataWebSocket - Separate WebSocket for metadata/subtitle track delivery
 *
 * MistServer delivers metadata tracks (subtitles, scores, events) on a separate
 * JSON WebSocket at `output.js?rate=1`, distinct from the binary raw frame socket.
 *
 * Port of OG player.js metaTrackSubscriptions system (lines 696-946).
 *
 * Features:
 * - Per-track subscription with callback routing
 * - Time-synchronized delivery via check() loop
 * - Overflow protection (hold when metadata runs too far ahead)
 * - Player event sync (seek, pause, play, rate change)
 * - Automatic reconnection on close
 */

import type { MetaTrackEvent, MetaTrackEventType } from "../../types";

interface MetaMessage {
  time: number;
  track: string;
  data: string;
  duration?: number;
}

interface Subscription {
  buffer: MetaMessage[];
  callbacks: Array<(event: MetaTrackEvent) => void>;
}

export interface MetadataWebSocketOptions {
  debug?: boolean;
  /** Seconds to request ahead of current playback (default: 1) */
  stayAhead?: number;
}

type MetadataCallback = (event: MetaTrackEvent) => void;

export class MetadataWebSocket {
  private ws: WebSocket | null = null;
  private url: string;
  private getCurrentTime: () => number;
  private getPlaybackRate: () => number;
  private getPaused: () => boolean;
  private subscriptions: Record<string, Subscription> = {};
  private sendQueue: object[] = [];
  private checkTimer: ReturnType<typeof setTimeout> | null = null;
  private lastFfTime = Date.now();
  private isFarAhead = false;
  private farAheadTimer: ReturnType<typeof setTimeout> | null = null;
  private destroyed = false;
  private options: Required<MetadataWebSocketOptions>;

  constructor(
    url: string,
    getCurrentTime: () => number,
    getPlaybackRate: () => number,
    getPaused: () => boolean,
    options: MetadataWebSocketOptions = {}
  ) {
    this.url = url;
    this.getCurrentTime = getCurrentTime;
    this.getPlaybackRate = getPlaybackRate;
    this.getPaused = getPaused;
    this.options = {
      debug: options.debug ?? false,
      stayAhead: options.stayAhead ?? 1,
    };
  }

  /**
   * Subscribe to a metadata track. Pass "all" to receive events from all tracks.
   * Returns an unsubscribe function.
   */
  subscribe(trackId: string, callback: MetadataCallback): () => void {
    if (!(trackId in this.subscriptions)) {
      this.subscriptions[trackId] = { buffer: [], callbacks: [] };
    }
    this.subscriptions[trackId].callbacks.push(callback);

    if (this.ws === null) {
      this.connect();
    } else {
      this.sendTrackSelection();
    }

    return () => this.unsubscribe(trackId, callback);
  }

  /**
   * Remove a specific callback from a track subscription.
   * Destroys the socket when no subscriptions remain.
   */
  unsubscribe(trackId: string, callback: MetadataCallback): void {
    const sub = this.subscriptions[trackId];
    if (!sub) return;

    const idx = sub.callbacks.indexOf(callback);
    if (idx !== -1) {
      sub.callbacks.splice(idx, 1);
    }

    if (sub.callbacks.length === 0) {
      delete this.subscriptions[trackId];
      if (Object.keys(this.subscriptions).length > 0) {
        this.sendTrackSelection();
      } else {
        this.disconnect();
      }
    }
  }

  /** Notify metadata socket that playback has seeked */
  notifySeek(): void {
    this.clearBuffers();
    this.stopCheckTimer();
    const currentMs = Math.round(this.getCurrentTime() * 1e3);
    const ffTo = Math.round((this.getCurrentTime() + this.options.stayAhead) * 1e3);
    this.send({ type: "seek", seek_time: currentMs, ff_to: ffTo });
    this.lastFfTime = Date.now();
  }

  /** Notify metadata socket that playback has paused */
  notifyPause(): void {
    this.send({ type: "hold" });
    this.stopCheckTimer();
  }

  /** Notify metadata socket that playback has resumed */
  notifyPlay(): void {
    this.send({ type: "play" });
    if (!this.checkTimer) this.check();
  }

  /** Notify metadata socket of playback rate change */
  notifyRateChange(rate: number): void {
    this.send({ type: "set_speed", play_rate: rate });
  }

  /** Clean up everything */
  destroy(): void {
    this.destroyed = true;
    this.disconnect();
    this.subscriptions = {};
  }

  private connect(): void {
    if (this.destroyed) return;

    this.log("Connecting metadata socket");
    this.ws = new WebSocket(this.url);
    this.sendQueue = [];
    this.checkTimer = null;

    this.ws.onopen = () => {
      this.log("Metadata socket opened");
      this.sendTrackSelection();

      const rate = this.getPlaybackRate();
      if (rate !== 1) {
        this.send({ type: "set_speed", play_rate: rate });
      }

      const currentMs = Math.round(this.getCurrentTime() * 1e3);
      const ffTo = Math.round((this.getCurrentTime() + this.options.stayAhead) * 1e3);
      this.send({ type: "seek", seek_time: currentMs, ff_to: ffTo });
      this.lastFfTime = Date.now();

      this.ws!.onmessage = (e: MessageEvent) => this.handleMessage(e);
      this.ws!.onclose = () => {
        this.log("Metadata socket closed");
      };

      // Drain queued messages
      while (this.sendQueue.length && this.ws?.readyState === WebSocket.OPEN) {
        const msg = this.sendQueue.shift()!;
        this.ws!.send(JSON.stringify(msg));
      }
    };

    this.ws.onerror = () => {
      this.log("Metadata socket error");
    };
  }

  private disconnect(): void {
    this.stopCheckTimer();
    if (this.farAheadTimer) {
      clearTimeout(this.farAheadTimer);
      this.farAheadTimer = null;
    }
    if (this.ws) {
      this.ws.onopen = null;
      this.ws.onmessage = null;
      this.ws.onclose = null;
      this.ws.onerror = null;
      this.ws.close();
      this.ws = null;
    }
    this.sendQueue = [];
    this.isFarAhead = false;
  }

  private send(obj: object): void {
    if (this.ws?.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify(obj));
      return;
    }

    if (this.ws && this.ws.readyState >= WebSocket.CLOSING) {
      this.connect();
    }

    this.sendQueue.push(obj);
  }

  private sendTrackSelection(): void {
    const trackIds = Object.keys(this.subscriptions)
      .filter((id) => id !== "all")
      .join(",");
    if (trackIds) {
      this.send({ type: "tracks", meta: trackIds });
    }
  }

  private handleMessage(event: MessageEvent): void {
    if (!event.data) return;

    let message: any;
    try {
      message = JSON.parse(event.data as string);
    } catch {
      this.log("Invalid metadata message");
      return;
    }

    // Data message: {time, track, data}
    if ("time" in message && "track" in message && "data" in message) {
      this.bufferMessage(message as MetaMessage);
    }

    // Control messages
    if ("type" in message) {
      switch (message.type) {
        case "on_time":
          this.handleOnTime(message);
          break;
        case "seek":
          this.clearBuffers();
          this.stopCheckTimer();
          this.log("Cleared metadata buffer after server seek");
          break;
      }
    }
  }

  private bufferMessage(msg: MetaMessage): void {
    let pushed = false;

    if ("all" in this.subscriptions) {
      this.subscriptions.all.buffer.push(msg);
      pushed = true;
    }
    if (msg.track in this.subscriptions) {
      this.subscriptions[msg.track].buffer.push(msg);
      pushed = true;
    }

    if (pushed) {
      if (!this.checkTimer) {
        this.check();
      } else {
        // If this message should display sooner than the scheduled check, reset
        const msgDisplayTime = Date.now() + msg.time - this.getCurrentTime() * 1e3;
        const scheduledCheck = this.checkScheduledAt;
        if (scheduledCheck && scheduledCheck > msgDisplayTime) {
          this.stopCheckTimer();
          this.check();
        }
      }
    }
  }

  private checkScheduledAt: number | null = null;

  /**
   * Time-synchronized delivery loop (port of OG player.js:822-868).
   * Delivers buffered messages whose time <= currentTime, drops stale ones,
   * schedules the next check for the earliest pending message.
   */
  private check(): void {
    this.stopCheckTimer();

    if (this.getPaused()) return;

    const currentTimeMs = this.getCurrentTime() * 1e3;
    let nextAtGlobal: number | null = null;

    for (const trackId of Object.keys(this.subscriptions)) {
      const sub = this.subscriptions[trackId];
      const buffer = sub.buffer;

      while (buffer.length && buffer[0].time <= currentTimeMs) {
        const msg = buffer.shift()!;

        // Drop messages more than 5 seconds behind
        if (msg.time < (this.getCurrentTime() - 5) * 1e3) {
          continue;
        }

        const event = this.toMetaTrackEvent(msg);
        for (const cb of sub.callbacks) {
          try {
            cb(event);
          } catch (err) {
            console.error("Metadata callback error:", err);
          }
        }
      }

      if (buffer.length) {
        nextAtGlobal = Math.min(nextAtGlobal ?? 1e9, buffer[0].time);
      }
    }

    // Rate-limited fast_forward (every 5 seconds)
    const now = Date.now();
    if (now > this.lastFfTime + 5e3) {
      const ffTo = Math.round((this.getCurrentTime() + this.options.stayAhead) * 1e3);
      this.send({ type: "fast_forward", ff_to: ffTo });
      this.lastFfTime = now;
    }

    if (nextAtGlobal !== null) {
      const delay = nextAtGlobal - currentTimeMs;
      this.checkScheduledAt = Date.now() + delay;
      this.checkTimer = setTimeout(
        () => {
          this.checkTimer = null;
          this.checkScheduledAt = null;
          this.check();
        },
        Math.max(0, delay)
      );
    }
  }

  /** Hold the metadata socket when it runs too far ahead of playback */
  private handleOnTime(message: any): void {
    const serverCurrent = message.data?.current ?? message.current;
    if (serverCurrent === undefined) return;

    const threshold = (this.getCurrentTime() + this.options.stayAhead * 6) * 1e3;

    if (!this.isFarAhead && serverCurrent > threshold) {
      this.isFarAhead = true;
      this.send({ type: "hold" });
      this.log(`Pausing metadata buffer (too far ahead: ${serverCurrent} > ${threshold})`);

      this.farAheadTimer = setTimeout(() => {
        this.farAheadTimer = null;
        this.isFarAhead = false;
        if (!this.getPaused()) {
          this.send({ type: "play" });
        }
        const ffTo = Math.round((this.getCurrentTime() + this.options.stayAhead) * 1e3);
        this.send({ type: "fast_forward", ff_to: ffTo });
      }, 5000);
    }
  }

  private toMetaTrackEvent(msg: MetaMessage): MetaTrackEvent {
    const eventType = this.guessEventType(msg);
    return {
      type: eventType,
      timestamp: msg.time,
      trackId: String(msg.track),
      data: msg.data,
    };
  }

  private guessEventType(_msg: MetaMessage): MetaTrackEventType {
    // MistServer doesn't send an explicit event type — infer from track metadata
    // or just default to "subtitle" since that's the primary use case
    return "subtitle";
  }

  private clearBuffers(): void {
    for (const trackId of Object.keys(this.subscriptions)) {
      this.subscriptions[trackId].buffer = [];
    }
  }

  private stopCheckTimer(): void {
    if (this.checkTimer) {
      clearTimeout(this.checkTimer);
      this.checkTimer = null;
      this.checkScheduledAt = null;
    }
  }

  private log(msg: string): void {
    if (this.options.debug) {
      console.log(`[MetadataWS] ${msg}`);
    }
  }
}

/**
 * Build the metadata WebSocket URL from a raw data WebSocket URL.
 * Raw data uses `output.js` (or similar), metadata uses the same URL with `?rate=1`.
 */
export function buildMetadataWsUrl(rawWsUrl: string): string {
  const url = new URL(rawWsUrl);
  url.searchParams.set("rate", "1");
  return url.toString();
}

import type { MetaTrackEvent, MetaTrackEventType } from "../types";
import { TimerManager } from "./TimerManager";

export interface MetaTrackSubscription {
  trackId: string;
  callback: (event: MetaTrackEvent) => void;
}

export interface MetaTrackManagerConfig {
  /** MistServer base URL */
  mistBaseUrl: string;
  /** Stream name */
  streamName: string;
  /** Initial subscriptions */
  subscriptions?: MetaTrackSubscription[];
  /** Debug logging */
  debug?: boolean;
  /** Buffer ahead duration in seconds (default: 5) */
  bufferAhead?: number;
  /** Max age for messages in seconds before filtering (default: 5) */
  maxMessageAge?: number;
  /** Fast-forward interval in seconds for catching up (default: 5) */
  fastForwardInterval?: number;
}

type ConnectionState = "disconnected" | "connecting" | "connected" | "reconnecting";

/**
 * MetaTrackManager - Handles real-time metadata subscriptions via MistServer WebSocket
 *
 * Uses native MistServer WebSocket protocol (from embed/player.js):
 * - Connect: ws://{baseUrl}/json_{streamName}.js?rate=1
 * - Set tracks: {type:"tracks", meta:"1,2,3"} (comma-separated indices)
 * - Seek: {type:"seek", seek_time:<ms>, ff_to:<ms>}
 * - Receive: {time:<ms>, track:<index>, data:{...}}
 * - Control: {type:"hold"}, {type:"play"}, {type:"fast_forward", ff_to:<ms>}
 *
 * Features:
 * - Automatic reconnection with exponential backoff
 * - Message buffering during reconnection
 * - Type detection for subtitle/score/event/chapter data
 * - Stay-ahead buffering for smooth playback
 *
 * @example
 * ```ts
 * const manager = new MetaTrackManager({
 *   mistBaseUrl: 'https://mist.example.com',
 *   streamName: 'pk_...', // playbackId (view key)
 * });
 *
 * manager.subscribe('1', (event) => {
 *   if (event.type === 'subtitle') {
 *     console.log('Subtitle:', event.data);
 *   }
 * });
 *
 * manager.connect();
 * ```
 */
export class MetaTrackManager {
  private config: MetaTrackManagerConfig;
  private ws: WebSocket | null = null;
  private state: ConnectionState = "disconnected";
  private subscriptions: Map<string, Set<(event: MetaTrackEvent) => void>> = new Map();
  private pendingSubscriptions: Set<string> = new Set();
  private reconnectAttempt = 0;
  private timers = new TimerManager();
  private messageBuffer: MetaTrackEvent[] = [];
  private debug: boolean;
  private connectionId: number = 0; // Track connection attempts to prevent stale callbacks

  // Debounce time for rapid mount/unmount cycles (ms)
  private static readonly CONNECTION_DEBOUNCE_MS = 100;

  // Reconnection settings
  private static readonly MAX_RECONNECT_ATTEMPTS = 5;
  private static readonly INITIAL_RECONNECT_DELAY = 1000;
  private static readonly MAX_RECONNECT_DELAY = 30000;
  private static readonly MESSAGE_BUFFER_SIZE = 100;

  // Buffer management (MistMetaPlayer feature backport)
  private currentPlaybackTime = 0;
  private bufferAhead: number;
  private maxMessageAge: number;
  private fastForwardInterval: number;
  private lastFastForwardTime = 0;
  private timedEventBuffer: Map<string, MetaTrackEvent[]> = new Map(); // trackId -> events sorted by time

  constructor(config: MetaTrackManagerConfig) {
    this.config = config;
    this.debug = config.debug ?? false;

    // Buffer management settings (MistMetaPlayer defaults)
    this.bufferAhead = config.bufferAhead ?? 5;
    this.maxMessageAge = config.maxMessageAge ?? 5;
    this.fastForwardInterval = config.fastForwardInterval ?? 5;

    // Add initial subscriptions
    if (config.subscriptions) {
      for (const sub of config.subscriptions) {
        this.subscribe(sub.trackId, sub.callback);
      }
    }
  }

  /**
   * Connect to MistServer WebSocket
   * Debounced to prevent orphaned connections during rapid mount/unmount cycles.
   */
  connect(): void {
    if (this.state === "connecting" || this.state === "connected") {
      return;
    }

    this.state = "connecting";
    this.log("Connecting...");

    // Increment connection ID to invalidate any pending callbacks
    const currentConnectionId = ++this.connectionId;

    // Debounce connection
    this.timers.start(
      () => {
        // Check if this connection attempt is still valid
        if (this.state !== "connecting" || this.connectionId !== currentConnectionId) {
          return;
        }

        this.createWebSocket(currentConnectionId);
      },
      MetaTrackManager.CONNECTION_DEBOUNCE_MS,
      "connect"
    );
  }

  /**
   * Internal method to create WebSocket after debounce
   */
  private createWebSocket(connectionId: number): void {
    try {
      const wsUrl = this.buildWsUrl();
      this.ws = new WebSocket(wsUrl);

      this.ws.onopen = () => {
        // Verify still valid
        if (this.connectionId !== connectionId) {
          this.ws?.close();
          return;
        }

        this.log("Connected");
        this.state = "connected";
        this.reconnectAttempt = 0;

        // Merge pending subscriptions into existing
        for (const trackId of this.pendingSubscriptions) {
          if (!this.subscriptions.has(trackId)) {
            this.subscriptions.set(trackId, new Set());
          }
        }
        this.pendingSubscriptions.clear();

        // Send all subscribed tracks at once (MistServer protocol)
        this.sendTracksUpdate();

        // Send initial seek to current playback position
        this.sendSeek(this.currentPlaybackTime);

        // Flush message buffer
        this.flushMessageBuffer();
      };

      this.ws.onmessage = (event) => {
        this.handleMessage(event.data);
      };

      this.ws.onerror = (event) => {
        this.log("WebSocket error");
        console.warn("[MetaTrackManager] WebSocket error:", event);
      };

      this.ws.onclose = () => {
        this.log("Disconnected");
        this.ws = null;

        if (this.state !== "disconnected") {
          this.scheduleReconnect();
        }
      };
    } catch (error) {
      this.log(`Connection error: ${error}`);
      this.scheduleReconnect();
    }
  }

  /**
   * Disconnect from MistServer
   */
  disconnect(): void {
    this.state = "disconnected";

    // Clear all timers
    this.timers.destroy();

    if (this.ws) {
      this.ws.close();
      this.ws = null;
    }
  }

  /**
   * Subscribe to a meta track
   * @param trackId Track index (number as string) or "all" for all meta tracks
   */
  subscribe(trackId: string, callback: (event: MetaTrackEvent) => void): () => void {
    const isNewTrack = !this.subscriptions.has(trackId);

    if (isNewTrack) {
      this.subscriptions.set(trackId, new Set());
    }

    this.subscriptions.get(trackId)!.add(callback);

    // Send updated track list if connected and this is a new track
    if (this.state === "connected" && this.ws && isNewTrack) {
      this.sendTracksUpdate();
    } else if (isNewTrack) {
      this.pendingSubscriptions.add(trackId);
    }

    // Return unsubscribe function
    return () => this.unsubscribe(trackId, callback);
  }

  /**
   * Unsubscribe from a meta track
   */
  unsubscribe(trackId: string, callback: (event: MetaTrackEvent) => void): void {
    const callbacks = this.subscriptions.get(trackId);
    if (callbacks) {
      callbacks.delete(callback);

      // If no more callbacks, remove subscription and update MistServer
      if (callbacks.size === 0) {
        this.subscriptions.delete(trackId);
        // Send updated track list (MistServer doesn't have explicit unsubscribe)
        if (this.state === "connected" && this.ws) {
          this.sendTracksUpdate();
        }
      }
    }
  }

  /**
   * Get list of subscribed track IDs
   */
  getSubscribedTracks(): string[] {
    return Array.from(this.subscriptions.keys());
  }

  /**
   * Get connection state
   */
  getState(): ConnectionState {
    return this.state;
  }

  /**
   * Check if connected
   */
  isConnected(): boolean {
    return this.state === "connected";
  }

  // ========================================
  // Buffer Management (MistMetaPlayer backport)
  // ========================================

  /**
   * Update current playback time
   * Call this on video timeupdate events to keep buffer in sync
   */
  setPlaybackTime(timeInSeconds: number): void {
    this.currentPlaybackTime = timeInSeconds;

    // Process any buffered events that are now due
    this.processTimedEvents();
  }

  /**
   * Get current playback time
   */
  getPlaybackTime(): number {
    return this.currentPlaybackTime;
  }

  /**
   * Handle seek event - clears buffer and sends seek command to MistServer
   * Call this when video seeks to a new position
   */
  onSeek(newTimeInSeconds: number): void {
    this.log(`Seek to ${newTimeInSeconds}s - clearing buffer and notifying server`);
    this.currentPlaybackTime = newTimeInSeconds;

    // Clear all timed event buffers
    this.timedEventBuffer.clear();

    // Reset fast-forward tracking
    this.lastFastForwardTime = 0;

    // Tell MistServer to seek its metadata stream
    this.sendSeek(newTimeInSeconds);
  }

  /**
   * Process buffered events up to current playback time
   * Dispatches events that are ready to be shown
   */
  private processTimedEvents(): void {
    const now = this.currentPlaybackTime * 1000; // Convert to ms

    for (const [trackId, events] of this.timedEventBuffer) {
      // Find events that should be dispatched
      const dueEvents: MetaTrackEvent[] = [];
      const remainingEvents: MetaTrackEvent[] = [];

      for (const event of events) {
        if (event.timestamp <= now) {
          // Check if event is too old (filter stale events)
          const ageSeconds = (now - event.timestamp) / 1000;
          if (ageSeconds <= this.maxMessageAge) {
            dueEvents.push(event);
          } else {
            this.log(`Filtering stale event (${ageSeconds.toFixed(1)}s old)`);
          }
        } else {
          remainingEvents.push(event);
        }
      }

      // Dispatch due events
      for (const event of dueEvents) {
        this.dispatchEvent(event);
      }

      // Update buffer with remaining events
      if (remainingEvents.length > 0) {
        this.timedEventBuffer.set(trackId, remainingEvents);
      } else {
        this.timedEventBuffer.delete(trackId);
      }
    }
  }

  /**
   * Add event to timed buffer (sorted by timestamp)
   * Used for events that should be dispatched at specific playback times
   */
  private addToTimedBuffer(event: MetaTrackEvent): void {
    const trackId = event.trackId;

    if (!this.timedEventBuffer.has(trackId)) {
      this.timedEventBuffer.set(trackId, []);
    }

    const buffer = this.timedEventBuffer.get(trackId)!;

    // Insert in sorted order by timestamp
    let insertIndex = buffer.length;
    for (let i = 0; i < buffer.length; i++) {
      if (buffer[i].timestamp > event.timestamp) {
        insertIndex = i;
        break;
      }
    }
    buffer.splice(insertIndex, 0, event);

    // Limit buffer size per track
    while (buffer.length > MetaTrackManager.MESSAGE_BUFFER_SIZE) {
      buffer.shift();
    }
  }

  /**
   * Check if we need to request more data (stay bufferAhead seconds ahead)
   * Returns true if buffer is running low
   */
  needsMoreData(trackId: string): boolean {
    const buffer = this.timedEventBuffer.get(trackId);
    if (!buffer || buffer.length === 0) return true;

    const lastEventTime = buffer[buffer.length - 1].timestamp / 1000;
    const currentTime = this.currentPlaybackTime;
    const bufferedAhead = lastEventTime - currentTime;

    return bufferedAhead < this.bufferAhead;
  }

  /**
   * Fast-forward through buffered events (rate-limited)
   * Used when playback jumps ahead and needs to catch up
   * Also notifies MistServer to fast-forward its metadata stream
   */
  fastForward(): void {
    const now = Date.now();

    // Rate limit fast-forward (once per fastForwardInterval seconds)
    if (now - this.lastFastForwardTime < this.fastForwardInterval * 1000) {
      return;
    }

    this.lastFastForwardTime = now;
    this.log("Fast-forwarding through buffered events");

    // Process all events up to current time + bufferAhead
    const targetTime = (this.currentPlaybackTime + this.bufferAhead) * 1000;

    for (const [trackId, events] of this.timedEventBuffer) {
      const processEvents: MetaTrackEvent[] = [];
      const remainingEvents: MetaTrackEvent[] = [];

      for (const event of events) {
        if (event.timestamp <= targetTime) {
          // Only dispatch if not too old
          const ageSeconds = (this.currentPlaybackTime * 1000 - event.timestamp) / 1000;
          if (ageSeconds <= this.maxMessageAge) {
            processEvents.push(event);
          }
        } else {
          remainingEvents.push(event);
        }
      }

      // Dispatch events
      for (const event of processEvents) {
        this.dispatchEvent(event);
      }

      // Update buffer
      if (remainingEvents.length > 0) {
        this.timedEventBuffer.set(trackId, remainingEvents);
      } else {
        this.timedEventBuffer.delete(trackId);
      }
    }

    // Tell MistServer to fast-forward as well
    this.sendFastForward(this.currentPlaybackTime + this.bufferAhead);
  }

  /**
   * Get buffer status for debugging
   */
  getBufferStatus(): Record<string, { count: number; oldestMs: number; newestMs: number }> {
    const status: Record<string, { count: number; oldestMs: number; newestMs: number }> = {};

    for (const [trackId, events] of this.timedEventBuffer) {
      if (events.length > 0) {
        status[trackId] = {
          count: events.length,
          oldestMs: events[0].timestamp,
          newestMs: events[events.length - 1].timestamp,
        };
      }
    }

    return status;
  }

  /**
   * Build WebSocket URL for MistServer meta track subscription
   * Uses the same endpoint as JSON info polling, just over WebSocket
   */
  private buildWsUrl(): string {
    const baseUrl = this.config.mistBaseUrl
      .replace(/^http:/, "ws:")
      .replace(/^https:/, "wss:")
      .replace(/\/$/, "");

    // MistServer meta track WebSocket uses /json_<streamname>.js endpoint
    // The rate=1 param tells MistServer to stream metadata in real-time
    return `${baseUrl}/json_${this.config.streamName}.js?rate=1`;
  }

  /**
   * Send tracks update to MistServer
   * MistServer protocol: {type:"tracks", meta:"1,2,3"} (comma-separated track indices)
   */
  private sendTracksUpdate(): void {
    if (this.ws && this.ws.readyState === WebSocket.OPEN) {
      const trackIds = Array.from(this.subscriptions.keys());
      // Support "all" as special track ID to subscribe to all meta tracks
      const metaValue = trackIds.includes("all") ? "all" : trackIds.join(",");

      const message = JSON.stringify({
        type: "tracks",
        meta: metaValue,
      });
      this.ws.send(message);
      this.log(`Set tracks: ${metaValue}`);
    }
  }

  /**
   * Send seek command to MistServer
   * MistServer protocol: {type:"seek", seek_time:<ms>, ff_to:<ms>}
   */
  private sendSeek(timeInSeconds: number): void {
    if (this.ws && this.ws.readyState === WebSocket.OPEN) {
      const seekTimeMs = Math.round(timeInSeconds * 1000);
      const ffToMs = Math.round((timeInSeconds + this.bufferAhead) * 1000);

      const message = JSON.stringify({
        type: "seek",
        seek_time: seekTimeMs,
        ff_to: ffToMs,
      });
      this.ws.send(message);
      this.log(`Seek to ${timeInSeconds}s, buffer ahead to ${timeInSeconds + this.bufferAhead}s`);
    }
  }

  /**
   * Send hold command (pause metadata delivery)
   */
  private sendHold(): void {
    if (this.ws && this.ws.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify({ type: "hold" }));
      this.log("Sent hold");
    }
  }

  /**
   * Send play command (resume metadata delivery)
   */
  private sendPlay(): void {
    if (this.ws && this.ws.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify({ type: "play" }));
      this.log("Sent play");
    }
  }

  /**
   * Send fast-forward command
   */
  private sendFastForward(targetTimeSeconds: number): void {
    if (this.ws && this.ws.readyState === WebSocket.OPEN) {
      const message = JSON.stringify({
        type: "fast_forward",
        ff_to: Math.round(targetTimeSeconds * 1000),
      });
      this.ws.send(message);
      this.log(`Fast-forward to ${targetTimeSeconds}s`);
    }
  }

  /**
   * Handle incoming WebSocket message
   * MistServer format:
   * - Metadata: {time:<ms>, track:<index>, data:{...}}
   * - Status: {type:"on_time", data:{current:<ms>}}
   * - Seek complete: {type:"seek", ...}
   */
  private handleMessage(data: string): void {
    try {
      const parsed = JSON.parse(data);

      // Handle metadata event: {time, track, data}
      if ("time" in parsed && "track" in parsed && "data" in parsed) {
        const event = this.parseMetaTrackEvent(parsed);

        // Check if we're subscribed to this track (or "all")
        const trackId = String(parsed.track);
        if (this.subscriptions.has(trackId) || this.subscriptions.has("all")) {
          // Subtitles and chapters should be buffered for timed playback
          // Other events (scores, generic events) dispatch immediately
          if (event.type === "subtitle" || event.type === "chapter") {
            this.addToTimedBuffer(event);
            // Also process immediately in case we're already past this time
            this.processTimedEvents();
          } else {
            // Dispatch immediately for non-timed events
            this.dispatchEvent(event);
          }
        }
        return;
      }

      // Handle server status messages: {type:..., ...}
      if ("type" in parsed) {
        switch (parsed.type) {
          case "on_time":
            // Server time update - can be used for buffer management
            if (parsed.data?.current) {
              const serverTimeMs = parsed.data.current;
              const playerTimeMs = this.currentPlaybackTime * 1000;
              const aheadMs = serverTimeMs - playerTimeMs;

              // If server is too far ahead, pause and wait
              if (aheadMs > this.bufferAhead * 6 * 1000) {
                this.log(`Server ${aheadMs}ms ahead, sending hold`);
                this.sendHold();
              }
            }
            break;

          case "seek":
            // Seek completed - clear buffers
            this.log("Server confirmed seek, clearing buffers");
            this.timedEventBuffer.clear();
            break;

          default:
            this.log(`Unknown message type: ${parsed.type}`);
        }
      }
    } catch (error) {
      this.log(`Failed to parse message: ${error}`);
    }
  }

  /**
   * Parse meta track event from MistServer message
   * MistServer format: {time:<ms>, track:<index>, data:{...}}
   */
  private parseMetaTrackEvent(message: {
    track: string | number;
    time: number;
    data?: unknown;
    [key: string]: unknown;
  }): MetaTrackEvent {
    const trackId = String(message.track);
    const timestamp = Number(message.time);
    const data = message.data ?? message;

    // Detect event type from data shape
    const type = this.detectEventType(data);

    return {
      type,
      timestamp,
      trackId,
      data,
    };
  }

  /**
   * Detect event type from data shape
   */
  private detectEventType(data: unknown): MetaTrackEventType {
    if (typeof data !== "object" || data === null) {
      return "unknown";
    }

    const obj = data as Record<string, unknown>;

    // Subtitle: has text, startTime/endTime
    if ("text" in obj && ("startTime" in obj || "start" in obj)) {
      return "subtitle";
    }

    // Score: has key and value
    if ("key" in obj && "value" in obj) {
      return "score";
    }

    // Chapter: has title and startTime
    if ("title" in obj && "startTime" in obj) {
      return "chapter";
    }

    // Event: has name
    if ("name" in obj) {
      return "event";
    }

    return "unknown";
  }

  /**
   * Dispatch event to subscribers
   */
  private dispatchEvent(event: MetaTrackEvent): void {
    const callbacks = this.subscriptions.get(event.trackId);
    if (callbacks) {
      for (const callback of callbacks) {
        try {
          callback(event);
        } catch (error) {
          console.error("[MetaTrackManager] Callback error:", error);
        }
      }
    }
  }

  /**
   * Schedule reconnection attempt
   */
  private scheduleReconnect(): void {
    if (this.state === "disconnected") return;

    if (this.reconnectAttempt >= MetaTrackManager.MAX_RECONNECT_ATTEMPTS) {
      this.log("Max reconnect attempts reached");
      this.state = "disconnected";
      return;
    }

    this.state = "reconnecting";
    this.reconnectAttempt++;

    const delay = Math.min(
      MetaTrackManager.INITIAL_RECONNECT_DELAY * Math.pow(2, this.reconnectAttempt - 1),
      MetaTrackManager.MAX_RECONNECT_DELAY
    );

    this.log(`Reconnecting in ${delay}ms (attempt ${this.reconnectAttempt})`);

    this.timers.start(
      () => {
        this.connect();
      },
      delay,
      "reconnect"
    );
  }

  /**
   * Buffer message for later delivery
   */
  private bufferMessage(event: MetaTrackEvent): void {
    this.messageBuffer.push(event);

    // Limit buffer size
    while (this.messageBuffer.length > MetaTrackManager.MESSAGE_BUFFER_SIZE) {
      this.messageBuffer.shift();
    }
  }

  /**
   * Flush buffered messages to subscribers
   */
  private flushMessageBuffer(): void {
    const buffered = [...this.messageBuffer];
    this.messageBuffer = [];

    for (const event of buffered) {
      this.dispatchEvent(event);
    }
  }

  /**
   * Debug logging
   */
  private log(message: string): void {
    if (this.debug) {
      console.debug(`[MetaTrackManager] ${message}`);
    }
  }
}

export default MetaTrackManager;

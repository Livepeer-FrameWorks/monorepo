/**
 * StreamStateClient.ts
 *
 * Framework-agnostic client for polling MistServer stream status via WebSocket or HTTP.
 * Extracted from useStreamState.ts for use in headless core.
 */

import { TypedEventEmitter } from "./EventEmitter";
import { TimerManager } from "./TimerManager";
import type { StreamState, StreamStatus, MistStreamInfo } from "../types";

// ============================================================================
// Types
// ============================================================================

export interface StreamStateClientConfig {
  /** MistServer base URL (e.g., https://mist.example.com) */
  mistBaseUrl: string;
  /** Stream name to poll */
  streamName: string;
  /** Poll interval in ms for HTTP fallback (default: 3000) */
  pollInterval?: number;
  /** Use WebSocket if available (default: true) */
  useWebSocket?: boolean;
}

type StreamStateClientResolvedConfig = Omit<
  StreamStateClientConfig,
  "pollInterval" | "useWebSocket"
> & {
  pollInterval: number;
  useWebSocket: boolean;
};

export interface StreamStateClientEvents {
  /** Emitted when stream state changes */
  stateChange: { state: StreamState };
  /** Emitted when stream comes online */
  online: void;
  /** Emitted when stream goes offline */
  offline: void;
  /** Emitted on connection error */
  error: { error: string };
}

// ============================================================================
// Constants
// ============================================================================

const DEFAULT_POLL_INTERVAL = 3000;

const initialState: StreamState = {
  status: "OFFLINE",
  isOnline: false,
  message: "Connecting...",
  lastUpdate: 0,
};

// ============================================================================
// Helper Functions
// ============================================================================

/**
 * Parse MistServer error string into StreamStatus enum.
 */
function parseErrorToStatus(error: string): StreamStatus {
  const lowerError = error.toLowerCase();

  if (lowerError.includes("offline")) return "OFFLINE";
  if (lowerError.includes("initializing")) return "INITIALIZING";
  if (lowerError.includes("booting")) return "BOOTING";
  if (lowerError.includes("waiting for data")) return "WAITING_FOR_DATA";
  if (lowerError.includes("shutting down")) return "SHUTTING_DOWN";
  if (lowerError.includes("invalid")) return "INVALID";

  return "ERROR";
}

/**
 * Get human-readable message for stream status.
 */
function getStatusMessage(status: StreamStatus, percentage?: number): string {
  switch (status) {
    case "ONLINE":
      return "Stream is online";
    case "OFFLINE":
      return "Stream is offline";
    case "INITIALIZING":
      return percentage !== undefined
        ? `Initializing... ${Math.round(percentage * 10) / 10}%`
        : "Stream is initializing";
    case "BOOTING":
      return "Stream is starting up";
    case "WAITING_FOR_DATA":
      return "Waiting for stream data";
    case "SHUTTING_DOWN":
      return "Stream is shutting down";
    case "INVALID":
      return "Stream status is invalid";
    case "ERROR":
    default:
      return "Stream error";
  }
}

// ============================================================================
// StreamStateClient Class
// ============================================================================

/**
 * Client for polling MistServer stream status via WebSocket or HTTP.
 *
 * @example
 * ```typescript
 * const client = new StreamStateClient({
 *   mistBaseUrl: 'https://mist.example.com',
 *   streamName: 'pk_...', // playbackId (view key)
 * });
 *
 * client.on('stateChange', ({ state }) => console.log('State:', state));
 * client.on('online', () => console.log('Stream is online!'));
 * client.on('offline', () => console.log('Stream is offline'));
 *
 * client.start();
 * // ...later
 * client.stop();
 * ```
 */
export class StreamStateClient extends TypedEventEmitter<StreamStateClientEvents> {
  private config: StreamStateClientResolvedConfig;
  private state: StreamState = { ...initialState };
  private ws: WebSocket | null = null;
  private timers = new TimerManager();
  private isRunning: boolean = false;
  private wasOnline: boolean = false;
  private connectionId: number = 0; // Track connection attempts to prevent stale callbacks

  // Debounce time for rapid mount/unmount cycles (ms)
  private static readonly CONNECTION_DEBOUNCE_MS = 100;

  constructor(config: StreamStateClientConfig) {
    super();
    this.config = {
      pollInterval: DEFAULT_POLL_INTERVAL,
      useWebSocket: true,
      ...config,
    };
  }

  /**
   * Start polling/WebSocket connection.
   * Always does initial HTTP poll to get full stream info (including sources),
   * then connects WebSocket for real-time status updates.
   *
   * Debounced to prevent orphaned connections during rapid mount/unmount cycles.
   */
  start(): void {
    if (this.isRunning) return;
    this.isRunning = true;

    const { mistBaseUrl, streamName, useWebSocket } = this.config;

    if (!mistBaseUrl || !streamName) {
      console.warn("[StreamStateClient] Missing mistBaseUrl or streamName");
      return;
    }

    // Reset state
    this.setState({
      ...initialState,
      message: "Connecting...",
      lastUpdate: Date.now(),
    });

    // Increment connection ID to invalidate any pending callbacks from previous attempts
    const currentConnectionId = ++this.connectionId;

    // Debounce connection to prevent rapid reconnects during mount/unmount cycles
    this.timers.start(
      () => {
        // Check if this connection attempt is still valid
        if (!this.isRunning || this.connectionId !== currentConnectionId) {
          return;
        }

        // Always do initial HTTP poll to get full data (including sources)
        // Then connect WebSocket for real-time updates
        this.pollHttp().then(() => {
          // Verify still valid before WebSocket connection
          if (useWebSocket && this.isRunning && this.connectionId === currentConnectionId) {
            this.connectWebSocket();
          }
        });
      },
      StreamStateClient.CONNECTION_DEBOUNCE_MS,
      "connect"
    );
  }

  /**
   * Stop polling and close connections.
   */
  stop(): void {
    this.isRunning = false;

    // Close WebSocket
    if (this.ws) {
      this.ws.close();
      this.ws = null;
    }

    // Clear all timers
    this.timers.destroy();
  }

  /**
   * Manual refresh - trigger an immediate poll.
   */
  refresh(): void {
    if (this.config.useWebSocket && this.ws?.readyState === WebSocket.OPEN) {
      // WebSocket will receive updates automatically
      return;
    }
    this.pollHttp();
  }

  /**
   * Get the underlying WebSocket connection (for MistReporter integration).
   * Returns null if WebSocket is not connected.
   */
  getSocket(): WebSocket | null {
    return this.ws;
  }

  /**
   * Check if the WebSocket is connected and ready.
   */
  isSocketReady(): boolean {
    return this.ws?.readyState === WebSocket.OPEN;
  }

  /**
   * Get current stream state.
   */
  getState(): StreamState {
    return { ...this.state };
  }

  /**
   * Check if stream is online.
   */
  isOnline(): boolean {
    return this.state.isOnline;
  }

  /**
   * Update configuration (stops and restarts if running).
   */
  updateConfig(config: Partial<StreamStateClientConfig>): void {
    const wasRunning = this.isRunning;
    this.stop();
    this.config = { ...this.config, ...config };
    if (wasRunning) {
      this.start();
    }
  }

  /**
   * Clean up resources.
   */
  destroy(): void {
    this.stop();
    this.removeAllListeners();
  }

  // ============================================================================
  // Private Methods
  // ============================================================================

  private connectWebSocket(): void {
    if (!this.isRunning) return;

    const { mistBaseUrl, streamName } = this.config;
    const currentConnectionId = this.connectionId;

    // Clean up existing connection
    if (this.ws) {
      this.ws.close();
      this.ws = null;
    }

    try {
      // Convert http(s) to ws(s)
      const wsUrl = mistBaseUrl
        .replace(/^http:/, "ws:")
        .replace(/^https:/, "wss:")
        .replace(/\/$/, "");

      const ws = new WebSocket(
        `${wsUrl}/json_${encodeURIComponent(streamName)}.js?metaeverywhere=1&inclzero=1`
      );
      this.ws = ws;

      ws.onopen = () => {
        console.debug("[StreamStateClient] WebSocket connected");
      };

      ws.onmessage = (event) => {
        try {
          const data = JSON.parse(event.data) as MistStreamInfo;
          this.processStreamInfo(data);
        } catch (e) {
          console.warn("[StreamStateClient] Failed to parse WebSocket message:", e);
        }
      };

      ws.onerror = () => {
        console.warn("[StreamStateClient] WebSocket error, falling back to HTTP polling");
        ws.close();
      };

      ws.onclose = () => {
        this.ws = null;

        if (!this.isRunning || this.connectionId !== currentConnectionId) return;

        // Disable WebSocket and switch to HTTP polling
        this.config.useWebSocket = false;
        console.debug("[StreamStateClient] WebSocket closed, switching to HTTP polling");
        this.pollHttp();
      };
    } catch (error) {
      console.warn("[StreamStateClient] WebSocket connection failed:", error);
      // Disable WebSocket and switch to HTTP polling
      this.config.useWebSocket = false;
      this.pollHttp();
    }
  }

  private async pollHttp(): Promise<void> {
    if (!this.isRunning) return;

    const { mistBaseUrl, streamName, pollInterval } = this.config;

    try {
      const url = `${mistBaseUrl.replace(/\/$/, "")}/json_${encodeURIComponent(streamName)}.js?metaeverywhere=1&inclzero=1`;
      const response = await fetch(url, {
        method: "GET",
        headers: { Accept: "application/json" },
      });

      if (!response.ok) {
        throw new Error(`HTTP ${response.status}`);
      }

      // MistServer returns JSON with potential JSONP wrapper
      let text = await response.text();
      // Strip JSONP callback if present
      const jsonpMatch = text.match(/^[^(]+\(([\s\S]*)\);?$/);
      if (jsonpMatch) {
        text = jsonpMatch[1];
      }

      const data = JSON.parse(text) as MistStreamInfo;
      this.processStreamInfo(data);
    } catch (error) {
      if (!this.isRunning) return;

      const errorMessage = error instanceof Error ? error.message : "Connection failed";
      this.setState({
        ...this.state,
        status: "ERROR",
        isOnline: false,
        message: errorMessage,
        lastUpdate: Date.now(),
        error: errorMessage,
      });
      this.emit("error", { error: errorMessage });
    }

    // Schedule next poll
    if (this.isRunning && !this.config.useWebSocket) {
      this.timers.start(() => this.pollHttp(), pollInterval, "poll");
    }
  }

  private processStreamInfo(data: MistStreamInfo): void {
    if (!this.isRunning) return;

    let newState: StreamState;

    if (data.error) {
      // Stream has an error state - preserve existing streamInfo
      const status = parseErrorToStatus(data.error);
      const message = data.on_error || getStatusMessage(status, data.perc);

      newState = {
        status,
        isOnline: false,
        message,
        percentage: data.perc,
        lastUpdate: Date.now(),
        error: data.error,
        streamInfo: this.state.streamInfo, // Preserve existing source/tracks
      };
    } else {
      // Stream is online with valid metadata
      // Merge new data with existing streamInfo to preserve source/tracks from initial fetch
      // WebSocket updates may not include source array
      const mergedStreamInfo: MistStreamInfo = {
        ...this.state.streamInfo, // Keep existing source/meta if present
        ...data, // Override with new data
        // Explicitly preserve source if not in new data
        source: data.source || this.state.streamInfo?.source,
        // Merge meta to preserve tracks
        meta: {
          ...this.state.streamInfo?.meta,
          ...data.meta,
          // Preserve tracks if not in new data
          tracks: data.meta?.tracks || this.state.streamInfo?.meta?.tracks,
        },
      };

      // TEMP: log buffer_window and track timing from MistServer
      const bw = mergedStreamInfo.meta?.buffer_window;
      const tracks = mergedStreamInfo.meta?.tracks;
      if (bw !== undefined || tracks) {
        const trackTiming = tracks
          ? Object.entries(tracks)
              .map(([k, t]) => `${k}:[${(t as any).firstms ?? "?"},${(t as any).lastms ?? "?"}]`)
              .join(" ")
          : "none";
        console.debug(
          `[StreamStateClient] buffer_window=${bw ?? "undefined"} tracks=${trackTiming}`
        );
      }

      newState = {
        status: "ONLINE",
        isOnline: true,
        message: "Stream is online",
        lastUpdate: Date.now(),
        streamInfo: mergedStreamInfo,
      };
    }

    this.setState(newState);

    // Emit online/offline events on state transitions
    if (newState.isOnline && !this.wasOnline) {
      this.emit("online", undefined as never);
    } else if (!newState.isOnline && this.wasOnline) {
      this.emit("offline", undefined as never);
    }
    this.wasOnline = newState.isOnline;
  }

  private setState(state: StreamState): void {
    const prevState = this.state;
    this.state = state;

    // Emit if ANY state field changed - including streamInfo (track data)
    // Previously only checked status/isOnline/message, causing track updates to be lost
    const hasChanged =
      prevState.status !== state.status ||
      prevState.isOnline !== state.isOnline ||
      prevState.message !== state.message ||
      prevState.streamInfo !== state.streamInfo ||
      prevState.lastUpdate !== state.lastUpdate;

    if (hasChanged) {
      this.emit("stateChange", { state });
    }
  }
}

export default StreamStateClient;

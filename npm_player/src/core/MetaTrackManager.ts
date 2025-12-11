import type { MetaTrackEvent, MetaTrackEventType } from '../types';

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
}

type ConnectionState = 'disconnected' | 'connecting' | 'connected' | 'reconnecting';

/**
 * MetaTrackManager - Handles real-time metadata subscriptions via MistServer WebSocket
 *
 * Uses native MistServer WebSocket protocol:
 * - Connect: ws://{baseUrl}/api/{streamName}
 * - Subscribe: {"subscribe": {"meta_track": trackId}}
 * - Receive: {"meta_track": trackId, "time": ms, "data": {...}}
 *
 * Features:
 * - Automatic reconnection with exponential backoff
 * - Message buffering during reconnection
 * - Type detection for subtitle/score/event/chapter data
 *
 * @example
 * ```ts
 * const manager = new MetaTrackManager({
 *   mistBaseUrl: 'https://mist.example.com',
 *   streamName: 'my-stream',
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
  private state: ConnectionState = 'disconnected';
  private subscriptions: Map<string, Set<(event: MetaTrackEvent) => void>> = new Map();
  private pendingSubscriptions: Set<string> = new Set();
  private reconnectAttempt = 0;
  private reconnectTimeout: ReturnType<typeof setTimeout> | null = null;
  private messageBuffer: MetaTrackEvent[] = [];
  private debug: boolean;

  // Reconnection settings
  private static readonly MAX_RECONNECT_ATTEMPTS = 5;
  private static readonly INITIAL_RECONNECT_DELAY = 1000;
  private static readonly MAX_RECONNECT_DELAY = 30000;
  private static readonly MESSAGE_BUFFER_SIZE = 100;

  constructor(config: MetaTrackManagerConfig) {
    this.config = config;
    this.debug = config.debug ?? false;

    // Add initial subscriptions
    if (config.subscriptions) {
      for (const sub of config.subscriptions) {
        this.subscribe(sub.trackId, sub.callback);
      }
    }
  }

  /**
   * Connect to MistServer WebSocket
   */
  connect(): void {
    if (this.state === 'connecting' || this.state === 'connected') {
      return;
    }

    this.state = 'connecting';
    this.log('Connecting...');

    try {
      const wsUrl = this.buildWsUrl();
      this.ws = new WebSocket(wsUrl);

      this.ws.onopen = () => {
        this.log('Connected');
        this.state = 'connected';
        this.reconnectAttempt = 0;

        // Subscribe to all pending tracks
        for (const trackId of this.pendingSubscriptions) {
          this.sendSubscribe(trackId);
        }
        this.pendingSubscriptions.clear();

        // Re-subscribe to existing subscriptions
        for (const trackId of this.subscriptions.keys()) {
          this.sendSubscribe(trackId);
        }

        // Flush message buffer
        this.flushMessageBuffer();
      };

      this.ws.onmessage = (event) => {
        this.handleMessage(event.data);
      };

      this.ws.onerror = (event) => {
        this.log('WebSocket error');
        console.warn('[MetaTrackManager] WebSocket error:', event);
      };

      this.ws.onclose = () => {
        this.log('Disconnected');
        this.ws = null;

        if (this.state !== 'disconnected') {
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
    this.state = 'disconnected';

    if (this.reconnectTimeout) {
      clearTimeout(this.reconnectTimeout);
      this.reconnectTimeout = null;
    }

    if (this.ws) {
      this.ws.close();
      this.ws = null;
    }
  }

  /**
   * Subscribe to a meta track
   */
  subscribe(trackId: string, callback: (event: MetaTrackEvent) => void): () => void {
    if (!this.subscriptions.has(trackId)) {
      this.subscriptions.set(trackId, new Set());
    }

    this.subscriptions.get(trackId)!.add(callback);

    // Send subscribe message if connected
    if (this.state === 'connected' && this.ws) {
      this.sendSubscribe(trackId);
    } else {
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

      // If no more callbacks, remove subscription entirely
      if (callbacks.size === 0) {
        this.subscriptions.delete(trackId);
        this.sendUnsubscribe(trackId);
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
    return this.state === 'connected';
  }

  /**
   * Build WebSocket URL
   */
  private buildWsUrl(): string {
    const baseUrl = this.config.mistBaseUrl
      .replace(/^http:/, 'ws:')
      .replace(/^https:/, 'wss:')
      .replace(/\/$/, '');

    return `${baseUrl}/api/${this.config.streamName}`;
  }

  /**
   * Send subscribe message
   */
  private sendSubscribe(trackId: string): void {
    if (this.ws && this.ws.readyState === WebSocket.OPEN) {
      const message = JSON.stringify({
        subscribe: { meta_track: trackId }
      });
      this.ws.send(message);
      this.log(`Subscribed to track ${trackId}`);
    }
  }

  /**
   * Send unsubscribe message
   */
  private sendUnsubscribe(trackId: string): void {
    if (this.ws && this.ws.readyState === WebSocket.OPEN) {
      const message = JSON.stringify({
        unsubscribe: { meta_track: trackId }
      });
      this.ws.send(message);
      this.log(`Unsubscribed from track ${trackId}`);
    }
  }

  /**
   * Handle incoming WebSocket message
   */
  private handleMessage(data: string): void {
    try {
      const parsed = JSON.parse(data);

      // Check for meta track data
      if ('meta_track' in parsed && 'time' in parsed) {
        const event = this.parseMetaTrackEvent(parsed);
        this.dispatchEvent(event);
      }
    } catch (error) {
      this.log(`Failed to parse message: ${error}`);
    }
  }

  /**
   * Parse meta track event from MistServer message
   */
  private parseMetaTrackEvent(message: {
    meta_track: string;
    time: number;
    data?: unknown;
    [key: string]: unknown;
  }): MetaTrackEvent {
    const trackId = String(message.meta_track);
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
    if (typeof data !== 'object' || data === null) {
      return 'unknown';
    }

    const obj = data as Record<string, unknown>;

    // Subtitle: has text, startTime/endTime
    if ('text' in obj && ('startTime' in obj || 'start' in obj)) {
      return 'subtitle';
    }

    // Score: has key and value
    if ('key' in obj && 'value' in obj) {
      return 'score';
    }

    // Chapter: has title and startTime
    if ('title' in obj && 'startTime' in obj) {
      return 'chapter';
    }

    // Event: has name
    if ('name' in obj) {
      return 'event';
    }

    return 'unknown';
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
          console.error('[MetaTrackManager] Callback error:', error);
        }
      }
    }
  }

  /**
   * Schedule reconnection attempt
   */
  private scheduleReconnect(): void {
    if (this.state === 'disconnected') return;

    if (this.reconnectAttempt >= MetaTrackManager.MAX_RECONNECT_ATTEMPTS) {
      this.log('Max reconnect attempts reached');
      this.state = 'disconnected';
      return;
    }

    this.state = 'reconnecting';
    this.reconnectAttempt++;

    const delay = Math.min(
      MetaTrackManager.INITIAL_RECONNECT_DELAY * Math.pow(2, this.reconnectAttempt - 1),
      MetaTrackManager.MAX_RECONNECT_DELAY
    );

    this.log(`Reconnecting in ${delay}ms (attempt ${this.reconnectAttempt})`);

    this.reconnectTimeout = setTimeout(() => {
      this.reconnectTimeout = null;
      this.connect();
    }, delay);
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

/**
 * WebSocket Manager for MEWS Player
 *
 * Handles WebSocket connection, reconnection with exponential backoff,
 * message sending, and typed message listeners.
 *
 * Ported from reference: mews.js:387-883
 */

import type { WebSocketManagerOptions, MewsMessage, MewsMessageListener } from './types';

export class WebSocketManager {
  private ws: WebSocket | null = null;
  private url: string;
  private maxReconnectAttempts: number;
  private reconnectAttempts = 0;
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private wasConnected = false;
  private isDestroyed = false;

  // Track pending retry timers so they can be cancelled on destroy
  private pendingRetryTimers: Set<ReturnType<typeof setTimeout>> = new Set();

  // Message listener registry (ported from mews.js:440-451)
  // Allows multiple listeners per message type for proper seek/play sequencing
  private listeners: Record<string, MewsMessageListener[]> = {};

  private onMessage: (data: ArrayBuffer | string) => void;
  private onOpen: () => void;
  private onClose: () => void;
  private onError: (message: string) => void;

  constructor(options: WebSocketManagerOptions) {
    this.url = options.url;
    this.maxReconnectAttempts = options.maxReconnectAttempts ?? 5;
    this.onMessage = options.onMessage;
    this.onOpen = options.onOpen;
    this.onClose = options.onClose;
    this.onError = options.onError;
  }

  connect(): void {
    if (this.isDestroyed) return;

    // Protocol mismatch check (non-fatal warning)
    try {
      const pageProto = window.location.protocol.replace(/^http/, 'ws');
      const srcProto = new URL(this.url, window.location.href).protocol;
      if (pageProto !== srcProto) {
        this.onError(`Protocol mismatch ${pageProto} vs ${srcProto}`);
      }
    } catch {}

    const ws = new WebSocket(this.url);
    ws.binaryType = 'arraybuffer';
    this.ws = ws;

    ws.onopen = () => {
      this.wasConnected = true;
      this.reconnectAttempts = 0;
      this.clearReconnectTimer();
      this.onOpen();
    };

    ws.onmessage = (e: MessageEvent<ArrayBuffer | string>) => {
      this.onMessage(e.data);
    };

    ws.onerror = () => {
      this.onError('WebSocket error');
    };

    ws.onclose = () => {
      if (this.isDestroyed) return;

      if (this.wasConnected && this.reconnectAttempts < this.maxReconnectAttempts) {
        const backoff = Math.min(5000, 500 * Math.pow(2, this.reconnectAttempts));
        this.reconnectAttempts++;
        this.reconnectTimer = setTimeout(() => {
          if (!this.isDestroyed) {
            this.connect();
          }
        }, backoff);
      } else {
        this.onClose();
        this.onError('WebSocket closed');
      }
    };
  }

  /**
   * Send a command with retry logic (3.3.6).
   * If not connected, will retry up to 5 times with 500ms delay.
   * If connection is closing/closed, will attempt reconnect then retry.
   */
  send(cmd: object, retry = 0): boolean {
    const MAX_RETRIES = 5;
    const RETRY_DELAY = 500;

    // Early exit if destroyed - don't schedule any retries
    if (this.isDestroyed) return false;

    if (retry > MAX_RETRIES) {
      this.onError('Too many send retries');
      return false;
    }

    const scheduleRetry = (delay: number) => {
      const timer = setTimeout(() => {
        this.pendingRetryTimers.delete(timer);
        if (!this.isDestroyed) {
          this.send(cmd, retry + 1);
        }
      }, delay);
      this.pendingRetryTimers.add(timer);
    };

    if (!this.ws) {
      // No socket at all, try to connect and retry
      if (!this.isDestroyed && retry < MAX_RETRIES) {
        scheduleRetry(RETRY_DELAY);
      }
      return false;
    }

    if (this.ws.readyState < WebSocket.OPEN) {
      // Still connecting, wait and retry (if not destroyed)
      if (!this.isDestroyed && retry < MAX_RETRIES) {
        scheduleRetry(RETRY_DELAY);
      }
      return false;
    }

    if (this.ws.readyState >= WebSocket.CLOSING) {
      // Closing or closed, trigger reconnect and retry
      if (!this.isDestroyed && retry < MAX_RETRIES) {
        this.connect();
        scheduleRetry(RETRY_DELAY * 2);
      }
      return false;
    }

    try {
      this.ws.send(JSON.stringify(cmd));
      return true;
    } catch {
      return false;
    }
  }

  private clearReconnectTimer(): void {
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
  }

  destroy(): void {
    console.debug('[WebSocketManager] destroy() called');
    this.isDestroyed = true;
    this.clearReconnectTimer();

    // Cancel ALL pending retry timers to prevent any scheduled sends
    for (const timer of this.pendingRetryTimers) {
      clearTimeout(timer);
    }
    this.pendingRetryTimers.clear();

    // Clear all listeners to prevent memory leaks
    this.listeners = {};

    if (this.ws) {
      try {
        this.ws.close();
      } catch {}
      this.ws = null;
    }
    console.debug('[WebSocketManager] destroy() completed');
  }

  isConnected(): boolean {
    return this.ws?.readyState === WebSocket.OPEN;
  }

  /**
   * Add a listener for a specific message type.
   * Ported from mews.js:441-444
   *
   * @param type - Message type to listen for (e.g., 'on_time', 'codec_data', 'seek')
   * @param callback - Function to call when message is received
   */
  addListener(type: string, callback: MewsMessageListener): void {
    if (!(type in this.listeners)) {
      this.listeners[type] = [];
    }
    this.listeners[type].push(callback);
  }

  /**
   * Remove a listener for a specific message type.
   * Ported from mews.js:445-450
   *
   * @param type - Message type
   * @param callback - The exact callback function to remove
   * @returns true if listener was found and removed
   */
  removeListener(type: string, callback: MewsMessageListener): boolean {
    if (!(type in this.listeners)) {
      return false;
    }
    const index = this.listeners[type].indexOf(callback);
    if (index < 0) {
      return false;
    }
    this.listeners[type].splice(index, 1);
    return true;
  }

  /**
   * Notify all listeners for a given message type.
   * Called internally when a JSON message is received.
   * Ported from mews.js:795-799
   *
   * @param msg - Parsed message object
   */
  notifyListeners(msg: MewsMessage): void {
    if (msg.type in this.listeners) {
      // Iterate backwards in case listeners remove themselves
      const callbacks = this.listeners[msg.type];
      for (let i = callbacks.length - 1; i >= 0; i--) {
        try {
          callbacks[i](msg);
        } catch (e) {
          // Don't let one listener crash others
          console.error('MEWS listener error:', e);
        }
      }
    }
  }
}

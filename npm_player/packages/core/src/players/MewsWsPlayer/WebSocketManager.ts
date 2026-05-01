/**
 * WebSocket Manager for MEWS Player
 *
 * Handles WebSocket connection, reconnection with exponential backoff,
 * message sending, and typed message listeners.
 *
 */

import type { WebSocketManagerOptions, MewsMessage, MewsMessageListener } from "./types";
import type { MistCommand } from "../../core/mist/protocol";
import type { MistSendDecorator } from "../../core/mist/transport";
import { MistWebSocketTransport } from "../../core/mist/transports/websocket-transport";

export class WebSocketManager {
  private transport: MistWebSocketTransport;
  private connected = false;
  private url: string;
  private maxReconnectAttempts: number;
  private reconnectAttempts = 0;
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private wasConnected = false;
  private isDestroyed = false;
  private connectionTimeout: ReturnType<typeof setTimeout> | null = null;
  private static readonly CONNECTION_TIMEOUT_MS = 5000;

  // Track pending retry timers so they can be cancelled on destroy
  private pendingRetryTimers: Set<ReturnType<typeof setTimeout>> = new Set();

  // Message listener registry
  // Allows multiple listeners per message type for proper seek/play sequencing
  private listeners: Record<string, MewsMessageListener[]> = {};

  private reconnectionDisabled = false;

  private onMessage: (data: ArrayBuffer | string) => void;
  private onOpen: () => void;
  private onClose: () => void;
  private onError: (message: string) => void;
  private shouldReconnect?: () => boolean;

  constructor(options: WebSocketManagerOptions) {
    this.url = options.url;
    this.maxReconnectAttempts = options.maxReconnectAttempts ?? 5;
    this.onMessage = options.onMessage;
    this.onOpen = options.onOpen;
    this.onClose = options.onClose;
    this.onError = options.onError;
    this.shouldReconnect = options.shouldReconnect;
    this.transport = new MistWebSocketTransport(this.url, {
      maxReconnectAttempts: this.maxReconnectAttempts,
      reconnectDelayMs: 500,
      maxReconnectDelayMs: 5000,
    });
    this.bindTransport();
  }

  connect(): void {
    if (this.isDestroyed) return;

    // Protocol mismatch check (non-fatal warning)
    try {
      const pageProto = window.location.protocol.replace(/^http/, "ws");
      const srcProto = new URL(this.url, window.location.href).protocol;
      if (pageProto !== srcProto) {
        this.onError(`Protocol mismatch ${pageProto} vs ${srcProto}`);
      }
    } catch {}

    this.clearConnectionTimeout();
    this.connectionTimeout = setTimeout(() => {
      if (!this.connected) {
        this.onError("WebSocket connection timeout");
      }
    }, WebSocketManager.CONNECTION_TIMEOUT_MS);
    void this.transport.connect().catch(() => {});
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
      this.onError("Too many send retries");
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

    if (!this.connected) {
      // No socket at all, try to connect and retry
      if (!this.isDestroyed && retry < MAX_RETRIES) {
        scheduleRetry(RETRY_DELAY);
      }
      return false;
    }

    try {
      return this.transport.send(cmd as any);
    } catch {
      return false;
    }
  }

  private clearConnectionTimeout(): void {
    if (this.connectionTimeout) {
      clearTimeout(this.connectionTimeout);
      this.connectionTimeout = null;
    }
  }

  private clearReconnectTimer(): void {
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
  }

  destroy(): void {
    console.debug("[WebSocketManager] destroy() called");
    this.isDestroyed = true;
    this.clearConnectionTimeout();
    this.clearReconnectTimer();

    // Cancel ALL pending retry timers to prevent any scheduled sends
    for (const timer of this.pendingRetryTimers) {
      clearTimeout(timer);
    }
    this.pendingRetryTimers.clear();

    // Clear all listeners to prevent memory leaks
    this.listeners = {};

    this.transport.destroy();
    this.connected = false;
    console.debug("[WebSocketManager] destroy() completed");
  }

  disableReconnection(): void {
    this.reconnectionDisabled = true;
    this.clearReconnectTimer();
  }

  sendDirect(cmd: object): boolean {
    if (!this.connected) return false;
    try {
      return this.transport.send(cmd as any);
    } catch {
      return false;
    }
  }

  addSendDecorator(decorator: MistSendDecorator<MistCommand>): () => void {
    return this.transport.addSendDecorator(decorator);
  }

  isConnected(): boolean {
    return this.connected;
  }

  private bindTransport(): void {
    this.transport.on("statechange", ({ state }) => {
      if (state === "connected") {
        this.connected = true;
        this.wasConnected = true;
        this.reconnectAttempts = 0;
        this.clearReconnectTimer();
        this.clearConnectionTimeout();
        this.onOpen();
      } else if (state === "disconnected") {
        const shouldRetry =
          !this.isDestroyed &&
          !this.reconnectionDisabled &&
          (!this.shouldReconnect || this.shouldReconnect());
        this.connected = false;
        if (!shouldRetry) {
          this.onClose();
        }
      }
    });

    this.transport.on("binary", ({ data }) => this.onMessage(data));
    this.transport.on("event", ({ event }) => this.onMessage(JSON.stringify(event)));
    this.transport.on("error", ({ message }) => this.onError(message));
  }

  /**
   * Add a listener for a specific message type.
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
          console.error("MEWS listener error:", e);
        }
      }
    }
  }
}

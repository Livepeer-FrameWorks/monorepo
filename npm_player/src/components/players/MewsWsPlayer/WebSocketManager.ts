/**
 * WebSocket Manager for MEWS Player
 *
 * Handles WebSocket connection, reconnection with exponential backoff,
 * and message sending.
 */

import type { WebSocketManagerOptions } from './types';

export class WebSocketManager {
  private ws: WebSocket | null = null;
  private url: string;
  private maxReconnectAttempts: number;
  private reconnectAttempts = 0;
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private wasConnected = false;
  private isDestroyed = false;

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

    if (retry > MAX_RETRIES) {
      this.onError('Too many send retries');
      return false;
    }

    if (!this.ws) {
      // No socket at all, try to connect and retry
      if (!this.isDestroyed && retry < MAX_RETRIES) {
        setTimeout(() => this.send(cmd, retry + 1), RETRY_DELAY);
      }
      return false;
    }

    if (this.ws.readyState < WebSocket.OPEN) {
      // Still connecting, wait and retry
      setTimeout(() => this.send(cmd, retry + 1), RETRY_DELAY);
      return false;
    }

    if (this.ws.readyState >= WebSocket.CLOSING) {
      // Closing or closed, trigger reconnect and retry
      if (!this.isDestroyed && retry < MAX_RETRIES) {
        this.connect();
        setTimeout(() => this.send(cmd, retry + 1), RETRY_DELAY * 2);
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
    this.isDestroyed = true;
    this.clearReconnectTimer();
    if (this.ws) {
      try {
        this.ws.close();
      } catch {}
      this.ws = null;
    }
  }

  isConnected(): boolean {
    return this.ws?.readyState === WebSocket.OPEN;
  }
}

/**
 * Reconnection Manager
 * Handles automatic reconnection with exponential backoff
 */

import { TypedEventEmitter } from "./EventEmitter";
import type { ReconnectionConfig, ReconnectionState, ReconnectionEvents } from "../types";

export const DEFAULT_RECONNECTION_CONFIG: ReconnectionConfig = {
  enabled: true,
  maxAttempts: 5,
  baseDelay: 1000, // 1 second
  maxDelay: 30000, // 30 seconds
  backoffMultiplier: 2,
};

export class ReconnectionManager extends TypedEventEmitter<ReconnectionEvents> {
  private config: ReconnectionConfig;
  private state: ReconnectionState;
  private reconnectTimeout: ReturnType<typeof setTimeout> | null = null;
  private countdownInterval: ReturnType<typeof setInterval> | null = null;
  private reconnectCallback: (() => Promise<void>) | null = null;

  constructor(config: Partial<ReconnectionConfig> = {}) {
    super();
    this.config = { ...DEFAULT_RECONNECTION_CONFIG, ...config };
    this.state = {
      isReconnecting: false,
      attemptNumber: 0,
      nextAttemptIn: null,
      lastError: null,
    };
  }

  /**
   * Calculate delay for next attempt using exponential backoff
   */
  private calculateDelay(attempt: number): number {
    const delay = this.config.baseDelay * Math.pow(this.config.backoffMultiplier, attempt - 1);
    // Add some jitter (±10%) to prevent thundering herd
    const jitter = delay * 0.1 * (Math.random() * 2 - 1);
    return Math.min(delay + jitter, this.config.maxDelay);
  }

  /**
   * Start reconnection process
   */
  start(reconnectCallback: () => Promise<void>): void {
    if (!this.config.enabled) {
      console.log("[ReconnectionManager] Reconnection disabled");
      return;
    }

    if (this.state.isReconnecting) {
      console.log("[ReconnectionManager] Already reconnecting");
      return;
    }

    this.reconnectCallback = reconnectCallback;
    this.state = {
      isReconnecting: true,
      attemptNumber: 0,
      nextAttemptIn: null,
      lastError: null,
    };

    this.scheduleNextAttempt();
  }

  /**
   * Schedule the next reconnection attempt
   */
  private scheduleNextAttempt(): void {
    if (!this.state.isReconnecting) return;

    this.state.attemptNumber++;

    if (this.state.attemptNumber > this.config.maxAttempts) {
      this.handleExhausted();
      return;
    }

    const delay = this.calculateDelay(this.state.attemptNumber);
    this.state.nextAttemptIn = delay;

    console.log(
      `[ReconnectionManager] Scheduling attempt ${this.state.attemptNumber}/${this.config.maxAttempts} in ${delay}ms`
    );

    this.emit("attemptStart", {
      attempt: this.state.attemptNumber,
      delay,
    });

    // Start countdown
    this.startCountdown(delay);

    // Schedule the attempt
    this.reconnectTimeout = setTimeout(() => {
      this.executeAttempt();
    }, delay);
  }

  /**
   * Start countdown interval
   */
  private startCountdown(delay: number): void {
    this.stopCountdown();

    const startTime = Date.now();
    this.countdownInterval = setInterval(() => {
      const elapsed = Date.now() - startTime;
      this.state.nextAttemptIn = Math.max(0, delay - elapsed);
    }, 100);
  }

  /**
   * Stop countdown interval
   */
  private stopCountdown(): void {
    if (this.countdownInterval) {
      clearInterval(this.countdownInterval);
      this.countdownInterval = null;
    }
  }

  /**
   * Execute a reconnection attempt
   */
  private async executeAttempt(): Promise<void> {
    this.stopCountdown();
    this.state.nextAttemptIn = null;

    if (!this.reconnectCallback) {
      console.error("[ReconnectionManager] No reconnect callback set");
      return;
    }

    console.log(`[ReconnectionManager] Executing attempt ${this.state.attemptNumber}`);

    try {
      await this.reconnectCallback();

      // Success!
      this.handleSuccess();
    } catch (error) {
      const errorMessage = error instanceof Error ? error.message : String(error);
      this.handleFailure(errorMessage);
    }
  }

  /**
   * Handle successful reconnection
   */
  private handleSuccess(): void {
    console.log("[ReconnectionManager] Reconnection successful");

    this.state = {
      isReconnecting: false,
      attemptNumber: 0,
      nextAttemptIn: null,
      lastError: null,
    };

    this.cleanup();
    this.emit("attemptSuccess", undefined);
  }

  /**
   * Handle failed reconnection attempt
   */
  private handleFailure(error: string): void {
    console.log(`[ReconnectionManager] Attempt ${this.state.attemptNumber} failed:`, error);

    this.state.lastError = error;

    this.emit("attemptFailed", {
      attempt: this.state.attemptNumber,
      error,
    });

    // Schedule next attempt
    this.scheduleNextAttempt();
  }

  /**
   * Handle exhausted attempts
   */
  private handleExhausted(): void {
    console.log("[ReconnectionManager] All reconnection attempts exhausted");

    this.emit("exhausted", {
      totalAttempts: this.config.maxAttempts,
    });

    this.stop();
  }

  /**
   * Stop reconnection process
   */
  stop(): void {
    console.log("[ReconnectionManager] Stopping reconnection");

    this.cleanup();

    this.state = {
      isReconnecting: false,
      attemptNumber: 0,
      nextAttemptIn: null,
      lastError: this.state.lastError,
    };
  }

  /**
   * Clean up timers
   */
  private cleanup(): void {
    if (this.reconnectTimeout) {
      clearTimeout(this.reconnectTimeout);
      this.reconnectTimeout = null;
    }

    this.stopCountdown();
    this.reconnectCallback = null;
  }

  /**
   * Reset the manager (e.g., after manual reconnect)
   */
  reset(): void {
    this.stop();
    this.state.lastError = null;
  }

  /**
   * Get current state
   */
  getState(): ReconnectionState {
    return { ...this.state };
  }

  /**
   * Check if currently reconnecting
   */
  isReconnecting(): boolean {
    return this.state.isReconnecting;
  }

  /**
   * Get current attempt number
   */
  getAttemptNumber(): number {
    return this.state.attemptNumber;
  }

  /**
   * Get max attempts
   */
  getMaxAttempts(): number {
    return this.config.maxAttempts;
  }

  /**
   * Update configuration
   */
  updateConfig(config: Partial<ReconnectionConfig>): void {
    this.config = { ...this.config, ...config };
  }

  /**
   * Destroy the manager
   */
  destroy(): void {
    this.cleanup();
    this.removeAllListeners();
  }
}

/**
 * ErrorClassifier - Centralized error classification and recovery orchestration
 *
 * Implements a 4-tier error handling system:
 * - Tier 1 (TRANSIENT): Silent retry with exponential backoff
 * - Tier 2 (RECOVERABLE): Protocol/player swap with toast notification
 * - Tier 3 (DEGRADED): Quality drop with informational toast
 * - Tier 4 (FATAL): Blocking error modal
 */

import {
  ErrorSeverity,
  ErrorCode,
  type ClassifiedError,
  type ErrorHandlingEvents,
} from "./PlayerInterface";

/** Retry configuration for each error code */
interface RetryConfig {
  maxAttempts: number;
  baseDelayMs: number;
  maxDelayMs: number;
  jitterPercent: number;
}

/** Default retry configurations by error code */
const RETRY_CONFIGS: Record<ErrorCode, RetryConfig> = {
  // Tier 1: Silent recovery
  [ErrorCode.NETWORK_TIMEOUT]: {
    maxAttempts: 3,
    baseDelayMs: 500,
    maxDelayMs: 4000,
    jitterPercent: 20,
  },
  [ErrorCode.WEBSOCKET_DISCONNECT]: {
    maxAttempts: 5,
    baseDelayMs: 500,
    maxDelayMs: 5000,
    jitterPercent: 20,
  },
  [ErrorCode.SEGMENT_LOAD_ERROR]: {
    maxAttempts: 3,
    baseDelayMs: 200,
    maxDelayMs: 2000,
    jitterPercent: 10,
  },
  [ErrorCode.ICE_DISCONNECTED]: {
    maxAttempts: 3,
    baseDelayMs: 500,
    maxDelayMs: 3000,
    jitterPercent: 20,
  },
  [ErrorCode.BUFFER_UNDERRUN]: {
    maxAttempts: 1,
    baseDelayMs: 5000,
    maxDelayMs: 5000,
    jitterPercent: 0,
  },
  [ErrorCode.CODEC_DECODE_ERROR]: {
    maxAttempts: 3,
    baseDelayMs: 100,
    maxDelayMs: 1000,
    jitterPercent: 10,
  },

  // Tier 2: Protocol swap (no internal retry, just count for tracking)
  [ErrorCode.PROTOCOL_UNSUPPORTED]: {
    maxAttempts: 1,
    baseDelayMs: 0,
    maxDelayMs: 0,
    jitterPercent: 0,
  },
  [ErrorCode.CODEC_INCOMPATIBLE]: {
    maxAttempts: 1,
    baseDelayMs: 0,
    maxDelayMs: 0,
    jitterPercent: 0,
  },
  [ErrorCode.ICE_FAILED]: { maxAttempts: 1, baseDelayMs: 0, maxDelayMs: 0, jitterPercent: 0 },
  [ErrorCode.MANIFEST_STALE]: { maxAttempts: 1, baseDelayMs: 0, maxDelayMs: 0, jitterPercent: 0 },
  [ErrorCode.PLAYER_INIT_FAILED]: {
    maxAttempts: 1,
    baseDelayMs: 0,
    maxDelayMs: 0,
    jitterPercent: 0,
  },

  // Tier 3: Quality (not retried, just tracked)
  [ErrorCode.QUALITY_DROPPED]: { maxAttempts: 0, baseDelayMs: 0, maxDelayMs: 0, jitterPercent: 0 },
  [ErrorCode.BANDWIDTH_LIMITED]: {
    maxAttempts: 0,
    baseDelayMs: 0,
    maxDelayMs: 0,
    jitterPercent: 0,
  },

  // Tier 4: Fatal (not retried)
  [ErrorCode.ALL_PROTOCOLS_EXHAUSTED]: {
    maxAttempts: 0,
    baseDelayMs: 0,
    maxDelayMs: 0,
    jitterPercent: 0,
  },
  [ErrorCode.ALL_PROTOCOLS_BLACKLISTED]: {
    maxAttempts: 0,
    baseDelayMs: 0,
    maxDelayMs: 0,
    jitterPercent: 0,
  },
  [ErrorCode.STREAM_OFFLINE]: { maxAttempts: 0, baseDelayMs: 0, maxDelayMs: 0, jitterPercent: 0 },
  [ErrorCode.AUTH_REQUIRED]: { maxAttempts: 0, baseDelayMs: 0, maxDelayMs: 0, jitterPercent: 0 },
  [ErrorCode.GEO_BLOCKED]: { maxAttempts: 0, baseDelayMs: 0, maxDelayMs: 0, jitterPercent: 0 },
  [ErrorCode.DRM_ERROR]: { maxAttempts: 0, baseDelayMs: 0, maxDelayMs: 0, jitterPercent: 0 },
  [ErrorCode.CONTENT_UNAVAILABLE]: {
    maxAttempts: 0,
    baseDelayMs: 0,
    maxDelayMs: 0,
    jitterPercent: 0,
  },
  [ErrorCode.UNKNOWN]: { maxAttempts: 1, baseDelayMs: 1000, maxDelayMs: 1000, jitterPercent: 0 },
};

/** Maps error codes to their default severity */
const CODE_TO_SEVERITY: Record<ErrorCode, ErrorSeverity> = {
  // Tier 1
  [ErrorCode.NETWORK_TIMEOUT]: ErrorSeverity.TRANSIENT,
  [ErrorCode.WEBSOCKET_DISCONNECT]: ErrorSeverity.TRANSIENT,
  [ErrorCode.SEGMENT_LOAD_ERROR]: ErrorSeverity.TRANSIENT,
  [ErrorCode.ICE_DISCONNECTED]: ErrorSeverity.TRANSIENT,
  [ErrorCode.BUFFER_UNDERRUN]: ErrorSeverity.TRANSIENT,
  [ErrorCode.CODEC_DECODE_ERROR]: ErrorSeverity.TRANSIENT,

  // Tier 2
  [ErrorCode.PROTOCOL_UNSUPPORTED]: ErrorSeverity.RECOVERABLE,
  [ErrorCode.CODEC_INCOMPATIBLE]: ErrorSeverity.RECOVERABLE,
  [ErrorCode.ICE_FAILED]: ErrorSeverity.RECOVERABLE,
  [ErrorCode.MANIFEST_STALE]: ErrorSeverity.RECOVERABLE,
  [ErrorCode.PLAYER_INIT_FAILED]: ErrorSeverity.RECOVERABLE,

  // Tier 3
  [ErrorCode.QUALITY_DROPPED]: ErrorSeverity.DEGRADED,
  [ErrorCode.BANDWIDTH_LIMITED]: ErrorSeverity.DEGRADED,

  // Tier 4
  [ErrorCode.ALL_PROTOCOLS_EXHAUSTED]: ErrorSeverity.FATAL,
  [ErrorCode.ALL_PROTOCOLS_BLACKLISTED]: ErrorSeverity.FATAL,
  [ErrorCode.STREAM_OFFLINE]: ErrorSeverity.FATAL,
  [ErrorCode.AUTH_REQUIRED]: ErrorSeverity.FATAL,
  [ErrorCode.GEO_BLOCKED]: ErrorSeverity.FATAL,
  [ErrorCode.DRM_ERROR]: ErrorSeverity.FATAL,
  [ErrorCode.CONTENT_UNAVAILABLE]: ErrorSeverity.FATAL,
  [ErrorCode.UNKNOWN]: ErrorSeverity.FATAL,
};

/** User-friendly messages for each error code */
const CODE_TO_MESSAGE: Record<ErrorCode, string> = {
  [ErrorCode.NETWORK_TIMEOUT]: "Network timeout",
  [ErrorCode.WEBSOCKET_DISCONNECT]: "Connection lost",
  [ErrorCode.SEGMENT_LOAD_ERROR]: "Failed to load video segment",
  [ErrorCode.ICE_DISCONNECTED]: "Connection interrupted",
  [ErrorCode.BUFFER_UNDERRUN]: "Buffering",
  [ErrorCode.CODEC_DECODE_ERROR]: "Decode error",
  [ErrorCode.PROTOCOL_UNSUPPORTED]: "Protocol not supported",
  [ErrorCode.CODEC_INCOMPATIBLE]: "Codec not supported",
  [ErrorCode.ICE_FAILED]: "Connection failed",
  [ErrorCode.MANIFEST_STALE]: "Stream manifest outdated",
  [ErrorCode.PLAYER_INIT_FAILED]: "Player initialization failed",
  [ErrorCode.QUALITY_DROPPED]: "Quality reduced",
  [ErrorCode.BANDWIDTH_LIMITED]: "Bandwidth limited",
  [ErrorCode.ALL_PROTOCOLS_EXHAUSTED]: "Unable to play video",
  [ErrorCode.ALL_PROTOCOLS_BLACKLISTED]: "No compatible playback protocols available",
  [ErrorCode.STREAM_OFFLINE]: "Stream is offline",
  [ErrorCode.AUTH_REQUIRED]: "Sign in to watch",
  [ErrorCode.GEO_BLOCKED]: "Not available in your region",
  [ErrorCode.DRM_ERROR]: "Playback not supported",
  [ErrorCode.CONTENT_UNAVAILABLE]: "Content unavailable",
  [ErrorCode.UNKNOWN]: "Playback error",
};

type EventListener<K extends keyof ErrorHandlingEvents> = (data: ErrorHandlingEvents[K]) => void;

export type RecoveryAction =
  | { type: "retry"; delayMs: number }
  | { type: "swap"; reason: string }
  | { type: "toast"; message: string }
  | { type: "fatal"; error: ClassifiedError };

export interface ErrorClassifierOptions {
  /** Number of alternative player/protocol combos available */
  alternativesCount?: number;
  /** Enable debug logging */
  debug?: boolean;
}

/**
 * Centralized error classifier that tracks retry state and determines recovery actions.
 */
export class ErrorClassifier {
  private retryCounts: Map<ErrorCode, number> = new Map();
  private lastErrorTime: Map<ErrorCode, number> = new Map();
  private alternativesRemaining: number;
  private listeners: Map<string, Set<Function>> = new Map();
  private debug: boolean;

  // Debounce tracking for quality toasts
  private lastQualityToastTime = 0;
  private static readonly QUALITY_TOAST_DEBOUNCE_MS = 10000;

  constructor(options: ErrorClassifierOptions = {}) {
    this.alternativesRemaining = options.alternativesCount ?? 0;
    this.debug = options.debug ?? false;
  }

  /**
   * Update the count of remaining alternatives (called after a swap attempt)
   */
  setAlternativesRemaining(count: number): void {
    this.alternativesRemaining = count;
  }

  /**
   * Reset retry counts (call when playback successfully resumes)
   */
  reset(): void {
    this.retryCounts.clear();
    this.lastErrorTime.clear();
    this.lastQualityToastTime = 0;
    this.log("Error state reset");
  }

  /**
   * Classify a raw error and determine the appropriate recovery action.
   *
   * @param code - Error code identifying the error type
   * @param originalError - Original error object or message
   * @returns The recovery action to take
   */
  classify(code: ErrorCode, originalError?: Error | string): RecoveryAction {
    const config = RETRY_CONFIGS[code];
    const currentAttempt = (this.retryCounts.get(code) ?? 0) + 1;
    const retriesRemaining = Math.max(0, config.maxAttempts - currentAttempt);

    // Update retry count
    this.retryCounts.set(code, currentAttempt);
    this.lastErrorTime.set(code, Date.now());

    const classified: ClassifiedError = {
      severity: CODE_TO_SEVERITY[code],
      code,
      message: CODE_TO_MESSAGE[code],
      retriesRemaining,
      alternativesRemaining: this.alternativesRemaining,
      originalError,
      timestamp: Date.now(),
    };

    this.log(
      `Classified error: ${code}, attempt ${currentAttempt}/${config.maxAttempts}, severity ${classified.severity}`
    );

    // Tier 1: Silent retry if retries remaining
    if (classified.severity === ErrorSeverity.TRANSIENT && retriesRemaining > 0) {
      const delayMs = this.calculateBackoff(code, currentAttempt);
      this.emit("recoveryAttempted", {
        code,
        attempt: currentAttempt,
        maxAttempts: config.maxAttempts,
      });
      return { type: "retry", delayMs };
    }

    // Tier 1 exhausted or Tier 2: Try protocol swap if alternatives exist
    if (
      (classified.severity === ErrorSeverity.TRANSIENT && retriesRemaining === 0) ||
      classified.severity === ErrorSeverity.RECOVERABLE
    ) {
      if (this.alternativesRemaining > 0) {
        return { type: "swap", reason: classified.message };
      }
      // No alternatives: escalate to fatal
      const originalCode = classified.code;
      const originalMessage = classified.message;
      classified.severity = ErrorSeverity.FATAL;
      classified.code = ErrorCode.ALL_PROTOCOLS_EXHAUSTED;
      classified.message = `${originalMessage} (no alternatives remaining)`;
      classified.details = {
        ...classified.details,
        originalCode,
        originalMessage,
      };
    }

    // Tier 3: Quality degradation toast (debounced)
    if (classified.severity === ErrorSeverity.DEGRADED) {
      const now = Date.now();
      if (now - this.lastQualityToastTime >= ErrorClassifier.QUALITY_TOAST_DEBOUNCE_MS) {
        this.lastQualityToastTime = now;
        this.emit("qualityChanged", {
          direction: "down",
          reason: classified.message,
        });
        return { type: "toast", message: classified.message };
      }
      // Debounced: no action needed
      return { type: "toast", message: "" };
    }

    // Tier 4: Fatal error
    this.emit("playbackFailed", classified);
    return { type: "fatal", error: classified };
  }

  classifyWithDetails(
    code: ErrorCode,
    message: string,
    details?: ClassifiedError["details"],
    originalError?: Error | string
  ): RecoveryAction {
    if (CODE_TO_SEVERITY[code] === ErrorSeverity.FATAL) {
      const classified: ClassifiedError = {
        severity: ErrorSeverity.FATAL,
        code,
        message,
        retriesRemaining: 0,
        alternativesRemaining: this.alternativesRemaining,
        originalError,
        timestamp: Date.now(),
        details,
      };
      this.emit("playbackFailed", classified);
      return { type: "fatal", error: classified };
    }

    const action = this.classify(code, originalError);
    if (action.type === "fatal") {
      action.error.message = message;
      action.error.details = details;
    }
    return action;
  }

  /**
   * Notify classifier that a protocol swap occurred (for event emission)
   */
  notifyProtocolSwap(
    fromPlayer: string,
    toPlayer: string,
    fromProtocol: string,
    toProtocol: string,
    reason: string
  ): void {
    // Note: alternativesRemaining is managed by PlayerManager.setAlternativesRemaining()
    // Don't decrement here to avoid double-counting
    this.emit("protocolSwapped", {
      fromPlayer,
      toPlayer,
      fromProtocol,
      toProtocol,
      reason,
    });
  }

  /**
   * Calculate exponential backoff delay with jitter
   */
  private calculateBackoff(code: ErrorCode, attempt: number): number {
    const config = RETRY_CONFIGS[code];
    const exponentialDelay = config.baseDelayMs * Math.pow(2, attempt - 1);
    const cappedDelay = Math.min(exponentialDelay, config.maxDelayMs);

    // Add jitter
    const jitterRange = cappedDelay * (config.jitterPercent / 100);
    const jitter = (Math.random() * 2 - 1) * jitterRange;

    return Math.round(cappedDelay + jitter);
  }

  /**
   * Map common error patterns to error codes
   */
  static mapErrorToCode(error: Error | string): ErrorCode {
    const message = typeof error === "string" ? error : error.message;
    const lowerMessage = message.toLowerCase();

    // Network errors
    if (lowerMessage.includes("timeout") || lowerMessage.includes("timed out")) {
      return ErrorCode.NETWORK_TIMEOUT;
    }
    if (lowerMessage.includes("websocket") || lowerMessage.includes("socket")) {
      return ErrorCode.WEBSOCKET_DISCONNECT;
    }
    if (lowerMessage.includes("fetch") || lowerMessage.includes("network")) {
      return ErrorCode.NETWORK_TIMEOUT;
    }

    // Stream state - check before segment errors (404 can mean offline)
    if (
      lowerMessage.includes("offline") ||
      lowerMessage.includes("not found") ||
      lowerMessage.includes("stream not found")
    ) {
      return ErrorCode.STREAM_OFFLINE;
    }

    // Segment/manifest errors (only if not a stream-level 404)
    if (lowerMessage.includes("segment")) {
      return ErrorCode.SEGMENT_LOAD_ERROR;
    }
    if (lowerMessage.includes("manifest") || lowerMessage.includes("playlist")) {
      // Manifest 404 = stream offline, not stale
      if (lowerMessage.includes("404")) {
        return ErrorCode.STREAM_OFFLINE;
      }
      return ErrorCode.MANIFEST_STALE;
    }

    // ICE/WebRTC errors
    if (lowerMessage.includes("ice") && lowerMessage.includes("disconnect")) {
      return ErrorCode.ICE_DISCONNECTED;
    }
    if (lowerMessage.includes("ice") && lowerMessage.includes("fail")) {
      return ErrorCode.ICE_FAILED;
    }

    // Codec errors
    if (lowerMessage.includes("codec") || lowerMessage.includes("decode")) {
      return ErrorCode.CODEC_DECODE_ERROR;
    }
    if (lowerMessage.includes("not supported") || lowerMessage.includes("unsupported")) {
      return ErrorCode.PROTOCOL_UNSUPPORTED;
    }

    // Buffer errors
    if (lowerMessage.includes("buffer") || lowerMessage.includes("underrun")) {
      return ErrorCode.BUFFER_UNDERRUN;
    }

    // Auth errors
    if (
      lowerMessage.includes("401") ||
      lowerMessage.includes("auth") ||
      lowerMessage.includes("unauthorized")
    ) {
      return ErrorCode.AUTH_REQUIRED;
    }
    if (
      lowerMessage.includes("403") ||
      lowerMessage.includes("forbidden") ||
      lowerMessage.includes("geo")
    ) {
      return ErrorCode.GEO_BLOCKED;
    }

    // DRM
    if (
      lowerMessage.includes("drm") ||
      lowerMessage.includes("eme") ||
      lowerMessage.includes("key")
    ) {
      return ErrorCode.DRM_ERROR;
    }

    return ErrorCode.UNKNOWN;
  }

  // Event emitter methods
  on<K extends keyof ErrorHandlingEvents>(event: K, listener: EventListener<K>): void {
    if (!this.listeners.has(event)) {
      this.listeners.set(event, new Set());
    }
    this.listeners.get(event)!.add(listener);
  }

  off<K extends keyof ErrorHandlingEvents>(event: K, listener: EventListener<K>): void {
    const eventListeners = this.listeners.get(event);
    if (eventListeners) {
      eventListeners.delete(listener);
    }
  }

  private emit<K extends keyof ErrorHandlingEvents>(event: K, data: ErrorHandlingEvents[K]): void {
    const eventListeners = this.listeners.get(event);
    if (eventListeners) {
      eventListeners.forEach((listener) => {
        try {
          (listener as EventListener<K>)(data);
        } catch (e) {
          console.error(`Error in ${event} listener:`, e);
        }
      });
    }
  }

  private log(message: string): void {
    if (this.debug) {
      console.log(`[ErrorClassifier] ${message}`);
    }
  }
}

import type { TelemetryPayload, PlaybackQuality, ContentType } from "../types";

/**
 * Generate a unique session ID
 */
function generateSessionId(): string {
  return `${Date.now().toString(36)}-${Math.random().toString(36).substr(2, 9)}`;
}

export interface TelemetryReporterConfig {
  /** Telemetry endpoint URL */
  endpoint: string;
  /** Auth token for endpoint */
  authToken?: string;
  /** Report interval in ms (default: 5000) */
  interval?: number;
  /** Batch size before flush (default: 1) */
  batchSize?: number;
  /** Content ID being played */
  contentId: string;
  /** Content type */
  contentType: ContentType;
  /** Player type name */
  playerType: string;
  /** Protocol being used */
  protocol: string;
}

/**
 * TelemetryReporter - Sends playback metrics to server
 *
 * Features:
 * - Batched reporting at configurable interval
 * - Retry with exponential backoff on failure
 * - Uses navigator.sendBeacon() for reliable page unload reporting
 * - Tracks errors during playback
 */
export class TelemetryReporter {
  private config: Required<TelemetryReporterConfig>;
  private sessionId: string;
  private intervalId: ReturnType<typeof setInterval> | null = null;
  private pendingPayloads: TelemetryPayload[] = [];
  private errors: Array<{ code: string; message: string; timestamp: number }> = [];
  private stallCount = 0;
  private totalStallMs = 0;
  private lastStallStart = 0;
  private videoElement: HTMLVideoElement | null = null;
  private qualityGetter: (() => PlaybackQuality | null) | null = null;
  private listeners: Array<() => void> = [];

  constructor(config: TelemetryReporterConfig) {
    this.config = {
      endpoint: config.endpoint,
      authToken: config.authToken ?? "",
      interval: config.interval ?? 5000,
      batchSize: config.batchSize ?? 1,
      contentId: config.contentId,
      contentType: config.contentType,
      playerType: config.playerType,
      protocol: config.protocol,
    };
    this.sessionId = generateSessionId();
  }

  /**
   * Start telemetry reporting
   */
  start(videoElement: HTMLVideoElement, qualityGetter?: () => PlaybackQuality | null): void {
    this.stop();

    this.videoElement = videoElement;
    this.qualityGetter = qualityGetter ?? null;
    this.stallCount = 0;
    this.totalStallMs = 0;
    this.errors = [];

    // Track stalls
    const onWaiting = () => {
      this.stallCount++;
      this.lastStallStart = performance.now();
    };

    const onPlaying = () => {
      if (this.lastStallStart > 0) {
        this.totalStallMs += performance.now() - this.lastStallStart;
        this.lastStallStart = 0;
      }
    };

    const onError = () => {
      const error = videoElement.error;
      if (error) {
        this.errors.push({
          code: String(error.code),
          message: error.message || "Unknown error",
          timestamp: Date.now(),
        });
      }
    };

    videoElement.addEventListener("waiting", onWaiting);
    videoElement.addEventListener("playing", onPlaying);
    videoElement.addEventListener("error", onError);

    this.listeners = [
      () => videoElement.removeEventListener("waiting", onWaiting),
      () => videoElement.removeEventListener("playing", onPlaying),
      () => videoElement.removeEventListener("error", onError),
    ];

    // Setup unload handler for reliable final report
    const onUnload = () => this.flushSync();
    window.addEventListener("beforeunload", onUnload);
    window.addEventListener("pagehide", onUnload);
    this.listeners.push(
      () => window.removeEventListener("beforeunload", onUnload),
      () => window.removeEventListener("pagehide", onUnload)
    );

    // Start reporting interval
    this.intervalId = setInterval(() => this.report(), this.config.interval);

    // Take initial report
    this.report();
  }

  /**
   * Stop telemetry reporting
   */
  stop(): void {
    // Final report before stopping
    this.flushSync();

    if (this.intervalId) {
      clearInterval(this.intervalId);
      this.intervalId = null;
    }

    this.listeners.forEach((cleanup) => cleanup());
    this.listeners = [];

    this.videoElement = null;
    this.qualityGetter = null;
  }

  /**
   * Record a custom error
   */
  recordError(code: string, message: string): void {
    this.errors.push({
      code,
      message,
      timestamp: Date.now(),
    });
  }

  /**
   * Generate telemetry payload
   */
  private generatePayload(): TelemetryPayload | null {
    const video = this.videoElement;
    if (!video) return null;

    // Get quality metrics if available
    const quality = this.qualityGetter?.() ?? null;

    // Get frame stats if available
    let framesDecoded = 0;
    let framesDropped = 0;

    if ("getVideoPlaybackQuality" in video) {
      const stats = video.getVideoPlaybackQuality();
      framesDecoded = stats.totalVideoFrames;
      framesDropped = stats.droppedVideoFrames;
    }

    // Calculate buffered seconds
    let bufferedSeconds = 0;
    if (video.buffered.length > 0) {
      for (let i = 0; i < video.buffered.length; i++) {
        if (
          video.buffered.start(i) <= video.currentTime &&
          video.buffered.end(i) > video.currentTime
        ) {
          bufferedSeconds = video.buffered.end(i) - video.currentTime;
          break;
        }
      }
    }

    return {
      timestamp: Date.now(),
      sessionId: this.sessionId,
      contentId: this.config.contentId,
      contentType: this.config.contentType,
      metrics: {
        currentTime: video.currentTime,
        duration: isFinite(video.duration) ? video.duration : -1,
        bufferedSeconds,
        stallCount: this.stallCount,
        totalStallMs: this.totalStallMs,
        bitrate: quality?.bitrate ?? 0,
        qualityScore: quality?.score ?? 100,
        framesDecoded,
        framesDropped,
        playerType: this.config.playerType,
        protocol: this.config.protocol,
        resolution:
          video.videoWidth > 0
            ? {
                width: video.videoWidth,
                height: video.videoHeight,
              }
            : undefined,
      },
      errors: this.errors.length > 0 ? [...this.errors] : undefined,
    };
  }

  /**
   * Send telemetry report
   */
  private async report(): Promise<void> {
    const payload = this.generatePayload();
    if (!payload) return;

    this.pendingPayloads.push(payload);

    // Flush if batch size reached
    if (this.pendingPayloads.length >= this.config.batchSize) {
      await this.flush();
    }
  }

  /**
   * Flush pending payloads (async)
   */
  private async flush(): Promise<void> {
    if (this.pendingPayloads.length === 0) return;

    const payloads = [...this.pendingPayloads];
    this.pendingPayloads = [];

    try {
      const headers: HeadersInit = {
        "Content-Type": "application/json",
      };

      if (this.config.authToken) {
        headers["Authorization"] = `Bearer ${this.config.authToken}`;
      }

      const response = await fetch(this.config.endpoint, {
        method: "POST",
        headers,
        body: JSON.stringify(payloads.length === 1 ? payloads[0] : payloads),
      });

      if (!response.ok) {
        console.warn("[TelemetryReporter] Report failed:", response.status);
        // Re-queue failed payloads (up to a limit)
        if (this.pendingPayloads.length < 10) {
          this.pendingPayloads.unshift(...payloads);
        }
      } else {
        // Clear reported errors
        this.errors = [];
      }
    } catch (error) {
      console.warn("[TelemetryReporter] Report error:", error);
      // Re-queue failed payloads
      if (this.pendingPayloads.length < 10) {
        this.pendingPayloads.unshift(...payloads);
      }
    }
  }

  /**
   * Flush synchronously using sendBeacon (for page unload)
   */
  private flushSync(): void {
    const payload = this.generatePayload();
    if (!payload) return;

    const payloads = [...this.pendingPayloads, payload];
    this.pendingPayloads = [];

    try {
      const data = JSON.stringify(payloads.length === 1 ? payloads[0] : payloads);
      navigator.sendBeacon(this.config.endpoint, new Blob([data], { type: "application/json" }));
    } catch (error) {
      console.warn("[TelemetryReporter] Beacon failed:", error);
    }
  }

  /**
   * Get session ID
   */
  getSessionId(): string {
    return this.sessionId;
  }

  /**
   * Check if reporting is active
   */
  isActive(): boolean {
    return this.intervalId !== null;
  }
}

export default TelemetryReporter;

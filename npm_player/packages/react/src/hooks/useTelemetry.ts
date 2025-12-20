import { useEffect, useRef, useCallback } from 'react';
import { TelemetryReporter, type TelemetryOptions, type PlaybackQuality, type ContentType } from '@livepeer-frameworks/player-core';

export interface UseTelemetryOptions extends TelemetryOptions {
  /** Video element to monitor */
  videoElement: HTMLVideoElement | null;
  /** Content ID being played */
  contentId: string;
  /** Content type */
  contentType: ContentType;
  /** Player type name */
  playerType: string;
  /** Protocol being used */
  protocol: string;
  /** Optional quality getter function */
  getQuality?: () => PlaybackQuality | null;
}

/**
 * Hook to send telemetry data to a server
 *
 * Reports playback metrics at configurable intervals:
 * - Current time and duration
 * - Buffer health
 * - Stall count and duration
 * - Quality score and bitrate
 * - Frame decode/drop stats
 * - Errors encountered
 *
 * Uses navigator.sendBeacon() for reliable reporting on page unload.
 *
 * @example
 * ```tsx
 * const { sessionId, recordError } = useTelemetry({
 *   enabled: true,
 *   endpoint: '/api/telemetry',
 *   interval: 5000,
 *   videoElement,
 *   contentId: 'my-stream',
 *   contentType: 'live',
 *   playerType: 'hlsjs',
 *   protocol: 'HLS',
 *   getQuality: () => qualityMonitor.getCurrentQuality(),
 * });
 *
 * // Record custom error
 * recordError('NETWORK_ERROR', 'Connection lost');
 * ```
 */
export function useTelemetry(options: UseTelemetryOptions) {
  const {
    enabled,
    endpoint,
    interval,
    authToken,
    batchSize,
    videoElement,
    contentId,
    contentType,
    playerType,
    protocol,
    getQuality,
  } = options;

  const reporterRef = useRef<TelemetryReporter | null>(null);

  // Create reporter instance
  useEffect(() => {
    if (!enabled || !endpoint || !contentId) {
      reporterRef.current?.stop();
      reporterRef.current = null;
      return;
    }

    reporterRef.current = new TelemetryReporter({
      endpoint,
      authToken,
      interval,
      batchSize,
      contentId,
      contentType,
      playerType,
      protocol,
    });

    return () => {
      reporterRef.current?.stop();
      reporterRef.current = null;
    };
  }, [enabled, endpoint, authToken, interval, batchSize, contentId, contentType, playerType, protocol]);

  // Start/stop reporting when video element changes
  useEffect(() => {
    if (!enabled || !videoElement || !reporterRef.current) {
      return;
    }

    reporterRef.current.start(videoElement, getQuality);

    return () => {
      reporterRef.current?.stop();
    };
  }, [enabled, videoElement, getQuality]);

  /**
   * Record a custom error
   */
  const recordError = useCallback((code: string, message: string) => {
    reporterRef.current?.recordError(code, message);
  }, []);

  /**
   * Get current session ID
   */
  const getSessionId = useCallback((): string | null => {
    return reporterRef.current?.getSessionId() ?? null;
  }, []);

  /**
   * Check if telemetry is active
   */
  const isActive = useCallback((): boolean => {
    return reporterRef.current?.isActive() ?? false;
  }, []);

  return {
    /** Session ID for this playback session */
    sessionId: reporterRef.current?.getSessionId() ?? null,
    /** Record a custom error */
    recordError,
    /** Get current session ID */
    getSessionId,
    /** Check if telemetry is active */
    isActive,
  };
}

export default useTelemetry;

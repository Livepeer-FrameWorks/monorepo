import { useEffect, useState, useRef, useCallback } from 'react';
import type { PlaybackQuality, UsePlaybackQualityOptions } from '../types';
import { QualityMonitor } from '../core/QualityMonitor';

/**
 * Hook to monitor video playback quality
 *
 * Tracks:
 * - Buffer health (seconds ahead)
 * - Stall count
 * - Frame drop rate
 * - Estimated bitrate
 * - Latency (live streams)
 * - Composite quality score (0-100)
 *
 * @example
 * ```tsx
 * const { quality, isMonitoring } = usePlaybackQuality({
 *   videoElement,
 *   enabled: true,
 *   thresholds: { minScore: 60 },
 *   onQualityDegraded: (q) => console.log('Quality dropped:', q.score),
 * });
 *
 * return <div>Quality: {quality?.score ?? '--'}</div>;
 * ```
 */
export function usePlaybackQuality(options: UsePlaybackQualityOptions) {
  const {
    videoElement,
    enabled = true,
    sampleInterval = 500,
    thresholds,
    onQualityDegraded,
  } = options;

  const [quality, setQuality] = useState<PlaybackQuality | null>(null);
  const [isMonitoring, setIsMonitoring] = useState(false);
  const monitorRef = useRef<QualityMonitor | null>(null);

  // Create/update monitor instance
  useEffect(() => {
    monitorRef.current = new QualityMonitor({
      sampleInterval,
      thresholds,
      onQualityDegraded,
      onSample: setQuality,
    });

    return () => {
      monitorRef.current?.stop();
      monitorRef.current = null;
    };
  }, [sampleInterval, thresholds, onQualityDegraded]);

  // Start/stop monitoring based on videoElement and enabled state
  useEffect(() => {
    if (!enabled || !videoElement || !monitorRef.current) {
      monitorRef.current?.stop();
      setIsMonitoring(false);
      return;
    }

    monitorRef.current.start(videoElement);
    setIsMonitoring(true);

    return () => {
      monitorRef.current?.stop();
      setIsMonitoring(false);
    };
  }, [videoElement, enabled]);

  /**
   * Get current quality snapshot
   */
  const getCurrentQuality = useCallback((): PlaybackQuality | null => {
    return monitorRef.current?.getCurrentQuality() ?? null;
  }, []);

  /**
   * Get rolling average quality
   */
  const getAverageQuality = useCallback((): PlaybackQuality | null => {
    return monitorRef.current?.getAverageQuality() ?? null;
  }, []);

  /**
   * Get quality history
   */
  const getHistory = useCallback((): PlaybackQuality[] => {
    return monitorRef.current?.getHistory() ?? [];
  }, []);

  /**
   * Reset stall counters
   */
  const resetStallCounters = useCallback(() => {
    monitorRef.current?.resetStallCounters();
  }, []);

  /**
   * Get total stall time
   */
  const getTotalStallMs = useCallback((): number => {
    return monitorRef.current?.getTotalStallMs() ?? 0;
  }, []);

  return {
    /** Current quality metrics */
    quality,
    /** Whether monitoring is active */
    isMonitoring,
    /** Get current quality snapshot */
    getCurrentQuality,
    /** Get rolling average quality */
    getAverageQuality,
    /** Get quality history */
    getHistory,
    /** Reset stall counters */
    resetStallCounters,
    /** Get total stall time in ms */
    getTotalStallMs,
  };
}

export default usePlaybackQuality;

/**
 * useStreamStats Hook
 * React hook for streaming statistics
 */

import { useState, useEffect, useRef, useCallback } from 'react';
import type { IngestStats } from '@livepeer-frameworks/streamcrafter-core';

export interface UseStreamStatsOptions {
  /** Polling interval in milliseconds (default: 1000) */
  interval?: number;
  /** Whether to auto-start polling (default: false) */
  autoStart?: boolean;
}

export interface UseStreamStatsReturn {
  stats: IngestStats | null;
  isPolling: boolean;
  startPolling: () => void;
  stopPolling: () => void;
  /** Manual fetch of stats */
  fetchStats: () => Promise<IngestStats | null>;
}

export function useStreamStats(
  getStats: () => Promise<IngestStats | null>,
  options: UseStreamStatsOptions = {}
): UseStreamStatsReturn {
  const { interval = 1000, autoStart = false } = options;

  const [stats, setStats] = useState<IngestStats | null>(null);
  const [isPolling, setIsPolling] = useState(autoStart);
  const intervalRef = useRef<ReturnType<typeof setInterval> | null>(null);
  const getStatsRef = useRef(getStats);

  // Keep getStats ref up to date
  useEffect(() => {
    getStatsRef.current = getStats;
  }, [getStats]);

  const fetchStats = useCallback(async () => {
    try {
      const newStats = await getStatsRef.current();
      setStats(newStats);
      return newStats;
    } catch {
      return null;
    }
  }, []);

  const startPolling = useCallback(() => {
    if (intervalRef.current) return;
    setIsPolling(true);
    fetchStats();
    intervalRef.current = setInterval(fetchStats, interval);
  }, [interval, fetchStats]);

  const stopPolling = useCallback(() => {
    if (intervalRef.current) {
      clearInterval(intervalRef.current);
      intervalRef.current = null;
    }
    setIsPolling(false);
  }, []);

  // Auto-start if enabled
  useEffect(() => {
    if (autoStart) {
      startPolling();
    }
    return () => {
      stopPolling();
    };
  }, [autoStart, startPolling, stopPolling]);

  return {
    stats,
    isPolling,
    startPolling,
    stopPolling,
    fetchStats,
  };
}

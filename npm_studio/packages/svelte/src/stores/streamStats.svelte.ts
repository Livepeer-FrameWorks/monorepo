/**
 * Stream Stats Store
 * Svelte 5 store for streaming statistics
 */

import type { IngestStats } from "@livepeer-frameworks/streamcrafter-core";

export interface StreamStatsState {
  stats: IngestStats | null;
  isPolling: boolean;
}

export interface StreamStatsStore {
  subscribe: (fn: (state: StreamStatsState) => void) => () => void;
  startPolling: () => void;
  stopPolling: () => void;
  fetchStats: () => Promise<IngestStats | null>;
  destroy: () => void;
}

export function createStreamStatsStore(
  getStats: () => Promise<IngestStats | null>,
  options: { interval?: number; autoStart?: boolean } = {}
): StreamStatsStore {
  const { interval = 1000, autoStart = false } = options;

  let state = $state<StreamStatsState>({
    stats: null,
    isPolling: false,
  });

  const subscribers = new Set<(state: StreamStatsState) => void>();
  let pollInterval: ReturnType<typeof setInterval> | null = null;

  function notify() {
    const snapshot = { ...state };
    subscribers.forEach((fn) => fn(snapshot));
  }

  async function fetchStats() {
    try {
      const newStats = await getStats();
      state.stats = newStats;
      notify();
      return newStats;
    } catch {
      return null;
    }
  }

  function startPolling() {
    if (pollInterval) return;
    state.isPolling = true;
    notify();
    fetchStats();
    pollInterval = setInterval(fetchStats, interval);
  }

  function stopPolling() {
    if (pollInterval) {
      clearInterval(pollInterval);
      pollInterval = null;
    }
    state.isPolling = false;
    notify();
  }

  return {
    subscribe(fn) {
      subscribers.add(fn);
      fn({ ...state });

      if (autoStart && !pollInterval) {
        startPolling();
      }

      return () => {
        subscribers.delete(fn);
      };
    },

    startPolling,
    stopPolling,
    fetchStats,

    destroy() {
      stopPolling();
      subscribers.clear();
    },
  };
}

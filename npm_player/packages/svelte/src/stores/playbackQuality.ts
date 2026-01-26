/**
 * Svelte store for playback quality monitoring.
 *
 * Wraps QualityMonitor for reactive quality metrics.
 */

import { writable, derived, type Readable } from 'svelte/store';
import { QualityMonitor, type PlaybackQuality } from '@livepeer-frameworks/player-core';

export interface PlaybackQualityOptions {
  sampleInterval?: number;
  enabled?: boolean;
}

export interface PlaybackQualityStore extends Readable<PlaybackQuality | null> {
  start: (videoElement: HTMLVideoElement) => void;
  stop: () => void;
  getPlaybackScore: () => number;
  destroy: () => void;
}

/**
 * Create a playback quality monitoring store.
 *
 * @example
 * ```svelte
 * <script>
 *   import { createPlaybackQualityMonitor } from './stores/playbackQuality';
 *
 *   const quality = createPlaybackQualityMonitor();
 *
 *   // Start monitoring when video element is available
 *   $: if (videoElement) quality.start(videoElement);
 *
 *   // Access quality metrics
 *   $: score = $quality?.score ?? 100;
 *   $: stallCount = $quality?.stallCount ?? 0;
 * </script>
 * ```
 */
export function createPlaybackQualityMonitor(options: PlaybackQualityOptions = {}): PlaybackQualityStore {
  const { sampleInterval = 500, enabled = true } = options;

  const store = writable<PlaybackQuality | null>(null);
  let monitor: QualityMonitor | null = null;
  let currentVideoElement: HTMLVideoElement | null = null;

  /**
   * Start monitoring a video element
   */
  function start(videoElement: HTMLVideoElement) {
    if (!enabled) return;

    // Stop existing monitor if different element
    if (currentVideoElement && currentVideoElement !== videoElement) {
      stop();
    }

    currentVideoElement = videoElement;

    if (!monitor) {
      monitor = new QualityMonitor({
        sampleInterval,
        onSample: (quality) => {
          store.set(quality);
        },
      });
    }

    monitor.start(videoElement);
  }

  /**
   * Stop monitoring
   */
  function stop() {
    monitor?.stop();
    currentVideoElement = null;
  }

  /**
   * Get current playback score
   */
  function getPlaybackScore(): number {
    return monitor?.getPlaybackScore() ?? 1.0;
  }

  /**
   * Cleanup
   */
  function destroy() {
    stop();
    monitor = null;
    store.set(null);
  }

  return {
    subscribe: store.subscribe,
    start,
    stop,
    getPlaybackScore,
    destroy,
  };
}

// Convenience derived stores
export function createDerivedQualityScore(store: PlaybackQualityStore) {
  return derived(store, $quality => $quality?.score ?? 100);
}

export function createDerivedStallCount(store: PlaybackQualityStore) {
  return derived(store, $quality => $quality?.stallCount ?? 0);
}

export function createDerivedFrameDropRate(store: PlaybackQualityStore) {
  return derived(store, $quality => $quality?.frameDropRate ?? 0);
}

export function createDerivedBitrate(store: PlaybackQualityStore) {
  return derived(store, $quality => $quality?.bitrate ?? 0);
}

export function createDerivedLatency(store: PlaybackQualityStore) {
  return derived(store, $quality => $quality?.latency);
}

export default createPlaybackQualityMonitor;

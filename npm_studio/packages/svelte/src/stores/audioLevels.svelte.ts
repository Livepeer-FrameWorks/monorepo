/**
 * Audio Levels Store
 * Svelte 5 store for VU meter / audio level monitoring
 */

import type { IngestControllerV2 } from '@livepeer-frameworks/streamcrafter-core';

export interface AudioLevelsState {
  level: number; // Current level 0-1
  peakLevel: number; // Peak level with decay 0-1
  isMonitoring: boolean;
}

export interface AudioLevelsStore {
  subscribe: (fn: (state: AudioLevelsState) => void) => () => void;
  startMonitoring: () => void;
  stopMonitoring: () => void;
  destroy: () => void;
}

/**
 * Create an audio levels store for VU meter
 *
 * @example
 * ```svelte
 * <script>
 *   const crafter = createStreamCrafterContextV2();
 *   const levels = createAudioLevelsStore(crafter.getController);
 * </script>
 *
 * <div class="vu-meter">
 *   <div style:width="{$levels.level * 100}%" />
 * </div>
 * ```
 */
export function createAudioLevelsStore(
  getController: () => IngestControllerV2 | null,
  options: { autoStart?: boolean } = {}
): AudioLevelsStore {
  const { autoStart = true } = options;

  let state = $state<AudioLevelsState>({
    level: 0,
    peakLevel: 0,
    isMonitoring: false,
  });

  const subscribers = new Set<(state: AudioLevelsState) => void>();
  let unsubscribeFromMixer: (() => void) | null = null;

  function notify() {
    const snapshot = { ...state };
    subscribers.forEach((fn) => fn(snapshot));
  }

  function startMonitoring() {
    const controller = getController();
    if (!controller) return;

    const audioMixer = controller.getAudioMixer();
    if (!audioMixer) return;

    // Subscribe to level updates
    unsubscribeFromMixer = audioMixer.on('levelUpdate', (event) => {
      state.level = event.level;
      state.peakLevel = event.peakLevel;
      notify();
    });

    // Start the audio mixer's level monitoring
    audioMixer.startLevelMonitoring();
    state.isMonitoring = true;
    notify();
  }

  function stopMonitoring() {
    const controller = getController();
    if (controller) {
      const audioMixer = controller.getAudioMixer();
      if (audioMixer) {
        audioMixer.stopLevelMonitoring();
      }
    }

    if (unsubscribeFromMixer) {
      unsubscribeFromMixer();
      unsubscribeFromMixer = null;
    }

    state.level = 0;
    state.peakLevel = 0;
    state.isMonitoring = false;
    notify();
  }

  return {
    subscribe(fn) {
      subscribers.add(fn);
      fn({ ...state });

      // Auto-start if configured and not already monitoring
      if (autoStart && !state.isMonitoring) {
        // Delay to allow controller to be ready
        setTimeout(() => {
          if (!state.isMonitoring && getController()) {
            startMonitoring();
          }
        }, 100);
      }

      return () => {
        subscribers.delete(fn);
      };
    },

    startMonitoring,
    stopMonitoring,

    destroy() {
      stopMonitoring();
      subscribers.clear();
    },
  };
}

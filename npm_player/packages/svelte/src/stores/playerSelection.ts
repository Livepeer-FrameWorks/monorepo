/**
 * Svelte store for player selection.
 *
 * Subscribes to PlayerManager events for reactive selection updates.
 * Uses event-driven updates instead of polling - no render spam.
 */

import { writable, derived, type Readable } from "svelte/store";
import {
  type PlayerManager,
  type PlayerSelection,
  type PlayerCombination,
  type StreamInfo,
  type PlaybackMode,
} from "@livepeer-frameworks/player-core";

export interface PlayerSelectionOptions {
  /** Enable debug logging */
  debug?: boolean;
}

export interface PlayerSelectionState {
  /** Current best selection (null if no compatible player) */
  selection: PlayerSelection | null;
  /** All player+source combinations with scores */
  combinations: PlayerCombination[];
  /** Whether initial computation has completed */
  ready: boolean;
}

export interface PlayerSelectionStore extends Readable<PlayerSelectionState> {
  /** Update stream info to compute selections for */
  setStreamInfo: (streamInfo: StreamInfo | null, playbackMode?: PlaybackMode) => void;
  /** Force recomputation (invalidates cache) */
  refresh: () => void;
  /** Cleanup subscriptions */
  destroy: () => void;
}

const initialState: PlayerSelectionState = {
  selection: null,
  combinations: [],
  ready: false,
};

/**
 * Create a player selection store that subscribes to PlayerManager events.
 *
 * This store uses the event system in PlayerManager, which means:
 * - Initial computation happens once when streamInfo is provided
 * - Updates only fire when selection actually changes (different player+source)
 * - No render spam from frequent reactive updates
 *
 * @example
 * ```svelte
 * <script>
 *   import { createPlayerSelectionStore } from './stores/playerSelection';
 *   import { globalPlayerManager } from '@livepeer-frameworks/player-core';
 *
 *   const playerSelection = createPlayerSelectionStore(globalPlayerManager);
 *
 *   // Set stream info to trigger selection
 *   $: if (streamInfo) playerSelection.setStreamInfo(streamInfo, playbackMode);
 *
 *   // Access selection state
 *   $: selection = $playerSelection.selection;
 *   $: combinations = $playerSelection.combinations;
 *   $: ready = $playerSelection.ready;
 * </script>
 * ```
 */
export function createPlayerSelectionStore(
  manager: PlayerManager,
  options: PlayerSelectionOptions = {}
): PlayerSelectionStore {
  const { debug = false } = options;

  const store = writable<PlayerSelectionState>({ ...initialState });

  let currentStreamInfo: StreamInfo | null = null;
  let currentPlaybackMode: PlaybackMode = "auto";
  let unsubSelection: (() => void) | null = null;
  let unsubCombos: (() => void) | null = null;

  // Subscribe to PlayerManager events
  function subscribe() {
    // Clean up existing subscriptions
    unsubscribe();

    unsubSelection = manager.on("selection-changed", (sel) => {
      if (debug) {
        console.log("[playerSelection store] Selection changed:", sel?.player, sel?.source?.type);
      }
      store.update((state) => ({
        ...state,
        selection: sel,
      }));
    });

    unsubCombos = manager.on("combinations-updated", (combos) => {
      if (debug) {
        console.log("[playerSelection store] Combinations updated:", combos.length);
      }
      store.update((state) => ({
        ...state,
        combinations: combos,
        ready: true,
      }));
    });
  }

  function unsubscribe() {
    unsubSelection?.();
    unsubCombos?.();
    unsubSelection = null;
    unsubCombos = null;
  }

  // Initialize subscriptions
  subscribe();

  /**
   * Set stream info to compute selections for.
   * This triggers computation (using cache if available).
   */
  function setStreamInfo(streamInfo: StreamInfo | null, playbackMode?: PlaybackMode) {
    currentStreamInfo = streamInfo;
    currentPlaybackMode = playbackMode ?? "auto";

    if (!streamInfo) {
      store.set({ ...initialState });
      return;
    }

    // This will use cache if available, or compute + emit events if not
    manager.getAllCombinations(streamInfo, currentPlaybackMode);
  }

  /**
   * Force recomputation (invalidates cache).
   */
  function refresh() {
    if (!currentStreamInfo) return;
    manager.invalidateCache();
    manager.getAllCombinations(currentStreamInfo, currentPlaybackMode);
  }

  /**
   * Cleanup subscriptions.
   */
  function destroy() {
    unsubscribe();
    store.set({ ...initialState });
    currentStreamInfo = null;
  }

  return {
    subscribe: store.subscribe,
    setStreamInfo,
    refresh,
    destroy,
  };
}

// Convenience derived stores

/**
 * Derive just the current selection from the store.
 */
export function createDerivedSelection(
  store: PlayerSelectionStore
): Readable<PlayerSelection | null> {
  return derived(store, ($state) => $state.selection);
}

/**
 * Derive just the combinations array from the store.
 */
export function createDerivedCombinations(
  store: PlayerSelectionStore
): Readable<PlayerCombination[]> {
  return derived(store, ($state) => $state.combinations);
}

/**
 * Derive the ready state from the store.
 */
export function createDerivedReady(store: PlayerSelectionStore): Readable<boolean> {
  return derived(store, ($state) => $state.ready);
}

/**
 * Derive the selected player name.
 */
export function createDerivedSelectedPlayer(store: PlayerSelectionStore): Readable<string | null> {
  return derived(store, ($state) => $state.selection?.player ?? null);
}

/**
 * Derive the selected source type.
 */
export function createDerivedSelectedSourceType(
  store: PlayerSelectionStore
): Readable<string | null> {
  return derived(store, ($state) => $state.selection?.source?.type ?? null);
}

/**
 * Derive only compatible combinations (filtered from all).
 */
export function createDerivedCompatibleCombinations(
  store: PlayerSelectionStore
): Readable<PlayerCombination[]> {
  return derived(store, ($state) => $state.combinations.filter((c) => c.compatible));
}

/**
 * Derive only incompatible combinations.
 */
export function createDerivedIncompatibleCombinations(
  store: PlayerSelectionStore
): Readable<PlayerCombination[]> {
  return derived(store, ($state) => $state.combinations.filter((c) => !c.compatible));
}

export default createPlayerSelectionStore;

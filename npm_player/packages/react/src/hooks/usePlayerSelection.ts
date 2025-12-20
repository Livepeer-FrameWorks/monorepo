/**
 * usePlayerSelection
 *
 * React hook for subscribing to PlayerManager selection events.
 * Uses event-driven updates instead of polling - no render spam.
 */

import { useState, useEffect, useCallback } from 'react';
import type {
  PlayerManager,
  PlayerSelection,
  PlayerCombination,
  StreamInfo,
  PlaybackMode,
} from '@livepeer-frameworks/player-core';

export interface UsePlayerSelectionOptions {
  /** Stream info to compute selections for */
  streamInfo: StreamInfo | null;
  /** Playback mode override */
  playbackMode?: PlaybackMode;
  /** Enable debug logging */
  debug?: boolean;
}

export interface UsePlayerSelectionReturn {
  /** Current best selection (null if no compatible player) */
  selection: PlayerSelection | null;
  /** All player+source combinations with scores */
  combinations: PlayerCombination[];
  /** Whether initial computation has completed */
  ready: boolean;
  /** Force recomputation (invalidates cache) */
  refresh: () => void;
}

/**
 * Subscribe to player selection changes from a PlayerManager.
 *
 * This hook uses the event system in PlayerManager, which means:
 * - Initial computation happens once when streamInfo is provided
 * - Updates only fire when selection actually changes (different player+source)
 * - No render spam from React strict mode or frequent re-renders
 *
 * @example
 * ```tsx
 * const { selection, combinations, ready } = usePlayerSelection(globalPlayerManager, {
 *   streamInfo,
 *   playbackMode: 'auto',
 * });
 *
 * if (!ready) return <Loading />;
 * if (!selection) return <NoPlayerAvailable />;
 *
 * return <div>Selected: {selection.player} + {selection.source.type}</div>;
 * ```
 */
export function usePlayerSelection(
  manager: PlayerManager,
  options: UsePlayerSelectionOptions
): UsePlayerSelectionReturn {
  const { streamInfo, playbackMode, debug } = options;

  const [selection, setSelection] = useState<PlayerSelection | null>(null);
  const [combinations, setCombinations] = useState<PlayerCombination[]>([]);
  const [ready, setReady] = useState(false);

  // Subscribe to events
  useEffect(() => {
    const unsubSelection = manager.on('selection-changed', (sel) => {
      if (debug) {
        console.log('[usePlayerSelection] Selection changed:', sel?.player, sel?.source?.type);
      }
      setSelection(sel);
    });

    const unsubCombos = manager.on('combinations-updated', (combos) => {
      if (debug) {
        console.log('[usePlayerSelection] Combinations updated:', combos.length);
      }
      setCombinations(combos);
      setReady(true);
    });

    return () => {
      unsubSelection();
      unsubCombos();
    };
  }, [manager, debug]);

  // Trigger initial computation when streamInfo changes
  useEffect(() => {
    if (!streamInfo) {
      setSelection(null);
      setCombinations([]);
      setReady(false);
      return;
    }

    // This will use cache if available, or compute + emit events if not
    manager.getAllCombinations(streamInfo, playbackMode);
  }, [manager, streamInfo, playbackMode]);

  // Manual refresh function
  const refresh = useCallback(() => {
    if (!streamInfo) return;
    manager.invalidateCache();
    manager.getAllCombinations(streamInfo, playbackMode);
  }, [manager, streamInfo, playbackMode]);

  return {
    selection,
    combinations,
    ready,
    refresh,
  };
}

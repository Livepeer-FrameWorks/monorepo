/**
 * PlayerContext
 *
 * Provides player instance to child components, decoupling controls from
 * the global singleton. This enables:
 * - Multiple players per page
 * - Easier testing (mock player via context)
 * - Explicit dependencies
 */

import React, { createContext, useContext, useState, useCallback, ReactNode } from 'react';
import type { IPlayer } from '../core/PlayerInterface';
import { globalPlayerManager } from '../core/PlayerRegistry';

interface PlayerContextValue {
  /** Current active player instance */
  player: IPlayer | null;

  /** Set the current player (called by Player component on init) */
  setPlayer: (player: IPlayer | null) => void;

  /** Video element from current player */
  videoElement: HTMLVideoElement | null;

  /** Convenience: check if player is ready */
  isReady: boolean;
}

const PlayerContext = createContext<PlayerContextValue | null>(null);

interface PlayerProviderProps {
  children: ReactNode;
  /** Optional initial player (for testing) */
  initialPlayer?: IPlayer | null;
}

/**
 * Provider component that wraps Player and its controls.
 * Manages the current player instance.
 */
export function PlayerProvider({ children, initialPlayer = null }: PlayerProviderProps) {
  const [player, setPlayerState] = useState<IPlayer | null>(initialPlayer);

  const setPlayer = useCallback((p: IPlayer | null) => {
    setPlayerState(p);
  }, []);

  const videoElement = player?.getVideoElement() ?? null;
  const isReady = player !== null && videoElement !== null;

  const value: PlayerContextValue = {
    player,
    setPlayer,
    videoElement,
    isReady
  };

  return (
    <PlayerContext.Provider value={value}>
      {children}
    </PlayerContext.Provider>
  );
}

/**
 * Hook to access player context.
 * Must be used within a PlayerProvider.
 */
export function usePlayer(): PlayerContextValue {
  const context = useContext(PlayerContext);
  if (!context) {
    throw new Error('usePlayer must be used within a PlayerProvider');
  }
  return context;
}

/**
 * Hook to access player context with fallback to global.
 * For backwards compatibility - prefer usePlayer() in new code.
 */
export function usePlayerWithFallback(): PlayerContextValue {
  const context = useContext(PlayerContext);

  // If no context, fall back to globalPlayerManager for backwards compatibility
  if (!context) {
    const player = globalPlayerManager.getCurrentPlayer();
    return {
      player,
      setPlayer: () => {
        console.warn('setPlayer called outside PlayerProvider - no effect');
      },
      videoElement: player?.getVideoElement() ?? null,
      isReady: player !== null
    };
  }

  return context;
}

export { PlayerContext };
export type { PlayerContextValue };

/**
 * PlayerContext
 *
 * React context for sharing PlayerController state across components.
 * Follows the "context wraps hook" pattern (same as npm_studio).
 *
 * Usage:
 * ```tsx
 * <PlayerProvider config={{ contentId: 'stream-1', contentType: 'live' }}>
 *   <PlayerControls />
 * </PlayerProvider>
 * ```
 */

import React, { createContext, useContext, type ReactNode } from "react";
import {
  usePlayerController,
  type UsePlayerControllerConfig,
  type UsePlayerControllerReturn,
} from "../hooks/usePlayerController";

// Context holds the full hook return value
const PlayerContext = createContext<UsePlayerControllerReturn | null>(null);

export interface PlayerProviderProps {
  children: ReactNode;
  /** Configuration for the player controller */
  config: UsePlayerControllerConfig;
}

/**
 * Provider component that wraps Player and its controls.
 * Calls usePlayerController internally and shares state via context.
 */
export function PlayerProvider({ children, config }: PlayerProviderProps) {
  const playerController = usePlayerController(config);

  return <PlayerContext.Provider value={playerController}>{children}</PlayerContext.Provider>;
}

/**
 * Hook to access player context.
 * Must be used within a PlayerProvider.
 */
export function usePlayerContext(): UsePlayerControllerReturn {
  const context = useContext(PlayerContext);
  if (!context) {
    throw new Error("usePlayerContext must be used within a PlayerProvider");
  }
  return context;
}

/**
 * Hook to optionally access player context.
 * Returns null if not within a PlayerProvider (no error thrown).
 * Use this when component may or may not be within a PlayerProvider.
 */
export function usePlayerContextOptional(): UsePlayerControllerReturn | null {
  return useContext(PlayerContext);
}

// Export context for advanced use cases
export { PlayerContext };

// Type exports
export type { UsePlayerControllerReturn as PlayerContextValue };
export type { UsePlayerControllerConfig };

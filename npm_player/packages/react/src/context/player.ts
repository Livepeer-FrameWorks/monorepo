import { createContext, useContext } from "react";
import type {
  UsePlayerControllerConfig,
  UsePlayerControllerReturn,
} from "../hooks/usePlayerController";

export const PlayerContext = createContext<UsePlayerControllerReturn | null>(null);

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

// Type exports
export type { UsePlayerControllerReturn as PlayerContextValue };
export type { UsePlayerControllerConfig };

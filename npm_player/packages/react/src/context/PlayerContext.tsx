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

import React, { type ReactNode } from "react";
import {
  usePlayerController,
  type UsePlayerControllerConfig,
  type UsePlayerControllerReturn,
} from "../hooks/usePlayerController";
import { PlayerContext } from "./player";

export type PlayerProviderProps = {
  children: ReactNode;
} & (
  | { config: UsePlayerControllerConfig; value?: never }
  | { value: UsePlayerControllerReturn; config?: never }
);

/**
 * Provider component that wraps Player and its controls.
 *
 * Two modes:
 * - `config` — creates a new hook instance internally
 * - `value` — wraps children with a pre-computed hook return (used by Player component)
 */
export function PlayerProvider({ children, config, value }: PlayerProviderProps) {
  if (value !== undefined) {
    return <PlayerContext.Provider value={value}>{children}</PlayerContext.Provider>;
  }
  return <HookProvider config={config!}>{children}</HookProvider>;
}

function HookProvider({
  children,
  config,
}: {
  children: ReactNode;
  config: UsePlayerControllerConfig;
}) {
  const playerController = usePlayerController(config);
  return <PlayerContext.Provider value={playerController}>{children}</PlayerContext.Provider>;
}

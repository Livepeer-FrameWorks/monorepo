/**
 * StreamCrafter Context
 * React context for sharing StreamCrafter state across components
 */

import React, { createContext, useContext, type ReactNode } from "react";
import {
  useStreamCrafterV2 as useStreamCrafter,
  type UseStreamCrafterV2Options as UseStreamCrafterOptions,
  type UseStreamCrafterV2Return as UseStreamCrafterReturn,
} from "../hooks/useStreamCrafterV2";
import type { UseCompositorReturn } from "../hooks/useCompositor";

/**
 * What composable children of `<StreamCrafter>` read: the capture and
 * streaming state and actions, plus the compositor of that StreamCrafter.
 */
export interface StreamCrafterContextValue extends UseStreamCrafterReturn {
  /** The StreamCrafter's compositor; null when it has none, as with `config` mode. */
  compositor?: UseCompositorReturn | null;
}

const StreamCrafterContext = createContext<StreamCrafterContextValue | null>(null);

export type StreamCrafterProviderProps = {
  children: ReactNode;
} & (
  | { config: UseStreamCrafterOptions; value?: never }
  | { value: StreamCrafterContextValue; config?: never }
);

/**
 * Provides StreamCrafter state to child components.
 *
 * Two modes:
 * - `config` — creates a new hook instance internally, without a compositor
 * - `value` — wraps children with a pre-computed value (used by the StreamCrafter component)
 */
export function StreamCrafterProvider({ children, config, value }: StreamCrafterProviderProps) {
  if (value !== undefined) {
    return <StreamCrafterContext.Provider value={value}>{children}</StreamCrafterContext.Provider>;
  }
  return <HookProvider config={config!}>{children}</HookProvider>;
}

function HookProvider({
  children,
  config,
}: {
  children: ReactNode;
  config: UseStreamCrafterOptions;
}) {
  const streamCrafter = useStreamCrafter(config);
  return (
    <StreamCrafterContext.Provider value={streamCrafter}>{children}</StreamCrafterContext.Provider>
  );
}

export function useStreamCrafterContext(): StreamCrafterContextValue {
  const context = useContext(StreamCrafterContext);
  if (!context) {
    throw new Error("useStreamCrafterContext must be used within a StreamCrafterProvider");
  }
  return context;
}

/** Returns the context value or null if not inside a provider. */
export function useStreamCrafterContextOptional(): StreamCrafterContextValue | null {
  return useContext(StreamCrafterContext);
}

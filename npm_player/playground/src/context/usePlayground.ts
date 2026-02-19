import { createContext, useContext } from "react";
import type { PlaygroundState, PlaygroundActions } from "../lib/types";

export type PlaygroundContextValue = PlaygroundState & PlaygroundActions;

export const PlaygroundContext = createContext<PlaygroundContextValue | null>(null);

export function usePlayground(): PlaygroundContextValue {
  const context = useContext(PlaygroundContext);
  if (!context) {
    throw new Error("usePlayground must be used within a PlaygroundProvider");
  }
  return context;
}

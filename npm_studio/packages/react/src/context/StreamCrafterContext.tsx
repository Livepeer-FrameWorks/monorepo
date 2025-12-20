/**
 * StreamCrafter Context
 * React context for sharing StreamCrafter state across components
 */

import React, { createContext, useContext, type ReactNode } from 'react';
import { useStreamCrafterV2 as useStreamCrafter, type UseStreamCrafterV2Options as UseStreamCrafterOptions, type UseStreamCrafterV2Return as UseStreamCrafterReturn } from '../hooks/useStreamCrafterV2';

const StreamCrafterContext = createContext<UseStreamCrafterReturn | null>(null);

export interface StreamCrafterProviderProps {
  children: ReactNode;
  config: UseStreamCrafterOptions;
}

export function StreamCrafterProvider({ children, config }: StreamCrafterProviderProps) {
  const streamCrafter = useStreamCrafter(config);

  return (
    <StreamCrafterContext.Provider value={streamCrafter}>
      {children}
    </StreamCrafterContext.Provider>
  );
}

export function useStreamCrafterContext(): UseStreamCrafterReturn {
  const context = useContext(StreamCrafterContext);
  if (!context) {
    throw new Error('useStreamCrafterContext must be used within a StreamCrafterProvider');
  }
  return context;
}

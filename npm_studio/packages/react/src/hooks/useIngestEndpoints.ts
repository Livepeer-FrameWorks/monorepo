/**
 * useIngestEndpoints - React hook for gateway-based ingest endpoint resolution
 * Mirrors useViewerEndpoints from npm_player for consistency
 */

import { useState, useEffect, useCallback, useRef } from 'react';
import {
  IngestClient,
  type IngestEndpoints,
  type IngestClientStatus,
} from '@livepeer-frameworks/streamcrafter-core';

export interface UseIngestEndpointsOptions {
  /** Gateway GraphQL URL */
  gatewayUrl?: string;
  /** Stream key for ingest */
  streamKey?: string;
  /** JWT auth token (optional) */
  authToken?: string;
  /** Max retry attempts (default: 3) */
  maxRetries?: number;
  /** Initial retry delay in ms (default: 1000) */
  initialDelayMs?: number;
  /** Auto-resolve on mount (default: true) */
  autoResolve?: boolean;
}

export interface UseIngestEndpointsResult {
  /** Resolved endpoints (null if not resolved) */
  endpoints: IngestEndpoints | null;
  /** Current resolution status */
  status: IngestClientStatus;
  /** Error message if status is 'error' */
  error: string | null;
  /** Primary WHIP URL for streaming */
  whipUrl: string | null;
  /** Primary RTMP URL */
  rtmpUrl: string | null;
  /** Primary SRT URL */
  srtUrl: string | null;
  /** Manually trigger resolution */
  resolve: () => Promise<IngestEndpoints | null>;
  /** Reset state and clear endpoints */
  reset: () => void;
}

export function useIngestEndpoints(
  options: UseIngestEndpointsOptions = {}
): UseIngestEndpointsResult {
  const {
    gatewayUrl,
    streamKey,
    authToken,
    maxRetries = 3,
    initialDelayMs = 1000,
    autoResolve = true,
  } = options;

  const [endpoints, setEndpoints] = useState<IngestEndpoints | null>(null);
  const [status, setStatus] = useState<IngestClientStatus>('idle');
  const [error, setError] = useState<string | null>(null);

  const clientRef = useRef<IngestClient | null>(null);
  const lastOptionsRef = useRef<string>('');

  // Cleanup function
  const cleanup = useCallback(() => {
    if (clientRef.current) {
      clientRef.current.destroy();
      clientRef.current = null;
    }
  }, []);

  // Reset function
  const reset = useCallback(() => {
    cleanup();
    setEndpoints(null);
    setStatus('idle');
    setError(null);
  }, [cleanup]);

  // Resolve function
  const resolve = useCallback(async (): Promise<IngestEndpoints | null> => {
    if (!gatewayUrl || !streamKey) {
      return null;
    }

    // Cleanup previous client
    cleanup();

    // Create new client
    const client = new IngestClient({
      gatewayUrl,
      streamKey,
      authToken,
      maxRetries,
      initialDelayMs,
    });

    clientRef.current = client;

    // Set up event listeners
    client.on('statusChange', ({ status: newStatus, error: newError }) => {
      setStatus(newStatus);
      if (newError) {
        setError(newError);
      }
    });

    client.on('endpointsResolved', ({ endpoints: resolved }) => {
      setEndpoints(resolved);
      setError(null);
    });

    try {
      const resolved = await client.resolve();
      return resolved;
    } catch (err) {
      const message = err instanceof Error ? err.message : 'Unknown error';
      setError(message);
      return null;
    }
  }, [gatewayUrl, streamKey, authToken, maxRetries, initialDelayMs, cleanup]);

  // Auto-resolve when options change
  useEffect(() => {
    const optionsKey = JSON.stringify({ gatewayUrl, streamKey, authToken });

    // Skip if options haven't changed
    if (optionsKey === lastOptionsRef.current) {
      return;
    }
    lastOptionsRef.current = optionsKey;

    // Only auto-resolve if we have required params
    if (autoResolve && gatewayUrl && streamKey) {
      resolve();
    }

    return cleanup;
  }, [gatewayUrl, streamKey, authToken, autoResolve, resolve, cleanup]);

  // Cleanup on unmount
  useEffect(() => {
    return cleanup;
  }, [cleanup]);

  return {
    endpoints,
    status,
    error,
    whipUrl: endpoints?.primary?.whipUrl || null,
    rtmpUrl: endpoints?.primary?.rtmpUrl || null,
    srtUrl: endpoints?.primary?.srtUrl || null,
    resolve,
    reset,
  };
}

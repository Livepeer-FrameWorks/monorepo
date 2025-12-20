/**
 * ingestEndpoints - Svelte store for gateway-based ingest endpoint resolution
 * Mirrors viewerEndpoints from npm_player for consistency
 */

import { writable, derived, type Readable } from 'svelte/store';
import {
  IngestClient,
  type IngestEndpoints,
  type IngestClientStatus,
} from '@livepeer-frameworks/streamcrafter-core';

export interface IngestEndpointsOptions {
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
}

export interface IngestEndpointsState {
  endpoints: IngestEndpoints | null;
  status: IngestClientStatus;
  error: string | null;
}

export interface IngestEndpointsStore extends Readable<IngestEndpointsState> {
  /** Resolve ingest endpoints */
  resolve: (options: IngestEndpointsOptions) => Promise<IngestEndpoints | null>;
  /** Reset state and clear endpoints */
  reset: () => void;
  /** Destroy the client */
  destroy: () => void;
  /** Derived store for WHIP URL */
  whipUrl: Readable<string | null>;
  /** Derived store for RTMP URL */
  rtmpUrl: Readable<string | null>;
  /** Derived store for SRT URL */
  srtUrl: Readable<string | null>;
}

export function createIngestEndpointsStore(): IngestEndpointsStore {
  const { subscribe, set, update } = writable<IngestEndpointsState>({
    endpoints: null,
    status: 'idle',
    error: null,
  });

  let client: IngestClient | null = null;

  function cleanup(): void {
    if (client) {
      client.destroy();
      client = null;
    }
  }

  function reset(): void {
    cleanup();
    set({
      endpoints: null,
      status: 'idle',
      error: null,
    });
  }

  async function resolve(options: IngestEndpointsOptions): Promise<IngestEndpoints | null> {
    const { gatewayUrl, streamKey, authToken, maxRetries = 3, initialDelayMs = 1000 } = options;

    if (!gatewayUrl || !streamKey) {
      return null;
    }

    // Cleanup previous client
    cleanup();

    // Create new client
    client = new IngestClient({
      gatewayUrl,
      streamKey,
      authToken,
      maxRetries,
      initialDelayMs,
    });

    // Set up event listeners
    client.on('statusChange', ({ status, error }) => {
      update((state) => ({
        ...state,
        status,
        error: error || state.error,
      }));
    });

    client.on('endpointsResolved', ({ endpoints }) => {
      update((state) => ({
        ...state,
        endpoints,
        error: null,
      }));
    });

    try {
      const resolved = await client.resolve();
      return resolved;
    } catch (err) {
      const message = err instanceof Error ? err.message : 'Unknown error';
      update((state) => ({
        ...state,
        error: message,
      }));
      return null;
    }
  }

  function destroy(): void {
    cleanup();
  }

  // Create derived stores for convenience
  const whipUrl = derived({ subscribe }, ($state) => $state.endpoints?.primary?.whipUrl || null);
  const rtmpUrl = derived({ subscribe }, ($state) => $state.endpoints?.primary?.rtmpUrl || null);
  const srtUrl = derived({ subscribe }, ($state) => $state.endpoints?.primary?.srtUrl || null);

  return {
    subscribe,
    resolve,
    reset,
    destroy,
    whipUrl,
    rtmpUrl,
    srtUrl,
  };
}

// Default singleton instance
let defaultStore: IngestEndpointsStore | null = null;

export function getIngestEndpointsStore(): IngestEndpointsStore {
  if (!defaultStore) {
    defaultStore = createIngestEndpointsStore();
  }
  return defaultStore;
}

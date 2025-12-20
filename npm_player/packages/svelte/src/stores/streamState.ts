/**
 * Svelte store for MistServer stream state polling via WebSocket/HTTP.
 *
 * Port of useStreamState.ts React hook to Svelte 5 stores.
 */

import { writable, derived, type Readable } from 'svelte/store';
import type { StreamStatus, MistStreamInfo, StreamState } from '@livepeer-frameworks/player-core';

export interface StreamStateOptions {
  mistBaseUrl: string;
  streamName: string;
  pollInterval?: number;
  enabled?: boolean;
  useWebSocket?: boolean;
}

export interface StreamStateStore extends Readable<StreamState> {
  refetch: () => void;
  getSocket: () => WebSocket | null;
  isSocketReady: Readable<boolean>;
  destroy: () => void;
}

/**
 * Parse MistServer error string into StreamStatus enum
 */
function parseErrorToStatus(error: string): StreamStatus {
  const lowerError = error.toLowerCase();

  if (lowerError.includes('offline')) return 'OFFLINE';
  if (lowerError.includes('initializing')) return 'INITIALIZING';
  if (lowerError.includes('booting')) return 'BOOTING';
  if (lowerError.includes('waiting for data')) return 'WAITING_FOR_DATA';
  if (lowerError.includes('shutting down')) return 'SHUTTING_DOWN';
  if (lowerError.includes('invalid')) return 'INVALID';

  return 'ERROR';
}

/**
 * Get human-readable message for stream status
 */
function getStatusMessage(status: StreamStatus, percentage?: number): string {
  switch (status) {
    case 'ONLINE':
      return 'Stream is online';
    case 'OFFLINE':
      return 'Stream is offline';
    case 'INITIALIZING':
      return percentage !== undefined
        ? `Initializing... ${Math.round(percentage * 10) / 10}%`
        : 'Stream is initializing';
    case 'BOOTING':
      return 'Stream is starting up';
    case 'WAITING_FOR_DATA':
      return 'Waiting for stream data';
    case 'SHUTTING_DOWN':
      return 'Stream is shutting down';
    case 'INVALID':
      return 'Stream status is invalid';
    case 'ERROR':
    default:
      return 'Stream error';
  }
}

const initialState: StreamState = {
  status: 'OFFLINE',
  isOnline: false,
  message: 'Connecting...',
  lastUpdate: 0,
};

/**
 * Create a stream state manager store for MistServer polling.
 *
 * @example
 * ```svelte
 * <script>
 *   import { createStreamStateManager } from './stores/streamState';
 *
 *   const streamState = createStreamStateManager({
 *     mistBaseUrl: 'https://mist.example.com',
 *     streamName: 'my-stream',
 *   });
 *
 *   // Access values
 *   $: status = $streamState.status;
 *   $: isOnline = $streamState.isOnline;
 * </script>
 * ```
 */
export function createStreamStateManager(options: StreamStateOptions): StreamStateStore {
  const {
    mistBaseUrl,
    streamName,
    pollInterval = 3000,
    enabled = true,
    useWebSocket = true,
  } = options;

  const WS_TIMEOUT_MS = 5000;

  // Internal state
  const store = writable<StreamState>(initialState);
  const socketReady = writable(false);
  let ws: WebSocket | null = null;
  let pollTimeout: ReturnType<typeof setTimeout> | null = null;
  let wsTimeout: ReturnType<typeof setTimeout> | null = null;
  let mounted = true;

  /**
   * Process MistServer response data
   */
  function processStreamInfo(data: MistStreamInfo) {
    if (!mounted) return;

    if (data.error) {
      const status = parseErrorToStatus(data.error);
      const message = data.on_error || getStatusMessage(status, data.perc);

      store.update(prev => ({
        status,
        isOnline: false,
        message,
        percentage: data.perc,
        lastUpdate: Date.now(),
        error: data.error,
        streamInfo: prev.streamInfo, // Preserve track data through error states
      }));
    } else {
      // Stream is online with valid metadata
      store.update(prev => {
        const mergedStreamInfo: MistStreamInfo = {
          ...prev.streamInfo,
          ...data,
          source: data.source || prev.streamInfo?.source,
          meta: {
            ...prev.streamInfo?.meta,
            ...data.meta,
            tracks: data.meta?.tracks || prev.streamInfo?.meta?.tracks,
          },
        };

        return {
          status: 'ONLINE',
          isOnline: true,
          message: 'Stream is online',
          lastUpdate: Date.now(),
          streamInfo: mergedStreamInfo,
        };
      });
    }
  }

  /**
   * HTTP polling fallback
   */
  async function pollHttp() {
    if (!mounted || !enabled) return;

    try {
      const baseUrl = `${mistBaseUrl.replace(/\/$/, '')}/json_${encodeURIComponent(streamName)}.js`;
      const url = `${baseUrl}?metaeverywhere=1&inclzero=1`;
      const response = await fetch(url, {
        method: 'GET',
        headers: { 'Accept': 'application/json' },
      });

      if (!response.ok) {
        throw new Error(`HTTP ${response.status}`);
      }

      let text = await response.text();
      // Strip JSONP callback if present
      const jsonpMatch = text.match(/^[^(]+\(([\s\S]*)\);?$/);
      if (jsonpMatch) {
        text = jsonpMatch[1];
      }

      const data = JSON.parse(text) as MistStreamInfo;
      processStreamInfo(data);
    } catch (error) {
      if (!mounted) return;

      store.update(prev => ({
        ...prev,
        status: 'ERROR',
        isOnline: false,
        message: error instanceof Error ? error.message : 'Connection failed',
        lastUpdate: Date.now(),
        error: error instanceof Error ? error.message : 'Unknown error',
      }));
    }

    // Schedule next poll
    if (mounted && enabled && !useWebSocket) {
      pollTimeout = setTimeout(pollHttp, pollInterval);
    }
  }

  /**
   * WebSocket connection with timeout fallback
   */
  function connectWebSocket() {
    if (!mounted || !enabled || !useWebSocket) return;

    // Clean up existing
    if (wsTimeout) {
      clearTimeout(wsTimeout);
      wsTimeout = null;
    }
    if (ws) {
      ws.close();
      ws = null;
    }

    try {
      const wsUrl = mistBaseUrl
        .replace(/^http:/, 'ws:')
        .replace(/^https:/, 'wss:')
        .replace(/\/$/, '');

      const url = `${wsUrl}/json_${encodeURIComponent(streamName)}.js?metaeverywhere=1&inclzero=1`;
      const socket = new WebSocket(url);
      ws = socket;

      // Timeout: if no message within 5 seconds, fall back to HTTP
      wsTimeout = setTimeout(() => {
        if (socket.readyState <= WebSocket.OPEN) {
          console.debug('[streamState] WebSocket timeout (5s), falling back to HTTP');
          socket.close();
          pollHttp();
        }
      }, WS_TIMEOUT_MS);

      socket.onopen = () => {
        console.debug('[streamState] WebSocket connected');
        socketReady.set(true);
      };

      socket.onmessage = (event) => {
        if (wsTimeout) {
          clearTimeout(wsTimeout);
          wsTimeout = null;
        }

        try {
          const data = JSON.parse(event.data) as MistStreamInfo;
          processStreamInfo(data);
        } catch (e) {
          console.warn('[streamState] Failed to parse WebSocket message:', e);
        }
      };

      socket.onerror = () => {
        console.warn('[streamState] WebSocket error, falling back to HTTP');
        if (wsTimeout) {
          clearTimeout(wsTimeout);
          wsTimeout = null;
        }
        socket.close();
      };

      socket.onclose = () => {
        ws = null;
        socketReady.set(false);

        if (!mounted || !enabled) return;

        console.debug('[streamState] WebSocket closed, starting HTTP polling');
        pollHttp();
      };
    } catch (error) {
      console.warn('[streamState] WebSocket connection failed:', error);
      pollHttp();
    }
  }

  /**
   * Manual refetch
   */
  function refetch() {
    if (useWebSocket && ws?.readyState === WebSocket.OPEN) {
      return; // WebSocket will receive updates automatically
    }
    pollHttp();
  }

  /**
   * Get current WebSocket reference
   */
  function getSocket(): WebSocket | null {
    return ws;
  }

  /**
   * Cleanup and destroy the store
   */
  function destroy() {
    mounted = false;

    if (wsTimeout) {
      clearTimeout(wsTimeout);
      wsTimeout = null;
    }

    if (ws) {
      ws.onclose = null;
      ws.onerror = null;
      ws.onmessage = null;
      ws.onopen = null;
      ws.close();
      ws = null;
    }

    if (pollTimeout) {
      clearTimeout(pollTimeout);
      pollTimeout = null;
    }

    socketReady.set(false);
    store.set(initialState);
  }

  // Initialize connection
  if (enabled && mistBaseUrl && streamName) {
    store.set({
      ...initialState,
      message: 'Connecting...',
      lastUpdate: Date.now(),
    });

    // Initial HTTP poll then WebSocket
    const init = async () => {
      await pollHttp();
      if (useWebSocket && mounted) {
        connectWebSocket();
      }
    };
    init();
  }

  return {
    subscribe: store.subscribe,
    refetch,
    getSocket,
    isSocketReady: { subscribe: socketReady.subscribe },
    destroy,
  };
}

// Convenience derived stores for common values
export function createDerivedStreamStatus(store: StreamStateStore) {
  return derived(store, $state => $state.status);
}

export function createDerivedIsOnline(store: StreamStateStore) {
  return derived(store, $state => $state.isOnline);
}

export function createDerivedStreamInfo(store: StreamStateStore) {
  return derived(store, $state => $state.streamInfo);
}

export default createStreamStateManager;

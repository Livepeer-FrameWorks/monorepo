import { useEffect, useState, useRef, useCallback } from 'react';
import type {
  UseStreamStateOptions,
  StreamState,
  StreamStatus,
  MistStreamInfo,
} from '../types';

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

/**
 * Initial stream state
 */
const initialState: StreamState = {
  status: 'OFFLINE',
  isOnline: false,
  message: 'Connecting...',
  lastUpdate: 0,
};

/**
 * Hook to poll MistServer for stream status via WebSocket or HTTP
 *
 * Uses native MistServer protocol:
 * - WebSocket: ws://{baseUrl}/json_{streamName}.js
 * - HTTP fallback: GET {baseUrl}/json_{streamName}.js
 *
 * @example
 * ```tsx
 * const { status, isOnline, message } = useStreamState({
 *   mistBaseUrl: 'https://mist.example.com',
 *   streamName: 'pk_...', // playbackId (view key)
 *   pollInterval: 3000,
 * });
 * ```
 */
export interface UseStreamStateReturn extends StreamState {
  /** Manual refetch function */
  refetch: () => void;
  /** WebSocket reference for sharing with MistReporter */
  socketRef: React.RefObject<WebSocket | null>;
  /** True when WebSocket is connected and ready (triggers re-render) */
  socketReady: boolean;
}

export function useStreamState(options: UseStreamStateOptions): UseStreamStateReturn {
  const {
    mistBaseUrl,
    streamName,
    pollInterval = 3000,
    enabled = true,
    useWebSocket = true,
    debug = false,
  } = options;

  const [state, setState] = useState<StreamState>(initialState);
  const [socketReady, setSocketReady] = useState(false);
  const wsRef = useRef<WebSocket | null>(null);
  const pollTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const wsTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const mountedRef = useRef(true);

  // MistPlayer-style WebSocket timeout (5 seconds)
  const WS_TIMEOUT_MS = 5000;

  /**
   * Process MistServer response data
   */
  const processStreamInfo = useCallback((data: MistStreamInfo) => {
    if (!mountedRef.current) return;

    if (data.error) {
      // Stream has an error state - preserve previous streamInfo (track data)
      const status = parseErrorToStatus(data.error);
      const message = data.on_error || getStatusMessage(status, data.perc);

      setState(prev => ({
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
      // Merge new data with existing streamInfo to preserve source/tracks from initial fetch
      // WebSocket updates may not include source array - only status updates
      setState(prev => {
        const mergedStreamInfo: MistStreamInfo = {
          ...prev.streamInfo,  // Keep existing source/meta if present
          ...data,             // Override with new data
          // Explicitly preserve source if not in new data
          source: data.source || prev.streamInfo?.source,
          // Merge meta to preserve tracks
          meta: {
            ...prev.streamInfo?.meta,
            ...data.meta,
            // Preserve tracks if not in new data
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
  }, []);

  /**
   * HTTP polling fallback
   * Adds metaeverywhere=1 and inclzero=1 like MistPlayer
   */
  const pollHttp = useCallback(async () => {
    if (!mountedRef.current || !enabled) return;

    try {
      // Build URL with MistPlayer-style params
      const baseUrl = `${mistBaseUrl.replace(/\/$/, '')}/json_${encodeURIComponent(streamName)}.js`;
      const url = `${baseUrl}?metaeverywhere=1&inclzero=1`;
      const response = await fetch(url, {
        method: 'GET',
        headers: { 'Accept': 'application/json' },
      });

      if (!response.ok) {
        throw new Error(`HTTP ${response.status}`);
      }

      // MistServer returns JSON with potential JSONP wrapper
      let text = await response.text();
      // Strip JSONP callback if present (use [\s\S]* instead of /s flag for ES5 compat)
      const jsonpMatch = text.match(/^[^(]+\(([\s\S]*)\);?$/);
      if (jsonpMatch) {
        text = jsonpMatch[1];
      }

      const data = JSON.parse(text) as MistStreamInfo;
      processStreamInfo(data);
    } catch (error) {
      if (!mountedRef.current) return;

      setState(prev => ({
        ...prev,
        status: 'ERROR',
        isOnline: false,
        message: error instanceof Error ? error.message : 'Connection failed',
        lastUpdate: Date.now(),
        error: error instanceof Error ? error.message : 'Unknown error',
      }));
    }

    // Schedule next poll
    if (mountedRef.current && enabled && !useWebSocket) {
      pollTimeoutRef.current = setTimeout(pollHttp, pollInterval);
    }
  }, [mistBaseUrl, streamName, enabled, useWebSocket, pollInterval, processStreamInfo]);

  /**
   * WebSocket connection with MistPlayer-style 5-second timeout
   */
  const connectWebSocket = useCallback(() => {
    if (!mountedRef.current || !enabled || !useWebSocket) return;

    // Clean up existing connection and timeout
    if (wsTimeoutRef.current) {
      clearTimeout(wsTimeoutRef.current);
      wsTimeoutRef.current = null;
    }
    if (wsRef.current) {
      wsRef.current.close();
      wsRef.current = null;
    }

    try {
      // Convert http(s) to ws(s)
      const wsUrl = mistBaseUrl
        .replace(/^http:/, 'ws:')
        .replace(/^https:/, 'wss:')
        .replace(/\/$/, '');

      // Build URL with MistPlayer-style params
      const url = `${wsUrl}/json_${encodeURIComponent(streamName)}.js?metaeverywhere=1&inclzero=1`;
      const ws = new WebSocket(url);
      wsRef.current = ws;

      // MistPlayer-style timeout: if no message within 5 seconds, fall back to HTTP
      wsTimeoutRef.current = setTimeout(() => {
        if (ws.readyState <= WebSocket.OPEN) {
          if (debug) {
            console.debug('[useStreamState] WebSocket timeout (5s), falling back to HTTP polling');
          }
          ws.close();
          pollHttp();
        }
      }, WS_TIMEOUT_MS);

      ws.onopen = () => {
        if (debug) {
          console.debug('[useStreamState] WebSocket connected');
        }
        setSocketReady(true);
      };

      ws.onmessage = (event) => {
        // Clear timeout on first message
        if (wsTimeoutRef.current) {
          clearTimeout(wsTimeoutRef.current);
          wsTimeoutRef.current = null;
        }

        try {
          const data = JSON.parse(event.data) as MistStreamInfo;
          processStreamInfo(data);
        } catch (e) {
          console.warn('[useStreamState] Failed to parse WebSocket message:', e);
        }
      };

      ws.onerror = (_event) => {
        console.warn('[useStreamState] WebSocket error, falling back to HTTP polling');
        if (wsTimeoutRef.current) {
          clearTimeout(wsTimeoutRef.current);
          wsTimeoutRef.current = null;
        }
        ws.close();
      };

      ws.onclose = () => {
        wsRef.current = null;
        setSocketReady(false);

        if (!mountedRef.current || !enabled) return;

        // Fallback to HTTP polling or reconnect
        if (debug) {
          console.debug('[useStreamState] WebSocket closed, starting HTTP polling');
        }
        pollHttp();
      };
    } catch (error) {
      console.warn('[useStreamState] WebSocket connection failed:', error);
      // Fallback to HTTP polling
      pollHttp();
    }
  }, [mistBaseUrl, streamName, enabled, useWebSocket, debug, processStreamInfo, pollHttp]);

  /**
   * Manual refetch function
   */
  const refetch = useCallback(() => {
    if (useWebSocket && wsRef.current?.readyState === WebSocket.OPEN) {
      // WebSocket will receive updates automatically
      return;
    }
    pollHttp();
  }, [useWebSocket, pollHttp]);

  /**
   * Setup connection on mount and when options change
   * Always do initial HTTP poll to get full stream info (including sources),
   * then connect WebSocket for real-time status updates.
   * MistServer WebSocket updates may not include source array.
   */
  useEffect(() => {
    mountedRef.current = true;

    if (!enabled || !mistBaseUrl || !streamName) {
      setState(initialState);
      return;
    }

    // Reset state when stream changes
    setState({
      ...initialState,
      message: 'Connecting...',
      lastUpdate: Date.now(),
    });

    // Always do initial HTTP poll to get full data (including sources)
    // Then connect WebSocket for real-time updates
    const initializeConnection = async () => {
      // First HTTP poll to get complete stream info
      await pollHttp();

      // Then connect WebSocket for status updates (if enabled)
      if (useWebSocket && mountedRef.current) {
        connectWebSocket();
      }
    };

    initializeConnection();

    return () => {
      // Set mounted=false FIRST before any other cleanup
      mountedRef.current = false;
      if (debug) {
        console.debug('[useStreamState] cleanup starting, mountedRef set to false');
      }

      // Cleanup WebSocket timeout
      if (wsTimeoutRef.current) {
        clearTimeout(wsTimeoutRef.current);
        wsTimeoutRef.current = null;
      }

      // Cleanup WebSocket - remove handlers BEFORE closing to prevent onclose callback
      if (wsRef.current) {
        // Detach handlers first to prevent onclose from triggering pollHttp
        wsRef.current.onclose = null;
        wsRef.current.onerror = null;
        wsRef.current.onmessage = null;
        wsRef.current.onopen = null;
        wsRef.current.close();
        wsRef.current = null;
      }

      // Cleanup polling timeout
      if (pollTimeoutRef.current) {
        clearTimeout(pollTimeoutRef.current);
        pollTimeoutRef.current = null;
      }
    };
  }, [enabled, mistBaseUrl, streamName, useWebSocket, debug, connectWebSocket, pollHttp]);

  return {
    ...state,
    refetch,
    socketRef: wsRef,
    socketReady,
  };
}

export default useStreamState;

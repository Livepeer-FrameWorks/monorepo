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
 * - WebSocket: ws://{baseUrl}/api/{streamName}
 * - HTTP fallback: GET {baseUrl}/api/{streamName}/info.js
 *
 * @example
 * ```tsx
 * const { status, isOnline, message } = useStreamState({
 *   mistBaseUrl: 'https://mist.example.com',
 *   streamName: 'my-stream',
 *   pollInterval: 3000,
 * });
 * ```
 */
export function useStreamState(options: UseStreamStateOptions) {
  const {
    mistBaseUrl,
    streamName,
    pollInterval = 3000,
    enabled = true,
    useWebSocket = true,
  } = options;

  const [state, setState] = useState<StreamState>(initialState);
  const wsRef = useRef<WebSocket | null>(null);
  const pollTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const mountedRef = useRef(true);

  /**
   * Process MistServer response data
   */
  const processStreamInfo = useCallback((data: MistStreamInfo) => {
    if (!mountedRef.current) return;

    if (data.error) {
      // Stream has an error state
      const status = parseErrorToStatus(data.error);
      const message = data.on_error || getStatusMessage(status, data.perc);

      setState({
        status,
        isOnline: false,
        message,
        percentage: data.perc,
        lastUpdate: Date.now(),
        error: data.error,
      });
    } else {
      // Stream is online with valid metadata
      setState({
        status: 'ONLINE',
        isOnline: true,
        message: 'Stream is online',
        lastUpdate: Date.now(),
        streamInfo: data,
      });
    }
  }, []);

  /**
   * HTTP polling fallback
   */
  const pollHttp = useCallback(async () => {
    if (!mountedRef.current || !enabled) return;

    try {
      const url = `${mistBaseUrl.replace(/\/$/, '')}/api/${streamName}`;
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
   * WebSocket connection
   */
  const connectWebSocket = useCallback(() => {
    if (!mountedRef.current || !enabled || !useWebSocket) return;

    // Clean up existing connection
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

      const ws = new WebSocket(`${wsUrl}/api/${streamName}`);
      wsRef.current = ws;

      ws.onopen = () => {
        console.debug('[useStreamState] WebSocket connected');
      };

      ws.onmessage = (event) => {
        try {
          const data = JSON.parse(event.data) as MistStreamInfo;
          processStreamInfo(data);
        } catch (e) {
          console.warn('[useStreamState] Failed to parse WebSocket message:', e);
        }
      };

      ws.onerror = (event) => {
        console.warn('[useStreamState] WebSocket error, falling back to HTTP polling');
        ws.close();
      };

      ws.onclose = () => {
        wsRef.current = null;

        if (!mountedRef.current || !enabled) return;

        // Fallback to HTTP polling or reconnect
        console.debug('[useStreamState] WebSocket closed, starting HTTP polling');
        pollHttp();
      };
    } catch (error) {
      console.warn('[useStreamState] WebSocket connection failed:', error);
      // Fallback to HTTP polling
      pollHttp();
    }
  }, [mistBaseUrl, streamName, enabled, useWebSocket, processStreamInfo, pollHttp]);

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

    if (useWebSocket) {
      connectWebSocket();
    } else {
      pollHttp();
    }

    return () => {
      mountedRef.current = false;

      // Cleanup WebSocket
      if (wsRef.current) {
        wsRef.current.close();
        wsRef.current = null;
      }

      // Cleanup polling timeout
      if (pollTimeoutRef.current) {
        clearTimeout(pollTimeoutRef.current);
        pollTimeoutRef.current = null;
      }
    };
  }, [enabled, mistBaseUrl, streamName, useWebSocket, connectWebSocket, pollHttp]);

  return {
    ...state,
    refetch,
  };
}

export default useStreamState;

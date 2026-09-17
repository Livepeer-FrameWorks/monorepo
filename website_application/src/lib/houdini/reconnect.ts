type ReconnectListener = () => void;

const listeners = new Set<ReconnectListener>();

/**
 * Runs `listener` each time the shared GraphQL WebSocket reconnects after a
 * drop. graphql-ws resubscribes active operations on its own, but events sent
 * while the socket was down are lost, so consumers refetch here.
 */
export function onRealtimeReconnect(listener: ReconnectListener): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

export function notifyRealtimeReconnected(): void {
  for (const listener of [...listeners]) {
    try {
      listener();
    } catch (error) {
      console.warn("[WebSocket] Reconnect listener failed:", error);
    }
  }
}

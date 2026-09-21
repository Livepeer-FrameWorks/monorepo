import type { ClientOptions } from "graphql-ws";
import { refreshAuthSession } from "$lib/auth/refresh";
import { notifyRealtimeReconnected } from "$lib/houdini/reconnect";

/**
 * graphql-ws options of the webapp's shared realtime connection.
 *
 * The connection authenticates only with the HttpOnly access_token cookie
 * the browser sends on the upgrade. connectionParams carries no token: the
 * session token the page rendered with expires while the page stays open,
 * and the gateway closes any connection whose Authorization value does not
 * authenticate, so sending it would reject every reconnect after expiry.
 * Before each reconnect the session is refreshed, so the upgrade carries a
 * live cookie even after the tab slept past the refresh timer.
 */
export function realtimeClientOptions(url: string): ClientOptions {
  return {
    url,
    retryAttempts: Number.POSITIVE_INFINITY,
    retryWait: async (retries) => {
      const delayMs = Math.min(30_000, 1_000 * 2 ** retries);
      // A failed refresh still reconnects: the cookie may be valid, and an
      // expired one connects as anonymous until the next refresh.
      await refreshAuthSession().catch(() => "transient");
      await new Promise((resolve) => setTimeout(resolve, delayMs));
    },
    shouldRetry: () => true,
    // Handle connection errors gracefully (logged, not thrown to global error handler)
    on: {
      connecting: (isRetry) => {
        if (isRetry) {
          console.info("[WebSocket] Reconnecting");
        }
      },
      connected: (_socket, _payload, wasRetry) => {
        if (wasRetry) {
          console.info("[WebSocket] Reconnected");
          notifyRealtimeReconnected();
        }
      },
      error: (error) => {
        console.warn("[WebSocket] Connection error:", error);
      },
      closed: (event) => {
        // Only log unexpected closures (not clean shutdowns)
        if (event && typeof event === "object" && "code" in event) {
          const closeEvent = event as CloseEvent;
          if (closeEvent.code !== 1000) {
            console.warn("[WebSocket] Connection closed:", closeEvent.code, closeEvent.reason);
          }
        }
      },
    },
  };
}

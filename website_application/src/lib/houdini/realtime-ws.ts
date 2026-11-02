import type { ClientOptions } from "graphql-ws";
import { refreshAuthSession } from "$lib/auth/refresh";
import { notifyRealtimeReconnected } from "$lib/houdini/reconnect";
import {
  ACCOUNT_DEACTIVATED_CLOSE_REASON,
  CLOSE_FORBIDDEN,
  endSession,
} from "$lib/houdini/session-end";

function closeEventOf(event: unknown): CloseEvent | null {
  return event && typeof event === "object" && "code" in event ? (event as CloseEvent) : null;
}

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
 *
 * A 4403 close means the gateway stopped accepting the credential. With the
 * reason "account deactivated" the tab signs out without retrying; otherwise
 * the next refresh decides, and a refused refresh signs the tab out too.
 */
export function realtimeClientOptions(url: string): ClientOptions {
  let forbidden = false;
  let sessionEnded = false;

  return {
    url,
    retryAttempts: Number.POSITIVE_INFINITY,
    retryWait: async (retries) => {
      const delayMs = Math.min(30_000, 1_000 * 2 ** retries);
      // A failed refresh after any other close still reconnects: the cookie
      // may be valid, and an expired one connects as anonymous until the next refresh.
      const refreshed = await refreshAuthSession().catch(() => "transient" as const);
      if (refreshed === "unauthorized" && forbidden) {
        sessionEnded = true;
        void endSession("expired");
        throw new Error("session ended: the gateway refused the credential and its refresh");
      }
      await new Promise((resolve) => setTimeout(resolve, delayMs));
    },
    shouldRetry: (event) => {
      const close = closeEventOf(event);
      if (close?.code === CLOSE_FORBIDDEN && close.reason === ACCOUNT_DEACTIVATED_CLOSE_REASON) {
        sessionEnded = true;
        void endSession("deactivated");
      }
      return !sessionEnded;
    },
    // Handle connection errors gracefully (logged, not thrown to global error handler)
    on: {
      connecting: (isRetry) => {
        if (isRetry) {
          console.info("[WebSocket] Reconnecting");
        }
      },
      connected: (_socket, _payload, wasRetry) => {
        forbidden = false;
        if (wasRetry) {
          console.info("[WebSocket] Reconnected");
          notifyRealtimeReconnected();
        }
      },
      error: (error) => {
        console.warn("[WebSocket] Connection error:", error);
      },
      closed: (event) => {
        const close = closeEventOf(event);
        if (!close) {
          return;
        }
        forbidden = close.code === CLOSE_FORBIDDEN;
        // Only log unexpected closures (not clean shutdowns)
        if (close.code !== 1000) {
          console.warn("[WebSocket] Connection closed:", close.code, close.reason);
        }
      },
    },
  };
}

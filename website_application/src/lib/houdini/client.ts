import { HoudiniClient, type ClientPlugin } from "$houdini";
import { subscription } from "$houdini/plugins";
import { createClient } from "graphql-ws";
import { browser } from "$app/environment";

const GRAPHQL_HTTP_URL = import.meta.env.VITE_GRAPHQL_HTTP_URL ?? "";
const GRAPHQL_WS_URL = import.meta.env.VITE_GRAPHQL_WS_URL ?? "";

// Session type from hooks.server.ts
type Session = {
  token: string | null;
  tenantId: string | null;
};

export default new HoudiniClient({
  url: GRAPHQL_HTTP_URL,

  // HTTP requests: cookies sent automatically with credentials: 'include'
  // SSR: headers added from session (populated from cookies in hooks.server.ts)
  fetchParams({ session }) {
    const sess = session as Session | null;
    const headers: Record<string, string> = {
      "Content-Type": "application/json",
    };

    // For SSR: add headers from session (cookies not available server-side)
    if (!browser && sess?.token) {
      headers["Authorization"] = `Bearer ${sess.token}`;
    }
    if (!browser && sess?.tenantId) {
      headers["X-Tenant-ID"] = sess.tenantId;
    }

    return {
      headers,
      credentials: "include" as RequestCredentials, // Send cookies with requests
    };
  },

  // WebSocket subscriptions - use session for auth
  plugins: browser
    ? [
        subscription(({ session }) => {
          const sess = session as Session | null;
          return createClient({
            url: GRAPHQL_WS_URL,
            connectionParams: () => {
              // Pass auth from session to WebSocket connection
              const params: Record<string, string> = {};
              if (sess?.token) {
                params["Authorization"] = `Bearer ${sess.token}`;
              }
              if (sess?.tenantId) {
                params["X-Tenant-ID"] = sess.tenantId;
              }
              return params;
            },
            retryAttempts: 3,
            shouldRetry: () => true,
            // Handle connection errors gracefully (logged, not thrown to global error handler)
            on: {
              error: (error) => {
                console.warn("[WebSocket] Connection error:", error);
              },
              closed: (event) => {
                // Only log unexpected closures (not clean shutdowns)
                if (event && typeof event === "object" && "code" in event) {
                  const closeEvent = event as CloseEvent;
                  if (closeEvent.code !== 1000) {
                    console.warn(
                      "[WebSocket] Connection closed:",
                      closeEvent.code,
                      closeEvent.reason,
                    );
                  }
                }
              },
            },
          });
        }) as ClientPlugin,
      ]
    : [],
});

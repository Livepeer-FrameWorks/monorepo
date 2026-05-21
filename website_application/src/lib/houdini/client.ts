import { HoudiniClient, type ClientPlugin } from "$houdini";
import { fetch as houdiniFetch, subscription, type RequestHandlerArgs } from "$houdini/plugins";
import { createClient } from "graphql-ws";
import { browser } from "$app/environment";

const GRAPHQL_HTTP_URL = import.meta.env.VITE_GRAPHQL_HTTP_URL ?? "";
const GRAPHQL_WS_URL = import.meta.env.VITE_GRAPHQL_WS_URL ?? "";
const AUTH_URL = import.meta.env.VITE_AUTH_URL ?? "";

// Session type from hooks.server.ts
type Session = {
  token: string | null;
  tenantId: string | null;
};

type X402ResponseBody = {
  accepts?: unknown;
  code?: string;
  error?: string;
  message?: string;
  operation?: string;
  topup_url?: string;
  x402Version?: number;
};

let refreshSessionPromise: Promise<boolean> | null = null;

async function refreshAuthCookies(fetch: typeof globalThis.fetch): Promise<boolean> {
  if (!browser || !AUTH_URL) {
    return false;
  }

  refreshSessionPromise ??= (async () => {
    const response = await fetch(`${AUTH_URL}/refresh`, {
      method: "POST",
      credentials: "include",
      headers: {
        Accept: "application/json",
        "Content-Type": "application/json",
      },
    });

    if (!response.ok) {
      return false;
    }

    const payload = (await response.json()) as { user?: unknown };
    if (payload.user && typeof payload.user === "object") {
      localStorage.setItem("user", JSON.stringify(payload.user));
    }
    return true;
  })();

  try {
    return await refreshSessionPromise;
  } catch {
    return false;
  } finally {
    refreshSessionPromise = null;
  }
}

async function postGraphQL(
  fetch: typeof globalThis.fetch,
  name: string,
  text: string,
  variables: Record<string, unknown>,
  headers: Record<string, string>
) {
  return fetch(GRAPHQL_HTTP_URL, {
    method: "POST",
    credentials: "include",
    headers,
    body: JSON.stringify({ operationName: name, query: text, variables }),
  });
}

async function graphQLFetch({ fetch, name, text, variables, session }: RequestHandlerArgs) {
  const sess = session as Session | null;
  const headers: Record<string, string> = {
    Accept: "application/graphql+json, application/json",
    "Content-Type": "application/json",
  };

  if (!browser && sess?.token) {
    headers["Authorization"] = `Bearer ${sess.token}`;
  }
  if (!browser && sess?.tenantId) {
    headers["X-Tenant-ID"] = sess.tenantId;
  }

  let response = await postGraphQL(fetch, name, text, variables, headers);
  if ((response.status === 401 || response.status === 402) && (await refreshAuthCookies(fetch))) {
    response = await postGraphQL(fetch, name, text, variables, headers);
  }

  const contentType = response.headers.get("content-type") ?? "";
  const isJSON =
    contentType.startsWith("application/json") ||
    contentType.startsWith("application/graphql+json");

  if (!isJSON) {
    if (!response.ok) {
      throw new Error(
        `Failed to fetch: server returned invalid response with error ${response.status}: ${response.statusText}`
      );
    }
    return response.json();
  }

  const payload = await response.json();

  if (response.status === 402) {
    const body = payload as X402ResponseBody;
    return {
      data: null,
      errors: [
        {
          message: body.message || "Payment required",
        },
      ],
    };
  }

  if (!response.ok && !("errors" in payload)) {
    throw new Error(`Failed to fetch: server returned ${response.status}: ${response.statusText}`);
  }

  return payload;
}

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
            retryAttempts: Number.POSITIVE_INFINITY,
            retryWait: async (retries) => {
              const delayMs = Math.min(30_000, 1_000 * 2 ** retries);
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
                    console.warn(
                      "[WebSocket] Connection closed:",
                      closeEvent.code,
                      closeEvent.reason
                    );
                  }
                }
              },
            },
          });
        }) as ClientPlugin,
        houdiniFetch(graphQLFetch) as ClientPlugin,
      ]
    : [],
});

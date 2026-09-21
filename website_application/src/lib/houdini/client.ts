import { HoudiniClient, type ClientPlugin } from "$houdini";
import { fetch as houdiniFetch, subscription, type RequestHandlerArgs } from "$houdini/plugins";
import { createClient } from "graphql-ws";
import { browser } from "$app/environment";
import { refreshAuthSession } from "$lib/auth/refresh";
import { realtimeClientOptions } from "$lib/houdini/realtime-ws";

const GRAPHQL_HTTP_URL = import.meta.env.VITE_GRAPHQL_HTTP_URL ?? "";
const GRAPHQL_WS_URL = import.meta.env.VITE_GRAPHQL_WS_URL ?? "";

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
  required_fields?: unknown;
  requirements?: unknown;
  x402Version?: number;
};

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
  if (response.status === 401 && (await refreshAuthSession(fetch)) === "ok") {
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
          extensions: {
            code: body.code || "PAYMENT_REQUIRED",
            error: body.error,
            operation: body.operation,
            topup_url: body.topup_url,
            accepts: body.accepts,
            required_fields: body.required_fields,
            requirements: body.requirements,
            x402Version: body.x402Version,
          },
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

  // WebSocket subscriptions authenticate with the access_token cookie.
  plugins: browser
    ? [
        subscription(() => createClient(realtimeClientOptions(GRAPHQL_WS_URL))) as ClientPlugin,
        houdiniFetch(graphQLFetch) as ClientPlugin,
      ]
    : [],
});

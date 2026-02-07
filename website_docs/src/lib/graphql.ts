import { createClient, type Client } from "graphql-ws";

const API_URL = import.meta.env.PUBLIC_API_URL || "";

export async function gqlQuery<T>(query: string, variables?: Record<string, unknown>): Promise<T> {
  const res = await fetch(`${API_URL}/graphql/`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    credentials: "include",
    body: JSON.stringify({ query, variables }),
  });
  const json = await res.json();
  if (json.errors) throw new Error(json.errors[0].message);
  return json.data as T;
}

let wsClient: Client | null = null;

export function getSubscriptionClient(): Client {
  if (!wsClient) {
    const wsUrl = API_URL
      ? API_URL.replace(/^http/, "ws") + "/graphql/ws"
      : `${location.protocol === "https:" ? "wss:" : "ws:"}//${location.host}/graphql/ws`;

    wsClient = createClient({
      url: wsUrl,
      connectionParams: () => ({}), // Auth via cookie on HTTP upgrade
    });
  }
  return wsClient;
}

export function disposeSubscriptionClient() {
  if (wsClient) {
    wsClient.dispose();
    wsClient = null;
  }
}

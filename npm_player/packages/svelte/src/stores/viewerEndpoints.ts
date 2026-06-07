/**
 * Svelte store for Gateway GraphQL endpoint resolution with retry logic.
 *
 * Port of useViewerEndpoints.ts React hook to Svelte 5 stores.
 */

import { writable, derived, type Readable } from "svelte/store";
import type { ContentEndpoints, ContentType } from "@livepeer-frameworks/player-core";

const DEFAULT_GATEWAY_URL = "https://bridge.frameworks.network/graphql";

export interface ViewerEndpointsOptions {
  gatewayUrl?: string;
  contentId: string;
  contentType?: ContentType;
  authToken?: string;
}

export type EndpointStatus = "idle" | "loading" | "ready" | "error";

export interface ViewerEndpointsState {
  endpoints: ContentEndpoints | null;
  status: EndpointStatus;
  error: string | null;
}

export interface ViewerEndpointsStore extends Readable<ViewerEndpointsState> {
  refetch: () => void;
  destroy: () => void;
}

const MAX_RETRIES = 3;
const INITIAL_DELAY_MS = 500;
const RETRYABLE_HTTP_STATUSES = new Set([408, 429, 500, 502, 503, 504]);

type GraphQLErrorPayload = {
  message?: string;
  extensions?: {
    code?: string;
  };
};

function getFirstGraphQLError(payload: unknown): GraphQLErrorPayload | null {
  const errors = (payload as { errors?: GraphQLErrorPayload[] })?.errors;
  return Array.isArray(errors) && errors.length > 0 ? errors[0] : null;
}

function isRetryableGraphQLError(error: GraphQLErrorPayload): boolean {
  const code = error.extensions?.code?.toUpperCase();
  const message = (error.message || "").toLowerCase();

  return (
    code === "UNAVAILABLE" ||
    code === "SERVICE_UNAVAILABLE" ||
    message.includes("temporarily unavailable")
  );
}

/**
 * Fetch and parse Gateway resolution responses with exponential backoff retry.
 */
async function fetchResolvePayloadWithRetry(
  url: string,
  options: RequestInit,
  maxRetries: number = MAX_RETRIES,
  initialDelay: number = INITIAL_DELAY_MS
): Promise<unknown> {
  let lastError: Error | null = null;

  for (let attempt = 0; attempt < maxRetries; attempt++) {
    let response: Response | null = null;

    try {
      response = await fetch(url, options);
    } catch (e) {
      lastError = e instanceof Error ? e : new Error("Fetch failed");

      // Don't retry on abort
      if (options.signal?.aborted) {
        throw lastError;
      }

      if (attempt < maxRetries - 1) {
        const delay = initialDelay * Math.pow(2, attempt);
        console.warn(`[viewerEndpoints] Retry ${attempt + 1}/${maxRetries - 1} after ${delay}ms`);
        await new Promise((resolve) => setTimeout(resolve, delay));
        continue;
      }
    }

    if (!response) {
      throw lastError ?? new Error("Gateway unreachable after retries");
    }

    if (!response.ok) {
      lastError = new Error(`Gateway GQL error ${response.status}`);

      if (RETRYABLE_HTTP_STATUSES.has(response.status) && attempt < maxRetries - 1) {
        const delay = initialDelay * Math.pow(2, attempt);
        console.warn(`[viewerEndpoints] Retry ${attempt + 1}/${maxRetries - 1} after ${delay}ms`);
        await new Promise((resolve) => setTimeout(resolve, delay));
        continue;
      }

      throw lastError;
    }

    const payload = await response.json();
    const gqlError = getFirstGraphQLError(payload);

    if (gqlError) {
      lastError = new Error(gqlError.message || "GraphQL error");

      if (isRetryableGraphQLError(gqlError) && attempt < maxRetries - 1) {
        const delay = initialDelay * Math.pow(2, attempt);
        console.warn(`[viewerEndpoints] Retry ${attempt + 1}/${maxRetries - 1} after ${delay}ms`);
        await new Promise((resolve) => setTimeout(resolve, delay));
        continue;
      }

      throw lastError;
    }

    return payload;
  }

  throw lastError ?? new Error("Gateway unreachable after retries");
}

const initialState: ViewerEndpointsState = {
  endpoints: null,
  status: "idle",
  error: null,
};

/**
 * Create a viewer endpoints resolver store for Gateway GraphQL queries.
 *
 * @example
 * ```svelte
 * <script>
 *   import { createEndpointResolver } from './stores/viewerEndpoints';
 *
 *   const resolver = createEndpointResolver({
 *     contentType: 'live',
 *     contentId: 'pk_...',
 *   });
 *
 *   $: endpoints = $resolver.endpoints;
 *   $: status = $resolver.status;
 * </script>
 * ```
 */
export function createEndpointResolver(options: ViewerEndpointsOptions): ViewerEndpointsStore {
  const { gatewayUrl, contentId, authToken } = options;
  const effectiveGatewayUrl = gatewayUrl?.trim() || DEFAULT_GATEWAY_URL;

  const store = writable<ViewerEndpointsState>(initialState);
  let abortController: AbortController | null = null;
  let mounted = true;

  /**
   * Fetch endpoints from Gateway
   */
  async function fetchEndpoints() {
    if (!contentId || !mounted) return;

    // Abort previous request
    abortController?.abort();
    abortController = new AbortController();

    store.update((s) => ({ ...s, status: "loading", error: null }));

    try {
      const graphqlEndpoint = effectiveGatewayUrl.replace(/\/$/, "");
      const query = `
        query ResolveViewer($contentId: String!) {
          resolveViewerEndpoint(contentId: $contentId) {
            primary { nodeId baseUrl protocol url geoDistance loadScore outputs }
            fallbacks { nodeId baseUrl protocol url geoDistance loadScore outputs }
            metadata { contentType contentId title description durationSeconds status isLive viewers recordingSizeBytes clipSource createdAt telemetryToken }
          }
        }
      `;

      const payload = await fetchResolvePayloadWithRetry(graphqlEndpoint, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          ...(authToken ? { Authorization: `Bearer ${authToken}` } : {}),
        },
        body: JSON.stringify({ query, variables: { contentId } }),
        signal: abortController.signal,
      });

      const resp = (payload as { data?: { resolveViewerEndpoint?: any } }).data
        ?.resolveViewerEndpoint;
      const primary = resp?.primary;
      const fallbacks = Array.isArray(resp?.fallbacks) ? resp.fallbacks : [];

      if (!primary) throw new Error("No endpoints");

      // Parse outputs JSON string (GraphQL returns JSON scalar as string)
      if (primary.outputs && typeof primary.outputs === "string") {
        try {
          primary.outputs = JSON.parse(primary.outputs);
        } catch {}
      }
      if (fallbacks) {
        fallbacks.forEach((fb: typeof primary) => {
          if (fb.outputs && typeof fb.outputs === "string") {
            try {
              fb.outputs = JSON.parse(fb.outputs);
            } catch {}
          }
        });
      }

      store.set({
        endpoints: { primary, fallbacks, metadata: resp?.metadata },
        status: "ready",
        error: null,
      });
    } catch (e) {
      if (abortController?.signal.aborted || !mounted) return;

      const message = e instanceof Error ? e.message : "Unknown gateway error";
      console.error("[viewerEndpoints] Gateway resolution failed:", message);

      store.set({
        endpoints: null,
        status: "error",
        error: message,
      });
    }
  }

  /**
   * Manual refetch
   */
  function refetch() {
    fetchEndpoints();
  }

  /**
   * Cleanup
   */
  function destroy() {
    mounted = false;
    abortController?.abort();
    abortController = null;
    store.set(initialState);
  }

  // Auto-fetch on creation if params are valid
  if (contentId) {
    fetchEndpoints();
  }

  return {
    subscribe: store.subscribe,
    refetch,
    destroy,
  };
}

// Convenience derived stores
export function createDerivedEndpoints(store: ViewerEndpointsStore) {
  return derived(store, ($state) => $state.endpoints);
}

export function createDerivedPrimaryEndpoint(store: ViewerEndpointsStore) {
  return derived(store, ($state) => $state.endpoints?.primary ?? null);
}

export function createDerivedMetadata(store: ViewerEndpointsStore) {
  return derived(store, ($state) => $state.endpoints?.metadata ?? null);
}

export function createDerivedStatus(store: ViewerEndpointsStore) {
  return derived(store, ($state) => $state.status);
}

export default createEndpointResolver;

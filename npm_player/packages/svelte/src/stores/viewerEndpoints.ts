/**
 * Svelte store for Gateway GraphQL endpoint resolution with retry logic.
 *
 * Port of useViewerEndpoints.ts React hook to Svelte 5 stores.
 */

import { writable, derived, type Readable } from "svelte/store";
import type { ContentEndpoints, ContentType } from "@livepeer-frameworks/player-core";

export interface ViewerEndpointsOptions {
  gatewayUrl: string;
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

/**
 * Fetch with exponential backoff retry
 */
async function fetchWithRetry(
  url: string,
  options: RequestInit,
  maxRetries: number = MAX_RETRIES,
  initialDelay: number = INITIAL_DELAY_MS
): Promise<Response> {
  let lastError: Error | null = null;

  for (let attempt = 0; attempt < maxRetries; attempt++) {
    try {
      const response = await fetch(url, options);
      return response;
    } catch (e) {
      lastError = e instanceof Error ? e : new Error("Fetch failed");

      // Don't retry on abort
      if (options.signal?.aborted) {
        throw lastError;
      }

      // Wait before retrying (exponential backoff)
      if (attempt < maxRetries - 1) {
        const delay = initialDelay * Math.pow(2, attempt);
        console.warn(`[viewerEndpoints] Retry ${attempt + 1}/${maxRetries - 1} after ${delay}ms`);
        await new Promise((resolve) => setTimeout(resolve, delay));
      }
    }
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
 *     gatewayUrl: 'https://gateway.example.com/graphql',
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
  const { gatewayUrl, contentType, contentId, authToken } = options;

  const store = writable<ViewerEndpointsState>(initialState);
  let abortController: AbortController | null = null;
  let mounted = true;

  /**
   * Fetch endpoints from Gateway
   */
  async function fetchEndpoints() {
    if (!gatewayUrl || !contentId || !mounted) return;

    // Abort previous request
    abortController?.abort();
    abortController = new AbortController();

    store.update((s) => ({ ...s, status: "loading", error: null }));

    try {
      const graphqlEndpoint = gatewayUrl.replace(/\/$/, "");
      const query = `
        query ResolveViewer($contentId: String!) {
          resolveViewerEndpoint(contentId: $contentId) {
            primary { nodeId baseUrl protocol url geoDistance loadScore outputs }
            fallbacks { nodeId baseUrl protocol url geoDistance loadScore outputs }
            metadata { contentType contentId title description durationSeconds status isLive viewers recordingSizeBytes clipSource createdAt }
          }
        }
      `;

      const res = await fetchWithRetry(graphqlEndpoint, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          ...(authToken ? { Authorization: `Bearer ${authToken}` } : {}),
        },
        body: JSON.stringify({ query, variables: { contentId } }),
        signal: abortController.signal,
      });

      if (!res.ok) throw new Error(`Gateway GQL error ${res.status}`);

      const payload = await res.json();
      if (payload.errors?.length) {
        throw new Error(payload.errors[0]?.message || "GraphQL error");
      }

      const resp = payload.data?.resolveViewerEndpoint;
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
  if (gatewayUrl && contentType && contentId) {
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

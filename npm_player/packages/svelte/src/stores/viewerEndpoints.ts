import { writable, derived, type Readable } from "svelte/store";
import {
  GatewayClient,
  type ContentEndpoints,
  type ContentType,
  type PlaybackAuth,
  type ViewerProtocol,
} from "@livepeer-frameworks/player-core";

export interface ViewerEndpointsOptions {
  gatewayUrl?: string;
  contentId: string;
  contentType?: ContentType;
  authToken?: string;
  playbackAuth?: PlaybackAuth;
  /** Required format for placement; never retried as an unqualified query. */
  protocol?: ViewerProtocol;
}

export type EndpointStatus = "idle" | "loading" | "ready" | "error";

export interface ViewerEndpointsState {
  endpoints: ContentEndpoints | null;
  status: EndpointStatus;
  error: string | null;
}

export interface ViewerEndpointsStore extends Readable<ViewerEndpointsState> {
  refetch: () => void;
  /** Replace request fields, cancel old work, and resolve the new destination. */
  update: (options: Partial<ViewerEndpointsOptions>) => void;
  destroy: () => void;
}

const initialState: ViewerEndpointsState = { endpoints: null, status: "idle", error: null };

export function createEndpointResolver(options: ViewerEndpointsOptions): ViewerEndpointsStore {
  let config = { ...options };
  const client = new GatewayClient(config);
  const store = writable<ViewerEndpointsState>(initialState);
  let generation = 0;
  let destroyed = false;

  async function resolve() {
    if (destroyed) return;
    const requestGeneration = ++generation;
    // Refetch replaces pending work as well as cached destinations.
    client.updateConfig(config);
    if (!config.contentId) {
      store.set(initialState);
      return;
    }
    store.set({ endpoints: null, status: "loading", error: null });
    try {
      const endpoints = await client.resolve(true);
      if (!destroyed && generation === requestGeneration) {
        store.set({ endpoints, status: "ready", error: null });
      }
    } catch (error) {
      if (!destroyed && generation === requestGeneration) {
        store.set({
          endpoints: null,
          status: "error",
          error: error instanceof Error ? error.message : "Gateway resolution failed",
        });
      }
    }
  }

  void resolve();
  return {
    subscribe: store.subscribe,
    refetch: () => {
      void resolve();
    },
    update: (changes) => {
      if (destroyed) return;
      config = { ...config, ...changes };
      void resolve();
    },
    destroy: () => {
      if (destroyed) return;
      destroyed = true;
      generation++;
      client.destroy();
      store.set(initialState);
    },
  };
}

export function createDerivedEndpoints(store: ViewerEndpointsStore) {
  return derived(store, (state) => state.endpoints);
}

export function createDerivedPrimaryEndpoint(store: ViewerEndpointsStore) {
  return derived(store, (state) => state.endpoints?.primary ?? null);
}

export function createDerivedMetadata(store: ViewerEndpointsStore) {
  return derived(store, (state) => state.endpoints?.metadata ?? null);
}

export function createDerivedStatus(store: ViewerEndpointsStore) {
  return derived(store, (state) => state.status);
}

export default createEndpointResolver;

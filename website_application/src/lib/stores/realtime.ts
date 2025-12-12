import { writable, derived } from "svelte/store";
import {
  StreamEventsStore,
  ViewerMetricsStreamStore,
  SystemHealthStore,
  TrackListUpdatesStore,
  ClipLifecycleStore,
  DvrLifecycleStore,
} from "$houdini";
import type {
  StreamEvents$result,
  ViewerMetricsStream$result,
  SystemHealth$result,
  TrackListUpdates$result,
  ClipLifecycle$result,
  DvrLifecycle$result,
} from "$houdini";
import { browser } from "$app/environment";

// Type aliases for subscription results
type StreamEventData = NonNullable<StreamEvents$result["streamEvents"]>;
type ViewerMetricData = NonNullable<
  ViewerMetricsStream$result["viewerMetrics"]
>;
type SystemHealthData = NonNullable<SystemHealth$result["systemHealth"]>;
type TrackListEventData = NonNullable<
  TrackListUpdates$result["trackListUpdates"]
>;
type ClipLifecycleEventData = NonNullable<
  ClipLifecycle$result["clipLifecycle"]
>;
type DvrLifecycleEventData = NonNullable<DvrLifecycle$result["dvrLifecycle"]>;

// Real-time per-client connection event from ViewerMetrics subscription
interface StreamMetric {
  // From StreamEvents subscription
  status?: string;
  lastUpdate?: Date;

  // From ViewerMetrics subscription (per-client events)
  action?: string; // "connect" or "disconnect"
  nodeId?: string;
  protocol?: string;
  sessionId?: string;
  bandwidthInBps?: number; // Per-client upload bandwidth
  bandwidthOutBps?: number; // Per-client download bandwidth
  timestamp?: number;

  // Client-side aggregated counts (maintained by tracking connect/disconnect)
  activeConnections?: number;
}

interface NodeMetric {
  node: string;
  location: string;
  status: string;
  timestamp: Date;
  cpuTenths?: number;
  isHealthy?: boolean;
  ramMax?: number;
  ramCurrent?: number;
  diskTotalBytes?: number;
  diskUsedBytes?: number;
}

interface ConnectionStatus {
  status: "connected" | "disconnected" | "reconnecting" | "error";
  message: string;
}

// Stream type for realtimeStreams store
interface StreamData {
  id: string;
  name: string;
  metrics?: {
    isLive?: boolean;
  } | null;
}

// Connection state
export const wsConnected = writable<boolean>(false);
export const wsReconnecting = writable<boolean>(false);
export const wsError = writable<string>("");

// Real-time data stores
export const realtimeStreams = writable<StreamData[]>([]);
export const realtimeEvents = writable<StreamEventData[]>([]);
export const streamMetrics = writable<Record<string, StreamMetric>>({});
export const nodeMetrics = writable<Record<string, NodeMetric>>({});

// Derived: Total active connections across all streams (from WebSocket events)
export const realtimeViewers = derived(streamMetrics, ($metrics) => {
  return Object.values($metrics).reduce(
    (total, stream) => total + (stream.activeConnections || 0),
    0,
  );
});

// Track list updates per stream
export const trackListUpdates = writable<Record<string, TrackListEventData>>(
  {},
);

// Clip/DVR lifecycle events
export const clipLifecycleEvents = writable<ClipLifecycleEventData[]>([]);
export const dvrLifecycleEvents = writable<DvrLifecycleEventData[]>([]);

// Active Houdini subscription stores
let streamEventsStore: StreamEventsStore | null = null;
let viewerMetricsStores: Record<string, ViewerMetricsStreamStore> = {};
let systemHealthStore: SystemHealthStore | null = null;
let trackListStores: Record<string, TrackListUpdatesStore> = {};
let clipLifecycleStores: Record<string, ClipLifecycleStore> = {};
let dvrLifecycleStores: Record<string, DvrLifecycleStore> = {};

// Effect cleanup functions
let cleanupFunctions: Array<() => void> = [];

export function initializeWebSocket(): void {
  if (!browser) return;

  disconnectWebSocket();

  console.log("Initializing Houdini GraphQL subscriptions");
  wsConnected.set(true);
  wsReconnecting.set(false);
  wsError.set("");

  try {
    // Create and listen to StreamEvents subscription
    streamEventsStore = new StreamEventsStore();
    streamEventsStore.listen();

    // Set up effect to handle incoming events
    const unsubscribe = streamEventsStore.subscribe((result) => {
      // Handle subscription errors gracefully
      if (result.errors?.length) {
        console.warn("[StreamEvents] Subscription error:", result.errors);
        return;
      }

      if (result.data?.streamEvents) {
        const event = result.data.streamEvents;

        streamMetrics.update((metrics) => ({
          ...metrics,
          [event.stream]: {
            ...metrics[event.stream],
            status: event.status ?? undefined,
            lastUpdate: new Date(),
          },
        }));

        // Update realtimeStreams store with latest stream status
        realtimeStreams.update((currentStreams) => {
          const existingStreamIndex = currentStreams.findIndex(
            (s) => s.id === event.stream,
          );
          if (existingStreamIndex !== -1) {
            // Update existing stream
            const updatedStreams = [...currentStreams];
            updatedStreams[existingStreamIndex] = {
              ...updatedStreams[existingStreamIndex],
              metrics: { isLive: event.status === "LIVE" },
            };
            return updatedStreams;
          } else {
            // Add new stream if not found (or if it's a new "LIVE" event)
            return [
              ...currentStreams,
              {
                id: event.stream,
                name: event.stream,
                metrics: { isLive: event.status === "LIVE" },
              },
            ];
          }
        });

        realtimeEvents.update((events) => {
          const newEvents = [event, ...events.slice(0, 99)];
          return newEvents;
        });
      }
    });

    cleanupFunctions.push(unsubscribe);
  } catch (error) {
    console.error("Failed to initialize GraphQL subscriptions:", error);
    wsError.set("Failed to initialize real-time connections");
    wsConnected.set(false);
  }
}

export function subscribeToStreamMetrics(streamId: string): () => void {
  if (!browser || !streamId) return () => {};

  // Unlisten from existing subscription for this stream
  if (viewerMetricsStores[streamId]) {
    viewerMetricsStores[streamId].unlisten();
  }

  try {
    const store = new ViewerMetricsStreamStore();
    viewerMetricsStores[streamId] = store;
    store.listen({ stream: streamId });

    const unsubscribe = store.subscribe((result) => {
      if (result.errors?.length) {
        console.warn(
          `[ViewerMetrics:${streamId}] Subscription error:`,
          result.errors,
        );
        return;
      }

      if (result.data?.viewerMetrics) {
        const metrics = result.data.viewerMetrics;
        const streamName = metrics.internalName;

        streamMetrics.update((currentMetrics) => {
          const existing = currentMetrics[streamName] || {
            activeConnections: 0,
          };
          let activeConnections = existing.activeConnections || 0;

          if (metrics.action === "connect") {
            activeConnections++;
          } else if (metrics.action === "disconnect") {
            activeConnections = Math.max(0, activeConnections - 1);
          }

          return {
            ...currentMetrics,
            [streamName]: {
              ...existing,
              action: metrics.action ?? undefined,
              nodeId: metrics.nodeId ?? undefined,
              protocol: metrics.protocol ?? undefined,
              bandwidthInBps: metrics.bandwidthInBps ?? undefined,
              bandwidthOutBps: metrics.bandwidthOutBps ?? undefined,
              timestamp: metrics.timestamp ?? undefined,
              lastUpdate: new Date(),
              activeConnections,
            },
          };
        });
      }
    });

    return () => {
      unsubscribe();
      if (viewerMetricsStores[streamId]) {
        viewerMetricsStores[streamId].unlisten();
        delete viewerMetricsStores[streamId];
      }
    };
  } catch (error) {
    console.error(
      `Failed to subscribe to stream metrics for ${streamId}:`,
      error,
    );
    return () => {};
  }
}

export function subscribeToSystemHealth(): () => void {
  if (!browser) return () => {};

  if (systemHealthStore) {
    systemHealthStore.unlisten();
  }

  try {
    systemHealthStore = new SystemHealthStore();
    systemHealthStore.listen();

    const unsubscribe = systemHealthStore.subscribe((result) => {
      if (result.errors?.length) {
        console.warn("[SystemHealth] Subscription error:", result.errors);
        return;
      }

      if (result.data?.systemHealth) {
        const health = result.data.systemHealth;

        nodeMetrics.update((metrics) => ({
          ...metrics,
          [health.node]: {
            node: health.node,
            location: health.location,
            status: health.status,
            timestamp: new Date(health.timestamp),
            cpuTenths: health.cpuTenths ?? undefined,
            isHealthy: health.isHealthy ?? undefined,
            ramMax: health.ramMax ?? undefined,
            ramCurrent: health.ramCurrent ?? undefined,
            diskTotalBytes: health.diskTotalBytes ?? undefined,
            diskUsedBytes: health.diskUsedBytes ?? undefined,
          },
        }));
      }
    });

    return () => {
      unsubscribe();
      if (systemHealthStore) {
        systemHealthStore.unlisten();
        systemHealthStore = null;
      }
    };
  } catch (error) {
    console.error("Failed to subscribe to system health:", error);
    return () => {};
  }
}

export function subscribeToTrackListUpdates(streamId: string): () => void {
  if (!browser || !streamId) return () => {};

  if (trackListStores[streamId]) {
    trackListStores[streamId].unlisten();
  }

  try {
    const store = new TrackListUpdatesStore();
    trackListStores[streamId] = store;
    store.listen({ stream: streamId });

    const unsubscribe = store.subscribe((result) => {
      if (result.errors?.length) {
        console.warn(
          `[TrackListUpdates:${streamId}] Subscription error:`,
          result.errors,
        );
        return;
      }

      if (result.data?.trackListUpdates) {
        const update = result.data.trackListUpdates;
        trackListUpdates.update((current) => ({
          ...current,
          [update.streamName]: update,
        }));
      }
    });

    return () => {
      unsubscribe();
      if (trackListStores[streamId]) {
        trackListStores[streamId].unlisten();
        delete trackListStores[streamId];
      }
    };
  } catch (error) {
    console.error(
      `Failed to subscribe to track list updates for ${streamId}:`,
      error,
    );
    return () => {};
  }
}

export function subscribeToClipLifecycle(streamId: string): () => void {
  if (!browser || !streamId) return () => {};

  if (clipLifecycleStores[streamId]) {
    clipLifecycleStores[streamId].unlisten();
  }

  try {
    const store = new ClipLifecycleStore();
    clipLifecycleStores[streamId] = store;
    store.listen({ stream: streamId });

    const unsubscribe = store.subscribe((result) => {
      if (result.errors?.length) {
        console.warn(
          `[ClipLifecycle:${streamId}] Subscription error:`,
          result.errors,
        );
        return;
      }

      if (result.data?.clipLifecycle) {
        const event = result.data.clipLifecycle;
        clipLifecycleEvents.update((events) => {
          const existingIndex = events.findIndex(
            (e) => e.clipHash === event.clipHash,
          );
          if (existingIndex >= 0) {
            const updated = [...events];
            updated[existingIndex] = event;
            return updated;
          }
          return [event, ...events.slice(0, 99)];
        });
      }
    });

    return () => {
      unsubscribe();
      if (clipLifecycleStores[streamId]) {
        clipLifecycleStores[streamId].unlisten();
        delete clipLifecycleStores[streamId];
      }
    };
  } catch (error) {
    console.error(
      `Failed to subscribe to clip lifecycle for ${streamId}:`,
      error,
    );
    return () => {};
  }
}

export function subscribeToDvrLifecycle(streamId: string): () => void {
  if (!browser || !streamId) return () => {};

  if (dvrLifecycleStores[streamId]) {
    dvrLifecycleStores[streamId].unlisten();
  }

  try {
    const store = new DvrLifecycleStore();
    dvrLifecycleStores[streamId] = store;
    store.listen({ stream: streamId });

    const unsubscribe = store.subscribe((result) => {
      if (result.errors?.length) {
        console.warn(
          `[DvrLifecycle:${streamId}] Subscription error:`,
          result.errors,
        );
        return;
      }

      if (result.data?.dvrLifecycle) {
        const event = result.data.dvrLifecycle;
        dvrLifecycleEvents.update((events) => {
          const existingIndex = events.findIndex(
            (e) => e.dvrHash === event.dvrHash,
          );
          if (existingIndex >= 0) {
            const updated = [...events];
            updated[existingIndex] = event;
            return updated;
          }
          return [event, ...events.slice(0, 99)];
        });
      }
    });

    return () => {
      unsubscribe();
      if (dvrLifecycleStores[streamId]) {
        dvrLifecycleStores[streamId].unlisten();
        delete dvrLifecycleStores[streamId];
      }
    };
  } catch (error) {
    console.error(
      `Failed to subscribe to DVR lifecycle for ${streamId}:`,
      error,
    );
    return () => {};
  }
}

export function cleanupStaleMetrics(validStreamIds: string[]): void {
  const validSet = new Set(validStreamIds);

  streamMetrics.update((metrics) => {
    const cleaned: Record<string, StreamMetric> = {};
    for (const [streamId, metric] of Object.entries(metrics)) {
      if (validSet.has(streamId)) {
        cleaned[streamId] = metric;
      }
    }
    return cleaned;
  });

  trackListUpdates.update((updates) => {
    const cleaned: Record<string, TrackListEventData> = {};
    for (const [streamId, update] of Object.entries(updates)) {
      if (validSet.has(streamId)) {
        cleaned[streamId] = update;
      }
    }
    return cleaned;
  });
}

export function disconnectWebSocket(): void {
  // Clean up all subscriptions
  cleanupFunctions.forEach((fn) => fn());
  cleanupFunctions = [];

  if (streamEventsStore) {
    streamEventsStore.unlisten();
    streamEventsStore = null;
  }

  Object.values(viewerMetricsStores).forEach((store) => store.unlisten());
  viewerMetricsStores = {};

  Object.values(trackListStores).forEach((store) => store.unlisten());
  trackListStores = {};

  Object.values(clipLifecycleStores).forEach((store) => store.unlisten());
  clipLifecycleStores = {};

  Object.values(dvrLifecycleStores).forEach((store) => store.unlisten());
  dvrLifecycleStores = {};

  if (systemHealthStore) {
    systemHealthStore.unlisten();
    systemHealthStore = null;
  }

  wsConnected.set(false);
  wsReconnecting.set(false);
  wsError.set("");

  console.log("Houdini GraphQL subscriptions disconnected");
}

// Derived stores
export const connectionStatus = derived(
  [wsConnected, wsReconnecting, wsError],
  ([connected, reconnecting, error]): ConnectionStatus => {
    if (error) return { status: "error", message: error };
    if (connected) return { status: "connected", message: "Connected" };
    if (reconnecting)
      return { status: "reconnecting", message: "Reconnecting..." };
    return { status: "disconnected", message: "Disconnected" };
  },
);

export const liveStreamCount = derived(
  realtimeStreams,
  ($streams) => $streams.filter((s) => s.metrics?.isLive).length,
);

export const totalBandwidth = derived(streamMetrics, ($metrics) => {
  return Object.values($metrics).reduce((total, stream) => {
    return total + (stream.bandwidthOutBps || 0);
  }, 0);
});

// Auto-cleanup on page unload
if (browser) {
  window.addEventListener("beforeunload", () => {
    disconnectWebSocket();
  });
}

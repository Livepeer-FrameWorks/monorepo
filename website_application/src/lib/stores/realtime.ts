import { writable, derived } from "svelte/store";
import { client } from "$lib/graphql/client.js";
import {
  StreamEventsDocument,
  ViewerMetricsStreamDocument,
  SystemHealthDocument,
} from "$lib/graphql/generated/apollo-helpers";
import { browser } from "$app/environment";
import type { Subscription } from "@apollo/client";
import type { Stream, StreamEvent } from "$lib/graphql/generated/types";

interface StreamMetric {
  status?: string;
  lastUpdate?: Date;
  currentViewers?: number;
  peakViewers?: number;
  bandwidth?: number;
  connectionQuality?: number;
  bufferHealth?: number;
  timestamp?: string;
}

interface NodeMetric {
  nodeId: string;
  timestamp: Date;
  cpuUsage?: number;
  memoryUsage?: number;
  diskUsage?: number;
  networkTraffic?: number;
}

interface ConnectionStatus {
  status: "connected" | "disconnected" | "reconnecting" | "error";
  message: string;
}

// Connection state
export const wsConnected = writable<boolean>(false);
export const wsReconnecting = writable<boolean>(false);
export const wsError = writable<string>("");

// Real-time data stores
export const realtimeStreams = writable<Stream[]>([]);
export const realtimeViewers = writable<number>(0);
export const realtimeEvents = writable<StreamEvent[]>([]);
export const streamMetrics = writable<Record<string, StreamMetric>>({});
export const nodeMetrics = writable<Record<string, NodeMetric>>({});

// Active subscriptions
let globalSubscriptions: Subscription[] = [];
let streamSubscriptions: Record<string, Subscription> = {};
let systemSubscription: Subscription | null = null;

export function initializeWebSocket(token: string): void {
  if (!browser || !token) return;

  disconnectWebSocket();

  console.log("Initializing GraphQL subscriptions");
  wsConnected.set(true);
  wsReconnecting.set(false);
  wsError.set("");

  try {
    const streamEventsSubscription = client
      .subscribe({
        query: StreamEventsDocument,
        variables: {},
      })
      .subscribe({
        next: (result) => {
          if (result.data?.streamEvents) {
            const event = result.data.streamEvents;

            streamMetrics.update((metrics) => ({
              ...metrics,
              [event.streamId]: {
                ...metrics[event.streamId],
                status: event.status,
                lastUpdate: new Date(),
              },
            }));

            realtimeEvents.update((events) => {
              const newEvents = [event, ...events.slice(0, 99)];
              return newEvents;
            });
          }
        },
        error: (error) => {
          console.error("Stream events subscription error:", error);
          wsError.set("Stream events connection failed");
        },
      });

    globalSubscriptions = [streamEventsSubscription];
  } catch (error) {
    console.error("Failed to initialize GraphQL subscriptions:", error);
    wsError.set("Failed to initialize real-time connections");
    wsConnected.set(false);
  }
}

export function subscribeToStreamMetrics(streamId: string): () => void {
  if (!browser || !streamId) return () => {};

  if (streamSubscriptions[streamId]) {
    streamSubscriptions[streamId].unsubscribe();
  }

  try {
    const subscription = client
      .subscribe({
        query: ViewerMetricsStreamDocument,
        variables: { stream: streamId },
      })
      .subscribe({
        next: (result) => {
          if (result.data?.viewerMetrics) {
            const metrics = result.data.viewerMetrics;

            streamMetrics.update((currentMetrics) => ({
              ...currentMetrics,
              [metrics.streamId]: {
                ...currentMetrics[metrics.streamId],
                currentViewers: metrics.currentViewers,
                peakViewers: metrics.peakViewers,
                bandwidth: metrics.bandwidth,
                connectionQuality: metrics.connectionQuality,
                bufferHealth: metrics.bufferHealth,
                timestamp: metrics.timestamp,
              },
            }));

            realtimeViewers.set(metrics.currentViewers || 0);
          }
        },
        error: (error) => {
          console.error(
            `Viewer metrics subscription error for stream ${streamId}:`,
            error
          );
        },
      });

    streamSubscriptions[streamId] = subscription;

    return () => {
      if (streamSubscriptions[streamId]) {
        streamSubscriptions[streamId].unsubscribe();
        delete streamSubscriptions[streamId];
      }
    };
  } catch (error) {
    console.error(`Failed to subscribe to stream metrics for ${streamId}:`, error);
    return () => {};
  }
}

export function subscribeToSystemHealth(): () => void {
  if (!browser) return () => {};

  if (systemSubscription) {
    systemSubscription.unsubscribe();
  }

  try {
    systemSubscription = client
      .subscribe({
        query: SystemHealthDocument,
        variables: {},
      })
      .subscribe({
        next: (result) => {
          if (result.data?.systemHealth) {
            const health = result.data.systemHealth;

            nodeMetrics.update((metrics) => ({
              ...metrics,
              [health.nodeId]: {
                ...health,
                timestamp: new Date(health.timestamp),
              },
            }));
          }
        },
        error: (error) => {
          console.error("System health subscription error:", error);
        },
      });

    return () => {
      if (systemSubscription) {
        systemSubscription.unsubscribe();
        systemSubscription = null;
      }
    };
  } catch (error) {
    console.error("Failed to subscribe to system health:", error);
    return () => {};
  }
}

export function disconnectWebSocket(): void {
  globalSubscriptions.forEach((subscription) => {
    if (subscription) {
      subscription.unsubscribe();
    }
  });
  globalSubscriptions = [];

  Object.values(streamSubscriptions).forEach((subscription) => {
    if (subscription) {
      subscription.unsubscribe();
    }
  });
  streamSubscriptions = {};

  if (systemSubscription) {
    systemSubscription.unsubscribe();
    systemSubscription = null;
  }

  wsConnected.set(false);
  wsReconnecting.set(false);
  wsError.set("");

  console.log("GraphQL subscriptions disconnected");
}

// Derived stores
export const connectionStatus = derived(
  [wsConnected, wsReconnecting, wsError],
  ([connected, reconnecting, error]): ConnectionStatus => {
    if (error) return { status: "error", message: error };
    if (connected) return { status: "connected", message: "Connected" };
    if (reconnecting) return { status: "reconnecting", message: "Reconnecting..." };
    return { status: "disconnected", message: "Disconnected" };
  }
);

export const liveStreamCount = derived(realtimeStreams, ($streams) =>
  $streams.filter((s) => s.status === "live").length
);

export const totalBandwidth = derived(streamMetrics, ($metrics) => {
  return Object.values($metrics).reduce((total, stream) => {
    return total + (stream.bandwidth || 0);
  }, 0);
});

// Auto-cleanup on page unload
if (browser) {
  window.addEventListener("beforeunload", () => {
    disconnectWebSocket();
  });
}

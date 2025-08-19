import { writable, derived } from 'svelte/store';
import { client } from '$lib/graphql/client.js';
import { 
  StreamEventsDocument, 
  ViewerMetricsStreamDocument, 
  SystemHealthDocument 
} from '$lib/graphql/generated/apollo-helpers';
import { browser } from '$app/environment';

// Connection state
export const wsConnected = writable(false);
export const wsReconnecting = writable(false);
export const wsError = writable('');

// Real-time data stores
export const realtimeStreams = writable(/** @type {any[]} */([]));
export const realtimeViewers = writable(0);
export const realtimeEvents = writable(/** @type {any[]} */([]));
export const streamMetrics = writable(/** @type {Record<string, any>} */({}));
export const nodeMetrics = writable(/** @type {Record<string, any>} */({}));

// Active subscriptions
/** @type {any[]} */
let globalSubscriptions = [];
/** @type {Record<string, any>} */
let streamSubscriptions = {};
/** @type {any} */
let systemSubscription = null;

/**
 * Initialize basic GraphQL subscriptions (stream events only)
 * @param {string} token - JWT token for authentication
 */
export function initializeWebSocket(token) {
  if (!browser || !token) return;
  
  // Clean up existing subscriptions
  disconnectWebSocket();
  
  console.log('✅ Initializing GraphQL subscriptions');
  wsConnected.set(true);
  wsReconnecting.set(false);
  wsError.set('');

  try {
    // Subscribe to general stream events (no specific streamId required)
    const streamEventsSubscription = client.subscribe({
      query: StreamEventsDocument,
      variables: {} // Optional parameters
    }).subscribe({
      next: (result) => {
        if (result.data?.streamEvents) {
          const event = result.data.streamEvents;
          
          // Update stream metrics
          streamMetrics.update(metrics => ({
            ...metrics,
            [event.streamId]: {
              ...metrics[event.streamId],
              status: event.status,
              lastUpdate: new Date()
            }
          }));
          
          // Add to events list
          realtimeEvents.update(events => {
            const newEvents = [event, ...events.slice(0, 99)]; // Keep last 100 events
            return newEvents;
          });
        }
      },
      error: (error) => {
        console.error('❌ Stream events subscription error:', error);
        wsError.set('Stream events connection failed');
      }
    });

    // Store global subscriptions for cleanup
    globalSubscriptions = [streamEventsSubscription];

  } catch (error) {
    console.error('❌ Failed to initialize GraphQL subscriptions:', error);
    wsError.set('Failed to initialize real-time connections');
    wsConnected.set(false);
  }
}

/**
 * Subscribe to viewer metrics for a specific stream
 * @param {string} streamId - The stream ID to monitor
 * @returns {Function} Unsubscribe function
 */
export function subscribeToStreamMetrics(streamId) {
  if (!browser || !streamId) return () => {};

  // Clean up existing subscription for this stream
  if (streamSubscriptions[streamId]) {
    streamSubscriptions[streamId].unsubscribe();
  }

  try {
    const subscription = client.subscribe({
      query: ViewerMetricsStreamDocument,
      variables: { streamId }
    }).subscribe({
      next: (result) => {
        if (result.data?.viewerMetrics) {
          const metrics = result.data.viewerMetrics;
          
          // Update stream metrics
          streamMetrics.update(currentMetrics => ({
            ...currentMetrics,
            [metrics.streamId]: {
              ...currentMetrics[metrics.streamId],
              currentViewers: metrics.currentViewers,
              peakViewers: metrics.peakViewers,
              bandwidth: metrics.bandwidth,
              connectionQuality: metrics.connectionQuality,
              bufferHealth: metrics.bufferHealth,
              timestamp: metrics.timestamp
            }
          }));
          
          // Update total viewers (could aggregate from all streams)
          realtimeViewers.set(metrics.currentViewers || 0);
        }
      },
      error: (error) => {
        console.error(`❌ Viewer metrics subscription error for stream ${streamId}:`, error);
      }
    });

    streamSubscriptions[streamId] = subscription;

    // Return unsubscribe function
    return () => {
      if (streamSubscriptions[streamId]) {
        streamSubscriptions[streamId].unsubscribe();
        delete streamSubscriptions[streamId];
      }
    };
  } catch (error) {
    console.error(`❌ Failed to subscribe to stream metrics for ${streamId}:`, error);
    return () => {};
  }
}

/**
 * Subscribe to system health updates (for admin users)
 * @returns {Function} Unsubscribe function
 */
export function subscribeToSystemHealth() {
  if (!browser) return () => {};

  // Clean up existing system subscription
  if (systemSubscription) {
    systemSubscription.unsubscribe();
  }

  try {
    systemSubscription = client.subscribe({
      query: SystemHealthDocument,
      variables: {}
    }).subscribe({
      next: (result) => {
        if (result.data?.systemHealth) {
          const health = result.data.systemHealth;
          
          // Update node metrics
          nodeMetrics.update(metrics => ({
            ...metrics,
            [health.nodeId]: {
              ...health,
              timestamp: new Date(health.timestamp)
            }
          }));
        }
      },
      error: (error) => {
        console.error('❌ System health subscription error:', error);
        // Don't set global error for system health as it might not be available for all users
      }
    });

    // Return unsubscribe function
    return () => {
      if (systemSubscription) {
        systemSubscription.unsubscribe();
        systemSubscription = null;
      }
    };
  } catch (error) {
    console.error('❌ Failed to subscribe to system health:', error);
    return () => {};
  }
}

/**
 * Disconnect all GraphQL subscriptions
 */
export function disconnectWebSocket() {
  // Unsubscribe from global subscriptions
  globalSubscriptions.forEach(subscription => {
    if (subscription) {
      subscription.unsubscribe();
    }
  });
  globalSubscriptions = [];
  
  // Unsubscribe from stream-specific subscriptions
  Object.values(streamSubscriptions).forEach(subscription => {
    if (subscription) {
      subscription.unsubscribe();
    }
  });
  streamSubscriptions = {};
  
  // Unsubscribe from system health
  if (systemSubscription) {
    systemSubscription.unsubscribe();
    systemSubscription = null;
  }
  
  wsConnected.set(false);
  wsReconnecting.set(false);
  wsError.set('');
  
  console.log('✅ GraphQL subscriptions disconnected');
}

// Derived stores for computed values
export const connectionStatus = derived(
  [wsConnected, wsReconnecting, wsError],
  ([connected, reconnecting, error]) => {
    if (error) return { status: 'error', message: error };
    if (connected) return { status: 'connected', message: 'Connected' };
    if (reconnecting) return { status: 'reconnecting', message: 'Reconnecting...' };
    return { status: 'disconnected', message: 'Disconnected' };
  }
);

export const liveStreamCount = derived(
  realtimeStreams,
  $streams => $streams.filter(s => s.status === 'live').length
);

export const totalBandwidth = derived(
  streamMetrics,
  $metrics => {
    return Object.values($metrics).reduce((total, stream) => {
      return total + (stream.bandwidth || 0);
    }, 0);
  }
);

// Auto-cleanup on page unload
if (browser) {
  window.addEventListener('beforeunload', () => {
    disconnectWebSocket();
  });
}
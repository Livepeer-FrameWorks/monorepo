import { writable, derived } from 'svelte/store';
import { SignalmanWebSocket } from '../api.js';
import { browser } from '$app/environment';

// Connection state
export const wsConnected = writable(false);
export const wsReconnecting = writable(false);
export const wsError = writable(null);

// Real-time data stores
export const realtimeStreams = writable([]);
export const realtimeViewers = writable(0);
export const realtimeEvents = writable([]);
export const streamMetrics = writable({});
export const nodeMetrics = writable({});

// WebSocket instance (null when not connected)
let wsInstance = null;

/**
 * Initialize WebSocket connection with auth token
 * @param {string} token - JWT token for authentication
 */
export function initializeWebSocket(token) {
  if (!browser || !token) return;
  
  // Close existing connection if any
  if (wsInstance) {
    wsInstance.disconnect();
  }
  
  wsInstance = new SignalmanWebSocket(token);
  
  // Connection events
  wsInstance.on('connected', () => {
    wsConnected.set(true);
    wsReconnecting.set(false);
    wsError.set(null);
    console.log('✅ Real-time connection established');
    
    // Subscribe to relevant channels
    wsInstance.send({
      action: 'subscribe',
      channels: ['streams', 'metrics', 'events']
    });
  });
  
  wsInstance.on('disconnected', () => {
    wsConnected.set(false);
    wsReconnecting.set(true);
  });
  
  wsInstance.on('error', (error) => {
    wsError.set(error.message || 'WebSocket connection failed');
    console.error('❌ WebSocket error:', error);
  });
  
  // Data event handlers
  wsInstance.on('stream-metrics', (data) => {
    streamMetrics.update(metrics => ({
      ...metrics,
      [data.stream_id]: data
    }));
  });
  
  wsInstance.on('node-metrics', (data) => {
    nodeMetrics.update(metrics => ({
      ...metrics,
      [data.node_id]: data
    }));
  });
  
  wsInstance.on('realtime-streams', (data) => {
    realtimeStreams.set(data.streams || []);
  });
  
  wsInstance.on('realtime-viewers', (data) => {
    realtimeViewers.set(data.total_viewers || 0);
  });
  
  wsInstance.on('stream-event', (data) => {
    realtimeEvents.update(events => {
      const newEvents = [data, ...events.slice(0, 99)]; // Keep last 100 events
      return newEvents;
    });
  });
  
  // Start connection
  wsInstance.connect();
}

/**
 * Disconnect WebSocket
 */
export function disconnectWebSocket() {
  if (wsInstance) {
    wsInstance.disconnect();
    wsInstance = null;
  }
  wsConnected.set(false);
  wsReconnecting.set(false);
  wsError.set(null);
}

/**
 * Send message through WebSocket
 * @param {object} message - Message to send
 */
export function sendMessage(message) {
  if (wsInstance) {
    wsInstance.send(message);
  }
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
      return total + (stream.bandwidth_in || 0) + (stream.bandwidth_out || 0);
    }, 0);
  }
);

// Auto-cleanup on page unload
if (browser) {
  window.addEventListener('beforeunload', () => {
    disconnectWebSocket();
  });
}
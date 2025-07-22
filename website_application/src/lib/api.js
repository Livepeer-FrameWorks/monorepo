import axios from 'axios';

// API configuration - Updated to match nginx routing
const API_URL = import.meta.env.VITE_API_URL || 'http://localhost:18090';
const ANALYTICS_API_URL = import.meta.env.VITE_ANALYTICS_API_URL || 'http://localhost:18090/periscope';
const BILLING_API_URL = import.meta.env.VITE_BILLING_API_URL || 'http://localhost:18090/api';
const REALTIME_WS_URL = import.meta.env.VITE_REALTIME_WS_URL || 'ws://localhost:18090/ws';

export { API_URL, ANALYTICS_API_URL, BILLING_API_URL, REALTIME_WS_URL };

export const api = axios.create({
  baseURL: `${API_URL}/api`,
  headers: {
    'Content-Type': 'application/json',
  },
});

export const analyticsAPI = axios.create({
  baseURL: ANALYTICS_API_URL,
  headers: {
    'Content-Type': 'application/json',
  },
});

// Billing API (Purser)
export const billingAPIClient = axios.create({
  baseURL: `${BILLING_API_URL}/billing`,
  headers: {
    'Content-Type': 'application/json',
  },
});


// Request interceptor to add auth token and tenant ID for all APIs
const addAuthInterceptor = (apiInstance) => {
  apiInstance.interceptors.request.use(
    (config) => {
      const token = localStorage.getItem('token');
      if (token) {
        config.headers.Authorization = `Bearer ${token}`;
      }

      // Don't add tenant_id for auth endpoints (login/register) since user hasn't logged in yet
      const isAuthEndpoint = config.url && (
        config.url.includes('/login') ||
        config.url.includes('/register') ||
        config.url.includes('/verify-email')
      );

      // Add tenant ID from user data if available (except for auth endpoints)
      if (!isAuthEndpoint) {
        const userData = localStorage.getItem('user');
        if (userData) {
          try {
            const user = JSON.parse(userData);
            if (user.tenant_id) {
              config.headers['X-Tenant-ID'] = user.tenant_id;
            }
          } catch (e) {
            console.warn('Failed to parse user data from localStorage:', e);
          }
        }
      }

      return config;
    },
    (error) => {
      return Promise.reject(error);
    }
  );
};

// Response interceptor to handle errors for both APIs
const addResponseInterceptor = (apiInstance) => {
  apiInstance.interceptors.response.use(
    (response) => response,
    (error) => {
      if (error.response?.status === 401) {
        // Only handle token expiry, not login failures
        const currentPath = typeof window !== 'undefined' ? window.location.pathname : '';
        const isAuthPage = currentPath.includes('/login') || currentPath.includes('/register');

        console.log('401 interceptor:', { currentPath, isAuthPage, url: error.config?.url });

        // If we're on auth pages, this is likely a login failure, not token expiry
        if (!isAuthPage) {
          // Token expired or invalid - redirect to login
          console.log('Redirecting to login due to token expiry');
          localStorage.removeItem('token');
          window.location.href = '/login/';
        } else {
          console.log('On auth page, not redirecting');
        }
        // If we ARE on auth pages, let the error bubble up normally
      }
      return Promise.reject(error);
    }
  );
};

// Apply interceptors to all API instances
addAuthInterceptor(api);
addAuthInterceptor(analyticsAPI);
addAuthInterceptor(billingAPIClient);
addResponseInterceptor(api);
addResponseInterceptor(analyticsAPI);
addResponseInterceptor(billingAPIClient);

// Stream API functions
export const streamAPI = {
  // Get all user streams
  getStreams: () => api.get('/streams'),

  // Get specific stream
  /** @param {string} streamId */
  getStream: (streamId) => api.get(`/streams/${streamId}`),

  // Create new stream
  /** @param {{ title: string, description?: string }} data */
  createStream: (data) => api.post('/streams', data),

  // Delete stream
  /** @param {string} streamId */
  deleteStream: (streamId) => api.delete(`/streams/${streamId}`),

  // Get embed code for stream
  /** @param {string} streamId */
  getStreamEmbed: (streamId) => api.get(`/streams/${streamId}/embed`),

  // Refresh stream key
  /** @param {string} streamId */
  refreshStreamKey: (streamId) => api.post(`/streams/${streamId}/refresh-key`),
};

// Auth API functions
export const authAPI = {
  /** @param {{ email: string, password: string }} data */
  login: (data) => api.post('/login', data),
  /** @param {{ email: string, password: string }} data */
  register: (data) => api.post('/register', data),
  getMe: () => api.get('/me'),
};

// Analytics API functions
export const analyticsAPIFunctions = {
  // Stream analytics endpoints
  getStreamAnalytics: () => analyticsAPI.get('/analytics/streams'),
  getStreamDetails: (streamId) => analyticsAPI.get(`/analytics/streams/${streamId}`),
  getStreamEvents: (streamId) => analyticsAPI.get(`/analytics/streams/${streamId}/events`),
  getStreamViewers: (streamId) => analyticsAPI.get(`/analytics/streams/${streamId}/viewers`),

  // Time-series analytics endpoints
  getViewerMetrics: () => analyticsAPI.get('/analytics/viewer-metrics'),
  getConnectionEvents: () => analyticsAPI.get('/analytics/connection-events'),
  getNodeMetrics: () => analyticsAPI.get('/analytics/node-metrics'),
  getRoutingEvents: () => analyticsAPI.get('/analytics/routing-events'),
  getStreamHealth: () => analyticsAPI.get('/analytics/stream-health'),

  // Aggregated analytics endpoints
  getViewerMetrics5m: () => analyticsAPI.get('/analytics/viewer-metrics/5m'),
  getNodeMetrics1h: () => analyticsAPI.get('/analytics/node-metrics/1h'),

  // Platform analytics endpoints
  getPlatformOverview: () => analyticsAPI.get('/analytics/platform/overview'),
  getPlatformMetrics: () => analyticsAPI.get('/analytics/platform/metrics'),
  getPlatformEvents: () => analyticsAPI.get('/analytics/platform/events'),

  // Realtime analytics endpoints
  getRealtimeStreams: () => analyticsAPI.get('/analytics/realtime/streams'),
  getRealtimeViewers: () => analyticsAPI.get('/analytics/realtime/viewers'),
  getRealtimeEvents: () => analyticsAPI.get('/analytics/realtime/events'),

  // Usage endpoints
  getUsageSummary: () => analyticsAPI.get('/usage/summary'),
  triggerHourlySummary: () => analyticsAPI.post('/usage/trigger-hourly'),
  triggerDailySummary: () => analyticsAPI.post('/usage/trigger-daily'),

  // Stream health metrics (includes codec info)
  getStreamHealthMetrics: (params = {}) => {
    const queryString = new URLSearchParams(params).toString();
    return analyticsAPI.get(`/analytics/stream-health${queryString ? '?' + queryString : ''}`);
  }
};

// Billing API functions (Purser)
export const billingAPI = {
  // Tier management
  getTiers: () => billingAPIClient.get('/tiers'),
  getTier: (tierId) => billingAPIClient.get(`/tiers/${tierId}`),

  // Subscription management  
  getBillingStatus: () => billingAPIClient.get('/status'),
  getSubscription: () => billingAPIClient.get('/subscription'),
  updateSubscription: (data) => billingAPIClient.put('/subscription', data),

  // Invoice management
  getInvoices: () => billingAPIClient.get('/invoices'),
  getInvoice: (invoiceId) => billingAPIClient.get(`/invoices/${invoiceId}`),

  // Payment management
  getPaymentMethods: () => billingAPIClient.get('/payment-methods'),
  createPayment: (invoiceId, method, currency) => billingAPIClient.post('/payments', {
    invoice_id: invoiceId,
    method,
    currency
  }),

  // Usage tracking
  getUsageRecords: (params = {}) => {
    const queryString = new URLSearchParams(params).toString();
    return billingAPIClient.get(`/usage/records${queryString ? '?' + queryString : ''}`);
  },

  // Webhooks (for payment confirmations)
  handlePaymentWebhook: (data) => billingAPIClient.post('/webhooks/payment', data),
};


// WebSocket connection for real-time updates
export class SignalmanWebSocket {
  constructor(token) {
    this.url = REALTIME_WS_URL;
    this.token = token;
    this.ws = null;
    this.reconnectAttempts = 0;
    this.maxReconnectAttempts = 5;
    this.reconnectInterval = 1000;
    this.listeners = new Map();
  }

  connect() {
    try {
      this.ws = new WebSocket(`${this.url}?token=${encodeURIComponent(this.token)}`);

      this.ws.onopen = () => {
        console.log('WebSocket connected');
        this.reconnectAttempts = 0;
        this.emit('connected');
      };

      this.ws.onmessage = (event) => {
        try {
          const data = JSON.parse(event.data);
          this.emit(data.type || 'message', data);
        } catch (e) {
          console.error('Failed to parse WebSocket message:', e);
        }
      };

      this.ws.onclose = () => {
        console.log('WebSocket disconnected');
        this.emit('disconnected');
        this.attemptReconnect();
      };

      this.ws.onerror = (error) => {
        console.error('WebSocket error:', error);
        this.emit('error', error);
      };
    } catch (error) {
      console.error('Failed to create WebSocket connection:', error);
      this.attemptReconnect();
    }
  }

  attemptReconnect() {
    if (this.reconnectAttempts < this.maxReconnectAttempts) {
      this.reconnectAttempts++;
      setTimeout(() => {
        console.log(`Reconnecting... (${this.reconnectAttempts}/${this.maxReconnectAttempts})`);
        this.connect();
      }, this.reconnectInterval * this.reconnectAttempts);
    }
  }

  on(event, callback) {
    if (!this.listeners.has(event)) {
      this.listeners.set(event, []);
    }
    this.listeners.get(event).push(callback);
  }

  off(event, callback) {
    if (this.listeners.has(event)) {
      const callbacks = this.listeners.get(event);
      const index = callbacks.indexOf(callback);
      if (index > -1) {
        callbacks.splice(index, 1);
      }
    }
  }

  emit(event, data) {
    if (this.listeners.has(event)) {
      this.listeners.get(event).forEach(callback => {
        try {
          callback(data);
        } catch (error) {
          console.error('Error in WebSocket event callback:', error);
        }
      });
    }
  }

  send(data) {
    if (this.ws && this.ws.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify(data));
    }
  }

  disconnect() {
    this.reconnectAttempts = this.maxReconnectAttempts;
    if (this.ws) {
      this.ws.close();
      this.ws = null;
    }
  }
} 
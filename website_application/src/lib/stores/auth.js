import { writable } from 'svelte/store';
import { api } from '$lib/api';
import { initializeWebSocket, disconnectWebSocket } from './realtime.js';

function createAuthStore() {
  const { subscribe, set, update } = writable({
    isAuthenticated: false,
    user: null,
    loading: true, // Start with loading true
    error: null,
    initialized: false // Track if we've done initial auth check
  });

  return {
    subscribe,

    /**
     * @param {string} email
     * @param {string} password
     */
    async login(email, password) {
      update(state => ({ ...state, loading: true, error: null }));

      try {
        const response = await api.post('/login', { email, password });
        const { token, user } = response.data;

        localStorage.setItem('token', token);
        api.defaults.headers.common['Authorization'] = `Bearer ${token}`;

        // After login, fetch full user data including streams
        const meResponse = await api.get('/me');

        // Store user data in localStorage for API client to access tenant_id
        localStorage.setItem('user', JSON.stringify(meResponse.data.user));

        set({
          isAuthenticated: true,
          user: meResponse.data, // This contains { user: {...}, streams: [...] }
          loading: false,
          error: null,
          initialized: true
        });

        // Initialize WebSocket for real-time updates
        initializeWebSocket(token);

        return { success: true };
      } catch (error) {
        const errorMessage = /** @type {any} */ (error).response?.data?.error || 'Login failed';
        update(state => ({ ...state, loading: false, error: errorMessage }));
        return { success: false, error: errorMessage };
      }
    },

    /**
     * @param {string} email
     * @param {string} password
     */
    async register(email, password, botProtectionData = {}) {
      update(state => ({ ...state, loading: true, error: null }));

      try {
        const response = await api.post('/register', { email, password, ...botProtectionData });
        const { token, user } = response.data;

        localStorage.setItem('token', token);
        api.defaults.headers.common['Authorization'] = `Bearer ${token}`;

        set({
          isAuthenticated: true,
          user,
          loading: false,
          error: null,
          initialized: true
        });

        // Initialize WebSocket for real-time updates
        initializeWebSocket(token);

        return { success: true };
      } catch (error) {
        const errorMessage = /** @type {any} */ (error).response?.data?.error || 'Registration failed';
        update(state => ({ ...state, loading: false, error: errorMessage }));
        return { success: false, error: errorMessage };
      }
    },

    /**
     * Update streams in the auth store
     * @param {any[]} streams
     */
    updateStreams(streams) {
      update(state => {
        if (!state.user) return state;
        
        return {
          ...state,
          user: {
            ...state.user,
            streams: streams
          }
        };
      });
    },

    async checkAuth() {
      // Don't check if already initialized and not loading
      const currentState = { isAuthenticated: false, user: null, loading: false, error: null, initialized: false };
      const unsubscribe = subscribe(state => {
        Object.assign(currentState, state);
      });
      unsubscribe();

      if (currentState.initialized && !currentState.loading) {
        return;
      }

      update(state => ({ ...state, loading: true }));

      const token = localStorage.getItem('token');
      if (!token) {
        set({ isAuthenticated: false, user: null, loading: false, error: null, initialized: true });
        return;
      }

      try {
        api.defaults.headers.common['Authorization'] = `Bearer ${token}`;
        const response = await api.get('/me');

        // Store user data in localStorage for API client to access tenant_id
        localStorage.setItem('user', JSON.stringify(response.data.user));

        set({
          isAuthenticated: true,
          user: response.data,
          loading: false,
          error: null,
          initialized: true
        });

        // Initialize WebSocket for real-time updates
        initializeWebSocket(token);
      } catch (error) {
        localStorage.removeItem('token');
        delete api.defaults.headers.common['Authorization'];
        set({ isAuthenticated: false, user: null, loading: false, error: null, initialized: true });
      }
    },

    logout() {
      localStorage.removeItem('token');
      localStorage.removeItem('user');
      delete api.defaults.headers.common['Authorization'];
      
      // Disconnect WebSocket
      disconnectWebSocket();
      
      set({ isAuthenticated: false, user: null, loading: false, error: null, initialized: true });
    }
  };
}

export const auth = createAuthStore(); 
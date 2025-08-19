import { writable } from 'svelte/store';
import { authAPI } from '$lib/authAPI.js';
import { authService } from '$lib/graphql/services/auth.js';
import { initializeWebSocket, disconnectWebSocket } from './realtime.js';

function createAuthStore() {
  const { subscribe, set, update } = writable({
    isAuthenticated: false,
    user: null,
    token: null,
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
        const response = await authAPI.post('/login', { email, password });
        const { token, user } = response.data;

        localStorage.setItem('token', token);
        authAPI.defaults.headers.common['Authorization'] = `Bearer ${token}`;

        // Store user data in localStorage for API client to access tenant_id
        localStorage.setItem('user', JSON.stringify(user));

        set({
          isAuthenticated: true,
          user: { user, streams: [] }, // Match expected structure with empty streams for now
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
        const response = await authAPI.post('/register', { email, password, ...botProtectionData });
        const { token, user } = response.data;

        localStorage.setItem('token', token);
        authAPI.defaults.headers.common['Authorization'] = `Bearer ${token}`;

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

      try {
        // Use the GraphQL auth service to check authentication
        const authResult = await authService.checkAuth();
        
        if (authResult.isAuthenticated && authResult.user) {
          set({
            isAuthenticated: true,
            user: { user: authResult.user, streams: [] }, // Match expected structure
            token: authResult.token,
            loading: false,
            error: null,
            initialized: true
          });

          // Initialize WebSocket for real-time updates
          if (authResult.token) {
            initializeWebSocket(authResult.token);
          }
        } else {
          set({ 
            isAuthenticated: false, 
            user: null, 
            token: null,
            loading: false, 
            error: null, 
            initialized: true 
          });
        }
      } catch (error) {
        console.error('Auth check failed:', error);
        set({ 
          isAuthenticated: false, 
          user: null, 
          token: null,
          loading: false, 
          error: 'Authentication check failed', 
          initialized: true 
        });
      }
    },

    logout() {
      localStorage.removeItem('token');
      localStorage.removeItem('user');
      delete authAPI.defaults.headers.common['Authorization'];
      
      // Disconnect WebSocket
      disconnectWebSocket();
      
      set({ isAuthenticated: false, user: null, loading: false, error: null, initialized: true });
    }
  };
}

export const auth = createAuthStore(); 
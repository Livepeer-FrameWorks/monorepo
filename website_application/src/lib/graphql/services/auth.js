import { authAPI } from '../../authAPI.js';
import { resetGraphQLClient, updateAuthToken } from '../client.js';

export const authService = {
  // REST-based auth operations (login/register happen before GraphQL token)
  async login(email, password) {
    try {
      const response = await authAPI.post('/login', { email, password });
      const { token, user } = response.data;
      
      // Update GraphQL client with new token
      updateAuthToken(token);
      
      // Store user data
      localStorage.setItem('user', JSON.stringify(user));
      
      return { token, user };
    } catch (error) {
      console.error('Login failed:', error);
      throw error;
    }
  },

  async register(email, password, name) {
    try {
      const response = await authAPI.post('/register', { email, password, name });
      const { token, user } = response.data;
      
      // Update GraphQL client with new token
      updateAuthToken(token);
      
      // Store user data
      localStorage.setItem('user', JSON.stringify(user));
      
      return { token, user };
    } catch (error) {
      console.error('Registration failed:', error);
      throw error;
    }
  },

  async logout() {
    // Clear all auth data and reset GraphQL client
    resetGraphQLClient();
    
    // Let the caller handle navigation
  },

  // Check if user is authenticated and get current user from localStorage
  async checkAuth() {
    const token = localStorage.getItem('token');
    const userData = localStorage.getItem('user');
    
    if (!token || !userData) {
      return { isAuthenticated: false, user: null };
    }

    try {
      const user = JSON.parse(userData);
      return { isAuthenticated: true, user, token };
    } catch (error) {
      // Invalid user data, clear it
      this.logout();
      return { isAuthenticated: false, user: null };
    }
  }
};
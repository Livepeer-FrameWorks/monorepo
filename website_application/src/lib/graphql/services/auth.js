import { client } from '../client.js';
import { authAPI, resetGraphQLClient, updateAuthToken } from '../../api.js';
import { GetMeDocument } from '../generated/apollo-helpers';

export const authService = {
  // REST-based auth operations (login/register happen before GraphQL token)
  async login(email, password) {
    try {
      const response = await authAPI.login({ email, password });
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
      const response = await authAPI.register({ email, password, name });
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

  // GraphQL-based user data operations (after authentication)
  async getMe() {
    try {
      const result = await client.query({
        query: GetMeDocument,
        fetchPolicy: 'network-only' // Always get fresh user data
      });
      return result.data.me;
    } catch (error) {
      console.error('Failed to get user data:', error);
      throw error;
    }
  },

  async logout() {
    // Clear all auth data and reset GraphQL client
    resetGraphQLClient();
    
    // Let the caller handle navigation
  },

  // Check if user is authenticated and get current user
  async checkAuth() {
    const token = localStorage.getItem('token');
    if (!token) {
      return { isAuthenticated: false, user: null };
    }

    try {
      const user = await this.getMe();
      return { isAuthenticated: true, user, token };
    } catch (error) {
      // Token is invalid, clear it
      this.logout();
      return { isAuthenticated: false, user: null };
    }
  }
};
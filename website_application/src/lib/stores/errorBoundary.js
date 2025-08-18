import { writable } from 'svelte/store';

/**
 * @typedef {Object} ErrorBoundaryState
 * @property {boolean} hasError - Whether there's currently an error
 * @property {Error | null} error - The current error object
 * @property {string | null} errorMessage - User-friendly error message
 * @property {string | null} errorInfo - Additional error context
 */

function createErrorBoundaryStore() {
  /** @type {import('svelte/store').Writable<ErrorBoundaryState>} */
  const { subscribe, update, set } = writable({
    hasError: false,
    error: null,
    errorMessage: null,
    errorInfo: null
  });

  return {
    subscribe,
    
    /**
     * Set an error state
     * @param {Error} error - The error object
     * @param {string} [userMessage] - User-friendly error message
     * @param {string} [additionalInfo] - Additional context about the error
     */
    setError(error, userMessage, additionalInfo) {
      console.error('ErrorBoundary caught error:', error);
      
      set({
        hasError: true,
        error,
        errorMessage: userMessage || 'An unexpected error occurred',
        errorInfo: additionalInfo || error.message
      });
    },
    
    /**
     * Clear the error state
     */
    clearError() {
      set({
        hasError: false,
        error: null,
        errorMessage: null,
        errorInfo: null
      });
    },
    
    /**
     * Handle GraphQL errors specifically
     * @param {any} graphQLError - GraphQL error object
     */
    handleGraphQLError(graphQLError) {
      let userMessage = 'Failed to load data';
      let additionalInfo = '';
      
      if (graphQLError.networkError) {
        if (graphQLError.networkError.statusCode === 401) {
          userMessage = 'Authentication required. Please log in again.';
        } else if (graphQLError.networkError.statusCode >= 500) {
          userMessage = 'Server error. Please try again later.';
        } else {
          userMessage = 'Network error. Please check your connection.';
        }
      } else if (graphQLError.graphQLErrors && graphQLError.graphQLErrors.length > 0) {
        const firstError = graphQLError.graphQLErrors[0];
        if (firstError.extensions?.code === 'FORBIDDEN') {
          userMessage = 'You do not have permission to access this resource.';
        } else if (firstError.extensions?.code === 'UNAUTHENTICATED') {
          userMessage = 'Please log in to continue.';
        } else {
          userMessage = firstError.message || 'GraphQL request failed';
        }
      }
      
      this.setError(new Error(userMessage), userMessage, additionalInfo);
    }
  };
}

export const errorBoundary = createErrorBoundaryStore();
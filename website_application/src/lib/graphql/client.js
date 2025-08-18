import { ApolloClient, InMemoryCache, createHttpLink, from, split } from '@apollo/client';
import { setContext } from '@apollo/client/link/context';
import { onError } from '@apollo/client/link/error';
import { getMainDefinition } from '@apollo/client/utilities';
import { GraphQLWsLink } from '@apollo/client/link/subscriptions';
import { createClient } from 'graphql-ws';
import { errorBoundary } from '$lib/stores/errorBoundary.js';

// Configuration
const GRAPHQL_HTTP_URL = import.meta.env.VITE_GRAPHQL_HTTP_URL || 'http://localhost:18000/graphql/';
const GRAPHQL_WS_URL = import.meta.env.VITE_GRAPHQL_WS_URL || 'ws://localhost:18000/graphql/';

// HTTP Link for queries and mutations
const httpLink = createHttpLink({
  uri: GRAPHQL_HTTP_URL,
});

// WebSocket Link for subscriptions (only in browser)
const wsLink = typeof window !== 'undefined' 
  ? new GraphQLWsLink(
      createClient({
        url: GRAPHQL_WS_URL,
        connectionParams: () => {
          const token = localStorage.getItem('token');
          /** @type {Record<string, string>} */
          const connectionParams = {
            Authorization: token ? `Bearer ${token}` : '',
          };

          // Add tenant ID from user data if available
          const userData = localStorage.getItem('user');
          if (userData) {
            try {
              const user = JSON.parse(userData);
              if (user.tenant_id) {
                connectionParams['X-Tenant-ID'] = user.tenant_id;
              }
            } catch (e) {
              console.warn('Failed to parse user data from localStorage:', e);
            }
          }

          return connectionParams;
        },
        retryAttempts: 5,
        shouldRetry: () => true,
      })
    )
  : null;

// Auth Link - adds JWT token and tenant ID to requests
const authLink = setContext((_, { headers }) => {
  const token = typeof window !== 'undefined' ? localStorage.getItem('token') : null;
  
  const authHeaders = {
    ...headers,
    authorization: token ? `Bearer ${token}` : '',
  };

  // Add tenant ID from user data if available
  if (typeof window !== 'undefined') {
    const userData = localStorage.getItem('user');
    if (userData) {
      try {
        const user = JSON.parse(userData);
        if (user.tenant_id) {
          authHeaders['X-Tenant-ID'] = user.tenant_id;
        }
      } catch (e) {
        console.warn('Failed to parse user data from localStorage:', e);
      }
    }
  }
  
  return {
    headers: authHeaders,
  };
});

// Error Link - handles authentication errors
const errorLink = onError(({ graphQLErrors, networkError }) => {
  // Handle GraphQL errors
  if (graphQLErrors) {
    graphQLErrors.forEach(({ message, locations, path, extensions }) => {
      console.error(`GraphQL error: Message: ${message}, Location: ${locations}, Path: ${path}`);
      
      // Handle authentication errors
      if (extensions?.code === 'UNAUTHENTICATED' || message.includes('authentication')) {
        // Clear auth data - let layout handle redirect
        localStorage.removeItem('token');
        localStorage.removeItem('user');
      }
      
      // Handle other GraphQL errors with error boundary
      if (extensions?.code === 'INTERNAL_SERVER_ERROR' || extensions?.code === 'GRAPHQL_VALIDATION_FAILED') {
        errorBoundary.handleGraphQLError({ graphQLErrors: [{ message, extensions }] });
      }
    });
  }
  
  // Handle network errors
  if (networkError) {
    console.error(`Network error: ${networkError}`);
    
    // Handle network authentication errors
    if (networkError && 'statusCode' in networkError && networkError.statusCode === 401) {
      // Clear auth data - let layout handle redirect
      localStorage.removeItem('token');
      localStorage.removeItem('user');
    }
    
    // Handle other network errors with error boundary for severe issues
    if (networkError && 'statusCode' in networkError && networkError.statusCode >= 500) {
      errorBoundary.handleGraphQLError({ networkError });
    }
  }
});

// Split link - route subscriptions to WebSocket (if available), everything else to HTTP
const splitLink = typeof window !== 'undefined' && wsLink
  ? split(
      ({ query }) => {
        const definition = getMainDefinition(query);
        return (
          definition.kind === 'OperationDefinition' &&
          definition.operation === 'subscription'
        );
      },
      wsLink,
      from([errorLink, authLink, httpLink])
    )
  : from([errorLink, authLink, httpLink]);

// Apollo Client
export const client = new ApolloClient({
  link: splitLink,
  cache: new InMemoryCache({
    typePolicies: {
      Query: {
        fields: {
          streams: {
            merge: true,
          },
          viewerMetrics: {
            merge: false, // Always replace for real-time data
          },
        },
      },
    },
  }),
  defaultOptions: {
    watchQuery: {
      errorPolicy: 'all',
    },
    query: {
      errorPolicy: 'all',
    },
  },
});

// Helper function to update token in WebSocket connection
/**
 * @param {string} token 
 */
export function updateAuthToken(token) {
  // Store token for future connections
  if (token) {
    localStorage.setItem('token', token);
  } else {
    localStorage.removeItem('token');
  }
  
  // Note: WebSocket connection will automatically use new token on next connection
  // due to connectionParams being a function
}

// Helper function to clear cache and reset authentication
export function resetGraphQLClient() {
  client.clearStore();
  localStorage.removeItem('token');
  localStorage.removeItem('user');
}
import { client } from '$lib/graphql/client.js';
import { gql } from '@apollo/client';

// Define queries since codegen might not have run yet
const INTROSPECT_SCHEMA = gql`
  query IntrospectSchema {
    __schema {
      queryType {
        name
        fields {
          name
          description
          type {
            ...TypeRef
          }
          args {
            name
            description
            type {
              ...TypeRef
            }
            defaultValue
          }
        }
      }
      mutationType {
        name
        fields {
          name
          description
          type {
            ...TypeRef
          }
          args {
            name
            description
            type {
              ...TypeRef
            }
            defaultValue
          }
        }
      }
      subscriptionType {
        name
        fields {
          name
          description
          type {
            ...TypeRef
          }
          args {
            name
            description
            type {
              ...TypeRef
            }
            defaultValue
          }
        }
      }
      types {
        ...FullType
      }
    }
  }

  fragment FullType on __Type {
    kind
    name
    description
    fields(includeDeprecated: true) {
      name
      description
      args {
        ...InputValue
      }
      type {
        ...TypeRef
      }
      isDeprecated
      deprecationReason
    }
    inputFields {
      ...InputValue
    }
    interfaces {
      ...TypeRef
    }
    enumValues(includeDeprecated: true) {
      name
      description
      isDeprecated
      deprecationReason
    }
    possibleTypes {
      ...TypeRef
    }
  }

  fragment InputValue on __InputValue {
    name
    description
    type { ...TypeRef }
    defaultValue
  }

  fragment TypeRef on __Type {
    kind
    name
    ofType {
      kind
      name
      ofType {
        kind
        name
        ofType {
          kind
          name
          ofType {
            kind
            name
            ofType {
              kind
              name
              ofType {
                kind
                name
                ofType {
                  kind
                  name
                }
              }
            }
          }
        }
      }
    }
  }
`;

const GET_ROOT_TYPES = gql`
  query GetRootTypes {
    __schema {
      queryType {
        name
        fields {
          name
          description
        }
      }
      mutationType {
        name
        fields {
          name
          description
        }
      }
      subscriptionType {
        name
        fields {
          name
          description
        }
      }
    }
  }
`;

/**
 * GraphQL Explorer Service
 * Handles schema introspection and query execution for the custom explorer
 */
export const explorerService = {
  /**
   * Get the full GraphQL schema via introspection
   */
  async getSchema() {
    try {
      const { data } = await client.query({
        query: INTROSPECT_SCHEMA,
        fetchPolicy: 'cache-first',
      });
      return data.__schema;
    } catch (error) {
      console.error('Failed to introspect schema:', error);
      throw error;
    }
  },

  /**
   * Get just the root types for quick overview
   */
  async getRootTypes() {
    try {
      const { data } = await client.query({
        query: GET_ROOT_TYPES,
        fetchPolicy: 'cache-first',
      });
      return data.__schema;
    } catch (error) {
      console.error('Failed to get root types:', error);
      throw error;
    }
  },

  /**
   * Execute a GraphQL query with variables
   * @param {string} query - The GraphQL query string
   * @param {Object} variables - Variables for the query
   * @param {string} operationType - 'query', 'mutation', or 'subscription'
   * @param {boolean} demoMode - Enable demo mode for predictable responses
   */
  async executeQuery(query, variables = {}, operationType = 'query', demoMode = false) {
    try {
      const startTime = Date.now();
      
      // Configure client for demo mode
      const headers = demoMode ? { 'X-Demo-Mode': 'true' } : {};
      
      let result;
      if (operationType === 'mutation') {
        result = await client.mutate({
          mutation: gql(query),
          variables,
          context: {
            headers
          }
        });
      } else {
        result = await client.query({
          query: gql(query),
          variables,
          fetchPolicy: 'network-only', // Always fetch fresh data for explorer
          context: {
            headers
          }
        });
      }

      const endTime = Date.now();
      const duration = endTime - startTime;

      return {
        data: result.data,
        loading: result.loading,
        error: result.error,
        networkStatus: result.networkStatus,
        duration,
        timestamp: new Date().toISOString(),
        demoMode
      };
    } catch (error) {
      console.error('GraphQL query execution failed:', error);
      return {
        data: null,
        loading: false,
        error,
        duration: 0,
        timestamp: new Date().toISOString(),
        demoMode
      };
    }
  },

  /**
   * Get query templates for common operations
   */
  getQueryTemplates() {
    return {
      queries: [
        {
          name: 'Get Current User',
          description: 'Get information about the currently authenticated user',
          query: `query GetCurrentUser {
  me {
    id
    email
    name
    role
    createdAt
  }
}`,
          variables: {}
        },
        {
          name: 'List Streams',
          description: 'Get all streams for the current user',
          query: `query GetStreams {
  streams {
    id
    name
    description
    streamKey
    playbackId
    status
    record
    createdAt
    updatedAt
  }
}`,
          variables: {}
        },
        {
          name: 'Get Stream Details',
          description: 'Get details for a specific stream',
          query: `query GetStream($id: ID!) {
  stream(id: $id) {
    id
    name
    description
    streamKey
    playbackId
    status
    record
    createdAt
    updatedAt
  }
}`,
          variables: {
            id: "demo_live_stream_001"
          }
        },
        {
          name: 'Stream Analytics',
          description: 'Get analytics for a stream',
          query: `query GetStreamAnalytics($streamId: ID!, $timeRange: TimeRangeInput) {
  streamAnalytics(streamId: $streamId, timeRange: $timeRange) {
    streamId
    totalViews
    totalViewTime
    peakViewers
    averageViewers
    uniqueViewers
    timeRange {
      start
      end
    }
  }
}`,
          variables: {
            streamId: "demo_live_stream_001",
            timeRange: {
              start: "2025-01-01T00:00:00Z",
              end: "2025-01-31T23:59:59Z"
            }
          }
        },
        {
          name: 'Billing Status',
          description: 'Get current billing status and tier',
          query: `query GetBillingStatus {
  billingStatus {
    currentTier {
      id
      name
      description
      price
      currency
      features
    }
    nextBillingDate
    outstandingAmount
    status
  }
}`,
          variables: {}
        }
      ],
      mutations: [
        {
          name: 'Create Stream',
          description: 'Create a new stream',
          query: `mutation CreateStream($input: CreateStreamInput!) {
  createStream(input: $input) {
    id
    name
    description
    streamKey
    playbackId
    status
    record
    createdAt
    updatedAt
  }
}`,
          variables: {
            input: {
              name: "My New Stream",
              description: "Created via GraphQL Explorer",
              record: false
            }
          }
        },
        {
          name: 'Update Stream',
          description: 'Update an existing stream',
          query: `mutation UpdateStream($id: ID!, $input: UpdateStreamInput!) {
  updateStream(id: $id, input: $input) {
    id
    name
    description
    streamKey
    playbackId
    status
    record
    createdAt
    updatedAt
  }
}`,
          variables: {
            id: "demo_live_stream_001",
            input: {
              name: "Updated Stream Name",
              description: "Updated description",
              record: true
            }
          }
        },
        {
          name: 'Delete Stream',
          description: 'Delete a stream',
          query: `mutation DeleteStream($id: ID!) {
  deleteStream(id: $id)
}`,
          variables: {
            id: "demo_live_stream_001"
          }
        },
        {
          name: 'Create Clip',
          description: 'Create a clip from a stream',
          query: `mutation CreateClip($input: CreateClipInput!) {
  createClip(input: $input) {
    id
    stream
    title
    description
    startTime
    endTime
    duration
    playbackId
    status
    createdAt
    updatedAt
  }
}`,
          variables: {
            input: {
              stream: "demo_live_stream_001",
              startTime: 0,
              endTime: 30,
              title: "My Clip",
              description: "A highlight from my stream"
            }
          }
        }
      ],
      subscriptions: [
        {
          name: 'Stream Events',
          description: 'Subscribe to stream lifecycle events',
          query: `subscription StreamEvents($streamId: ID) {
  streamEvents(streamId: $streamId) {
    type
    stream
    status
    timestamp
    details
  }
}`,
          variables: {
            streamId: "demo_live_stream_001"
          }
        },
        {
          name: 'Viewer Metrics',
          description: 'Subscribe to real-time viewer metrics',
          query: `subscription ViewerMetrics($streamId: ID!) {
  viewerMetrics(streamId: $streamId) {
    timestamp
    viewerCount
  }
}`,
          variables: {
            streamId: "demo_live_stream_001"
          }
        },
        {
          name: 'System Health',
          description: 'Subscribe to system health events (infrastructure monitoring)',
          query: `subscription SystemHealth {
  systemHealth {
    nodeId
    clusterId
    status
    cpuUsage
    memoryUsage
    diskUsage
    healthScore
    timestamp
  }
}`,
          variables: {}
        },
        {
          name: 'Track List Updates',
          description: 'Subscribe to track list changes for a stream',
          query: `subscription TrackListUpdates($streamId: ID!) {
  trackListUpdates(streamId: $streamId) {
    streamId
    tenantId
    trackList
    trackCount
    timestamp
  }
}`,
          variables: {
            streamId: "stream-id-here"
          }
        },
        {
          name: 'Tenant Events',
          description: 'Subscribe to all events for current tenant',
          query: `subscription TenantEvents($tenantId: ID!) {
  tenantEvents(tenantId: $tenantId) {
    ... on StreamEvent {
      type
      streamId
      tenantId
      status
      timestamp
      nodeId
      details
    }
    ... on ViewerMetrics {
      streamId
      currentViewers
      peakViewers
      bandwidth
      connectionQuality
      bufferHealth
      timestamp
    }
    ... on TrackListEvent {
      streamId
      tenantId
      trackList
      trackCount
      timestamp
    }
  }
}`,
          variables: {
            tenantId: "tenant-id-here"
          }
        }
      ]
    };
  },

  /**
   * Generate code examples for different languages
   * @param {string} query - The GraphQL query
   * @param {Object} variables - Query variables
   * @param {string} token - Auth token
   */
  generateCodeExamples(query, variables = {}, token = null) {
    const tokenValue = token || 'your_token_here';
    const hasVariables = Object.keys(variables).length > 0;
    
    const examples = {
      javascript: `// JavaScript (Apollo Client)
import { ApolloClient, InMemoryCache, gql } from '@apollo/client';

const client = new ApolloClient({
  uri: '${import.meta.env.VITE_GRAPHQL_HTTP_URL || 'http://localhost:18000/graphql/'}',
  cache: new InMemoryCache(),
  headers: {
    Authorization: 'Bearer ${tokenValue}'
  }
});

const query = gql\`${query}\`;

${hasVariables ? `const variables = ${JSON.stringify(variables, null, 2)};

const { data, error } = await client.query({
  query,
  variables
});` : `const { data, error } = await client.query({
  query
});`}

console.log(data);`,

      fetch: `// JavaScript (Fetch API)
const query = \`${query}\`;
${hasVariables ? `const variables = ${JSON.stringify(variables, null, 2)};` : ''}

const response = await fetch('${import.meta.env.VITE_GRAPHQL_HTTP_URL || 'http://localhost:18000/graphql/'}', {
  method: 'POST',
  headers: {
    'Content-Type': 'application/json',
    'Authorization': 'Bearer ${tokenValue}'
  },
  body: JSON.stringify({
    query${hasVariables ? ',\n    variables' : ''}
  })
});

const result = await response.json();
console.log(result.data);`,

      curl: `# cURL
curl -X POST \\
  -H "Content-Type: application/json" \\
  -H "Authorization: Bearer ${tokenValue}" \\
  -d '{"query":"${query.replace(/"/g, '\\"').replace(/\n/g, '\\n')}"${hasVariables ? `,"variables":${JSON.stringify(variables)}` : ''}}' \\
  ${import.meta.env.VITE_GRAPHQL_HTTP_URL || 'http://localhost:18000/graphql/'}`,

      python: `# Python (requests)
import requests
import json

url = "${import.meta.env.VITE_GRAPHQL_HTTP_URL || 'http://localhost:18000/graphql/'}"
headers = {
    "Content-Type": "application/json",
    "Authorization": "Bearer ${tokenValue}"
}

query = """${query}"""
${hasVariables ? `variables = ${JSON.stringify(variables, null, 4)}` : ''}

payload = {
    "query": query${hasVariables ? ',\n    "variables": variables' : ''}
}

response = requests.post(url, headers=headers, json=payload)
result = response.json()
print(result["data"])`,

      go: `// Go
package main

import (
    "bytes"
    "encoding/json"
    "fmt"
    "net/http"
)

type GraphQLRequest struct {
    Query     string      \`json:"query"\`${hasVariables ? '\n    Variables interface{} `json:"variables,omitempty"`' : ''}
}

func main() {
    url := "${import.meta.env.VITE_GRAPHQL_HTTP_URL || 'http://localhost:18000/graphql/'}"
    
    query := \`${query}\`
    ${hasVariables ? `variables := map[string]interface{}{
${Object.entries(variables).map(([key, value]) => `        "${key}": ${JSON.stringify(value)},`).join('\n')}
    }` : ''}
    
    reqBody := GraphQLRequest{
        Query: query,${hasVariables ? '\n        Variables: variables,' : ''}
    }
    
    jsonData, _ := json.Marshal(reqBody)
    
    req, _ := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
    req.Header.Set("Content-Type", "application/json")
    req.Header.Set("Authorization", "Bearer ${tokenValue}")
    
    client := &http.Client{}
    resp, err := client.Do(req)
    if err != nil {
        panic(err)
    }
    defer resp.Body.Close()
    
    var result map[string]interface{}
    json.NewDecoder(resp.Body).Decode(&result)
    fmt.Printf("%+v\\n", result["data"])
}`
    };

    return examples;
  },

  /**
   * Validate GraphQL query syntax
   * @param {string} query - The query to validate
   */
  validateQuery(query) {
    try {
      // Basic validation - check for balanced braces and basic structure
      const braceCount = (query.match(/\{/g) || []).length - (query.match(/\}/g) || []).length;
      if (braceCount !== 0) {
        return {
          valid: false,
          error: 'Unbalanced braces in query'
        };
      }

      // Check for query/mutation/subscription keywords
      const hasOperation = /^(query|mutation|subscription)\s+/.test(query.trim()) || 
                          /\{\s*(query|mutation|subscription)\s+/.test(query);
      
      if (!hasOperation && !query.trim().startsWith('{')) {
        return {
          valid: false,
          error: 'Query must start with query, mutation, subscription, or {'
        };
      }

      return {
        valid: true,
        error: null
      };
    } catch (error) {
      return {
        valid: false,
        error: error.message
      };
    }
  },

  /**
   * Format query response for display
   * @param {Object} result - Query result from executeQuery
   */
  formatResponse(result) {
    const { data, error, duration, timestamp, networkStatus } = result;
    
    // Note: These will be rendered as HTML strings in the GraphQL Explorer UI
    // The component consuming this will handle the HTML rendering
    let status = 'success';
    let statusIcon = 'success'; // Will be mapped to proper Lucide icon in UI
    
    if (error) {
      status = 'error';
      statusIcon = 'error';
    } else if (networkStatus === 1) {
      status = 'loading';
      statusIcon = 'loading';
    }

    const response = {
      status,
      statusIcon,
      timestamp: new Date(timestamp).toLocaleTimeString(),
      duration: `${duration}ms`,
      data: data ? JSON.stringify(data, null, 2) : null,
      error: error ? {
        message: error.message,
        graphQLErrors: error.graphQLErrors?.map(e => ({
          message: e.message,
          locations: e.locations,
          path: e.path
        })),
        networkError: error.networkError ? {
          message: error.networkError.message,
          statusCode: error.networkError.statusCode
        } : null
      } : null
    };

    return response;
  }
};
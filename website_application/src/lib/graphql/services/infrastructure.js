import { client } from '../client.js';
import { 
  GetTenantDocument,
  GetClustersDocument,
  GetClusterDocument,
  GetNodesDocument,
  GetNodeDocument,
  SystemHealthDocument,
  UpdateTenantDocument
} from '../generated/apollo-helpers';

export const infrastructureService = {
  // Queries
  async getTenant() {
    const result = await client.query({
      query: GetTenantDocument,
      fetchPolicy: 'cache-first'
    });
    return result.data.tenant;
  },

  async getClusters() {
    const result = await client.query({
      query: GetClustersDocument,
      fetchPolicy: 'cache-first'
    });
    return result.data.clusters;
  },

  async getCluster(id) {
    const result = await client.query({
      query: GetClusterDocument,
      variables: { id },
      fetchPolicy: 'cache-first'
    });
    return result.data.cluster;
  },

  async getNodes() {
    const result = await client.query({
      query: GetNodesDocument,
      fetchPolicy: 'cache-first'
    });
    return result.data.nodes;
  },

  async getNode(id) {
    const result = await client.query({
      query: GetNodeDocument,
      variables: { id },
      fetchPolicy: 'cache-first'
    });
    return result.data.node;
  },

  // Mutations
  async updateTenant(input) {
    const result = await client.mutate({
      mutation: UpdateTenantDocument,
      variables: { input },
      refetchQueries: [{ query: GetTenantDocument }]
    });
    return result.data.updateTenant;
  },

  // Subscriptions
  subscribeToSystemHealth(callbacks) {
    const observable = client.subscribe({
      query: SystemHealthDocument
    });

    return observable.subscribe({
      next: (result) => {
        if (callbacks.onSystemHealth) {
          callbacks.onSystemHealth(result.data.systemHealth);
        }
      },
      error: (error) => {
        if (callbacks.onError) {
          callbacks.onError(error);
        }
        console.error('System health subscription error:', error);
      }
    });
  }
};
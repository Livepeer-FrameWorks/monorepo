import { client } from "../client.js";
import type { ApolloError } from "@apollo/client";
import {
  GetTenantDocument,
  GetClustersDocument,
  GetClusterDocument,
  GetNodesDocument,
  GetNodeDocument,
  GetServiceInstancesDocument,
  GetTenantClusterAssignmentsDocument,
  SystemHealthDocument,
  UpdateTenantDocument,
} from "../generated/apollo-helpers";
import type {
  GetTenantQuery,
  GetClustersQuery,
  GetClusterQuery,
  GetClusterQueryVariables,
  GetNodesQuery,
  GetNodeQuery,
  GetNodeQueryVariables,
  GetServiceInstancesQuery,
  GetServiceInstancesQueryVariables,
  GetTenantClusterAssignmentsQuery,
  UpdateTenantMutation,
  UpdateTenantMutationVariables,
  SystemHealthSubscription,
} from "../generated/types";

interface SystemHealthCallbacks {
  onSystemHealth?: (event: SystemHealthSubscription["systemHealth"]) => void;
  onError?: (err: ApolloError) => void;
}

export const infrastructureService = {
  // Queries
  async getTenant(): Promise<GetTenantQuery["tenant"]> {
    const result = await client.query({
      query: GetTenantDocument,
      fetchPolicy: "cache-first",
    });
    return result.data.tenant;
  },

  async getClusters(): Promise<GetClustersQuery["clusters"]> {
    const result = await client.query({
      query: GetClustersDocument,
      fetchPolicy: "cache-first",
    });
    return result.data.clusters;
  },

  async getCluster(id: GetClusterQueryVariables["id"]): Promise<GetClusterQuery["cluster"]> {
    const result = await client.query({
      query: GetClusterDocument,
      variables: { id },
      fetchPolicy: "cache-first",
    });
    return result.data.cluster;
  },

  async getNodes(): Promise<GetNodesQuery["nodes"]> {
    const result = await client.query({
      query: GetNodesDocument,
      fetchPolicy: "cache-first",
    });
    return result.data.nodes;
  },

  async getNode(id: GetNodeQueryVariables["id"]): Promise<GetNodeQuery["node"]> {
    const result = await client.query({
      query: GetNodeDocument,
      variables: { id },
      fetchPolicy: "cache-first",
    });
    return result.data.node;
  },

  // Service Instances
  async getServiceInstances(
    clusterId: GetServiceInstancesQueryVariables["clusterId"] = null,
  ): Promise<GetServiceInstancesQuery["serviceInstances"]> {
    try {
      const result = await client.query({
        query: GetServiceInstancesDocument,
        variables: clusterId ? { clusterId } : {},
        fetchPolicy: "cache-first",
        errorPolicy: "all",
      });
      return result.data?.serviceInstances || [];
    } catch (error) {
      console.error("Failed to fetch service instances:", error);
      return [];
    }
  },

  // Tenant Cluster Assignments
  async getTenantClusterAssignments(): Promise<GetTenantClusterAssignmentsQuery["tenantClusterAssignments"]> {
    try {
      const result = await client.query({
        query: GetTenantClusterAssignmentsDocument,
        fetchPolicy: "cache-first",
        errorPolicy: "all",
      });
      return result.data?.tenantClusterAssignments || [];
    } catch (error) {
      console.error("Failed to fetch tenant cluster assignments:", error);
      return [];
    }
  },

  // Mutations
  async updateTenant(
    input: UpdateTenantMutationVariables["input"],
  ): Promise<UpdateTenantMutation["updateTenant"]> {
    const result = await client.mutate({
      mutation: UpdateTenantDocument,
      variables: { input },
      refetchQueries: [{ query: GetTenantDocument }],
    });
    return result.data!.updateTenant;
  },

  // Subscriptions
  subscribeToSystemHealth(callbacks: SystemHealthCallbacks) {
    const observable = client.subscribe({
      query: SystemHealthDocument,
    });

    return observable.subscribe({
      next: (result) => {
        if (result.data?.systemHealth) {
          callbacks.onSystemHealth?.(result.data.systemHealth);
        }
      },
      error: (error: ApolloError) => {
        callbacks.onError?.(error);
        console.error("System health subscription error:", error);
      },
    });
  },
};

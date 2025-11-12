import { client } from "../client.js";
import {
  GetViewerGeographicsDocument,
  GetGeographicDistributionDocument,
  GetLoadBalancingMetricsDocument,
} from "../generated/apollo-helpers";
import type {
  GetViewerGeographicsQuery,
  GetViewerGeographicsQueryVariables,
  GetGeographicDistributionQuery,
  GetGeographicDistributionQueryVariables,
  GetLoadBalancingMetricsQuery,
  GetLoadBalancingMetricsQueryVariables,
} from "../generated/types";

export const geographicService = {
  // Get viewer geographic data for a stream over time range
  async getViewerGeographics(
    streamId: GetViewerGeographicsQueryVariables["stream"],
    timeRange: GetViewerGeographicsQueryVariables["timeRange"],
  ): Promise<GetViewerGeographicsQuery["viewerGeographics"]> {
    try {
      const result = await client.query({
        query: GetViewerGeographicsDocument,
        variables: { stream: streamId, timeRange },
        fetchPolicy: "cache-first",
        errorPolicy: "all",
      });
      return result.data?.viewerGeographics || [];
    } catch (error) {
      console.error("Failed to fetch viewer geographics:", error);
      return [];
    }
  },

  // Get geographic distribution for a stream
  async getGeographicDistribution(
    streamId: GetGeographicDistributionQueryVariables["stream"],
    timeRange: GetGeographicDistributionQueryVariables["timeRange"],
  ): Promise<GetGeographicDistributionQuery["geographicDistribution"] | null> {
    try {
      const result = await client.query({
        query: GetGeographicDistributionDocument,
        variables: { stream: streamId, timeRange },
        fetchPolicy: "cache-first",
        errorPolicy: "all",
      });
      return result.data?.geographicDistribution || null;
    } catch (error) {
      console.error("Failed to fetch geographic distribution:", error);
      return null;
    }
  },

  // Get load balancing metrics with geographic data
  async getLoadBalancingMetrics(
    timeRange: GetLoadBalancingMetricsQueryVariables["timeRange"],
  ): Promise<GetLoadBalancingMetricsQuery["loadBalancingMetrics"]> {
    try {
      const result = await client.query({
        query: GetLoadBalancingMetricsDocument,
        variables: { timeRange },
        fetchPolicy: "cache-first",
        errorPolicy: "all",
      });
      return result.data?.loadBalancingMetrics || [];
    } catch (error) {
      console.error("Failed to fetch load balancing metrics:", error);
      return [];
    }
  },
};

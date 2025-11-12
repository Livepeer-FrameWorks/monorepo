import { client } from "../client.js";
import {
  GetRoutingEventsDocument,
  GetConnectionEventsDocument,
  GetNodeMetricsDocument,
  GetPlatformRoutingEventsDocument,
  GetStreamConnectionEventsDocument,
  GetAllNodeMetricsDocument,
} from "../generated/apollo-helpers";
import type {
  GetRoutingEventsQuery,
  GetRoutingEventsQueryVariables,
  GetConnectionEventsQuery,
  GetConnectionEventsQueryVariables,
  GetNodeMetricsQuery,
  GetNodeMetricsQueryVariables,
  GetPlatformRoutingEventsQuery,
  GetPlatformRoutingEventsQueryVariables,
  GetStreamConnectionEventsQuery,
  GetStreamConnectionEventsQueryVariables,
  GetAllNodeMetricsQuery,
  GetAllNodeMetricsQueryVariables,
} from "../generated/types";

interface RoutingEfficiency {
  efficiency: number;
  avgScore: number;
  totalDecisions: number;
  successfulRoutes?: number;
}

interface ConnectionPatterns {
  totalConnections: number;
  totalDisconnections: number;
  netConnections: number;
  countryDistribution: Record<string, number>;
  nodeDistribution: Record<string, number>;
  connectionEvents: GetConnectionEventsQuery["connectionEvents"];
}

interface NodeHealthSummary {
  healthy: number;
  degraded: number;
  unhealthy: number;
  total: number;
  nodes?: Record<string, GetAllNodeMetricsQuery["nodeMetrics"][0]>;
}

export const routingService = {
  // Get routing events for a specific stream
  async getRoutingEvents(
    streamId: GetRoutingEventsQueryVariables["stream"],
    timeRange: GetRoutingEventsQueryVariables["timeRange"],
  ): Promise<GetRoutingEventsQuery["routingEvents"]> {
    try {
      const result = await client.query({
        query: GetRoutingEventsDocument,
        variables: { stream: streamId, timeRange },
        fetchPolicy: "cache-first",
        errorPolicy: "all",
      });
      return result.data?.routingEvents || [];
    } catch (error) {
      console.error("Failed to fetch routing events:", error);
      return [];
    }
  },

  // Get connection events for a specific stream
  async getConnectionEvents(
    streamId: GetConnectionEventsQueryVariables["stream"],
    timeRange: GetConnectionEventsQueryVariables["timeRange"],
  ): Promise<GetConnectionEventsQuery["connectionEvents"]> {
    try {
      const result = await client.query({
        query: GetConnectionEventsDocument,
        variables: { stream: streamId, timeRange },
        fetchPolicy: "cache-first",
        errorPolicy: "all",
      });
      return result.data?.connectionEvents || [];
    } catch (error) {
      console.error("Failed to fetch connection events:", error);
      return [];
    }
  },

  // Get node performance metrics for a specific node
  async getNodeMetrics(
    nodeId: GetNodeMetricsQueryVariables["nodeId"],
    timeRange: GetNodeMetricsQueryVariables["timeRange"],
  ): Promise<GetNodeMetricsQuery["nodeMetrics"]> {
    try {
      const result = await client.query({
        query: GetNodeMetricsDocument,
        variables: { nodeId, timeRange },
        fetchPolicy: "cache-first",
        errorPolicy: "all",
      });
      return result.data?.nodeMetrics || [];
    } catch (error) {
      console.error("Failed to fetch node metrics:", error);
      return [];
    }
  },

  // Get all routing events across the platform (admin view)
  async getPlatformRoutingEvents(
    timeRange: GetPlatformRoutingEventsQueryVariables["timeRange"],
  ): Promise<GetPlatformRoutingEventsQuery["routingEvents"]> {
    try {
      const result = await client.query({
        query: GetPlatformRoutingEventsDocument,
        variables: { timeRange },
        fetchPolicy: "cache-first",
        errorPolicy: "all",
      });
      return result.data?.routingEvents || [];
    } catch (error) {
      console.error("Failed to fetch platform routing events:", error);
      return [];
    }
  },

  // Get connection events specifically for a stream
  async getStreamConnectionEvents(
    streamId: GetStreamConnectionEventsQueryVariables["stream"],
    timeRange: GetStreamConnectionEventsQueryVariables["timeRange"],
  ): Promise<GetStreamConnectionEventsQuery["connectionEvents"]> {
    try {
      const result = await client.query({
        query: GetStreamConnectionEventsDocument,
        variables: { stream: streamId, timeRange },
        fetchPolicy: "cache-first",
        errorPolicy: "all",
      });
      return result.data?.connectionEvents || [];
    } catch (error) {
      console.error("Failed to fetch stream connection events:", error);
      return [];
    }
  },

  // Get metrics for all nodes
  async getAllNodeMetrics(
    timeRange: GetAllNodeMetricsQueryVariables["timeRange"],
  ): Promise<GetAllNodeMetricsQuery["nodeMetrics"]> {
    try {
      const result = await client.query({
        query: GetAllNodeMetricsDocument,
        variables: { timeRange },
        fetchPolicy: "cache-first",
        errorPolicy: "all",
      });
      return result.data?.nodeMetrics || [];
    } catch (error) {
      console.error("Failed to fetch all node metrics:", error);
      return [];
    }
  },

  // Helper method to analyze routing efficiency
  async getRoutingEfficiency(
    streamId: string,
    timeRange: GetRoutingEventsQueryVariables["timeRange"],
  ): Promise<RoutingEfficiency> {
    try {
      const routingEvents = await this.getRoutingEvents(streamId, timeRange);

      if (routingEvents.length === 0) {
        return { efficiency: 0, avgScore: 0, totalDecisions: 0 };
      }

      const successfulRoutes = routingEvents.filter(
        (event) => event.status === "success",
      );
      const totalScore = routingEvents.reduce(
        (sum, event) => sum + (event.score || 0),
        0,
      );

      return {
        efficiency: (successfulRoutes.length / routingEvents.length) * 100,
        avgScore: totalScore / routingEvents.length,
        totalDecisions: routingEvents.length,
        successfulRoutes: successfulRoutes.length,
      };
    } catch (error) {
      console.error("Failed to analyze routing efficiency:", error);
      return { efficiency: 0, avgScore: 0, totalDecisions: 0 };
    }
  },

  // Helper method to analyze connection patterns
  async getConnectionPatterns(
    streamId: string,
    timeRange: GetConnectionEventsQueryVariables["timeRange"],
  ): Promise<ConnectionPatterns> {
    try {
      const connectionEvents = await this.getConnectionEvents(
        streamId,
        timeRange,
      );

      const connections = connectionEvents.filter(
        (event) => event.eventType === "connect",
      );
      const disconnections = connectionEvents.filter(
        (event) => event.eventType === "disconnect",
      );

      // Group by country
      const countryConnections = connections.reduce<Record<string, number>>((acc, event) => {
        const country = event.countryCode || "unknown";
        acc[country] = (acc[country] || 0) + 1;
        return acc;
      }, {});

      // Group by node
      const nodeConnections = connections.reduce<Record<string, number>>((acc, event) => {
        const node = event.nodeId || "unknown";
        acc[node] = (acc[node] || 0) + 1;
        return acc;
      }, {});

      return {
        totalConnections: connections.length,
        totalDisconnections: disconnections.length,
        netConnections: connections.length - disconnections.length,
        countryDistribution: countryConnections,
        nodeDistribution: nodeConnections,
        connectionEvents,
      };
    } catch (error) {
      console.error("Failed to analyze connection patterns:", error);
      return {
        totalConnections: 0,
        totalDisconnections: 0,
        netConnections: 0,
        countryDistribution: {},
        nodeDistribution: {},
        connectionEvents: [],
      };
    }
  },

  // Helper method to get node health summary
  async getNodeHealthSummary(
    timeRange: GetAllNodeMetricsQueryVariables["timeRange"],
  ): Promise<NodeHealthSummary> {
    try {
      const nodeMetrics = await this.getAllNodeMetrics(timeRange);

      if (nodeMetrics.length === 0) {
        return { healthy: 0, degraded: 0, unhealthy: 0, total: 0 };
      }

      // Get latest metrics for each node
      const latestMetrics = nodeMetrics.reduce<Record<string, GetAllNodeMetricsQuery["nodeMetrics"][0]>>((acc, metric) => {
        if (
          !acc[metric.nodeId] ||
          new Date(metric.timestamp) > new Date(acc[metric.nodeId].timestamp)
        ) {
          acc[metric.nodeId] = metric;
        }
        return acc;
      }, {});

      const healthCounts = Object.values(latestMetrics).reduce(
        (acc, metric) => {
          if (metric.status === "HEALTHY") acc.healthy++;
          else if (metric.status === "DEGRADED") acc.degraded++;
          else acc.unhealthy++;
          return acc;
        },
        { healthy: 0, degraded: 0, unhealthy: 0 },
      );

      return {
        ...healthCounts,
        total: Object.keys(latestMetrics).length,
        nodes: latestMetrics,
      };
    } catch (error) {
      console.error("Failed to get node health summary:", error);
      return { healthy: 0, degraded: 0, unhealthy: 0, total: 0 };
    }
  },
};

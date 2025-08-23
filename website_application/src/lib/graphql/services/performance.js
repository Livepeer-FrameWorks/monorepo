import { client } from '../client.js';
import { 
  GetViewerMetrics5mDocument,
  GetPerformanceServiceInstancesDocument,
  GetPlatformPerformanceDocument,
  GetStreamPerformanceDocument,
  GetNodeEfficiencyDocument,
  GetRegionalPerformanceDocument
} from '../generated/apollo-helpers';

export const performanceService = {
  // Get 5-minute aggregated viewer metrics
  async getViewerMetrics5m(streamId, timeRange) {
    try {
      const result = await client.query({
        query: GetViewerMetrics5mDocument,
        variables: { stream: streamId, timeRange },
        fetchPolicy: 'cache-first',
        errorPolicy: 'all'
      });
      return result.data?.viewerMetrics5m || [];
    } catch (error) {
      console.error('Failed to fetch 5m viewer metrics:', error);
      return [];
    }
  },

  // Get service instances for a cluster
  async getServiceInstances(clusterId) {
    try {
      const result = await client.query({
        query: GetPerformanceServiceInstancesDocument,
        variables: { clusterId },
        fetchPolicy: 'cache-first',
        errorPolicy: 'all'
      });
      return result.data?.serviceInstances || [];
    } catch (error) {
      console.error('Failed to fetch service instances:', error);
      return [];
    }
  },

  // Get platform-wide performance overview
  async getPlatformPerformance(timeRange) {
    try {
      const result = await client.query({
        query: GetPlatformPerformanceDocument,
        variables: { timeRange },
        fetchPolicy: 'cache-first',
        errorPolicy: 'all'
      });
      return {
        viewerMetrics: result.data?.viewerMetrics5m || [],
        nodeMetrics: result.data?.nodeMetrics || []
      };
    } catch (error) {
      console.error('Failed to fetch platform performance:', error);
      return { viewerMetrics: [], nodeMetrics: [] };
    }
  },

  // Get comprehensive stream performance data
  async getStreamPerformance(streamId, timeRange) {
    try {
      const result = await client.query({
        query: GetStreamPerformanceDocument,
        variables: { stream: streamId, timeRange },
        fetchPolicy: 'cache-first',
        errorPolicy: 'all'
      });
      return {
        viewerMetrics: result.data?.viewerMetrics5m || [],
        routingEvents: result.data?.routingEvents || []
      };
    } catch (error) {
      console.error('Failed to fetch stream performance:', error);
      return { viewerMetrics: [], routingEvents: [] };
    }
  },

  // Get node efficiency combining performance and routing
  async getNodeEfficiency(nodeId, timeRange) {
    try {
      const result = await client.query({
        query: GetNodeEfficiencyDocument,
        variables: { nodeId, timeRange },
        fetchPolicy: 'cache-first',
        errorPolicy: 'all'
      });
      return {
        nodeMetrics: result.data?.nodeMetrics || [],
        routingEvents: result.data?.routingEvents || []
      };
    } catch (error) {
      console.error('Failed to fetch node efficiency:', error);
      return { nodeMetrics: [], routingEvents: [] };
    }
  },

  // Get regional performance analysis
  async getRegionalPerformance(timeRange) {
    try {
      const result = await client.query({
        query: GetRegionalPerformanceDocument,
        variables: { timeRange },
        fetchPolicy: 'cache-first',
        errorPolicy: 'all'
      });
      return {
        viewerMetrics: result.data?.viewerMetrics5m || [],
        connectionEvents: result.data?.connectionEvents || []
      };
    } catch (error) {
      console.error('Failed to fetch regional performance:', error);
      return { viewerMetrics: [], connectionEvents: [] };
    }
  },

  // Helper method to calculate platform performance summary
  async getPlatformSummary(timeRange) {
    try {
      const performance = await this.getPlatformPerformance(timeRange);
      
      if (performance.viewerMetrics.length === 0) {
        return {
          totalViewers: 0,
          avgViewers: 0,
          totalStreams: 0,
          avgConnectionQuality: 0,
          avgBufferHealth: 0,
          uniqueCountries: 0,
          uniqueCities: 0,
          nodesHealthy: 0,
          nodesDegraded: 0,
          nodesUnhealthy: 0
        };
      }

      // Calculate viewer metrics
      const latestViewerMetrics = performance.viewerMetrics.reduce((acc, metric) => {
        const key = metric.internalName;
        if (!acc[key] || new Date(metric.timestamp) > new Date(acc[key].timestamp)) {
          acc[key] = metric;
        }
        return acc;
      }, {});

      const viewerStats = Object.values(latestViewerMetrics).reduce((acc, metric) => {
        acc.totalViewers += metric.avgViewers || 0;
        acc.uniqueCountries += metric.uniqueCountries || 0;
        acc.uniqueCities += metric.uniqueCities || 0;
        acc.totalConnectionQuality += metric.avgConnectionQuality || 0;
        acc.totalBufferHealth += metric.avgBufferHealth || 0;
        acc.count++;
        return acc;
      }, { totalViewers: 0, uniqueCountries: 0, uniqueCities: 0, totalConnectionQuality: 0, totalBufferHealth: 0, count: 0 });

      // Calculate node health
      const nodeHealth = performance.nodeMetrics.reduce((acc, metric) => {
        if (metric.status === 'HEALTHY') acc.healthy++;
        else if (metric.status === 'DEGRADED') acc.degraded++;
        else acc.unhealthy++;
        return acc;
      }, { healthy: 0, degraded: 0, unhealthy: 0 });

      return {
        totalViewers: Math.round(viewerStats.totalViewers),
        avgViewers: Math.round(viewerStats.totalViewers / (viewerStats.count || 1)),
        totalStreams: viewerStats.count,
        avgConnectionQuality: viewerStats.count > 0 ? (viewerStats.totalConnectionQuality / viewerStats.count) : 0,
        avgBufferHealth: viewerStats.count > 0 ? (viewerStats.totalBufferHealth / viewerStats.count) : 0,
        uniqueCountries: viewerStats.uniqueCountries,
        uniqueCities: viewerStats.uniqueCities,
        nodesHealthy: nodeHealth.healthy,
        nodesDegraded: nodeHealth.degraded,
        nodesUnhealthy: nodeHealth.unhealthy
      };
    } catch (error) {
      console.error('Failed to calculate platform summary:', error);
      return {
        totalViewers: 0,
        avgViewers: 0,
        totalStreams: 0,
        avgConnectionQuality: 0,
        avgBufferHealth: 0,
        uniqueCountries: 0,
        uniqueCities: 0,
        nodesHealthy: 0,
        nodesDegraded: 0,
        nodesUnhealthy: 0
      };
    }
  },

  // Helper method to analyze stream efficiency
  async analyzeStreamEfficiency(streamId, timeRange) {
    try {
      const performance = await this.getStreamPerformance(streamId, timeRange);
      
      if (performance.viewerMetrics.length === 0) {
        return { efficiency: 0, qualityScore: 0, routingScore: 0 };
      }

      // Analyze viewer metrics efficiency
      const avgMetrics = performance.viewerMetrics.reduce((acc, metric) => {
        acc.connectionQuality += metric.avgConnectionQuality || 0;
        acc.bufferHealth += metric.avgBufferHealth || 0;
        acc.count++;
        return acc;
      }, { connectionQuality: 0, bufferHealth: 0, count: 0 });

      const qualityScore = avgMetrics.count > 0 ? 
        (avgMetrics.connectionQuality + avgMetrics.bufferHealth) / (2 * avgMetrics.count) : 0;

      // Analyze routing efficiency
      const routingStats = performance.routingEvents.reduce((acc, event) => {
        acc.totalScore += event.score || 0;
        acc.successCount += event.status === 'success' ? 1 : 0;
        acc.count++;
        return acc;
      }, { totalScore: 0, successCount: 0, count: 0 });

      const routingScore = routingStats.count > 0 ? 
        (routingStats.successCount / routingStats.count) * (routingStats.totalScore / routingStats.count) / 100 : 0;

      const efficiency = (qualityScore + routingScore) / 2;

      return {
        efficiency: Math.round(efficiency * 100),
        qualityScore: Math.round(qualityScore * 100),
        routingScore: Math.round(routingScore * 100),
        avgConnectionQuality: avgMetrics.count > 0 ? avgMetrics.connectionQuality / avgMetrics.count : 0,
        avgBufferHealth: avgMetrics.count > 0 ? avgMetrics.bufferHealth / avgMetrics.count : 0,
        routingSuccessRate: routingStats.count > 0 ? (routingStats.successCount / routingStats.count) * 100 : 0
      };
    } catch (error) {
      console.error('Failed to analyze stream efficiency:', error);
      return { efficiency: 0, qualityScore: 0, routingScore: 0 };
    }
  },

  // Helper method to get top performing nodes
  async getTopPerformingNodes(timeRange, limit = 10) {
    try {
      const performance = await this.getPlatformPerformance(timeRange);
      
      // Calculate node performance scores
      const nodeScores = performance.nodeMetrics.reduce((acc, metric) => {
        const nodeId = metric.nodeId;
        if (!acc[nodeId]) {
          acc[nodeId] = {
            nodeId,
            totalScore: 0,
            count: 0,
            avgCpu: 0,
            avgMemory: 0,
            avgDisk: 0,
            healthScore: 0
          };
        }
        
        // Performance score based on resource usage (lower is better) and health score (higher is better)
        const resourceScore = (100 - metric.cpuUsage) + (100 - metric.memoryUsage) + (100 - metric.diskUsage);
        const performanceScore = (resourceScore / 3) * 0.7 + (metric.healthScore || 0) * 0.3;
        
        acc[nodeId].totalScore += performanceScore;
        acc[nodeId].avgCpu += metric.cpuUsage;
        acc[nodeId].avgMemory += metric.memoryUsage;
        acc[nodeId].avgDisk += metric.diskUsage;
        acc[nodeId].healthScore += metric.healthScore || 0;
        acc[nodeId].count++;
        
        return acc;
      }, {});

      // Calculate averages and sort by performance
      const rankedNodes = Object.values(nodeScores)
        .map(node => ({
          ...node,
          avgScore: node.count > 0 ? node.totalScore / node.count : 0,
          avgCpu: node.count > 0 ? node.avgCpu / node.count : 0,
          avgMemory: node.count > 0 ? node.avgMemory / node.count : 0,
          avgDisk: node.count > 0 ? node.avgDisk / node.count : 0,
          avgHealthScore: node.count > 0 ? node.healthScore / node.count : 0
        }))
        .sort((a, b) => b.avgScore - a.avgScore)
        .slice(0, limit);

      return rankedNodes;
    } catch (error) {
      console.error('Failed to get top performing nodes:', error);
      return [];
    }
  }
};
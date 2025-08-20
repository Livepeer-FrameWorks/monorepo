import { client } from '../client.js';
import { 
  GetViewerMetricsDocument,
  GetStreamAnalyticsDocument,
  GetPlatformOverviewDocument
} from '../generated/apollo-helpers';

export const analyticsService = {
  // Get viewer metrics for a stream over time range
  async getViewerMetrics(streamId, timeRange) {
    try {
      const result = await client.query({
        query: GetViewerMetricsDocument,
        variables: { stream: streamId, timeRange },
        fetchPolicy: 'cache-first',
        errorPolicy: 'all'
      });
      return result.data?.viewerMetrics || [];
    } catch (error) {
      console.error('Failed to fetch viewer metrics:', error);
      return [];
    }
  },

  // Get detailed analytics for a specific stream
  async getStreamAnalytics(streamId, timeRange) {
    try {
      const result = await client.query({
        query: GetStreamAnalyticsDocument,
        variables: { stream: streamId, timeRange },
        fetchPolicy: 'cache-first',
        errorPolicy: 'all'
      });
      return result.data?.streamAnalytics || null;
    } catch (error) {
      console.error('Failed to fetch stream analytics:', error);
      return null;
    }
  },

  // Get platform-wide overview metrics
  async getPlatformOverview(timeRange) {
    try {
      const result = await client.query({
        query: GetPlatformOverviewDocument,
        variables: { timeRange },
        fetchPolicy: 'cache-first',
        errorPolicy: 'all'
      });
      return result.data?.platformOverview || null;
    } catch (error) {
      console.error('Failed to fetch platform overview:', error);
      return null;
    }
  }
};
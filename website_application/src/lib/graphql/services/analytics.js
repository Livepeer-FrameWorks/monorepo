import { client } from '../client.js';
import { 
  GetViewerMetricsDocument,
  GetStreamAnalyticsDocument,
  GetPlatformOverviewDocument
} from '../generated/apollo-helpers';

export const analyticsService = {
  // Get viewer metrics for a stream over time range
  async getViewerMetrics(streamId, timeRange) {
    const result = await client.query({
      query: GetViewerMetricsDocument,
      variables: { streamId, timeRange },
      fetchPolicy: 'cache-first'
    });
    return result.data.viewerMetrics;
  },

  // Get detailed analytics for a specific stream
  async getStreamAnalytics(streamId, timeRange) {
    const result = await client.query({
      query: GetStreamAnalyticsDocument,
      variables: { streamId, timeRange },
      fetchPolicy: 'cache-first'
    });
    return result.data.streamAnalytics;
  },

  // Get platform-wide overview metrics
  async getPlatformOverview(timeRange) {
    const result = await client.query({
      query: GetPlatformOverviewDocument,
      variables: { timeRange },
      fetchPolicy: 'cache-first'
    });
    return result.data.platformOverview;
  }
};
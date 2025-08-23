import { client } from '../client.js';
import { 
  GetViewerMetricsDocument,
  GetStreamAnalyticsDocument,
  GetPlatformOverviewDocument
} from '../generated/apollo-helpers';

export const analyticsService = {
  // Get viewer metrics for a stream over time range
  /**
   * @param {string|null} streamId - Stream ID or null for all streams
   * @param {Object|null} timeRange - Time range object with start and end
   */
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
  /**
   * @param {string} streamId - Stream ID
   * @param {Object|null} timeRange - Time range object
   */
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
  /**
   * @param {Object|null} timeRange - Time range object
   */
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
  },

  // Enhanced analytics methods for Helmsman and Foghorn data

  // Get comprehensive stream analytics with all new fields
  /**
   * @param {string} streamId - Stream ID
   * @param {Object|null} timeRange - Time range object
   */
  async getEnhancedStreamAnalytics(streamId, timeRange) {
    try {
      const result = await client.query({
        query: GetStreamAnalyticsDocument,
        variables: { stream: streamId, timeRange },
        fetchPolicy: 'cache-first',
        errorPolicy: 'all'
      });
      
      const analytics = result.data?.streamAnalytics;
      if (!analytics) return null;

      // Enrich with calculated metrics
      return {
        ...analytics,
        // Calculate derived metrics
        sessionDurationHours: analytics.totalSessionDuration ? analytics.totalSessionDuration / 3600 : 0,
        avgBandwidthMbps: analytics.bandwidthOut ? (analytics.bandwidthOut / 1024 / 1024) : 0,
        connectionEfficiency: analytics.totalConnections > 0 ? 
          (analytics.currentViewers / analytics.totalConnections) * 100 : 0,
        packetLossRate: analytics.packetsSent > 0 ? 
          (analytics.packetsLost / analytics.packetsSent) * 100 : 0,
        
        // Format display values
        resolutionDisplay: this.formatResolution(analytics.resolution),
        bitrateDisplay: this.formatBitrate(analytics.bitrateKbps),
        locationDisplay: analytics.location || `${analytics.nodeName || analytics.nodeId}`,
        
        // Health indicators
        isHealthy: (analytics.currentHealthScore || 0) > 0.8,
        hasNetworkIssues: (analytics.packetLossRate || 0) > 1.0,
        hasBufferIssues: analytics.currentBufferState === 'DRY' || analytics.currentBufferState === 'EMPTY'
      };
    } catch (error) {
      console.error('Failed to fetch enhanced stream analytics:', error);
      return null;
    }
  },

  // Get analytics summary for dashboard
  /**
   * @param {Object|null} timeRange - Time range object
   */
  async getAnalyticsSummary(timeRange) {
    try {
      const [platformOverview, viewerMetrics] = await Promise.all([
        this.getPlatformOverview(timeRange),
        this.getViewerMetrics(null, timeRange) // All streams
      ]);

      // Calculate additional metrics
      const totalViewerTime = viewerMetrics.reduce((sum, metric) => sum + (metric.viewerCount || 0), 0);
      const avgViewers = viewerMetrics.length > 0 ? totalViewerTime / viewerMetrics.length : 0;
      
      return {
        ...platformOverview,
        totalViewerTime,
        avgViewers: Math.round(avgViewers),
        dataPoints: viewerMetrics.length,
        lastUpdated: new Date()
      };
    } catch (error) {
      console.error('Failed to fetch analytics summary:', error);
      return {
        totalStreams: 0,
        totalViewers: 0,
        totalBandwidth: 0,
        totalUsers: 0,
        avgViewers: 0,
        totalViewerTime: 0,
        dataPoints: 0,
        lastUpdated: new Date()
      };
    }
  },

  // Get analytics trends over time
  /**
   * @param {Object|null} timeRange - Time range object
   */
  async getAnalyticsTrends(timeRange) {
    try {
      const viewerMetrics = await this.getViewerMetrics(null, timeRange);
      
      if (viewerMetrics.length === 0) {
        return { trend: 'stable', change: 0, data: [] };
      }

      // Sort by timestamp and calculate trend
      const sortedMetrics = viewerMetrics.sort((a, b) => new Date(a.timestamp) - new Date(b.timestamp));
      const firstHalf = sortedMetrics.slice(0, Math.floor(sortedMetrics.length / 2));
      const secondHalf = sortedMetrics.slice(Math.floor(sortedMetrics.length / 2));

      const firstHalfAvg = firstHalf.reduce((sum, m) => sum + m.viewerCount, 0) / firstHalf.length;
      const secondHalfAvg = secondHalf.reduce((sum, m) => sum + m.viewerCount, 0) / secondHalf.length;

      const change = firstHalfAvg > 0 ? ((secondHalfAvg - firstHalfAvg) / firstHalfAvg) * 100 : 0;
      
      let trend = 'stable';
      if (change > 10) trend = 'growing';
      else if (change < -10) trend = 'declining';

      return {
        trend,
        change: Math.round(change),
        data: sortedMetrics,
        firstHalfAvg: Math.round(firstHalfAvg),
        secondHalfAvg: Math.round(secondHalfAvg)
      };
    } catch (error) {
      console.error('Failed to fetch analytics trends:', error);
      return { trend: 'stable', change: 0, data: [] };
    }
  },

  // Helper methods for formatting

  formatResolution(resolution) {
    if (!resolution) return 'Unknown';
    
    // Parse common resolution formats
    if (resolution.includes('1920x1080')) return '1080p';
    if (resolution.includes('1280x720')) return '720p';
    if (resolution.includes('854x480')) return '480p';
    if (resolution.includes('640x360')) return '360p';
    
    return resolution;
  },

  formatBitrate(bitrateKbps) {
    if (!bitrateKbps) return 'Unknown';
    
    if (bitrateKbps >= 1000) {
      return `${(bitrateKbps / 1000).toFixed(1)} Mbps`;
    }
    return `${bitrateKbps} Kbps`;
  },

  formatBandwidth(bytesPerSecond) {
    if (!bytesPerSecond) return '0 B/s';
    
    const units = ['B/s', 'KB/s', 'MB/s', 'GB/s'];
    let value = bytesPerSecond;
    let unitIndex = 0;
    
    while (value >= 1024 && unitIndex < units.length - 1) {
      value /= 1024;
      unitIndex++;
    }
    
    return `${value.toFixed(1)} ${units[unitIndex]}`;
  },

  formatDuration(seconds) {
    if (!seconds) return '0s';
    
    const hours = Math.floor(seconds / 3600);
    const minutes = Math.floor((seconds % 3600) / 60);
    const remainingSeconds = Math.floor(seconds % 60);
    
    if (hours > 0) {
      return `${hours}h ${minutes}m ${remainingSeconds}s`;
    } else if (minutes > 0) {
      return `${minutes}m ${remainingSeconds}s`;
    } else {
      return `${remainingSeconds}s`;
    }
  }
};
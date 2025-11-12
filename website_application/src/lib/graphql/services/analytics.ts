import { client } from "../client.js";
import {
  GetViewerMetricsDocument,
  GetStreamAnalyticsDocument,
  GetPlatformOverviewDocument,
} from "../generated/apollo-helpers";
import type {
  GetViewerMetricsQuery,
  GetViewerMetricsQueryVariables,
  GetStreamAnalyticsQuery,
  GetStreamAnalyticsQueryVariables,
  GetPlatformOverviewQuery,
  GetPlatformOverviewQueryVariables,
} from "../generated/types";

interface EnhancedAnalytics extends NonNullable<GetStreamAnalyticsQuery["streamAnalytics"]> {
  sessionDurationHours: number;
  avgBandwidthMbps: number;
  connectionEfficiency: number;
  packetLossRate: number;
  resolutionDisplay: string;
  bitrateDisplay: string;
  locationDisplay: string;
  isHealthy: boolean;
  hasNetworkIssues: boolean;
  hasBufferIssues: boolean;
}

interface AnalyticsSummary extends GetPlatformOverviewQuery["platformOverview"] {
  totalViewerTime: number;
  avgViewers: number;
  dataPoints: number;
  lastUpdated: Date;
}

interface AnalyticsTrends {
  trend: "growing" | "declining" | "stable";
  change: number;
  data: GetViewerMetricsQuery["viewerMetrics"];
  firstHalfAvg?: number;
  secondHalfAvg?: number;
}

export const analyticsService = {
  // Get viewer metrics for a stream over time range
  async getViewerMetrics(
    streamId: GetViewerMetricsQueryVariables["stream"],
    timeRange: GetViewerMetricsQueryVariables["timeRange"],
  ): Promise<GetViewerMetricsQuery["viewerMetrics"]> {
    try {
      const result = await client.query({
        query: GetViewerMetricsDocument,
        variables: { stream: streamId, timeRange },
        fetchPolicy: "cache-first",
        errorPolicy: "all",
      });
      return result.data?.viewerMetrics || [];
    } catch (error) {
      console.error("Failed to fetch viewer metrics:", error);
      return [];
    }
  },

  // Get detailed analytics for a specific stream
  async getStreamAnalytics(
    streamId: GetStreamAnalyticsQueryVariables["stream"],
    timeRange: GetStreamAnalyticsQueryVariables["timeRange"],
  ): Promise<GetStreamAnalyticsQuery["streamAnalytics"] | null> {
    try {
      const result = await client.query({
        query: GetStreamAnalyticsDocument,
        variables: { stream: streamId, timeRange },
        fetchPolicy: "cache-first",
        errorPolicy: "all",
      });
      return result.data?.streamAnalytics || null;
    } catch (error) {
      console.error("Failed to fetch stream analytics:", error);
      return null;
    }
  },

  // Get platform-wide overview metrics
  async getPlatformOverview(
    timeRange: GetPlatformOverviewQueryVariables["timeRange"],
  ): Promise<GetPlatformOverviewQuery["platformOverview"] | null> {
    try {
      const result = await client.query({
        query: GetPlatformOverviewDocument,
        variables: { timeRange },
        fetchPolicy: "cache-first",
        errorPolicy: "all",
      });
      return result.data?.platformOverview || null;
    } catch (error) {
      console.error("Failed to fetch platform overview:", error);
      return null;
    }
  },

  // Enhanced analytics methods for Helmsman and Foghorn data

  // Get comprehensive stream analytics with all new fields
  async getEnhancedStreamAnalytics(
    streamId: string,
    timeRange: GetStreamAnalyticsQueryVariables["timeRange"],
  ): Promise<EnhancedAnalytics | null> {
    try {
      const result = await client.query({
        query: GetStreamAnalyticsDocument,
        variables: { stream: streamId, timeRange },
        fetchPolicy: "cache-first",
        errorPolicy: "all",
      });

      const analytics = result.data?.streamAnalytics;
      if (!analytics) return null;

      // Enrich with calculated metrics
      return {
        ...analytics,
        // Calculate derived metrics
        sessionDurationHours: analytics.totalSessionDuration
          ? analytics.totalSessionDuration / 3600
          : 0,
        avgBandwidthMbps: analytics.bandwidthOut
          ? analytics.bandwidthOut / 1024 / 1024
          : 0,
        connectionEfficiency:
          analytics.totalConnections > 0
            ? (analytics.currentViewers / analytics.totalConnections) * 100
            : 0,
        packetLossRate:
          analytics.packetsSent > 0
            ? (analytics.packetsLost / analytics.packetsSent) * 100
            : 0,

        // Format display values
        resolutionDisplay: this.formatResolution(analytics.resolution || ""),
        bitrateDisplay: this.formatBitrate(analytics.bitrateKbps || 0),
        locationDisplay:
          analytics.location || `${analytics.nodeName || analytics.nodeId}`,

        // Health indicators
        isHealthy: (analytics.currentHealthScore || 0) > 0.8,
        hasNetworkIssues: (analytics.packetLossRate || 0) > 1.0,
        hasBufferIssues:
          analytics.currentBufferState === "DRY" ||
          analytics.currentBufferState === "EMPTY",
      };
    } catch (error) {
      console.error("Failed to fetch enhanced stream analytics:", error);
      return null;
    }
  },

  // Get analytics summary for dashboard
  async getAnalyticsSummary(
    timeRange: GetPlatformOverviewQueryVariables["timeRange"],
  ): Promise<AnalyticsSummary> {
    try {
      const [platformOverview, viewerMetrics] = await Promise.all([
        this.getPlatformOverview(timeRange),
        this.getViewerMetrics(null, timeRange), // All streams
      ]);

      // Calculate additional metrics
      const totalViewerTime = viewerMetrics.reduce(
        (sum, metric) => sum + (metric.viewerCount || 0),
        0,
      );
      const avgViewers =
        viewerMetrics.length > 0 ? totalViewerTime / viewerMetrics.length : 0;

      return {
        ...platformOverview,
        totalViewerTime,
        avgViewers: Math.round(avgViewers),
        dataPoints: viewerMetrics.length,
        lastUpdated: new Date(),
      };
    } catch (error) {
      console.error("Failed to fetch analytics summary:", error);
      return {
        totalStreams: 0,
        totalViewers: 0,
        totalBandwidth: 0,
        totalUsers: 0,
        avgViewers: 0,
        totalViewerTime: 0,
        dataPoints: 0,
        lastUpdated: new Date(),
      };
    }
  },

  // Get analytics trends over time
  async getAnalyticsTrends(
    timeRange: GetViewerMetricsQueryVariables["timeRange"],
  ): Promise<AnalyticsTrends> {
    try {
      const viewerMetrics = await this.getViewerMetrics(null, timeRange);

      if (viewerMetrics.length === 0) {
        return { trend: "stable", change: 0, data: [] };
      }

      // Sort by timestamp and calculate trend
      const sortedMetrics = viewerMetrics.sort(
        (a, b) => new Date(a.timestamp).getTime() - new Date(b.timestamp).getTime(),
      );
      const firstHalf = sortedMetrics.slice(
        0,
        Math.floor(sortedMetrics.length / 2),
      );
      const secondHalf = sortedMetrics.slice(
        Math.floor(sortedMetrics.length / 2),
      );

      const firstHalfAvg =
        firstHalf.reduce((sum, m) => sum + m.viewerCount, 0) / firstHalf.length;
      const secondHalfAvg =
        secondHalf.reduce((sum, m) => sum + m.viewerCount, 0) /
        secondHalf.length;

      const change =
        firstHalfAvg > 0
          ? ((secondHalfAvg - firstHalfAvg) / firstHalfAvg) * 100
          : 0;

      let trend: "growing" | "declining" | "stable" = "stable";
      if (change > 10) trend = "growing";
      else if (change < -10) trend = "declining";

      return {
        trend,
        change: Math.round(change),
        data: sortedMetrics,
        firstHalfAvg: Math.round(firstHalfAvg),
        secondHalfAvg: Math.round(secondHalfAvg),
      };
    } catch (error) {
      console.error("Failed to fetch analytics trends:", error);
      return { trend: "stable", change: 0, data: [] };
    }
  },

  // Helper methods for formatting

  formatResolution(resolution: string): string {
    if (!resolution) return "Unknown";

    // Parse common resolution formats
    if (resolution.includes("1920x1080")) return "1080p";
    if (resolution.includes("1280x720")) return "720p";
    if (resolution.includes("854x480")) return "480p";
    if (resolution.includes("640x360")) return "360p";

    return resolution;
  },

  formatBitrate(bitrateKbps: number): string {
    if (!bitrateKbps) return "Unknown";

    if (bitrateKbps >= 1000) {
      return `${(bitrateKbps / 1000).toFixed(1)} Mbps`;
    }
    return `${bitrateKbps} Kbps`;
  },

  formatBandwidth(bytesPerSecond: number): string {
    if (!bytesPerSecond) return "0 B/s";

    const units = ["B/s", "KB/s", "MB/s", "GB/s"];
    let value = bytesPerSecond;
    let unitIndex = 0;

    while (value >= 1024 && unitIndex < units.length - 1) {
      value /= 1024;
      unitIndex++;
    }

    return `${value.toFixed(1)} ${units[unitIndex]}`;
  },

  formatDuration(seconds: number): string {
    if (!seconds) return "0s";

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
  },
};

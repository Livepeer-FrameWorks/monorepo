import { client } from "$lib/graphql/client.js";
import {
  GetStreamHealthMetricsDocument,
  GetCurrentStreamHealthDocument,
  GetTrackListEventsDocument,
  GetStreamHealthAlertsDocument,
  GetRebufferingEventsDocument,
} from "$lib/graphql/generated/apollo-helpers.ts";
import type {
  GetStreamHealthMetricsQuery,
  GetStreamHealthMetricsQueryVariables,
  GetCurrentStreamHealthQuery,
  GetCurrentStreamHealthQueryVariables,
  GetTrackListEventsQuery,
  GetTrackListEventsQueryVariables,
  GetStreamHealthAlertsQuery,
  GetStreamHealthAlertsQueryVariables,
  GetRebufferingEventsQuery,
  GetRebufferingEventsQueryVariables,
} from "$lib/graphql/generated/types";

type HealthTrend = "improving" | "degrading" | "stable";
type TrackListActivity = "stable" | "minor-changes" | "unstable";
type AlertSeverity = "none" | "low" | "medium" | "high" | "critical";
type RebufferImpact = "none" | "low" | "medium" | "high";

interface HealthAnalysis {
  healthTrend: HealthTrend;
  trackListActivity: TrackListActivity;
  alertSeverity: AlertSeverity;
  rebufferImpact: RebufferImpact;
  overallScore: number;
}

interface ComprehensiveHealthAnalysis {
  healthMetrics: GetStreamHealthMetricsQuery["streamHealthMetrics"];
  trackListEvents: GetTrackListEventsQuery["trackListEvents"];
  alerts: GetStreamHealthAlertsQuery["streamHealthAlerts"];
  rebufferingEvents: GetRebufferingEventsQuery["rebufferingEvents"];
  analysis: HealthAnalysis;
}

interface CalculateOverallHealthScoreParams {
  healthTrend: HealthTrend;
  trackListActivity: TrackListActivity;
  alertSeverity: AlertSeverity;
  rebufferImpact: RebufferImpact;
}

interface FormattedHealthMetrics {
  healthScorePercent: number;
  frameJitterFormatted: string;
  packetLossFormatted: string;
  qualityDisplay: string;
  bufferHealthPercent: number | null;
  [key: string]: unknown;
}

export const healthService = {
  // Get historical health metrics for a stream
  async getStreamHealthMetrics(
    streamId: GetStreamHealthMetricsQueryVariables["stream"],
    timeRange: GetStreamHealthMetricsQueryVariables["timeRange"],
  ): Promise<GetStreamHealthMetricsQuery["streamHealthMetrics"]> {
    try {
      const result = await client.query({
        query: GetStreamHealthMetricsDocument,
        variables: { stream: streamId, timeRange },
        fetchPolicy: "cache-first",
        errorPolicy: "all",
      });
      return result.data?.streamHealthMetrics || [];
    } catch (error) {
      console.warn("Health metrics not available:", (error as Error).message);
      return [];
    }
  },

  // Get current health status for a stream
  async getCurrentStreamHealth(
    streamId: GetCurrentStreamHealthQueryVariables["stream"],
  ): Promise<GetCurrentStreamHealthQuery["currentStreamHealth"] | null> {
    try {
      const result = await client.query({
        query: GetCurrentStreamHealthDocument,
        variables: { stream: streamId },
        fetchPolicy: "network-only", // Always get fresh health data
        errorPolicy: "all", // Return partial data even if there are errors
      });
      return result.data?.currentStreamHealth || null;
    } catch (error) {
      console.warn("Health monitoring not available:", (error as Error).message);
      return null; // Return null if health monitoring is not available
    }
  },

  // Get recent track list updates (e.g., new renditions, captions)
  async getTrackListEvents(
    streamId: GetTrackListEventsQueryVariables["stream"],
    timeRange: GetTrackListEventsQueryVariables["timeRange"],
  ): Promise<GetTrackListEventsQuery["trackListEvents"]> {
    try {
      const result = await client.query({
        query: GetTrackListEventsDocument,
        variables: { stream: streamId, timeRange },
        fetchPolicy: "cache-first",
        errorPolicy: "all",
      });
      return result.data?.trackListEvents || [];
    } catch (error) {
      console.warn("Track list events not available:", (error as Error).message);
      return [];
    }
  },

  // Get health alerts for a stream
  async getStreamHealthAlerts(
    streamId: GetStreamHealthAlertsQueryVariables["stream"],
    timeRange: GetStreamHealthAlertsQueryVariables["timeRange"],
  ): Promise<GetStreamHealthAlertsQuery["streamHealthAlerts"]> {
    try {
      const result = await client.query({
        query: GetStreamHealthAlertsDocument,
        variables: { stream: streamId, timeRange },
        fetchPolicy: "cache-first",
        errorPolicy: "all",
      });
      return result.data?.streamHealthAlerts || [];
    } catch (error) {
      console.warn("Health alerts not available:", (error as Error).message);
      return [];
    }
  },

  // Get rebuffering events for UX analysis
  async getRebufferingEvents(
    streamId: GetRebufferingEventsQueryVariables["stream"],
    timeRange: GetRebufferingEventsQueryVariables["timeRange"],
  ): Promise<GetRebufferingEventsQuery["rebufferingEvents"]> {
    try {
      const result = await client.query({
        query: GetRebufferingEventsDocument,
        variables: { stream: streamId, timeRange },
        fetchPolicy: "cache-first",
        errorPolicy: "all",
      });
      return result.data?.rebufferingEvents || [];
    } catch (error) {
      console.warn("Rebuffering events not available:", (error as Error).message);
      return [];
    }
  },

  // Helper function to get health score color
  getHealthScoreColor(healthScore: number): string {
    if (healthScore >= 0.9) return "text-green-500";
    if (healthScore >= 0.7) return "text-yellow-500";
    if (healthScore >= 0.5) return "text-orange-500";
    return "text-red-500";
  },

  // Helper function to get buffer state color
  getBufferStateColor(bufferState: string): string {
    switch (bufferState) {
      case "FULL":
        return "text-green-500";
      case "EMPTY":
        return "text-yellow-500";
      case "DRY":
        return "text-red-500";
      case "RECOVER":
        return "text-blue-500";
      default:
        return "text-gray-500";
    }
  },

  // Helper function to get alert severity color
  getAlertSeverityColor(severity: string): string {
    switch (severity) {
      case "LOW":
        return "text-blue-500";
      case "MEDIUM":
        return "text-yellow-500";
      case "HIGH":
        return "text-orange-500";
      case "CRITICAL":
        return "text-red-500";
      default:
        return "text-gray-500";
    }
  },

  // Helper function to format health metrics for display
  formatHealthMetrics(metrics: Record<string, unknown>): FormattedHealthMetrics | null {
    if (!metrics) return null;

    return {
      ...metrics,
      healthScorePercent: Math.round((metrics.healthScore as number) * 100),
      frameJitterFormatted: metrics.frameJitterMs
        ? `${(metrics.frameJitterMs as number).toFixed(1)}ms`
        : "N/A",
      packetLossFormatted: metrics.packetLossPercentage
        ? `${(metrics.packetLossPercentage as number).toFixed(2)}%`
        : "N/A",
      qualityDisplay: (metrics.qualityTier as string) || "Unknown",
      bufferHealthPercent: metrics.bufferHealth
        ? Math.round((metrics.bufferHealth as number) * 100)
        : null,
    };
  },

  // Comprehensive health analysis combining multiple data sources
  async getComprehensiveHealthAnalysis(
    streamId: string,
    timeRange: GetStreamHealthMetricsQueryVariables["timeRange"],
  ): Promise<ComprehensiveHealthAnalysis> {
    try {
      const [healthMetrics, trackListEvents, alerts, rebufferingEvents] =
        await Promise.all([
          this.getStreamHealthMetrics(streamId, timeRange),
          this.getTrackListEvents(streamId, timeRange),
          this.getStreamHealthAlerts(streamId, timeRange),
          this.getRebufferingEvents(streamId, timeRange),
        ]);

      // Calculate health trends
      const healthTrend = this.calculateHealthTrend(healthMetrics);
      const trackListActivity = this.calculateTrackListActivity(trackListEvents);
      const alertSeverity = this.calculateAlertSeverity(alerts);
      const rebufferImpact = this.calculateRebufferImpact(rebufferingEvents);

      return {
        healthMetrics,
        trackListEvents,
        alerts,
        rebufferingEvents,
        analysis: {
          healthTrend,
          trackListActivity,
          alertSeverity,
          rebufferImpact,
          overallScore: this.calculateOverallHealthScore({
            healthTrend,
            trackListActivity,
            alertSeverity,
            rebufferImpact,
          }),
        },
      };
    } catch (error) {
      console.error("Failed to get comprehensive health analysis:", error);
      return {
        healthMetrics: [],
        trackListEvents: [],
        alerts: [],
        rebufferingEvents: [],
        analysis: {
          healthTrend: "stable",
          trackListActivity: "stable",
          alertSeverity: "low",
          rebufferImpact: "low",
          overallScore: 0,
        },
      };
    }
  },

  // Helper method to calculate health trend
  calculateHealthTrend(
    healthMetrics: GetStreamHealthMetricsQuery["streamHealthMetrics"],
  ): HealthTrend {
    if (healthMetrics.length < 2) return "stable";

    const recent = healthMetrics.slice(-10);
    const older = healthMetrics.slice(-20, -10);

    if (older.length === 0) return "stable";

    const recentAvg =
      recent.reduce((sum, m) => sum + (m.healthScore || 0), 0) / recent.length;
    const olderAvg =
      older.reduce((sum, m) => sum + (m.healthScore || 0), 0) / older.length;

    const change = (recentAvg - olderAvg) / olderAvg;

    if (change > 0.1) return "improving";
    if (change < -0.1) return "degrading";
    return "stable";
  },

  // Helper method to calculate quality stability
  calculateTrackListActivity(
    events: GetTrackListEventsQuery["trackListEvents"],
  ): TrackListActivity {
    if (events.length === 0) return "stable";

    const recent = events.filter((event) => {
      const eventTime = new Date(event.timestamp);
      const oneHourAgo = new Date(Date.now() - 60 * 60 * 1000);
      return eventTime > oneHourAgo;
    });

    if (recent.length === 0) return "stable";
    if (recent.length <= 2) return "minor-changes";
    return "unstable";
  },

  // Helper method to calculate alert severity
  calculateAlertSeverity(
    alerts: GetStreamHealthAlertsQuery["streamHealthAlerts"],
  ): AlertSeverity {
    if (alerts.length === 0) return "none";

    const criticalCount = alerts.filter(
      (a) => a.severity === "CRITICAL",
    ).length;
    const highCount = alerts.filter((a) => a.severity === "HIGH").length;
    const mediumCount = alerts.filter((a) => a.severity === "MEDIUM").length;

    if (criticalCount > 0) return "critical";
    if (highCount > 0) return "high";
    if (mediumCount > 0) return "medium";
    return "low";
  },

  // Helper method to calculate rebuffer impact
  calculateRebufferImpact(
    rebufferingEvents: GetRebufferingEventsQuery["rebufferingEvents"],
  ): RebufferImpact {
    if (rebufferingEvents.length === 0) return "none";

    const recentRebuffers = rebufferingEvents.filter((event) => {
      const eventTime = new Date(event.timestamp);
      const oneHourAgo = new Date(Date.now() - 60 * 60 * 1000);
      return eventTime > oneHourAgo;
    });

    if (recentRebuffers.length === 0) return "none";
    if (recentRebuffers.length <= 5) return "low";
    if (recentRebuffers.length <= 15) return "medium";
    return "high";
  },

  // Helper method to calculate overall health score
  calculateOverallHealthScore({
    healthTrend,
    trackListActivity,
    alertSeverity,
    rebufferImpact,
  }: CalculateOverallHealthScoreParams): number {
    let score = 100;

    // Health trend impact
    if (healthTrend === "degrading") score -= 20;
    else if (healthTrend === "improving") score += 10;

    // Track activity impact
    if (trackListActivity === "unstable") score -= 10;
    else if (trackListActivity === "minor-changes") score -= 3;

    // Alert severity impact
    switch (alertSeverity) {
      case "critical":
        score -= 30;
        break;
      case "high":
        score -= 20;
        break;
      case "medium":
        score -= 10;
        break;
      case "low":
        score -= 5;
        break;
    }

    // Rebuffer impact
    switch (rebufferImpact) {
      case "high":
        score -= 25;
        break;
      case "medium":
        score -= 15;
        break;
      case "low":
        score -= 5;
        break;
    }

    return Math.max(0, Math.min(100, score));
  },
};

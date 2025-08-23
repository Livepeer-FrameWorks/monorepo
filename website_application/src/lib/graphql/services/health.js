import { client } from '$lib/graphql/client.js';
import {
  GetStreamHealthMetricsDocument,
  GetCurrentStreamHealthDocument,
  GetStreamQualityChangesDocument,
  GetStreamHealthAlertsDocument,
  GetRebufferingEventsDocument
} from '$lib/graphql/generated/apollo-helpers.ts';

/**
 * Stream Health Monitoring Service
 * Provides real-time and historical health monitoring for streams
 */
export const healthService = {
  // Get historical health metrics for a stream
  /**
   * @param {string} streamId - Stream ID
   * @param {Object|null} timeRange - Time range object
   */
  async getStreamHealthMetrics(streamId, timeRange) {
    try {
      const result = await client.query({
        query: GetStreamHealthMetricsDocument,
        variables: { stream: streamId, timeRange },
        fetchPolicy: 'cache-first',
        errorPolicy: 'all'
      });
      return result.data?.streamHealthMetrics || [];
    } catch (error) {
      console.warn('Health metrics not available:', error.message);
      return [];
    }
  },

  // Get current health status for a stream
  async getCurrentStreamHealth(streamId) {
    try {
      const result = await client.query({
        query: GetCurrentStreamHealthDocument,
        variables: { stream: streamId },
        fetchPolicy: 'network-only', // Always get fresh health data
        errorPolicy: 'all' // Return partial data even if there are errors
      });
      return result.data?.currentStreamHealth || null;
    } catch (error) {
      console.warn('Health monitoring not available:', error.message);
      return null; // Return null if health monitoring is not available
    }
  },

  // Get stream quality changes over time
  async getStreamQualityChanges(streamId, timeRange) {
    try {
      const result = await client.query({
        query: GetStreamQualityChangesDocument,
        variables: { stream: streamId, timeRange },
        fetchPolicy: 'cache-first',
        errorPolicy: 'all'
      });
      return result.data?.streamQualityChanges || [];
    } catch (error) {
      console.warn('Quality changes not available:', error.message);
      return [];
    }
  },

  // Get health alerts for a stream
  async getStreamHealthAlerts(streamId, timeRange) {
    try {
      const result = await client.query({
        query: GetStreamHealthAlertsDocument,
        variables: { stream: streamId, timeRange },
        fetchPolicy: 'cache-first',
        errorPolicy: 'all'
      });
      return result.data?.streamHealthAlerts || [];
    } catch (error) {
      console.warn('Health alerts not available:', error.message);
      return [];
    }
  },

  // Get rebuffering events for UX analysis
  async getRebufferingEvents(streamId, timeRange) {
    try {
      const result = await client.query({
        query: GetRebufferingEventsDocument,
        variables: { stream: streamId, timeRange },
        fetchPolicy: 'cache-first',
        errorPolicy: 'all'
      });
      return result.data?.rebufferingEvents || [];
    } catch (error) {
      console.warn('Rebuffering events not available:', error.message);
      return [];
    }
  },

  // Helper function to get health score color
  getHealthScoreColor(healthScore) {
    if (healthScore >= 0.9) return 'text-green-500';
    if (healthScore >= 0.7) return 'text-yellow-500';
    if (healthScore >= 0.5) return 'text-orange-500';
    return 'text-red-500';
  },

  // Helper function to get buffer state color
  getBufferStateColor(bufferState) {
    switch (bufferState) {
      case 'FULL': return 'text-green-500';
      case 'EMPTY': return 'text-yellow-500';
      case 'DRY': return 'text-red-500';
      case 'RECOVER': return 'text-blue-500';
      default: return 'text-gray-500';
    }
  },

  // Helper function to get alert severity color
  getAlertSeverityColor(severity) {
    switch (severity) {
      case 'LOW': return 'text-blue-500';
      case 'MEDIUM': return 'text-yellow-500';
      case 'HIGH': return 'text-orange-500';
      case 'CRITICAL': return 'text-red-500';
      default: return 'text-gray-500';
    }
  },

  // Helper function to format health metrics for display
  formatHealthMetrics(metrics) {
    if (!metrics) return null;

    return {
      ...metrics,
      healthScorePercent: Math.round(metrics.healthScore * 100),
      frameJitterFormatted: metrics.frameJitterMs ? `${metrics.frameJitterMs.toFixed(1)}ms` : 'N/A',
      packetLossFormatted: metrics.packetLossPercentage ? `${metrics.packetLossPercentage.toFixed(2)}%` : 'N/A',
      qualityDisplay: metrics.qualityTier || 'Unknown',
      bufferHealthPercent: metrics.bufferHealth ? Math.round(metrics.bufferHealth * 100) : null
    };
  },

  // Comprehensive health analysis combining multiple data sources
  async getComprehensiveHealthAnalysis(streamId, timeRange) {
    try {
      const [healthMetrics, qualityChanges, alerts, rebufferingEvents] = await Promise.all([
        this.getStreamHealthMetrics(streamId, timeRange),
        this.getStreamQualityChanges(streamId, timeRange),
        this.getStreamHealthAlerts(streamId, timeRange),
        this.getRebufferingEvents(streamId, timeRange)
      ]);

      // Calculate health trends
      const healthTrend = this.calculateHealthTrend(healthMetrics);
      const qualityStability = this.calculateQualityStability(qualityChanges);
      const alertSeverity = this.calculateAlertSeverity(alerts);
      const rebufferImpact = this.calculateRebufferImpact(rebufferingEvents);

      return {
        healthMetrics,
        qualityChanges,
        alerts,
        rebufferingEvents,
        analysis: {
          healthTrend,
          qualityStability,
          alertSeverity,
          rebufferImpact,
          overallScore: this.calculateOverallHealthScore({
            healthTrend,
            qualityStability,
            alertSeverity,
            rebufferImpact
          })
        }
      };
    } catch (error) {
      console.error('Failed to get comprehensive health analysis:', error);
      return {
        healthMetrics: [],
        qualityChanges: [],
        alerts: [],
        rebufferingEvents: [],
        analysis: {
          healthTrend: 'stable',
          qualityStability: 'stable',
          alertSeverity: 'low',
          rebufferImpact: 'low',
          overallScore: 0
        }
      };
    }
  },

  // Helper method to calculate health trend
  calculateHealthTrend(healthMetrics) {
    if (healthMetrics.length < 2) return 'stable';

    const recent = healthMetrics.slice(-10);
    const older = healthMetrics.slice(-20, -10);

    if (older.length === 0) return 'stable';

    const recentAvg = recent.reduce((sum, m) => sum + (m.healthScore || 0), 0) / recent.length;
    const olderAvg = older.reduce((sum, m) => sum + (m.healthScore || 0), 0) / older.length;

    const change = (recentAvg - olderAvg) / olderAvg;

    if (change > 0.1) return 'improving';
    if (change < -0.1) return 'degrading';
    return 'stable';
  },

  // Helper method to calculate quality stability
  calculateQualityStability(qualityChanges) {
    if (qualityChanges.length === 0) return 'stable';

    const recentChanges = qualityChanges.filter(change => {
      const changeTime = new Date(change.timestamp);
      const oneHourAgo = new Date(Date.now() - 60 * 60 * 1000);
      return changeTime > oneHourAgo;
    });

    if (recentChanges.length === 0) return 'stable';
    if (recentChanges.length <= 2) return 'minor-changes';
    return 'unstable';
  },

  // Helper method to calculate alert severity
  calculateAlertSeverity(alerts) {
    if (alerts.length === 0) return 'none';

    const criticalCount = alerts.filter(a => a.severity === 'CRITICAL').length;
    const highCount = alerts.filter(a => a.severity === 'HIGH').length;
    const mediumCount = alerts.filter(a => a.severity === 'MEDIUM').length;

    if (criticalCount > 0) return 'critical';
    if (highCount > 0) return 'high';
    if (mediumCount > 0) return 'medium';
    return 'low';
  },

  // Helper method to calculate rebuffer impact
  calculateRebufferImpact(rebufferingEvents) {
    if (rebufferingEvents.length === 0) return 'none';

    const recentRebuffers = rebufferingEvents.filter(event => {
      const eventTime = new Date(event.timestamp);
      const oneHourAgo = new Date(Date.now() - 60 * 60 * 1000);
      return eventTime > oneHourAgo;
    });

    if (recentRebuffers.length === 0) return 'none';
    if (recentRebuffers.length <= 5) return 'low';
    if (recentRebuffers.length <= 15) return 'medium';
    return 'high';
  },

  // Helper method to calculate overall health score
  calculateOverallHealthScore({ healthTrend, qualityStability, alertSeverity, rebufferImpact }) {
    let score = 100;

    // Health trend impact
    if (healthTrend === 'degrading') score -= 20;
    else if (healthTrend === 'improving') score += 10;

    // Quality stability impact
    if (qualityStability === 'unstable') score -= 15;
    else if (qualityStability === 'minor-changes') score -= 5;

    // Alert severity impact
    switch (alertSeverity) {
      case 'critical': score -= 30; break;
      case 'high': score -= 20; break;
      case 'medium': score -= 10; break;
      case 'low': score -= 5; break;
    }

    // Rebuffer impact
    switch (rebufferImpact) {
      case 'high': score -= 25; break;
      case 'medium': score -= 15; break;
      case 'low': score -= 5; break;
    }

    return Math.max(0, Math.min(100, score));
  }
};
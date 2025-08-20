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
  }
};
package resolvers

import (
	"context"
	"fmt"
	"time"

	"frameworks/api_gateway/graph/model"
	"frameworks/pkg/models"
)

// DoGetStreamAnalytics returns analytics for a specific stream
func (r *Resolver) DoGetStreamAnalytics(ctx context.Context, streamID string, timeRange *model.TimeRangeInput) (*models.StreamAnalytics, error) {
	// Extract tenant ID from context
	tenantID, ok := ctx.Value("tenant_id").(string)
	if !ok {
		return nil, fmt.Errorf("tenant context required")
	}

	// Convert time range to string format for Periscope client
	var startTime, endTime string
	if timeRange != nil {
		startTime = timeRange.Start.Format("2006-01-02T15:04:05Z")
		endTime = timeRange.End.Format("2006-01-02T15:04:05Z")
	}

	// Get analytics from Periscope Query
	analytics, err := r.Clients.Periscope.GetStreamAnalytics(ctx, tenantID, streamID, startTime, endTime)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get stream analytics")
		return nil, fmt.Errorf("failed to get stream analytics: %w", err)
	}

	// Return the first analytics result if available
	if len(*analytics) > 0 {
		return &(*analytics)[0], nil
	}
	return nil, fmt.Errorf("no analytics found for stream")
}

// DoGetViewerMetrics returns viewer metrics
func (r *Resolver) DoGetViewerMetrics(ctx context.Context, streamID *string, timeRange *model.TimeRangeInput) ([]*model.ViewerMetric, error) {
	// Extract tenant ID from context
	tenantID, ok := ctx.Value("tenant_id").(string)
	if !ok {
		return nil, fmt.Errorf("tenant context required")
	}

	// Convert time range for Periscope client
	var startTime, endTime *time.Time
	if timeRange != nil {
		startTime = &timeRange.Start
		endTime = &timeRange.End
	}

	// Determine stream context
	var internalName string
	if streamID != nil {
		internalName = *streamID
	}

	// Get viewer metrics from Periscope Query
	metrics, err := r.Clients.Periscope.GetViewerMetrics(ctx, tenantID, internalName, startTime, endTime)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get viewer metrics")
		return nil, fmt.Errorf("failed to get viewer metrics: %w", err)
	}

	// Convert to GraphQL models
	result := make([]*model.ViewerMetric, len(*metrics))
	for i, metric := range *metrics {
		result[i] = &model.ViewerMetric{
			Timestamp:   metric.Timestamp,
			ViewerCount: metric.ViewerCount,
		}
	}

	return result, nil
}

// DoGetPlatformOverview returns platform-wide metrics
func (r *Resolver) DoGetPlatformOverview(ctx context.Context, timeRange *model.TimeRangeInput) (*model.PlatformOverview, error) {
	// Extract tenant ID from context
	tenantID, ok := ctx.Value("tenant_id").(string)
	if !ok {
		return nil, fmt.Errorf("tenant context required")
	}

	// Convert time range to string format for Periscope client
	var startTime, endTime string
	if timeRange != nil {
		startTime = timeRange.Start.Format("2006-01-02T15:04:05Z")
		endTime = timeRange.End.Format("2006-01-02T15:04:05Z")
	}

	// Get platform overview from Periscope Query
	overview, err := r.Clients.Periscope.GetPlatformOverview(ctx, tenantID, startTime, endTime)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get platform overview")
		return nil, fmt.Errorf("failed to get platform overview: %w", err)
	}

	// Convert to GraphQL model
	var gqlTimeRange *model.TimeRange
	if timeRange != nil {
		gqlTimeRange = &model.TimeRange{
			Start: timeRange.Start,
			End:   timeRange.End,
		}
	}

	return &model.PlatformOverview{
		TotalStreams:   overview.TotalStreams,
		TotalViewers:   overview.TotalViewers,
		TotalBandwidth: overview.PeakBandwidth,
		TotalUsers:     overview.TotalUsers,
		TimeRange:      gqlTimeRange,
	}, nil
}

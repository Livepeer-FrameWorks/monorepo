package resolvers

import (
	"context"
	"fmt"
	"time"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/demo"
	"frameworks/api_gateway/internal/middleware"
	"frameworks/pkg/models"
)

// DoGetStreamAnalytics returns analytics for a specific stream
func (r *Resolver) DoGetStreamAnalytics(ctx context.Context, streamId string, timeRange *model.TimeRangeInput) (*models.StreamAnalytics, error) {
	// Check for demo mode
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo stream analytics data")
		return demo.GenerateStreamAnalytics(streamId), nil
	}

	// Extract tenant ID from context for data isolation
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

	// Get analytics from Periscope Query using tenant_id from JWT context
	analytics, err := r.Clients.Periscope.GetStreamAnalytics(ctx, tenantID, streamId, startTime, endTime)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get stream analytics")
		return nil, fmt.Errorf("failed to get stream analytics: %w", err)
	}

	// Return the first analytics result if available
	if len(*analytics) > 0 {
		return &(*analytics)[0], nil
	}
	// Return null instead of error when no analytics found - this is normal for new streams
	return nil, nil
}

// DoGetViewerMetrics returns viewer metrics
func (r *Resolver) DoGetViewerMetrics(ctx context.Context, streamId *string, timeRange *model.TimeRangeInput) ([]*model.ViewerMetric, error) {
	// Check for demo mode
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo viewer metrics data")
		return demo.GenerateViewerMetrics(), nil
	}

	// Extract tenant ID from context for data isolation
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
	if streamId != nil {
		internalName = *streamId
	}

	// Get viewer metrics from Periscope Query using tenant_id from JWT context
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
	// Check for demo mode
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo platform overview data")
		return demo.GeneratePlatformOverview(), nil
	}

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

	// Convert to GraphQL model - always provide a TimeRange (default to 24h if none provided)
	var gqlTimeRange *model.TimeRange
	if timeRange != nil {
		gqlTimeRange = &model.TimeRange{
			Start: timeRange.Start,
			End:   timeRange.End,
		}
	} else {
		// Default to last 24 hours if no time range provided
		now := time.Now()
		gqlTimeRange = &model.TimeRange{
			Start: now.Add(-24 * time.Hour),
			End:   now,
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

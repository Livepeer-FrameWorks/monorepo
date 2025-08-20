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

// DoGetStreamHealthMetrics returns stream health metrics
func (r *Resolver) DoGetStreamHealthMetrics(ctx context.Context, streamId string, timeRange *model.TimeRangeInput) ([]*model.StreamHealthMetric, error) {
	// Check for demo mode
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo stream health metrics data")
		return demo.GenerateStreamHealthMetrics(), nil
	}

	// Convert time range for Periscope client
	var startTime, endTime *time.Time
	if timeRange != nil {
		startTime = &timeRange.Start
		endTime = &timeRange.End
	}

	// Get health metrics from Periscope Query
	metrics, err := r.Clients.Periscope.GetStreamHealthMetrics(ctx, startTime, endTime)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get stream health metrics")
		return nil, fmt.Errorf("failed to get stream health metrics: %w", err)
	}

	// Convert to GraphQL models and filter by stream
	var result []*model.StreamHealthMetric
	for _, metric := range *metrics {
		// Filter by stream ID if metrics don't already filter
		if streamId == "" || metric.InternalName == streamId {
			// Calculate derived fields
			packetLossPercentage := float64(0)
			if metric.PacketsSent > 0 {
				packetLossPercentage = float64(metric.PacketsLost) / float64(metric.PacketsSent) * 100.0
			}

			// Calculate health score from various metrics
			healthScore := float64(metric.BufferHealth * 0.5) // Base from buffer health
			if metric.FPS > 0 {
				healthScore += 0.3 // Add for good FPS
			}
			if packetLossPercentage < 1.0 {
				healthScore += 0.2 // Add for low packet loss
			}

			result = append(result, &model.StreamHealthMetric{
				Timestamp:           metric.Timestamp,
				Stream:              metric.InternalName,
				NodeID:              metric.NodeID,
				HealthScore:         healthScore,
				FrameJitterMs:       func(f float32) *float64 { v := float64(f * 16.67); return &v }(metric.FPS), // Approximate from FPS
				KeyframeStabilityMs: func(g int) *float64 { v := float64(g * 33); return &v }(metric.GOPSize),    // From GOP size
				IssuesDescription: func() *string {
					s := ""
					if healthScore < 0.8 {
						s = "Performance degradation detected"
					}
					return &s
				}(),
				HasIssues:            healthScore < 0.8,
				Bitrate:              &metric.Bitrate,
				Fps:                  func(f float32) *float64 { v := float64(f); return &v }(metric.FPS),
				Width:                &metric.Width,
				Height:               &metric.Height,
				Codec:                &metric.Codec,
				QualityTier:          func() *string { tier := fmt.Sprintf("%dx%d", metric.Width, metric.Height); return &tier }(),
				PacketsSent:          func(p int64) *int { v := int(p); return &v }(metric.PacketsSent),
				PacketsLost:          func(p int64) *int { v := int(p); return &v }(metric.PacketsLost),
				PacketLossPercentage: &packetLossPercentage,
				BufferState: func() model.BufferState {
					if metric.BufferHealth > 0.9 {
						return model.BufferStateFull
					}
					if metric.BufferHealth > 0.5 {
						return model.BufferStateEmpty
					}
					if metric.BufferHealth > 0.1 {
						return model.BufferStateDry
					}
					return model.BufferStateRecover
				}(),
				BufferHealth:    func(b float32) *float64 { v := float64(b); return &v }(metric.BufferHealth),
				AudioChannels:   func() *int { v := 2; return &v }(),        // Default stereo
				AudioSampleRate: func() *int { v := 44100; return &v }(),    // Default sample rate
				AudioCodec:      func() *string { s := "AAC"; return &s }(), // Default audio codec
				AudioBitrate:    func() *int { v := 128; return &v }(),      // Default audio bitrate
			})
		}
	}

	return result, nil
}

// DoGetCurrentStreamHealth returns current health for a stream
func (r *Resolver) DoGetCurrentStreamHealth(ctx context.Context, streamId string) (*model.StreamHealthMetric, error) {
	// Get recent health metrics (last 5 minutes)
	now := time.Now()
	startTime := now.Add(-5 * time.Minute)
	timeRange := &model.TimeRangeInput{
		Start: startTime,
		End:   now,
	}

	// Get health metrics
	metrics, err := r.DoGetStreamHealthMetrics(ctx, streamId, timeRange)
	if err != nil {
		return nil, err
	}

	// Return the most recent metric
	if len(metrics) > 0 {
		return metrics[len(metrics)-1], nil
	}

	return nil, nil
}

// DoGetStreamQualityChanges returns quality change events for a stream
func (r *Resolver) DoGetStreamQualityChanges(ctx context.Context, streamId string, timeRange *model.TimeRangeInput) ([]*model.StreamQualityChange, error) {
	// Check for demo mode
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo stream quality changes data")
		return demo.GenerateStreamQualityChanges(), nil
	}

	// Convert time range for Periscope client
	var startTime, endTime *time.Time
	if timeRange != nil {
		startTime = &timeRange.Start
		endTime = &timeRange.End
	}

	// For now, get track list events as a proxy for quality changes
	// TODO: Add dedicated GetStreamQualityChanges method to Periscope client
	tracks, err := r.Clients.Periscope.GetTrackListEvents(ctx, streamId, startTime, endTime)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get track list events")
		return nil, fmt.Errorf("failed to get quality changes: %w", err)
	}

	// Convert to quality changes (simplified for now)
	var result []*model.StreamQualityChange
	for _, track := range *tracks {
		result = append(result, &model.StreamQualityChange{
			Timestamp:      track.Timestamp,
			Stream:         streamId,
			NodeID:         track.NodeID,
			ChangeType:     model.QualityChangeTypeTrackUpdate,
			NewTracks:      &track.TrackList,
			NewQualityTier: func() *string { tier := fmt.Sprintf("Track %d", track.TrackCount); return &tier }(),
		})
	}

	return result, nil
}

// DoGetStreamHealthAlerts returns health alerts for streams
func (r *Resolver) DoGetStreamHealthAlerts(ctx context.Context, streamId *string, timeRange *model.TimeRangeInput) ([]*model.StreamHealthAlert, error) {
	// Check for demo mode
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo stream health alerts data")
		return demo.GenerateStreamHealthAlerts(), nil
	}

	// Get health metrics and derive alerts from them
	var targetStream string
	if streamId != nil {
		targetStream = *streamId
	}

	metrics, err := r.DoGetStreamHealthMetrics(ctx, targetStream, timeRange)
	if err != nil {
		return nil, err
	}

	// Convert health metrics to alerts based on thresholds
	var result []*model.StreamHealthAlert
	for _, metric := range metrics {
		if metric.HasIssues || (metric.FrameJitterMs != nil && *metric.FrameJitterMs > 30) ||
			(metric.PacketLossPercentage != nil && *metric.PacketLossPercentage > 2.0) {

			// Determine alert type and severity
			alertType := model.AlertTypeQualityDegradation
			severity := model.AlertSeverityMedium

			if metric.FrameJitterMs != nil && *metric.FrameJitterMs > 50 {
				alertType = model.AlertTypeHighJitter
			}
			if metric.PacketLossPercentage != nil && *metric.PacketLossPercentage > 5.0 {
				alertType = model.AlertTypePacketLoss
				severity = model.AlertSeverityHigh
			}
			if metric.BufferState == model.BufferStateDry {
				alertType = model.AlertTypeRebuffering
				severity = model.AlertSeverityHigh
			}

			result = append(result, &model.StreamHealthAlert{
				Timestamp:            metric.Timestamp,
				Stream:               metric.Stream,
				NodeID:               metric.NodeID,
				AlertType:            alertType,
				Severity:             severity,
				HealthScore:          &metric.HealthScore,
				FrameJitterMs:        metric.FrameJitterMs,
				PacketLossPercentage: metric.PacketLossPercentage,
				IssuesDescription:    metric.IssuesDescription,
				BufferState:          &metric.BufferState,
				QualityTier:          metric.QualityTier,
			})
		}
	}

	return result, nil
}

// DoGetRebufferingEvents returns rebuffering events for a stream
func (r *Resolver) DoGetRebufferingEvents(ctx context.Context, streamId string, timeRange *model.TimeRangeInput) ([]*model.RebufferingEvent, error) {
	// Check for demo mode
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo rebuffering events data")
		return demo.GenerateRebufferingEvents(), nil
	}

	// Convert time range for Periscope client
	var startTime, endTime *time.Time
	if timeRange != nil {
		startTime = &timeRange.Start
		endTime = &timeRange.End
	}

	// Get buffer events as proxy for rebuffering
	bufferEvents, err := r.Clients.Periscope.GetStreamBufferEvents(ctx, streamId, startTime, endTime)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get buffer events")
		return nil, fmt.Errorf("failed to get rebuffering events: %w", err)
	}

	// Convert buffer events to rebuffering events
	var result []*model.RebufferingEvent
	var prevState model.BufferState = model.BufferStateFull

	for _, event := range *bufferEvents {
		// Parse buffer state from event status or data
		bufferState := model.BufferStateEmpty // Default
		if event.Status == "FULL" {
			bufferState = model.BufferStateFull
		} else if event.Status == "DRY" {
			bufferState = model.BufferStateDry
		} else if event.Status == "RECOVER" {
			bufferState = model.BufferStateRecover
		}

		// Detect rebuffer start (transition from FULL to DRY)
		rebufferStart := (prevState == model.BufferStateFull && bufferState == model.BufferStateDry)
		rebufferEnd := (prevState == model.BufferStateDry && bufferState == model.BufferStateRecover)

		if rebufferStart || rebufferEnd {
			result = append(result, &model.RebufferingEvent{
				Timestamp:            event.Timestamp,
				Stream:               streamId,
				NodeID:               event.NodeID,
				BufferState:          bufferState,
				PreviousState:        prevState,
				RebufferStart:        rebufferStart,
				RebufferEnd:          rebufferEnd,
				HealthScore:          func() *float64 { f := 0.5; return &f }(),  // Default health score
				FrameJitterMs:        func() *float64 { f := 10.0; return &f }(), // Default jitter
				PacketLossPercentage: func() *float64 { f := 1.0; return &f }(),  // Default packet loss
			})
		}

		prevState = bufferState
	}

	return result, nil
}

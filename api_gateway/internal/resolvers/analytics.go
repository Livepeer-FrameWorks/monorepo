package resolvers

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/demo"
	"frameworks/api_gateway/internal/middleware"
	"frameworks/pkg/api/periscope"
	"frameworks/pkg/models"

	"github.com/sirupsen/logrus"
)

type streamBufferPayload struct {
	HealthScore *float64                   `json:"health_score"`
	Tracks      []streamBufferTrackPayload `json:"tracks"`
}

type streamBufferTrackPayload struct {
	TrackType     string   `json:"track_type"`
	FrameJitterMs *float64 `json:"frame_jitter_ms"`
	Jitter        *float64 `json:"jitter"`
}

// DoGetStreamAnalytics returns analytics for a specific stream
func (r *Resolver) DoGetStreamAnalytics(ctx context.Context, streamId string, timeRange *model.TimeRangeInput) (*models.StreamAnalytics, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning synthetic stream analytics")
		return demo.GenerateStreamAnalytics(streamId), nil
	}

	// Extract tenant ID from context for data isolation
	tenantID, ok := ctx.Value("tenant_id").(string)
	if !ok {
		return nil, fmt.Errorf("tenant context required")
	}

	// Get analytics from Periscope Query using tenant_id from JWT context
	var startStr, endStr string
	if timeRange != nil {
		startStr = timeRange.Start.Format("2006-01-02T15:04:05Z")
		endStr = timeRange.End.Format("2006-01-02T15:04:05Z")
	}
	analytics, err := r.Clients.Periscope.GetStreamAnalytics(ctx, tenantID, streamId, startStr, endStr)
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
func (r *Resolver) DoGetViewerMetrics(ctx context.Context, stream *string, timeRange *model.TimeRangeInput) ([]*models.AnalyticsViewerSession, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning synthetic viewer metrics")
		demoMetrics := demo.GenerateViewerMetrics()
		out := make([]*models.AnalyticsViewerSession, 0, len(demoMetrics))
		for _, dm := range demoMetrics {
			out = append(out, &models.AnalyticsViewerSession{
				Timestamp:   dm.Timestamp,
				ViewerCount: dm.ViewerCount,
			})
		}
		return out, nil
	}

	// Extract tenant ID from context for data isolation
	tenantID, ok := ctx.Value("tenant_id").(string)
	if !ok {
		return nil, fmt.Errorf("tenant context required")
	}

	// Determine stream context
	var internalName string
	if stream != nil {
		internalName = *stream
	}

	// Get viewer metrics from Periscope Query using tenant_id from JWT context
	var startTime, endTime *time.Time
	if timeRange != nil {
		var err error
		startTime, endTime, err = r.normalizeTimeRange(TimeRangeParams{Start: &timeRange.Start, End: &timeRange.End, MaxWindow: 31 * 24 * time.Hour, DefaultWindow: 24 * time.Hour})
		if err != nil {
			return nil, fmt.Errorf("invalid time range: %w", err)
		}
	} else {
		var err error
		startTime, endTime, err = r.normalizeTimeRange(TimeRangeParams{})
		if err != nil {
			return nil, fmt.Errorf("invalid time range: %w", err)
		}
	}
	metrics, err := r.Clients.Periscope.GetViewerMetrics(ctx, tenantID, internalName, startTime, endTime)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get viewer metrics")
		return nil, fmt.Errorf("failed to get viewer metrics: %w", err)
	}

	// Convert to slice of pointers for binding
	result := make([]*models.AnalyticsViewerSession, 0, len(*metrics))
	for i := range *metrics {
		result = append(result, &(*metrics)[i])
	}

	return result, nil
}

// DoGetPlatformOverview returns platform-wide metrics
func (r *Resolver) DoGetPlatformOverview(ctx context.Context, timeRange *model.TimeRangeInput) (*periscope.PlatformOverviewResponse, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning synthetic platform overview")
		return demo.GeneratePlatformOverview(), nil
	}

	// Extract tenant ID from context
	tenantID, ok := ctx.Value("tenant_id").(string)
	if !ok {
		return nil, fmt.Errorf("tenant context required")
	}

	// Get platform overview from Periscope Query
	var startStr2, endStr2 string
	if timeRange != nil {
		startStr2 = timeRange.Start.Format("2006-01-02T15:04:05Z")
		endStr2 = timeRange.End.Format("2006-01-02T15:04:05Z")
	}
	overview, err := r.Clients.Periscope.GetPlatformOverview(ctx, tenantID, startStr2, endStr2)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get platform overview")
		return nil, fmt.Errorf("failed to get platform overview: %w", err)
	}

	return overview, nil
}

// DoGetStreamHealthMetrics returns stream health metrics
func (r *Resolver) DoGetStreamHealthMetrics(ctx context.Context, streamId string, timeRange *model.TimeRangeInput) ([]*periscope.StreamHealthMetric, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning synthetic stream health metrics")
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

	// Filter by streamId if provided
	var result []*periscope.StreamHealthMetric
	for i := range *metrics {
		m := &(*metrics)[i]
		if streamId == "" || m.InternalName == streamId {
			result = append(result, m)
		}
	}

	return result, nil
}

// DoGetCurrentStreamHealth returns current health for a stream
func (r *Resolver) DoGetCurrentStreamHealth(ctx context.Context, streamId string) (*periscope.StreamHealthMetric, error) {
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

// DoGetTrackListEvents returns track list updates for a stream
func (r *Resolver) DoGetTrackListEvents(ctx context.Context, streamID string, timeRange *model.TimeRangeInput) ([]*periscope.AnalyticsTrackListEvent, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning synthetic track list events")
		return demo.GenerateTrackListEvents(), nil
	}

	var startTime, endTime *time.Time
	if timeRange != nil {
		startTime = &timeRange.Start
		endTime = &timeRange.End
	}

	events, err := r.Clients.Periscope.GetTrackListEvents(ctx, streamID, startTime, endTime)
	if err != nil {
		r.Logger.WithError(err).
			WithField("stream", streamID).
			Error("Failed to get track list events")
		return nil, fmt.Errorf("failed to get track list events: %w", err)
	}

	if events == nil || len(*events) == 0 {
		return []*periscope.AnalyticsTrackListEvent{}, nil
	}

	result := make([]*periscope.AnalyticsTrackListEvent, len(*events))
	for i := range *events {
		result[i] = &((*events)[i])
	}

	return result, nil
}

// DoGetStreamHealthAlerts returns health alerts for streams
func (r *Resolver) DoGetStreamHealthAlerts(ctx context.Context, streamId *string, timeRange *model.TimeRangeInput) ([]*model.StreamHealthAlert, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning synthetic stream health alerts")
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

	// Convert health metrics to alerts based on thresholds now using Periscope-computed fields
	var result []*model.StreamHealthAlert
	for _, m := range metrics {
		// Map buffer state string to enum
		var bs model.BufferState
		switch strings.ToUpper(m.BufferState) {
		case "FULL":
			bs = model.BufferStateFull
		case "EMPTY":
			bs = model.BufferStateEmpty
		case "DRY":
			bs = model.BufferStateDry
		default:
			bs = model.BufferStateRecover
		}
		bsPtr := bs

		// Map alert type and severity heuristically from computed fields
		alertType := model.AlertTypeQualityDegradation
		severity := model.AlertSeverityMedium
		if m.FrameJitterMs != nil && *m.FrameJitterMs > 50 {
			alertType = model.AlertTypeHighJitter
		}
		if m.PacketLossPercentage != nil && *m.PacketLossPercentage > 5.0 {
			alertType = model.AlertTypePacketLoss
			severity = model.AlertSeverityHigh
		}
		if strings.ToUpper(m.BufferState) == "DRY" {
			alertType = model.AlertTypeRebuffering
			severity = model.AlertSeverityHigh
		}

		// Convert health score to float64 pointer
		hs := float64(m.HealthScore)

		result = append(result, &model.StreamHealthAlert{
			Timestamp:            m.Timestamp,
			Stream:               m.InternalName,
			NodeID:               m.NodeID,
			AlertType:            alertType,
			Severity:             severity,
			HealthScore:          &hs,
			FrameJitterMs:        m.FrameJitterMs,
			PacketLossPercentage: m.PacketLossPercentage,
			IssuesDescription:    m.IssuesDescription,
			BufferState:          &bsPtr,
			QualityTier:          m.QualityTier,
		})
	}

	return result, nil
}

// DoGetRebufferingEvents returns rebuffering events for a stream
func (r *Resolver) DoGetRebufferingEvents(ctx context.Context, streamId string, timeRange *model.TimeRangeInput) ([]*model.RebufferingEvent, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning synthetic rebuffering events")
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

		var healthScore *float64
		var frameJitter *float64
		if payload := parseStreamBufferPayload(event); payload != nil {
			healthScore = normalizeHealthScore(payload.HealthScore)
			frameJitter = extractMaxJitter(payload.Tracks)
		}

		if rebufferStart || rebufferEnd {
			result = append(result, &model.RebufferingEvent{
				Timestamp:     event.Timestamp,
				Stream:        streamId,
				NodeID:        event.NodeID,
				BufferState:   bufferState,
				PreviousState: prevState,
				RebufferStart: rebufferStart,
				RebufferEnd:   rebufferEnd,
				HealthScore:   healthScore,
				FrameJitterMs: frameJitter,
			})
		}

		prevState = bufferState
	}

	return result, nil
}

// DoGetViewerGeographics returns geographic data for individual viewer/connection events
func (r *Resolver) DoGetViewerGeographics(ctx context.Context, stream *string, timeRange *model.TimeRangeInput) ([]*periscope.ConnectionEvent, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning synthetic viewer geographics")
		return demo.GenerateViewerGeographics(), nil
	}

	// Get geographic data from Periscope Query
	var sTime, eTime *time.Time
	if timeRange != nil {
		var err error
		sTime, eTime, err = r.normalizeTimeRange(TimeRangeParams{Start: &timeRange.Start, End: &timeRange.End})
		if err != nil {
			return []*periscope.ConnectionEvent{}, nil
		}
	} else {
		var err error
		sTime, eTime, err = r.normalizeTimeRange(TimeRangeParams{})
		if err != nil {
			return []*periscope.ConnectionEvent{}, nil
		}
	}
	connResp, err := r.Clients.Periscope.GetConnectionEvents(ctx, sTime, eTime)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get connection events for geographics")
		return []*periscope.ConnectionEvent{}, nil
	}

	var out []*periscope.ConnectionEvent
	for i := range *connResp {
		ev := &(*connResp)[i]
		if stream != nil && *stream != "" && ev.InternalName != *stream {
			continue
		}
		out = append(out, ev)
	}
	return out, nil
}

func parseStreamBufferPayload(event periscope.BufferEvent) *streamBufferPayload {
	if payload := decodeStreamBufferPayload(event.EventData); payload != nil {
		return payload
	}
	if event.EventPayload != nil {
		if encoded, err := json.Marshal(event.EventPayload); err == nil {
			return decodeStreamBufferPayload(string(encoded))
		}
	}
	return nil
}

func decodeStreamBufferPayload(raw string) *streamBufferPayload {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var payload streamBufferPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil
	}
	return &payload
}

func normalizeHealthScore(raw *float64) *float64 {
	if raw == nil {
		return nil
	}
	normalized := *raw
	if normalized > 1.0 {
		normalized = normalized / 100.0
	}
	if normalized < 0 {
		normalized = 0
	}
	if normalized > 1 {
		normalized = 1
	}
	return &normalized
}

func extractMaxJitter(tracks []streamBufferTrackPayload) *float64 {
	var bestVideo float64
	var haveVideo bool
	var bestAny float64
	var haveAny bool
	for _, track := range tracks {
		candidate, ok := jitterFromTrack(track)
		if !ok {
			continue
		}
		if strings.EqualFold(track.TrackType, "video") {
			if !haveVideo || candidate > bestVideo {
				haveVideo = true
				bestVideo = candidate
			}
		}
		if !haveAny || candidate > bestAny {
			haveAny = true
			bestAny = candidate
		}
	}
	if haveVideo {
		return &bestVideo
	}
	if haveAny {
		return &bestAny
	}
	return nil
}

func jitterFromTrack(track streamBufferTrackPayload) (float64, bool) {
	if track.FrameJitterMs != nil {
		return *track.FrameJitterMs, true
	}
	if track.Jitter != nil {
		return *track.Jitter, true
	}
	return 0, false
}

// DoGetGeographicDistribution returns aggregated geographic distribution analytics
func (r *Resolver) DoGetGeographicDistribution(ctx context.Context, stream *string, timeRange *model.TimeRangeInput) (*model.GeographicDistribution, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning synthetic geographic distribution")
		return demo.GenerateGeographicDistribution(), nil
	}

	// Extract tenant ID from context for data isolation
	tenantID, ok := ctx.Value("tenant_id").(string)
	if !ok {
		return nil, fmt.Errorf("tenant context required")
	}

	var streamID string
	if stream != nil {
		streamID = *stream
	}

	var sTime, eTime *time.Time
	if timeRange != nil {
		sTime = &timeRange.Start
		eTime = &timeRange.End
	}
	vmResp, err := r.Clients.Periscope.GetViewerMetrics(ctx, tenantID, streamID, sTime, eTime)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get viewer metrics for geographic distribution")
		return nil, fmt.Errorf("failed to get geographic distribution: %w", err)
	}

	// Aggregate counts
	countryCounts := map[string]int{}
	cityCounts := map[string]struct {
		country  string
		count    int
		lat, lon float64
	}{}
	viewersByCountry := []*model.CountryTimeSeries{}
	uniqCountries := map[string]struct{}{}
	totalViewers := 0

	for _, m := range *vmResp {
		if stream != nil && *stream != "" && m.InternalName != *stream {
			continue
		}
		if m.CountryCode != "" {
			countryCounts[m.CountryCode] += m.ViewerCount
			uniqCountries[m.CountryCode] = struct{}{}
			viewersByCountry = append(viewersByCountry, &model.CountryTimeSeries{
				Timestamp:   m.Timestamp,
				CountryCode: m.CountryCode,
				ViewerCount: m.ViewerCount,
			})
		}
		if m.City != "" {
			k := m.City + "|" + m.CountryCode
			entry := cityCounts[k]
			entry.country = m.CountryCode
			entry.count += m.ViewerCount
			entry.lat = m.Latitude
			entry.lon = m.Longitude
			cityCounts[k] = entry
		}
		totalViewers += m.ViewerCount
	}

	// Build top lists with percentages
	var topCountries []*model.CountryMetric
	for cc, cnt := range countryCounts {
		perc := 0.0
		if totalViewers > 0 {
			perc = (float64(cnt) / float64(totalViewers)) * 100.0
		}
		topCountries = append(topCountries, &model.CountryMetric{CountryCode: cc, ViewerCount: cnt, Percentage: perc})
	}
	var topCities []*model.CityMetric
	for key, v := range cityCounts {
		parts := strings.SplitN(key, "|", 2)
		city := parts[0]
		perc := 0.0
		if totalViewers > 0 {
			perc = (float64(v.count) / float64(totalViewers)) * 100.0
		}
		topCities = append(topCities, &model.CityMetric{City: city, CountryCode: &v.country, ViewerCount: v.count, Latitude: &v.lat, Longitude: &v.lon, Percentage: perc})
	}

	tr := &model.TimeRange{
		Start: func() time.Time {
			if timeRange != nil {
				return timeRange.Start
			}
			return time.Now().Add(-24 * time.Hour)
		}(),
		End: func() time.Time {
			if timeRange != nil {
				return timeRange.End
			}
			return time.Now()
		}(),
	}

	return &model.GeographicDistribution{
		TimeRange:        tr,
		Stream:           stream,
		TopCountries:     topCountries,
		TopCities:        topCities,
		UniqueCountries:  len(uniqCountries),
		UniqueCities:     len(topCities),
		TotalViewers:     totalViewers,
		ViewersByCountry: viewersByCountry,
	}, nil
}

// DoGetLoadBalancingMetrics returns load balancing and routing metrics with geographic context
func (r *Resolver) DoGetLoadBalancingMetrics(ctx context.Context, timeRange *model.TimeRangeInput) ([]*model.LoadBalancingMetric, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning synthetic load balancing metrics")
		return demo.GenerateLoadBalancingMetrics(), nil
	}

	// Extract tenant ID from context for data isolation
	tenantID, ok := ctx.Value("tenant_id").(string)
	if !ok {
		return nil, fmt.Errorf("tenant context required")
	}

	var startTime, endTime *time.Time
	if timeRange != nil {
		startTime = &timeRange.Start
		endTime = &timeRange.End
	}

	// Fetch routing events from Periscope
	events, err := r.Clients.Periscope.GetRoutingEvents(ctx, startTime, endTime)
	if err != nil {
		r.Logger.WithError(err).WithFields(logrus.Fields{
			"tenant_id": tenantID,
		}).Error("Failed to get routing events")
		return nil, fmt.Errorf("failed to get load balancing metrics: %w", err)
	}

	// Map to GraphQL LoadBalancingMetric
	var out []*model.LoadBalancingMetric
	for i := range *events {
		e := &(*events)[i]

		metric := &model.LoadBalancingMetric{
			Timestamp:    e.Timestamp,
			Stream:       e.StreamName,
			SelectedNode: e.SelectedNode,
			Status:       e.Status,
		}

		// Optional fields
		if e.Details != "" {
			metric.Details = &e.Details
		}
		if e.ClientIP != "" {
			metric.ClientIP = &e.ClientIP
		}
		if e.ClientCountry != "" {
			metric.ClientCountry = &e.ClientCountry
		}
		// Use SelectedNode as NodeID when explicit ID is not provided separately
		if e.SelectedNode != "" {
			metric.NodeID = &e.SelectedNode
		}
		// Coordinates
		clientLat := e.ClientLatitude
		clientLon := e.ClientLongitude
		nodeLat := e.NodeLatitude
		nodeLon := e.NodeLongitude
		metric.ClientLatitude = &clientLat
		metric.ClientLongitude = &clientLon
		metric.NodeLatitude = &nodeLat
		metric.NodeLongitude = &nodeLon
		if e.NodeName != "" {
			metric.NodeName = &e.NodeName
		}
		if e.Score != 0 {
			s := e.Score
			metric.Score = &s
		}

		// Compute routing distance if both coordinate pairs look valid
		if !isZeroCoord(clientLat, clientLon) && !isZeroCoord(nodeLat, nodeLon) {
			d := haversineKm(clientLat, clientLon, nodeLat, nodeLon)
			metric.RoutingDistance = &d
		}

		out = append(out, metric)
	}

	return out, nil
}

func isZeroCoord(lat, lon float64) bool {
	return lat == 0 && lon == 0
}

func haversineKm(lat1, lon1, lat2, lon2 float64) float64 {
	const earthRadiusKm = 6371.0
	toRad := func(deg float64) float64 { return deg * math.Pi / 180 }
	lat1Rad := toRad(lat1)
	lon1Rad := toRad(lon1)
	lat2Rad := toRad(lat2)
	lon2Rad := toRad(lon2)
	dLat := lat2Rad - lat1Rad
	dLon := lon2Rad - lon1Rad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(lat1Rad)*math.Cos(lat2Rad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
	return earthRadiusKm * c
}

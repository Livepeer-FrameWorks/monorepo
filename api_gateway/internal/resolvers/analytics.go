package resolvers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/datafetcher"
	"frameworks/api_gateway/internal/demo"
	"frameworks/api_gateway/internal/middleware"
	periscopeclient "frameworks/pkg/clients/periscope"
	"frameworks/pkg/ctxkeys"
	pb "frameworks/pkg/proto"

	"github.com/sirupsen/logrus"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func toTimeRangeOpts(timeRange *model.TimeRangeInput) *periscopeclient.TimeRangeOpts {
	if timeRange == nil {
		return nil
	}
	return &periscopeclient.TimeRangeOpts{
		StartTime: timeRange.Start,
		EndTime:   timeRange.End,
	}
}

func timePtrsToTimeRangeOpts(startTime, endTime *time.Time) *periscopeclient.TimeRangeOpts {
	if startTime == nil || endTime == nil {
		return nil
	}
	return &periscopeclient.TimeRangeOpts{
		StartTime: *startTime,
		EndTime:   *endTime,
	}
}

// DoGetStreamAnalyticsSummary returns MV-backed range aggregates for a stream.
func (r *Resolver) DoGetStreamAnalyticsSummary(ctx context.Context, streamID string, timeRange *model.TimeRangeInput) (*pb.StreamAnalyticsSummary, error) {
	if err := middleware.RequirePermission(ctx, "analytics:read"); err != nil {
		return nil, err
	}
	normalizedID, err := normalizeStreamID(streamID)
	if err != nil {
		return nil, err
	}
	streamID = normalizedID

	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning synthetic stream analytics summary")
		return demo.GenerateStreamAnalyticsSummary(streamID), nil
	}

	// Extract tenant ID from context for data isolation
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	cacheKey := tenantID + ":" + streamID
	if timeRange != nil {
		cacheKey += ":" + timeRange.Start.Format(time.RFC3339) + ":" + timeRange.End.Format(time.RFC3339)
	}

	val, err := r.fetchPeriscope(ctx, "stream_analytics_summary", []string{cacheKey}, func(ctx context.Context) (interface{}, error) {
		return r.Clients.Periscope.GetStreamAnalyticsSummary(ctx, tenantID, streamID, toTimeRangeOpts(timeRange))
	})
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get stream analytics summary")
		return nil, fmt.Errorf("failed to get stream analytics summary: %w", err)
	}
	resp, ok := val.(*pb.GetStreamAnalyticsSummaryResponse)
	if !ok {
		return nil, fmt.Errorf("unexpected response type for stream analytics summary: %T", val)
	}
	if resp == nil {
		return nil, nil
	}

	return resp.GetSummary(), nil
}

// DoGetPlatformOverview returns platform-wide metrics
func (r *Resolver) DoGetPlatformOverview(ctx context.Context, timeRange *model.TimeRangeInput) (*pb.GetPlatformOverviewResponse, error) {
	if err := middleware.RequirePermission(ctx, "analytics:read"); err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning synthetic platform overview")
		return demo.GeneratePlatformOverview(), nil
	}

	// Extract tenant ID from context
	// Extract tenant ID from context for data isolation
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	// Get platform overview from Periscope Query
	tr := toTimeRangeOpts(timeRange)
	cacheKey := tenantID
	if timeRange != nil {
		cacheKey += ":" + timeRange.Start.Format(time.RFC3339) + ":" + timeRange.End.Format(time.RFC3339)
	}
	val, err := r.fetchPeriscope(ctx, "platform_overview", []string{cacheKey}, func(ctx context.Context) (interface{}, error) {
		return r.Clients.Periscope.GetPlatformOverview(ctx, tenantID, tr)
	})
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get platform overview")
		return nil, fmt.Errorf("failed to get platform overview: %w", err)
	}
	resp, ok := val.(*pb.GetPlatformOverviewResponse)
	if !ok {
		return nil, fmt.Errorf("unexpected response type for platform overview: %T", val)
	}
	return resp, nil
}

// DoGetViewerCountTimeSeries returns time-bucketed viewer counts for charts
// interval should be "5m", "15m", "1h", or "1d"
func (r *Resolver) DoGetViewerCountTimeSeries(ctx context.Context, stream *string, timeRange *model.TimeRangeInput, interval *string) ([]*pb.ViewerCountBucket, error) {
	if err := middleware.RequirePermission(ctx, "analytics:read"); err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning synthetic viewer count time series")
		return demo.GenerateViewerCountTimeSeries(), nil
	}

	normalizedStream, err := normalizeStreamIDPtr(stream)
	if err != nil {
		return nil, err
	}
	stream = normalizedStream

	// Extract tenant ID from context for data isolation
	// Extract tenant ID from context for data isolation
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	// Default interval to 5m if not specified
	intervalVal := "5m"
	if interval != nil && *interval != "" {
		intervalVal = *interval
	}

	// Build cache key
	streamKey := ""
	if stream != nil {
		streamKey = *stream
	}
	cacheKey := tenantID + ":" + streamKey + ":" + intervalVal
	if timeRange != nil {
		cacheKey += ":" + timeRange.Start.Format(time.RFC3339) + ":" + timeRange.End.Format(time.RFC3339)
	}

	// Get viewer count time series from Periscope
	val, err := r.fetchPeriscope(ctx, "viewer_count_timeseries", []string{cacheKey}, func(ctx context.Context) (interface{}, error) {
		return r.Clients.Periscope.GetViewerCountTimeSeries(ctx, tenantID, stream, toTimeRangeOpts(timeRange), intervalVal)
	})
	if err != nil {
		r.Logger.WithError(err).WithFields(logrus.Fields{
			"tenant_id": tenantID,
			"stream":    streamKey,
			"interval":  intervalVal,
		}).Error("Failed to get viewer count time series")
		return nil, fmt.Errorf("failed to get viewer count time series: %w", err)
	}
	resp, ok := val.(*pb.GetViewerCountTimeSeriesResponse)
	if !ok {
		return nil, fmt.Errorf("unexpected response type for viewer count time series: %T", val)
	}

	return resp.Buckets, nil
}

// DoGetStreamHealthMetrics returns stream health metrics
func (r *Resolver) DoGetStreamHealthMetrics(ctx context.Context, streamId string, timeRange *model.TimeRangeInput) ([]*pb.StreamHealthMetric, error) {
	if err := middleware.RequirePermission(ctx, "analytics:read"); err != nil {
		return nil, err
	}
	normalizedID, err := normalizeStreamID(streamId)
	if err != nil {
		return nil, err
	}
	streamId = normalizedID

	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning synthetic stream health metrics")
		return demo.GenerateStreamHealthMetrics(), nil
	}

	tenantID := tenantIDFromContext(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	// Convert time range for Periscope client
	tr := toTimeRangeOpts(timeRange)
	var streamID *string
	if streamId != "" {
		streamID = &streamId
	}

	// Get health metrics from Periscope Query
	cacheKey := tenantID + ":all"
	if streamId != "" {
		cacheKey = tenantID + ":" + streamId
	}
	if timeRange != nil {
		cacheKey += ":" + timeRange.Start.Format(time.RFC3339) + ":" + timeRange.End.Format(time.RFC3339)
	}
	val, err := r.fetchPeriscope(ctx, "stream_health", []string{cacheKey}, func(ctx context.Context) (interface{}, error) {
		return r.Clients.Periscope.GetStreamHealthMetrics(ctx, tenantID, streamID, tr, nil)
	})
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get stream health metrics")
		return nil, fmt.Errorf("failed to get stream health metrics: %w", err)
	}
	resp, ok := val.(*pb.GetStreamHealthMetricsResponse)
	if !ok {
		return nil, fmt.Errorf("unexpected response type for stream health metrics: %T", val)
	}

	// Return metrics
	result := make([]*pb.StreamHealthMetric, len(resp.Metrics))
	copy(result, resp.Metrics)

	return result, nil
}

// DoGetCurrentStreamHealth returns current health for a stream
func (r *Resolver) DoGetCurrentStreamHealth(ctx context.Context, streamId string) (*pb.StreamHealthMetric, error) {
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

// DoGetViewerGeographics returns geographic data for individual viewer/connection events
func (r *Resolver) DoGetViewerGeographics(ctx context.Context, stream *string, timeRange *model.TimeRangeInput) ([]*pb.ConnectionEvent, error) {
	if err := middleware.RequirePermission(ctx, "analytics:read"); err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning synthetic viewer geographics")
		return demo.GenerateViewerGeographics(), nil
	}

	tenantID := tenantIDFromContext(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	normalizedStream, err := normalizeStreamIDPtr(stream)
	if err != nil {
		return nil, err
	}
	stream = normalizedStream

	// Get geographic data from Periscope Query
	tr := toTimeRangeOpts(timeRange)
	cacheKey := tenantID + ":all"
	if stream != nil && *stream != "" {
		cacheKey = tenantID + ":" + *stream
	}
	if timeRange != nil {
		cacheKey += ":" + timeRange.Start.Format(time.RFC3339) + ":" + timeRange.End.Format(time.RFC3339)
	}
	val, err := r.fetchPeriscope(ctx, "connection_events", []string{cacheKey}, func(ctx context.Context) (interface{}, error) {
		return r.Clients.Periscope.GetConnectionEvents(ctx, tenantID, stream, tr, nil)
	})
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get connection events for geographics")
		return nil, fmt.Errorf("failed to fetch geographic data: %w", err)
	}
	connResp, ok := val.(*pb.GetConnectionEventsResponse)
	if !ok {
		return nil, fmt.Errorf("unexpected response type for connection events: %T", val)
	}

	var out []*pb.ConnectionEvent
	for _, ev := range connResp.Events {
		if stream != nil && *stream != "" && ev.StreamId != *stream {
			continue
		}
		out = append(out, ev)
	}
	return out, nil
}

// DoGetGeographicDistribution returns aggregated geographic distribution analytics
// Uses server-side ClickHouse aggregation for scalability
func (r *Resolver) DoGetGeographicDistribution(ctx context.Context, stream *string, timeRange *model.TimeRangeInput, topN *int) (*model.GeographicDistribution, error) {
	if err := middleware.RequirePermission(ctx, "analytics:read"); err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning synthetic geographic distribution")
		return demo.GenerateGeographicDistribution(), nil
	}

	normalizedStream, err := normalizeStreamIDPtr(stream)
	if err != nil {
		return nil, err
	}
	stream = normalizedStream

	// Extract tenant ID from context for data isolation
	// Extract tenant ID from context for data isolation
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	// Default topN to 10 if not specified
	topNVal := int32(10)
	if topN != nil && *topN > 0 {
		topNVal = int32(*topN)
	}

	// Build cache key
	streamKey := ""
	if stream != nil {
		streamKey = *stream
	}
	cacheKey := tenantID + ":" + streamKey + ":" + fmt.Sprintf("%d", topNVal)
	if timeRange != nil {
		cacheKey += ":" + timeRange.Start.Format(time.RFC3339) + ":" + timeRange.End.Format(time.RFC3339)
	}

	// Get geographic distribution from Periscope (server-side aggregation)
	val, err := r.fetchPeriscope(ctx, "geographic_distribution", []string{cacheKey}, func(ctx context.Context) (interface{}, error) {
		return r.Clients.Periscope.GetGeographicDistribution(ctx, tenantID, stream, toTimeRangeOpts(timeRange), topNVal)
	})
	if err != nil {
		r.Logger.WithError(err).WithFields(logrus.Fields{
			"tenant_id": tenantID,
			"stream":    streamKey,
			"topN":      topNVal,
		}).Error("Failed to get geographic distribution")
		return nil, fmt.Errorf("failed to get geographic distribution: %w", err)
	}
	resp, ok := val.(*pb.GetGeographicDistributionResponse)
	if !ok {
		return nil, fmt.Errorf("unexpected response type for geographic distribution: %T", val)
	}

	// Build time range for response
	startTime := time.Now().Add(-24 * time.Hour)
	endTime := time.Now()
	if timeRange != nil {
		startTime = timeRange.Start
		endTime = timeRange.End
	}
	tr := &pb.TimeRange{
		Start: timestamppb.New(startTime),
		End:   timestamppb.New(endTime),
	}

	// Proto types from Periscope are directly compatible with the model
	return &model.GeographicDistribution{
		TimeRange:        tr,
		Stream:           stream,
		TopCountries:     resp.TopCountries,
		TopCities:        resp.TopCities,
		UniqueCountries:  int(resp.UniqueCountries),
		UniqueCities:     int(resp.UniqueCities),
		TotalViewers:     int(resp.TotalViewers),
		ViewersByCountry: []*model.CountryTimeSeries{}, // Server-side doesn't return time series yet
	}, nil
}

func (r *Resolver) fetchPeriscope(ctx context.Context, operation string, keyParts []string, loader func(context.Context) (interface{}, error)) (interface{}, error) {
	return r.fetchPeriscopeWithOptions(ctx, operation, keyParts, loader, false)
}

func (r *Resolver) fetchPeriscopeWithOptions(ctx context.Context, operation string, keyParts []string, loader func(context.Context) (interface{}, error), skipCache bool) (interface{}, error) {
	if r.Fetcher == nil {
		return loader(ctx)
	}
	req := datafetcher.FetchRequest{
		Service:   datafetcher.ServicePeriscope,
		Operation: operation,
		KeyParts:  keyParts,
		Loader:    loader,
		SkipCache: skipCache,
	}
	return r.Fetcher.Fetch(ctx, req)
}

func timeKey(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func (r *Resolver) loadRoutingEvents(ctx context.Context, stream *string, startTime, endTime *time.Time, opts *periscopeclient.CursorPaginationOpts, skipCache bool, relatedTenantIDs []string, subjectTenantID, clusterID *string) (*pb.GetRoutingEventsResponse, error) {
	tenantID := tenantIDFromContext(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	streamKey := ""
	if stream != nil {
		streamKey = *stream
	}

	// Build cache key including pagination parameters and related tenants
	relatedKey := strings.Join(relatedTenantIDs, ",")
	keyParts := []string{tenantID, streamKey, timeKey(startTime), timeKey(endTime), relatedKey}
	if subjectTenantID != nil {
		keyParts = append(keyParts, "st:"+*subjectTenantID)
	}
	if clusterID != nil {
		keyParts = append(keyParts, "cl:"+*clusterID)
	}
	if opts != nil {
		keyParts = append(keyParts, fmt.Sprintf("f%d", opts.First))
		if opts.After != nil {
			keyParts = append(keyParts, *opts.After)
		}
		if opts.Last > 0 {
			keyParts = append(keyParts, fmt.Sprintf("l%d", opts.Last))
		}
		if opts.Before != nil {
			keyParts = append(keyParts, *opts.Before)
		}
	}

	// Convert time pointers to TimeRangeOpts
	var tr *periscopeclient.TimeRangeOpts
	if startTime != nil && endTime != nil {
		tr = &periscopeclient.TimeRangeOpts{StartTime: *startTime, EndTime: *endTime}
	}

	val, err := r.fetchPeriscopeWithOptions(ctx, "routing_events", keyParts, func(ctx context.Context) (interface{}, error) {
		return r.Clients.Periscope.GetRoutingEvents(ctx, tenantID, stream, tr, opts, relatedTenantIDs, subjectTenantID, clusterID)
	}, skipCache)
	if err != nil {
		return nil, err
	}
	resp, ok := val.(*pb.GetRoutingEventsResponse)
	if !ok {
		return nil, fmt.Errorf("unexpected response type for routing events: %T", val)
	}
	return resp, nil
}

func (r *Resolver) loadConnectionEvents(ctx context.Context, stream *string, startTime, endTime *time.Time, opts *periscopeclient.CursorPaginationOpts, skipCache bool) (*pb.GetConnectionEventsResponse, error) {
	tenantID := tenantIDFromContext(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	streamKey := ""
	if stream != nil {
		streamKey = *stream
	}

	// Build cache key including pagination parameters
	keyParts := []string{tenantID, streamKey, timeKey(startTime), timeKey(endTime)}
	if opts != nil {
		keyParts = append(keyParts, fmt.Sprintf("f%d", opts.First))
		if opts.After != nil {
			keyParts = append(keyParts, *opts.After)
		}
	}

	// Convert time pointers to TimeRangeOpts
	var tr *periscopeclient.TimeRangeOpts
	if startTime != nil && endTime != nil {
		tr = &periscopeclient.TimeRangeOpts{StartTime: *startTime, EndTime: *endTime}
	}

	val, err := r.fetchPeriscopeWithOptions(ctx, "connection_events", keyParts, func(ctx context.Context) (interface{}, error) {
		return r.Clients.Periscope.GetConnectionEvents(ctx, tenantID, stream, tr, opts)
	}, skipCache)
	if err != nil {
		return nil, err
	}
	resp, ok := val.(*pb.GetConnectionEventsResponse)
	if !ok {
		return nil, fmt.Errorf("unexpected response type for connection events: %T", val)
	}
	return resp, nil
}

func (r *Resolver) loadNodeMetrics(ctx context.Context, nodeID *string, startTime, endTime *time.Time, opts *periscopeclient.CursorPaginationOpts, skipCache bool) (*pb.GetNodeMetricsResponse, error) {
	tenantID := tenantIDFromContext(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	nodeKey := ""
	if nodeID != nil {
		nodeKey = *nodeID
	}

	// Build cache key including pagination parameters
	keyParts := []string{tenantID, nodeKey, timeKey(startTime), timeKey(endTime)}
	if opts != nil {
		keyParts = append(keyParts, fmt.Sprintf("f%d", opts.First))
		if opts.After != nil {
			keyParts = append(keyParts, *opts.After)
		}
	}

	// Convert time pointers to TimeRangeOpts
	var tr *periscopeclient.TimeRangeOpts
	if startTime != nil && endTime != nil {
		tr = &periscopeclient.TimeRangeOpts{StartTime: *startTime, EndTime: *endTime}
	}

	val, err := r.fetchPeriscopeWithOptions(ctx, "node_metrics", keyParts, func(ctx context.Context) (interface{}, error) {
		return r.Clients.Periscope.GetNodeMetrics(ctx, tenantID, nodeID, tr, opts)
	}, skipCache)
	if err != nil {
		return nil, err
	}
	resp, ok := val.(*pb.GetNodeMetricsResponse)
	if !ok {
		return nil, fmt.Errorf("unexpected response type for node metrics: %T", val)
	}
	return resp, nil
}

func (r *Resolver) loadNodeMetrics1h(ctx context.Context, nodeID *string, startTime, endTime *time.Time, opts *periscopeclient.CursorPaginationOpts, skipCache bool) (*pb.GetNodeMetrics1HResponse, error) {
	tenantID := tenantIDFromContext(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	nodeKey := ""
	if nodeID != nil {
		nodeKey = *nodeID
	}

	// Build cache key including pagination parameters
	keyParts := []string{tenantID, nodeKey, timeKey(startTime), timeKey(endTime)}
	if opts != nil {
		keyParts = append(keyParts, fmt.Sprintf("f%d", opts.First))
		if opts.After != nil {
			keyParts = append(keyParts, *opts.After)
		}
	}

	// Convert time pointers to TimeRangeOpts
	var tr *periscopeclient.TimeRangeOpts
	if startTime != nil && endTime != nil {
		tr = &periscopeclient.TimeRangeOpts{StartTime: *startTime, EndTime: *endTime}
	}

	val, err := r.fetchPeriscopeWithOptions(ctx, "node_metrics_1h", keyParts, func(ctx context.Context) (interface{}, error) {
		return r.Clients.Periscope.GetNodeMetrics1H(ctx, tenantID, nodeID, tr, opts)
	}, skipCache)
	if err != nil {
		return nil, err
	}
	resp, ok := val.(*pb.GetNodeMetrics1HResponse)
	if !ok {
		return nil, fmt.Errorf("unexpected response type for node metrics 1h: %T", val)
	}
	return resp, nil
}

func (r *Resolver) loadClipEvents(ctx context.Context, streamID, stage, contentType *string, startTime, endTime *time.Time, opts *periscopeclient.CursorPaginationOpts, skipCache bool) (*pb.GetClipEventsResponse, error) {
	tenantID := tenantIDFromContext(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	streamIDKey := ""
	if streamID != nil {
		streamIDKey = *streamID
	}
	stageKey := ""
	if stage != nil {
		stageKey = *stage
	}
	contentTypeKey := ""
	if contentType != nil {
		contentTypeKey = *contentType
	}

	// Build cache key including pagination parameters
	keyParts := []string{tenantID, streamIDKey, stageKey, contentTypeKey, timeKey(startTime), timeKey(endTime)}
	if opts != nil {
		keyParts = append(keyParts, fmt.Sprintf("f%d", opts.First))
		if opts.After != nil {
			keyParts = append(keyParts, *opts.After)
		}
	}

	// Convert time pointers to TimeRangeOpts
	var tr *periscopeclient.TimeRangeOpts
	if startTime != nil && endTime != nil {
		tr = &periscopeclient.TimeRangeOpts{StartTime: *startTime, EndTime: *endTime}
	}

	val, err := r.fetchPeriscopeWithOptions(ctx, "clip_events", keyParts, func(ctx context.Context) (interface{}, error) {
		return r.Clients.Periscope.GetClipEvents(ctx, tenantID, streamID, stage, contentType, tr, opts)
	}, skipCache)
	if err != nil {
		return nil, err
	}
	resp, ok := val.(*pb.GetClipEventsResponse)
	if !ok {
		return nil, fmt.Errorf("unexpected response type for clip events: %T", val)
	}
	return resp, nil
}

func (r *Resolver) loadStreamEvents(ctx context.Context, streamID string, startTime, endTime *time.Time, opts *periscopeclient.CursorPaginationOpts, skipCache bool) (*pb.GetStreamEventsResponse, error) {
	tenantID := tenantIDFromContext(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	// Build cache key including pagination parameters
	keyParts := []string{tenantID, streamID, timeKey(startTime), timeKey(endTime)}
	if opts != nil {
		keyParts = append(keyParts, fmt.Sprintf("f%d", opts.First))
		if opts.After != nil {
			keyParts = append(keyParts, *opts.After)
		}
	}

	// Convert time pointers to TimeRangeOpts
	var tr *periscopeclient.TimeRangeOpts
	if startTime != nil && endTime != nil {
		tr = &periscopeclient.TimeRangeOpts{StartTime: *startTime, EndTime: *endTime}
	}

	val, err := r.fetchPeriscopeWithOptions(ctx, "stream_events", keyParts, func(ctx context.Context) (interface{}, error) {
		return r.Clients.Periscope.GetStreamEvents(ctx, tenantID, streamID, tr, opts)
	}, skipCache)
	if err != nil {
		return nil, err
	}
	resp, ok := val.(*pb.GetStreamEventsResponse)
	if !ok {
		return nil, fmt.Errorf("unexpected response type for stream events: %T", val)
	}
	return resp, nil
}

func (r *Resolver) loadTrackListEvents(ctx context.Context, streamID string, startTime, endTime *time.Time, opts *periscopeclient.CursorPaginationOpts, skipCache bool) (*pb.GetTrackListEventsResponse, error) {
	tenantID := tenantIDFromContext(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	// Build cache key including pagination parameters
	keyParts := []string{tenantID, streamID, timeKey(startTime), timeKey(endTime)}
	if opts != nil {
		keyParts = append(keyParts, fmt.Sprintf("f%d", opts.First))
		if opts.After != nil {
			keyParts = append(keyParts, *opts.After)
		}
	}

	// Convert time pointers to TimeRangeOpts
	var tr *periscopeclient.TimeRangeOpts
	if startTime != nil && endTime != nil {
		tr = &periscopeclient.TimeRangeOpts{StartTime: *startTime, EndTime: *endTime}
	}

	val, err := r.fetchPeriscopeWithOptions(ctx, "track_list_events", keyParts, func(ctx context.Context) (interface{}, error) {
		return r.Clients.Periscope.GetTrackListEvents(ctx, tenantID, streamID, tr, opts)
	}, skipCache)
	if err != nil {
		return nil, err
	}
	resp, ok := val.(*pb.GetTrackListEventsResponse)
	if !ok {
		return nil, fmt.Errorf("unexpected response type for track list events: %T", val)
	}
	return resp, nil
}

func (r *Resolver) loadStreamHealthMetrics(ctx context.Context, stream *string, startTime, endTime *time.Time, opts *periscopeclient.CursorPaginationOpts, skipCache bool) (*pb.GetStreamHealthMetricsResponse, error) {
	tenantID := tenantIDFromContext(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	// Build cache key including stream and pagination parameters
	streamKey := ""
	if stream != nil {
		streamKey = *stream
	}
	keyParts := []string{tenantID, streamKey, timeKey(startTime), timeKey(endTime)}
	if opts != nil {
		keyParts = append(keyParts, fmt.Sprintf("f%d", opts.First))
		if opts.After != nil {
			keyParts = append(keyParts, *opts.After)
		}
	}

	// Convert time pointers to TimeRangeOpts
	var tr *periscopeclient.TimeRangeOpts
	if startTime != nil && endTime != nil {
		tr = &periscopeclient.TimeRangeOpts{StartTime: *startTime, EndTime: *endTime}
	}

	val, err := r.fetchPeriscopeWithOptions(ctx, "stream_health_metrics", keyParts, func(ctx context.Context) (interface{}, error) {
		return r.Clients.Periscope.GetStreamHealthMetrics(ctx, tenantID, stream, tr, opts)
	}, skipCache)
	if err != nil {
		return nil, err
	}
	resp, ok := val.(*pb.GetStreamHealthMetricsResponse)
	if !ok {
		return nil, fmt.Errorf("unexpected response type for stream health metrics: %T", val)
	}
	return resp, nil
}

// DoGetTenantDailyStats returns daily tenant statistics for PlatformOverview.dailyStats.
func (r *Resolver) DoGetTenantDailyStats(ctx context.Context, days *int) ([]*pb.TenantDailyStat, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning synthetic tenant daily stats")
		return demo.GenerateTenantDailyStats(days), nil
	}

	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	// Default to 7 days if not specified
	daysVal := int32(7)
	if days != nil && *days > 0 {
		daysVal = int32(*days)
	}

	response, err := r.Clients.Periscope.GetTenantDailyStats(ctx, tenantID, daysVal)
	if err != nil {
		r.Logger.WithError(err).WithField("tenant_id", tenantID).Error("Failed to get tenant daily stats")
		return nil, fmt.Errorf("failed to get tenant daily stats: %w", err)
	}

	return response.Stats, nil
}

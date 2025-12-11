package resolvers

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/datafetcher"
	"frameworks/api_gateway/internal/demo"
	"frameworks/api_gateway/internal/middleware"
	periscopeclient "frameworks/pkg/clients/periscope"
	pb "frameworks/pkg/proto"

	"github.com/sirupsen/logrus"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Helper function to convert model time range to Periscope TimeRangeOpts
func toTimeRangeOpts(timeRange *model.TimeRangeInput) *periscopeclient.TimeRangeOpts {
	if timeRange == nil {
		return nil
	}
	return &periscopeclient.TimeRangeOpts{
		StartTime: timeRange.Start,
		EndTime:   timeRange.End,
	}
}

// Helper function to convert time pointers to TimeRangeOpts
func timePtrsToTimeRangeOpts(startTime, endTime *time.Time) *periscopeclient.TimeRangeOpts {
	if startTime == nil || endTime == nil {
		return nil
	}
	return &periscopeclient.TimeRangeOpts{
		StartTime: *startTime,
		EndTime:   *endTime,
	}
}

// Helper function to convert model pagination to cursor pagination opts
func toCursorPaginationOpts(first *int, after *string) *periscopeclient.CursorPaginationOpts {
	opts := &periscopeclient.CursorPaginationOpts{
		First: 100,
	}
	if first != nil {
		opts.First = int32(*first)
	}
	if after != nil {
		opts.After = after
	}
	return opts
}

// DoGetStreamAnalytics returns analytics for a specific stream
func (r *Resolver) DoGetStreamAnalytics(ctx context.Context, streamId string, timeRange *model.TimeRangeInput) (*pb.StreamAnalytics, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning synthetic stream analytics")
		return demo.GenerateStreamAnalytics(streamId), nil
	}

	// Extract tenant ID from context for data isolation
	tenantID, ok := ctx.Value("tenant_id").(string)
	if !ok {
		return nil, fmt.Errorf("tenant context required")
	}

	// Build cache key
	cacheKey := tenantID + ":" + streamId
	if timeRange != nil {
		cacheKey += ":" + timeRange.Start.Format(time.RFC3339) + ":" + timeRange.End.Format(time.RFC3339)
	}

	// Get analytics from Periscope Query using tenant_id from JWT context
	val, err := r.fetchPeriscope(ctx, "stream_analytics", []string{cacheKey}, func(ctx context.Context) (interface{}, error) {
		return r.Clients.Periscope.GetStreamAnalytics(ctx, tenantID, &streamId, toTimeRangeOpts(timeRange), nil)
	})
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get stream analytics")
		return nil, fmt.Errorf("failed to get stream analytics: %w", err)
	}
	resp := val.(*pb.GetStreamAnalyticsResponse)

	// Return the first analytics result if available
	if len(resp.GetStreams()) > 0 {
		return resp.GetStreams()[0], nil
	}
	// Return null instead of error when no analytics found - this is normal for new streams
	return nil, nil
}

// DoGetPlatformOverview returns platform-wide metrics
func (r *Resolver) DoGetPlatformOverview(ctx context.Context, timeRange *model.TimeRangeInput) (*pb.GetPlatformOverviewResponse, error) {
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

	return val.(*pb.GetPlatformOverviewResponse), nil
}

// DoGetViewerCountTimeSeries returns time-bucketed viewer counts for charts
// interval should be "5m", "15m", "1h", or "1d"
func (r *Resolver) DoGetViewerCountTimeSeries(ctx context.Context, stream *string, timeRange *model.TimeRangeInput, interval *string) ([]*pb.ViewerCountBucket, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning synthetic viewer count time series")
		return demo.GenerateViewerCountTimeSeries(), nil
	}

	// Extract tenant ID from context for data isolation
	tenantID, ok := ctx.Value("tenant_id").(string)
	if !ok {
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
	resp := val.(*pb.GetViewerCountTimeSeriesResponse)

	return resp.Buckets, nil
}

// DoGetStreamHealthMetrics returns stream health metrics
func (r *Resolver) DoGetStreamHealthMetrics(ctx context.Context, streamId string, timeRange *model.TimeRangeInput) ([]*pb.StreamHealthMetric, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning synthetic stream health metrics")
		return demo.GenerateStreamHealthMetrics(), nil
	}

	// Convert time range for Periscope client
	tr := toTimeRangeOpts(timeRange)
	var internalName *string
	if streamId != "" {
		internalName = &streamId
	}

	// Get health metrics from Periscope Query
	cacheKey := "all"
	if streamId != "" {
		cacheKey = streamId
	}
	if timeRange != nil {
		cacheKey += ":" + timeRange.Start.Format(time.RFC3339) + ":" + timeRange.End.Format(time.RFC3339)
	}
	val, err := r.fetchPeriscope(ctx, "stream_health", []string{cacheKey}, func(ctx context.Context) (interface{}, error) {
		return r.Clients.Periscope.GetStreamHealthMetrics(ctx, internalName, tr, nil)
	})
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get stream health metrics")
		return nil, fmt.Errorf("failed to get stream health metrics: %w", err)
	}
	resp := val.(*pb.GetStreamHealthMetricsResponse)

	// Return metrics
	result := make([]*pb.StreamHealthMetric, len(resp.Metrics))
	for i := range resp.Metrics {
		result[i] = resp.Metrics[i]
	}

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

// DoGetRebufferingEvents returns rebuffering events for a stream
func (r *Resolver) DoGetRebufferingEvents(ctx context.Context, streamId string, timeRange *model.TimeRangeInput) ([]*model.RebufferingEvent, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning synthetic rebuffering events")
		return demo.GenerateRebufferingEvents(), nil
	}

	// Convert time range for Periscope client
	tr := toTimeRangeOpts(timeRange)
	cacheKey := streamId
	if timeRange != nil {
		cacheKey += ":" + timeRange.Start.Format(time.RFC3339) + ":" + timeRange.End.Format(time.RFC3339)
	}

	// Get buffer events as proxy for rebuffering
	val, err := r.fetchPeriscope(ctx, "buffer_events", []string{cacheKey}, func(ctx context.Context) (interface{}, error) {
		return r.Clients.Periscope.GetBufferEvents(ctx, streamId, tr, nil)
	})
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get buffer events")
		return nil, fmt.Errorf("failed to get rebuffering events: %w", err)
	}
	bufferEvents := val.(*pb.GetBufferEventsResponse)

	// Convert buffer events to rebuffering events
	var result []*model.RebufferingEvent
	var prevState model.BufferState = model.BufferStateFull

	for _, event := range bufferEvents.Events {
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
			ts := time.Time{}
			if event.Timestamp != nil {
				ts = event.Timestamp.AsTime()
			}
			result = append(result, &model.RebufferingEvent{
				Timestamp:     ts,
				Stream:        streamId,
				NodeID:        event.NodeId,
				BufferState:   bufferState,
				PreviousState: prevState,
				RebufferStart: rebufferStart,
				RebufferEnd:   rebufferEnd,
			})
		}

		prevState = bufferState
	}

	return result, nil
}

// DoGetViewerGeographics returns geographic data for individual viewer/connection events
func (r *Resolver) DoGetViewerGeographics(ctx context.Context, stream *string, timeRange *model.TimeRangeInput) ([]*pb.ConnectionEvent, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning synthetic viewer geographics")
		return demo.GenerateViewerGeographics(), nil
	}

	// Get geographic data from Periscope Query
	tr := toTimeRangeOpts(timeRange)
	cacheKey := "all"
	if stream != nil && *stream != "" {
		cacheKey = *stream
	}
	if timeRange != nil {
		cacheKey += ":" + timeRange.Start.Format(time.RFC3339) + ":" + timeRange.End.Format(time.RFC3339)
	}
	val, err := r.fetchPeriscope(ctx, "connection_events", []string{cacheKey}, func(ctx context.Context) (interface{}, error) {
		return r.Clients.Periscope.GetConnectionEvents(ctx, stream, tr, nil)
	})
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get connection events for geographics")
		return []*pb.ConnectionEvent{}, nil
	}
	connResp := val.(*pb.GetConnectionEventsResponse)

	var out []*pb.ConnectionEvent
	for _, ev := range connResp.Events {
		if stream != nil && *stream != "" && ev.InternalName != *stream {
			continue
		}
		out = append(out, ev)
	}
	return out, nil
}

// DoGetGeographicDistribution returns aggregated geographic distribution analytics
// Uses server-side ClickHouse aggregation for scalability
func (r *Resolver) DoGetGeographicDistribution(ctx context.Context, stream *string, timeRange *model.TimeRangeInput, topN *int) (*model.GeographicDistribution, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning synthetic geographic distribution")
		return demo.GenerateGeographicDistribution(), nil
	}

	// Extract tenant ID from context for data isolation
	tenantID, ok := ctx.Value("tenant_id").(string)
	if !ok {
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
	resp := val.(*pb.GetGeographicDistributionResponse)

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

// DoGetLoadBalancingMetrics returns load balancing and routing metrics with geographic context
func (r *Resolver) DoGetLoadBalancingMetrics(ctx context.Context, timeRange *model.TimeRangeInput) ([]*pb.RoutingEvent, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning synthetic load balancing metrics")
		return demo.GenerateLoadBalancingMetrics(), nil
	}

	// Extract tenant ID from context for data isolation
	tenantID, ok := ctx.Value("tenant_id").(string)
	if !ok {
		return nil, fmt.Errorf("tenant context required")
	}

	tr := toTimeRangeOpts(timeRange)
	cacheKey := "all"
	if timeRange != nil {
		cacheKey = timeRange.Start.Format(time.RFC3339) + ":" + timeRange.End.Format(time.RFC3339)
	}

	// Fetch related tenant IDs (from subscriptions)
	var relatedTenantIDs []string
	if user := middleware.GetUserFromContext(ctx); user != nil {
		// Fetch subscribed clusters to find their owners
		subs, err := r.Clients.Quartermaster.ListMySubscriptions(ctx, &pb.ListMySubscriptionsRequest{
			TenantId: user.TenantID,
		})
		if err == nil && subs != nil {
			for _, cluster := range subs.Clusters {
				if cluster.OwnerTenantId != nil && *cluster.OwnerTenantId != "" && *cluster.OwnerTenantId != user.TenantID {
					relatedTenantIDs = append(relatedTenantIDs, *cluster.OwnerTenantId)
				}
			}
		}
	}

	// Fetch routing events from Periscope
	val, err := r.fetchPeriscope(ctx, "routing_events", []string{cacheKey}, func(ctx context.Context) (interface{}, error) {
		return r.Clients.Periscope.GetRoutingEvents(ctx, nil, tr, nil, relatedTenantIDs)
	})
	if err != nil {
		r.Logger.WithError(err).WithFields(logrus.Fields{
			"tenant_id": tenantID,
		}).Error("Failed to get routing events")
		return nil, fmt.Errorf("failed to get load balancing metrics: %w", err)
	}
	resp := val.(*pb.GetRoutingEventsResponse)

	// Events already privacy-safe (no IP, bucketed coords); return as-is
	return resp.Events, nil
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

func clampPagination(p *model.PaginationInput, total int) (start int, end int) {
	const defaultLimit = 100
	const maxLimit = 1000

	limit := defaultLimit
	if p != nil {
		if p.Offset != nil {
			start = *p.Offset
			if start < 0 {
				start = 0
			}
		}
		if p.Limit != nil {
			limit = *p.Limit
		}
	}
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	end = total
	if start+limit < end {
		end = start + limit
	}
	return start, end
}

func (r *Resolver) loadRoutingEvents(ctx context.Context, stream *string, startTime, endTime *time.Time, opts *periscopeclient.CursorPaginationOpts, skipCache bool, relatedTenantIDs []string) (*pb.GetRoutingEventsResponse, error) {
	streamKey := ""
	if stream != nil {
		streamKey = *stream
	}

	// Build cache key including pagination parameters and related tenants
	relatedKey := strings.Join(relatedTenantIDs, ",")
	keyParts := []string{streamKey, timeKey(startTime), timeKey(endTime), relatedKey}
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

	val, err := r.fetchPeriscopeWithOptions(ctx, "routing_events", keyParts, func(ctx context.Context) (interface{}, error) {
		return r.Clients.Periscope.GetRoutingEvents(ctx, stream, tr, opts, relatedTenantIDs)
	}, skipCache)
	if err != nil {
		return nil, err
	}
	return val.(*pb.GetRoutingEventsResponse), nil
}

func (r *Resolver) loadConnectionEvents(ctx context.Context, stream *string, startTime, endTime *time.Time, opts *periscopeclient.CursorPaginationOpts, skipCache bool) (*pb.GetConnectionEventsResponse, error) {
	streamKey := ""
	if stream != nil {
		streamKey = *stream
	}

	// Build cache key including pagination parameters
	keyParts := []string{streamKey, timeKey(startTime), timeKey(endTime)}
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
		return r.Clients.Periscope.GetConnectionEvents(ctx, stream, tr, opts)
	}, skipCache)
	if err != nil {
		return nil, err
	}
	return val.(*pb.GetConnectionEventsResponse), nil
}

func (r *Resolver) loadNodeMetrics(ctx context.Context, nodeID *string, startTime, endTime *time.Time, opts *periscopeclient.CursorPaginationOpts, skipCache bool) (*pb.GetNodeMetricsResponse, error) {
	nodeKey := ""
	if nodeID != nil {
		nodeKey = *nodeID
	}

	// Build cache key including pagination parameters
	keyParts := []string{nodeKey, timeKey(startTime), timeKey(endTime)}
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
		return r.Clients.Periscope.GetNodeMetrics(ctx, nodeID, tr, opts)
	}, skipCache)
	if err != nil {
		return nil, err
	}
	return val.(*pb.GetNodeMetricsResponse), nil
}

func (r *Resolver) loadNodeMetrics1h(ctx context.Context, nodeID *string, startTime, endTime *time.Time, opts *periscopeclient.CursorPaginationOpts, skipCache bool) (*pb.GetNodeMetrics1HResponse, error) {
	nodeKey := ""
	if nodeID != nil {
		nodeKey = *nodeID
	}

	// Build cache key including pagination parameters
	keyParts := []string{nodeKey, timeKey(startTime), timeKey(endTime)}
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
		return r.Clients.Periscope.GetNodeMetrics1H(ctx, nodeID, tr, opts)
	}, skipCache)
	if err != nil {
		return nil, err
	}
	return val.(*pb.GetNodeMetrics1HResponse), nil
}

func (r *Resolver) loadClipEvents(ctx context.Context, internalName, stage *string, startTime, endTime *time.Time, opts *periscopeclient.CursorPaginationOpts, skipCache bool) (*pb.GetClipEventsResponse, error) {
	internalNameKey := ""
	if internalName != nil {
		internalNameKey = *internalName
	}
	stageKey := ""
	if stage != nil {
		stageKey = *stage
	}

	// Build cache key including pagination parameters
	keyParts := []string{internalNameKey, stageKey, timeKey(startTime), timeKey(endTime)}
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
		return r.Clients.Periscope.GetClipEvents(ctx, internalName, stage, tr, opts)
	}, skipCache)
	if err != nil {
		return nil, err
	}
	return val.(*pb.GetClipEventsResponse), nil
}

func (r *Resolver) loadStreamEvents(ctx context.Context, internalName string, startTime, endTime *time.Time, opts *periscopeclient.CursorPaginationOpts, skipCache bool) (*pb.GetStreamEventsResponse, error) {
	// Build cache key including pagination parameters
	keyParts := []string{internalName, timeKey(startTime), timeKey(endTime)}
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
		return r.Clients.Periscope.GetStreamEvents(ctx, internalName, tr, opts)
	}, skipCache)
	if err != nil {
		return nil, err
	}
	return val.(*pb.GetStreamEventsResponse), nil
}

func (r *Resolver) loadTrackListEvents(ctx context.Context, stream string, startTime, endTime *time.Time, opts *periscopeclient.CursorPaginationOpts, skipCache bool) (*pb.GetTrackListEventsResponse, error) {
	// Build cache key including pagination parameters
	keyParts := []string{stream, timeKey(startTime), timeKey(endTime)}
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
		return r.Clients.Periscope.GetTrackListEvents(ctx, stream, tr, opts)
	}, skipCache)
	if err != nil {
		return nil, err
	}
	return val.(*pb.GetTrackListEventsResponse), nil
}

func (r *Resolver) loadStreamHealthMetrics(ctx context.Context, stream *string, startTime, endTime *time.Time, opts *periscopeclient.CursorPaginationOpts, skipCache bool) (*pb.GetStreamHealthMetricsResponse, error) {
	// Build cache key including stream and pagination parameters
	streamKey := ""
	if stream != nil {
		streamKey = *stream
	}
	keyParts := []string{streamKey, timeKey(startTime), timeKey(endTime)}
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
		return r.Clients.Periscope.GetStreamHealthMetrics(ctx, stream, tr, opts)
	}, skipCache)
	if err != nil {
		return nil, err
	}
	return val.(*pb.GetStreamHealthMetricsResponse), nil
}

package resolvers

import (
	"context"
	"fmt"
	"time"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/demo"
	"frameworks/api_gateway/internal/loaders"
	"frameworks/api_gateway/internal/middleware"
	periscopeclient "frameworks/pkg/clients/periscope"
	"frameworks/pkg/ctxkeys"
	"frameworks/pkg/pagination"
	pb "frameworks/pkg/proto"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// encodeStableCursor creates a stable cursor from timestamp and ID.
// This cursor format survives data insertions/deletions.
// Use this when the backend supports keyset pagination.
func encodeStableCursor(timestamp time.Time, id string) string {
	return pagination.EncodeCursor(timestamp, id)
}

// decodeStableCursor parses a stable cursor back to timestamp and ID.
// Returns nil if cursor is empty, error if format is invalid.
func decodeStableCursor(cursor string) (*pagination.Cursor, error) {
	return pagination.DecodeCursor(cursor)
}

// parseTimeRange converts GraphQL TimeRangeInput to time pointers
func parseTimeRange(timeRange *model.TimeRangeInput) (startTime *time.Time, endTime *time.Time) {
	if timeRange != nil {
		startTime = &timeRange.Start
		endTime = &timeRange.End
	}
	return startTime, endTime
}

func cursorTimeFromProto(ts *timestamppb.Timestamp) time.Time {
	if ts == nil {
		return time.Now()
	}
	return ts.AsTime()
}

// DoGetRoutingEventsConnection returns a connection-style payload for routing events.
func (r *Resolver) DoGetRoutingEventsConnection(ctx context.Context, stream *string, timeRange *model.TimeRangeInput, subjectTenantID *string, clusterID *string, first *int, after *string, last *int, before *string, noCache *bool) (*model.RoutingEventsConnection, error) {
	if err := middleware.RequirePermission(ctx, "analytics:read"); err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateRoutingEventsConnection(), nil
	}

	tenantID := tenantIDFromContext(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	normalizedStreamID, err := normalizeStreamIDPtr(stream)
	if err != nil {
		return nil, err
	}
	stream = normalizedStreamID

	// Build cursor pagination options
	opts := &periscopeclient.CursorPaginationOpts{
		First: int32(pagination.DefaultLimit),
	}
	if first != nil {
		opts.First = int32(pagination.ClampLimit(*first))
	}
	if after != nil && *after != "" {
		opts.After = after
	}
	if last != nil {
		opts.Last = int32(pagination.ClampLimit(*last))
	}
	if before != nil && *before != "" {
		opts.Before = before
	}

	startTime, endTime := parseTimeRange(timeRange)
	skipCache := noCache != nil && *noCache

	// Fetch related tenant IDs (from subscriptions) to allow seeing events from shared infra
	var relatedTenantIDs []string
	if user := middleware.GetUserFromContext(ctx); user != nil {
		// Fetch subscribed clusters to find their owners
		// Note: Pagination handled by Quartermaster, here we just want the list.
		// If user has >100 subscriptions, we might miss some providers here without paging loop.
		// For now, assume <100 subscriptions.
		subs, subsErr := r.Clients.Quartermaster.ListMySubscriptions(ctx, &pb.ListMySubscriptionsRequest{
			TenantId: user.TenantID,
		})
		if subsErr == nil && subs != nil {
			for _, cluster := range subs.Clusters {
				if cluster.OwnerTenantId != nil && *cluster.OwnerTenantId != "" && *cluster.OwnerTenantId != user.TenantID {
					relatedTenantIDs = append(relatedTenantIDs, *cluster.OwnerTenantId)
				}
			}
		}
	}

	// Fetch from datafetcher with pagination and optional stream filter
	response, err := r.loadRoutingEvents(ctx, stream, startTime, endTime, opts, skipCache, relatedTenantIDs, subjectTenantID, clusterID)
	if err != nil {
		return nil, err
	}

	streamIDs := make([]string, len(response.Events))
	for i, e := range response.Events {
		streamIDs[i] = e.GetStreamId()
	}
	loaders.PreloadStreams(ctx, tenantID, streamIDs)

	// Build edges from proto response
	edges := make([]*model.RoutingEventEdge, len(response.Events))
	for i, event := range response.Events {
		cursorTime := time.Now()
		if event.Timestamp != nil {
			cursorTime = event.Timestamp.AsTime()
		}
		cursor := encodeStableCursor(cursorTime, event.Id)
		edges[i] = &model.RoutingEventEdge{
			Cursor: cursor,
			Node:   event,
		}
	}

	// Build page info
	totalCount := 0
	hasMore := false
	if response.Pagination != nil {
		totalCount = int(response.Pagination.TotalCount)
		hasMore = response.Pagination.HasNextPage
	}

	pageInfo := &model.PageInfo{
		HasPreviousPage: response.Pagination != nil && response.Pagination.HasPreviousPage,
		HasNextPage:     hasMore,
	}
	if response.Pagination != nil {
		pageInfo.StartCursor = response.Pagination.StartCursor
		pageInfo.EndCursor = response.Pagination.EndCursor
	}

	edgeNodes := make([]*pb.RoutingEvent, 0, len(edges))
	for _, edge := range edges {
		if edge != nil {
			edgeNodes = append(edgeNodes, edge.Node)
		}
	}

	return &model.RoutingEventsConnection{
		Edges:      edges,
		Nodes:      edgeNodes,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}, nil
}

// DoGetConnectionEventsConnection returns a connection-style payload for connection events.
func (r *Resolver) DoGetConnectionEventsConnection(ctx context.Context, stream *string, timeRange *model.TimeRangeInput, first *int, after *string, last *int, before *string, noCache *bool) (*model.ConnectionEventsConnection, error) {
	if err := middleware.RequirePermission(ctx, "analytics:read"); err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateConnectionEventsConnection(), nil
	}

	tenantID := tenantIDFromContext(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	normalizedStreamID, err := normalizeStreamIDPtr(stream)
	if err != nil {
		return nil, err
	}
	stream = normalizedStreamID

	// Build cursor pagination options
	opts := &periscopeclient.CursorPaginationOpts{
		First: int32(pagination.DefaultLimit),
	}
	if first != nil {
		opts.First = int32(pagination.ClampLimit(*first))
	}
	if after != nil && *after != "" {
		opts.After = after
	}
	if last != nil {
		opts.Last = int32(pagination.ClampLimit(*last))
	}
	if before != nil && *before != "" {
		opts.Before = before
	}

	startTime, endTime := parseTimeRange(timeRange)
	skipCache := noCache != nil && *noCache

	// Fetch from datafetcher with pagination and optional stream filter
	response, err := r.loadConnectionEvents(ctx, stream, startTime, endTime, opts, skipCache)
	if err != nil {
		return nil, err
	}

	streamIDs := make([]string, len(response.Events))
	for i, e := range response.Events {
		streamIDs[i] = e.GetStreamId()
	}
	loaders.PreloadStreams(ctx, tenantID, streamIDs)

	// Build edges from proto response
	edges := make([]*model.ConnectionEventEdge, len(response.Events))
	for i, event := range response.Events {
		cursorTime := time.Now()
		if event.Timestamp != nil {
			cursorTime = event.Timestamp.AsTime()
		}
		cursor := encodeStableCursor(cursorTime, event.EventId)
		edges[i] = &model.ConnectionEventEdge{
			Cursor: cursor,
			Node:   event,
		}
	}

	// Build page info
	totalCount := 0
	hasMore := false
	if response.Pagination != nil {
		totalCount = int(response.Pagination.TotalCount)
		hasMore = response.Pagination.HasNextPage
	}

	pageInfo := &model.PageInfo{
		HasPreviousPage: response.Pagination != nil && response.Pagination.HasPreviousPage,
		HasNextPage:     hasMore,
	}
	if response.Pagination != nil {
		pageInfo.StartCursor = response.Pagination.StartCursor
		pageInfo.EndCursor = response.Pagination.EndCursor
	}

	edgeNodes := make([]*pb.ConnectionEvent, 0, len(edges))
	for _, edge := range edges {
		if edge != nil {
			edgeNodes = append(edgeNodes, edge.Node)
		}
	}

	return &model.ConnectionEventsConnection{
		Edges:      edges,
		Nodes:      edgeNodes,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}, nil
}

// DoGetArtifactEventsConnection returns a connection-style payload for artifact events (clip/dvr/vod).
// NOTE: Filters already working - handler and client support streamId, stage, contentType
func (r *Resolver) DoGetArtifactEventsConnection(ctx context.Context, streamId *string, stage *string, contentType *string, timeRange *model.TimeRangeInput, first *int, after *string, last *int, before *string, noCache *bool) (*model.ArtifactEventsConnection, error) {
	if err := middleware.RequirePermission(ctx, "analytics:read"); err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateArtifactEventsConnection(), nil
	}

	tenantID := tenantIDFromContext(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	normalizedStreamID, err := normalizeStreamIDPtr(streamId)
	if err != nil {
		return nil, err
	}
	streamId = normalizedStreamID

	// Build cursor pagination options
	opts := &periscopeclient.CursorPaginationOpts{
		First: int32(pagination.DefaultLimit),
	}
	if first != nil {
		opts.First = int32(pagination.ClampLimit(*first))
	}
	if after != nil && *after != "" {
		opts.After = after
	}
	if last != nil {
		opts.Last = int32(pagination.ClampLimit(*last))
	}
	if before != nil && *before != "" {
		opts.Before = before
	}

	// Parse filters
	var name, stageFilter, contentTypeFilter *string
	if streamId != nil && *streamId != "" {
		name = streamId
	}
	if stage != nil && *stage != "" {
		stageFilter = stage
	}
	if contentType != nil && *contentType != "" {
		contentTypeFilter = contentType
	}

	startTime, endTime := parseTimeRange(timeRange)
	skipCache := noCache != nil && *noCache

	// Fetch from datafetcher with pagination
	response, err := r.loadClipEvents(ctx, name, stageFilter, contentTypeFilter, startTime, endTime, opts, skipCache)
	if err != nil {
		return nil, err
	}

	streamIDs := make([]string, len(response.Events))
	for i, e := range response.Events {
		streamIDs[i] = e.GetStreamId()
	}
	loaders.PreloadStreams(ctx, tenantID, streamIDs)

	// Build edges from proto response
	edges := make([]*model.ArtifactEventEdge, len(response.Events))
	for i, event := range response.Events {
		cursorTime := time.Now()
		if event.Timestamp != nil {
			cursorTime = event.Timestamp.AsTime()
		}
		cursor := encodeStableCursor(cursorTime, event.RequestId)
		edges[i] = &model.ArtifactEventEdge{
			Cursor: cursor,
			Node:   event,
		}
	}

	// Build page info
	totalCount := 0
	hasMore := false
	if response.Pagination != nil {
		totalCount = int(response.Pagination.TotalCount)
		hasMore = response.Pagination.HasNextPage
	}

	pageInfo := &model.PageInfo{
		HasPreviousPage: response.Pagination != nil && response.Pagination.HasPreviousPage,
		HasNextPage:     hasMore,
	}
	if response.Pagination != nil {
		pageInfo.StartCursor = response.Pagination.StartCursor
		pageInfo.EndCursor = response.Pagination.EndCursor
	}

	edgeNodes := make([]*pb.ClipEvent, 0, len(edges))
	for _, edge := range edges {
		if edge != nil {
			edgeNodes = append(edgeNodes, edge.Node)
		}
	}

	return &model.ArtifactEventsConnection{
		Edges:      edges,
		Nodes:      edgeNodes,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}, nil
}

// DoGetNodeMetricsConnection returns a connection-style payload for node metrics.
func (r *Resolver) DoGetNodeMetricsConnection(ctx context.Context, nodeID *string, timeRange *model.TimeRangeInput, first *int, after *string, last *int, before *string, noCache *bool) (*model.NodeMetricsConnection, error) {
	if err := middleware.RequirePermission(ctx, "analytics:read"); err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateNodeMetricsConnection(), nil
	}

	tenantID := tenantIDFromContext(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	// Build cursor pagination options
	opts := &periscopeclient.CursorPaginationOpts{
		First: int32(pagination.DefaultLimit),
	}
	if first != nil {
		opts.First = int32(pagination.ClampLimit(*first))
	}
	if after != nil && *after != "" {
		opts.After = after
	}
	if last != nil {
		opts.Last = int32(pagination.ClampLimit(*last))
	}
	if before != nil && *before != "" {
		opts.Before = before
	}

	startTime, endTime := parseTimeRange(timeRange)
	skipCache := noCache != nil && *noCache

	// Fetch from datafetcher with pagination and optional nodeID filter
	response, err := r.loadNodeMetrics(ctx, nodeID, startTime, endTime, opts, skipCache)
	if err != nil {
		return nil, err
	}

	// Build edges from proto response
	edges := make([]*model.NodeMetricEdge, len(response.Metrics))
	for i, metric := range response.Metrics {
		cursor := encodeStableCursor(cursorTimeFromProto(metric.Timestamp), metric.Id)
		edges[i] = &model.NodeMetricEdge{
			Cursor: cursor,
			Node:   metric,
		}
	}

	// Build page info
	totalCount := 0
	hasMore := false
	if response.Pagination != nil {
		totalCount = int(response.Pagination.TotalCount)
		hasMore = response.Pagination.HasNextPage
	}

	pageInfo := &model.PageInfo{
		HasPreviousPage: response.Pagination != nil && response.Pagination.HasPreviousPage,
		HasNextPage:     hasMore,
	}
	if response.Pagination != nil {
		pageInfo.StartCursor = response.Pagination.StartCursor
		pageInfo.EndCursor = response.Pagination.EndCursor
	}

	edgeNodes := make([]*pb.NodeMetric, 0, len(edges))
	for _, edge := range edges {
		if edge != nil {
			edgeNodes = append(edgeNodes, edge.Node)
		}
	}

	return &model.NodeMetricsConnection{
		Edges:      edges,
		Nodes:      edgeNodes,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}, nil
}

// DoGetNodeMetrics1hConnection returns a connection-style payload for 1h node metrics.
func (r *Resolver) DoGetNodeMetrics1hConnection(ctx context.Context, timeRange *model.TimeRangeInput, nodeID *string, first *int, after *string, last *int, before *string, noCache *bool) (*model.NodeMetrics1hConnection, error) {
	if err := middleware.RequirePermission(ctx, "analytics:read"); err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateNodeMetrics1hConnection(), nil
	}

	tenantID := tenantIDFromContext(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	// Build cursor pagination options
	opts := &periscopeclient.CursorPaginationOpts{
		First: int32(pagination.DefaultLimit),
	}
	if first != nil {
		opts.First = int32(pagination.ClampLimit(*first))
	}
	if after != nil && *after != "" {
		opts.After = after
	}
	if last != nil {
		opts.Last = int32(pagination.ClampLimit(*last))
	}
	if before != nil && *before != "" {
		opts.Before = before
	}

	startTime, endTime := parseTimeRange(timeRange)
	skipCache := noCache != nil && *noCache

	// Fetch from datafetcher with pagination and optional nodeID filter
	response, err := r.loadNodeMetrics1h(ctx, nodeID, startTime, endTime, opts, skipCache)
	if err != nil {
		return nil, err
	}

	// Build edges from proto response
	edges := make([]*model.NodeMetricHourlyEdge, len(response.Metrics))
	for i, metric := range response.Metrics {
		cursor := encodeStableCursor(cursorTimeFromProto(metric.Timestamp), metric.Id)
		edges[i] = &model.NodeMetricHourlyEdge{
			Cursor: cursor,
			Node:   metric,
		}
	}

	// Build page info
	totalCount := 0
	hasMore := false
	if response.Pagination != nil {
		totalCount = int(response.Pagination.TotalCount)
		hasMore = response.Pagination.HasNextPage
	}

	pageInfo := &model.PageInfo{
		HasPreviousPage: response.Pagination != nil && response.Pagination.HasPreviousPage,
		HasNextPage:     hasMore,
	}
	if response.Pagination != nil {
		pageInfo.StartCursor = response.Pagination.StartCursor
		pageInfo.EndCursor = response.Pagination.EndCursor
	}

	edgeNodes := make([]*pb.NodeMetricHourly, 0, len(edges))
	for _, edge := range edges {
		if edge != nil {
			edgeNodes = append(edgeNodes, edge.Node)
		}
	}

	return &model.NodeMetrics1hConnection{
		Edges:      edges,
		Nodes:      edgeNodes,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}, nil
}

// DoGetNodeMetricsAggregated returns per-node aggregates for a time range.
func (r *Resolver) DoGetNodeMetricsAggregated(ctx context.Context, timeRange *model.TimeRangeInput, nodeID *string, noCache *bool) ([]*pb.NodeMetricsAggregated, error) {
	if err := middleware.RequirePermission(ctx, "analytics:read"); err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateNodeMetricsAggregated(), nil
	}

	tenantID := tenantIDFromContext(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	startTime, endTime := parseTimeRange(timeRange)
	skipCache := noCache != nil && *noCache

	nodeKey := ""
	if nodeID != nil {
		nodeKey = *nodeID
	}
	keyParts := []string{tenantID, nodeKey, timeKey(startTime), timeKey(endTime)}

	val, err := r.fetchPeriscopeWithOptions(ctx, "node_metrics_aggregated", keyParts, func(ctx context.Context) (interface{}, error) {
		return r.Clients.Periscope.GetNodeMetricsAggregated(ctx, tenantID, nodeID, timePtrsToTimeRangeOpts(startTime, endTime))
	}, skipCache)
	if err != nil {
		return nil, err
	}
	resp, ok := val.(*pb.GetNodeMetricsAggregatedResponse)
	if !ok {
		return nil, fmt.Errorf("unexpected response type for node metrics aggregated: %T", val)
	}

	return resp.Metrics, nil
}

// DoGetStreamHealthMetricsConnection returns a connection-style payload for stream health metrics.
func (r *Resolver) DoGetStreamHealthMetricsConnection(ctx context.Context, stream string, timeRange *model.TimeRangeInput, first *int, after *string, last *int, before *string, noCache *bool) (*model.StreamHealthMetricsConnection, error) {
	if err := middleware.RequirePermission(ctx, "analytics:read"); err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateStreamHealthMetricsConnection(), nil
	}

	if tenantIDFromContext(ctx) == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	normalizedID, err := normalizeStreamID(stream)
	if err != nil {
		return nil, err
	}
	stream = normalizedID

	// Build cursor pagination options
	opts := &periscopeclient.CursorPaginationOpts{
		First: int32(pagination.DefaultLimit),
	}
	if first != nil {
		opts.First = int32(pagination.ClampLimit(*first))
	}
	if after != nil && *after != "" {
		opts.After = after
	}
	if last != nil {
		opts.Last = int32(pagination.ClampLimit(*last))
	}
	if before != nil && *before != "" {
		opts.Before = before
	}

	startTime, endTime := parseTimeRange(timeRange)
	skipCache := noCache != nil && *noCache

	// Build stream filter - pass nil for empty string to query all streams
	var streamFilter *string
	if stream != "" {
		streamFilter = &stream
	}

	// Fetch from datafetcher with pagination and stream filter
	response, err := r.loadStreamHealthMetrics(ctx, streamFilter, startTime, endTime, opts, skipCache)
	if err != nil {
		return nil, err
	}

	loaders.PreloadStreams(ctx, ctxkeys.GetTenantID(ctx), []string{stream})

	// Build edges from proto response
	edges := make([]*model.StreamHealthMetricEdge, len(response.Metrics))
	for i, metric := range response.Metrics {
		cursor := encodeStableCursor(cursorTimeFromProto(metric.Timestamp), metric.Id)
		edges[i] = &model.StreamHealthMetricEdge{
			Cursor: cursor,
			Node:   metric,
		}
	}

	// Build page info
	totalCount := 0
	hasMore := false
	if response.Pagination != nil {
		totalCount = int(response.Pagination.TotalCount)
		hasMore = response.Pagination.HasNextPage
	}

	pageInfo := &model.PageInfo{
		HasPreviousPage: response.Pagination != nil && response.Pagination.HasPreviousPage,
		HasNextPage:     hasMore,
	}
	if response.Pagination != nil {
		pageInfo.StartCursor = response.Pagination.StartCursor
		pageInfo.EndCursor = response.Pagination.EndCursor
	}

	edgeNodes := make([]*pb.StreamHealthMetric, 0, len(edges))
	for _, edge := range edges {
		if edge != nil {
			edgeNodes = append(edgeNodes, edge.Node)
		}
	}

	return &model.StreamHealthMetricsConnection{
		Edges:      edges,
		Nodes:      edgeNodes,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}, nil
}

// DoGetTrackListEventsConnection returns a connection-style payload for track list events.
func (r *Resolver) DoGetTrackListEventsConnection(ctx context.Context, stream string, timeRange *model.TimeRangeInput, first *int, after *string, last *int, before *string, noCache *bool) (*model.TrackListEventsConnection, error) {
	if err := middleware.RequirePermission(ctx, "analytics:read"); err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateTrackListEventsConnection(), nil
	}
	if stream == "" {
		return nil, fmt.Errorf("stream_id required")
	}

	if tenantIDFromContext(ctx) == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	// Build cursor pagination options
	opts := &periscopeclient.CursorPaginationOpts{
		First: int32(pagination.DefaultLimit),
	}
	if first != nil {
		opts.First = int32(pagination.ClampLimit(*first))
	}
	if after != nil && *after != "" {
		opts.After = after
	}
	if last != nil {
		opts.Last = int32(pagination.ClampLimit(*last))
	}
	if before != nil && *before != "" {
		opts.Before = before
	}

	startTime, endTime := parseTimeRange(timeRange)
	skipCache := noCache != nil && *noCache

	// Fetch from datafetcher with pagination
	response, err := r.loadTrackListEvents(ctx, stream, startTime, endTime, opts, skipCache)
	if err != nil {
		return nil, err
	}

	loaders.PreloadStreams(ctx, ctxkeys.GetTenantID(ctx), []string{stream})

	// Build edges from proto response
	edges := make([]*model.TrackListEventEdge, len(response.Events))
	for i, event := range response.Events {
		cursorTime := time.Now()
		if event.Timestamp != nil {
			cursorTime = event.Timestamp.AsTime()
		}
		cursor := encodeStableCursor(cursorTime, event.Id)
		edges[i] = &model.TrackListEventEdge{
			Cursor: cursor,
			Node:   event,
		}
	}

	// Build page info
	totalCount := 0
	hasMore := false
	if response.Pagination != nil {
		totalCount = int(response.Pagination.TotalCount)
		hasMore = response.Pagination.HasNextPage
	}

	pageInfo := &model.PageInfo{
		HasPreviousPage: response.Pagination != nil && response.Pagination.HasPreviousPage,
		HasNextPage:     hasMore,
	}
	if response.Pagination != nil {
		pageInfo.StartCursor = response.Pagination.StartCursor
		pageInfo.EndCursor = response.Pagination.EndCursor
	}

	edgeNodes := make([]*pb.TrackListEvent, 0, len(edges))
	for _, edge := range edges {
		if edge != nil {
			edgeNodes = append(edgeNodes, edge.Node)
		}
	}

	return &model.TrackListEventsConnection{
		Edges:      edges,
		Nodes:      edgeNodes,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}, nil
}

// DoGetStreamEventsConnection returns a connection-style payload for stream events.
// NOTE: stream filter already supported by client method
func (r *Resolver) DoGetStreamEventsConnection(ctx context.Context, streamId string, timeRange *model.TimeRangeInput, first *int, after *string, last *int, before *string, noCache *bool) (*model.StreamEventsConnection, error) {
	if err := middleware.RequirePermission(ctx, "analytics:read"); err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateStreamEventsConnection(), nil
	}
	normalizedID, err := normalizeStreamID(streamId)
	if err != nil {
		return nil, err
	}
	streamId = normalizedID

	if streamId == "" {
		return nil, fmt.Errorf("streamId required")
	}

	if tenantIDFromContext(ctx) == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	// Build cursor pagination options
	opts := &periscopeclient.CursorPaginationOpts{
		First: int32(pagination.DefaultLimit),
	}
	if first != nil {
		opts.First = int32(pagination.ClampLimit(*first))
	}
	if after != nil && *after != "" {
		opts.After = after
	}
	if last != nil {
		opts.Last = int32(pagination.ClampLimit(*last))
	}
	if before != nil && *before != "" {
		opts.Before = before
	}

	startTime, endTime := parseTimeRange(timeRange)
	skipCache := noCache != nil && *noCache

	// Fetch from datafetcher with pagination and optional cache bypass
	response, err := r.loadStreamEvents(ctx, streamId, startTime, endTime, opts, skipCache)
	if err != nil {
		return nil, err
	}

	loaders.PreloadStreams(ctx, ctxkeys.GetTenantID(ctx), []string{streamId})

	// Build edges from proto response
	edges := make([]*model.StreamEventEdge, 0, len(response.Events))
	for i, event := range response.Events {
		mapped := mapPeriscopeStreamEvent(event)
		if mapped == nil {
			continue
		}

		cursorTime := mapped.Timestamp
		cursorID := mapped.EventId
		if cursorID == "" {
			cursorID = fmt.Sprintf("se_cursor_%d", i)
		}

		cursor := encodeStableCursor(cursorTime, cursorID)
		edges = append(edges, &model.StreamEventEdge{
			Cursor: cursor,
			Node:   mapped,
		})
	}

	// Build page info
	totalCount := 0
	hasMore := false
	if response.Pagination != nil {
		totalCount = int(response.Pagination.TotalCount)
		hasMore = response.Pagination.HasNextPage
	}

	pageInfo := &model.PageInfo{
		HasPreviousPage: response.Pagination != nil && response.Pagination.HasPreviousPage,
		HasNextPage:     hasMore,
	}
	if response.Pagination != nil {
		pageInfo.StartCursor = response.Pagination.StartCursor
		pageInfo.EndCursor = response.Pagination.EndCursor
	}

	edgeNodes := make([]*model.StreamEvent, 0, len(edges))
	for _, edge := range edges {
		if edge != nil {
			edgeNodes = append(edgeNodes, edge.Node)
		}
	}

	return &model.StreamEventsConnection{
		Edges:      edges,
		Nodes:      edgeNodes,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}, nil
}

// DoGetBufferEventsConnection returns a connection-style payload for stream buffer events.
func (r *Resolver) DoGetBufferEventsConnection(ctx context.Context, streamId string, timeRange *model.TimeRangeInput, first *int, after *string, last *int, before *string, noCache *bool) (*model.BufferEventsConnection, error) {
	if err := middleware.RequirePermission(ctx, "analytics:read"); err != nil {
		return nil, err
	}
	normalizedID, err := normalizeStreamID(streamId)
	if err != nil {
		return nil, err
	}
	streamId = normalizedID

	if middleware.IsDemoMode(ctx) {
		return demo.GenerateBufferEventsConnection(streamId), nil
	}

	if streamId == "" {
		return nil, fmt.Errorf("streamId required")
	}

	tenantID := tenantIDFromContext(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant_id required")
	}

	// Build cursor pagination options
	opts := &periscopeclient.CursorPaginationOpts{
		First: int32(pagination.DefaultLimit),
	}
	if first != nil {
		opts.First = int32(pagination.ClampLimit(*first))
	}
	if after != nil && *after != "" {
		opts.After = after
	}
	if last != nil {
		opts.Last = int32(pagination.ClampLimit(*last))
	}
	if before != nil && *before != "" {
		opts.Before = before
	}

	startTime, endTime := parseTimeRange(timeRange)
	skipCache := noCache != nil && *noCache

	var timeOpts *periscopeclient.TimeRangeOpts
	if startTime != nil && endTime != nil {
		timeOpts = &periscopeclient.TimeRangeOpts{StartTime: *startTime, EndTime: *endTime}
	}

	keyParts := []string{tenantID, streamId, timeKey(startTime), timeKey(endTime)}
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

	val, err := r.fetchPeriscopeWithOptions(ctx, "buffer_events", keyParts, func(ctx context.Context) (interface{}, error) {
		return r.Clients.Periscope.GetBufferEvents(ctx, tenantID, streamId, timeOpts, opts)
	}, skipCache)
	if err != nil {
		return nil, err
	}
	response, ok := val.(*pb.GetBufferEventsResponse)
	if !ok {
		return nil, fmt.Errorf("unexpected response type for buffer events: %T", val)
	}

	loaders.PreloadStreams(ctx, tenantID, []string{streamId})

	edges := make([]*model.BufferEventEdge, len(response.Events))
	for i, event := range response.Events {
		cursorTime := time.Now()
		if event.Timestamp != nil {
			cursorTime = event.Timestamp.AsTime()
		}
		cursorID := event.EventId
		if cursorID == "" {
			cursorID = fmt.Sprintf("be_cursor_%d", i)
		}
		cursor := encodeStableCursor(cursorTime, cursorID)
		edges[i] = &model.BufferEventEdge{
			Cursor: cursor,
			Node:   event,
		}
	}

	totalCount := 0
	hasMore := false
	if response.Pagination != nil {
		totalCount = int(response.Pagination.TotalCount)
		hasMore = response.Pagination.HasNextPage
	}

	pageInfo := &model.PageInfo{
		HasPreviousPage: response.Pagination != nil && response.Pagination.HasPreviousPage,
		HasNextPage:     hasMore,
	}
	if response.Pagination != nil {
		pageInfo.StartCursor = response.Pagination.StartCursor
		pageInfo.EndCursor = response.Pagination.EndCursor
	}

	edgeNodes := make([]*pb.BufferEvent, 0, len(edges))
	for _, edge := range edges {
		if edge != nil {
			edgeNodes = append(edgeNodes, edge.Node)
		}
	}

	return &model.BufferEventsConnection{
		Edges:      edges,
		Nodes:      edgeNodes,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}, nil
}

// DoGetStreamHealthConnection returns a connection-style payload for stream health metrics.
func (r *Resolver) DoGetStreamHealthConnection(ctx context.Context, obj *pb.Stream, timeRange *model.TimeRangeInput, first *int, after *string) (*model.StreamHealthMetricsConnection, error) {
	if err := middleware.RequirePermission(ctx, "analytics:read"); err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateStreamHealthMetricsConnection(), nil
	}

	if tenantIDFromContext(ctx) == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	// Build cursor pagination options
	opts := &periscopeclient.CursorPaginationOpts{
		First: int32(pagination.DefaultLimit),
	}
	if first != nil {
		opts.First = int32(pagination.ClampLimit(*first))
	}
	if after != nil && *after != "" {
		opts.After = after
	}

	startTime, endTime := parseTimeRange(timeRange)

	// Pass stream filter from the parent Stream object
	var streamFilter *string
	if obj.StreamId != "" {
		streamFilter = &obj.StreamId
	}

	// Fetch from datafetcher with pagination and stream filter (no noCache param on this resolver)
	response, err := r.loadStreamHealthMetrics(ctx, streamFilter, startTime, endTime, opts, false)
	if err != nil {
		return nil, err
	}

	// Build edges from proto response
	edges := make([]*model.StreamHealthMetricEdge, len(response.Metrics))
	for i, metric := range response.Metrics {
		cursor := encodeStableCursor(cursorTimeFromProto(metric.Timestamp), metric.Id)
		edges[i] = &model.StreamHealthMetricEdge{
			Cursor: cursor,
			Node:   metric,
		}
	}

	// Build page info
	totalCount := 0
	hasMore := false
	if response.Pagination != nil {
		totalCount = int(response.Pagination.TotalCount)
		hasMore = response.Pagination.HasNextPage
	}

	pageInfo := &model.PageInfo{
		HasPreviousPage: response.Pagination != nil && response.Pagination.HasPreviousPage,
		HasNextPage:     hasMore,
	}
	if response.Pagination != nil {
		pageInfo.StartCursor = response.Pagination.StartCursor
		pageInfo.EndCursor = response.Pagination.EndCursor
	}

	edgeNodes := make([]*pb.StreamHealthMetric, 0, len(edges))
	for _, edge := range edges {
		if edge != nil {
			edgeNodes = append(edgeNodes, edge.Node)
		}
	}

	return &model.StreamHealthMetricsConnection{
		Edges:      edges,
		Nodes:      edgeNodes,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}, nil
}

// DoGetNodeMetricsConnectionForNode returns a connection-style payload for node metrics (for node resolver).
func (r *Resolver) DoGetNodeMetricsConnectionForNode(ctx context.Context, obj *pb.InfrastructureNode, timeRange *model.TimeRangeInput, first *int, after *string) (*model.NodeMetricsConnection, error) {
	if err := middleware.RequirePermission(ctx, "analytics:read"); err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateNodeMetricsConnection(), nil
	}

	if tenantIDFromContext(ctx) == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	// Build cursor pagination options
	opts := &periscopeclient.CursorPaginationOpts{
		First: int32(pagination.DefaultLimit),
	}
	if first != nil {
		opts.First = int32(pagination.ClampLimit(*first))
	}
	if after != nil && *after != "" {
		opts.After = after
	}

	startTime, endTime := parseTimeRange(timeRange)

	// Fetch from datafetcher with pagination and node filter (no noCache param on this resolver)
	response, err := r.loadNodeMetrics(ctx, &obj.Id, startTime, endTime, opts, false)
	if err != nil {
		return nil, err
	}

	// Build edges from proto response
	edges := make([]*model.NodeMetricEdge, len(response.Metrics))
	for i, metric := range response.Metrics {
		cursor := encodeStableCursor(cursorTimeFromProto(metric.Timestamp), metric.Id)
		edges[i] = &model.NodeMetricEdge{
			Cursor: cursor,
			Node:   metric,
		}
	}

	// Build page info
	totalCount := 0
	hasMore := false
	if response.Pagination != nil {
		totalCount = int(response.Pagination.TotalCount)
		hasMore = response.Pagination.HasNextPage
	}

	pageInfo := &model.PageInfo{
		HasPreviousPage: response.Pagination != nil && response.Pagination.HasPreviousPage,
		HasNextPage:     hasMore,
	}
	if response.Pagination != nil {
		pageInfo.StartCursor = response.Pagination.StartCursor
		pageInfo.EndCursor = response.Pagination.EndCursor
	}

	edgeNodes := make([]*pb.NodeMetric, 0, len(edges))
	for _, edge := range edges {
		if edge != nil {
			edgeNodes = append(edgeNodes, edge.Node)
		}
	}

	return &model.NodeMetricsConnection{
		Edges:      edges,
		Nodes:      edgeNodes,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}, nil
}

// DoGetNodeMetrics1hConnectionForNode returns a connection-style payload for 1h node metrics (for node resolver).
func (r *Resolver) DoGetNodeMetrics1hConnectionForNode(ctx context.Context, obj *pb.InfrastructureNode, timeRange *model.TimeRangeInput, first *int, after *string) (*model.NodeMetrics1hConnection, error) {
	if err := middleware.RequirePermission(ctx, "analytics:read"); err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateNodeMetrics1hConnection(), nil
	}

	if tenantIDFromContext(ctx) == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	// Build cursor pagination options
	opts := &periscopeclient.CursorPaginationOpts{
		First: int32(pagination.DefaultLimit),
	}
	if first != nil {
		opts.First = int32(pagination.ClampLimit(*first))
	}
	if after != nil && *after != "" {
		opts.After = after
	}

	startTime, endTime := parseTimeRange(timeRange)

	// Fetch from datafetcher with pagination and node filter (no noCache param on this resolver)
	response, err := r.loadNodeMetrics1h(ctx, &obj.Id, startTime, endTime, opts, false)
	if err != nil {
		return nil, err
	}

	// Build edges from proto response
	edges := make([]*model.NodeMetricHourlyEdge, len(response.Metrics))
	for i, metric := range response.Metrics {
		cursor := encodeStableCursor(cursorTimeFromProto(metric.Timestamp), metric.Id)
		edges[i] = &model.NodeMetricHourlyEdge{
			Cursor: cursor,
			Node:   metric,
		}
	}

	// Build page info
	totalCount := 0
	hasMore := false
	if response.Pagination != nil {
		totalCount = int(response.Pagination.TotalCount)
		hasMore = response.Pagination.HasNextPage
	}

	pageInfo := &model.PageInfo{
		HasPreviousPage: response.Pagination != nil && response.Pagination.HasPreviousPage,
		HasNextPage:     hasMore,
	}
	if response.Pagination != nil {
		pageInfo.StartCursor = response.Pagination.StartCursor
		pageInfo.EndCursor = response.Pagination.EndCursor
	}

	edgeNodes := make([]*pb.NodeMetricHourly, 0, len(edges))
	for _, edge := range edges {
		if edge != nil {
			edgeNodes = append(edgeNodes, edge.Node)
		}
	}

	return &model.NodeMetrics1hConnection{
		Edges:      edges,
		Nodes:      edgeNodes,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}, nil
}

// DoGetLiveNodeState returns the real-time state of a node from live_nodes.
// Supports multi-tenant access for subscribed clusters.
func (r *Resolver) DoGetLiveNodeState(ctx context.Context, nodeID string) (*pb.LiveNode, error) {
	if err := middleware.RequirePermission(ctx, "analytics:read"); err != nil {
		return nil, err
	}
	user := middleware.GetUserFromContext(ctx)
	if user == nil || user.TenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	// Build related tenant IDs from subscribed clusters (for multi-tenant infra access)
	var relatedTenantIDs []string
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

	response, err := r.Clients.Periscope.GetLiveNodes(ctx, user.TenantID, &nodeID, relatedTenantIDs)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get live node state")
		return nil, err
	}

	// Return first (and should be only) node matching the ID
	if len(response.Nodes) > 0 {
		return response.Nodes[0], nil
	}

	// Node not found in live_nodes (might be offline or not reporting yet)
	return nil, nil
}

// DoGetArtifactState returns the current state of a single artifact (clip/DVR).
func (r *Resolver) DoGetArtifactState(ctx context.Context, requestID string) (*pb.ArtifactState, error) {
	if err := middleware.RequirePermission(ctx, "analytics:read"); err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateArtifactState(requestID), nil
	}

	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant_id required")
	}

	response, err := r.Clients.Periscope.GetArtifactState(ctx, tenantID, requestID)
	if err != nil {
		return nil, err
	}

	return response.Artifact, nil
}

// DoGetArtifactStatesConnection returns a connection-style payload for artifact states.
func (r *Resolver) DoGetArtifactStatesConnection(ctx context.Context, streamId *string, contentType *string, stage *string, first *int, after *string, last *int, before *string) (*model.ArtifactStatesConnection, error) {
	if err := middleware.RequirePermission(ctx, "analytics:read"); err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateArtifactStatesConnection(), nil
	}

	normalizedStreamID, err := normalizeStreamIDPtr(streamId)
	if err != nil {
		return nil, err
	}
	streamId = normalizedStreamID

	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant_id required")
	}

	// Build cursor pagination options
	opts := &periscopeclient.CursorPaginationOpts{
		First: int32(pagination.DefaultLimit),
	}
	if first != nil {
		opts.First = int32(pagination.ClampLimit(*first))
	}
	if after != nil && *after != "" {
		opts.After = after
	}
	if last != nil {
		opts.Last = int32(pagination.ClampLimit(*last))
	}
	if before != nil && *before != "" {
		opts.Before = before
	}

	// Fetch from Periscope
	response, err := r.Clients.Periscope.GetArtifactStates(ctx, tenantID, streamId, contentType, stage, opts)
	if err != nil {
		return nil, err
	}

	streamIDs := make([]string, len(response.Artifacts))
	for i, a := range response.Artifacts {
		streamIDs[i] = a.GetStreamId()
	}
	loaders.PreloadStreams(ctx, tenantID, streamIDs)

	// Build edges from proto response
	edges := make([]*model.ArtifactStateEdge, len(response.Artifacts))
	for i, artifact := range response.Artifacts {
		cursor := encodeStableCursor(cursorTimeFromProto(artifact.UpdatedAt), artifact.RequestId)
		edges[i] = &model.ArtifactStateEdge{
			Cursor: cursor,
			Node:   artifact,
		}
	}

	// Build page info
	totalCount := 0
	hasMore := false
	if response.Pagination != nil {
		totalCount = int(response.Pagination.TotalCount)
		hasMore = response.Pagination.HasNextPage
	}

	pageInfo := &model.PageInfo{
		HasPreviousPage: response.Pagination != nil && response.Pagination.HasPreviousPage,
		HasNextPage:     hasMore,
	}
	if response.Pagination != nil {
		pageInfo.StartCursor = response.Pagination.StartCursor
		pageInfo.EndCursor = response.Pagination.EndCursor
	}

	edgeNodes := make([]*pb.ArtifactState, 0, len(edges))
	for _, edge := range edges {
		if edge != nil {
			edgeNodes = append(edgeNodes, edge.Node)
		}
	}

	return &model.ArtifactStatesConnection{
		Edges:      edges,
		Nodes:      edgeNodes,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}, nil
}

// ============================================================================
// Pre-Aggregated Analytics Connections (Materialized Views)
// ============================================================================

// DoGetStreamConnectionHourlyConnection returns a connection-style payload for hourly connection aggregates.
func (r *Resolver) DoGetStreamConnectionHourlyConnection(ctx context.Context, stream *string, timeRange *model.TimeRangeInput, first *int, after *string, last *int, before *string, noCache *bool) (*model.StreamConnectionHourlyConnection, error) {
	if err := middleware.RequirePermission(ctx, "analytics:read"); err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateStreamConnectionHourlyConnection(), nil
	}

	normalizedStreamID, err := normalizeStreamIDPtr(stream)
	if err != nil {
		return nil, err
	}
	stream = normalizedStreamID

	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant_id required")
	}

	opts := &periscopeclient.CursorPaginationOpts{
		First: int32(pagination.DefaultLimit),
	}
	if first != nil {
		opts.First = int32(pagination.ClampLimit(*first))
	}
	if after != nil && *after != "" {
		opts.After = after
	}
	if last != nil {
		opts.Last = int32(pagination.ClampLimit(*last))
	}
	if before != nil && *before != "" {
		opts.Before = before
	}

	var timeOpts *periscopeclient.TimeRangeOpts
	if timeRange != nil {
		timeOpts = &periscopeclient.TimeRangeOpts{
			StartTime: timeRange.Start,
			EndTime:   timeRange.End,
		}
	}

	response, err := r.Clients.Periscope.GetStreamConnectionHourly(ctx, tenantID, stream, timeOpts, opts)
	if err != nil {
		return nil, err
	}

	streamIDs := make([]string, len(response.Records))
	for i, r := range response.Records {
		streamIDs[i] = r.GetStreamId()
	}
	loaders.PreloadStreams(ctx, tenantID, streamIDs)

	edges := make([]*model.StreamConnectionHourlyEdge, len(response.Records))
	for i, record := range response.Records {
		cursor := encodeStableCursor(cursorTimeFromProto(record.Hour), record.StreamId)
		edges[i] = &model.StreamConnectionHourlyEdge{
			Cursor: cursor,
			Node:   record,
		}
	}

	totalCount := 0
	hasMore := false
	if response.Pagination != nil {
		totalCount = int(response.Pagination.TotalCount)
		hasMore = response.Pagination.HasNextPage
	}

	pageInfo := &model.PageInfo{
		HasPreviousPage: response.Pagination != nil && response.Pagination.HasPreviousPage,
		HasNextPage:     hasMore,
	}
	if response.Pagination != nil {
		pageInfo.StartCursor = response.Pagination.StartCursor
		pageInfo.EndCursor = response.Pagination.EndCursor
	}

	edgeNodes := make([]*pb.StreamConnectionHourly, 0, len(edges))
	for _, edge := range edges {
		if edge != nil {
			edgeNodes = append(edgeNodes, edge.Node)
		}
	}

	return &model.StreamConnectionHourlyConnection{
		Edges:      edges,
		Nodes:      edgeNodes,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}, nil
}

// DoGetClientMetrics5mConnection returns a connection-style payload for 5-minute client metrics.
func (r *Resolver) DoGetClientMetrics5mConnection(ctx context.Context, stream *string, nodeID *string, timeRange *model.TimeRangeInput, first *int, after *string, last *int, before *string, noCache *bool) (*model.ClientMetrics5mConnection, error) {
	if err := middleware.RequirePermission(ctx, "analytics:read"); err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateClientMetrics5mConnection(), nil
	}

	normalizedStreamID, err := normalizeStreamIDPtr(stream)
	if err != nil {
		return nil, err
	}
	stream = normalizedStreamID

	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant_id required")
	}

	opts := &periscopeclient.CursorPaginationOpts{
		First: int32(pagination.DefaultLimit),
	}
	if first != nil {
		opts.First = int32(pagination.ClampLimit(*first))
	}
	if after != nil && *after != "" {
		opts.After = after
	}
	if last != nil {
		opts.Last = int32(pagination.ClampLimit(*last))
	}
	if before != nil && *before != "" {
		opts.Before = before
	}

	var timeOpts *periscopeclient.TimeRangeOpts
	if timeRange != nil {
		timeOpts = &periscopeclient.TimeRangeOpts{
			StartTime: timeRange.Start,
			EndTime:   timeRange.End,
		}
	}

	response, err := r.Clients.Periscope.GetClientMetrics5m(ctx, tenantID, stream, nodeID, timeOpts, opts)
	if err != nil {
		return nil, err
	}

	streamIDs := make([]string, len(response.Records))
	for i, r := range response.Records {
		streamIDs[i] = r.GetStreamId()
	}
	loaders.PreloadStreams(ctx, tenantID, streamIDs)

	edges := make([]*model.ClientMetrics5mEdge, len(response.Records))
	for i, record := range response.Records {
		cursor := encodeStableCursor(cursorTimeFromProto(record.Timestamp), record.Id)
		edges[i] = &model.ClientMetrics5mEdge{
			Cursor: cursor,
			Node:   record,
		}
	}

	totalCount := 0
	hasMore := false
	if response.Pagination != nil {
		totalCount = int(response.Pagination.TotalCount)
		hasMore = response.Pagination.HasNextPage
	}

	pageInfo := &model.PageInfo{
		HasPreviousPage: response.Pagination != nil && response.Pagination.HasPreviousPage,
		HasNextPage:     hasMore,
	}
	if response.Pagination != nil {
		pageInfo.StartCursor = response.Pagination.StartCursor
		pageInfo.EndCursor = response.Pagination.EndCursor
	}

	edgeNodes := make([]*pb.ClientMetrics5M, 0, len(edges))
	for _, edge := range edges {
		if edge != nil {
			edgeNodes = append(edgeNodes, edge.Node)
		}
	}

	return &model.ClientMetrics5mConnection{
		Edges:      edges,
		Nodes:      edgeNodes,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}, nil
}

// DoGetQualityTierDailyConnection returns a connection-style payload for daily quality tier distribution.
func (r *Resolver) DoGetQualityTierDailyConnection(ctx context.Context, stream *string, timeRange *model.TimeRangeInput, first *int, after *string, last *int, before *string, noCache *bool) (*model.QualityTierDailyConnection, error) {
	if err := middleware.RequirePermission(ctx, "analytics:read"); err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateQualityTierDailyConnection(), nil
	}

	normalizedStreamID, err := normalizeStreamIDPtr(stream)
	if err != nil {
		return nil, err
	}
	stream = normalizedStreamID

	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant_id required")
	}

	opts := &periscopeclient.CursorPaginationOpts{
		First: int32(pagination.DefaultLimit),
	}
	if first != nil {
		opts.First = int32(pagination.ClampLimit(*first))
	}
	if after != nil && *after != "" {
		opts.After = after
	}
	if last != nil {
		opts.Last = int32(pagination.ClampLimit(*last))
	}
	if before != nil && *before != "" {
		opts.Before = before
	}

	var timeOpts *periscopeclient.TimeRangeOpts
	if timeRange != nil {
		timeOpts = &periscopeclient.TimeRangeOpts{
			StartTime: timeRange.Start,
			EndTime:   timeRange.End,
		}
	}

	response, err := r.Clients.Periscope.GetQualityTierDaily(ctx, tenantID, stream, timeOpts, opts)
	if err != nil {
		return nil, err
	}

	streamIDs := make([]string, len(response.Records))
	for i, r := range response.Records {
		streamIDs[i] = r.GetStreamId()
	}
	loaders.PreloadStreams(ctx, tenantID, streamIDs)

	edges := make([]*model.QualityTierDailyEdge, len(response.Records))
	for i, record := range response.Records {
		cursor := encodeStableCursor(cursorTimeFromProto(record.Day), record.Id)
		edges[i] = &model.QualityTierDailyEdge{
			Cursor: cursor,
			Node:   record,
		}
	}

	totalCount := 0
	hasMore := false
	if response.Pagination != nil {
		totalCount = int(response.Pagination.TotalCount)
		hasMore = response.Pagination.HasNextPage
	}

	pageInfo := &model.PageInfo{
		HasPreviousPage: response.Pagination != nil && response.Pagination.HasPreviousPage,
		HasNextPage:     hasMore,
	}
	if response.Pagination != nil {
		pageInfo.StartCursor = response.Pagination.StartCursor
		pageInfo.EndCursor = response.Pagination.EndCursor
	}

	edgeNodes := make([]*pb.QualityTierDaily, 0, len(edges))
	for _, edge := range edges {
		if edge != nil {
			edgeNodes = append(edgeNodes, edge.Node)
		}
	}

	return &model.QualityTierDailyConnection{
		Edges:      edges,
		Nodes:      edgeNodes,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}, nil
}

// DoGetStorageUsageConnection returns a connection-style payload for storage usage records.
func (r *Resolver) DoGetStorageUsageConnection(ctx context.Context, nodeID *string, storageScope *string, timeRange *model.TimeRangeInput, first *int, after *string, last *int, before *string, noCache *bool) (*model.StorageUsageConnection, error) {
	if err := middleware.RequirePermission(ctx, "analytics:read"); err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateStorageUsageConnection(), nil
	}

	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant_id required")
	}

	opts := &periscopeclient.CursorPaginationOpts{
		First: int32(pagination.DefaultLimit),
	}
	if first != nil {
		opts.First = int32(pagination.ClampLimit(*first))
	}
	if after != nil && *after != "" {
		opts.After = after
	}
	if last != nil {
		opts.Last = int32(pagination.ClampLimit(*last))
	}
	if before != nil && *before != "" {
		opts.Before = before
	}

	var timeOpts *periscopeclient.TimeRangeOpts
	if timeRange != nil {
		timeOpts = &periscopeclient.TimeRangeOpts{
			StartTime: timeRange.Start,
			EndTime:   timeRange.End,
		}
	}

	response, err := r.Clients.Periscope.GetStorageUsage(ctx, tenantID, nodeID, storageScope, timeOpts, opts)
	if err != nil {
		return nil, err
	}

	edges := make([]*model.StorageUsageEdge, len(response.Records))
	for i, record := range response.Records {
		cursor := encodeStableCursor(cursorTimeFromProto(record.Timestamp), record.Id)
		edges[i] = &model.StorageUsageEdge{
			Cursor: cursor,
			Node:   record,
		}
	}

	totalCount := 0
	hasMore := false
	if response.Pagination != nil {
		totalCount = int(response.Pagination.TotalCount)
		hasMore = response.Pagination.HasNextPage
	}

	pageInfo := &model.PageInfo{
		HasPreviousPage: response.Pagination != nil && response.Pagination.HasPreviousPage,
		HasNextPage:     hasMore,
	}
	if response.Pagination != nil {
		pageInfo.StartCursor = response.Pagination.StartCursor
		pageInfo.EndCursor = response.Pagination.EndCursor
	}

	edgeNodes := make([]*pb.StorageUsageRecord, 0, len(edges))
	for _, edge := range edges {
		if edge != nil {
			edgeNodes = append(edgeNodes, edge.Node)
		}
	}

	return &model.StorageUsageConnection{
		Edges:      edges,
		Nodes:      edgeNodes,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}, nil
}

// DoGetStorageEventsConnection returns a connection-style payload for storage lifecycle events.
func (r *Resolver) DoGetStorageEventsConnection(ctx context.Context, streamId *string, assetType *string, timeRange *model.TimeRangeInput, first *int, after *string, last *int, before *string, noCache *bool) (*model.StorageEventsConnection, error) {
	if err := middleware.RequirePermission(ctx, "analytics:read"); err != nil {
		return nil, err
	}
	normalizedStreamID, err := normalizeStreamIDPtr(streamId)
	if err != nil {
		return nil, err
	}
	streamId = normalizedStreamID

	if middleware.IsDemoMode(ctx) {
		return demo.GenerateStorageEventsConnection(streamId), nil
	}

	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant_id required")
	}

	opts := &periscopeclient.CursorPaginationOpts{
		First: int32(pagination.DefaultLimit),
	}
	if first != nil {
		opts.First = int32(pagination.ClampLimit(*first))
	}
	if after != nil && *after != "" {
		opts.After = after
	}
	if last != nil {
		opts.Last = int32(pagination.ClampLimit(*last))
	}
	if before != nil && *before != "" {
		opts.Before = before
	}

	var timeOpts *periscopeclient.TimeRangeOpts
	if timeRange != nil {
		timeOpts = &periscopeclient.TimeRangeOpts{
			StartTime: timeRange.Start,
			EndTime:   timeRange.End,
		}
	}

	response, err := r.Clients.Periscope.GetStorageEvents(ctx, tenantID, streamId, assetType, timeOpts, opts)
	if err != nil {
		return nil, err
	}

	streamIDs := make([]string, len(response.Events))
	for i, e := range response.Events {
		streamIDs[i] = e.GetStreamId()
	}
	loaders.PreloadStreams(ctx, tenantID, streamIDs)

	edges := make([]*model.StorageEventEdge, len(response.Events))
	for i, event := range response.Events {
		cursorTime := time.Now()
		if event.Timestamp != nil {
			cursorTime = event.Timestamp.AsTime()
		}
		cursor := encodeStableCursor(cursorTime, event.Id)
		edges[i] = &model.StorageEventEdge{
			Cursor: cursor,
			Node:   event,
		}
	}

	totalCount := 0
	hasMore := false
	if response.Pagination != nil {
		totalCount = int(response.Pagination.TotalCount)
		hasMore = response.Pagination.HasNextPage
	}

	pageInfo := &model.PageInfo{
		HasPreviousPage: response.Pagination != nil && response.Pagination.HasPreviousPage,
		HasNextPage:     hasMore,
	}
	if response.Pagination != nil {
		pageInfo.StartCursor = response.Pagination.StartCursor
		pageInfo.EndCursor = response.Pagination.EndCursor
	}

	edgeNodes := make([]*pb.StorageEvent, 0, len(edges))
	for _, edge := range edges {
		if edge != nil {
			edgeNodes = append(edgeNodes, edge.Node)
		}
	}

	return &model.StorageEventsConnection{
		Edges:      edges,
		Nodes:      edgeNodes,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}, nil
}

// DoGetNodePerformance5mConnection returns 5-minute node performance aggregates with cursor pagination.
func (r *Resolver) DoGetNodePerformance5mConnection(ctx context.Context, nodeID *string, timeRange *model.TimeRangeInput, first *int, after *string, last *int, before *string, noCache *bool) (*model.NodePerformance5mConnection, error) {
	if err := middleware.RequirePermission(ctx, "analytics:read"); err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateNodePerformance5mConnection(nodeID), nil
	}

	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant_id required")
	}

	opts := &periscopeclient.CursorPaginationOpts{
		First: int32(pagination.DefaultLimit),
	}
	if first != nil {
		opts.First = int32(pagination.ClampLimit(*first))
	}
	if after != nil && *after != "" {
		opts.After = after
	}
	if last != nil {
		opts.Last = int32(pagination.ClampLimit(*last))
	}
	if before != nil && *before != "" {
		opts.Before = before
	}

	var timeOpts *periscopeclient.TimeRangeOpts
	if timeRange != nil {
		timeOpts = &periscopeclient.TimeRangeOpts{
			StartTime: timeRange.Start,
			EndTime:   timeRange.End,
		}
	}

	response, err := r.Clients.Periscope.GetNodePerformance5m(ctx, tenantID, nodeID, timeOpts, opts)
	if err != nil {
		return nil, err
	}

	edges := make([]*model.NodePerformance5mEdge, len(response.Records))
	for i, record := range response.Records {
		cursor := encodeStableCursor(cursorTimeFromProto(record.Timestamp), record.Id)
		edges[i] = &model.NodePerformance5mEdge{
			Cursor: cursor,
			Node:   record,
		}
	}

	totalCount := 0
	hasMore := false
	if response.Pagination != nil {
		totalCount = int(response.Pagination.TotalCount)
		hasMore = response.Pagination.HasNextPage
	}

	pageInfo := &model.PageInfo{
		HasPreviousPage: response.Pagination != nil && response.Pagination.HasPreviousPage,
		HasNextPage:     hasMore,
	}
	if response.Pagination != nil {
		pageInfo.StartCursor = response.Pagination.StartCursor
		pageInfo.EndCursor = response.Pagination.EndCursor
	}

	edgeNodes := make([]*pb.NodePerformance5M, 0, len(edges))
	for _, edge := range edges {
		if edge != nil {
			edgeNodes = append(edgeNodes, edge.Node)
		}
	}

	return &model.NodePerformance5mConnection{
		Edges:      edges,
		Nodes:      edgeNodes,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}, nil
}

// DoGetViewerHoursHourlyConnection returns hourly viewer-hours aggregates with cursor pagination.
func (r *Resolver) DoGetViewerHoursHourlyConnection(ctx context.Context, stream *string, timeRange *model.TimeRangeInput, first *int, after *string, last *int, before *string, noCache *bool) (*model.ViewerHoursHourlyConnection, error) {
	if err := middleware.RequirePermission(ctx, "analytics:read"); err != nil {
		return nil, err
	}
	normalizedStreamID, err := normalizeStreamIDPtr(stream)
	if err != nil {
		return nil, err
	}
	stream = normalizedStreamID

	if middleware.IsDemoMode(ctx) {
		return demo.GenerateViewerHoursHourlyConnection(stream), nil
	}

	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant_id required")
	}

	opts := &periscopeclient.CursorPaginationOpts{
		First: int32(pagination.DefaultLimit),
	}
	if first != nil {
		opts.First = int32(pagination.ClampLimit(*first))
	}
	if after != nil && *after != "" {
		opts.After = after
	}
	if last != nil {
		opts.Last = int32(pagination.ClampLimit(*last))
	}
	if before != nil && *before != "" {
		opts.Before = before
	}

	var timeOpts *periscopeclient.TimeRangeOpts
	if timeRange != nil {
		timeOpts = &periscopeclient.TimeRangeOpts{
			StartTime: timeRange.Start,
			EndTime:   timeRange.End,
		}
	}

	response, err := r.Clients.Periscope.GetViewerHoursHourly(ctx, tenantID, stream, timeOpts, opts)
	if err != nil {
		return nil, err
	}

	streamIDs := make([]string, len(response.Records))
	for i, r := range response.Records {
		streamIDs[i] = r.GetStreamId()
	}
	loaders.PreloadStreams(ctx, tenantID, streamIDs)

	edges := make([]*model.ViewerHoursHourlyEdge, len(response.Records))
	for i, record := range response.Records {
		cursor := encodeStableCursor(cursorTimeFromProto(record.Hour), record.Id)
		edges[i] = &model.ViewerHoursHourlyEdge{
			Cursor: cursor,
			Node:   record,
		}
	}

	totalCount := 0
	hasMore := false
	if response.Pagination != nil {
		totalCount = int(response.Pagination.TotalCount)
		hasMore = response.Pagination.HasNextPage
	}

	pageInfo := &model.PageInfo{
		HasPreviousPage: response.Pagination != nil && response.Pagination.HasPreviousPage,
		HasNextPage:     hasMore,
	}
	if response.Pagination != nil {
		pageInfo.StartCursor = response.Pagination.StartCursor
		pageInfo.EndCursor = response.Pagination.EndCursor
	}

	edgeNodes := make([]*pb.ViewerHoursHourly, 0, len(edges))
	for _, edge := range edges {
		if edge != nil {
			edgeNodes = append(edgeNodes, edge.Node)
		}
	}

	return &model.ViewerHoursHourlyConnection{
		Edges:      edges,
		Nodes:      edgeNodes,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}, nil
}

// DoGetViewerGeoHourlyConnection returns hourly geographic viewer distribution with cursor pagination.
func (r *Resolver) DoGetViewerGeoHourlyConnection(ctx context.Context, timeRange *model.TimeRangeInput, first *int, after *string, last *int, before *string, noCache *bool) (*model.ViewerGeoHourlyConnection, error) {
	if err := middleware.RequirePermission(ctx, "analytics:read"); err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateViewerGeoHourlyConnection(), nil
	}

	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant_id required")
	}

	opts := &periscopeclient.CursorPaginationOpts{
		First: int32(pagination.DefaultLimit),
	}
	if first != nil {
		opts.First = int32(pagination.ClampLimit(*first))
	}
	if after != nil && *after != "" {
		opts.After = after
	}
	if last != nil {
		opts.Last = int32(pagination.ClampLimit(*last))
	}
	if before != nil && *before != "" {
		opts.Before = before
	}

	var timeOpts *periscopeclient.TimeRangeOpts
	if timeRange != nil {
		timeOpts = &periscopeclient.TimeRangeOpts{
			StartTime: timeRange.Start,
			EndTime:   timeRange.End,
		}
	}

	response, err := r.Clients.Periscope.GetViewerGeoHourly(ctx, tenantID, nil, timeOpts, opts)
	if err != nil {
		return nil, err
	}

	edges := make([]*model.ViewerGeoHourlyEdge, len(response.Records))
	for i, record := range response.Records {
		cursor := encodeStableCursor(cursorTimeFromProto(record.Hour), record.Id)
		edges[i] = &model.ViewerGeoHourlyEdge{
			Cursor: cursor,
			Node:   record,
		}
	}

	totalCount := 0
	hasMore := false
	if response.Pagination != nil {
		totalCount = int(response.Pagination.TotalCount)
		hasMore = response.Pagination.HasNextPage
	}

	pageInfo := &model.PageInfo{
		HasPreviousPage: response.Pagination != nil && response.Pagination.HasPreviousPage,
		HasNextPage:     hasMore,
	}
	if response.Pagination != nil {
		pageInfo.StartCursor = response.Pagination.StartCursor
		pageInfo.EndCursor = response.Pagination.EndCursor
	}

	edgeNodes := make([]*pb.ViewerGeoHourly, 0, len(edges))
	for _, edge := range edges {
		if edge != nil {
			edgeNodes = append(edgeNodes, edge.Node)
		}
	}

	return &model.ViewerGeoHourlyConnection{
		Edges:      edges,
		Nodes:      edgeNodes,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}, nil
}

// DoGetStreamHealth5mConnection returns 5-minute stream health aggregates with cursor pagination.
func (r *Resolver) DoGetStreamHealth5mConnection(ctx context.Context, streamId string, timeRange *model.TimeRangeInput, first *int, after *string, last *int, before *string, noCache *bool) (*model.StreamHealth5mConnection, error) {
	if err := middleware.RequirePermission(ctx, "analytics:read"); err != nil {
		return nil, err
	}
	normalizedID, err := normalizeStreamID(streamId)
	if err != nil {
		return nil, err
	}
	streamId = normalizedID

	if middleware.IsDemoMode(ctx) {
		return demo.GenerateStreamHealth5mConnection(&streamId), nil
	}

	if streamId == "" {
		return nil, fmt.Errorf("streamId required")
	}

	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant_id required")
	}

	opts := &periscopeclient.CursorPaginationOpts{
		First: int32(pagination.DefaultLimit),
	}
	if first != nil {
		opts.First = int32(pagination.ClampLimit(*first))
	}
	if after != nil && *after != "" {
		opts.After = after
	}
	if last != nil {
		opts.Last = int32(pagination.ClampLimit(*last))
	}
	if before != nil && *before != "" {
		opts.Before = before
	}

	var timeOpts *periscopeclient.TimeRangeOpts
	if timeRange != nil {
		timeOpts = &periscopeclient.TimeRangeOpts{
			StartTime: timeRange.Start,
			EndTime:   timeRange.End,
		}
	}

	response, err := r.Clients.Periscope.GetStreamHealth5m(ctx, tenantID, streamId, timeOpts, opts)
	if err != nil {
		return nil, err
	}

	loaders.PreloadStreams(ctx, tenantID, []string{streamId})

	edges := make([]*model.StreamHealth5mEdge, len(response.Records))
	for i, record := range response.Records {
		cursor := encodeStableCursor(cursorTimeFromProto(record.Timestamp), record.Id)
		edges[i] = &model.StreamHealth5mEdge{
			Cursor: cursor,
			Node:   record,
		}
	}

	totalCount := 0
	hasMore := false
	if response.Pagination != nil {
		totalCount = int(response.Pagination.TotalCount)
		hasMore = response.Pagination.HasNextPage
	}

	pageInfo := &model.PageInfo{
		HasPreviousPage: response.Pagination != nil && response.Pagination.HasPreviousPage,
		HasNextPage:     hasMore,
	}
	if response.Pagination != nil {
		pageInfo.StartCursor = response.Pagination.StartCursor
		pageInfo.EndCursor = response.Pagination.EndCursor
	}

	edgeNodes := make([]*pb.StreamHealth5M, 0, len(edges))
	for _, edge := range edges {
		if edge != nil {
			edgeNodes = append(edgeNodes, edge.Node)
		}
	}

	return &model.StreamHealth5mConnection{
		Edges:      edges,
		Nodes:      edgeNodes,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}, nil
}

// DoGetViewerSessionsConnection returns viewer sessions with cursor pagination.
// This exposes ClickHouse viewer_sessions data through GraphQL.
func (r *Resolver) DoGetViewerSessionsConnection(ctx context.Context, stream *string, timeRange *model.TimeRangeInput, first *int, after *string, last *int, before *string, noCache *bool) (*model.ViewerSessionsConnection, error) {
	if err := middleware.RequirePermission(ctx, "analytics:read"); err != nil {
		return nil, err
	}
	normalizedStreamID, err := normalizeStreamIDPtr(stream)
	if err != nil {
		return nil, err
	}
	stream = normalizedStreamID

	if middleware.IsDemoMode(ctx) {
		return demo.GenerateViewerSessionsConnection(stream), nil
	}

	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant_id required")
	}

	opts := &periscopeclient.CursorPaginationOpts{
		First: int32(pagination.DefaultLimit),
	}
	if first != nil {
		opts.First = int32(pagination.ClampLimit(*first))
	}
	if after != nil && *after != "" {
		opts.After = after
	}
	if last != nil {
		opts.Last = int32(pagination.ClampLimit(*last))
	}
	if before != nil && *before != "" {
		opts.Before = before
	}

	var timeOpts *periscopeclient.TimeRangeOpts
	if timeRange != nil {
		timeOpts = &periscopeclient.TimeRangeOpts{
			StartTime: timeRange.Start,
			EndTime:   timeRange.End,
		}
	}

	response, err := r.Clients.Periscope.GetViewerMetrics(ctx, tenantID, stream, timeOpts, opts)
	if err != nil {
		return nil, err
	}

	streamIDs := make([]string, len(response.Sessions))
	for i, s := range response.Sessions {
		streamIDs[i] = s.GetStreamId()
	}
	loaders.PreloadStreams(ctx, tenantID, streamIDs)

	edges := make([]*model.ViewerSessionEdge, len(response.Sessions))
	for i, session := range response.Sessions {
		cursor := encodeStableCursor(cursorTimeFromProto(session.Timestamp), session.SessionId)
		edges[i] = &model.ViewerSessionEdge{
			Cursor: cursor,
			Node:   session,
		}
	}

	totalCount := 0
	hasMore := false
	if response.Pagination != nil {
		totalCount = int(response.Pagination.TotalCount)
		hasMore = response.Pagination.HasNextPage
	}

	pageInfo := &model.PageInfo{
		HasPreviousPage: response.Pagination != nil && response.Pagination.HasPreviousPage,
		HasNextPage:     hasMore,
	}
	if response.Pagination != nil {
		pageInfo.StartCursor = response.Pagination.StartCursor
		pageInfo.EndCursor = response.Pagination.EndCursor
	}

	edgeNodes := make([]*pb.ViewerSession, 0, len(edges))
	for _, edge := range edges {
		if edge != nil {
			edgeNodes = append(edgeNodes, edge.Node)
		}
	}

	return &model.ViewerSessionsConnection{
		Edges:      edges,
		Nodes:      edgeNodes,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}, nil
}

// DoGetViewerGeographicsConnection returns connection-style payload for viewer geographic events.
// This wraps individual connection events with location data for map visualizations.
func (r *Resolver) DoGetViewerGeographicsConnection(ctx context.Context, stream *string, timeRange *model.TimeRangeInput, first *int, after *string, last *int, before *string) (*model.ViewerGeographicsConnection, error) {
	if err := middleware.RequirePermission(ctx, "analytics:read"); err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		events := demo.GenerateViewerGeographics()
		edges := make([]*model.ViewerGeographicEdge, len(events))
		for i, ev := range events {
			cursor := encodeStableCursor(cursorTimeFromProto(ev.Timestamp), ev.EventId)
			edges[i] = &model.ViewerGeographicEdge{
				Cursor: cursor,
				Node:   ev,
			}
		}
		pageInfo := &model.PageInfo{
			HasPreviousPage: false,
			HasNextPage:     false,
		}
		if len(edges) > 0 {
			pageInfo.StartCursor = &edges[0].Cursor
			pageInfo.EndCursor = &edges[len(edges)-1].Cursor
		}
		edgeNodes := make([]*pb.ConnectionEvent, 0, len(edges))
		for _, edge := range edges {
			if edge != nil {
				edgeNodes = append(edgeNodes, edge.Node)
			}
		}

		return &model.ViewerGeographicsConnection{
			Edges:      edges,
			Nodes:      edgeNodes,
			PageInfo:   pageInfo,
			TotalCount: len(events),
		}, nil
	}

	if tenantIDFromContext(ctx) == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	normalizedStreamID, err := normalizeStreamIDPtr(stream)
	if err != nil {
		return nil, err
	}
	stream = normalizedStreamID

	// Build cursor pagination options
	opts := &periscopeclient.CursorPaginationOpts{
		First: int32(pagination.DefaultLimit),
	}
	if first != nil {
		opts.First = int32(pagination.ClampLimit(*first))
	}
	if after != nil && *after != "" {
		opts.After = after
	}
	if last != nil {
		opts.Last = int32(pagination.ClampLimit(*last))
	}
	if before != nil && *before != "" {
		opts.Before = before
	}

	startTime, endTime := parseTimeRange(timeRange)
	skipCache := false

	// Fetch connection events (which contain geo data)
	response, err := r.loadConnectionEvents(ctx, stream, startTime, endTime, opts, skipCache)
	if err != nil {
		return nil, err
	}

	streamIDs := make([]string, len(response.Events))
	for i, e := range response.Events {
		streamIDs[i] = e.GetStreamId()
	}
	loaders.PreloadStreams(ctx, ctxkeys.GetTenantID(ctx), streamIDs)

	// Build edges from proto response - ConnectionEvent contains geo fields
	edges := make([]*model.ViewerGeographicEdge, len(response.Events))
	for i, event := range response.Events {
		cursorTime := time.Now()
		if event.Timestamp != nil {
			cursorTime = event.Timestamp.AsTime()
		}
		cursor := encodeStableCursor(cursorTime, event.EventId)
		edges[i] = &model.ViewerGeographicEdge{
			Cursor: cursor,
			Node:   event,
		}
	}

	// Build page info
	totalCount := 0
	hasMore := false
	if response.Pagination != nil {
		totalCount = int(response.Pagination.TotalCount)
		hasMore = response.Pagination.HasNextPage
	}

	pageInfo := &model.PageInfo{
		HasPreviousPage: response.Pagination != nil && response.Pagination.HasPreviousPage,
		HasNextPage:     hasMore,
	}
	if response.Pagination != nil {
		pageInfo.StartCursor = response.Pagination.StartCursor
		pageInfo.EndCursor = response.Pagination.EndCursor
	}

	edgeNodes := make([]*pb.ConnectionEvent, 0, len(edges))
	for _, edge := range edges {
		if edge != nil {
			edgeNodes = append(edgeNodes, edge.Node)
		}
	}

	return &model.ViewerGeographicsConnection{
		Edges:      edges,
		Nodes:      edgeNodes,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}, nil
}

// DoGetViewerTimeSeriesConnection returns connection-style payload for viewer count time series.
// This is used for charting viewer counts over time intervals (5m, 15m, 1h, 1d).
func (r *Resolver) DoGetViewerTimeSeriesConnection(ctx context.Context, streamId string, timeRange *model.TimeRangeInput, interval *string, first *int, after *string, last *int, before *string) (*model.ViewerTimeSeriesConnection, error) {
	if err := middleware.RequirePermission(ctx, "analytics:read"); err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		buckets := demo.GenerateViewerTimeSeries()
		edges := make([]*model.ViewerCountBucketEdge, len(buckets))
		for i, bucket := range buckets {
			cursorID := bucket.StreamId
			if cursorID == "" {
				cursorID = "all"
			}
			cursor := encodeStableCursor(cursorTimeFromProto(bucket.Timestamp), cursorID)
			edges[i] = &model.ViewerCountBucketEdge{
				Cursor: cursor,
				Node:   bucket,
			}
		}
		pageInfo := &model.PageInfo{
			HasPreviousPage: false,
			HasNextPage:     false,
		}
		if len(edges) > 0 {
			pageInfo.StartCursor = &edges[0].Cursor
			pageInfo.EndCursor = &edges[len(edges)-1].Cursor
		}
		edgeNodes := make([]*pb.ViewerCountBucket, 0, len(edges))
		for _, edge := range edges {
			if edge != nil {
				edgeNodes = append(edgeNodes, edge.Node)
			}
		}

		return &model.ViewerTimeSeriesConnection{
			Edges:      edges,
			Nodes:      edgeNodes,
			PageInfo:   pageInfo,
			TotalCount: len(buckets),
		}, nil
	}

	normalizedID, err := normalizeStreamID(streamId)
	if err != nil {
		return nil, err
	}
	streamId = normalizedID

	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant_id required")
	}

	// Default interval to 5m
	intervalStr := "5m"
	if interval != nil && *interval != "" {
		intervalStr = *interval
	}

	// Build time range options
	var timeOpts *periscopeclient.TimeRangeOpts
	if timeRange != nil {
		timeOpts = &periscopeclient.TimeRangeOpts{
			StartTime: timeRange.Start,
			EndTime:   timeRange.End,
		}
	}

	// Fetch viewer count time series from Periscope
	response, err := r.Clients.Periscope.GetViewerCountTimeSeries(ctx, tenantID, &streamId, timeOpts, intervalStr)
	if err != nil {
		return nil, fmt.Errorf("failed to get viewer time series: %w", err)
	}

	// Client-side pagination since the API doesn't support it natively
	buckets := response.Buckets
	totalCount := len(buckets)

	// Apply pagination
	startIdx := 0
	endIdx := totalCount
	limit := pagination.DefaultLimit
	if first != nil {
		limit = pagination.ClampLimit(*first)
	}

	// Handle cursor-based pagination
	if after != nil && *after != "" {
		cursor, err := decodeStableCursor(*after)
		if err == nil && cursor != nil {
			// Find position after cursor
			for i, bucket := range buckets {
				if bucket.Timestamp == nil {
					continue
				}
				bucketTime := bucket.Timestamp.AsTime()
				if bucketTime.Equal(cursor.Timestamp) || bucketTime.After(cursor.Timestamp) {
					startIdx = i + 1
					break
				}
			}
		}
	}

	if startIdx+limit < endIdx {
		endIdx = startIdx + limit
	}

	// Slice the buckets
	if startIdx >= totalCount {
		buckets = []*pb.ViewerCountBucket{}
	} else {
		buckets = buckets[startIdx:endIdx]
	}

	loaders.PreloadStreams(ctx, tenantID, []string{streamId})

	// Build edges
	edges := make([]*model.ViewerCountBucketEdge, len(buckets))
	for i, bucket := range buckets {
		cursorID := bucket.StreamId
		if cursorID == "" {
			cursorID = "all"
		}
		cursor := encodeStableCursor(cursorTimeFromProto(bucket.Timestamp), cursorID)
		edges[i] = &model.ViewerCountBucketEdge{
			Cursor: cursor,
			Node:   bucket,
		}
	}

	// Build page info
	pageInfo := &model.PageInfo{
		HasPreviousPage: startIdx > 0,
		HasNextPage:     endIdx < totalCount,
	}
	if len(edges) > 0 {
		pageInfo.StartCursor = &edges[0].Cursor
		pageInfo.EndCursor = &edges[len(edges)-1].Cursor
	}

	edgeNodes := make([]*pb.ViewerCountBucket, 0, len(edges))
	for _, edge := range edges {
		if edge != nil {
			edgeNodes = append(edgeNodes, edge.Node)
		}
	}

	return &model.ViewerTimeSeriesConnection{
		Edges:      edges,
		Nodes:      edgeNodes,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}, nil
}

// DoGetProcessingUsageConnection returns transcoding/processing usage records with cursor pagination.
// This exposes data from the process_billing table (Livepeer Gateway and MistProcAV events).
func (r *Resolver) DoGetProcessingUsageConnection(ctx context.Context, streamName *string, processType *string, timeRange *model.TimeRangeInput, first *int, after *string, last *int, before *string, noCache *bool) (*model.ProcessingUsageConnection, error) {
	if err := middleware.RequirePermission(ctx, "analytics:read"); err != nil {
		return nil, err
	}
	normalizedStreamID, err := normalizeStreamIDPtr(streamName)
	if err != nil {
		return nil, err
	}
	streamName = normalizedStreamID

	if middleware.IsDemoMode(ctx) {
		return demo.GenerateProcessingUsageConnection(streamName, processType), nil
	}

	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant_id required")
	}

	opts := &periscopeclient.CursorPaginationOpts{
		First: int32(pagination.DefaultLimit),
	}
	if first != nil {
		opts.First = int32(pagination.ClampLimit(*first))
	}
	if after != nil && *after != "" {
		opts.After = after
	}
	if last != nil {
		opts.Last = int32(pagination.ClampLimit(*last))
	}
	if before != nil && *before != "" {
		opts.Before = before
	}

	var timeOpts *periscopeclient.TimeRangeOpts
	if timeRange != nil {
		timeOpts = &periscopeclient.TimeRangeOpts{
			StartTime: timeRange.Start,
			EndTime:   timeRange.End,
		}
	}

	// Call Periscope to get processing usage (summaryOnly=false for detailed records + summaries)
	response, err := r.Clients.Periscope.GetProcessingUsage(ctx, tenantID, streamName, processType, timeOpts, opts, false)
	if err != nil {
		return nil, err
	}

	streamIDs := make([]string, len(response.Records))
	for i, r := range response.Records {
		streamIDs[i] = r.GetStreamId()
	}
	loaders.PreloadStreams(ctx, tenantID, streamIDs)

	// Build edges from records (proto → model via binding)
	edges := make([]*model.ProcessingUsageEdge, len(response.Records))
	for i, record := range response.Records {
		cursor := encodeStableCursor(cursorTimeFromProto(record.Timestamp), record.Id)
		edges[i] = &model.ProcessingUsageEdge{
			Cursor: cursor,
			Node:   record,
		}
	}

	// Build summaries (proto → model via binding)
	summaries := make([]*pb.ProcessingUsageSummary, len(response.Summaries))
	copy(summaries, response.Summaries)

	totalCount := 0
	hasMore := false
	if response.Pagination != nil {
		totalCount = int(response.Pagination.TotalCount)
		hasMore = response.Pagination.HasNextPage
	}

	pageInfo := &model.PageInfo{
		HasPreviousPage: response.Pagination != nil && response.Pagination.HasPreviousPage,
		HasNextPage:     hasMore,
	}
	if response.Pagination != nil {
		pageInfo.StartCursor = response.Pagination.StartCursor
		pageInfo.EndCursor = response.Pagination.EndCursor
	}

	edgeNodes := make([]*pb.ProcessingUsageRecord, 0, len(edges))
	for _, edge := range edges {
		if edge != nil {
			edgeNodes = append(edgeNodes, edge.Node)
		}
	}

	return &model.ProcessingUsageConnection{
		Edges:      edges,
		Nodes:      edgeNodes,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
		Summaries:  summaries,
	}, nil
}

// DoGetRebufferingEventsConnection returns rebuffering events with cursor pagination.
func (r *Resolver) DoGetRebufferingEventsConnection(ctx context.Context, streamId *string, nodeID *string, timeRange *model.TimeRangeInput, first *int, after *string, last *int, before *string, noCache *bool) (*model.RebufferingEventsConnection, error) {
	if err := middleware.RequirePermission(ctx, "analytics:read"); err != nil {
		return nil, err
	}
	normalizedStreamID, err := normalizeStreamIDPtr(streamId)
	if err != nil {
		return nil, err
	}
	streamId = normalizedStreamID

	if middleware.IsDemoMode(ctx) {
		return demo.GenerateRebufferingEventsConnection(streamId), nil
	}

	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant_id required")
	}

	opts := &periscopeclient.CursorPaginationOpts{
		First: int32(pagination.DefaultLimit),
	}
	if first != nil {
		opts.First = int32(pagination.ClampLimit(*first))
	}
	if after != nil && *after != "" {
		opts.After = after
	}
	if last != nil {
		opts.Last = int32(pagination.ClampLimit(*last))
	}
	if before != nil && *before != "" {
		opts.Before = before
	}

	var timeOpts *periscopeclient.TimeRangeOpts
	if timeRange != nil {
		timeOpts = &periscopeclient.TimeRangeOpts{
			StartTime: timeRange.Start,
			EndTime:   timeRange.End,
		}
	}

	response, err := r.Clients.Periscope.GetRebufferingEvents(ctx, tenantID, streamId, nodeID, timeOpts, opts)
	if err != nil {
		return nil, err
	}

	streamIDs := make([]string, len(response.Events))
	for i, e := range response.Events {
		streamIDs[i] = e.GetStreamId()
	}
	loaders.PreloadStreams(ctx, tenantID, streamIDs)

	edges := make([]*model.RebufferingEventEdge, len(response.Events))
	for i, event := range response.Events {
		cursorTime := time.Now()
		if event.Timestamp != nil {
			cursorTime = event.Timestamp.AsTime()
		}
		cursor := encodeStableCursor(cursorTime, event.Id)
		edges[i] = &model.RebufferingEventEdge{
			Cursor: cursor,
			Node:   event,
		}
	}

	totalCount := 0
	hasMore := false
	if response.Pagination != nil {
		totalCount = int(response.Pagination.TotalCount)
		hasMore = response.Pagination.HasNextPage
	}

	pageInfo := &model.PageInfo{
		HasPreviousPage: response.Pagination != nil && response.Pagination.HasPreviousPage,
		HasNextPage:     hasMore,
	}
	if response.Pagination != nil {
		pageInfo.StartCursor = response.Pagination.StartCursor
		pageInfo.EndCursor = response.Pagination.EndCursor
	}

	edgeNodes := make([]*pb.RebufferingEvent, 0, len(edges))
	for _, edge := range edges {
		if edge != nil {
			edgeNodes = append(edgeNodes, edge.Node)
		}
	}

	return &model.RebufferingEventsConnection{
		Edges:      edges,
		Nodes:      edgeNodes,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}, nil
}

// DoGetTenantAnalyticsDailyConnection returns daily tenant analytics with cursor pagination.
func (r *Resolver) DoGetTenantAnalyticsDailyConnection(ctx context.Context, timeRange *model.TimeRangeInput, first *int, after *string, last *int, before *string, noCache *bool) (*model.TenantAnalyticsDailyConnection, error) {
	if err := middleware.RequirePermission(ctx, "analytics:read"); err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateTenantAnalyticsDailyConnection(), nil
	}

	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant_id required")
	}

	opts := &periscopeclient.CursorPaginationOpts{
		First: int32(pagination.DefaultLimit),
	}
	if first != nil {
		opts.First = int32(pagination.ClampLimit(*first))
	}
	if after != nil && *after != "" {
		opts.After = after
	}
	if last != nil {
		opts.Last = int32(pagination.ClampLimit(*last))
	}
	if before != nil && *before != "" {
		opts.Before = before
	}

	var timeOpts *periscopeclient.TimeRangeOpts
	if timeRange != nil {
		timeOpts = &periscopeclient.TimeRangeOpts{
			StartTime: timeRange.Start,
			EndTime:   timeRange.End,
		}
	}

	response, err := r.Clients.Periscope.GetTenantAnalyticsDaily(ctx, tenantID, timeOpts, opts)
	if err != nil {
		return nil, err
	}

	edges := make([]*model.TenantAnalyticsDailyEdge, len(response.Records))
	for i, record := range response.Records {
		cursor := encodeStableCursor(cursorTimeFromProto(record.Day), "")
		edges[i] = &model.TenantAnalyticsDailyEdge{
			Cursor: cursor,
			Node:   record,
		}
	}

	totalCount := 0
	hasMore := false
	if response.Pagination != nil {
		totalCount = int(response.Pagination.TotalCount)
		hasMore = response.Pagination.HasNextPage
	}

	pageInfo := &model.PageInfo{
		HasPreviousPage: response.Pagination != nil && response.Pagination.HasPreviousPage,
		HasNextPage:     hasMore,
	}
	if response.Pagination != nil {
		pageInfo.StartCursor = response.Pagination.StartCursor
		pageInfo.EndCursor = response.Pagination.EndCursor
	}

	edgeNodes := make([]*pb.TenantAnalyticsDaily, 0, len(edges))
	for _, edge := range edges {
		if edge != nil {
			edgeNodes = append(edgeNodes, edge.Node)
		}
	}

	return &model.TenantAnalyticsDailyConnection{
		Edges:      edges,
		Nodes:      edgeNodes,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}, nil
}

// DoGetStreamAnalyticsDailyConnection returns daily stream analytics with cursor pagination.
func (r *Resolver) DoGetStreamAnalyticsDailyConnection(ctx context.Context, streamId *string, timeRange *model.TimeRangeInput, first *int, after *string, last *int, before *string, noCache *bool) (*model.StreamAnalyticsDailyConnection, error) {
	if err := middleware.RequirePermission(ctx, "analytics:read"); err != nil {
		return nil, err
	}
	normalizedStreamID, err := normalizeStreamIDPtr(streamId)
	if err != nil {
		return nil, err
	}
	streamId = normalizedStreamID

	if middleware.IsDemoMode(ctx) {
		return demo.GenerateStreamAnalyticsDailyConnection(streamId), nil
	}

	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant_id required")
	}

	opts := &periscopeclient.CursorPaginationOpts{
		First: int32(pagination.DefaultLimit),
	}
	if first != nil {
		opts.First = int32(pagination.ClampLimit(*first))
	}
	if after != nil && *after != "" {
		opts.After = after
	}
	if last != nil {
		opts.Last = int32(pagination.ClampLimit(*last))
	}
	if before != nil && *before != "" {
		opts.Before = before
	}

	var timeOpts *periscopeclient.TimeRangeOpts
	if timeRange != nil {
		timeOpts = &periscopeclient.TimeRangeOpts{
			StartTime: timeRange.Start,
			EndTime:   timeRange.End,
		}
	}

	response, err := r.Clients.Periscope.GetStreamAnalyticsDaily(ctx, tenantID, streamId, timeOpts, opts)
	if err != nil {
		return nil, err
	}

	streamIDs := make([]string, len(response.Records))
	for i, r := range response.Records {
		streamIDs[i] = r.GetStreamId()
	}
	loaders.PreloadStreams(ctx, tenantID, streamIDs)

	edges := make([]*model.StreamAnalyticsDailyEdge, len(response.Records))
	for i, record := range response.Records {
		cursor := encodeStableCursor(cursorTimeFromProto(record.Day), record.StreamId)
		edges[i] = &model.StreamAnalyticsDailyEdge{
			Cursor: cursor,
			Node:   record,
		}
	}

	totalCount := 0
	hasMore := false
	if response.Pagination != nil {
		totalCount = int(response.Pagination.TotalCount)
		hasMore = response.Pagination.HasNextPage
	}

	pageInfo := &model.PageInfo{
		HasPreviousPage: response.Pagination != nil && response.Pagination.HasPreviousPage,
		HasNextPage:     hasMore,
	}
	if response.Pagination != nil {
		pageInfo.StartCursor = response.Pagination.StartCursor
		pageInfo.EndCursor = response.Pagination.EndCursor
	}

	edgeNodes := make([]*pb.StreamAnalyticsDaily, 0, len(edges))
	for _, edge := range edges {
		if edge != nil {
			edgeNodes = append(edgeNodes, edge.Node)
		}
	}

	return &model.StreamAnalyticsDailyConnection{
		Edges:      edges,
		Nodes:      edgeNodes,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}, nil
}

// DoGetAPIUsageConnection returns API usage records and daily summaries
func (r *Resolver) DoGetAPIUsageConnection(ctx context.Context, authType *string, operationType *string, operationName *string, timeRange *model.TimeRangeInput, first *int, after *string, last *int, before *string, noCache *bool) (*model.APIUsageConnection, error) {
	if err := middleware.RequirePermission(ctx, "analytics:read"); err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateAPIUsageConnection(authType, operationType, operationName), nil
	}

	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant_id required")
	}

	opts := &periscopeclient.CursorPaginationOpts{
		First: int32(pagination.DefaultLimit),
	}
	if first != nil {
		opts.First = int32(pagination.ClampLimit(*first))
	}
	if after != nil && *after != "" {
		opts.After = after
	}
	if last != nil {
		opts.Last = int32(pagination.ClampLimit(*last))
	}
	if before != nil && *before != "" {
		opts.Before = before
	}

	var timeOpts *periscopeclient.TimeRangeOpts
	if timeRange != nil {
		timeOpts = &periscopeclient.TimeRangeOpts{
			StartTime: timeRange.Start,
			EndTime:   timeRange.End,
		}
	}

	response, err := r.Clients.Periscope.GetAPIUsage(ctx, tenantID, authType, operationType, operationName, timeOpts, opts, false)
	if err != nil {
		return nil, err
	}

	edges := make([]*model.APIUsageEdge, len(response.Records))
	for i, record := range response.Records {
		cursorID := fmt.Sprintf("%s|%s|%s", record.AuthType, record.OperationType, record.OperationName)
		cursor := encodeStableCursor(cursorTimeFromProto(record.Timestamp), cursorID)
		edges[i] = &model.APIUsageEdge{
			Cursor: cursor,
			Node:   record,
		}
	}

	summaries := make([]*pb.APIUsageSummary, len(response.Summaries))
	copy(summaries, response.Summaries)
	operationSummaries := make([]*pb.APIUsageOperationSummary, len(response.OperationSummaries))
	copy(operationSummaries, response.OperationSummaries)

	totalCount := 0
	hasMore := false
	if response.Pagination != nil {
		totalCount = int(response.Pagination.TotalCount)
		hasMore = response.Pagination.HasNextPage
	}

	pageInfo := &model.PageInfo{
		HasPreviousPage: response.Pagination != nil && response.Pagination.HasPreviousPage,
		HasNextPage:     hasMore,
	}
	if response.Pagination != nil {
		pageInfo.StartCursor = response.Pagination.StartCursor
		pageInfo.EndCursor = response.Pagination.EndCursor
	}

	edgeNodes := make([]*pb.APIUsageRecord, 0, len(edges))
	for _, edge := range edges {
		if edge != nil {
			edgeNodes = append(edgeNodes, edge.Node)
		}
	}

	return &model.APIUsageConnection{
		Edges:              edges,
		Nodes:              edgeNodes,
		PageInfo:           pageInfo,
		TotalCount:         totalCount,
		Summaries:          summaries,
		OperationSummaries: operationSummaries,
	}, nil
}

// DoGetStreamAnalyticsSummariesConnection returns pre-aggregated summaries for multiple streams.
func (r *Resolver) DoGetStreamAnalyticsSummariesConnection(ctx context.Context, page *model.ConnectionInput, timeRange *model.TimeRangeInput, sortBy *pb.StreamSummarySortField, sortOrder *pb.SortOrder) (*model.StreamAnalyticsSummaryConnection, error) {
	if err := middleware.RequirePermission(ctx, "analytics:read"); err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateStreamAnalyticsSummariesConnection(), nil
	}

	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant_id required")
	}

	opts := &periscopeclient.CursorPaginationOpts{
		First: int32(pagination.DefaultLimit),
	}
	if page != nil {
		if page.First != nil {
			opts.First = int32(pagination.ClampLimit(*page.First))
		}
		if page.After != nil && *page.After != "" {
			opts.After = page.After
		}
		if page.Last != nil {
			opts.Last = int32(pagination.ClampLimit(*page.Last))
		}
		if page.Before != nil && *page.Before != "" {
			opts.Before = page.Before
		}
	}

	var timeOpts *periscopeclient.TimeRangeOpts
	if timeRange != nil {
		timeOpts = &periscopeclient.TimeRangeOpts{
			StartTime: timeRange.Start,
			EndTime:   timeRange.End,
		}
	}

	// Map proto enums to client types
	clientSortBy := periscopeclient.StreamSummarySortFieldEgressGB
	if sortBy != nil {
		switch *sortBy {
		case pb.StreamSummarySortField_STREAM_SUMMARY_SORT_FIELD_UNIQUE_VIEWERS:
			clientSortBy = periscopeclient.StreamSummarySortFieldUniqueViewers
		case pb.StreamSummarySortField_STREAM_SUMMARY_SORT_FIELD_TOTAL_VIEWS:
			clientSortBy = periscopeclient.StreamSummarySortFieldTotalViews
		case pb.StreamSummarySortField_STREAM_SUMMARY_SORT_FIELD_VIEWER_HOURS:
			clientSortBy = periscopeclient.StreamSummarySortFieldViewerHours
		}
	}

	clientSortOrder := periscopeclient.SortOrderDesc
	if sortOrder != nil && *sortOrder == pb.SortOrder_SORT_ORDER_ASC {
		clientSortOrder = periscopeclient.SortOrderAsc
	}

	response, err := r.Clients.Periscope.GetStreamAnalyticsSummaries(ctx, tenantID, timeOpts, clientSortBy, clientSortOrder, opts)
	if err != nil {
		return nil, err
	}

	streamIDs := make([]string, len(response.Summaries))
	for i, s := range response.Summaries {
		streamIDs[i] = s.GetStreamId()
	}
	loaders.PreloadStreams(ctx, tenantID, streamIDs)

	// Build keyset cursor for each edge using sort field value + stream_id
	edges := make([]*model.StreamAnalyticsSummaryEdge, len(response.Summaries))
	for i, summary := range response.Summaries {
		cursor := buildStreamSummaryCursor(summary, clientSortBy)
		edges[i] = &model.StreamAnalyticsSummaryEdge{
			Cursor: cursor,
			Node:   summary,
		}
	}

	totalCount := 0
	hasMore := false
	if response.Pagination != nil {
		totalCount = int(response.Pagination.TotalCount)
		hasMore = response.Pagination.HasNextPage
	}

	pageInfo := &model.PageInfo{
		HasPreviousPage: response.Pagination != nil && response.Pagination.HasPreviousPage,
		HasNextPage:     hasMore,
	}
	if response.Pagination != nil {
		pageInfo.StartCursor = response.Pagination.StartCursor
		pageInfo.EndCursor = response.Pagination.EndCursor
	}

	edgeNodes := make([]*pb.StreamAnalyticsSummary, 0, len(edges))
	for _, edge := range edges {
		if edge != nil {
			edgeNodes = append(edgeNodes, edge.Node)
		}
	}

	return &model.StreamAnalyticsSummaryConnection{
		Edges:      edges,
		Nodes:      edgeNodes,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}, nil
}

// buildStreamSummaryCursor creates a keyset cursor for a stream summary.
// Uses raw integer fields (RangeEgressBytes, RangeViewerSeconds) for precision.
func buildStreamSummaryCursor(summary *pb.StreamAnalyticsSummary, sortBy periscopeclient.StreamSummarySortField) string {
	var sortKey int64
	switch sortBy {
	case periscopeclient.StreamSummarySortFieldEgressGB:
		sortKey = summary.RangeEgressBytes
	case periscopeclient.StreamSummarySortFieldViewerHours:
		sortKey = summary.RangeViewerSeconds
	case periscopeclient.StreamSummarySortFieldUniqueViewers:
		sortKey = summary.RangeUniqueViewers
	case periscopeclient.StreamSummarySortFieldTotalViews:
		sortKey = summary.RangeTotalViews
	default:
		sortKey = summary.RangeEgressBytes
	}
	return pagination.EncodeCursorWithSortKey(sortKey, summary.StreamId)
}

// DoGetRoutingEfficiency returns pre-aggregated routing decision stats.
func (r *Resolver) DoGetRoutingEfficiency(ctx context.Context, streamID *string, timeRange *model.TimeRangeInput, noCache *bool) (*model.RoutingEfficiency, error) {
	if err := middleware.RequirePermission(ctx, "analytics:read"); err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateRoutingEfficiency(), nil
	}

	tenantID := tenantIDFromContext(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	startTime, endTime := parseTimeRange(timeRange)
	skipCache := noCache != nil && *noCache

	streamKey := ""
	if streamID != nil {
		streamKey = *streamID
	}
	keyParts := []string{tenantID, streamKey, timeKey(startTime), timeKey(endTime)}

	val, err := r.fetchPeriscopeWithOptions(ctx, "routing_efficiency", keyParts, func(ctx context.Context) (interface{}, error) {
		return r.Clients.Periscope.GetRoutingEfficiency(ctx, tenantID, streamID, timePtrsToTimeRangeOpts(startTime, endTime))
	}, skipCache)
	if err != nil {
		return nil, err
	}
	resp, ok := val.(*pb.GetRoutingEfficiencyResponse)
	if !ok {
		return nil, fmt.Errorf("unexpected response type for routing efficiency: %T", val)
	}

	s := resp.GetSummary()
	countries := make([]*model.RoutingCountryStat, 0, len(s.GetTopCountries()))
	for _, c := range s.GetTopCountries() {
		countries = append(countries, &model.RoutingCountryStat{
			CountryCode:  c.CountryCode,
			RequestCount: int(c.RequestCount),
		})
	}

	return &model.RoutingEfficiency{
		TotalDecisions:     int(s.TotalDecisions),
		SuccessCount:       int(s.SuccessCount),
		SuccessRate:        s.SuccessRate,
		AvgRoutingDistance: s.AvgRoutingDistance,
		AvgLatencyMs:       s.AvgLatencyMs,
		TopCountries:       countries,
	}, nil
}

// DoGetStreamHealthSummary returns pre-aggregated stream health stats.
func (r *Resolver) DoGetStreamHealthSummary(ctx context.Context, streamID *string, timeRange *model.TimeRangeInput, noCache *bool) (*model.StreamHealthSummary, error) {
	if err := middleware.RequirePermission(ctx, "analytics:read"); err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateStreamHealthSummary(), nil
	}

	tenantID := tenantIDFromContext(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	startTime, endTime := parseTimeRange(timeRange)
	skipCache := noCache != nil && *noCache

	streamKey := ""
	if streamID != nil {
		streamKey = *streamID
	}
	keyParts := []string{tenantID, streamKey, timeKey(startTime), timeKey(endTime)}

	val, err := r.fetchPeriscopeWithOptions(ctx, "stream_health_summary", keyParts, func(ctx context.Context) (interface{}, error) {
		return r.Clients.Periscope.GetStreamHealthSummary(ctx, tenantID, streamID, timePtrsToTimeRangeOpts(startTime, endTime))
	}, skipCache)
	if err != nil {
		return nil, err
	}
	resp, ok := val.(*pb.GetStreamHealthSummaryResponse)
	if !ok {
		return nil, fmt.Errorf("unexpected response type for stream health summary: %T", val)
	}

	s := resp.GetSummary()
	var tier *string
	if s.CurrentQualityTier != "" {
		tier = &s.CurrentQualityTier
	}

	return &model.StreamHealthSummary{
		AvgBitrate:         s.AvgBitrate,
		AvgFps:             s.AvgFps,
		AvgBufferHealth:    s.AvgBufferHealth,
		TotalRebufferCount: int(s.TotalRebufferCount),
		TotalIssueCount:    int(s.TotalIssueCount),
		SampleCount:        int(s.SampleCount),
		HasActiveIssues:    s.HasActiveIssues,
		CurrentQualityTier: tier,
	}, nil
}

// DoGetClientQoeSummary returns pre-aggregated client QoE stats.
func (r *Resolver) DoGetClientQoeSummary(ctx context.Context, streamID *string, timeRange *model.TimeRangeInput, noCache *bool) (*model.ClientQoeSummary, error) {
	if err := middleware.RequirePermission(ctx, "analytics:read"); err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateClientQoeSummary(), nil
	}

	tenantID := tenantIDFromContext(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	startTime, endTime := parseTimeRange(timeRange)
	skipCache := noCache != nil && *noCache

	streamKey := ""
	if streamID != nil {
		streamKey = *streamID
	}
	keyParts := []string{tenantID, streamKey, timeKey(startTime), timeKey(endTime)}

	val, err := r.fetchPeriscopeWithOptions(ctx, "client_qoe_summary", keyParts, func(ctx context.Context) (interface{}, error) {
		return r.Clients.Periscope.GetClientQoeSummary(ctx, tenantID, streamID, timePtrsToTimeRangeOpts(startTime, endTime))
	}, skipCache)
	if err != nil {
		return nil, err
	}
	resp, ok := val.(*pb.GetClientQoeSummaryResponse)
	if !ok {
		return nil, fmt.Errorf("unexpected response type for client QoE summary: %T", val)
	}

	s := resp.GetSummary()
	return &model.ClientQoeSummary{
		AvgPacketLossRate:   s.AvgPacketLossRate,
		PeakPacketLossRate:  s.PeakPacketLossRate,
		AvgBandwidthIn:      s.AvgBandwidthIn,
		AvgBandwidthOut:     s.AvgBandwidthOut,
		AvgConnectionTime:   s.AvgConnectionTime,
		TotalActiveSessions: int(s.TotalActiveSessions),
	}, nil
}

// DoGetClusterTrafficMatrix returns cross-cluster routing traffic.
func (r *Resolver) DoGetClusterTrafficMatrix(ctx context.Context, timeRange *model.TimeRangeInput, noCache *bool) ([]*pb.ClusterPairTraffic, error) {
	if err := middleware.RequirePermission(ctx, "analytics:read"); err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateClusterTrafficMatrix(), nil
	}

	tenantID := tenantIDFromContext(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	startTime, endTime := parseTimeRange(timeRange)
	skipCache := noCache != nil && *noCache
	keyParts := []string{tenantID, timeKey(startTime), timeKey(endTime)}

	val, err := r.fetchPeriscopeWithOptions(ctx, "cluster_traffic_matrix", keyParts, func(ctx context.Context) (interface{}, error) {
		return r.Clients.Periscope.GetClusterTrafficMatrix(ctx, tenantID, timePtrsToTimeRangeOpts(startTime, endTime))
	}, skipCache)
	if err != nil {
		return nil, err
	}
	resp, ok := val.(*pb.GetClusterTrafficMatrixResponse)
	if !ok {
		return nil, fmt.Errorf("unexpected response type for cluster traffic matrix: %T", val)
	}

	pairs := resp.GetPairs()
	r.enrichTrafficMatrixGeo(ctx, pairs)
	return pairs, nil
}

func computeClusterGeo(nodes []*pb.InfrastructureNode, clusterFilter map[string]struct{}) map[string]struct{ lat, lon float64 } {
	type acc struct {
		latSum, lonSum float64
		n              int
	}
	accum := make(map[string]*acc)
	for _, n := range nodes {
		if n == nil || n.ClusterId == "" {
			continue
		}
		if len(clusterFilter) > 0 {
			if _, ok := clusterFilter[n.ClusterId]; !ok {
				continue
			}
		}
		if n.Latitude == nil || n.Longitude == nil {
			continue
		}
		a, ok := accum[n.ClusterId]
		if !ok {
			a = &acc{}
			accum[n.ClusterId] = a
		}
		a.latSum += *n.Latitude
		a.lonSum += *n.Longitude
		a.n++
	}

	out := make(map[string]struct{ lat, lon float64 }, len(accum))
	for cid, a := range accum {
		if a.n > 0 {
			out[cid] = struct{ lat, lon float64 }{lat: a.latSum / float64(a.n), lon: a.lonSum / float64(a.n)}
		}
	}
	return out
}

func applyTrafficGeo(pairs []*pb.ClusterPairTraffic, clusterGeo map[string]struct{ lat, lon float64 }) {
	for _, p := range pairs {
		if p == nil {
			continue
		}
		if g, ok := clusterGeo[p.ClusterId]; ok {
			p.LocalLatitude = &g.lat
			p.LocalLongitude = &g.lon
		}
		if p.RemoteClusterId != "" {
			if g, ok := clusterGeo[p.RemoteClusterId]; ok {
				p.RemoteLatitude = &g.lat
				p.RemoteLongitude = &g.lon
			}
		}
	}
}

// enrichTrafficMatrixGeo attaches cluster lat/lon to traffic matrix pairs
// by averaging node geo per cluster.
func (r *Resolver) enrichTrafficMatrixGeo(ctx context.Context, pairs []*pb.ClusterPairTraffic) {
	if len(pairs) == 0 {
		return
	}
	clusterIDs := make(map[string]struct{}, len(pairs)*2)
	for _, p := range pairs {
		if p == nil {
			continue
		}
		if p.ClusterId != "" {
			clusterIDs[p.ClusterId] = struct{}{}
		}
		if p.RemoteClusterId != "" {
			clusterIDs[p.RemoteClusterId] = struct{}{}
		}
	}

	nodes := make([]*pb.InfrastructureNode, 0)
	for clusterID := range clusterIDs {
		var after *string
		for {
			resp, err := r.Clients.Quartermaster.ListNodes(ctx, clusterID, "", "", &pb.CursorPaginationRequest{First: 500, After: after})
			if err != nil || resp == nil {
				break
			}
			nodes = append(nodes, resp.Nodes...)
			if resp.Pagination == nil || !resp.Pagination.HasNextPage || resp.Pagination.EndCursor == nil || *resp.Pagination.EndCursor == "" {
				break
			}
			after = resp.Pagination.EndCursor
		}
	}

	if len(nodes) == 0 {
		return
	}
	clusterGeo := computeClusterGeo(nodes, clusterIDs)
	applyTrafficGeo(pairs, clusterGeo)
}

// DoGetFederationEventsConnection returns federation events as a connection.
func (r *Resolver) DoGetFederationEventsConnection(ctx context.Context, timeRange *model.TimeRangeInput, first *int, eventType *string, noCache *bool) (*model.FederationEventsConnection, error) {
	if err := middleware.RequirePermission(ctx, "analytics:read"); err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateFederationEventsConnection(), nil
	}

	tenantID := tenantIDFromContext(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	startTime, endTime := parseTimeRange(timeRange)
	skipCache := noCache != nil && *noCache

	limit := int32(100)
	if first != nil && *first > 0 {
		limit = int32(*first)
	}

	evtTypeStr := ""
	if eventType != nil {
		evtTypeStr = *eventType
	}
	keyParts := []string{tenantID, timeKey(startTime), timeKey(endTime), evtTypeStr, fmt.Sprintf("%d", limit)}

	val, err := r.fetchPeriscopeWithOptions(ctx, "federation_events", keyParts, func(ctx context.Context) (interface{}, error) {
		return r.Clients.Periscope.GetFederationEvents(ctx, tenantID, timePtrsToTimeRangeOpts(startTime, endTime), eventType, limit)
	}, skipCache)
	if err != nil {
		return nil, err
	}
	resp, ok := val.(*pb.GetFederationEventsResponse)
	if !ok {
		return nil, fmt.Errorf("unexpected response type for federation events: %T", val)
	}

	events := resp.GetEvents()
	edges := make([]*model.FederationEventEdge, len(events))
	for i, evt := range events {
		cursor := fmt.Sprintf("fed:%d", i)
		edges[i] = &model.FederationEventEdge{
			Cursor: cursor,
			Node:   evt,
		}
	}

	var startCursor, endCursor *string
	if len(edges) > 0 {
		startCursor = &edges[0].Cursor
		endCursor = &edges[len(edges)-1].Cursor
	}

	return &model.FederationEventsConnection{
		Edges: edges,
		PageInfo: &model.PageInfo{
			HasNextPage:     len(events) >= int(limit),
			HasPreviousPage: false,
			StartCursor:     startCursor,
			EndCursor:       endCursor,
		},
		TotalCount: int(resp.GetTotalCount()),
	}, nil
}

// DoGetFederationSummary returns aggregated federation event counts.
func (r *Resolver) DoGetFederationSummary(ctx context.Context, timeRange *model.TimeRangeInput, noCache *bool) (*pb.FederationSummary, error) {
	if err := middleware.RequirePermission(ctx, "analytics:read"); err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateFederationSummary(), nil
	}

	tenantID := tenantIDFromContext(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	startTime, endTime := parseTimeRange(timeRange)
	skipCache := noCache != nil && *noCache
	keyParts := []string{tenantID, timeKey(startTime), timeKey(endTime)}

	val, err := r.fetchPeriscopeWithOptions(ctx, "federation_summary", keyParts, func(ctx context.Context) (interface{}, error) {
		return r.Clients.Periscope.GetFederationSummary(ctx, tenantID, timePtrsToTimeRangeOpts(startTime, endTime))
	}, skipCache)
	if err != nil {
		return nil, err
	}
	resp, ok := val.(*pb.GetFederationSummaryResponse)
	if !ok {
		return nil, fmt.Errorf("unexpected response type for federation summary: %T", val)
	}
	return resp.GetSummary(), nil
}

// DoGetNetworkStatus returns public network topology (no tenant data).
func (r *Resolver) DoGetNetworkStatus(ctx context.Context) (*model.NetworkStatus, error) {
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateNetworkStatus(), nil
	}

	// Fetch all active clusters (gateway uses service token → sees all active)
	clustersResp, err := r.Clients.Quartermaster.ListClusters(ctx, &pb.CursorPaginationRequest{First: 500})
	if err != nil {
		r.Logger.WithError(err).Warn("networkStatus: Quartermaster unavailable, returning demo topology")
		return demo.GenerateNetworkStatus(), nil
	}

	// Fetch foghorn pool for peer connection derivation
	poolStatus, _ := r.Clients.Quartermaster.GetFoghornPoolStatus(ctx)

	// Fetch all nodes (for geo + counts)
	nodesResp, _ := r.Clients.Quartermaster.ListNodes(ctx, "", "", "", &pb.CursorPaginationRequest{First: 2000})

	// Group nodes by cluster: count and average geo
	type clusterGeo struct {
		nodeCount int
		latSum    float64
		lonSum    float64
		geoCount  int
	}
	nodesByCluster := make(map[string]*clusterGeo)
	if nodesResp != nil {
		for _, n := range nodesResp.Nodes {
			cg, ok := nodesByCluster[n.ClusterId]
			if !ok {
				cg = &clusterGeo{}
				nodesByCluster[n.ClusterId] = cg
			}
			cg.nodeCount++
			if n.Latitude != nil && n.Longitude != nil {
				cg.latSum += *n.Latitude
				cg.lonSum += *n.Longitude
				cg.geoCount++
			}
		}
	}

	activeClusters := make(map[string]*pb.InfrastructureCluster)
	for _, c := range clustersResp.Clusters {
		if c != nil && c.IsActive {
			activeClusters[c.ClusterId] = c
		}
	}

	// Track which active clusters have foghorn instances (for peer connection derivation)
	foghornClusters := make(map[string]bool)
	if poolStatus != nil {
		for _, entry := range poolStatus.Clusters {
			if entry.ClusterId != "" && entry.InstanceCount > 0 {
				if _, ok := activeClusters[entry.ClusterId]; !ok {
					continue
				}
				foghornClusters[entry.ClusterId] = true
			}
		}
	}

	var clusters []*model.NetworkClusterStatus
	var totalNodes, healthyNodes int
	for _, c := range activeClusters {
		var lat, lon float64
		nc := 0
		hn := 0
		if cg, ok := nodesByCluster[c.ClusterId]; ok {
			nc = cg.nodeCount
			hn = cg.nodeCount // healthy determined by presence
			if cg.geoCount > 0 {
				lat = cg.latSum / float64(cg.geoCount)
				lon = cg.lonSum / float64(cg.geoCount)
			}
		}

		peerCount := 0
		if foghornClusters[c.ClusterId] {
			// peers = other clusters that also have foghorn instances
			for otherID := range foghornClusters {
				if otherID != c.ClusterId {
					peerCount++
				}
			}
		}

		clusters = append(clusters, &model.NetworkClusterStatus{
			ClusterID:        c.ClusterId,
			Name:             c.ClusterName,
			Region:           c.ClusterType,
			Latitude:         lat,
			Longitude:        lon,
			NodeCount:        nc,
			HealthyNodeCount: hn,
			PeerCount:        peerCount,
			Status:           c.HealthStatus,
		})
		totalNodes += nc
		healthyNodes += hn
	}

	// Build peer connections: every pair of clusters with foghorn instances
	var peerConnections []*model.NetworkPeerConnection
	foghornList := make([]string, 0, len(foghornClusters))
	for id := range foghornClusters {
		foghornList = append(foghornList, id)
	}
	for i := 0; i < len(foghornList); i++ {
		for j := i + 1; j < len(foghornList); j++ {
			peerConnections = append(peerConnections, &model.NetworkPeerConnection{
				SourceCluster: foghornList[i],
				TargetCluster: foghornList[j],
				Connected:     true,
			})
		}
	}

	return &model.NetworkStatus{
		Clusters:        clusters,
		PeerConnections: peerConnections,
		TotalNodes:      totalNodes,
		HealthyNodes:    healthyNodes,
		UpdatedAt:       time.Now(),
	}, nil
}

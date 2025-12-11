package resolvers

import (
	"context"
	"fmt"
	"time"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/demo"
	"frameworks/api_gateway/internal/middleware"
	periscopeclient "frameworks/pkg/clients/periscope"
	"frameworks/pkg/pagination"
	pb "frameworks/pkg/proto"
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

// encodeIndexCursor encodes an offset-based cursor
func encodeIndexCursor(index int) string {
	return pagination.EncodeIndexCursor(index)
}

// parseTimeRange converts GraphQL TimeRangeInput to time pointers
func parseTimeRange(timeRange *model.TimeRangeInput) (startTime *time.Time, endTime *time.Time) {
	if timeRange != nil {
		startTime = &timeRange.Start
		endTime = &timeRange.End
	}
	return startTime, endTime
}

// buildPageInfo builds a PageInfo from pagination parameters
func buildPageInfo(offset, count, totalCount int, hasMore bool) *model.PageInfo {
	pageInfo := &model.PageInfo{
		HasPreviousPage: offset > 0,
		HasNextPage:     hasMore,
	}
	if count > 0 {
		firstCursor := encodeIndexCursor(offset)
		lastCursor := encodeIndexCursor(offset + count - 1)
		pageInfo.StartCursor = &firstCursor
		pageInfo.EndCursor = &lastCursor
	}
	return pageInfo
}

// DoGetRoutingEventsConnection returns a connection-style payload for routing events.
func (r *Resolver) DoGetRoutingEventsConnection(ctx context.Context, stream *string, timeRange *model.TimeRangeInput, first *int, after *string, last *int, before *string, noCache *bool) (*model.RoutingEventsConnection, error) {
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateRoutingEventsConnection(), nil
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

	// Fetch related tenant IDs (from subscriptions) to allow seeing events from shared infra
	var relatedTenantIDs []string
	if user := middleware.GetUserFromContext(ctx); user != nil {
		// Fetch subscribed clusters to find their owners
		// Note: Pagination handled by Quartermaster, here we just want the list.
		// If user has >100 subscriptions, we might miss some providers here without paging loop.
		// For now, assume <100 subscriptions.
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

	// Fetch from datafetcher with pagination and optional stream filter
	response, err := r.loadRoutingEvents(ctx, stream, startTime, endTime, opts, skipCache, relatedTenantIDs)
	if err != nil {
		return nil, err
	}

	// Build edges from proto response
	edges := make([]*model.RoutingEventEdge, len(response.Events))
	for i, event := range response.Events {
		cursor := event.Id
		if cursor == "" {
			cursor = encodeIndexCursor(i)
		}
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

	return &model.RoutingEventsConnection{
		Edges:      edges,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}, nil
}

// DoGetConnectionEventsConnection returns a connection-style payload for connection events.
func (r *Resolver) DoGetConnectionEventsConnection(ctx context.Context, stream *string, timeRange *model.TimeRangeInput, first *int, after *string, last *int, before *string, noCache *bool) (*model.ConnectionEventsConnection, error) {
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateConnectionEventsConnection(), nil
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

	// Fetch from datafetcher with pagination and optional stream filter
	response, err := r.loadConnectionEvents(ctx, stream, startTime, endTime, opts, skipCache)
	if err != nil {
		return nil, err
	}

	// Build edges from proto response
	edges := make([]*model.ConnectionEventEdge, len(response.Events))
	for i, event := range response.Events {
		cursor := event.EventId
		if cursor == "" {
			cursor = encodeIndexCursor(i)
		}
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

	return &model.ConnectionEventsConnection{
		Edges:      edges,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}, nil
}

// DoGetClipEventsConnection returns a connection-style payload for clip events.
// NOTE: Filters already working - handler and client both support internalName and stage
func (r *Resolver) DoGetClipEventsConnection(ctx context.Context, internalName *string, stage *string, timeRange *model.TimeRangeInput, first *int, after *string, last *int, before *string, noCache *bool) (*model.ClipEventsConnection, error) {
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateClipEventsConnection(), nil
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

	// Parse filters
	var name, stageFilter *string
	if internalName != nil && *internalName != "" {
		name = internalName
	}
	if stage != nil && *stage != "" {
		stageFilter = stage
	}

	startTime, endTime := parseTimeRange(timeRange)
	skipCache := noCache != nil && *noCache

	// Fetch from datafetcher with pagination
	response, err := r.loadClipEvents(ctx, name, stageFilter, startTime, endTime, opts, skipCache)
	if err != nil {
		return nil, err
	}

	// Build edges from proto response
	edges := make([]*model.ClipEventEdge, len(response.Events))
	for i, event := range response.Events {
		cursor := event.RequestId
		if cursor == "" {
			cursor = encodeIndexCursor(i)
		}
		edges[i] = &model.ClipEventEdge{
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

	return &model.ClipEventsConnection{
		Edges:      edges,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}, nil
}

// DoGetNodeMetricsConnection returns a connection-style payload for node metrics.
func (r *Resolver) DoGetNodeMetricsConnection(ctx context.Context, nodeID *string, timeRange *model.TimeRangeInput, first *int, after *string, last *int, before *string, noCache *bool) (*model.NodeMetricsConnection, error) {
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateNodeMetricsConnection(), nil
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
		cursor := metric.Id
		if cursor == "" {
			cursor = encodeIndexCursor(i)
		}
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

	return &model.NodeMetricsConnection{
		Edges:      edges,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}, nil
}

// DoGetNodeMetrics1hConnection returns a connection-style payload for 1h node metrics.
func (r *Resolver) DoGetNodeMetrics1hConnection(ctx context.Context, timeRange *model.TimeRangeInput, nodeID *string, first *int, after *string, last *int, before *string, noCache *bool) (*model.NodeMetrics1hConnection, error) {
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateNodeMetrics1hConnection(), nil
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
		cursor := metric.Id
		if cursor == "" {
			cursor = encodeIndexCursor(i)
		}
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

	return &model.NodeMetrics1hConnection{
		Edges:      edges,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}, nil
}

// DoGetStreamHealthMetricsConnection returns a connection-style payload for stream health metrics.
func (r *Resolver) DoGetStreamHealthMetricsConnection(ctx context.Context, stream string, timeRange *model.TimeRangeInput, first *int, after *string, last *int, before *string, noCache *bool) (*model.StreamHealthMetricsConnection, error) {
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateStreamHealthMetricsConnection(), nil
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

	// Build edges from proto response
	edges := make([]*model.StreamHealthMetricEdge, len(response.Metrics))
	for i, metric := range response.Metrics {
		cursor := metric.Id
		if cursor == "" {
			cursor = encodeIndexCursor(i)
		}
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

	return &model.StreamHealthMetricsConnection{
		Edges:      edges,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}, nil
}

// DoGetTrackListEventsConnection returns a connection-style payload for track list events.
// NOTE: stream filter supported by handler but not by client yet (Phase 3C.3)
func (r *Resolver) DoGetTrackListEventsConnection(ctx context.Context, stream string, timeRange *model.TimeRangeInput, first *int, after *string, last *int, before *string, noCache *bool) (*model.TrackListEventsConnection, error) {
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateTrackListEventsConnection(), nil
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

	// Build edges from proto response
	edges := make([]*model.TrackListEventEdge, len(response.Events))
	for i, event := range response.Events {
		cursor := event.Id
		if cursor == "" {
			cursor = encodeIndexCursor(i)
		}
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

	return &model.TrackListEventsConnection{
		Edges:      edges,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}, nil
}

// DoGetStreamEventsConnection returns a connection-style payload for stream events.
// NOTE: stream filter already supported by client method
func (r *Resolver) DoGetStreamEventsConnection(ctx context.Context, obj *pb.Stream, timeRange *model.TimeRangeInput, first *int, after *string) (*model.StreamEventsConnection, error) {
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateStreamEventsConnection(), nil
	}

	streamName := obj.InternalName

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

	// Fetch from datafetcher with pagination (no noCache param on this resolver)
	response, err := r.loadStreamEvents(ctx, streamName, startTime, endTime, opts, false)
	if err != nil {
		return nil, err
	}

	// Build edges from proto response
	edges := make([]*model.StreamEventEdge, len(response.Events))
	for i, event := range response.Events {
		cursor := event.EventId
		if cursor == "" {
			cursor = encodeIndexCursor(i)
		}
		edges[i] = &model.StreamEventEdge{
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

	return &model.StreamEventsConnection{
		Edges:      edges,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}, nil
}

// DoGetStreamHealthConnection returns a connection-style payload for stream health metrics.
func (r *Resolver) DoGetStreamHealthConnection(ctx context.Context, obj *pb.Stream, timeRange *model.TimeRangeInput, first *int, after *string) (*model.StreamHealthMetricsConnection, error) {
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateStreamHealthMetricsConnection(), nil
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
	streamFilter := &obj.InternalName

	// Fetch from datafetcher with pagination and stream filter (no noCache param on this resolver)
	response, err := r.loadStreamHealthMetrics(ctx, streamFilter, startTime, endTime, opts, false)
	if err != nil {
		return nil, err
	}

	// Build edges from proto response
	edges := make([]*model.StreamHealthMetricEdge, len(response.Metrics))
	for i, metric := range response.Metrics {
		cursor := metric.Id
		if cursor == "" {
			cursor = encodeIndexCursor(i)
		}
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

	return &model.StreamHealthMetricsConnection{
		Edges:      edges,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}, nil
}

// DoGetNodeMetricsConnectionForNode returns a connection-style payload for node metrics (for node resolver).
func (r *Resolver) DoGetNodeMetricsConnectionForNode(ctx context.Context, obj *pb.InfrastructureNode, timeRange *model.TimeRangeInput, first *int, after *string) (*model.NodeMetricsConnection, error) {
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateNodeMetricsConnection(), nil
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
		cursor := metric.Id
		if cursor == "" {
			cursor = encodeIndexCursor(i)
		}
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

	return &model.NodeMetricsConnection{
		Edges:      edges,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}, nil
}

// DoGetNodeMetrics1hConnectionForNode returns a connection-style payload for 1h node metrics (for node resolver).
func (r *Resolver) DoGetNodeMetrics1hConnectionForNode(ctx context.Context, obj *pb.InfrastructureNode, timeRange *model.TimeRangeInput, first *int, after *string) (*model.NodeMetrics1hConnection, error) {
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateNodeMetrics1hConnection(), nil
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
		cursor := metric.Id
		if cursor == "" {
			cursor = encodeIndexCursor(i)
		}
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

	return &model.NodeMetrics1hConnection{
		Edges:      edges,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}, nil
}

// DoGetArtifactState returns the current state of a single artifact (clip/DVR).
func (r *Resolver) DoGetArtifactState(ctx context.Context, requestID string) (*pb.ArtifactState, error) {
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateArtifactState(requestID), nil
	}

	tenantID, _ := ctx.Value("tenant_id").(string)
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
func (r *Resolver) DoGetArtifactStatesConnection(ctx context.Context, internalName *string, contentType *string, stage *string, first *int, after *string, last *int, before *string) (*model.ArtifactStatesConnection, error) {
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateArtifactStatesConnection(), nil
	}

	tenantID, _ := ctx.Value("tenant_id").(string)
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
	response, err := r.Clients.Periscope.GetArtifactStates(ctx, tenantID, internalName, contentType, stage, opts)
	if err != nil {
		return nil, err
	}

	// Build edges from proto response
	edges := make([]*model.ArtifactStateEdge, len(response.Artifacts))
	for i, artifact := range response.Artifacts {
		cursor := artifact.RequestId
		if cursor == "" {
			cursor = encodeIndexCursor(i)
		}
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

	return &model.ArtifactStatesConnection{
		Edges:      edges,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}, nil
}

// ============================================================================
// Pre-Aggregated Analytics Connections (Materialized Views)
// ============================================================================

// DoGetStreamConnectionHourlyConnection returns a connection-style payload for hourly connection aggregates.
func (r *Resolver) DoGetStreamConnectionHourlyConnection(ctx context.Context, stream *string, timeRange *model.TimeRangeInput, first *int, after *string, last *int, before *string, noCache *bool) (*model.StreamConnectionHourlyConnection, error) {
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateStreamConnectionHourlyConnection(), nil
	}

	tenantID, _ := ctx.Value("tenant_id").(string)
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

	edges := make([]*model.StreamConnectionHourlyEdge, len(response.Records))
	for i, record := range response.Records {
		cursor := record.Id
		if cursor == "" {
			cursor = encodeIndexCursor(i)
		}
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

	return &model.StreamConnectionHourlyConnection{
		Edges:      edges,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}, nil
}

// DoGetClientMetrics5mConnection returns a connection-style payload for 5-minute client metrics.
func (r *Resolver) DoGetClientMetrics5mConnection(ctx context.Context, stream *string, nodeID *string, timeRange *model.TimeRangeInput, first *int, after *string, last *int, before *string, noCache *bool) (*model.ClientMetrics5mConnection, error) {
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateClientMetrics5mConnection(), nil
	}

	tenantID, _ := ctx.Value("tenant_id").(string)
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

	edges := make([]*model.ClientMetrics5mEdge, len(response.Records))
	for i, record := range response.Records {
		cursor := record.Id
		if cursor == "" {
			cursor = encodeIndexCursor(i)
		}
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

	return &model.ClientMetrics5mConnection{
		Edges:      edges,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}, nil
}

// DoGetQualityTierDailyConnection returns a connection-style payload for daily quality tier distribution.
func (r *Resolver) DoGetQualityTierDailyConnection(ctx context.Context, stream *string, timeRange *model.TimeRangeInput, first *int, after *string, last *int, before *string, noCache *bool) (*model.QualityTierDailyConnection, error) {
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateQualityTierDailyConnection(), nil
	}

	tenantID, _ := ctx.Value("tenant_id").(string)
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

	edges := make([]*model.QualityTierDailyEdge, len(response.Records))
	for i, record := range response.Records {
		cursor := record.Id
		if cursor == "" {
			cursor = encodeIndexCursor(i)
		}
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

	return &model.QualityTierDailyConnection{
		Edges:      edges,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}, nil
}

// DoGetQualityChangesHourlyConnection returns a connection-style payload for hourly quality changes.
func (r *Resolver) DoGetQualityChangesHourlyConnection(ctx context.Context, stream *string, timeRange *model.TimeRangeInput, first *int, after *string, last *int, before *string, noCache *bool) (*model.QualityChangesHourlyConnection, error) {
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateQualityChangesHourlyConnection(), nil
	}

	tenantID, _ := ctx.Value("tenant_id").(string)
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

	response, err := r.Clients.Periscope.GetQualityChangesHourly(ctx, tenantID, stream, timeOpts, opts)
	if err != nil {
		return nil, err
	}

	edges := make([]*model.QualityChangesHourlyEdge, len(response.Records))
	for i, record := range response.Records {
		cursor := record.Id
		if cursor == "" {
			cursor = encodeIndexCursor(i)
		}
		edges[i] = &model.QualityChangesHourlyEdge{
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

	return &model.QualityChangesHourlyConnection{
		Edges:      edges,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}, nil
}

// DoGetStorageUsageConnection returns a connection-style payload for storage usage records.
func (r *Resolver) DoGetStorageUsageConnection(ctx context.Context, nodeID *string, timeRange *model.TimeRangeInput, first *int, after *string, last *int, before *string, noCache *bool) (*model.StorageUsageConnection, error) {
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateStorageUsageConnection(), nil
	}

	tenantID, _ := ctx.Value("tenant_id").(string)
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

	response, err := r.Clients.Periscope.GetStorageUsage(ctx, tenantID, nodeID, timeOpts, opts)
	if err != nil {
		return nil, err
	}

	edges := make([]*model.StorageUsageEdge, len(response.Records))
	for i, record := range response.Records {
		cursor := record.Id
		if cursor == "" {
			cursor = encodeIndexCursor(i)
		}
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

	return &model.StorageUsageConnection{
		Edges:      edges,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}, nil
}

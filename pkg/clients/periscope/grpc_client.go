package periscope

import (
	"context"
	"fmt"
	"time"

	"frameworks/pkg/logging"
	"frameworks/pkg/pagination"
	pb "frameworks/pkg/proto"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// GRPCClient is the gRPC client for Periscope analytics
type GRPCClient struct {
	conn       *grpc.ClientConn
	stream     pb.StreamAnalyticsServiceClient
	viewer     pb.ViewerAnalyticsServiceClient
	track      pb.TrackAnalyticsServiceClient
	connection pb.ConnectionAnalyticsServiceClient
	node       pb.NodeAnalyticsServiceClient
	routing    pb.RoutingAnalyticsServiceClient
	platform   pb.PlatformAnalyticsServiceClient
	clip       pb.ClipAnalyticsServiceClient
	aggregated pb.AggregatedAnalyticsServiceClient
	logger     logging.Logger
}

// GRPCConfig represents the configuration for the gRPC client
type GRPCConfig struct {
	// GRPCAddr is the gRPC server address (host:port, no scheme)
	GRPCAddr string
	// Timeout for gRPC calls
	Timeout time.Duration
	// Logger for the client
	Logger logging.Logger
	// ServiceToken for service-to-service authentication (fallback when no user JWT)
	ServiceToken string
}

// CursorPaginationOpts represents cursor-based pagination options
type CursorPaginationOpts struct {
	First  int32
	After  *string
	Last   int32
	Before *string
}

// TimeRangeOpts represents a time range filter
type TimeRangeOpts struct {
	StartTime time.Time
	EndTime   time.Time
}

// authInterceptor propagates authentication to gRPC metadata.
// This reads user_id, tenant_id, and jwt_token from the Go context (set by Gateway middleware)
// and adds them to outgoing gRPC metadata for downstream services.
// If no user JWT is available, it falls back to the service token for service-to-service calls.
func authInterceptor(serviceToken string) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		// Extract user context from Go context and add to gRPC metadata
		md := metadata.MD{}

		if userID, ok := ctx.Value("user_id").(string); ok && userID != "" {
			md.Set("x-user-id", userID)
		}
		if tenantID, ok := ctx.Value("tenant_id").(string); ok && tenantID != "" {
			md.Set("x-tenant-id", tenantID)
		}

		// Use user's JWT from context if available, otherwise fall back to service token
		if jwtToken, ok := ctx.Value("jwt_token").(string); ok && jwtToken != "" {
			md.Set("authorization", "Bearer "+jwtToken)
		} else if serviceToken != "" {
			md.Set("authorization", "Bearer "+serviceToken)
		}

		// Merge with existing outgoing metadata if any
		if existingMD, ok := metadata.FromOutgoingContext(ctx); ok {
			md = metadata.Join(existingMD, md)
		}

		ctx = metadata.NewOutgoingContext(ctx, md)
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

// NewGRPCClient creates a new gRPC client for Periscope
func NewGRPCClient(config GRPCConfig) (*GRPCClient, error) {
	if config.Timeout == 0 {
		config.Timeout = 30 * time.Second
	}

	// Connect to gRPC server with auth interceptor for user context and service token fallback
	conn, err := grpc.NewClient(
		config.GRPCAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.WaitForReady(true)),
		grpc.WithUnaryInterceptor(authInterceptor(config.ServiceToken)),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                10 * time.Second, // Send keepalive ping every 10s
			Timeout:             3 * time.Second,  // Wait 3s for ping ack before closing
			PermitWithoutStream: true,             // Keep connection alive even when idle
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Periscope gRPC: %w", err)
	}

	return &GRPCClient{
		conn:       conn,
		stream:     pb.NewStreamAnalyticsServiceClient(conn),
		viewer:     pb.NewViewerAnalyticsServiceClient(conn),
		track:      pb.NewTrackAnalyticsServiceClient(conn),
		connection: pb.NewConnectionAnalyticsServiceClient(conn),
		node:       pb.NewNodeAnalyticsServiceClient(conn),
		routing:    pb.NewRoutingAnalyticsServiceClient(conn),
		platform:   pb.NewPlatformAnalyticsServiceClient(conn),
		clip:       pb.NewClipAnalyticsServiceClient(conn),
		aggregated: pb.NewAggregatedAnalyticsServiceClient(conn),
		logger:     config.Logger,
	}, nil
}

// Close closes the gRPC connection
func (c *GRPCClient) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// buildTimeRange creates a proto TimeRange from TimeRangeOpts
func buildTimeRange(opts *TimeRangeOpts) *pb.TimeRange {
	if opts == nil {
		return nil
	}
	return &pb.TimeRange{
		Start: timestamppb.New(opts.StartTime),
		End:   timestamppb.New(opts.EndTime),
	}
}

// buildCursorPagination creates a proto CursorPaginationRequest from options
func buildCursorPagination(opts *CursorPaginationOpts) *pb.CursorPaginationRequest {
	if opts == nil {
		return &pb.CursorPaginationRequest{
			First: int32(pagination.DefaultLimit),
		}
	}
	req := &pb.CursorPaginationRequest{
		First: opts.First,
		Last:  opts.Last,
	}
	if opts.After != nil {
		req.After = opts.After
	}
	if opts.Before != nil {
		req.Before = opts.Before
	}
	return req
}

// ============================================================================
// Stream Analytics (Summary + Events)
// ============================================================================

// GetStreamAnalyticsSummary returns MV-backed range aggregates for a stream.
func (c *GRPCClient) GetStreamAnalyticsSummary(ctx context.Context, tenantID string, streamID string, timeRange *TimeRangeOpts) (*pb.GetStreamAnalyticsSummaryResponse, error) {
	req := &pb.GetStreamAnalyticsSummaryRequest{
		TenantId:  tenantID,
		StreamId:  streamID,
		TimeRange: buildTimeRange(timeRange),
	}
	return c.aggregated.GetStreamAnalyticsSummary(ctx, req)
}

// GetLiveUsageSummary returns near-real-time usage summary for billing dashboards.
func (c *GRPCClient) GetLiveUsageSummary(ctx context.Context, tenantID string, timeRange *TimeRangeOpts) (*pb.GetLiveUsageSummaryResponse, error) {
	req := &pb.GetLiveUsageSummaryRequest{
		TenantId:  tenantID,
		TimeRange: buildTimeRange(timeRange),
	}
	return c.aggregated.GetLiveUsageSummary(ctx, req)
}

// GetStreamEvents returns events for a specific stream with cursor pagination
func (c *GRPCClient) GetStreamEvents(ctx context.Context, streamID string, timeRange *TimeRangeOpts, opts *CursorPaginationOpts) (*pb.GetStreamEventsResponse, error) {
	return c.stream.GetStreamEvents(ctx, &pb.GetStreamEventsRequest{
		StreamId:   streamID,
		TimeRange:  buildTimeRange(timeRange),
		Pagination: buildCursorPagination(opts),
	})
}

// GetBufferEvents returns buffer events for a specific stream
func (c *GRPCClient) GetBufferEvents(ctx context.Context, streamID string, timeRange *TimeRangeOpts, opts *CursorPaginationOpts) (*pb.GetBufferEventsResponse, error) {
	return c.stream.GetBufferEvents(ctx, &pb.GetBufferEventsRequest{
		StreamId:   streamID,
		TimeRange:  buildTimeRange(timeRange),
		Pagination: buildCursorPagination(opts),
	})
}

// GetStreamHealthMetrics returns stream health metrics
func (c *GRPCClient) GetStreamHealthMetrics(ctx context.Context, streamID *string, timeRange *TimeRangeOpts, opts *CursorPaginationOpts) (*pb.GetStreamHealthMetricsResponse, error) {
	req := &pb.GetStreamHealthMetricsRequest{
		TimeRange:  buildTimeRange(timeRange),
		Pagination: buildCursorPagination(opts),
	}
	if streamID != nil {
		req.StreamId = streamID
	}
	return c.stream.GetStreamHealthMetrics(ctx, req)
}

// GetStreamStatus returns operational state for a single stream
// This is the Data Plane source of truth for stream status (replaces Commodore status)
func (c *GRPCClient) GetStreamStatus(ctx context.Context, tenantID string, streamID string) (*pb.StreamStatusResponse, error) {
	return c.stream.GetStreamStatus(ctx, &pb.GetStreamStatusRequest{
		TenantId: tenantID,
		StreamId: streamID,
	})
}

// GetStreamsStatus returns operational state for multiple streams (batch lookup)
// Use this to avoid N+1 queries when listing streams
func (c *GRPCClient) GetStreamsStatus(ctx context.Context, tenantID string, streamIDs []string) (*pb.StreamsStatusResponse, error) {
	return c.stream.GetStreamsStatus(ctx, &pb.GetStreamsStatusRequest{
		TenantId:  tenantID,
		StreamIds: streamIDs,
	})
}

// ============================================================================
// Viewer Analytics
// ============================================================================

// GetViewerMetrics returns viewer session metrics
func (c *GRPCClient) GetViewerMetrics(ctx context.Context, tenantID string, streamID *string, timeRange *TimeRangeOpts, opts *CursorPaginationOpts) (*pb.GetViewerMetricsResponse, error) {
	req := &pb.GetViewerMetricsRequest{
		TenantId:   tenantID,
		TimeRange:  buildTimeRange(timeRange),
		Pagination: buildCursorPagination(opts),
	}
	if streamID != nil {
		req.StreamId = streamID
	}
	return c.viewer.GetViewerMetrics(ctx, req)
}

// GetViewerCountTimeSeries returns time-bucketed viewer counts for charts
// interval should be "5m", "15m", "1h", or "1d"
func (c *GRPCClient) GetViewerCountTimeSeries(ctx context.Context, tenantID string, streamID *string, timeRange *TimeRangeOpts, interval string) (*pb.GetViewerCountTimeSeriesResponse, error) {
	req := &pb.GetViewerCountTimeSeriesRequest{
		TenantId:  tenantID,
		TimeRange: buildTimeRange(timeRange),
		Interval:  interval,
	}
	if streamID != nil {
		req.StreamId = streamID
	}
	return c.viewer.GetViewerCountTimeSeries(ctx, req)
}

// GetGeographicDistribution returns aggregated geographic distribution of viewers
// topN limits the number of results (default 10 if 0)
func (c *GRPCClient) GetGeographicDistribution(ctx context.Context, tenantID string, streamID *string, timeRange *TimeRangeOpts, topN int32) (*pb.GetGeographicDistributionResponse, error) {
	req := &pb.GetGeographicDistributionRequest{
		TenantId:  tenantID,
		TimeRange: buildTimeRange(timeRange),
		TopN:      topN,
	}
	if streamID != nil {
		req.StreamId = streamID
	}
	return c.viewer.GetGeographicDistribution(ctx, req)
}

// ============================================================================
// Track Analytics
// ============================================================================

// GetTrackListEvents returns track list updates for a specific stream
func (c *GRPCClient) GetTrackListEvents(ctx context.Context, streamID string, timeRange *TimeRangeOpts, opts *CursorPaginationOpts) (*pb.GetTrackListEventsResponse, error) {
	return c.track.GetTrackListEvents(ctx, &pb.GetTrackListEventsRequest{
		StreamId:   streamID,
		TimeRange:  buildTimeRange(timeRange),
		Pagination: buildCursorPagination(opts),
	})
}

// ============================================================================
// Connection Analytics
// ============================================================================

// GetConnectionEvents returns connection events
func (c *GRPCClient) GetConnectionEvents(ctx context.Context, streamID *string, timeRange *TimeRangeOpts, opts *CursorPaginationOpts) (*pb.GetConnectionEventsResponse, error) {
	req := &pb.GetConnectionEventsRequest{
		TimeRange:  buildTimeRange(timeRange),
		Pagination: buildCursorPagination(opts),
	}
	if streamID != nil {
		req.StreamId = streamID
	}
	return c.connection.GetConnectionEvents(ctx, req)
}

// ============================================================================
// Node Analytics
// ============================================================================

// GetNodeMetrics returns node performance metrics
func (c *GRPCClient) GetNodeMetrics(ctx context.Context, nodeID *string, timeRange *TimeRangeOpts, opts *CursorPaginationOpts) (*pb.GetNodeMetricsResponse, error) {
	req := &pb.GetNodeMetricsRequest{
		TimeRange:  buildTimeRange(timeRange),
		Pagination: buildCursorPagination(opts),
	}
	if nodeID != nil {
		req.NodeId = nodeID
	}
	return c.node.GetNodeMetrics(ctx, req)
}

// GetNodeMetrics1H returns hourly aggregated node metrics
func (c *GRPCClient) GetNodeMetrics1H(ctx context.Context, nodeID *string, timeRange *TimeRangeOpts, opts *CursorPaginationOpts) (*pb.GetNodeMetrics1HResponse, error) {
	req := &pb.GetNodeMetrics1HRequest{
		TimeRange:  buildTimeRange(timeRange),
		Pagination: buildCursorPagination(opts),
	}
	if nodeID != nil {
		req.NodeId = nodeID
	}
	return c.node.GetNodeMetrics1H(ctx, req)
}

// GetNodeMetricsAggregated returns per-node aggregates for the requested time range.
func (c *GRPCClient) GetNodeMetricsAggregated(ctx context.Context, nodeID *string, timeRange *TimeRangeOpts) (*pb.GetNodeMetricsAggregatedResponse, error) {
	req := &pb.GetNodeMetricsAggregatedRequest{
		TimeRange: buildTimeRange(timeRange),
	}
	if nodeID != nil {
		req.NodeId = nodeID
	}
	return c.node.GetNodeMetricsAggregated(ctx, req)
}

// GetLiveNodes returns current state of nodes from live_nodes (ReplacingMergeTree)
// Supports multi-tenant access for subscribed clusters via relatedTenantIDs
func (c *GRPCClient) GetLiveNodes(ctx context.Context, nodeID *string, relatedTenantIDs []string) (*pb.GetLiveNodesResponse, error) {
	req := &pb.GetLiveNodesRequest{
		RelatedTenantIds: relatedTenantIDs,
	}
	if nodeID != nil {
		req.NodeId = nodeID
	}
	return c.node.GetLiveNodes(ctx, req)
}

// ============================================================================
// Routing Analytics
// ============================================================================

// GetRoutingEvents returns routing decision events
func (c *GRPCClient) GetRoutingEvents(ctx context.Context, streamID *string, timeRange *TimeRangeOpts, opts *CursorPaginationOpts, relatedTenantIDs []string, subjectTenantID, clusterID *string) (*pb.GetRoutingEventsResponse, error) {
	req := &pb.GetRoutingEventsRequest{
		TimeRange:        buildTimeRange(timeRange),
		Pagination:       buildCursorPagination(opts),
		RelatedTenantIds: relatedTenantIDs,
	}
	if streamID != nil {
		req.StreamId = streamID
	}
	// Dual-tenant attribution filters (RFC: routing-events-dual-tenant-attribution)
	if subjectTenantID != nil {
		req.StreamTenantId = subjectTenantID
	}
	if clusterID != nil {
		req.ClusterId = clusterID
	}
	return c.routing.GetRoutingEvents(ctx, req)
}

// ============================================================================
// Platform Analytics
// ============================================================================

// GetPlatformOverview returns high-level platform metrics
func (c *GRPCClient) GetPlatformOverview(ctx context.Context, tenantID string, timeRange *TimeRangeOpts) (*pb.GetPlatformOverviewResponse, error) {
	return c.platform.GetPlatformOverview(ctx, &pb.GetPlatformOverviewRequest{
		TenantId:  tenantID,
		TimeRange: buildTimeRange(timeRange),
	})
}

// ============================================================================
// Clip Analytics
// ============================================================================

// GetClipEvents returns artifact lifecycle events (clip/dvr/vod)
func (c *GRPCClient) GetClipEvents(ctx context.Context, streamID *string, stage *string, contentType *string, timeRange *TimeRangeOpts, opts *CursorPaginationOpts) (*pb.GetClipEventsResponse, error) {
	req := &pb.GetClipEventsRequest{
		TimeRange:  buildTimeRange(timeRange),
		Pagination: buildCursorPagination(opts),
	}
	if streamID != nil {
		req.StreamId = streamID
	}
	if stage != nil {
		req.Stage = stage
	}
	if contentType != nil {
		req.ContentType = contentType
	}
	return c.clip.GetClipEvents(ctx, req)
}

// GetArtifactState returns the current state of a single artifact (clip/DVR)
func (c *GRPCClient) GetArtifactState(ctx context.Context, tenantID string, requestID string) (*pb.GetArtifactStateResponse, error) {
	return c.clip.GetArtifactState(ctx, &pb.GetArtifactStateRequest{
		TenantId:  tenantID,
		RequestId: requestID,
	})
}

// GetArtifactStates returns a list of artifact states with optional filtering
func (c *GRPCClient) GetArtifactStates(ctx context.Context, tenantID string, streamID *string, contentType *string, stage *string, opts *CursorPaginationOpts) (*pb.GetArtifactStatesResponse, error) {
	req := &pb.GetArtifactStatesRequest{
		TenantId:   tenantID,
		Pagination: buildCursorPagination(opts),
	}
	if streamID != nil {
		req.StreamId = streamID
	}
	if contentType != nil {
		req.ContentType = contentType
	}
	if stage != nil {
		req.Stage = stage
	}
	return c.clip.GetArtifactStates(ctx, req)
}

// GetArtifactStatesByIDs returns artifact states for specific request IDs (batch lookup)
// Used by GraphQL field resolvers to efficiently fetch lifecycle data for multiple clips/DVRs
func (c *GRPCClient) GetArtifactStatesByIDs(ctx context.Context, tenantID string, requestIDs []string, contentType *string) (*pb.GetArtifactStatesResponse, error) {
	req := &pb.GetArtifactStatesRequest{
		TenantId:   tenantID,
		RequestIds: requestIDs,
		Pagination: &pb.CursorPaginationRequest{
			First: int32(len(requestIDs)), // Request exactly the number we need
		},
	}
	if contentType != nil {
		req.ContentType = contentType
	}
	return c.clip.GetArtifactStates(ctx, req)
}

// ============================================================================
// Aggregated Analytics (Pre-computed Materialized Views)
// ============================================================================

// GetStreamConnectionHourly returns hourly connection aggregates
func (c *GRPCClient) GetStreamConnectionHourly(ctx context.Context, tenantID string, streamID *string, timeRange *TimeRangeOpts, opts *CursorPaginationOpts) (*pb.GetStreamConnectionHourlyResponse, error) {
	req := &pb.GetStreamConnectionHourlyRequest{
		TenantId:   tenantID,
		TimeRange:  buildTimeRange(timeRange),
		Pagination: buildCursorPagination(opts),
	}
	if streamID != nil {
		req.StreamId = streamID
	}
	return c.aggregated.GetStreamConnectionHourly(ctx, req)
}

// GetClientMetrics5m returns 5-minute client metrics aggregates
func (c *GRPCClient) GetClientMetrics5m(ctx context.Context, tenantID string, streamID *string, nodeID *string, timeRange *TimeRangeOpts, opts *CursorPaginationOpts) (*pb.GetClientMetrics5MResponse, error) {
	req := &pb.GetClientMetrics5MRequest{
		TenantId:   tenantID,
		TimeRange:  buildTimeRange(timeRange),
		Pagination: buildCursorPagination(opts),
	}
	if streamID != nil {
		req.StreamId = streamID
	}
	if nodeID != nil {
		req.NodeId = nodeID
	}
	return c.aggregated.GetClientMetrics5M(ctx, req)
}

// GetQualityTierDaily returns daily quality tier distribution
func (c *GRPCClient) GetQualityTierDaily(ctx context.Context, tenantID string, streamID *string, timeRange *TimeRangeOpts, opts *CursorPaginationOpts) (*pb.GetQualityTierDailyResponse, error) {
	req := &pb.GetQualityTierDailyRequest{
		TenantId:   tenantID,
		TimeRange:  buildTimeRange(timeRange),
		Pagination: buildCursorPagination(opts),
	}
	if streamID != nil {
		req.StreamId = streamID
	}
	return c.aggregated.GetQualityTierDaily(ctx, req)
}

// GetStorageUsage returns storage usage records
func (c *GRPCClient) GetStorageUsage(ctx context.Context, tenantID string, nodeID *string, storageScope *string, timeRange *TimeRangeOpts, opts *CursorPaginationOpts) (*pb.GetStorageUsageResponse, error) {
	req := &pb.GetStorageUsageRequest{
		TenantId:   tenantID,
		TimeRange:  buildTimeRange(timeRange),
		Pagination: buildCursorPagination(opts),
	}
	if nodeID != nil {
		req.NodeId = nodeID
	}
	if storageScope != nil {
		req.StorageScope = storageScope
	}
	return c.aggregated.GetStorageUsage(ctx, req)
}

// GetStorageEvents returns storage lifecycle events (freeze/defrost operations)
func (c *GRPCClient) GetStorageEvents(ctx context.Context, tenantID string, streamID *string, assetType *string, timeRange *TimeRangeOpts, opts *CursorPaginationOpts) (*pb.GetStorageEventsResponse, error) {
	req := &pb.GetStorageEventsRequest{
		TenantId:   tenantID,
		TimeRange:  buildTimeRange(timeRange),
		Pagination: buildCursorPagination(opts),
	}
	if streamID != nil {
		req.StreamId = streamID
	}
	if assetType != nil {
		req.AssetType = assetType
	}
	return c.aggregated.GetStorageEvents(ctx, req)
}

// GetStreamHealth5m returns 5-minute aggregated health metrics for a stream
func (c *GRPCClient) GetStreamHealth5m(ctx context.Context, tenantID string, streamID string, timeRange *TimeRangeOpts, opts *CursorPaginationOpts) (*pb.GetStreamHealth5MResponse, error) {
	req := &pb.GetStreamHealth5MRequest{
		TenantId:   tenantID,
		StreamId:   streamID,
		TimeRange:  buildTimeRange(timeRange),
		Pagination: buildCursorPagination(opts),
	}
	return c.aggregated.GetStreamHealth5M(ctx, req)
}

// GetNodePerformance5m returns 5-minute aggregated node performance metrics
func (c *GRPCClient) GetNodePerformance5m(ctx context.Context, tenantID string, nodeID *string, timeRange *TimeRangeOpts, opts *CursorPaginationOpts) (*pb.GetNodePerformance5MResponse, error) {
	req := &pb.GetNodePerformance5MRequest{
		TenantId:   tenantID,
		TimeRange:  buildTimeRange(timeRange),
		Pagination: buildCursorPagination(opts),
	}
	if nodeID != nil {
		req.NodeId = nodeID
	}
	return c.aggregated.GetNodePerformance5M(ctx, req)
}

// GetViewerHoursHourly returns hourly viewer hours aggregates
func (c *GRPCClient) GetViewerHoursHourly(ctx context.Context, tenantID string, streamID *string, timeRange *TimeRangeOpts, opts *CursorPaginationOpts) (*pb.GetViewerHoursHourlyResponse, error) {
	req := &pb.GetViewerHoursHourlyRequest{
		TenantId:   tenantID,
		TimeRange:  buildTimeRange(timeRange),
		Pagination: buildCursorPagination(opts),
	}
	if streamID != nil {
		req.StreamId = streamID
	}
	return c.aggregated.GetViewerHoursHourly(ctx, req)
}

// GetViewerGeoHourly returns hourly geographic breakdown of viewers
func (c *GRPCClient) GetViewerGeoHourly(ctx context.Context, tenantID string, streamID *string, timeRange *TimeRangeOpts, opts *CursorPaginationOpts) (*pb.GetViewerGeoHourlyResponse, error) {
	req := &pb.GetViewerGeoHourlyRequest{
		TenantId:   tenantID,
		TimeRange:  buildTimeRange(timeRange),
		Pagination: buildCursorPagination(opts),
	}
	if streamID != nil {
		req.StreamId = streamID
	}
	return c.aggregated.GetViewerGeoHourly(ctx, req)
}

// GetTenantDailyStats returns daily tenant statistics for PlatformOverview.dailyStats
func (c *GRPCClient) GetTenantDailyStats(ctx context.Context, tenantID string, days int32) (*pb.GetTenantDailyStatsResponse, error) {
	req := &pb.GetTenantDailyStatsRequest{
		TenantId: tenantID,
		Days:     days,
	}
	return c.aggregated.GetTenantDailyStats(ctx, req)
}

// GetProcessingUsage returns transcoding/processing usage records and daily summaries
// Used for billing display and transcoding analytics pages
func (c *GRPCClient) GetProcessingUsage(ctx context.Context, tenantID string, streamID *string, processType *string, timeRange *TimeRangeOpts, opts *CursorPaginationOpts, summaryOnly bool) (*pb.GetProcessingUsageResponse, error) {
	req := &pb.GetProcessingUsageRequest{
		TenantId:    tenantID,
		TimeRange:   buildTimeRange(timeRange),
		Pagination:  buildCursorPagination(opts),
		SummaryOnly: summaryOnly,
	}
	if streamID != nil {
		req.StreamId = streamID
	}
	if processType != nil {
		req.ProcessType = processType
	}
	return c.aggregated.GetProcessingUsage(ctx, req)
}

// GetRebufferingEvents returns buffer state transition events
func (c *GRPCClient) GetRebufferingEvents(ctx context.Context, tenantID string, streamID *string, nodeID *string, timeRange *TimeRangeOpts, opts *CursorPaginationOpts) (*pb.GetRebufferingEventsResponse, error) {
	req := &pb.GetRebufferingEventsRequest{
		TenantId:   tenantID,
		TimeRange:  buildTimeRange(timeRange),
		Pagination: buildCursorPagination(opts),
	}
	if streamID != nil {
		req.StreamId = streamID
	}
	if nodeID != nil {
		req.NodeId = nodeID
	}
	return c.aggregated.GetRebufferingEvents(ctx, req)
}

// GetTenantAnalyticsDaily returns daily tenant-level analytics rollups
func (c *GRPCClient) GetTenantAnalyticsDaily(ctx context.Context, tenantID string, timeRange *TimeRangeOpts, opts *CursorPaginationOpts) (*pb.GetTenantAnalyticsDailyResponse, error) {
	req := &pb.GetTenantAnalyticsDailyRequest{
		TenantId:   tenantID,
		TimeRange:  buildTimeRange(timeRange),
		Pagination: buildCursorPagination(opts),
	}
	return c.aggregated.GetTenantAnalyticsDaily(ctx, req)
}

// GetStreamAnalyticsDaily returns daily stream-level analytics rollups
func (c *GRPCClient) GetStreamAnalyticsDaily(ctx context.Context, tenantID string, streamID *string, timeRange *TimeRangeOpts, opts *CursorPaginationOpts) (*pb.GetStreamAnalyticsDailyResponse, error) {
	req := &pb.GetStreamAnalyticsDailyRequest{
		TenantId:   tenantID,
		TimeRange:  buildTimeRange(timeRange),
		Pagination: buildCursorPagination(opts),
	}
	if streamID != nil {
		req.StreamId = streamID
	}
	return c.aggregated.GetStreamAnalyticsDaily(ctx, req)
}

// GetAPIUsage returns API usage records and daily summaries
func (c *GRPCClient) GetAPIUsage(ctx context.Context, tenantID string, authType *string, operationType *string, operationName *string, timeRange *TimeRangeOpts, opts *CursorPaginationOpts, summaryOnly bool) (*pb.GetAPIUsageResponse, error) {
	req := &pb.GetAPIUsageRequest{
		TenantId:    tenantID,
		TimeRange:   buildTimeRange(timeRange),
		Pagination:  buildCursorPagination(opts),
		SummaryOnly: summaryOnly,
	}
	if authType != nil {
		req.AuthType = authType
	}
	if operationType != nil {
		req.OperationType = operationType
	}
	if operationName != nil {
		req.OperationName = operationName
	}
	return c.aggregated.GetAPIUsage(ctx, req)
}

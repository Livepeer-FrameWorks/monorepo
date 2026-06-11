package periscope

import (
	"context"
	"fmt"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/grpcutil"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/pagination"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"

	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const DefaultServerName = "periscope-query.internal"

// GRPCClient is the gRPC client for Periscope analytics
type GRPCClient struct {
	conn         *grpc.ClientConn
	stream       periscopepb.StreamAnalyticsServiceClient
	viewer       periscopepb.ViewerAnalyticsServiceClient
	track        periscopepb.TrackAnalyticsServiceClient
	connection   periscopepb.ConnectionAnalyticsServiceClient
	node         periscopepb.NodeAnalyticsServiceClient
	routing      periscopepb.RoutingAnalyticsServiceClient
	federation   periscopepb.FederationAnalyticsServiceClient
	platform     periscopepb.PlatformAnalyticsServiceClient
	clip         periscopepb.ClipAnalyticsServiceClient
	aggregated   periscopepb.AggregatedAnalyticsServiceClient
	orchestrator periscopepb.OrchestratorAnalyticsServiceClient
	logger       logging.Logger
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
	ServiceToken  string
	AllowInsecure bool
	CACertFile    string
	ServerName    string
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

		if userID := ctxkeys.GetUserID(ctx); userID != "" {
			md.Set("x-user-id", userID)
		}
		if tenantID := ctxkeys.GetTenantID(ctx); tenantID != "" {
			md.Set("x-tenant-id", tenantID)
		}

		// Use user's JWT from context if available, otherwise fall back to service token
		if jwtToken := ctxkeys.GetJWTToken(ctx); jwtToken != "" {
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

	tlsCfg := grpcutil.ClientTLSConfig{
		CACertFile:        config.CACertFile,
		ServerName:        config.ServerName,
		DefaultServerName: DefaultServerName,
		AllowInsecure:     config.AllowInsecure,
	}
	transport, err := grpcutil.ClientTLS(tlsCfg, config.Logger)
	if err != nil {
		return nil, fmt.Errorf("configure Periscope gRPC TLS: %w", err)
	}

	// Connect to gRPC server with auth interceptor for user context and service token fallback
	conn, err := grpc.NewClient(
		config.GRPCAddr,
		transport,
		grpc.WithDefaultCallOptions(grpc.WaitForReady(true)),
		grpc.WithChainUnaryInterceptor(
			authInterceptor(config.ServiceToken),
			clients.FailsafeUnaryInterceptor("periscope", config.Logger),
		),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                5 * time.Minute,  // Ping interval (must be >= server MinTime, default 5m)
			Timeout:             10 * time.Second, // Wait for ping ack before closing
			PermitWithoutStream: false,            // Only keepalive when active RPCs exist
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Periscope gRPC: %w", err)
	}

	return &GRPCClient{
		conn:         conn,
		stream:       periscopepb.NewStreamAnalyticsServiceClient(conn),
		viewer:       periscopepb.NewViewerAnalyticsServiceClient(conn),
		track:        periscopepb.NewTrackAnalyticsServiceClient(conn),
		connection:   periscopepb.NewConnectionAnalyticsServiceClient(conn),
		node:         periscopepb.NewNodeAnalyticsServiceClient(conn),
		routing:      periscopepb.NewRoutingAnalyticsServiceClient(conn),
		federation:   periscopepb.NewFederationAnalyticsServiceClient(conn),
		platform:     periscopepb.NewPlatformAnalyticsServiceClient(conn),
		clip:         periscopepb.NewClipAnalyticsServiceClient(conn),
		aggregated:   periscopepb.NewAggregatedAnalyticsServiceClient(conn),
		orchestrator: periscopepb.NewOrchestratorAnalyticsServiceClient(conn),
		logger:       config.Logger,
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
func buildTimeRange(opts *TimeRangeOpts) *commonpb.TimeRange {
	if opts == nil {
		return nil
	}
	return &commonpb.TimeRange{
		Start: timestamppb.New(opts.StartTime),
		End:   timestamppb.New(opts.EndTime),
	}
}

// buildCursorPagination creates a proto CursorPaginationRequest from options
func buildCursorPagination(opts *CursorPaginationOpts) *commonpb.CursorPaginationRequest {
	if opts == nil {
		return &commonpb.CursorPaginationRequest{
			First: int32(pagination.DefaultLimit),
		}
	}
	req := &commonpb.CursorPaginationRequest{
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

func requireTenantID(tenantID string) error {
	if tenantID == "" {
		return fmt.Errorf("tenantID required")
	}
	return nil
}

// ============================================================================
// Stream Analytics (Summary + Events)
// ============================================================================

// GetStreamAnalyticsSummary returns MV-backed range aggregates for a stream.
func (c *GRPCClient) GetStreamAnalyticsSummary(ctx context.Context, tenantID string, streamID string, timeRange *TimeRangeOpts) (*periscopepb.GetStreamAnalyticsSummaryResponse, error) {
	if err := requireTenantID(tenantID); err != nil {
		return nil, err
	}
	req := &periscopepb.GetStreamAnalyticsSummaryRequest{
		TenantId:  tenantID,
		StreamId:  streamID,
		TimeRange: buildTimeRange(timeRange),
	}
	return c.aggregated.GetStreamAnalyticsSummary(ctx, req)
}

// GetLiveUsageSummary returns near-real-time usage summary for billing dashboards.
func (c *GRPCClient) GetLiveUsageSummary(ctx context.Context, tenantID string, timeRange *TimeRangeOpts) (*periscopepb.GetLiveUsageSummaryResponse, error) {
	if err := requireTenantID(tenantID); err != nil {
		return nil, err
	}
	req := &periscopepb.GetLiveUsageSummaryRequest{
		TenantId:  tenantID,
		TimeRange: buildTimeRange(timeRange),
	}
	return c.aggregated.GetLiveUsageSummary(ctx, req)
}

// GetStreamEvents returns events for a specific stream with cursor pagination
func (c *GRPCClient) GetStreamEvents(ctx context.Context, tenantID string, streamID string, timeRange *TimeRangeOpts, opts *CursorPaginationOpts) (*periscopepb.GetStreamEventsResponse, error) {
	if err := requireTenantID(tenantID); err != nil {
		return nil, err
	}
	return c.stream.GetStreamEvents(ctx, &periscopepb.GetStreamEventsRequest{
		TenantId:   tenantID,
		StreamId:   streamID,
		TimeRange:  buildTimeRange(timeRange),
		Pagination: buildCursorPagination(opts),
	})
}

// GetBufferEvents returns buffer events for a specific stream
func (c *GRPCClient) GetBufferEvents(ctx context.Context, tenantID string, streamID string, timeRange *TimeRangeOpts, opts *CursorPaginationOpts) (*periscopepb.GetBufferEventsResponse, error) {
	if err := requireTenantID(tenantID); err != nil {
		return nil, err
	}
	return c.stream.GetBufferEvents(ctx, &periscopepb.GetBufferEventsRequest{
		TenantId:   tenantID,
		StreamId:   streamID,
		TimeRange:  buildTimeRange(timeRange),
		Pagination: buildCursorPagination(opts),
	})
}

// GetStreamHealthMetrics returns stream health metrics
func (c *GRPCClient) GetStreamHealthMetrics(ctx context.Context, tenantID string, streamID *string, timeRange *TimeRangeOpts, opts *CursorPaginationOpts) (*periscopepb.GetStreamHealthMetricsResponse, error) {
	if err := requireTenantID(tenantID); err != nil {
		return nil, err
	}
	req := &periscopepb.GetStreamHealthMetricsRequest{
		TenantId:   tenantID,
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
func (c *GRPCClient) GetStreamStatus(ctx context.Context, tenantID string, streamID string) (*periscopepb.StreamStatusResponse, error) {
	if err := requireTenantID(tenantID); err != nil {
		return nil, err
	}
	return c.stream.GetStreamStatus(ctx, &periscopepb.GetStreamStatusRequest{
		TenantId: tenantID,
		StreamId: streamID,
	})
}

// GetStreamsStatus returns operational state for multiple streams (batch lookup)
// Use this to avoid N+1 queries when listing streams
func (c *GRPCClient) GetStreamsStatus(ctx context.Context, tenantID string, streamIDs []string) (*periscopepb.StreamsStatusResponse, error) {
	if err := requireTenantID(tenantID); err != nil {
		return nil, err
	}
	return c.stream.GetStreamsStatus(ctx, &periscopepb.GetStreamsStatusRequest{
		TenantId:  tenantID,
		StreamIds: streamIDs,
	})
}

// ============================================================================
// Viewer Analytics
// ============================================================================

// GetViewerMetrics returns viewer session metrics
func (c *GRPCClient) GetViewerMetrics(ctx context.Context, tenantID string, streamID *string, timeRange *TimeRangeOpts, opts *CursorPaginationOpts) (*periscopepb.GetViewerMetricsResponse, error) {
	if err := requireTenantID(tenantID); err != nil {
		return nil, err
	}
	req := &periscopepb.GetViewerMetricsRequest{
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
func (c *GRPCClient) GetViewerCountTimeSeries(ctx context.Context, tenantID string, streamID *string, timeRange *TimeRangeOpts, interval string) (*periscopepb.GetViewerCountTimeSeriesResponse, error) {
	if err := requireTenantID(tenantID); err != nil {
		return nil, err
	}
	req := &periscopepb.GetViewerCountTimeSeriesRequest{
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
func (c *GRPCClient) GetGeographicDistribution(ctx context.Context, tenantID string, streamID *string, timeRange *TimeRangeOpts, topN int32) (*periscopepb.GetGeographicDistributionResponse, error) {
	if err := requireTenantID(tenantID); err != nil {
		return nil, err
	}
	req := &periscopepb.GetGeographicDistributionRequest{
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
func (c *GRPCClient) GetTrackListEvents(ctx context.Context, tenantID string, streamID string, timeRange *TimeRangeOpts, opts *CursorPaginationOpts) (*periscopepb.GetTrackListEventsResponse, error) {
	if err := requireTenantID(tenantID); err != nil {
		return nil, err
	}
	return c.track.GetTrackListEvents(ctx, &periscopepb.GetTrackListEventsRequest{
		TenantId:   tenantID,
		StreamId:   streamID,
		TimeRange:  buildTimeRange(timeRange),
		Pagination: buildCursorPagination(opts),
	})
}

// ============================================================================
// Connection Analytics
// ============================================================================

// GetConnectionEvents returns connection events
func (c *GRPCClient) GetConnectionEvents(ctx context.Context, tenantID string, streamID *string, timeRange *TimeRangeOpts, opts *CursorPaginationOpts) (*periscopepb.GetConnectionEventsResponse, error) {
	if err := requireTenantID(tenantID); err != nil {
		return nil, err
	}
	req := &periscopepb.GetConnectionEventsRequest{
		TenantId:   tenantID,
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
func (c *GRPCClient) GetNodeMetrics(ctx context.Context, tenantID string, nodeID *string, timeRange *TimeRangeOpts, opts *CursorPaginationOpts) (*periscopepb.GetNodeMetricsResponse, error) {
	if err := requireTenantID(tenantID); err != nil {
		return nil, err
	}
	req := &periscopepb.GetNodeMetricsRequest{
		TenantId:   tenantID,
		TimeRange:  buildTimeRange(timeRange),
		Pagination: buildCursorPagination(opts),
	}
	if nodeID != nil {
		req.NodeId = nodeID
	}
	return c.node.GetNodeMetrics(ctx, req)
}

// GetNodeMetrics1H returns hourly aggregated node metrics
func (c *GRPCClient) GetNodeMetrics1H(ctx context.Context, tenantID string, nodeID *string, timeRange *TimeRangeOpts, opts *CursorPaginationOpts) (*periscopepb.GetNodeMetrics1HResponse, error) {
	if err := requireTenantID(tenantID); err != nil {
		return nil, err
	}
	req := &periscopepb.GetNodeMetrics1HRequest{
		TenantId:   tenantID,
		TimeRange:  buildTimeRange(timeRange),
		Pagination: buildCursorPagination(opts),
	}
	if nodeID != nil {
		req.NodeId = nodeID
	}
	return c.node.GetNodeMetrics1H(ctx, req)
}

// GetNodeMetricsAggregated returns per-node aggregates for the requested time range.
func (c *GRPCClient) GetNodeMetricsAggregated(ctx context.Context, tenantID string, nodeID *string, timeRange *TimeRangeOpts) (*periscopepb.GetNodeMetricsAggregatedResponse, error) {
	if err := requireTenantID(tenantID); err != nil {
		return nil, err
	}
	req := &periscopepb.GetNodeMetricsAggregatedRequest{
		TenantId:  tenantID,
		TimeRange: buildTimeRange(timeRange),
	}
	if nodeID != nil {
		req.NodeId = nodeID
	}
	return c.node.GetNodeMetricsAggregated(ctx, req)
}

// GetLiveNodes returns current state of nodes from live_nodes (ReplacingMergeTree)
// Supports multi-tenant access for subscribed clusters via relatedTenantIDs
func (c *GRPCClient) GetLiveNodes(ctx context.Context, tenantID string, nodeID *string, relatedTenantIDs []string) (*periscopepb.GetLiveNodesResponse, error) {
	if err := requireTenantID(tenantID); err != nil {
		return nil, err
	}
	req := &periscopepb.GetLiveNodesRequest{
		TenantId:         tenantID,
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
func (c *GRPCClient) GetRoutingEvents(ctx context.Context, tenantID string, streamID *string, timeRange *TimeRangeOpts, opts *CursorPaginationOpts, relatedTenantIDs []string, subjectTenantID, clusterID *string) (*periscopepb.GetRoutingEventsResponse, error) {
	if err := requireTenantID(tenantID); err != nil {
		return nil, err
	}
	req := &periscopepb.GetRoutingEventsRequest{
		TenantId:         tenantID,
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
func (c *GRPCClient) GetPlatformOverview(ctx context.Context, tenantID string, timeRange *TimeRangeOpts) (*periscopepb.GetPlatformOverviewResponse, error) {
	if err := requireTenantID(tenantID); err != nil {
		return nil, err
	}
	return c.platform.GetPlatformOverview(ctx, &periscopepb.GetPlatformOverviewRequest{
		TenantId:  tenantID,
		TimeRange: buildTimeRange(timeRange),
	})
}

// ListTenantActivity returns the cross-tenant activity rollup for the
// platform-operator god view. No tenant scope; the server only answers
// service-credential calls. tenantIDs restricts the rollup to specific
// tenants — required when reading one tenant, because the unfiltered list
// is ranked and truncated by limit.
func (c *GRPCClient) ListTenantActivity(ctx context.Context, timeRange *TimeRangeOpts, tenantIDs []string, limit int32) (*periscopepb.ListTenantActivityResponse, error) {
	return c.platform.ListTenantActivity(ctx, &periscopepb.ListTenantActivityRequest{
		TimeRange: buildTimeRange(timeRange),
		TenantIds: tenantIDs,
		Limit:     limit,
	})
}

// GetNetworkLiveStats returns platform-wide per-cluster live stats (no tenant filter).
func (c *GRPCClient) GetNetworkLiveStats(ctx context.Context) (*periscopepb.GetNetworkLiveStatsResponse, error) {
	return c.platform.GetNetworkLiveStats(ctx, &periscopepb.GetNetworkLiveStatsRequest{})
}

// ============================================================================
// Clip Analytics
// ============================================================================

// GetClipEvents returns artifact lifecycle events (clip/dvr/vod)
func (c *GRPCClient) GetClipEvents(ctx context.Context, tenantID string, streamID *string, stage *string, contentType *string, timeRange *TimeRangeOpts, opts *CursorPaginationOpts) (*periscopepb.GetClipEventsResponse, error) {
	if err := requireTenantID(tenantID); err != nil {
		return nil, err
	}
	req := &periscopepb.GetClipEventsRequest{
		TenantId:   tenantID,
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
func (c *GRPCClient) GetArtifactState(ctx context.Context, tenantID string, requestID string) (*periscopepb.GetArtifactStateResponse, error) {
	if err := requireTenantID(tenantID); err != nil {
		return nil, err
	}
	return c.clip.GetArtifactState(ctx, &periscopepb.GetArtifactStateRequest{
		TenantId:  tenantID,
		RequestId: requestID,
	})
}

// GetArtifactStates returns a list of artifact states with optional filtering
func (c *GRPCClient) GetArtifactStates(ctx context.Context, tenantID string, streamID *string, contentType *string, stage *string, opts *CursorPaginationOpts) (*periscopepb.GetArtifactStatesResponse, error) {
	if err := requireTenantID(tenantID); err != nil {
		return nil, err
	}
	req := &periscopepb.GetArtifactStatesRequest{
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
func (c *GRPCClient) GetArtifactStatesByIDs(ctx context.Context, tenantID string, requestIDs []string, contentType *string) (*periscopepb.GetArtifactStatesResponse, error) {
	if err := requireTenantID(tenantID); err != nil {
		return nil, err
	}
	req := &periscopepb.GetArtifactStatesRequest{
		TenantId:   tenantID,
		RequestIds: requestIDs,
		Pagination: &commonpb.CursorPaginationRequest{
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
func (c *GRPCClient) GetStreamConnectionHourly(ctx context.Context, tenantID string, streamID *string, timeRange *TimeRangeOpts, opts *CursorPaginationOpts) (*periscopepb.GetStreamConnectionHourlyResponse, error) {
	if err := requireTenantID(tenantID); err != nil {
		return nil, err
	}
	req := &periscopepb.GetStreamConnectionHourlyRequest{
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
func (c *GRPCClient) GetClientMetrics5m(ctx context.Context, tenantID string, streamID *string, nodeID *string, timeRange *TimeRangeOpts, opts *CursorPaginationOpts) (*periscopepb.GetClientMetrics5MResponse, error) {
	if err := requireTenantID(tenantID); err != nil {
		return nil, err
	}
	req := &periscopepb.GetClientMetrics5MRequest{
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
func (c *GRPCClient) GetQualityTierDaily(ctx context.Context, tenantID string, streamID *string, timeRange *TimeRangeOpts, opts *CursorPaginationOpts) (*periscopepb.GetQualityTierDailyResponse, error) {
	if err := requireTenantID(tenantID); err != nil {
		return nil, err
	}
	req := &periscopepb.GetQualityTierDailyRequest{
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
func (c *GRPCClient) GetStorageUsage(ctx context.Context, tenantID string, nodeID *string, storageScope *string, timeRange *TimeRangeOpts, opts *CursorPaginationOpts) (*periscopepb.GetStorageUsageResponse, error) {
	if err := requireTenantID(tenantID); err != nil {
		return nil, err
	}
	req := &periscopepb.GetStorageUsageRequest{
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

// GetStorageEvents returns storage lifecycle events (freeze + read-through cache fill operations)
func (c *GRPCClient) GetStorageEvents(ctx context.Context, tenantID string, streamID *string, assetType *string, timeRange *TimeRangeOpts, opts *CursorPaginationOpts) (*periscopepb.GetStorageEventsResponse, error) {
	if err := requireTenantID(tenantID); err != nil {
		return nil, err
	}
	req := &periscopepb.GetStorageEventsRequest{
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
func (c *GRPCClient) GetStreamHealth5m(ctx context.Context, tenantID string, streamID string, timeRange *TimeRangeOpts, opts *CursorPaginationOpts) (*periscopepb.GetStreamHealth5MResponse, error) {
	if err := requireTenantID(tenantID); err != nil {
		return nil, err
	}
	req := &periscopepb.GetStreamHealth5MRequest{
		TenantId:   tenantID,
		StreamId:   streamID,
		TimeRange:  buildTimeRange(timeRange),
		Pagination: buildCursorPagination(opts),
	}
	return c.aggregated.GetStreamHealth5M(ctx, req)
}

// GetNodePerformance5m returns 5-minute aggregated node performance metrics
func (c *GRPCClient) GetNodePerformance5m(ctx context.Context, tenantID string, nodeID *string, timeRange *TimeRangeOpts, opts *CursorPaginationOpts) (*periscopepb.GetNodePerformance5MResponse, error) {
	if err := requireTenantID(tenantID); err != nil {
		return nil, err
	}
	req := &periscopepb.GetNodePerformance5MRequest{
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
func (c *GRPCClient) GetViewerHoursHourly(ctx context.Context, tenantID string, streamID *string, timeRange *TimeRangeOpts, opts *CursorPaginationOpts) (*periscopepb.GetViewerHoursHourlyResponse, error) {
	if err := requireTenantID(tenantID); err != nil {
		return nil, err
	}
	req := &periscopepb.GetViewerHoursHourlyRequest{
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
func (c *GRPCClient) GetViewerGeoHourly(ctx context.Context, tenantID string, streamID *string, timeRange *TimeRangeOpts, opts *CursorPaginationOpts) (*periscopepb.GetViewerGeoHourlyResponse, error) {
	if err := requireTenantID(tenantID); err != nil {
		return nil, err
	}
	req := &periscopepb.GetViewerGeoHourlyRequest{
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
func (c *GRPCClient) GetTenantDailyStats(ctx context.Context, tenantID string, days int32) (*periscopepb.GetTenantDailyStatsResponse, error) {
	if err := requireTenantID(tenantID); err != nil {
		return nil, err
	}
	req := &periscopepb.GetTenantDailyStatsRequest{
		TenantId: tenantID,
		Days:     days,
	}
	return c.aggregated.GetTenantDailyStats(ctx, req)
}

// GetProcessingUsage returns transcoding/processing usage records and daily summaries
// Used for billing display and transcoding analytics pages
func (c *GRPCClient) GetProcessingUsage(ctx context.Context, tenantID string, streamID *string, processType *string, timeRange *TimeRangeOpts, opts *CursorPaginationOpts, summaryOnly bool) (*periscopepb.GetProcessingUsageResponse, error) {
	if err := requireTenantID(tenantID); err != nil {
		return nil, err
	}
	req := &periscopepb.GetProcessingUsageRequest{
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
func (c *GRPCClient) GetRebufferingEvents(ctx context.Context, tenantID string, streamID *string, nodeID *string, timeRange *TimeRangeOpts, opts *CursorPaginationOpts) (*periscopepb.GetRebufferingEventsResponse, error) {
	if err := requireTenantID(tenantID); err != nil {
		return nil, err
	}
	req := &periscopepb.GetRebufferingEventsRequest{
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
func (c *GRPCClient) GetTenantAnalyticsDaily(ctx context.Context, tenantID string, timeRange *TimeRangeOpts, opts *CursorPaginationOpts) (*periscopepb.GetTenantAnalyticsDailyResponse, error) {
	if err := requireTenantID(tenantID); err != nil {
		return nil, err
	}
	req := &periscopepb.GetTenantAnalyticsDailyRequest{
		TenantId:   tenantID,
		TimeRange:  buildTimeRange(timeRange),
		Pagination: buildCursorPagination(opts),
	}
	return c.aggregated.GetTenantAnalyticsDaily(ctx, req)
}

// GetStreamAnalyticsDaily returns daily stream-level analytics rollups
func (c *GRPCClient) GetStreamAnalyticsDaily(ctx context.Context, tenantID string, streamID *string, timeRange *TimeRangeOpts, opts *CursorPaginationOpts) (*periscopepb.GetStreamAnalyticsDailyResponse, error) {
	if err := requireTenantID(tenantID); err != nil {
		return nil, err
	}
	req := &periscopepb.GetStreamAnalyticsDailyRequest{
		TenantId:   tenantID,
		TimeRange:  buildTimeRange(timeRange),
		Pagination: buildCursorPagination(opts),
	}
	if streamID != nil {
		req.StreamId = streamID
	}
	return c.aggregated.GetStreamAnalyticsDaily(ctx, req)
}

// StreamSummarySortField represents the field to sort stream summaries by
type StreamSummarySortField string

const (
	StreamSummarySortFieldEgressGB      StreamSummarySortField = "EGRESS_GB"
	StreamSummarySortFieldUniqueViewers StreamSummarySortField = "UNIQUE_VIEWERS"
	StreamSummarySortFieldTotalViews    StreamSummarySortField = "TOTAL_VIEWS"
	StreamSummarySortFieldViewerHours   StreamSummarySortField = "VIEWER_HOURS"
)

// SortOrder represents ascending or descending sort
type SortOrder string

const (
	SortOrderAsc  SortOrder = "ASC"
	SortOrderDesc SortOrder = "DESC"
)

// GetStreamAnalyticsSummaries returns bulk stream summaries with server-side aggregation
func (c *GRPCClient) GetStreamAnalyticsSummaries(ctx context.Context, tenantID string, timeRange *TimeRangeOpts, sortBy StreamSummarySortField, sortOrder SortOrder, opts *CursorPaginationOpts) (*periscopepb.GetStreamAnalyticsSummariesResponse, error) {
	// Map sort field to proto enum
	var pbSortBy periscopepb.StreamSummarySortField
	switch sortBy {
	case StreamSummarySortFieldUniqueViewers:
		pbSortBy = periscopepb.StreamSummarySortField_STREAM_SUMMARY_SORT_FIELD_UNIQUE_VIEWERS
	case StreamSummarySortFieldTotalViews:
		pbSortBy = periscopepb.StreamSummarySortField_STREAM_SUMMARY_SORT_FIELD_TOTAL_VIEWS
	case StreamSummarySortFieldViewerHours:
		pbSortBy = periscopepb.StreamSummarySortField_STREAM_SUMMARY_SORT_FIELD_VIEWER_HOURS
	default:
		pbSortBy = periscopepb.StreamSummarySortField_STREAM_SUMMARY_SORT_FIELD_EGRESS_GB
	}

	// Map sort order to proto enum
	var pbSortOrder commonpb.SortOrder
	if sortOrder == SortOrderAsc {
		pbSortOrder = commonpb.SortOrder_SORT_ORDER_ASC
	} else {
		pbSortOrder = commonpb.SortOrder_SORT_ORDER_DESC
	}

	req := &periscopepb.GetStreamAnalyticsSummariesRequest{
		TenantId:   tenantID,
		TimeRange:  buildTimeRange(timeRange),
		Pagination: buildCursorPagination(opts),
		SortBy:     pbSortBy,
		SortOrder:  pbSortOrder,
	}
	return c.aggregated.GetStreamAnalyticsSummaries(ctx, req)
}

// GetAPIUsage returns API usage records and daily summaries
func (c *GRPCClient) GetAPIUsage(ctx context.Context, tenantID string, authType *string, operationType *string, operationName *string, timeRange *TimeRangeOpts, opts *CursorPaginationOpts, summaryOnly bool) (*periscopepb.GetAPIUsageResponse, error) {
	if err := requireTenantID(tenantID); err != nil {
		return nil, err
	}
	req := &periscopepb.GetAPIUsageRequest{
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

// GetClusterTrafficMatrix returns cross-cluster routing traffic from routing_cluster_hourly MV
func (c *GRPCClient) GetClusterTrafficMatrix(ctx context.Context, tenantID string, timeRange *TimeRangeOpts) (*periscopepb.GetClusterTrafficMatrixResponse, error) {
	if err := requireTenantID(tenantID); err != nil {
		return nil, err
	}
	return c.routing.GetClusterTrafficMatrix(ctx, &periscopepb.GetClusterTrafficMatrixRequest{
		TenantId:  tenantID,
		TimeRange: buildTimeRange(timeRange),
	})
}

// GetFederationEvents returns federation events (origin pulls, peer connections, etc.)
func (c *GRPCClient) GetFederationEvents(ctx context.Context, tenantID string, timeRange *TimeRangeOpts, eventType *string, limit int32) (*periscopepb.GetFederationEventsResponse, error) {
	if err := requireTenantID(tenantID); err != nil {
		return nil, err
	}
	req := &periscopepb.GetFederationEventsRequest{
		TenantId:  tenantID,
		TimeRange: buildTimeRange(timeRange),
		Limit:     limit,
	}
	if eventType != nil {
		req.EventType = eventType
	}
	return c.federation.GetFederationEvents(ctx, req)
}

// GetFederationSummary returns aggregated federation event counts and latencies
func (c *GRPCClient) GetFederationSummary(ctx context.Context, tenantID string, timeRange *TimeRangeOpts) (*periscopepb.GetFederationSummaryResponse, error) {
	if err := requireTenantID(tenantID); err != nil {
		return nil, err
	}
	return c.federation.GetFederationSummary(ctx, &periscopepb.GetFederationSummaryRequest{
		TenantId:  tenantID,
		TimeRange: buildTimeRange(timeRange),
	})
}

// GetRoutingEfficiency returns pre-aggregated routing decision stats
func (c *GRPCClient) GetRoutingEfficiency(ctx context.Context, tenantID string, streamID *string, timeRange *TimeRangeOpts) (*periscopepb.GetRoutingEfficiencyResponse, error) {
	if err := requireTenantID(tenantID); err != nil {
		return nil, err
	}
	req := &periscopepb.GetRoutingEfficiencyRequest{
		TenantId:  tenantID,
		TimeRange: buildTimeRange(timeRange),
	}
	if streamID != nil {
		req.StreamId = streamID
	}
	return c.routing.GetRoutingEfficiency(ctx, req)
}

// GetStreamHealthSummary returns pre-aggregated stream health stats
func (c *GRPCClient) GetStreamHealthSummary(ctx context.Context, tenantID string, streamID *string, timeRange *TimeRangeOpts) (*periscopepb.GetStreamHealthSummaryResponse, error) {
	if err := requireTenantID(tenantID); err != nil {
		return nil, err
	}
	req := &periscopepb.GetStreamHealthSummaryRequest{
		TenantId:  tenantID,
		TimeRange: buildTimeRange(timeRange),
	}
	if streamID != nil {
		req.StreamId = streamID
	}
	return c.aggregated.GetStreamHealthSummary(ctx, req)
}

// GetClientQoeSummary returns pre-aggregated client QoE stats
func (c *GRPCClient) GetClientQoeSummary(ctx context.Context, tenantID string, streamID *string, timeRange *TimeRangeOpts) (*periscopepb.GetClientQoeSummaryResponse, error) {
	if err := requireTenantID(tenantID); err != nil {
		return nil, err
	}
	req := &periscopepb.GetClientQoeSummaryRequest{
		TenantId:  tenantID,
		TimeRange: buildTimeRange(timeRange),
	}
	if streamID != nil {
		req.StreamId = streamID
	}
	return c.aggregated.GetClientQoeSummary(ctx, req)
}

// GetPlayerBootSummary returns the tenant-scoped player startup summary
// (read-time TTF percentiles + span averages over player_boot_samples).
func (c *GRPCClient) GetPlayerBootSummary(ctx context.Context, tenantID string, streamID *string, artifactHash *string, timeRange *TimeRangeOpts) (*periscopepb.GetPlayerBootSummaryResponse, error) {
	if err := requireTenantID(tenantID); err != nil {
		return nil, err
	}
	req := &periscopepb.GetPlayerBootSummaryRequest{
		TenantId:  tenantID,
		TimeRange: buildTimeRange(timeRange),
	}
	if streamID != nil {
		req.StreamId = streamID
	}
	if artifactHash != nil {
		req.ArtifactHash = artifactHash
	}
	return c.aggregated.GetPlayerBootSummary(ctx, req)
}

// GetClusterBootOps returns the redacted operator boot aggregate for the given
// owned clusters (token-attributed rows only).
func (c *GRPCClient) GetClusterBootOps(ctx context.Context, tenantID string, clusterIDs []string, timeRange *TimeRangeOpts) (*periscopepb.GetClusterBootOpsResponse, error) {
	if err := requireTenantID(tenantID); err != nil {
		return nil, err
	}
	req := &periscopepb.GetClusterBootOpsRequest{
		TenantId:   tenantID,
		ClusterIds: clusterIDs,
		TimeRange:  buildTimeRange(timeRange),
	}
	return c.aggregated.GetClusterBootOps(ctx, req)
}

// GetSessionQoeSummary returns the tenant-scoped viewer-experienced QoE summary
// (read-time ratios over client_qoe_session_deltas).
func (c *GRPCClient) GetSessionQoeSummary(ctx context.Context, tenantID string, streamID *string, artifactHash *string, timeRange *TimeRangeOpts) (*periscopepb.GetSessionQoeSummaryResponse, error) {
	if err := requireTenantID(tenantID); err != nil {
		return nil, err
	}
	req := &periscopepb.GetSessionQoeSummaryRequest{
		TenantId:  tenantID,
		TimeRange: buildTimeRange(timeRange),
	}
	if streamID != nil {
		req.StreamId = streamID
	}
	if artifactHash != nil {
		req.ArtifactHash = artifactHash
	}
	return c.aggregated.GetSessionQoeSummary(ctx, req)
}

// GetClusterQoeOps returns the redacted operator QoE aggregate for the given owned
// clusters (token-attributed rows only).
func (c *GRPCClient) GetClusterQoeOps(ctx context.Context, tenantID string, clusterIDs []string, timeRange *TimeRangeOpts) (*periscopepb.GetClusterQoeOpsResponse, error) {
	if err := requireTenantID(tenantID); err != nil {
		return nil, err
	}
	req := &periscopepb.GetClusterQoeOpsRequest{
		TenantId:   tenantID,
		ClusterIds: clusterIDs,
		TimeRange:  buildTimeRange(timeRange),
	}
	return c.aggregated.GetClusterQoeOps(ctx, req)
}

// GetVodRetention returns the per-bucket VOD retention curve for one artifact.
func (c *GRPCClient) GetVodRetention(ctx context.Context, tenantID string, artifactHash string, timeRange *TimeRangeOpts) (*periscopepb.GetVodRetentionResponse, error) {
	if err := requireTenantID(tenantID); err != nil {
		return nil, err
	}
	req := &periscopepb.GetVodRetentionRequest{
		TenantId:     tenantID,
		ArtifactHash: artifactHash,
		TimeRange:    buildTimeRange(timeRange),
	}
	return c.aggregated.GetVodRetention(ctx, req)
}

// GetPlayerBootTimeSeries returns the boot-startup summary bucketed by interval
// ("5m"/"15m"/"1h"/"1d"; defaults to 5m server-side).
func (c *GRPCClient) GetPlayerBootTimeSeries(ctx context.Context, tenantID string, streamID *string, artifactHash *string, timeRange *TimeRangeOpts, interval string) (*periscopepb.GetPlayerBootTimeSeriesResponse, error) {
	if err := requireTenantID(tenantID); err != nil {
		return nil, err
	}
	req := &periscopepb.GetPlayerBootTimeSeriesRequest{
		TenantId:  tenantID,
		TimeRange: buildTimeRange(timeRange),
		Interval:  interval,
	}
	if streamID != nil {
		req.StreamId = streamID
	}
	if artifactHash != nil {
		req.ArtifactHash = artifactHash
	}
	return c.aggregated.GetPlayerBootTimeSeries(ctx, req)
}

// GetSessionQoeTimeSeries returns the viewer-experienced QoE summary bucketed by
// interval ("5m"/"15m"/"1h"/"1d"; defaults to 5m server-side).
func (c *GRPCClient) GetSessionQoeTimeSeries(ctx context.Context, tenantID string, streamID *string, artifactHash *string, timeRange *TimeRangeOpts, interval string) (*periscopepb.GetSessionQoeTimeSeriesResponse, error) {
	if err := requireTenantID(tenantID); err != nil {
		return nil, err
	}
	req := &periscopepb.GetSessionQoeTimeSeriesRequest{
		TenantId:  tenantID,
		TimeRange: buildTimeRange(timeRange),
		Interval:  interval,
	}
	if streamID != nil {
		req.StreamId = streamID
	}
	if artifactHash != nil {
		req.ArtifactHash = artifactHash
	}
	return c.aggregated.GetSessionQoeTimeSeries(ctx, req)
}

// ListVodRetentionAssets lists the tenant's VOD assets that have retention data in
// the window (cursor-paginated). Eligibility is owned by Periscope; the gateway
// composes human title/playback_id from the catalog by artifact_hash.
func (c *GRPCClient) ListVodRetentionAssets(ctx context.Context, tenantID string, timeRange *TimeRangeOpts, opts *CursorPaginationOpts) (*periscopepb.ListVodRetentionAssetsResponse, error) {
	if err := requireTenantID(tenantID); err != nil {
		return nil, err
	}
	req := &periscopepb.ListVodRetentionAssetsRequest{
		TenantId:   tenantID,
		TimeRange:  buildTimeRange(timeRange),
		Pagination: buildCursorPagination(opts),
	}
	return c.aggregated.ListVodRetentionAssets(ctx, req)
}

// ListOrchestrators lists vantage-independent orchestrator state for a tenant.
// orchAddr empty = full list; non-empty = single-row filter.
func (c *GRPCClient) ListOrchestrators(ctx context.Context, tenantID string, orchAddr *string, opts *CursorPaginationOpts) (*periscopepb.ListOrchestratorsResponse, error) {
	if err := requireTenantID(tenantID); err != nil {
		return nil, err
	}
	req := &periscopepb.ListOrchestratorsRequest{
		TenantId:   tenantID,
		Pagination: buildCursorPagination(opts),
	}
	if orchAddr != nil {
		req.OrchAddr = orchAddr
	}
	return c.orchestrator.ListOrchestrators(ctx, req)
}

// GetOrchestrator returns one orchestrator's state plus all per-vantage rows.
func (c *GRPCClient) GetOrchestrator(ctx context.Context, tenantID, orchAddr string) (*periscopepb.GetOrchestratorResponse, error) {
	if err := requireTenantID(tenantID); err != nil {
		return nil, err
	}
	return c.orchestrator.GetOrchestrator(ctx, &periscopepb.GetOrchestratorRequest{
		TenantId: tenantID,
		OrchAddr: orchAddr,
	})
}

// ListOrchestratorInstances returns per-instance rows for the tenant.
// Each instance carries its own price/capabilities/hardware — usually
// consistent within an orch's pool but not guaranteed.
func (c *GRPCClient) ListOrchestratorInstances(ctx context.Context, tenantID string, orchAddr *string) (*periscopepb.ListOrchestratorInstancesResponse, error) {
	if err := requireTenantID(tenantID); err != nil {
		return nil, err
	}
	req := &periscopepb.ListOrchestratorInstancesRequest{TenantId: tenantID}
	if orchAddr != nil {
		req.OrchAddr = orchAddr
	}
	return c.orchestrator.ListOrchestratorInstances(ctx, req)
}

// ListOrchestratorVantages returns every per-vantage row for the tenant
// (optionally filtered to one orch). Federation map calls this without a
// filter to render every observation in one pass.
func (c *GRPCClient) ListOrchestratorVantages(ctx context.Context, tenantID string, orchAddr *string) (*periscopepb.ListOrchestratorVantagesResponse, error) {
	if err := requireTenantID(tenantID); err != nil {
		return nil, err
	}
	req := &periscopepb.ListOrchestratorVantagesRequest{TenantId: tenantID}
	if orchAddr != nil {
		req.OrchAddr = orchAddr
	}
	return c.orchestrator.ListOrchestratorVantages(ctx, req)
}

// GetOrchestratorPerformanceSeries returns discovery and outcome points from
// the 5m or 1h orchestrator rollups. interval defaults to 5m on empty.
func (c *GRPCClient) GetOrchestratorPerformanceSeries(ctx context.Context, tenantID, orchAddr string, timeRange *TimeRangeOpts, interval *string, gatewayID, resolvedIP *string) (*periscopepb.GetOrchestratorPerformanceSeriesResponse, error) {
	if err := requireTenantID(tenantID); err != nil {
		return nil, err
	}
	req := &periscopepb.GetOrchestratorPerformanceSeriesRequest{
		TenantId:  tenantID,
		OrchAddr:  orchAddr,
		TimeRange: buildTimeRange(timeRange),
	}
	if interval != nil {
		req.Interval = interval
	}
	if gatewayID != nil {
		req.GatewayId = gatewayID
	}
	if resolvedIP != nil {
		req.ResolvedIp = resolvedIP
	}
	return c.orchestrator.GetOrchestratorPerformanceSeries(ctx, req)
}

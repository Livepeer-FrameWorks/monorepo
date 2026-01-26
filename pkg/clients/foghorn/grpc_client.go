package foghorn

import (
	"context"
	"fmt"
	"time"

	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

// GRPCClient is the gRPC client for Foghorn control plane services
type GRPCClient struct {
	conn    *grpc.ClientConn
	clip    pb.ClipControlServiceClient
	dvr     pb.DVRControlServiceClient
	viewer  pb.ViewerControlServiceClient
	vod     pb.VodControlServiceClient
	tenant  pb.TenantControlServiceClient
	logger  logging.Logger
	timeout time.Duration
}

// GRPCConfig represents the configuration for the Foghorn gRPC client
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

// NewGRPCClient creates a new gRPC client for Foghorn
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
	)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Foghorn gRPC: %w", err)
	}

	return &GRPCClient{
		conn:    conn,
		clip:    pb.NewClipControlServiceClient(conn),
		dvr:     pb.NewDVRControlServiceClient(conn),
		viewer:  pb.NewViewerControlServiceClient(conn),
		vod:     pb.NewVodControlServiceClient(conn),
		tenant:  pb.NewTenantControlServiceClient(conn),
		logger:  config.Logger,
		timeout: config.Timeout,
	}, nil
}

// Close closes the gRPC connection
func (c *GRPCClient) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// =============================================================================
// CLIP OPERATIONS
// =============================================================================

// CreateClip creates a new clip from a stream
func (c *GRPCClient) CreateClip(ctx context.Context, req *pb.CreateClipRequest) (*pb.CreateClipResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	return c.clip.CreateClip(ctx, req)
}

// DeleteClip deletes a clip
func (c *GRPCClient) DeleteClip(ctx context.Context, clipHash string, tenantID *string) (*pb.DeleteClipResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	req := &pb.DeleteClipRequest{
		ClipHash: clipHash,
	}
	if tenantID != nil {
		req.TenantId = *tenantID
	}
	return c.clip.DeleteClip(ctx, req)
}

// =============================================================================
// DVR OPERATIONS
// =============================================================================

// StartDVR initiates DVR recording for a stream
func (c *GRPCClient) StartDVR(ctx context.Context, req *pb.StartDVRRequest) (*pb.StartDVRResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	return c.dvr.StartDVR(ctx, req)
}

// StopDVR stops an active DVR recording
func (c *GRPCClient) StopDVR(ctx context.Context, dvrHash string, tenantID *string, streamID *string) (*pb.StopDVRResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	req := &pb.StopDVRRequest{
		DvrHash: dvrHash,
	}
	if tenantID != nil {
		req.TenantId = *tenantID
	}
	if streamID != nil && *streamID != "" {
		req.StreamId = streamID
	}
	return c.dvr.StopDVR(ctx, req)
}

// DeleteDVR deletes a DVR recording and its files
func (c *GRPCClient) DeleteDVR(ctx context.Context, dvrHash string, tenantID *string) (*pb.DeleteDVRResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	req := &pb.DeleteDVRRequest{
		DvrHash: dvrHash,
	}
	if tenantID != nil {
		req.TenantId = *tenantID
	}
	return c.dvr.DeleteDVR(ctx, req)
}

// =============================================================================
// VIEWER OPERATIONS
// =============================================================================

// ResolveViewerEndpoint resolves the best endpoint(s) for a viewer
func (c *GRPCClient) ResolveViewerEndpoint(ctx context.Context, contentID string, viewerIP *string) (*pb.ViewerEndpointResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	return c.viewer.ResolveViewerEndpoint(ctx, &pb.ViewerEndpointRequest{
		ContentId:   contentID,
		ViewerIp:    viewerIP,
	})
}

// ResolveIngestEndpoint resolves the best ingest endpoint(s) for StreamCrafter
func (c *GRPCClient) ResolveIngestEndpoint(ctx context.Context, streamKey string, viewerIP *string) (*pb.IngestEndpointResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	return c.viewer.ResolveIngestEndpoint(ctx, &pb.IngestEndpointRequest{
		StreamKey: streamKey,
		ViewerIp:  viewerIP,
	})
}

// =============================================================================
// VOD OPERATIONS
// =============================================================================

// CreateVodUpload initiates a multipart upload and returns presigned URLs
func (c *GRPCClient) CreateVodUpload(ctx context.Context, req *pb.CreateVodUploadRequest) (*pb.CreateVodUploadResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	return c.vod.CreateVodUpload(ctx, req)
}

// CompleteVodUpload finalizes a multipart upload after all parts are uploaded
func (c *GRPCClient) CompleteVodUpload(ctx context.Context, req *pb.CompleteVodUploadRequest) (*pb.CompleteVodUploadResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	return c.vod.CompleteVodUpload(ctx, req)
}

// AbortVodUpload cancels an in-progress multipart upload
func (c *GRPCClient) AbortVodUpload(ctx context.Context, tenantID, uploadID string) (*pb.AbortVodUploadResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	return c.vod.AbortVodUpload(ctx, &pb.AbortVodUploadRequest{
		TenantId: tenantID,
		UploadId: uploadID,
	})
}

// GetVodAsset returns a single VOD asset by hash
func (c *GRPCClient) GetVodAsset(ctx context.Context, tenantID, artifactHash string) (*pb.VodAssetInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	return c.vod.GetVodAsset(ctx, &pb.GetVodAssetRequest{
		TenantId:     tenantID,
		ArtifactHash: artifactHash,
	})
}

// ListVodAssets returns paginated list of VOD assets for a tenant
func (c *GRPCClient) ListVodAssets(ctx context.Context, tenantID string, pagination *pb.CursorPaginationRequest) (*pb.ListVodAssetsResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	return c.vod.ListVodAssets(ctx, &pb.ListVodAssetsRequest{
		TenantId:   tenantID,
		Pagination: pagination,
	})
}

// DeleteVodAsset deletes a VOD asset
func (c *GRPCClient) DeleteVodAsset(ctx context.Context, tenantID, artifactHash string) (*pb.DeleteVodAssetResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	return c.vod.DeleteVodAsset(ctx, &pb.DeleteVodAssetRequest{
		TenantId:     tenantID,
		ArtifactHash: artifactHash,
	})
}

// TerminateTenantStreams stops all active streams for a suspended tenant
func (c *GRPCClient) TerminateTenantStreams(ctx context.Context, tenantID, reason string) (*pb.TerminateTenantStreamsResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	return c.tenant.TerminateTenantStreams(ctx, &pb.TerminateTenantStreamsRequest{
		TenantId: tenantID,
		Reason:   reason,
	})
}

// InvalidateTenantCache clears cached suspension status for a tenant (called on reactivation)
func (c *GRPCClient) InvalidateTenantCache(ctx context.Context, tenantID, reason string) (*pb.InvalidateTenantCacheResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	return c.tenant.InvalidateTenantCache(ctx, &pb.InvalidateTenantCacheRequest{
		TenantId: tenantID,
		Reason:   reason,
	})
}
